package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// errDraining — инстанс останавливается: /readyz отвечает 503, балансировщик уводит трафик.
var errDraining = errors.New("shutting down")

func (a *App) checkNotDraining(context.Context) error {
	if a.draining.Load() {
		return errDraining
	}
	return nil
}

func (a *App) pingValkey(ctx context.Context) error {
	return a.Valkey.Do(ctx, a.Valkey.B().Ping().Build()).Error()
}

// pingNATS — круг до сервера (PING/PONG), а не только статус соединения.
func (a *App) pingNATS(ctx context.Context) error {
	if !a.NATS.IsConnected() {
		return fmt.Errorf("nats: %s", a.NATS.Status())
	}
	return a.NATS.FlushWithContext(ctx)
}

// checkRoles — защита от неверных DSN: логин приложения должен состоять в zafiro_app, платформенный —
// в zafiro_platform, и ни один не должен обходить RLS (суперпользователь или BYPASSRLS). Иначе
// второй рубеж тенантности молча не работает (db/migrations/0004_rls.sql).
func checkRoles(ctx context.Context, appPool, platformPool *pgxpool.Pool) error {
	check := func(pool *pgxpool.Pool, key, role string) error {
		var member, bypass bool
		err := pool.QueryRow(ctx, `
			SELECT pg_has_role(current_user, $1, 'MEMBER'),
			       r.rolsuper OR r.rolbypassrls
			FROM pg_roles r WHERE r.rolname = current_user`, role).Scan(&member, &bypass)
		if err != nil {
			return fmt.Errorf("%s: проверка роли: %w (миграции применены?)", key, err)
		}
		if bypass {
			return fmt.Errorf("%s: логин обходит RLS (суперпользователь или BYPASSRLS) — нужен логин-член роли %s", key, role)
		}
		if !member {
			return fmt.Errorf("%s: логин не состоит в роли %s", key, role)
		}
		return nil
	}
	if err := check(appPool, "database.url", "zafiro_app"); err != nil {
		return err
	}
	return check(platformPool, "database.platform_url", "zafiro_platform")
}

// partitionsWorker — месячные партиции журналов (db.EnsurePartitions, Q-081): при старте и раз в interval,
// один исполнитель на кластер (leader.RunOnce). Пул — zafiro_platform (EXECUTE на
// zafiro_create_monthly_partitions есть только у этой роли). После ошибки — повтор через минуту.
type partitionsWorker struct {
	pool     *pgxpool.Pool
	interval time.Duration
	months   int
	log      *slog.Logger
}

const (
	partitionsJob   = "db:ensure-partitions"
	partitionsRetry = time.Minute
)

func (w *partitionsWorker) tick(ctx context.Context) bool {
	_, err := leader.RunOnce(ctx, w.pool, partitionsJob, func(ctx context.Context, conn *pgxpool.Conn) error {
		n, err := db.EnsurePartitions(ctx, conn, time.Now().UTC(), w.months)
		if err == nil && n > 0 {
			w.log.InfoContext(ctx, "partitions created", "count", n)
		}
		return err
	})
	if err != nil && ctx.Err() == nil {
		w.log.WarnContext(ctx, "ensure partitions failed", "err", err)
		return false
	}
	return true
}

// Run — до отмены ctx. Ошибка тика не останавливает воркер.
func (w *partitionsWorker) Run(ctx context.Context) error {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			next := w.interval
			if !w.tick(ctx) {
				next = min(partitionsRetry, w.interval)
			}
			t.Reset(next)
		}
	}
}
