package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// AutoBillingJob — имя задачи и ключ advisory lock (hashtext('job:<name>')).
const AutoBillingJob = "agent:auto-billing"

// AutoBillingInterval — период тика: старый агент проверял автобиллинг раз в секунду
// (WindowsAgent/Frontend/main.js setInterval(autoBillingTick, 1000)).
const AutoBillingInterval = time.Second

// AutoBillingTicker — один проход автобиллинга (guestapi.Server.TickAutoBilling).
type AutoBillingTicker interface {
	TickAutoBilling(ctx context.Context) (int, error)
}

// AutoBilling — серверный автобиллинг гостей на ПК (решение Q-008, docs/open-questions/stage5-agent.md AG-12):
// когда кончается пауза на выбор тарифа или время тарифа, сервер сам покупает минуты на весь основной баланс.
// Один исполнитель на кластер — advisory lock на тик (leader.RunOnce, Q-086); покупка идемпотентна по ключу ПК + вход + шаг, поэтому
// короткое раздвоение лидерства при обрыве связи не спишет дважды.
type AutoBilling struct {
	// Platform — пул zafiro_platform (advisory lock).
	Platform *pgxpool.Pool
	// Engine — решение и покупка.
	Engine AutoBillingTicker
	// Interval — период (0 — AutoBillingInterval).
	Interval time.Duration
	// Log — журнал (nil — slog.Default()).
	Log *slog.Logger
}

// Run выполняет тики до отмены ctx. Каждый тик — под локом задачи (leader.RunOnce): соединение пула zafiro_platform
// берётся только на время тика, а не держится постоянно (пул платформы маленький: проверка готовности, воркеры).
func (w *AutoBilling) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = AutoBillingInterval
	}
	log := w.Log
	if log == nil {
		log = slog.Default()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Error("auto billing tick failed", "job", AutoBillingJob, "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick — один проход под локом; ran = false — тик выполняет другой инстанс.
func (w *AutoBilling) Tick(ctx context.Context) (ran bool, err error) {
	return leader.RunOnce(ctx, w.Platform, AutoBillingJob, func(ctx context.Context, _ *pgxpool.Conn) error {
		_, err := w.Engine.TickAutoBilling(ctx)
		return err
	})
}
