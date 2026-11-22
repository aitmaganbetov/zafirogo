// Package app собирает сервер zafiro из подсистем: пулы Postgres (zafiro_app и zafiro_platform), Valkey,
// NATS JetStream, PKI агентов, сервисы авторизации, регистрации и потока агентов, событий и денег,
// HTTP- и gRPC-серверы и фоновые воркеры. Build только создаёт и связывает; Run запускает всё под errgroup
// и выполняет graceful shutdown.
//
// Порядок остановки (бюджет — config.ShutdownTimeout):
//  1. /readyz начинает отвечать 503 (балансировщик перестаёт слать новые запросы), HTTP перестаёт
//     принимать соединения и дожидается текущих запросов;
//  2. stream.Drain: агенты получают Drain и переподключаются к другим инстансам в окне
//     agent.stream.drain_within; ждём, пока потоки закроются (или окно истечёт);
//  3. stream.Close и gRPC GracefulStop (по истечении бюджета — Stop);
//  4. отмена воркеров и ожидание их завершения;
//  5. закрытие NATS, Valkey и пулов.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/stream"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/config"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	httpserver "github.com/aitmaganbetov/zafirogo/server/internal/http"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/observability"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
	"github.com/aitmaganbetov/zafirogo/server/internal/realtime"
	"github.com/aitmaganbetov/zafirogo/server/internal/safego"
	"github.com/aitmaganbetov/zafirogo/server/internal/webui"
)

// App — собранный сервер. Поля сервисов открыты для сквозных тестов.
type App struct {
	cfg *config.Config
	log *slog.Logger

	// Подключения.
	AppPool      *pgxpool.Pool // логин роли zafiro_app (RLS по клубу)
	PlatformPool *pgxpool.Pool // логин роли zafiro_platform (все клубы)
	// I18n — один каталог переводов на приложение (HTTP, gRPC гостя, бот): одна фоновая сверка на платформенном пуле.
	I18n   *i18n.Catalog
	Valkey valkey.Client
	NATS   *nats.Conn
	JS     jetstream.JetStream

	// Сервисы.
	CA        *pki.CA
	Auth      *auth.Service
	Limiter   *ratelimit.Limiter
	Enroll    *enroll.Service
	CertCache *enroll.CertCache
	Stream    *stream.Service
	Billing   *billingsvc.Service
	Cash      *cashsvc.Service
	Realtime  *realtime.Handler
	// Obs — метрики Prometheus, трассировка и хуки журнала (Этап 6 O3, internal/observability).
	Obs *observability.Observer

	httpSrv *http.Server
	// metricsSrv/metricsLis — внутренний порт /metrics (observability.metrics_listen); nil — выключен.
	metricsSrv *http.Server
	metricsLis net.Listener
	httpLis    net.Listener
	grpcSrv    *grpcserver.Server
	grpcLis    net.Listener

	workers  []namedWorker
	revSub   *nats.Subscription
	closers  []func() // в обратном порядке при Close
	draining atomic.Bool
	baseCtx  context.Context
	// bg — разовые фоновые задачи (рассылки после ответа панели, отключение отозванного ПК): recover и ожидание
	// при остановке (BC-04).
	bg      *safego.Group
	cancel  context.CancelFunc
	runOnce sync.Once
	closed  sync.Once
}

type namedWorker struct {
	name string
	run  func(ctx context.Context) error
}

// withoutWorkers убирает фоновые задачи dev.disable_workers (только dev/test — проверяет config.Validate).
// Неизвестное имя — ошибка запуска: опечатка не должна молча оставлять задачу включённой.
func withoutWorkers(all []namedWorker, disable []string) ([]namedWorker, error) {
	if len(disable) == 0 {
		return all, nil
	}
	off := make(map[string]bool, len(disable))
	for _, name := range disable {
		off[name] = true
	}
	kept := make([]namedWorker, 0, len(all))
	for _, w := range all {
		if off[w.name] {
			delete(off, w.name)
			continue
		}
		kept = append(kept, w)
	}
	if len(off) > 0 {
		unknown := make([]string, 0, len(off))
		for name := range off {
			unknown = append(unknown, name)
		}
		slices.Sort(unknown)
		return nil, fmt.Errorf("dev.disable_workers: неизвестные фоновые задачи %v", unknown)
	}
	return kept, nil
}

// HTTPAddr — адрес, который слушает HTTP (полезно при ":0").
func (a *App) HTTPAddr() string { return a.httpLis.Addr().String() }

// GRPCAddr — адрес gRPC агентов ("" в режиме без зависимостей).
func (a *App) GRPCAddr() string {
	if a.grpcLis == nil {
		return ""
	}
	return a.grpcLis.Addr().String()
}

// Build создаёт все подсистемы и открывает порты, но ничего не запускает. При ошибке всё уже созданное
// закрывается. После успешного Build вызывающий обязан вызвать Run (или Close).
func Build(ctx context.Context, cfg *config.Config, log *slog.Logger) (_ *App, err error) {
	if log == nil {
		log = slog.Default()
	}
	baseCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	// a — локальная переменная, а не именованный результат: `return nil, err` не обнуляет её, и замыкания
	// closers при ошибке Build видят собранное (OBS-9). Ресурсы замыкания всё равно захватывают локально.
	a := &App{cfg: cfg, log: log, baseCtx: baseCtx, cancel: cancel, bg: safego.NewGroup(log)}
	a.closers = append(a.closers, cancel)
	defer func() {
		if err != nil {
			a.closeResources()
		}
	}()

	// Наблюдаемость (Этап 6 O3) — первой: обёртка журнала (request_id, trace_id, маскирование, выборка) действует
	// на все подсистемы ниже.
	if a.Obs, err = observability.New(ctx, observability.Options{
		Instance: cfg.Agent.InstanceID, OTLPEndpoint: cfg.Observability.OTLPEndpoint,
		SampleRatio: cfg.Observability.TraceSampleRatio, Log: log,
	}); err != nil {
		return nil, err
	}
	obs := a.Obs
	a.closers = append(a.closers, func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = obs.Shutdown(sctx)
	})
	log = slog.New(observability.NewLogHandler(log.Handler(), a.Obs, observability.LogOptions{
		SampleInitial: cfg.Observability.LogSampleInitial, SampleThereafter: cfg.Observability.LogSampleThereafter,
	}))
	a.log = log

	trusted, err := cfg.HTTP.TrustedPrefixes()
	if err != nil {
		return nil, err
	}
	routerOpts := httpserver.Options{
		Logger:         log,
		RequestTimeout: cfg.HTTP.RequestTimeout,
		TrustedProxies: trusted,
		Frontend:       webui.Handler(webui.FS(), webui.WithSuperadminDomain(cfg.Superadmin.Domain), webui.WithResellerDomain(cfg.Reseller.Domain), webui.WithPublicURL(cfg.HTTP.PublicOrigin()), webui.WithSiteMeta(siteTexts{a})),
		Checkers:       []httpserver.Checker{httpserver.CheckFunc{CheckName: "shutdown", Fn: a.checkNotDraining}},
		Observe:        a.Obs.HTTPMiddleware,
	}

	if cfg.Standalone() {
		// Режим без зависимостей (смоук-тест бинарника в CI): только /healthz, /readyz и фронт.
		log.Warn("database.url is empty: running without dependencies (healthz and frontend only)")
	} else {
		if err := a.buildCore(ctx, &routerOpts); err != nil {
			return nil, err
		}
	}

	a.httpSrv = &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           webui.Wiki(webui.FS(), cfg.HTTP.WikiHost, httpserver.NewRouter(routerOpts)),
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		MaxHeaderBytes:    cfg.HTTP.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	lc := net.ListenConfig{}
	if a.httpLis, err = lc.Listen(ctx, "tcp", cfg.HTTP.Addr); err != nil {
		return nil, fmt.Errorf("http listen: %w", err)
	}
	hlis := a.httpLis
	a.closers = append(a.closers, func() { _ = hlis.Close() })
	if addr := cfg.Observability.MetricsListen; addr != "" {
		if a.metricsLis, err = lc.Listen(ctx, "tcp", addr); err != nil {
			return nil, fmt.Errorf("observability.metrics_listen: %w", err)
		}
		mlis := a.metricsLis
		a.closers = append(a.closers, func() { _ = mlis.Close() })
		a.metricsSrv = a.Obs.NewServer()
	}
	if a.grpcSrv != nil {
		if a.grpcLis, err = lc.Listen(ctx, "tcp", cfg.Agent.GRPC.Listen); err != nil {
			return nil, fmt.Errorf("grpc listen: %w", err)
		}
		glis := a.grpcLis // сам TCP-listener; обёртка PROXY ниже закрывает его же
		a.closers = append(a.closers, func() { _ = glis.Close() })
		// За L4-балансировщиком адрес ПК приходит заголовком PROXY (docs/deploy.md): лимиты по IP и подсети ключа образа.
		proxyTrusted, err := cfg.Agent.GRPC.ProxyTrustedPrefixes()
		if err != nil {
			return nil, err
		}
		a.grpcLis = grpcserver.NewProxyListener(a.grpcLis, grpcserver.ProxyProtocol{
			Trusted: proxyTrusted, HeaderTimeout: cfg.Agent.GRPC.ProxyHeaderTimeout, Logger: log,
		})
	}
	return a, nil
}

func (a *App) closeResources() {
	a.closed.Do(func() {
		if a.revSub != nil {
			_ = a.revSub.Unsubscribe()
		}
		for i := len(a.closers) - 1; i >= 0; i-- {
			a.closers[i]()
		}
	})
}

// siteTexts — переводы заголовков страниц сайта для index.html (webui.WithSiteMeta, L-1). Каталог создаётся позже
// обработчика фронта (buildCore) и отсутствует в режиме без зависимостей — тогда тексты русские.
type siteTexts struct{ a *App }

func (s siteTexts) EnabledCodes(ctx context.Context) []string { return s.a.I18n.EnabledCodes(ctx) }

func (s siteTexts) Lookup(ctx context.Context, locale, key string) (string, bool) {
	return s.a.I18n.Lookup(ctx, locale, key)
}
