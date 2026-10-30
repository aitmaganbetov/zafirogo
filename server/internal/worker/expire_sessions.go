package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
)

// ExpireSessionsJob — имя задачи и ключ advisory lock (hashtext('job:<name>')).
const ExpireSessionsJob = "club-computers:expire-sessions"

// ExpireSessionsInterval — как Schedule::command('club-computers:expire-sessions')->everyMinute()
// (Laravel: routes/console.php стр. 34).
const ExpireSessionsInterval = time.Minute

// ExpireSessions — фоновая задача истечения сессий ПК (вместо cron Laravel).
//
// Один исполнитель на кластер (docs/parts/50-jobs.md п. 17, Q-086): в начале тика задача берёт
// pg_try_advisory_lock(hashtext('job:club-computers:expire-sessions')) на выделенном соединении пула;
// не взяла (идёт на другом инстансе) — пропускает тик. Лок снимается в конце тика, а при падении
// процесса — вместе с соединением.
//
// Тик — service.ExpireDueSessions: только снять истёкшие сессии, без расчёта (GAP-005, Q-010).
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
	interval := w.Interval
	if interval <= 0 {
		interval = ExpireSessionsInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
			w.log().Error("expire sessions tick failed", "job", ExpireSessionsJob, "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick — один запуск: ran = false, если лок держит другой исполнитель; n — сколько сессий снято.
func (w *ExpireSessions) Tick(ctx context.Context) (ran bool, n int, err error) {
	conn, err := w.Pool.Acquire(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("worker: acquire connection: %w", err)
	}
	defer conn.Release()

	key := "job:" + ExpireSessionsJob
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", key).Scan(&locked); err != nil {
		return false, 0, fmt.Errorf("worker: advisory lock: %w", err)
	}
	if !locked {
		return false, 0, nil
	}
	defer func() {
		// Отдельный контекст: лок нужно снять и при отменённом ctx, иначе соединение вернётся в пул с ним.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var ok bool
		if uerr := conn.QueryRow(unlockCtx, "SELECT pg_advisory_unlock(hashtext($1))", key).Scan(&ok); uerr != nil || !ok {
			// Соединение с невыясненным состоянием лока в пул не возвращаем.
			_ = conn.Conn().Close(unlockCtx)
		}
	}()

	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	// Тик выполняется на том же соединении, что держит лок.
	expired, err := service.ExpireDueSessions(ctx, conn, now())
	if err != nil {
		return true, 0, err
	}
	if len(expired) > 0 {
		w.log().Info("paid sessions expired", "job", ExpireSessionsJob, "count", len(expired))
		if w.OnExpired != nil {
			w.OnExpired(ctx, expired)
		}
	}
	return true, len(expired), nil
}

func (w *ExpireSessions) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
