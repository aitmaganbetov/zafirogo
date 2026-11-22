package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var instanceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var keyIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,16}$`)

// problems — найденные ошибки конфигурации; Validate возвращает их все сразу.
type problems []error

func (p *problems) add(format string, a ...any) { *p = append(*p, fmt.Errorf(format, a...)) }

func (p *problems) check(err error) {
	if err != nil {
		*p = append(*p, err)
	}
}

// Validate проверяет значения. Сообщения об ошибках не содержат секретов. Порядок ошибок в тексте не задан
// (длительности проверяются обходом map).
func (c *Config) Validate() error {
	var p problems
	c.validateServer(&p)
	c.validateConnections(&p)
	c.validateAuth(&p)
	c.validateAgent(&p)
	c.validateDev(&p)
	c.validateSites(&p)
	c.validateEvents(&p)
	c.validateObservability(&p)
	if len(p) > 0 {
		return fmt.Errorf("config: %w", errors.Join(p...))
	}
	return nil
}

// validateServer — окружение, HTTP и длительности, которые обязаны быть положительными.
func (c *Config) validateServer(p *problems) {
	switch c.Env {
	case "dev", "test", "prod":
	default:
		p.add("env: ожидается dev|test|prod, получено %q", c.Env)
	}
	if c.ShutdownTimeout <= 0 {
		p.add("shutdown_timeout: должен быть > 0")
	}
	p.hostPort("http.addr", c.HTTP.Addr)
	positive := map[string]time.Duration{
		"http.read_header_timeout":               c.HTTP.ReadHeaderTimeout,
		"http.read_timeout":                      c.HTTP.ReadTimeout,
		"http.write_timeout":                     c.HTTP.WriteTimeout,
		"http.idle_timeout":                      c.HTTP.IdleTimeout,
		"http.request_timeout":                   c.HTTP.RequestTimeout,
		"auth.access_ttl":                        c.Auth.AccessTTL,
		"auth.idle_ttl":                          c.Auth.IdleTTL,
		"auth.remember_ttl":                      c.Auth.RememberTTL,
		"auth.max_lifetime":                      c.Auth.MaxLifetime,
		"auth.refresh_reuse_grace":               c.Auth.RefreshReuseGrace,
		"auth.touch_every":                       c.Auth.TouchEvery,
		"agent.enroll.code_ttl":                  c.Agent.Enroll.CodeTTL,
		"agent.grpc.proxy_header_timeout":        c.Agent.GRPC.ProxyHeaderTimeout,
		"agent.enroll.diskless_cert_lifetime":    c.Agent.Enroll.DisklessCertLifetime,
		"agent.cert.lifetime":                    c.Agent.Cert.Lifetime,
		"agent.cert_cache.refresh":               c.Agent.CertCache.Refresh,
		"agent.cert_cache.full_reload":           c.Agent.CertCache.FullReload,
		"agent.stream.presence_ttl":              c.Agent.Stream.PresenceTTL,
		"agent.stream.offline_grace":             c.Agent.Stream.OfflineGrace,
		"agent.stream.hello_timeout":             c.Agent.Stream.HelloTimeout,
		"agent.stream.status_interval":           c.Agent.Stream.StatusInterval,
		"agent.stream.state_debounce":            c.Agent.Stream.StateDebounce,
		"agent.stream.ack_timeout":               c.Agent.Stream.AckTimeout,
		"agent.sweeper.interval":                 c.Agent.Sweeper.Interval,
		"agent.sweeper.reconcile_every":          c.Agent.Sweeper.ReconcileEvery,
		"agent.sweeper.command_retention":        c.Agent.Sweeper.CommandRetention,
		"events.stream.max_age":                  c.Events.Stream.MaxAge,
		"events.stream.duplicates":               c.Events.Stream.Duplicates,
		"outbox.interval":                        c.Outbox.Interval,
		"outbox.retention":                       c.Outbox.Retention,
		"webhooks.timeout":                       c.Webhooks.Timeout,
		"webhooks.connect_timeout":               c.Webhooks.ConnectTimeout,
		"webhooks.poll_interval":                 c.Webhooks.PollInterval,
		"realtime.ping_interval":                 c.Realtime.PingInterval,
		"realtime.ping_timeout":                  c.Realtime.PingTimeout,
		"workers.expire_sessions.interval":       c.Workers.ExpireSessions.Interval,
		"workers.partitions.interval":            c.Workers.Partitions.Interval,
		"workers.log_retention.interval":         c.Workers.LogRetention.Interval,
		"workers.ledger_reconcile.interval":      c.Workers.LedgerReconcile.Interval,
		"workers.ledger_reconcile.full_interval": c.Workers.LedgerReconcile.FullInterval,
		"integrations.request_log_retention":     c.Integrations.RequestLogRetention,
		"mobile.token_ttl":                       c.Mobile.TokenTTL,
	}
	for name, d := range positive {
		if d <= 0 {
			p.add("%s: должен быть > 0", name)
		}
	}
	if c.Realtime.MaxLifetime < 0 {
		p.add("realtime.max_lifetime: должен быть ≥ 0")
	}
	if c.Auth.AccessTTL > 0 && c.Auth.IdleTTL > 0 && c.Auth.AccessTTL > c.Auth.IdleTTL {
		p.add("auth.access_ttl: должен быть ≤ auth.idle_ttl")
	}
	if c.HTTP.MaxHeaderBytes <= 0 {
		p.add("http.max_header_bytes: должен быть > 0")
	}
	_, err := c.HTTP.TrustedPrefixes()
	p.check(err)
}

// validateConnections — Postgres (два логина: zafiro_app и zafiro_platform), Valkey, NATS, журнал.
func (c *Config) validateConnections(p *problems) {
	if c.Database.URL.IsZero() {
		if c.Env == "prod" {
			p.add("database.url: обязателен в prod")
		}
	} else {
		if !validPostgresURL(c.Database.URL) {
			// Не включаем ошибку разбора: она может содержать DSN с паролем.
			p.add("database.url: ожидается postgres://… или postgresql://…")
		}
		if c.Database.PlatformURL.IsZero() {
			p.add("database.platform_url: обязателен вместе с database.url (логин роли zafiro_platform)")
		} else if !validPostgresURL(c.Database.PlatformURL) {
			p.add("database.platform_url: ожидается postgres://… или postgresql://…")
		} else if c.Database.PlatformURL == c.Database.URL {
			p.add("database.platform_url: должен быть другим логином, чем database.url (роли zafiro_platform и zafiro_app)")
		}
	}
	if n := c.Database.LockPoolMaxConns; n < 2 || n > 256 {
		p.add("database.lock_pool_max_conns: ожидается 2..256")
	}
	p.hostPort("valkey.addr", c.Valkey.Addr)
	if c.Valkey.TLS {
		if _, err := c.Valkey.TLSConfig(); err != nil {
			p.check(err)
		}
	} else if c.Valkey.TLSCAFile != "" || c.Valkey.TLSServerName != "" {
		p.add("valkey.tls_ca_file, valkey.tls_server_name: заданы без valkey.tls=true")
	}
	// Аудит SEC-2/OPS-19: в Valkey — сессии, refresh-семейства, билеты WebSocket; без пароля любой в сети читает их.
	if c.Env == "prod" && !c.Standalone() && c.Valkey.Password.IsZero() {
		p.add("valkey.password: обязателен в prod (requirepass или пользователь ACL с valkey.username)")
	}
	if !c.NATS.URL.IsZero() {
		if u, err := url.Parse(c.NATS.URL.Reveal()); err != nil || u.Host == "" {
			p.add("nats.url: некорректный URL")
		}
	} else if !c.Standalone() {
		p.add("nats.url: пусто")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		p.add("log.level: ожидается debug|info|warn|error, получено %q", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		p.add("log.format: ожидается json|text, получено %q", c.Log.Format)
	}
}

// validateAuth — ключи подписи и шифрования данных, параметры Argon2id, ключ суперадмина.
func (c *Config) validateAuth(p *problems) {
	p.keys("auth.signing_keys", c.Auth.SigningKeys)
	p.keys("security.data_keys", c.Security.DataKeys)
	if k := c.Superadmin.SecretKey; !k.IsZero() && len(k.Reveal()) < 32 {
		p.add("superadmin.secret_key: нужно не меньше 32 байт")
	}
	if c.Password.Iterations < 1 {
		p.add("password.iterations: должен быть ≥ 1")
	}
	if c.Password.Parallelism < 1 {
		p.add("password.parallelism: должен быть ≥ 1")
	}
	if c.Password.MemoryKiB < 8*uint32(c.Password.Parallelism) {
		p.add("password.memory_kib: должен быть ≥ 8 × parallelism")
	}
	if c.Password.MaxConcurrent < 0 {
		p.add("password.max_concurrent: должен быть ≥ 0")
	}
}

// validateAgent — agent.*: инстанс, gRPC, PKI, регистрация, поток, гостевой API, обновления, здоровье.
func (c *Config) validateAgent(p *problems) {
	if !instanceRe.MatchString(c.Agent.InstanceID) {
		p.add("agent.instance_id: ожидается [A-Za-z0-9_-]{1,64}, получено %q", c.Agent.InstanceID)
	}
	p.hostPort("agent.grpc.listen", c.Agent.GRPC.Listen)
	if c.Agent.GRPC.Endpoint == "" {
		p.add("agent.grpc.endpoint: пусто")
	}
	_, err := c.Agent.GRPC.ProxyTrustedPrefixes()
	p.check(err)
	pki := c.Agent.PKI
	files := pki.CACertFile != "" || pki.CAKeyFile != "" || pki.ServerCertFile != "" || pki.ServerKeyFile != ""
	if files && (pki.CACertFile == "" || pki.CAKeyFile == "" || pki.ServerCertFile == "" || pki.ServerKeyFile == "") {
		p.add("agent.pki: нужны все четыре файла: ca_cert_file, ca_key_file, server_cert_file, server_key_file")
	}
	for _, f := range pki.ExtraTrustFiles {
		if strings.TrimSpace(f) == "" {
			p.add("agent.pki.extra_trust_files: пустой путь")
		} else if f == pki.CACertFile {
			p.add("agent.pki.extra_trust_files: совпадает с ca_cert_file (там прежний CA, а не текущий)")
		}
	}
	if c.Env == "prod" && pki.DevDir != "" {
		p.add("agent.pki.dev_dir: запрещён в prod — задайте файлы CA и серверного сертификата")
	}
	if c.Env == "prod" && !files {
		p.add("agent.pki: в prod нужны ca_cert_file, ca_key_file, server_cert_file, server_key_file")
	}
	if len(c.Agent.Enroll.CodePepper.Reveal()) < 32 {
		p.add("agent.enroll.code_pepper: нужно не меньше 32 байт")
	}
	if c.Agent.Enroll.DisklessCertLifetime > 0 && c.Agent.Cert.Lifetime > 0 && c.Agent.Enroll.DisklessCertLifetime > c.Agent.Cert.Lifetime {
		p.add("agent.enroll.diskless_cert_lifetime: должен быть ≤ agent.cert.lifetime")
	}
	_, err = c.Agent.Update.UpdatePublicKeys()
	p.check(err)
	if c.Agent.Guest.TokenTTL < 0 || c.Agent.Guest.IdempotencyTTL < 0 || c.Agent.Guest.QRPollEvery < 0 {
		p.add("agent.guest.*: длительности должны быть ≥ 0")
	}
	if c.Agent.Stream.KeyPrefix == "" || c.Agent.Stream.SubjectPrefix == "" {
		p.add("agent.stream.key_prefix и agent.stream.subject_prefix: не должны быть пустыми")
	}
	if c.Agent.Stream.DrainWithin < 0 {
		p.add("agent.stream.drain_within: должен быть ≥ 0")
	}
	c.validateRebalance(p)
	c.validateHealth(p)
}

// validateDev — dev.*: только dev/test.
func (c *Config) validateDev(p *problems) {
	prod := c.Env == "prod"
	if c.Dev.FixedNow != "" {
		if prod {
			p.add("dev.fixed_now: запрещён в prod")
		} else if _, err := time.Parse(time.RFC3339, c.Dev.FixedNow); err != nil {
			p.add("dev.fixed_now: ожидается RFC 3339 (2026-10-15T12:00:00+05:00), получено %q", c.Dev.FixedNow)
		}
	}
	if prod && len(c.Dev.DisableWorkers) > 0 {
		p.add("dev.disable_workers: запрещён в prod")
	}
	if prod && c.Dev.TOTPAllowReplay {
		p.add("dev.totp_allow_replay: запрещён в prod")
	}
}

// validateSites — хосты кабинетов и адреса сайта, гостевых страниц и старого сервера.
func (c *Config) validateSites(p *problems) {
	if d := c.Superadmin.Domain; d != "" && !validHostName(d) {
		p.add("superadmin.domain: ожидается имя хоста без схемы и порта, получено %q", d)
	}
	if d := c.Reseller.Domain; d != "" {
		switch {
		case !validHostName(d):
			p.add("reseller.domain: ожидается имя хоста без схемы и порта, получено %q", d)
		case strings.EqualFold(d, c.Superadmin.Domain) || strings.EqualFold(d, c.HTTP.WikiHost):
			p.add("reseller.domain: хост %q уже занят кабинетом суперадмина или wiki", d)
		}
	}
	if raw := c.HTTP.PublicURL; raw != "" {
		if u, ok := httpURL(raw); !ok || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			p.add("http.public_url: ожидается http(s)://хост без пути, получено %q", raw)
		}
	}
	for name, raw := range map[string]string{
		"agent.guest.qr_base_url":      c.Agent.Guest.QRBaseURL,
		"agent.guest.public_base_url":  c.Agent.Guest.PublicBaseURL,
		"mobile.upstream":              c.Mobile.Upstream,
		"integrations.legacy_upstream": c.Integrations.LegacyUpstream,
	} {
		if _, ok := httpURL(raw); raw != "" && !ok {
			p.add("%s: ожидается http(s)://хост", name)
		}
	}
}

// validateEvents — поток событий, outbox, вебхуки, интеграционный API, WebSocket, воркеры.
func (c *Config) validateEvents(p *problems) {
	if c.Events.Stream.Replicas < 1 || c.Events.Stream.Replicas > 5 {
		p.add("events.stream.replicas: ожидается 1..5")
	}
	if mb := c.Events.Stream.MaxBytes; mb < 0 || (mb > 0 && mb < 1<<20) {
		p.add("events.stream.max_bytes: 0 (без предела) или ≥ 1048576 (1 МиБ)")
	}
	if c.Outbox.BatchSize < 1 {
		p.add("outbox.batch_size: должен быть ≥ 1")
	}
	if len(c.Webhooks.Backoff) == 0 {
		p.add("webhooks.backoff: пусто")
	}
	for i, d := range c.Webhooks.Backoff {
		if d <= 0 {
			p.add("webhooks.backoff[%d]: должен быть > 0", i)
		}
	}
	if c.Webhooks.DisableAfter < 1 || c.Webhooks.Workers < 1 {
		p.add("webhooks.disable_after и webhooks.workers: должны быть ≥ 1")
	}
	if i := c.Integrations; i.KeyRateLimit < 1 || i.KeyRateLimitMax < 1 || i.ClubRateLimit < 1 || i.FailedAuthPerMinute < 1 ||
		i.MaxRequestKB < 1 || i.MaxBulkItems < 1 || i.IdempotencyRetention <= 0 {
		p.add("integrations.*: лимиты должны быть ≥ 1, idempotency_retention > 0")
	}
	if c.Realtime.MaxConnsPerUser < 1 || c.Realtime.SendBuffer < 1 {
		p.add("realtime.max_conns_per_user и realtime.send_buffer: должны быть ≥ 1")
	}
	if c.Workers.Partitions.Months < 1 || c.Workers.Partitions.Months > 24 {
		p.add("workers.partitions.months: ожидается 1..24")
	}
	if lr := c.Workers.LedgerReconcile; lr.Interval > 0 && lr.FullInterval < lr.Interval {
		p.add("workers.ledger_reconcile.full_interval: не меньше workers.ledger_reconcile.interval")
	}
}

// validateObservability — observability.*: порт метрик, OTLP, доли выборки.
func (c *Config) validateObservability(p *problems) {
	ob := c.Observability
	if ob.MetricsListen != "" {
		if _, port, err := net.SplitHostPort(ob.MetricsListen); err != nil {
			p.add("observability.metrics_listen: %w", err)
		} else if port != "0" && (ob.MetricsListen == c.HTTP.Addr || ob.MetricsListen == c.Agent.GRPC.Listen) {
			p.add("observability.metrics_listen: должен отличаться от http.addr и agent.grpc.listen")
		}
	}
	if ep := ob.OTLPEndpoint; ep != "" {
		if u, ok := httpURL(ep); !ok || u.User != nil {
			p.add("observability.otlp_endpoint: ожидается http(s)://host:port без логина и пароля")
		}
	}
	if r := ob.TraceSampleRatio; r < 0 || r > 1 {
		p.add("observability.trace_sample_ratio: ожидается 0..1")
	}
	if ob.LogSampleInitial < 0 || ob.LogSampleThereafter < 0 {
		p.add("observability.log_sample_*: должны быть ≥ 0")
	}
}

// validateHealth — agent.health.*: пороги в разумных пределах (опечатка не должна ни засыпать историю ложными
// проблемами, ни выключить их молча).
func (c *Config) validateHealth(p *problems) {
	h := c.Agent.Health
	if h.CPUTempC < 40 || h.CPUTempC > 130 || h.GPUTempC < 40 || h.GPUTempC > 130 {
		p.add("agent.health.cpu_temp_c, gpu_temp_c: ожидается 40..130 °C")
	}
	if h.TempHysteresisC < 0 || h.TempHysteresisC > 30 {
		p.add("agent.health.temp_hysteresis_c: ожидается 0..30 °C")
	}
	if h.SystemDiskFreePercent > 90 || h.OtherDiskFreePercent > 90 || h.SystemDiskFreeGB > 1024 {
		p.add("agent.health.*_disk_free_percent: ожидается 0..90; system_disk_free_gb: 0..1024")
	}
	if h.LinkMinMbps > h.LinkFullMbps || h.LinkFullMbps > 100000 {
		p.add("agent.health.link_min_mbps: должен быть ≤ link_full_mbps (≤ 100000)")
	}
	if h.RaiseAfter < 0 || h.ClearAfter < 0 || h.RaiseAfter > 24*time.Hour || h.ClearAfter > 24*time.Hour {
		p.add("agent.health.raise_after, clear_after: ожидается 0..24h")
	}
	if h.EventRetention < 24*time.Hour {
		p.add("agent.health.event_retention: должен быть ≥ 24h")
	}
	if h.LiveMinGap < time.Second || h.LiveLoadStepPercent < 1 || h.LiveLoadStepPercent > 100 || h.LiveTempStepC < 1 {
		p.add("agent.health.live_min_gap ≥ 1s, live_load_step_percent 1..100, live_temp_step_c ≥ 1")
	}
	if h.PingInterval < 0 || (h.PingInterval > 0 && h.PingInterval < 5*time.Second) {
		p.add("agent.health.ping_interval: 0 (не мерить) или ≥ 5s")
	}
	if h.HourlyRetention < 14*24*time.Hour {
		p.add("agent.health.hourly_retention: должен быть ≥ 336h (две недели: вердикт сравнивает неделю с прошлой)")
	}
	if h.VerdictHotHours < 1 || h.VerdictHotHoursRepair <= h.VerdictHotHours || h.VerdictHotHoursRepair > 168 ||
		h.VerdictShutdowns < 1 || h.VerdictBugchecks < 1 || h.VerdictTempRiseC < 1 || h.VerdictTempRiseC > 50 ||
		h.VerdictNetworkSlowHours < 1 || h.VerdictNetworkSlowHours > 168 {
		p.add("agent.health.verdict_*: hot_hours ≥ 1, hot_hours < hot_hours_repair ≤ 168, shutdowns ≥ 1, bugchecks ≥ 1, temp_rise_c 1..50, network_slow_hours 1..168")
	}
}

// validateRebalance — agent.stream.rebalance_* (проверяются и при выключенной перебалансировке: включить её
// можно одной переменной, и ошибка не должна всплыть только тогда).
func (c *Config) validateRebalance(p *problems) {
	st := c.Agent.Stream
	if st.RebalanceInterval < time.Second {
		p.add("agent.stream.rebalance_interval: должен быть ≥ 1s")
	}
	if !(st.RebalanceThreshold >= 1.05 && st.RebalanceThreshold <= 10) { // !(…) ловит и NaN
		p.add("agent.stream.rebalance_threshold: ожидается 1.05..10 (доля от среднего числа потоков)")
	}
	if st.RebalanceMinExcess < 1 {
		p.add("agent.stream.rebalance_min_excess: должен быть ≥ 1")
	}
	if st.RebalanceBatch < 1 || st.RebalanceBatch > 10000 {
		p.add("agent.stream.rebalance_batch: ожидается 1..10000")
	}
	// ПК в окне переподключения не должен успеть стать «офлайн» (offline_grace) и вернуться до следующего решения.
	if st.RebalanceReconnectWithin <= 0 || st.RebalanceReconnectWithin >= st.OfflineGrace ||
		st.RebalanceReconnectWithin >= st.RebalanceInterval {
		p.add("agent.stream.rebalance_reconnect_within: должен быть > 0 и меньше agent.stream.offline_grace и agent.stream.rebalance_interval")
	}
}

// hostPort — обязательный адрес «хост:порт» ключа name.
func (p *problems) hostPort(name, addr string) {
	if addr == "" {
		p.add("%s: пусто", name)
	} else if _, _, err := net.SplitHostPort(addr); err != nil {
		p.add("%s: %w", name, err)
	}
}

// keys — список ключей kid:секрет ключа name (auth.signing_keys, security.data_keys): хотя бы один, id по формату и
// без повторов, секрет ≥ 32 байт.
func (p *problems) keys(name string, keys []SigningKey) {
	if len(keys) == 0 {
		p.add("%s: нужен хотя бы один ключ (%s%s=kid:секрет)", name, EnvPrefix, strings.ToUpper(strings.ReplaceAll(name, ".", "_")))
	}
	seen := map[string]bool{}
	for i, k := range keys {
		if !keyIDRe.MatchString(k.ID) {
			p.add("%s[%d].id: ожидается [A-Za-z0-9_-]{1,16}", name, i)
		}
		if seen[k.ID] {
			p.add("%s[%d].id: повтор %q", name, i, k.ID)
		}
		seen[k.ID] = true
		if len(k.Secret.Reveal()) < 32 {
			p.add("%s[%d].secret: нужно не меньше 32 байт", name, i)
		}
	}
}

// validHostName — имя хоста без схемы, порта, пути и логина (superadmin.domain, reseller.domain).
func validHostName(d string) bool {
	return !strings.ContainsAny(d, "/:@ ") && !strings.HasPrefix(d, ".")
}

// httpURL — адрес http(s)://хост…; ok = false — не разбирается, другая схема или нет хоста.
func httpURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, false
	}
	return u, true
}

func validPostgresURL(s Secret) bool {
	u, err := url.Parse(s.Reveal())
	return err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql")
}
