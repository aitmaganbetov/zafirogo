package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// BurnPackagesJob — имя задачи и ключ advisory lock (hashtext('job:<name>')).
const BurnPackagesJob = "time-packages:burn-expired"

// BurnPackagesInterval — как Schedule::call(...)->daily()->name('session-purchases:prune') (Laravel: routes/console.php).
const BurnPackagesInterval = 24 * time.Hour

// burnPackagesRetry — повтор после ошибки тика.
const burnPackagesRetry = 5 * time.Minute

// BurnPackages — суточное сгорание просроченных пакетов времени во всех клубах.
// Решение Q-011 (docs/DECISIONS.md): пакет удаляется с записью ledger минут (package_expired) и журнала
// (time.package_expired), см. service.BurnExpiredPackages. Один исполнитель на кластер — leader.RunOnce
// (Q-086); работа идемпотентна (каждый пакет — своя транзакция с повторной проверкой срока), поэтому тик
// на нескольких инстансах подряд безопасен.
type BurnPackages struct {
	// Platform — пул zafiro_platform (список пакетов по всем клубам и advisory lock).
	Platform *pgxpool.Pool
	// App — пул zafiro_app (сгорание в транзакции клуба, RLS).
	App db.Beginner
	// Now — часы (nil — time.Now).
	Now func() time.Time
	// Interval — период (0 — BurnPackagesInterval).
	Interval time.Duration
	// Log — журнал (nil — slog.Default()).
	Log *slog.Logger
}

// Run выполняет тики до отмены ctx. Первый тик — сразу (пакеты, просроченные за время простоя, сгорают при старте).
func (w *BurnPackages) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = BurnPackagesInterval
	}
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			next := interval
			if _, _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
				w.log().Error("burn expired packages tick failed", "job", BurnPackagesJob, "err", err)
				next = min(burnPackagesRetry, interval)
			}
			t.Reset(next)
		}
	}
}

// Tick — один запуск: ran = false, если лок держит другой исполнитель; n — сколько пакетов сгорело.
func (w *BurnPackages) Tick(ctx context.Context) (ran bool, n int, err error) {
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	ran, err = leader.RunOnce(ctx, w.Platform, BurnPackagesJob, func(ctx context.Context, _ *pgxpool.Conn) error {
		// Системное действие: журнал требует IP, как у фоновых действий агента (guestapi hooks).
		burned, err := service.BurnExpiredPackages(ctx, w.Platform, w.App, now(), activity.RequestMeta{IP: "127.0.0.1"})
		n = len(burned)
		if n > 0 {
			w.log().Info("expired time packages burned", "job", BurnPackagesJob, "count", n)
		}
		return err
	})
	return ran, n, err
}

func (w *BurnPackages) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
