package worker

import (
	"context"
	"log/slog"
	"time"
)

// loop — параметры периодической задачи (общий цикл Run всех воркеров пакета, аудит BC-08).
type loop struct {
	// job — имя задачи (атрибут job журнала; по нему же считается worker_errors_total).
	job string
	// failed — текст записи журнала об ошибке тика.
	failed string
	// interval — период.
	interval time.Duration
	// retry — после ошибки тика следующий запуск через min(retry, interval); 0 — через обычный период.
	retry time.Duration
	// fixedRate — тики по сетке time.Ticker (период от начала тика; опоздавшие тики пропускаются), а не «период
	// после окончания тика». Так идут частые задачи: истечение сессий и автобиллинг.
	fixedRate bool
	log       *slog.Logger
}

// run выполняет tick до отмены ctx: первый тик — сразу, затем по периоду. Ошибка тика пишется в журнал и цикл
// не останавливает; отмена ctx — nil. Один исполнитель на кластер — забота самого тика (leader.RunOnce, Q-086).
//
// Паника тика здесь не перехватывается: её ловит safego.Run, которым app.Run оборачивает каждый воркер (журнал
// со стеком и перезапуск всего цикла с растущей задержкой, BC-04) — после перезапуска первый тик снова сразу.
func (l loop) run(ctx context.Context, tick func(ctx context.Context) error) error {
	log := l.log
	if log == nil {
		log = slog.Default()
	}
	once := func() (failed bool) {
		if err := tick(ctx); err != nil && ctx.Err() == nil {
			log.Error(l.failed, "job", l.job, "err", err)
			return true
		}
		return false
	}
	if l.fixedRate {
		t := time.NewTicker(l.interval)
		defer t.Stop()
		for {
			once()
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
			}
		}
	}
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			next := l.interval
			if once() && l.retry > 0 {
				next = min(l.retry, l.interval)
			}
			t.Reset(next)
		}
	}
}

// intervalOr — период задачи: заданный или умолчание.
func intervalOr(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}
