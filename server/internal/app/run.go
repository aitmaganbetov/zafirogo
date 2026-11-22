package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/aitmaganbetov/zafirogo/server/internal/observability"
	"github.com/aitmaganbetov/zafirogo/server/internal/safego"
)

// errShutdownTimeout — бюджет остановки исчерпан: часть воркеров не завершилась.
var errShutdownTimeout = errors.New("app: shutdown timeout exceeded")

// Run запускает HTTP, gRPC и воркеры и блокируется до отмены ctx (сигнал) или до ошибки любой подсистемы;
// затем выполняет graceful shutdown (см. описание пакета). Возвращает первую ошибку подсистемы
// (nil при штатной остановке по ctx). Повторный вызов — ошибка.
func (a *App) Run(ctx context.Context) error {
	started := false
	a.runOnce.Do(func() { started = true })
	if !started {
		return errors.New("app: Run called twice")
	}

	g, gctx := errgroup.WithContext(context.Background())
	// Воркеры живут дольше серверов: их отменяем после остановки HTTP/gRPC.
	wctx, cancelWorkers := context.WithCancel(a.baseCtx)
	defer cancelWorkers()

	for _, w := range a.workers {
		g.Go(func() error {
			a.log.Debug("worker started", "worker", w.name)
			// Паника задачи (например, nil в данных одного клуба) не роняет инстанс для всех клубов: запись в журнал
			// со стеком (worker_errors_total) и перезапуск задачи с растущей задержкой (BC-04).
			err := safego.Run(wctx, a.log, w.name, safego.Backoff{}, w.run)
			if err != nil && wctx.Err() == nil {
				a.log.Error("worker failed", "worker", w.name, "err", err)
				return fmt.Errorf("%s: %w", w.name, err)
			}
			a.log.Debug("worker stopped", "worker", w.name)
			return nil
		})
	}
	g.Go(func() error {
		a.log.Info("http listening", "addr", a.HTTPAddr())
		if err := a.httpSrv.Serve(a.httpLis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", err)
		}
		return nil
	})
	if a.metricsSrv != nil {
		// Внутренний порт /metrics (Этап 6 O3): закрывается после воркеров — метрики видны во время остановки.
		g.Go(func() error {
			a.log.Info("metrics listening", "addr", a.metricsLis.Addr().String())
			if err := observability.Serve(a.metricsSrv, a.metricsLis); err != nil {
				return fmt.Errorf("metrics: %w", err)
			}
			return nil
		})
	}
	if a.grpcSrv != nil {
		g.Go(func() error {
			a.log.Info("grpc listening", "addr", a.GRPCAddr(), "instance", a.cfg.Agent.InstanceID)
			// При остановке listener закрывается раньше GracefulStop (shutdownServers) — Serve возвращает ошибку Accept.
			if err := a.grpcSrv.Serve(a.grpcLis); err != nil && !a.draining.Load() {
				return fmt.Errorf("grpc: %w", err)
			}
			return nil
		})
	}

	var cause error
	select {
	case <-ctx.Done():
	case <-gctx.Done():
		cause = errors.New("subsystem failed")
	}
	a.log.Info("shutting down", "timeout", a.cfg.ShutdownTimeout, "cause", errString(cause))
	start := time.Now()
	deadline := start.Add(a.cfg.ShutdownTimeout)
	// Серверам (HTTP Shutdown, Drain агентов, GracefulStop) — не больше ⅔ бюджета, затем Close/Stop: медленный клиент
	// HTTP не должен съесть время воркеров и закрытия пулов (аудит OPS-16 — раньше выход с кодом 1 и сообщением
	// о воркерах, хотя виноват был HTTP).
	a.shutdownServers(serversDeadline(start, a.cfg.ShutdownTimeout))

	cancelWorkers()
	if a.metricsSrv != nil {
		_ = a.metricsSrv.Close()
	}
	done := make(chan error, 1)
	go func() { done <- g.Wait() }()
	var runErr error
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case runErr = <-done:
	case <-timer.C:
		a.log.Error("shutdown timeout: some workers did not stop; skipping resource cleanup")
		// Пулы pgx при Close ждут возврата соединений — с зависшим воркером это заблокирует выход.
		return errors.Join(errShutdownTimeout, firstErr(done))
	}
	// Разовые фоновые задачи (рассылки агентам после ответа панели) — дождаться в пределах бюджета остановки:
	// пулы и NATS закрываются после них (BC-04).
	bgCtx, bgCancel := context.WithDeadline(context.Background(), deadline)
	if !a.bg.Wait(bgCtx) {
		a.log.Warn("shutdown: background tasks did not finish in time")
	}
	bgCancel()
	a.closeResources()
	if runErr != nil {
		return runErr
	}
	a.log.Info("stopped")
	return nil
}

// serversDeadline — срок остановки серверов: ⅔ бюджета остановки (остальное — воркеры, фоновые задачи, закрытие).
func serversDeadline(start time.Time, budget time.Duration) time.Time {
	return start.Add(budget * 2 / 3)
}

// shutdownServers: readyz → 503; HTTP Shutdown параллельно с Drain агентов; затем stream.Close и
// GracefulStop gRPC (по дедлайну — Stop).
//
// Listener gRPC закрывается до Drain: новые соединения агентов получают отказ (RST), и L4-балансировщик сразу
// отправляет их на другой инстанс (nginx proxy_next_upstream). Иначе агенты, переподключающиеся по Drain, попадали бы
// обратно на этот инстанс (least_conn: на нём теперь меньше всего соединений) и рвались при его выходе — нагрузочный
// тест O4: 2 500 потоков → 7 300 обрывов и 78 с до полного восстановления (docs/load-test.md).
// Уже открытые соединения (потоки до переподключения, вызовы гостя) Serve не закрывает — их ведёт GracefulStop.
func (a *App) shutdownServers(deadline time.Time) {
	a.draining.Store(true)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	httpDone := make(chan struct{})
	go func() {
		defer close(httpDone)
		if err := a.httpSrv.Shutdown(ctx); err != nil {
			a.log.Warn("http shutdown: requests did not finish in time, closing connections", "err", err)
			_ = a.httpSrv.Close()
		}
	}()

	if a.grpcSrv != nil {
		if err := a.grpcLis.Close(); err != nil {
			a.log.Warn("grpc listener close", "err", err)
		}
		within := a.cfg.Agent.Stream.DrainWithin
		// Окно переподключения не дольше половины срока серверов (⅓ бюджета): остальное — на GracefulStop, воркеры и закрытие.
		if half := time.Until(deadline) / 2; within > half {
			within = half
		}
		if n := a.Stream.LocalStreams(); n > 0 && within > 0 {
			a.log.Info("draining agent streams", "streams", n, "within", within)
			a.Stream.Drain(within)
			waitStreams(ctx, a.Stream.LocalStreams, within)
		}
		// Закрыть оставшиеся потоки (UNAVAILABLE) — иначе GracefulStop ждал бы их до дедлайна.
		a.Stream.Close()
		grpcDone := make(chan struct{})
		go func() { a.grpcSrv.GracefulStop(); close(grpcDone) }()
		select {
		case <-grpcDone:
		case <-ctx.Done():
			a.grpcSrv.Stop()
			<-grpcDone
		}
	}
	<-httpDone
}

// waitStreams ждёт, пока локальных потоков не останется, не дольше within (+ запас на переподключение).
func waitStreams(ctx context.Context, count func() int, within time.Duration) {
	limit := time.NewTimer(within + time.Second)
	defer limit.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for count() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-limit.C:
			return
		case <-tick.C:
		}
	}
}

func firstErr(ch <-chan error) error {
	select {
	case err := <-ch:
		return err
	default:
		return nil
	}
}

func errString(err error) string {
	if err == nil {
		return "signal"
	}
	return err.Error()
}
