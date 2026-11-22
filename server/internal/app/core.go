package app

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/guestapi"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/stream"
	"github.com/aitmaganbetov/zafirogo/server/internal/apikey"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/mfa"
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
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
	"github.com/aitmaganbetov/zafirogo/server/internal/realtime"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/webhook"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker"
)

// wiring — части, которые шаги buildCore создают для следующих шагов (а не для App).
type wiring struct {
	ro         *httpserver.Options
	hasher     *password.Hasher
	tg         *telegramSet
	serverCert tls.Certificate
	updateKeys map[string]ed25519.PublicKey
	// guestOpts — одни опции API гостя для хуков стрима (до stream.New) и для gRPC-сервисов (после).
	guestOpts     guestapi.Options
	hooks         *guestapi.Hooks
	presence      *clubmap.PresenceEvents
	notifier      *commands.Notifier
	metrics       *monitoring.Recorder
	guestSrv      *guestapi.Server
	guestNotifier commands.SessionNotifier
	box           *secretbox.Box
	publisher     *outbox.Publisher
	deliverer     *webhook.Deliverer
	consumer      *webhook.Consumer
}

// buildCore — всё, кроме HTTP-сервера: подключения, сервисы, gRPC и воркеры. Шаги идут строго по порядку:
// каждый следующий пользуется созданным раньше, а closers закрываются в обратном порядке.
func (a *App) buildCore(ctx context.Context, ro *httpserver.Options) error {
	for _, k := range a.cfg.Generated {
		a.log.Warn("secret is not configured, a random one is used until restart (dev only)", "key", k)
	}
	w := &wiring{ro: ro}
	for _, step := range []func(context.Context, *wiring) error{
		a.connect,
		a.buildAuth,
		a.buildMoney,
		a.buildPKI,
		a.buildStream,
		a.buildGRPC,
		a.buildEvents,
		a.buildWorkers,
		a.buildRouterOptions,
	} {
		if err := step(ctx, w); err != nil {
			return err
		}
	}
	return nil
}

// connect — Postgres (два логина), Valkey, NATS и поток событий JetStream.
func (a *App) connect(ctx context.Context, _ *wiring) error {
	cfg, log := a.cfg, a.log
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
	a.I18n = i18n.NewCatalog(a.PlatformPool)
	a.closers = append(a.closers, a.I18n.Close) // закрывается раньше пула (closers — в обратном порядке)
	if err := checkRoles(ctx, a.AppPool, a.PlatformPool); err != nil {
		return err
	}

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
	return nil
}

// buildAuth — хэши паролей, сессии админки, сервис авторизации, лимиты запросов.
func (a *App) buildAuth(_ context.Context, w *wiring) error {
	cfg, log := a.cfg, a.log
	var err error
	if w.hasher, err = password.New(password.Options{
		Params: password.Params{
			MemoryKiB: cfg.Password.MemoryKiB, Iterations: cfg.Password.Iterations, Parallelism: cfg.Password.Parallelism,
			SaltLen: password.DefaultParams.SaltLen, KeyLen: password.DefaultParams.KeyLen,
		},
		MaxConcurrent: cfg.Password.MaxConcurrent,
	}); err != nil {
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
		Pool: a.AppPool, Hasher: w.hasher, Sessions: sessions, Logger: log,
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
	return nil
}

// buildMoney — деньги (Этап 1): касса как CashRecorder гостевых продаж, системные часы. Уведомления агента
// (SetNotifier, Cash.Notifier) подключаются, когда создан поток (buildStream, buildGRPC).
// dev.fixed_now (только dev/test, tools/visual) — фиксированное «сейчас» денег, кассы и обработчиков админки.
func (a *App) buildMoney(_ context.Context, w *wiring) error {
	now := a.cfg.Now()
	var clock billingsvc.Clock = billingsvc.SystemClock{}
	if a.cfg.Dev.FixedNow != "" {
		a.log.Warn("dev.fixed_now: the application clock is frozen (dev only)", "now", a.cfg.Dev.FixedNow)
		clock = billingsvc.ClockFunc(now)
		w.ro.Now = now
	}
	a.Cash = &cashsvc.Service{Now: w.ro.Now, ZNonces: cashsvc.NewValkeyZNonces(a.Valkey, "", 0)}
	a.Billing = billingsvc.New(a.AppPool, clock, a.Cash)
	return nil
}

// buildPKI — PKI агентов (CA и серверный сертификат gRPC) и кэш сертификатов ПК.
func (a *App) buildPKI(ctx context.Context, w *wiring) error {
	cfg, log := a.cfg, a.log
	popts := pki.Options{
		CACertFile: cfg.Agent.PKI.CACertFile, CAKeyFile: cfg.Agent.PKI.CAKeyFile,
		ServerCertFile: cfg.Agent.PKI.ServerCertFile, ServerKeyFile: cfg.Agent.PKI.ServerKeyFile,
		DevDir: cfg.Agent.PKI.DevDir, DevHosts: cfg.Agent.PKI.DevHosts,
		ExtraTrustFiles: cfg.Agent.PKI.ExtraTrustFiles, // ротация CA (S6-O1-1)
	}
	if popts.DevDir != "" {
		log.Warn("agent PKI: development CA in use (never in prod)", "dir", popts.DevDir)
	}
	var err error
	if a.CA, err = pki.LoadCA(popts); err != nil {
		return fmt.Errorf("agent pki: %w", err)
	}
	for _, c := range a.CA.ExtraTrust() {
		// Прежний выпускающий CA убирают из extra_trust_files, когда истекли его сертификаты (docs/deploy.md).
		log.Info("agent PKI: previous issuing CA is still trusted (rotation)", "subject", c.Subject.String(), "not_after", c.NotAfter)
	}
	if w.serverCert, err = pki.LoadServerCertificate(popts); err != nil {
		return fmt.Errorf("agent pki: server certificate: %w", err)
	}

	a.CertCache = enroll.NewCertCache(a.PlatformPool, enroll.CacheOptions{
		Refresh: cfg.Agent.CertCache.Refresh, FullReload: cfg.Agent.CertCache.FullReload, Logger: log,
	})
	// Кэш нужен до первого подключения агента.
	if err := a.CertCache.Load(ctx); err != nil {
		return fmt.Errorf("agent certificate cache: %w", err)
	}
	return nil
}

// buildStream — Telegram, хуки API гостя, поток агентов, отзыв сертификатов и регистрация ПК.
func (a *App) buildStream(_ context.Context, w *wiring) error {
	cfg, log, ro := a.cfg, a.log, w.ro
	// Telegram (Этап 4, H2): коды восстановления пароля и перевода, бот оповещений; ключи — в buildEvents (tg.store.Box).
	w.tg = newTelegram(a, w.hasher, cfg.Agent.Guest.TokenTTL, ro.Now, log)

	w.guestOpts = guestapi.Options{
		Recovery: w.tg.recovery, TransferCodes: w.tg.transfers,
		Pool: a.AppPool, Valkey: a.Valkey, Passwords: w.hasher, Limiter: a.Limiter, Billing: a.Billing,
		TokenTTL: cfg.Agent.Guest.TokenTTL, IdempotencyTTL: cfg.Agent.Guest.IdempotencyTTL,
		QRBaseURL: cfg.Agent.Guest.QRBaseURL, PublicBaseURL: cfg.Agent.Guest.PublicBaseURL,
		QRPollEvery: cfg.Agent.Guest.QRPollEvery, I18n: a.I18n, Log: log,
	}
	var err error
	if w.hooks, err = guestapi.NewHooks(w.guestOpts); err != nil {
		return fmt.Errorf("guest api hooks: %w", err)
	}

	// Решение «Обновления агента» (docs/DECISIONS.md): UpdateAvailable — только с проверенной подписью CI.
	if w.updateKeys, err = cfg.Agent.Update.UpdatePublicKeys(); err != nil {
		return fmt.Errorf("agent update keys: %w", err)
	}
	w.presence = &clubmap.PresenceEvents{Pool: a.AppPool, Log: log}
	a.Stream, err = stream.New(stream.Options{
		Instance: cfg.Agent.InstanceID, Pool: a.AppPool, Valkey: a.Valkey, NATS: a.NATS,
		KeyPrefix: cfg.Agent.Stream.KeyPrefix, SubjectPrefix: cfg.Agent.Stream.SubjectPrefix,
		PresenceTTL: cfg.Agent.Stream.PresenceTTL, OfflineGrace: cfg.Agent.Stream.OfflineGrace,
		HelloTimeout: cfg.Agent.Stream.HelloTimeout, StatusInterval: cfg.Agent.Stream.StatusInterval,
		StateDebounce: cfg.Agent.Stream.StateDebounce, AckTimeout: cfg.Agent.Stream.AckTimeout,
		Events: w.hooks, Welcome: w.hooks, Guests: w.hooks, UpdateKeys: w.updateKeys,
		// S6-O4-12: потоки после перезапуска/добавления инстанса выравниваются сами (docs/agent-protocol.md §13.6).
		Rebalance: stream.RebalanceOptions{
			Enabled: cfg.Agent.Stream.RebalanceEnabled, Interval: cfg.Agent.Stream.RebalanceInterval,
			Threshold: cfg.Agent.Stream.RebalanceThreshold, MinExcess: cfg.Agent.Stream.RebalanceMinExcess,
			Batch: cfg.Agent.Stream.RebalanceBatch, ReconnectWithin: cfg.Agent.Stream.RebalanceReconnectWithin,
		},
		// Присутствие → событие computer.state для карты клуба (вместо опроса /club-map/state, Этап 3).
		Presence: w.presence,
		// Этап 7: загрузка и здоровье ПК → computer.load (core NATS, без outbox), проблемы — agent/health.
		Live:   &clubmap.LiveEvents{NATS: a.NATS, Now: ro.Now, Log: log},
		Health: healthOptions(cfg.Agent.Health),
		// Итог команды агента → событие computer.command панели (решение W-1, command_results.go).
		Results: &commandResults{nats: a.NATS, now: ro.Now, log: log},
		Log:     log,
	})
	if err != nil {
		return fmt.Errorf("agent stream: %w", err)
	}
	a.closers = append(a.closers, a.Stream.Close)
	// Команды агенту после коммита денежных операций (SessionStarted/SessionEnded/BalanceChanged).
	w.notifier = a.Stream.Notifier()
	a.Billing.SetNotifier(w.notifier)
	a.Cash.Notifier = w.notifier

	revSubject := revokedSubject(cfg.Agent.Stream.SubjectPrefix)
	if a.revSub, err = subscribeRevoked(a.NATS, revSubject, a.CertCache, log); err != nil {
		return fmt.Errorf("nats: subscribe %s: %w", revSubject, err)
	}
	a.CertCache.OnRevoked(kickRevoked(a.baseCtx, a.bg, a.Stream, log))

	if a.Enroll, err = enroll.New(enroll.Options{
		Pool: a.AppPool, CA: a.CA, CodePepper: []byte(cfg.Agent.Enroll.CodePepper.Reveal()),
		CodeTTL: cfg.Agent.Enroll.CodeTTL, CertLifetime: cfg.Agent.Cert.Lifetime,
		DisklessCertLifetime: cfg.Agent.Enroll.DisklessCertLifetime,
		AgentEndpoint:        cfg.Agent.GRPC.Endpoint,
		Limiter:              &valkeyLimiter{c: a.Valkey, prefix: "zafiro:enroll-rl:"},
		Cache:                a.CertCache,
		OnRevoked:            publishRevoked(a.NATS, revSubject, log),
		GuestTokens:          w.hooks.Tokens(), // AG-5: отзыв сертификата ПК отзывает токен гостя этого ПК
		Activity:             enrollActivity(log),
		Logger:               log,
	}); err != nil {
		return fmt.Errorf("agent enroll: %w", err)
	}
	return nil
}

// buildGRPC — gRPC-сервер агентов: AgentService (Connect из a.Stream, GetTranslations, VerifyStaffPin) и сервисы
// гостя; уведомления агенту — через автобиллинг гостя.
func (a *App) buildGRPC(_ context.Context, w *wiring) error {
	var guestErr error
	// Счётчики запросов экрана «Сервер» суперадминки (RequestRecorder): HTTP, unary gRPC и сообщения стрима агента.
	w.metrics = monitoring.NewRecorder(nil)
	var err error
	if a.grpcSrv, err = grpcserver.NewServer(grpcserver.Options{
		// AG-16: тексты ошибок агента — на языке гостя (zafiro-locale) или клуба.
		ExtraUnary:  []grpc.UnaryServerInterceptor{w.metrics.UnaryInterceptor(), guestapi.LocalizeErrors(a.I18n)},
		ExtraStream: []grpc.StreamServerInterceptor{w.metrics.StreamInterceptor()},
		// Prometheus и спаны — внешними: отказы проверки сертификата и паники тоже считаются (Этап 6 O3).
		OuterUnary:        []grpc.UnaryServerInterceptor{a.Obs.UnaryServerInterceptor()},
		OuterStream:       []grpc.StreamServerInterceptor{a.Obs.StreamServerInterceptor()},
		ServerCertificate: w.serverCert, ClientCAs: a.CA.ClientCAs(), Certs: a.CertCache,
		Enrollment: enroll.NewGRPC(a.Enroll), Logger: a.log,
	}, func(s *grpc.Server) {
		o := w.guestOpts
		o.Stream = a.Stream
		w.guestSrv, guestErr = guestapi.Register(s, o)
	}); err != nil {
		return fmt.Errorf("grpc: %w", err)
	}
	if guestErr != nil {
		return fmt.Errorf("guest api: %w", guestErr)
	}
	// AG-12, AG-17: уведомления агенту через автобиллинг (сессия, баланс) и с названием тарифа в StartSession.
	w.guestNotifier = w.guestSrv.WrapNotifier(w.notifier)
	a.Billing.SetNotifier(w.guestNotifier)
	a.Cash.Notifier = w.guestNotifier
	return nil
}

// buildEvents — события: WebSocket админки, outbox, связка ключей данных, второй фактор суперадмина, вебхуки.
func (a *App) buildEvents(_ context.Context, w *wiring) error {
	cfg, log := a.cfg, a.log
	var err error
	if a.Realtime, err = realtime.NewHandler(realtime.Options{
		JS: a.JS, Live: a.NATS, MaxConnsPerUser: cfg.Realtime.MaxConnsPerUser, SendBuffer: cfg.Realtime.SendBuffer,
		PingInterval: cfg.Realtime.PingInterval, PingTimeout: cfg.Realtime.PingTimeout,
		MaxLifetime: cfg.Realtime.MaxLifetime, OriginPatterns: cfg.Realtime.OriginPatterns, Log: log,
	}); err != nil {
		return fmt.Errorf("realtime: %w", err)
	}
	if w.publisher, err = outbox.NewPublisher(outbox.PublisherOptions{
		Pool: a.PlatformPool, JS: a.JS, BatchSize: cfg.Outbox.BatchSize, Interval: cfg.Outbox.Interval,
		Retention: cfg.Outbox.Retention, Log: log,
	}); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	// Ключи шифрования секретов в базе (Q-050): security.data_keys; superadmin.secret_key Этапа 3 — только
	// расшифровка старых значений v1 (их перешифровывает воркер reencrypt-secrets).
	if w.box, err = newDataKeyring(cfg); err != nil {
		return err
	}
	w.tg.store.Box = w.box
	// Второй фактор суперадмина (Этап 7): ключи TOTP шифруются той же связкой; без него вход суперадмина закрыт.
	secondFactor, err := mfa.New(mfa.Options{Platform: a.PlatformPool, Valkey: a.Valkey, Box: w.box, Limiter: a.Limiter,
		AllowReplay: cfg.Dev.TOTPAllowReplay, Logger: log})
	if err != nil {
		return fmt.Errorf("superadmin second factor: %w", err)
	}
	if cfg.Dev.TOTPAllowReplay {
		log.Warn("dev.totp_allow_replay: superadmin TOTP codes may be reused within their window (dev only)")
	}
	a.Auth.SetSecondFactor(secondFactor)
	if w.deliverer, err = webhook.NewDeliverer(webhook.DelivererOptions{
		Secrets:  w.box,
		Platform: a.PlatformPool, App: a.AppPool,
		Guard:   webhookGuard(cfg),
		Backoff: cfg.Webhooks.Backoff, Timeout: cfg.Webhooks.Timeout, ConnectTimeout: cfg.Webhooks.ConnectTimeout,
		DisableAfter: cfg.Webhooks.DisableAfter, Workers: cfg.Webhooks.Workers, PollInterval: cfg.Webhooks.PollInterval,
		Log: log,
	}); err != nil {
		return fmt.Errorf("webhook deliverer: %w", err)
	}
	if w.consumer, err = webhook.NewConsumer(webhook.ConsumerOptions{JS: a.JS, App: a.AppPool, Wake: w.deliverer.Wake, Log: log}); err != nil {
		return fmt.Errorf("webhook consumer: %w", err)
	}
	return nil
}

// buildWorkers — фоновые задачи (a.workers) без выключенных dev.disable_workers, с метриками worker_up.
func (a *App) buildWorkers(_ context.Context, w *wiring) error {
	cfg, log, ro := a.cfg, a.log, w.ro
	sweeper := a.Stream.NewSweeper(a.PlatformPool)
	sweeper.Interval = cfg.Agent.Sweeper.Interval
	sweeper.ReconcileEvery = cfg.Agent.Sweeper.ReconcileEvery
	sweeper.CommandRetention = cfg.Agent.Sweeper.CommandRetention
	sweeper.OnOffline = w.presence.Offline // computer.state: ПК записан офлайн

	guestSrv := w.guestSrv
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
	monitor := &monitoring.Workers{Platform: a.PlatformPool, Recorder: w.metrics, Online: a.Stream.Presence(), Alerts: w.tg.monitor, Log: log}
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
		{"outbox-publisher", w.publisher.Run},
		{"webhook-consumer", w.consumer.Run},
		{"webhook-deliverer", w.deliverer.Run},
		{"realtime-hub", a.Realtime.Run},
		{"expire-sessions", expire.Run},
		{"burn-expired-packages", burnPackages.Run},
		{"agent-auto-billing", (&worker.AutoBilling{Platform: a.PlatformPool, Engine: guestSrv, Log: log}).Run}, // AG-12, Q-008
		// Тема Г (UQ-11): оплаченное время брони из приложения начинается, когда гость сидит за забронированным ПК —
		// по присутствию агентов и времени начала, а не на каждом heartbeat (один исполнитель, Q-086).
		{"start-app-bookings", (&worker.StartAppBookings{Platform: a.PlatformPool, App: a.AppPool, Notifier: w.guestNotifier, Now: ro.Now, Log: log,
			SignedIn: func(ctx context.Context, clubID, userID int64) ([]int64, error) {
				states, err := a.Stream.Presence().SignedInOrList(ctx, clubID, userID)
				ids := make([]int64, 0, len(states))
				for _, st := range states {
					ids = append(ids, st.ComputerID)
				}
				return ids, err
			}}).Run},
		{"reencrypt-secrets", (&worker.ReencryptSecrets{Platform: a.PlatformPool, Box: w.box, Log: log}).Run},
		{"ensure-partitions", partitions.Run},
		// Этап 7: почасовая история здоровья ПК — агрегаты часа из Valkey в Postgres (один исполнитель, Q-086).
		{"agent-health-hourly", (&health.HourlyFlusher{Valkey: a.Valkey, Prefix: a.Stream.Presence().Prefix(),
			App: a.AppPool, Platform: a.PlatformPool, Now: ro.Now, Log: log}).Run},
		// Аудит AC-3: сверка балансов клиентов и кассы с журналом операций (только находит и оповещает; один
		// исполнитель, Q-086).
		{"ledger-reconcile", (&worker.LedgerReconcile{Platform: a.PlatformPool, App: a.AppPool, Log: log,
			Interval: cfg.Workers.LedgerReconcile.Interval, FullInterval: cfg.Workers.LedgerReconcile.FullInterval}).Run},
		{"log-retention", (&worker.LogRetention{
			Platform: a.PlatformPool, Interval: cfg.Workers.LogRetention.Interval, Log: log,
			// Сроки журналов — как в старом (PruneIntegrationData); ledger и audit_log — никогда.
			Retention: map[string]time.Duration{"api_request_logs": cfg.Integrations.RequestLogRetention,
				// Этап 7: история проблем здоровья ПК (новая, в старом не было).
				"computer_health_events": cfg.Agent.Health.EventRetention,
				"computer_health_hourly": cfg.Agent.Health.HourlyRetention},
		}).Run},
	}
	var err error
	if a.workers, err = withoutWorkers(a.workers, cfg.Dev.DisableWorkers); err != nil {
		return err
	}
	if a.Stream.HealthHourly() == nil {
		// Здоровье выключено (agent.health.enabled = false): агрегатов часа нет — задача сброса не нужна. Убирается после
		// dev.disable_workers, чтобы её имя там оставалось допустимым.
		a.workers = slices.DeleteFunc(a.workers, func(w namedWorker) bool { return w.name == "agent-health-hourly" })
	}
	for i := range a.workers {
		a.workers[i].run = a.Obs.TrackWorker(a.workers[i].name, a.workers[i].run) // worker_up, worker_errors_total
	}
	a.setObservabilitySources(w.serverCert, guestSrv)
	return nil
}

// buildRouterOptions — зависимости HTTP-роутера (сервисы, хуки панели, старые API, проверки /readyz).
func (a *App) buildRouterOptions(_ context.Context, w *wiring) error {
	cfg, log, ro := a.cfg, a.log, w.ro
	ro.Auth = a.Auth
	ro.Limiter = a.Limiter
	ro.Pool = a.AppPool
	ro.PlatformPool = a.PlatformPool
	ro.I18n = a.I18n
	ro.Valkey = a.Valkey
	ro.Passwords = w.hasher
	ro.SignOutGuest = guestSignOut(a.Valkey, cfg.Agent.Guest.TokenTTL, a.Stream.Commands(), log)
	ro.ChatToGuest = chatToGuest(a.Stream.Commands(), a.Stream.Presence(), log)
	ro.ClubConfigChanged = clubConfigChanged(a.bg, w.hooks, a.Stream.Commands(), a.Stream.Presence(), log)
	guard := webhookGuard(cfg)
	ro.WebhookURLCheck = func(ctx context.Context, rawURL string) string {
		_, reason := guard.Check(ctx, rawURL)
		return reason
	}
	ro.WakeWebhooks = w.deliverer.Wake
	ro.APILimits = apikey.Limits{ // интеграционный API /api/v1 (config/integrations.php)
		KeyDefault: cfg.Integrations.KeyRateLimit, KeyMax: cfg.Integrations.KeyRateLimitMax, Club: cfg.Integrations.ClubRateLimit,
		FailedAuthPerMinute: cfg.Integrations.FailedAuthPerMinute, MaxRequestBytes: int64(cfg.Integrations.MaxRequestKB) * 1024,
		MaxBulkItems: cfg.Integrations.MaxBulkItems, IdempotencyRetention: cfg.Integrations.IdempotencyRetention,
	}
	ro.Secrets = w.box
	ro.Billing = a.Billing
	ro.Cash = a.Cash
	ro.Presence = a.Stream.Presence()
	ro.HealthVerdict = verdictRules(cfg.Agent.Health)
	ro.AgentCommands = a.Stream.Commands()
	ro.Enroll = a.Enroll
	ro.RequireSubscription = true
	if w.guestSrv != nil {
		ro.ApproveQrLogin = w.guestSrv.ApproveQrLogin // страница /pc-login/{code} (тот же Server, что будит WatchQrLogin)
	}
	ro.PasswordRecovery = w.tg.recovery
	var err error
	if ro.Legacy, err = a.legacyMiddleware(w); err != nil {
		return err
	}
	ro.Mounts = map[string]http.Handler{"/ws": a.Realtime}
	ro.Metrics = w.metrics
	ro.ResellerDomain = cfg.Reseller.Domain
	ro.Superadmin = httpx.SuperadminDeps{
		Domain: cfg.Superadmin.Domain, PublicURL: cfg.HTTP.PublicOrigin(), BackupPath: cfg.Superadmin.BackupPath, SSLHosts: cfg.Superadmin.SSLHosts,
		LedgerReconcileInterval: cfg.Workers.LedgerReconcile.Interval,
		Secrets:                 w.box, UpdateKeys: w.updateKeys, TelegramGateway: w.tg.gateway, TelegramAlerts: w.tg.alerts,
		BroadcastOnline: broadcastOnline(a.bg, a.Stream.Commands(), a.Stream.Presence(), log),
	}
	ro.Checkers = append(ro.Checkers,
		httpserver.CheckFunc{CheckName: "postgres", Fn: a.AppPool.Ping},
		httpserver.CheckFunc{CheckName: "postgres_platform", Fn: a.PlatformPool.Ping},
		httpserver.CheckFunc{CheckName: "valkey", Fn: a.pingValkey},
		httpserver.CheckFunc{CheckName: "nats", Fn: a.pingNATS},
	)
	return nil
}

// legacyMiddleware — старые API перед роутером: REST мобильных приложений (mobile.enabled, Q-058,
// docs/mobile-compat.md) и /api/v1 клубов, ещё работающих в Laravel (S6-O1-4, integrations.legacy_upstream).
// nil — ни того, ни другого.
func (a *App) legacyMiddleware(w *wiring) (func(d *httpx.Deps, root http.Handler) func(http.Handler) http.Handler, error) {
	cfg := a.cfg
	var mobileMW func(d *httpx.Deps, root http.Handler) func(http.Handler) http.Handler
	if cfg.Mobile.Enabled {
		lo := legacy.Options{Limiter: a.Limiter, Passwords: w.hasher, UpstreamHost: cfg.Mobile.UpstreamHost,
			PublicBaseURL: firstNonEmpty(cfg.Agent.Guest.PublicBaseURL, cfg.Agent.Guest.QRBaseURL, guestapi.DefaultQRBaseURL),
			TokenTTL:      cfg.Mobile.TokenTTL, Recovery: w.tg.recovery, TransferCodes: w.tg.transfers, Log: a.log}
		if cfg.Mobile.Upstream != "" {
			var err error
			if lo.Upstream, err = url.Parse(cfg.Mobile.Upstream); err != nil {
				return nil, fmt.Errorf("mobile.upstream: %w", err)
			}
		}
		mobileMW = func(d *httpx.Deps, root http.Handler) func(http.Handler) http.Handler {
			return legacy.New(lo, d, root).Middleware
		}
	}
	var v1Upstream *url.URL
	upRaw, upHost := cfg.IntegrationsUpstream()
	if upRaw != "" {
		var err error
		if v1Upstream, err = url.Parse(upRaw); err != nil {
			return nil, fmt.Errorf("integrations.legacy_upstream: %w", err)
		}
	}
	if mobileMW == nil && v1Upstream == nil {
		return nil, nil
	}
	return func(d *httpx.Deps, root http.Handler) func(http.Handler) http.Handler {
		v1mw := v1.LegacyUpstream(d, v1Upstream, upHost)
		if mobileMW == nil {
			return v1mw
		}
		mmw := mobileMW(d, root)
		return func(next http.Handler) http.Handler { return v1mw(mmw(next)) }
	}, nil
}

// webhookGuard — проверка адресов вебхуков по настройкам webhooks.*.
func webhookGuard(cfg *config.Config) webhook.Guard {
	return webhook.Guard{RequireHTTPS: cfg.Webhooks.RequireHTTPS, AllowPrivate: cfg.Webhooks.AllowPrivateHosts}
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

// firstNonEmpty — первая непустая строка.
func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
