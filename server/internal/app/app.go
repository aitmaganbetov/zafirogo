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
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/guestapi"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/stream"
	"github.com/aitmaganbetov/zafirogo/server/internal/apikey"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/config"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	httpserver "github.com/aitmaganbetov/zafirogo/server/internal/http"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/legacy"
	v1 "github.com/aitmaganbetov/zafirogo/server/internal/http/sections/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/monitoring"
	"github.com/aitmaganbetov/zafirogo/server/internal/observability"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
	"github.com/aitmaganbetov/zafirogo/server/internal/realtime"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/webhook"
	"github.com/aitmaganbetov/zafirogo/server/internal/webui"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker"
)

// App — собранный сервер. Поля сервисов открыты для сквозных тестов и будущих модулей волны B.
type App struct {
	cfg *config.Config
	log *slog.Logger

	// Подключения.
	AppPool      *pgxpool.Pool // логин роли zafiro_app (RLS по клубу)
	PlatformPool *pgxpool.Pool // логин роли zafiro_platform (все клубы)
	Valkey       valkey.Client
	NATS         *nats.Conn
	JS           jetstream.JetStream

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
	cancel   context.CancelFunc
	runOnce  sync.Once
	closed   sync.Once
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

// MetricsAddr — адрес внутреннего порта /metrics ("" — выключен).
func (a *App) MetricsAddr() string {
	if a.metricsLis == nil {
		return ""
	}
	return a.metricsLis.Addr().String()
}

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
	a := &App{cfg: cfg, log: log, baseCtx: baseCtx, cancel: cancel}
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
		Frontend:       webui.Handler(webui.FS(), webui.WithSuperadminDomain(cfg.Superadmin.Domain)),
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

// buildCore — всё, кроме HTTP-сервера: подключения, сервисы, gRPC и воркеры.
func (a *App) buildCore(ctx context.Context, ro *httpserver.Options) error {
	cfg, log := a.cfg, a.log
	for _, k := range cfg.Generated {
		log.Warn("secret is not configured, a random one is used until restart (dev only)", "key", k)
	}

	// --- Postgres: два логина ---
	var err error
	if a.AppPool, err = newPool(ctx, cfg.Database.URL.Reveal(), a.Obs.NewPgxTracer("app")); err != nil {
		// Текст ошибки pgx может содержать DSN — не выводим его.
		return errors.New("database.url: не удалось подключиться (проверьте DSN логина zafiro_app)")
	}
	a.closers = append(a.closers, a.AppPool.Close)
	if a.PlatformPool, err = newPool(ctx, cfg.Database.PlatformURL.Reveal(), a.Obs.NewPgxTracer("platform")); err != nil {
		return errors.New("database.platform_url: не удалось подключиться (проверьте DSN логина zafiro_platform)")
	}
	a.closers = append(a.closers, a.PlatformPool.Close)
	if err := checkRoles(ctx, a.AppPool, a.PlatformPool); err != nil {
		return err
	}

	// --- Valkey ---
	if a.Valkey, err = valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{cfg.Valkey.Addr},
		Password:    cfg.Valkey.Password.Reveal(),
		// Клиентский кэш (RESP3 tracking) не нужен: ключи короткоживущие, читаются точечно.
		DisableCache: true,
	}); err != nil {
		return fmt.Errorf("valkey: %w", err)
	}
	a.closers = append(a.closers, a.Valkey.Close)
	a.Valkey = a.Obs.WrapValkey(a.Valkey) // valkey_commands_total, valkey_errors_total

	// --- NATS + поток событий ---
	if a.NATS, err = nats.Connect(cfg.NATS.URL.Reveal(), append(a.Obs.NATSOptions(),
		nats.Name("zafiro-"+cfg.Agent.InstanceID),
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				a.Obs.NATSDisconnected()
				log.Warn("nats disconnected", "err", err)
			}
		}),
		nats.ReconnectHandler(func(*nats.Conn) { log.Info("nats reconnected") }),
	)...); err != nil {
		return errors.New("nats: не удалось подключиться (проверьте nats.url)")
	}
	a.closers = append(a.closers, a.NATS.Close)
	if a.JS, err = jetstream.New(a.NATS); err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}
	a.JS = a.Obs.WrapJetStream(a.JS) // спаны и счётчики публикации outbox и потребителей
	if _, err := outbox.EnsureStream(ctx, a.JS, outbox.StreamOptions{
		MaxAge: cfg.Events.Stream.MaxAge, Duplicates: cfg.Events.Stream.Duplicates, Replicas: cfg.Events.Stream.Replicas,
	}); err != nil {
		return fmt.Errorf("events stream: %w", err)
	}

	// --- Авторизация ---
	hasher, err := password.New(password.Options{
		Params: password.Params{
			MemoryKiB: cfg.Password.MemoryKiB, Iterations: cfg.Password.Iterations, Parallelism: cfg.Password.Parallelism,
			SaltLen: password.DefaultParams.SaltLen, KeyLen: password.DefaultParams.KeyLen,
		},
		MaxConcurrent: cfg.Password.MaxConcurrent,
	})
	if err != nil {
		return fmt.Errorf("password: %w", err)
	}
	keys := make([]session.Key, 0, len(cfg.Auth.SigningKeys))
	for _, k := range cfg.Auth.SigningKeys {
		keys = append(keys, session.Key{ID: k.ID, Secret: []byte(k.Secret.Reveal())})
	}
	sessions, err := session.New(session.Options{
		Client: a.Valkey, Keys: keys,
		AccessTTL: cfg.Auth.AccessTTL, IdleTTL: cfg.Auth.IdleTTL, RememberTTL: cfg.Auth.RememberTTL,
		MaxLifetime: cfg.Auth.MaxLifetime, ReuseGrace: cfg.Auth.RefreshReuseGrace, TouchEvery: cfg.Auth.TouchEvery,
	})
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	if a.Auth, err = auth.NewService(auth.Options{
		Pool: a.AppPool, Hasher: hasher, Sessions: sessions, Logger: log,
		Events: a.Obs.WrapAuthEvents(&authEvents{app: a.AppPool, platform: a.PlatformPool, log: log, now: cfg.Now()}),
	}); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if a.Limiter, err = ratelimit.New(ratelimit.Options{
		Client: a.Valkey, Enable: cfg.RateLimit.Enable, Disable: cfg.RateLimit.Disable,
		FailOpen: cfg.RateLimit.FailOpen, Logger: log,
	}); err != nil {
		return fmt.Errorf("ratelimit: %w", err)
	}

	// --- Деньги (Этап 1): касса как CashRecorder гостевых продаж, системные часы ---
	// Уведомления агента (SetNotifier, Cash.Notifier) подключаются ниже, когда создан поток.
	// dev.fixed_now (только dev/test, tools/visual) — фиксированное «сейчас» денег, кассы и обработчиков админки.
	now := cfg.Now()
	var clock billingsvc.Clock = billingsvc.SystemClock{}
	if cfg.Dev.FixedNow != "" {
		log.Warn("dev.fixed_now: the application clock is frozen (dev only)", "now", cfg.Dev.FixedNow)
		clock = billingsvc.ClockFunc(now)
		ro.Now = now
	}
	a.Cash = &cashsvc.Service{Now: ro.Now, ZNonces: cashsvc.NewValkeyZNonces(a.Valkey, "", 0)}
	a.Billing = billingsvc.New(a.AppPool, clock, a.Cash)

	// --- Агенты: PKI, кэш сертификатов, регистрация, поток ---
	popts := pki.Options{
		CACertFile: cfg.Agent.PKI.CACertFile, CAKeyFile: cfg.Agent.PKI.CAKeyFile,
		ServerCertFile: cfg.Agent.PKI.ServerCertFile, ServerKeyFile: cfg.Agent.PKI.ServerKeyFile,
		DevDir: cfg.Agent.PKI.DevDir, DevHosts: cfg.Agent.PKI.DevHosts,
		ExtraTrustFiles: cfg.Agent.PKI.ExtraTrustFiles, // ротация CA (S6-O1-1)
	}
	if popts.DevDir != "" {
		log.Warn("agent PKI: development CA in use (never in prod)", "dir", popts.DevDir)
	}
	if a.CA, err = pki.LoadCA(popts); err != nil {
		return fmt.Errorf("agent pki: %w", err)
	}
	for _, c := range a.CA.ExtraTrust() {
		// Прежний выпускающий CA убирают из extra_trust_files, когда истекли его сертификаты (docs/deploy.md).
		log.Info("agent PKI: previous issuing CA is still trusted (rotation)", "subject", c.Subject.String(), "not_after", c.NotAfter)
	}
	serverCert, err := pki.LoadServerCertificate(popts)
	if err != nil {
		return fmt.Errorf("agent pki: server certificate: %w", err)
	}

	a.CertCache = enroll.NewCertCache(a.PlatformPool, enroll.CacheOptions{
		Refresh: cfg.Agent.CertCache.Refresh, FullReload: cfg.Agent.CertCache.FullReload, Logger: log,
	})
	// Кэш нужен до первого подключения агента.
	if err := a.CertCache.Load(ctx); err != nil {
		return fmt.Errorf("agent certificate cache: %w", err)
	}

	// Telegram (Этап 4, H2): коды восстановления пароля и перевода, бот оповещений; ключи — ниже (tg.store.Box).
	tg := newTelegram(a, hasher, cfg.Agent.Guest.TokenTTL, ro.Now, log)

	// API гостя: те же опции для хуков стрима (до stream.New) и для gRPC-сервисов (после).
	guestOpts := guestapi.Options{
		Recovery: tg.recovery, TransferCodes: tg.transfers,
		Pool: a.AppPool, Valkey: a.Valkey, Passwords: hasher, Limiter: a.Limiter, Billing: a.Billing,
		TokenTTL: cfg.Agent.Guest.TokenTTL, IdempotencyTTL: cfg.Agent.Guest.IdempotencyTTL,
		QRBaseURL: cfg.Agent.Guest.QRBaseURL, PublicBaseURL: cfg.Agent.Guest.PublicBaseURL,
		QRPollEvery: cfg.Agent.Guest.QRPollEvery, Log: log,
	}
	hooks, err := guestapi.NewHooks(guestOpts)
	if err != nil {
		return fmt.Errorf("guest api hooks: %w", err)
	}

	// Решение «Обновления агента» (docs/DECISIONS.md): UpdateAvailable — только с проверенной подписью CI.
	updateKeys, err := cfg.Agent.Update.UpdatePublicKeys()
	if err != nil {
		return fmt.Errorf("agent update keys: %w", err)
	}
	presenceEvents := &clubmap.PresenceEvents{Pool: a.AppPool, Log: log}
	a.Stream, err = stream.New(stream.Options{
		Instance: cfg.Agent.InstanceID, Pool: a.AppPool, Valkey: a.Valkey, NATS: a.NATS,
		KeyPrefix: cfg.Agent.Stream.KeyPrefix, SubjectPrefix: cfg.Agent.Stream.SubjectPrefix,
		PresenceTTL: cfg.Agent.Stream.PresenceTTL, OfflineGrace: cfg.Agent.Stream.OfflineGrace,
		HelloTimeout: cfg.Agent.Stream.HelloTimeout, StatusInterval: cfg.Agent.Stream.StatusInterval,
		StateDebounce: cfg.Agent.Stream.StateDebounce, AckTimeout: cfg.Agent.Stream.AckTimeout,
		Events: hooks, Welcome: hooks, UpdateKeys: updateKeys,
		// S6-O4-12: потоки после перезапуска/добавления инстанса выравниваются сами (docs/agent-protocol.md §13.6).
		Rebalance: stream.RebalanceOptions{
			Enabled: cfg.Agent.Stream.RebalanceEnabled, Interval: cfg.Agent.Stream.RebalanceInterval,
			Threshold: cfg.Agent.Stream.RebalanceThreshold, MinExcess: cfg.Agent.Stream.RebalanceMinExcess,
			Batch: cfg.Agent.Stream.RebalanceBatch, ReconnectWithin: cfg.Agent.Stream.RebalanceReconnectWithin,
		},
		// Присутствие → событие computer.state для карты клуба (вместо опроса /club-map/state, Этап 3).
		Presence: presenceEvents,
		// TODO(B2): Results (stream.ResultHandler) —
		// реализаций пока нет (docs/open-questions/stage2-wiring.md W-1).
		Log: log,
	})
	if err != nil {
		return fmt.Errorf("agent stream: %w", err)
	}
	a.closers = append(a.closers, a.Stream.Close)
	// Команды агенту после коммита денежных операций (SessionStarted/SessionEnded/BalanceChanged).
	notifier := a.Stream.Notifier()
	a.Billing.SetNotifier(notifier)
	a.Cash.Notifier = notifier

	revSubject := revokedSubject(cfg.Agent.Stream.SubjectPrefix)
	if a.revSub, err = subscribeRevoked(a.NATS, revSubject, a.CertCache, log); err != nil {
		return fmt.Errorf("nats: subscribe %s: %w", revSubject, err)
	}
	a.CertCache.OnRevoked(kickRevoked(a.baseCtx, a.Stream, log))

	if a.Enroll, err = enroll.New(enroll.Options{
		Pool: a.AppPool, CA: a.CA, CodePepper: []byte(cfg.Agent.Enroll.CodePepper.Reveal()),
		CodeTTL: cfg.Agent.Enroll.CodeTTL, CertLifetime: cfg.Agent.Cert.Lifetime,
		DisklessCertLifetime: cfg.Agent.Enroll.DisklessCertLifetime,
		AgentEndpoint:        cfg.Agent.GRPC.Endpoint,
		Limiter:              &valkeyLimiter{c: a.Valkey, prefix: "zafiro:enroll-rl:"},
		Cache:                a.CertCache,
		OnRevoked:            publishRevoked(a.NATS, revSubject, log),
		GuestTokens:          hooks.Tokens(), // AG-5: отзыв сертификата ПК отзывает токен гостя этого ПК
		Activity:             enrollActivity(log),
		Logger:               log,
	}); err != nil {
		return fmt.Errorf("agent enroll: %w", err)
	}

	var guestErr error
	var guestSrv *guestapi.Server
	// Счётчики запросов экрана «Сервер» суперадминки (RequestRecorder): HTTP, unary gRPC и сообщения стрима агента.
	metrics := monitoring.NewRecorder(nil)
	if a.grpcSrv, err = grpcserver.NewServer(grpcserver.Options{
		// AG-16: тексты ошибок агента — на языке гостя (zafiro-locale) или клуба.
		ExtraUnary:  []grpc.UnaryServerInterceptor{metrics.UnaryInterceptor(), guestapi.LocalizeErrors(i18n.NewCatalog(a.AppPool))},
		ExtraStream: []grpc.StreamServerInterceptor{metrics.StreamInterceptor()},
		// Prometheus и спаны — внешними: отказы проверки сертификата и паники тоже считаются (Этап 6 O3).
		OuterUnary:        []grpc.UnaryServerInterceptor{a.Obs.UnaryServerInterceptor()},
		OuterStream:       []grpc.StreamServerInterceptor{a.Obs.StreamServerInterceptor()},
		ServerCertificate: serverCert, ClientCAs: a.CA.ClientCAs(), Certs: a.CertCache,
		Enrollment: enroll.NewGRPC(a.Enroll), Logger: log,
	}, func(s *grpc.Server) {
		// AgentService (Connect из a.Stream + GetTranslations, VerifyStaffPin) и сервисы гостя.
		o := guestOpts
		o.Stream = a.Stream
		guestSrv, guestErr = guestapi.Register(s, o)
	}); err != nil {
		return fmt.Errorf("grpc: %w", err)
	}
	if guestErr != nil {
		return fmt.Errorf("guest api: %w", guestErr)
	}
	// AG-12, AG-17: уведомления агенту через автобиллинг (сессия, баланс) и с названием тарифа в StartSession.
	guestNotifier := guestSrv.WrapNotifier(notifier)
	a.Billing.SetNotifier(guestNotifier)
	a.Cash.Notifier = guestNotifier

	// --- События: WebSocket админки, outbox, вебхуки ---
	if a.Realtime, err = realtime.NewHandler(realtime.Options{
		JS: a.JS, MaxConnsPerUser: cfg.Realtime.MaxConnsPerUser, SendBuffer: cfg.Realtime.SendBuffer,
		PingInterval: cfg.Realtime.PingInterval, PingTimeout: cfg.Realtime.PingTimeout,
		MaxLifetime: cfg.Realtime.MaxLifetime, OriginPatterns: cfg.Realtime.OriginPatterns, Log: log,
	}); err != nil {
		return fmt.Errorf("realtime: %w", err)
	}
	publisher, err := outbox.NewPublisher(outbox.PublisherOptions{
		Pool: a.PlatformPool, JS: a.JS, BatchSize: cfg.Outbox.BatchSize, Interval: cfg.Outbox.Interval,
		Retention: cfg.Outbox.Retention, Log: log,
	})
	if err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	// Ключи шифрования секретов в базе (Q-050): security.data_keys; superadmin.secret_key Этапа 3 — только
	// расшифровка старых значений v1 (их перешифровывает воркер reencrypt-secrets).
	box, err := newDataKeyring(cfg)
	if err != nil {
		return err
	}
	tg.store.Box = box
	deliverer, err := webhook.NewDeliverer(webhook.DelivererOptions{
		Secrets:  box,
		Platform: a.PlatformPool, App: a.AppPool,
		Guard:   webhook.Guard{RequireHTTPS: cfg.Webhooks.RequireHTTPS, AllowPrivate: cfg.Webhooks.AllowPrivateHosts},
		Backoff: cfg.Webhooks.Backoff, Timeout: cfg.Webhooks.Timeout, ConnectTimeout: cfg.Webhooks.ConnectTimeout,
		DisableAfter: cfg.Webhooks.DisableAfter, Workers: cfg.Webhooks.Workers, PollInterval: cfg.Webhooks.PollInterval,
		Log: log,
	})
	if err != nil {
		return fmt.Errorf("webhook deliverer: %w", err)
	}
	consumer, err := webhook.NewConsumer(webhook.ConsumerOptions{JS: a.JS, App: a.AppPool, Wake: deliverer.Wake, Log: log})
	if err != nil {
		return fmt.Errorf("webhook consumer: %w", err)
	}

	// --- Фоновые задачи ---
	sweeper := a.Stream.NewSweeper(a.PlatformPool)
	sweeper.Interval = cfg.Agent.Sweeper.Interval
	sweeper.ReconcileEvery = cfg.Agent.Sweeper.ReconcileEvery
	sweeper.CommandRetention = cfg.Agent.Sweeper.CommandRetention
	sweeper.OnOffline = presenceEvents.Offline // computer.state: ПК записан офлайн

	expire := &worker.ExpireSessions{
		Pool: a.PlatformPool, Interval: cfg.Workers.ExpireSessions.Interval, Log: log, Now: ro.Now, // dev.fixed_now — те же часы
		OnExpired: func(ctx context.Context, expired []billingsvc.ExpiredSession) {
			notifyExpired(a.Stream, log)(ctx, expired)
			guestSrv.OnExpired(ctx, expired) // AG-12: после конца тарифа автобиллинг покупает сразу
		},
	}
	// Решение Q-011 (docs/DECISIONS.md): суточное сгорание просроченных пакетов времени (ledger + журнал).
	burnPackages := &worker.BurnPackages{Platform: a.PlatformPool, App: a.AppPool, Log: log}
	partitions := &partitionsWorker{
		pool: a.PlatformPool, interval: cfg.Workers.Partitions.Interval, months: cfg.Workers.Partitions.Months, log: log,
	}

	// Мониторинг (CollectServerMetrics, SnapshotClubLoad, PruneMonitoringData) и истечение подписок (PD-10, Q-036).
	monitor := &monitoring.Workers{Platform: a.PlatformPool, Recorder: metrics, Online: a.Stream.Presence(), Alerts: tg.monitor, Log: log}
	expireSubs := &worker.ExpireSubscriptions{Platform: a.PlatformPool, Now: ro.Now, Log: log}
	a.workers = []namedWorker{
		{"monitoring-flush", monitor.RunFlush},
		{"monitoring-collect", monitor.RunCollect},
		{"monitoring-clubs", monitor.RunClubs},
		{"monitoring-prune", monitor.RunPrune},
		{"monitoring-watchdog", monitor.RunWatchdog},
		{"expire-subscriptions", expireSubs.Run},
		{"agent-cert-cache", a.CertCache.Run},
		{"agent-presence-sweeper", sweeper.Run},
		{"agent-stream-rebalance", a.Stream.RunRebalance}, // на каждом инстансе свой (не «один исполнитель»)
		{"outbox-publisher", publisher.Run},
		{"webhook-consumer", consumer.Run},
		{"webhook-deliverer", deliverer.Run},
		{"realtime-hub", a.Realtime.Run},
		{"expire-sessions", expire.Run},
		{"burn-expired-packages", burnPackages.Run},
		{"agent-auto-billing", (&worker.AutoBilling{Platform: a.PlatformPool, Engine: guestSrv, Log: log}).Run}, // AG-12, Q-008
		{"reencrypt-secrets", (&worker.ReencryptSecrets{Platform: a.PlatformPool, Box: box, Log: log}).Run},
		{"ensure-partitions", partitions.Run},
		{"log-retention", (&worker.LogRetention{
			Platform: a.PlatformPool, Interval: cfg.Workers.LogRetention.Interval, Log: log,
			// Сроки журналов — как в старом (PruneIntegrationData); ledger и audit_log — никогда.
			Retention: map[string]time.Duration{"api_request_logs": cfg.Integrations.RequestLogRetention},
		}).Run},
	}
	if a.workers, err = withoutWorkers(a.workers, cfg.Dev.DisableWorkers); err != nil {
		return err
	}
	for i := range a.workers {
		a.workers[i].run = a.Obs.TrackWorker(a.workers[i].name, a.workers[i].run) // worker_up, worker_errors_total
	}
	a.setObservabilitySources(serverCert, guestSrv)

	ro.Auth = a.Auth
	ro.Limiter = a.Limiter
	ro.Pool = a.AppPool
	ro.PlatformPool = a.PlatformPool
	ro.Valkey = a.Valkey
	ro.Passwords = hasher
	ro.SignOutGuest = guestSignOut(a.Valkey, cfg.Agent.Guest.TokenTTL, a.Stream.Commands(), log)
	ro.ChatToGuest = chatToGuest(a.Stream.Commands(), a.Stream.Presence(), log)
	ro.ClubConfigChanged = clubConfigChanged(hooks, a.Stream.Commands(), a.Stream.Presence(), log)
	webhookGuard := webhook.Guard{RequireHTTPS: cfg.Webhooks.RequireHTTPS, AllowPrivate: cfg.Webhooks.AllowPrivateHosts}
	ro.WebhookURLCheck = func(ctx context.Context, rawURL string) string {
		_, reason := webhookGuard.Check(ctx, rawURL)
		return reason
	}
	ro.WakeWebhooks = deliverer.Wake
	ro.APILimits = apikey.Limits{ // интеграционный API /api/v1 (config/integrations.php)
		KeyDefault: cfg.Integrations.KeyRateLimit, KeyMax: cfg.Integrations.KeyRateLimitMax, Club: cfg.Integrations.ClubRateLimit,
		FailedAuthPerMinute: cfg.Integrations.FailedAuthPerMinute, MaxRequestBytes: int64(cfg.Integrations.MaxRequestKB) * 1024,
		MaxBulkItems: cfg.Integrations.MaxBulkItems, IdempotencyRetention: cfg.Integrations.IdempotencyRetention,
	}
	ro.Secrets = box
	ro.Billing = a.Billing
	ro.Cash = a.Cash
	ro.Presence = a.Stream.Presence()
	ro.AgentCommands = a.Stream.Commands()
	ro.Enroll = a.Enroll
	ro.RequireSubscription = true
	if guestSrv != nil {
		ro.ApproveQrLogin = guestSrv.ApproveQrLogin // страница /pc-login/{code} (тот же Server, что будит WatchQrLogin)
	}
	ro.PasswordRecovery = tg.recovery
	var mobileMW func(d *httpx.Deps, root http.Handler) func(http.Handler) http.Handler
	if cfg.Mobile.Enabled {
		// Старый REST мобильных приложений (Q-058, docs/mobile-compat.md).
		lo := legacy.Options{Limiter: a.Limiter, Passwords: hasher, UpstreamHost: cfg.Mobile.UpstreamHost,
			PublicBaseURL: firstNonEmpty(cfg.Agent.Guest.PublicBaseURL, cfg.Agent.Guest.QRBaseURL, guestapi.DefaultQRBaseURL),
			TokenTTL:      cfg.Mobile.TokenTTL, Recovery: tg.recovery, TransferCodes: tg.transfers, Log: log}
		if cfg.Mobile.Upstream != "" {
			if lo.Upstream, err = url.Parse(cfg.Mobile.Upstream); err != nil {
				return fmt.Errorf("mobile.upstream: %w", err)
			}
		}
		mobileMW = func(d *httpx.Deps, root http.Handler) func(http.Handler) http.Handler {
			return legacy.New(lo, d, root).Middleware
		}
	}
	// /api/v1: ключи клубов, ещё работающих в Laravel, — туда (S6-O1-4, integrations.legacy_upstream).
	var v1Upstream *url.URL
	upRaw, upHost := cfg.IntegrationsUpstream()
	if upRaw != "" {
		if v1Upstream, err = url.Parse(upRaw); err != nil {
			return fmt.Errorf("integrations.legacy_upstream: %w", err)
		}
	}
	if mobileMW != nil || v1Upstream != nil {
		ro.Legacy = func(d *httpx.Deps, root http.Handler) func(http.Handler) http.Handler {
			v1mw := v1.LegacyUpstream(d, v1Upstream, upHost)
			if mobileMW == nil {
				return v1mw
			}
			mmw := mobileMW(d, root)
			return func(next http.Handler) http.Handler { return v1mw(mmw(next)) }
		}
	}
	ro.Mounts = map[string]http.Handler{"/ws": a.Realtime}
	ro.Metrics = metrics
	ro.Superadmin = httpx.SuperadminDeps{
		Domain: cfg.Superadmin.Domain, BackupPath: cfg.Superadmin.BackupPath, SSLHosts: cfg.Superadmin.SSLHosts,
		Secrets: box, UpdateKeys: updateKeys, TelegramGateway: tg.gateway, TelegramAlerts: tg.alerts,
		BroadcastOnline: broadcastOnline(a.Stream.Commands(), a.Stream.Presence(), log),
	}
	ro.Checkers = append(ro.Checkers,
		httpserver.CheckFunc{CheckName: "postgres", Fn: a.AppPool.Ping},
		httpserver.CheckFunc{CheckName: "postgres_platform", Fn: a.PlatformPool.Ping},
		httpserver.CheckFunc{CheckName: "valkey", Fn: a.pingValkey},
		httpserver.CheckFunc{CheckName: "nats", Fn: a.pingNATS},
	)
	return nil
}

// newDataKeyring — связка ключей данных из security.data_keys (+ устаревший superadmin.secret_key для v1).
func newDataKeyring(cfg *config.Config) (*secretbox.Box, error) {
	keys := make([]secretbox.Key, 0, len(cfg.Security.DataKeys))
	for _, k := range cfg.Security.DataKeys {
		keys = append(keys, secretbox.Key{ID: k.ID, Secret: k.Secret.Reveal()})
	}
	box, err := secretbox.NewKeyring(keys, cfg.Superadmin.SecretKey.Reveal())
	if err != nil {
		return nil, fmt.Errorf("security.data_keys: %w", err)
	}
	return box, nil
}

// Close освобождает ресурсы, если Run не вызывался (Run закрывает их сам).
func (a *App) Close() { a.closeResources() }

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

// firstNonEmpty — первая непустая строка.
func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
