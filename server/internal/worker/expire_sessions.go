package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// ExpireSessionsJob — имя задачи и ключ advisory lock (hashtext('job:<name>')).
const ExpireSessionsJob = "club-computers:expire-sessions"

// ExpireSessionsInterval — как Schedule::command('club-computers:expire-sessions')->everyMinute()
// (Laravel: routes/console.php стр. 34).
const ExpireSessionsInterval = time.Minute

// ExpireSessions — фоновая задача истечения сессий ПК (вместо cron Laravel).
//
// Один исполнитель на кластер (docs/parts/50-jobs.md п. 17, Q-086): тик идёт под leader.RunOnce —
// pg_try_advisory_lock(hashtext('job:club-computers:expire-sessions')) на выделенном соединении пула;
// не взяла (идёт на другом инстансе) — пропускает тик. Лок снимается в конце тика, а при падении
// процесса — вместе с соединением.
//
// Тик — service.ExpireDueSessions: снять истёкшие сессии без расчёта (GAP-005) и записать журнал
// computer.session_expired в той же транзакции (Решение Q-010 (docs/DECISIONS.md)).
type ExpireSessions struct {
	// Pool — пул логина zafiro_platform (задача обходит все клубы).
	Pool *pgxpool.Pool
	// Now — часы (nil — time.Now).
	Now func() time.Time
	// Interval — период (0 — ExpireSessionsInterval).
	Interval time.Duration
	// Log — журнал (nil — slog.Default()).
	Log *slog.Logger
	// OnExpired — необязательный обработчик снятых сессий (публикация события в NATS → админка, агент).
	OnExpired func(context.Context, []service.ExpiredSession)
}

// Run выполняет тики до отмены ctx. Первый тик — сразу.
func (w *ExpireSessions) Run(ctx context.Context) error {
	return loop{
		job: ExpireSessionsJob, failed: "expire sessions tick failed", log: w.Log,
		interval: intervalOr(w.Interval, ExpireSessionsInterval), fixedRate: true,
	}.run(ctx, func(ctx context.Context) error { _, _, err := w.Tick(ctx); return err })
}

// Tick — один запуск: ran = false, если лок держит другой исполнитель; n — сколько сессий снято.
func (w *ExpireSessions) Tick(ctx context.Context) (ran bool, n int, err error) {
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	ran, err = leader.RunOnce(ctx, w.Pool, ExpireSessionsJob, func(ctx context.Context, conn *pgxpool.Conn) error {
		// Тик выполняется на том же соединении, что держит лок.
		// Системное действие: журнал computer.session_expired (Решение Q-010) требует IP.
		expired, err := service.ExpireDueSessions(ctx, conn, now(), activity.RequestMeta{IP: "127.0.0.1"})
		if err != nil {
			return err
		}
		n = len(expired)
		if n > 0 {
			w.log().Info("paid sessions expired", "job", ExpireSessionsJob, "count", n)
			if w.OnExpired != nil {
				w.OnExpired(ctx, expired)
			}
		}
		return nil
	})
	return ran, n, err
}

func (w *ExpireSessions) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
