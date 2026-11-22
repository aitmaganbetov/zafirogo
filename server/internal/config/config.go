// Package config загружает конфигурацию сервера zafiro.
//
// Источники (каждый следующий перекрывает предыдущий):
//  1. значения по умолчанию (Defaults);
//  2. необязательный YAML-файл (путь из флага -config или ZAFIRO_CONFIG_FILE);
//  3. переменные окружения с префиксом ZAFIRO_.
//
// Имя переменной — ключ в верхнем регистре, точки заменены на "_": agent.grpc.listen →
// ZAFIRO_AGENT_GRPC_LISTEN, database.platform_url → ZAFIRO_DATABASE_PLATFORM_URL. Переменные, которым
// не соответствует ни один ключ из Defaults, игнорируются. Списки в окружении — через запятую;
// auth.signing_keys и security.data_keys — "kid:секрет,kid2:секрет2".
//
// Секреты (DSN, пароли, ключи подписи, перец кодов, ключ CA) имеют тип Secret и никогда не попадают в
// логи: Secret и Config маскируют их в fmt и slog.
package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// EnvPrefix — префикс переменных окружения.
const EnvPrefix = "ZAFIRO_"

// FileEnv — переменная окружения с путём к YAML-файлу конфигурации.
const FileEnv = EnvPrefix + "CONFIG_FILE"

// DevPKIDir — каталог dev-PKI по умолчанию (env dev/test, если файлы CA не заданы).
const DevPKIDir = ".dev/pki"

// Config — полная конфигурация процесса.
type Config struct {
	// Env — окружение: dev | test | prod.
	Env string `koanf:"env"`
	// ShutdownTimeout — общий бюджет graceful shutdown (перестать принимать → Drain агентов →
	// закрыть gRPC/HTTP → дождаться воркеров → закрыть NATS/Valkey/пулы).
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
	HTTP            HTTP          `koanf:"http"`
	Database        Database      `koanf:"database"`
	Valkey          Valkey        `koanf:"valkey"`
	NATS            NATS          `koanf:"nats"`
	Log             Log           `koanf:"log"`
	Auth            Auth          `koanf:"auth"`
	Password        Password      `koanf:"password"`
	RateLimit       RateLimit     `koanf:"ratelimit"`
	Agent           Agent         `koanf:"agent"`
	Events          Events        `koanf:"events"`
	Outbox          Outbox        `koanf:"outbox"`
	Webhooks        Webhooks      `koanf:"webhooks"`
	Integrations    Integrations  `koanf:"integrations"`
	Realtime        Realtime      `koanf:"realtime"`
	Workers         Workers       `koanf:"workers"`
	Superadmin      Superadmin    `koanf:"superadmin"`
	Reseller        Reseller      `koanf:"reseller"`
	Mobile          Mobile        `koanf:"mobile"`
	Security        Security      `koanf:"security"`
	Dev             Dev           `koanf:"dev"`
	Observability   Observability `koanf:"observability"`

	// Generated — ключи, для которых в dev/test сгенерирован случайный секрет (не заданы явно).
	// Такие секреты живут до перезапуска процесса. Не из конфигурации.
	Generated []string `koanf:"-"`
}

// HTTP — настройки HTTP-сервера.
type HTTP struct {
	Addr              string        `koanf:"addr"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout"`
	ReadTimeout       time.Duration `koanf:"read_timeout"`
	WriteTimeout      time.Duration `koanf:"write_timeout"`
	IdleTimeout       time.Duration `koanf:"idle_timeout"`
	// RequestTimeout — дедлайн контекста обработчика (middleware.Timeout).
	RequestTimeout time.Duration `koanf:"request_timeout"`
	MaxHeaderBytes int           `koanf:"max_header_bytes"`
	// TrustedProxies — IP или CIDR обратных прокси, которым разрешено передавать
	// X-Forwarded-For / X-Real-IP. Пусто — заголовкам не доверяем вообще.
	// В env — через запятую: ZAFIRO_HTTP_TRUSTED_PROXIES=10.0.0.0/8,127.0.0.1
	TrustedProxies []string `koanf:"trusted_proxies"`
	// WikiHost — хост wiki (WIKI_DOMAIN старого, config app.wiki_domain; решение Q-069), например wiki.zafiro.kz.
	// Пусто — wiki на /wiki этого сайта. ZAFIRO_HTTP_WIKI_HOST.
	WikiHost string `koanf:"wiki_host"`
	// PublicURL — адрес сайта, каким его видят люди: https://zafiro.kz (без пути). Из него строятся ссылки, которые
	// уходят за пределы ответа на запрос: оповещения Telegram (если нет superadmin.domain), og:image страниц сайта,
	// адрес входа реселлеров. Пусто — такие ссылки не строятся из заголовка Host: оповещения уходят без ссылки.
	// ZAFIRO_HTTP_PUBLIC_URL.
	PublicURL string `koanf:"public_url"`
}

// PublicOrigin — PublicURL без «/» в конце.
func (h HTTP) PublicOrigin() string { return strings.TrimRight(h.PublicURL, "/") }

// Database — подключения к PostgreSQL. Два логина (db/migrations/0004_rls.sql):
// URL — член роли zafiro_app (RLS по клубу), PlatformURL — член роли zafiro_platform
// (фоновые задачи и суперадминка по всем клубам). Владелец схемы (миграции) серверу не нужен.
type Database struct {
	// URL — DSN pgx логина zafiro_app. Пусто — режим без зависимостей (только /healthz и фронт;
	// не для prod).
	URL Secret `koanf:"url"`
	// PlatformURL — DSN pgx логина zafiro_platform. Обязателен, если задан URL.
	PlatformURL Secret `koanf:"platform_url"`
	// LockPoolMaxConns — предел отдельного пула соединений, на которых фоновые задачи держат advisory lock лидерства
	// (worker/leader.UseLockPool; логин — тот же, что у PlatformURL). Работа задач идёт в пуле PlatformURL, поэтому
	// держатели локов не занимают его слоты (аудит PERF-1). Хватает числа задач (≈ 14); меньше — тики задач ждут
	// очереди, но не блокируют друг друга. ZAFIRO_DATABASE_LOCK_POOL_MAX_CONNS.
	LockPoolMaxConns int `koanf:"lock_pool_max_conns"`
}

// Valkey — подключение к Valkey. Один адрес (основной узел или адрес управляемого сервиса/прокси); Sentinel и кластер
// не поддерживаются — см. docs/dev-setup.md «Valkey: TLS и ACL».
type Valkey struct {
	Addr string `koanf:"addr"`
	// Username — пользователь ACL (AUTH <username> <password>); пусто — пользователь default.
	Username string `koanf:"username"`
	Password Secret `koanf:"password"`
	// TLS — соединение по TLS (управляемый Valkey/Redis, общая сеть; аудит SEC-2/OPS-6).
	TLS bool `koanf:"tls"`
	// TLSCAFile — PEM с CA сервера; пусто — системные корневые сертификаты.
	TLSCAFile string `koanf:"tls_ca_file"`
	// TLSServerName — имя в сертификате сервера; пусто — хост из addr.
	TLSServerName string `koanf:"tls_server_name"`
}

// NATS — подключение к NATS JetStream.
type NATS struct {
	// URL может содержать учётные данные (nats://user:pass@host:4222) — поэтому Secret.
	URL Secret `koanf:"url"`
}

// Log — настройки журналирования.
type Log struct {
	// Level — debug | info | warn | error.
	Level string `koanf:"level"`
	// Format — json | text.
	Format string `koanf:"format"`
}

// SigningKey — ключ подписи access-токенов (auth/session.Key).
type SigningKey struct {
	ID     string `koanf:"id"`
	Secret Secret `koanf:"secret"`
}

// Auth — сессии и токены админки (server/internal/auth/session).
type Auth struct {
	// SigningKeys — первый подписывает, остальные только проверяют (ротация). Секрет ≥ 32 байт.
	SigningKeys       []SigningKey  `koanf:"signing_keys"`
	AccessTTL         time.Duration `koanf:"access_ttl"`
	IdleTTL           time.Duration `koanf:"idle_ttl"`
	RememberTTL       time.Duration `koanf:"remember_ttl"`
	MaxLifetime       time.Duration `koanf:"max_lifetime"`
	RefreshReuseGrace time.Duration `koanf:"refresh_reuse_grace"`
	TouchEvery        time.Duration `koanf:"touch_every"`
}

// Password — параметры Argon2id новых хэшей (server/internal/auth/password).
type Password struct {
	MemoryKiB   uint32 `koanf:"memory_kib"`
	Iterations  uint32 `koanf:"iterations"`
	Parallelism uint8  `koanf:"parallelism"`
	// MaxConcurrent — одновременных хэшей; 0 — 2 × GOMAXPROCS.
	MaxConcurrent int `koanf:"max_concurrent"`
}

// RateLimit — именованные лимиты (server/internal/auth/ratelimit).
type RateLimit struct {
	Enable   []string `koanf:"enable"`
	Disable  []string `koanf:"disable"`
	FailOpen bool     `koanf:"fail_open"`
}

// Agent — всё, что связано с агентами ПК.
type Agent struct {
	// InstanceID — id инстанса в кластере ([A-Za-z0-9_-]{1,64}); пусто — имя хоста.
	InstanceID string      `koanf:"instance_id"`
	GRPC       AgentGRPC   `koanf:"grpc"`
	PKI        AgentPKI    `koanf:"pki"`
	Enroll     AgentEnroll `koanf:"enroll"`
	Cert       AgentCert   `koanf:"cert"`
	CertCache  CertCache   `koanf:"cert_cache"`
	Stream     AgentStream `koanf:"stream"`
	Sweeper    Sweeper     `koanf:"sweeper"`
	Guest      AgentGuest  `koanf:"guest"`
	Update     AgentUpdate `koanf:"update"`
	Health     AgentHealth `koanf:"health"`
}

// AgentHealth — здоровье ПК (Этап 7, agent/health, docs/agent-protocol.md §16): пороги проблем, защита от дребезга,
// срок хранения истории, живые обновления карты клуба и пинг агента. Решения — docs/open-questions/stage7-health.md.
type AgentHealth struct {
	// Enabled — считать проблемы и писать историю (computer_health_events). false — только снимок здоровья.
	Enabled bool `koanf:"enabled"`
	// CPUTempC, GPUTempC — перегрев, °C; TempHysteresisC — проблема уходит ниже порога на столько °C.
	CPUTempC        int32 `koanf:"cpu_temp_c"`
	GPUTempC        int32 `koanf:"gpu_temp_c"`
	TempHysteresisC int32 `koanf:"temp_hysteresis_c"`
	// SystemDiskFreePercent, SystemDiskFreeGB — системный диск: свободно меньше процента или ГБ.
	SystemDiskFreePercent uint64 `koanf:"system_disk_free_percent"`
	SystemDiskFreeGB      uint64 `koanf:"system_disk_free_gb"`
	// OtherDiskFreePercent — остальные диски: свободно меньше процента.
	OtherDiskFreePercent uint64 `koanf:"other_disk_free_percent"`
	// LinkFullMbps — скорость упала ниже этой после того, как была не ниже; LinkMinMbps — ниже этой всегда проблема.
	LinkFullMbps uint32 `koanf:"link_full_mbps"`
	LinkMinMbps  uint32 `koanf:"link_min_mbps"`
	// UnstableShutdowns, UnstableBugchecks — нестабилен за 7 суток (0 — признак не проверяется).
	UnstableShutdowns uint32 `koanf:"unstable_shutdowns"`
	UnstableBugchecks uint32 `koanf:"unstable_bugchecks"`
	// RaiseAfter, ClearAfter — сколько проблема держится / отсутствует, прежде чем попасть в историю.
	RaiseAfter time.Duration `koanf:"raise_after"`
	ClearAfter time.Duration `koanf:"clear_after"`
	// EventRetention — срок хранения истории проблем (месячные партиции, задача logs:retention).
	EventRetention time.Duration `koanf:"event_retention"`
	// LiveMinGap, LiveLoadStepPercent, LiveTempStepC — живые обновления карты: не чаще раза в LiveMinGap на ПК и
	// только при заметном изменении (смена набора проблем — сразу).
	LiveMinGap          time.Duration `koanf:"live_min_gap"`
	LiveLoadStepPercent uint32        `koanf:"live_load_step_percent"`
	LiveTempStepC       int32         `koanf:"live_temp_step_c"`
	// PingInterval — как часто мерить время ответа агента по стриму (0 — не мерить).
	PingInterval time.Duration `koanf:"ping_interval"`
	// HourlyRetention — срок хранения почасовой истории computer_health_hourly (месячные партиции, logs:retention).
	HourlyRetention time.Duration `koanf:"hourly_retention"`
	// Verdict* — вердикт исправности за неделю (health.VerdictRules, S7-H-15): «наблюдать», если часов с перегревом
	// больше VerdictHotHours (больше VerdictHotHoursRepair — «ремонт»), внезапных выключений ≥ VerdictShutdowns, средняя
	// температура выросла к прошлой неделе на ≥ VerdictTempRiseC; «ремонт» — «синих экранов» ≥ VerdictBugchecks.
	VerdictHotHours       int64 `koanf:"verdict_hot_hours"`
	VerdictHotHoursRepair int64 `koanf:"verdict_hot_hours_repair"`
	VerdictShutdowns      int64 `koanf:"verdict_shutdowns"`
	VerdictBugchecks      int64 `koanf:"verdict_bugchecks"`
	VerdictTempRiseC      int32 `koanf:"verdict_temp_rise_c"`
	// VerdictNetworkSlowHours — бездисковый ПК (S7-H-18): «наблюдать», если часов с медленной сетью (network_slow, у
	// бездискового — ниже LinkFullMbps) за неделю больше этого.
	VerdictNetworkSlowHours int64 `koanf:"verdict_network_slow_hours"`
}

// AgentUpdate — обновления агента. Решение «Обновления агента» (docs/DECISIONS.md): подпись Ed25519 обязательна,
// ключ подписи — только в CI-секретах. Серверу нужен лишь открытый ключ: перед рассылкой UpdateAvailable сервер
// проверяет подпись CI и не отправляет на ПК неподписанное (agent/commands.VerifyUpdate).
type AgentUpdate struct {
	// PublicKeys — открытые ключи Ed25519 "key_id:base64(32 байта)" (не секрет). Несколько — на время ротации.
	// Пусто — обновления не рассылаются. В env — через запятую: ZAFIRO_AGENT_UPDATE_PUBLIC_KEYS=2026-10:MCowBQ….
	PublicKeys []string `koanf:"public_keys"`
}

// updateKeyIDRe — key_id ключа подписи обновлений агента (UpdateAvailable.key_id, например "2026-10").
var updateKeyIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// UpdatePublicKeys разбирает agent.update.public_keys: key_id → открытый ключ Ed25519.
func (u AgentUpdate) UpdatePublicKeys() (map[string]ed25519.PublicKey, error) {
	out := make(map[string]ed25519.PublicKey, len(u.PublicKeys))
	for i, item := range u.PublicKeys {
		id, b64, ok := strings.Cut(strings.TrimSpace(item), ":")
		if !ok || !updateKeyIDRe.MatchString(id) {
			return nil, fmt.Errorf("agent.update.public_keys[%d]: ожидается key_id:base64", i)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("agent.update.public_keys[%d]: нужен открытый ключ Ed25519 (32 байта в base64)", i)
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("agent.update.public_keys[%d]: повтор key_id %q", i, id)
		}
		out[id] = ed25519.PublicKey(raw)
	}
	return out, nil
}

// AgentGuest — API гостя на ПК (agent/guestapi). Нули и пустые строки — значения пакета по умолчанию.
type AgentGuest struct {
	// TokenTTL — срок токена гостя (0 — 12 ч).
	TokenTTL time.Duration `koanf:"token_ttl"`
	// IdempotencyTTL — хранение ответов по ключу идемпотентности (0 — 24 ч).
	IdempotencyTTL time.Duration `koanf:"idempotency_ttl"`
	// QRBaseURL — сайт для ссылки QR-входа (пусто — https://zafiro.kz).
	QRBaseURL string `koanf:"qr_base_url"`
	// PublicBaseURL — адрес картинок (логотип, новости); пусто — QRBaseURL.
	PublicBaseURL string `koanf:"public_base_url"`
	// QRPollEvery — период проверки статуса QR-входа (0 — 3 с).
	QRPollEvery time.Duration `koanf:"qr_poll_every"`
}

// AgentGRPC — gRPC-сервер агентов (mTLS).
type AgentGRPC struct {
	Listen string `koanf:"listen"`
	// Endpoint — публичный адрес AgentService, который получает агент в ответе Enroll.
	Endpoint string `koanf:"endpoint"`
	// ProxyTrusted — IP/CIDR L4-балансировщиков, которые ставят заголовок PROXY protocol (v1 или v2) перед TLS
	// (grpcserver.NewProxyListener): от них заголовок обязателен, адрес ПК берётся из него (лимиты по IP, подсети
	// ключа образа Q-037). Пусто — PROXY protocol выключен. ZAFIRO_AGENT_GRPC_PROXY_TRUSTED=10.0.0.5,10.1.0.0/24.
	ProxyTrusted []string `koanf:"proxy_trusted"`
	// ProxyHeaderTimeout — время на получение заголовка PROXY от балансировщика.
	ProxyHeaderTimeout time.Duration `koanf:"proxy_header_timeout"`
}

// ProxyTrustedPrefixes разбирает agent.grpc.proxy_trusted (IP или CIDR).
func (g AgentGRPC) ProxyTrustedPrefixes() ([]netip.Prefix, error) {
	return parsePrefixes("agent.grpc.proxy_trusted", g.ProxyTrusted)
}

// AgentPKI — CA агентов и серверный сертификат gRPC (server/internal/pki).
type AgentPKI struct {
	CACertFile string `koanf:"ca_cert_file"`
	// CAKeyFile — путь к ключу CA (сам путь не секрет, файл — секрет вне репозитория).
	CAKeyFile      string `koanf:"ca_key_file"`
	ServerCertFile string `koanf:"server_cert_file"`
	ServerKeyFile  string `koanf:"server_key_file"`
	// ExtraTrustFiles — прежние выпускающие CA агентов (PEM, первый сертификат файла) на время ротации CA
	// (S6-O1-1): их клиентские сертификаты принимаются, пока не истекут (≤ agent.cert.lifetime); выпускает и
	// продлевает только ca_cert_file. Порядок — docs/deploy.md «Ротация CA агентов».
	ExtraTrustFiles []string `koanf:"extra_trust_files"`
	// DevDir — только dev/test: PKI генерируется в этой папке при первом запуске.
	DevDir string `koanf:"dev_dir"`
	// DevHosts — имена/IP dev-сертификата сервера (пусто — localhost, 127.0.0.1, ::1).
	DevHosts []string `koanf:"dev_hosts"`
}

// AgentEnroll — коды регистрации ПК.
type AgentEnroll struct {
	// CodePepper — секрет HMAC кодов (≥ 32 байт), общий для всех инстансов.
	CodePepper Secret        `koanf:"code_pepper"`
	CodeTTL    time.Duration `koanf:"code_ttl"`
	// DisklessCertLifetime — срок сертификата бездискового ПК по ключу образа и его продлений (решение Q-037: 24 ч).
	DisklessCertLifetime time.Duration `koanf:"diskless_cert_lifetime"`
}

// AgentCert — клиентские сертификаты агентов.
type AgentCert struct {
	Lifetime time.Duration `koanf:"lifetime"`
}

// CertCache — кэш действующих сертификатов (enroll.CertCache).
type CertCache struct {
	Refresh    time.Duration `koanf:"refresh"`
	FullReload time.Duration `koanf:"full_reload"`
}

// AgentStream — поток Connect (agent/stream), docs/open-questions/stage2-agent-stream.md AS-12.
type AgentStream struct {
	PresenceTTL    time.Duration `koanf:"presence_ttl"`
	OfflineGrace   time.Duration `koanf:"offline_grace"`
	HelloTimeout   time.Duration `koanf:"hello_timeout"`
	StatusInterval time.Duration `koanf:"status_interval"`
	StateDebounce  time.Duration `koanf:"state_debounce"`
	AckTimeout     time.Duration `koanf:"ack_timeout"`
	KeyPrefix      string        `koanf:"key_prefix"`
	SubjectPrefix  string        `koanf:"subject_prefix"`
	// DrainWithin — окно, в котором агенты переподключаются при плановой остановке (Drain).
	DrainWithin time.Duration `koanf:"drain_within"`
	// Rebalance* — автоматическая перебалансировка потоков между инстансами (stream.RebalanceOptions,
	// docs/agent-protocol.md §13.6): инстанс, у которого потоков больше Threshold × среднего и не меньше чем
	// на MinExcess, раз в Interval просит до Batch простаивающих ПК переподключиться (Drain с окном ReconnectWithin).
	RebalanceEnabled         bool          `koanf:"rebalance_enabled"`
	RebalanceInterval        time.Duration `koanf:"rebalance_interval"`
	RebalanceThreshold       float64       `koanf:"rebalance_threshold"`
	RebalanceMinExcess       int           `koanf:"rebalance_min_excess"`
	RebalanceBatch           int           `koanf:"rebalance_batch"`
	RebalanceReconnectWithin time.Duration `koanf:"rebalance_reconnect_within"`
}

// Sweeper — фоновая проверка присутствия (presence.Sweeper).
type Sweeper struct {
	Interval         time.Duration `koanf:"interval"`
	ReconcileEvery   time.Duration `koanf:"reconcile_every"`
	CommandRetention time.Duration `koanf:"command_retention"`
}

// Events — поток событий JetStream.
type Events struct {
	Stream EventsStream `koanf:"stream"`
}

// EventsStream — outbox.StreamOptions.
type EventsStream struct {
	MaxAge     time.Duration `koanf:"max_age"`
	Duplicates time.Duration `koanf:"duplicates"`
	Replicas   int           `koanf:"replicas"`
	// MaxBytes — предел размера потока в байтах (аудит OPS-12): при заполнении JetStream удаляет самые старые
	// события (Discard old), а не останавливает публикацию. 0 — без предела (только MaxAge). Расчёт — docs/dev-setup.md.
	MaxBytes int64 `koanf:"max_bytes"`
}

// Outbox — публикатор outbox → JetStream.
type Outbox struct {
	BatchSize int           `koanf:"batch_size"`
	Interval  time.Duration `koanf:"interval"`
	Retention time.Duration `koanf:"retention"`
}

// Webhooks — доставка вебхуков.
type Webhooks struct {
	RequireHTTPS      bool            `koanf:"require_https"`
	AllowPrivateHosts bool            `koanf:"allow_private_hosts"`
	Timeout           time.Duration   `koanf:"timeout"`
	ConnectTimeout    time.Duration   `koanf:"connect_timeout"`
	Backoff           []time.Duration `koanf:"backoff"`
	DisableAfter      int             `koanf:"disable_after"`
	Workers           int             `koanf:"workers"`
	PollInterval      time.Duration   `koanf:"poll_interval"`
}

// Integrations — интеграционный API /api/v1 (config/integrations.php старого проекта; значения по умолчанию — старые).
type Integrations struct {
	// KeyRateLimit — запросов в минуту на ключ по умолчанию (default_rate_limit_per_minute, API_KEY_RATE_LIMIT).
	KeyRateLimit int `koanf:"key_rate_limit"`
	// KeyRateLimitMax — предел лимита ключа (max_rate_limit_per_minute, API_KEY_RATE_LIMIT_MAX).
	KeyRateLimitMax int `koanf:"key_rate_limit_max"`
	// ClubRateLimit — запросов в минуту на клуб по всем ключам (club_rate_limit_per_minute, API_CLUB_RATE_LIMIT).
	ClubRateLimit int `koanf:"club_rate_limit"`
	// FailedAuthPerMinute — неудачных проверок ключа в минуту с одного IP (failed_auth_per_minute_per_ip).
	FailedAuthPerMinute int `koanf:"failed_auth_per_minute"`
	// MaxRequestKB — предел тела запроса (max_request_kb).
	MaxRequestKB int `koanf:"max_request_kb"`
	// MaxBulkItems — ПК в одной команде управления (max_bulk_items).
	MaxBulkItems int `koanf:"max_bulk_items"`
	// IdempotencyRetention — сколько хранится ответ по Idempotency-Key (idempotency_retention_hours).
	IdempotencyRetention time.Duration `koanf:"idempotency_retention"`
	// RequestLogRetention — срок хранения журнала запросов api_request_logs (request_log_retention_days, 90 дней);
	// удаляет фоновая задача logs:retention целыми месячными партициями (worker.LogRetention).
	RequestLogRetention time.Duration `koanf:"request_log_retention"`
	// LegacyUpstream — старый Laravel на время поклубного перехода (S6-O1-4): запрос /api/v1 с ключом, префикса
	// которого нет на этом сервере, уходит туда без изменений; свой ключ (даже отозванный) обслуживается здесь.
	// Пусто — берётся mobile.upstream (тот же Laravel); оба пусты — прокси нет, все ключи здесь.
	LegacyUpstream string `koanf:"legacy_upstream"`
	// LegacyUpstreamHost — заголовок Host для LegacyUpstream (пусто — mobile.upstream_host, затем Host запроса).
	LegacyUpstreamHost string `koanf:"legacy_upstream_host"`
}

// IntegrationsUpstream — адрес и Host старого Laravel для неизвестных ключей /api/v1 ("" — прокси нет):
// integrations.legacy_upstream, иначе mobile.upstream (S6-O1-4).
func (c *Config) IntegrationsUpstream() (upstream, host string) {
	upstream, host = c.Integrations.LegacyUpstream, c.Integrations.LegacyUpstreamHost
	if upstream == "" {
		upstream = c.Mobile.Upstream
	}
	if host == "" {
		host = c.Mobile.UpstreamHost
	}
	return upstream, host
}

// Realtime — WebSocket админки (/ws).
type Realtime struct {
	MaxConnsPerUser int           `koanf:"max_conns_per_user"`
	SendBuffer      int           `koanf:"send_buffer"`
	PingInterval    time.Duration `koanf:"ping_interval"`
	PingTimeout     time.Duration `koanf:"ping_timeout"`
	MaxLifetime     time.Duration `koanf:"max_lifetime"`
	OriginPatterns  []string      `koanf:"origin_patterns"`
}

// Observability — эксплуатационная наблюдаемость (Этап 6, O3; server/internal/observability, docs/observability.md).
type Observability struct {
	// MetricsListen — адрес внутреннего порта /metrics Prometheus (например 127.0.0.1:9090 или :9090 во внутренней
	// сети контейнеров). Отдельный HTTP-сервер, не публичный роутер: без аутентификации, наружу не публиковать.
	// Пусто — метрики не отдаются (по умолчанию).
	MetricsListen string `koanf:"metrics_listen"`
	// OTLPEndpoint — адрес OTLP/HTTP коллектора трасс OpenTelemetry (http(s)://host:4318). Пусто — трассировка
	// выключена (по умолчанию).
	OTLPEndpoint string `koanf:"otlp_endpoint"`
	// TraceSampleRatio — доля трасс, которые начинает этот инстанс (0..1).
	TraceSampleRatio float64 `koanf:"trace_sample_ratio"`
	// LogSampleInitial / LogSampleThereafter — выборка шумных записей журнала debug/info: за секунду по одному
	// сообщению — первые LogSampleInitial, затем каждая LogSampleThereafter-я. 0 — без выборки. warn/error не
	// отбрасываются.
	LogSampleInitial    int `koanf:"log_sample_initial"`
	LogSampleThereafter int `koanf:"log_sample_thereafter"`
}

// Dev — только для dev/test, в prod запрещено валидацией.
type Dev struct {
	// FixedNow — «сейчас» приложения (RFC 3339, например 2026-10-15T12:00:00+05:00) вместо системных часов:
	// визуальное сравнение со старым Laravel на одних демо-данных (tools/visual). Пусто — системные часы.
	// Время в SQL (now() в запросах), сроки токенов и сессий не меняются.
	FixedNow string `koanf:"fixed_now"`
	// DisableWorkers — фоновые задачи, которые этот инстанс не запускает (имена — app.workers, например
	// monitoring-flush). Только dev/test: в стенде tools/visual у старого Laravel нет планировщика, и экраны «Сервер»
	// и карточки клуба не должны набирать метрики запросов самого прогона снимков.
	DisableWorkers []string `koanf:"disable_workers"`
	// TOTPAllowReplay — второй фактор суперадмина принимает повтор кода того же шага (Этап 7). Только dev/test: стенд
	// tools/visual входит демо-суперадмином на каждый снимок чаще, чем раз в 30 с. Код и окно ±1 шаг проверяются всегда.
	TOTPAllowReplay bool `koanf:"totp_allow_replay"`
}

// Now — часы приложения: фиксированное время dev.fixed_now или time.Now. Вызывать после Validate.
func (c *Config) Now() func() time.Time {
	if c.Dev.FixedNow == "" {
		return time.Now
	}
	if t, err := time.Parse(time.RFC3339, c.Dev.FixedNow); err == nil {
		return func() time.Time { return t }
	}
	return time.Now
}

// Security — ключи шифрования данных (секреты вебхуков, токены Telegram; server/internal/secretbox).
type Security struct {
	// DataKeys — связка ключей шифрования секретов в базе (Q-050): первый шифрует, остальные только
	// расшифровывают (ротация: новый ключ — первым, воркер reencrypt-secrets перешифровывает строки, затем старый
	// убирают). Секрет ≥ 32 байт. В env — ZAFIRO_SECURITY_DATA_KEYS=kid:секрет,kid2:секрет2. Обязателен в prod;
	// в dev/test без значения — случайный (после перезапуска сохранённые секреты не читаются).
	DataKeys []SigningKey `koanf:"data_keys"`
}

// Mobile — старый REST мобильных приложений Flutter (server/internal/http/legacy, решение Q-058,
// docs/mobile-compat.md).
type Mobile struct {
	// Enabled — обслуживать маршруты приложений (/api/auth/*, /api/guest/*, /api/staff/*). По умолчанию включено.
	Enabled bool `koanf:"enabled"`
	// Upstream — адрес старого Laravel для клубов, которые ещё не переехали (поклубный переход): запрос с токеном,
	// которого нет на этом сервере, и вход по телефону без клубов здесь уходят туда. Пусто — все клубы здесь.
	Upstream string `koanf:"upstream"`
	// UpstreamHost — заголовок Host для Upstream (пусто — Host исходного запроса).
	UpstreamHost string `koanf:"upstream_host"`
	// TokenTTL — срок токена приложения (Sanctum в старом: 30 дней).
	TokenTTL time.Duration `koanf:"token_ttl"`
}

// Reseller — кабинет реселлера (тема Д, docs/upstream-2026-11.md).
type Reseller struct {
	// Domain — отдельный хост кабинета реселлера (RESELLER_DOMAIN старого проекта, config/app.php; в проде —
	// resseler.zafiro.kz, написание как в старом, решение UQ-21): кабинет отвечает только на нём (с корня), на нём же
	// входят только реселлеры, а панели клуба и регистрации нет. Пусто — кабинет по /reseller на любом хосте.
	// В dev — поддомен localhost (например resseler.localhost).
	Domain string `koanf:"domain"`
}

// Superadmin — кабинет суперадмина (Этап 3, группа 6).
type Superadmin struct {
	// Domain — отдельный хост кабинета (SUPERADMIN_DOMAIN старого проекта, решение Q-069): маршруты кабинета
	// отвечают только на нём (с корня), на нём же — вход только для суперадминов. Пусто — кабинет по /superadmin.
	// В dev — поддомен localhost (например admin.localhost).
	Domain string `koanf:"domain"`
	// SecretKey — ключ шифрования Этапа 3 (формат secretbox v1). Устарел: секреты шифрует security.data_keys,
	// этот ключ (если задан) только расшифровывает старые значения v1, воркер reencrypt-secrets перешифровывает их
	// текущим ключом данных; после этого ключ можно убрать из конфигурации. ≥ 32 байт.
	SecretKey Secret `koanf:"secret_key"`
	// BackupPath — каталог бэкапов (BACKUP_PATH): экран показывает время самого нового файла.
	BackupPath string `koanf:"backup_path"`
	// SSLHosts — домены для проверки сертификатов (MONITORING_SSL_HOSTS); проверка — Этап 4.
	SSLHosts []string `koanf:"ssl_hosts"`
}

// Workers — периодические задачи.
type Workers struct {
	ExpireSessions ExpireSessions `koanf:"expire_sessions"`
	Partitions     Partitions     `koanf:"partitions"`
	LogRetention   LogRetention   `koanf:"log_retention"`
	// LedgerReconcile — фоновая сверка балансов и кассы с журналом операций (worker.LedgerReconcile, аудит AC-3).
	LedgerReconcile LedgerReconcile `koanf:"ledger_reconcile"`
}

// LedgerReconcile — периоды фоновой сверки проекций денег с ledger. Сверка только находит и оповещает.
type LedgerReconcile struct {
	// Interval — частая сверка: клиенты и операции, изменившиеся с прошлой (по умолчанию час).
	Interval time.Duration `koanf:"interval"`
	// FullInterval — полная сверка: все проводки каждого клуба (по умолчанию сутки); не меньше Interval.
	FullInterval time.Duration `koanf:"full_interval"`
}

// LogRetention — удаление месячных партиций журналов старше срока хранения (worker.LogRetention).
// Сроки — у журналов (integrations.request_log_retention); ledger и audit_log не удаляются никогда.
type LogRetention struct {
	// Interval — как часто проверять (integrations:prune в старом — hourly).
	Interval time.Duration `koanf:"interval"`
}

// ExpireSessions — снятие истёкших сессий ПК (worker.ExpireSessions).
type ExpireSessions struct {
	Interval time.Duration `koanf:"interval"`
}

// Partitions — создание месячных партиций (db.EnsurePartitions, Q-081).
type Partitions struct {
	Interval time.Duration `koanf:"interval"`
	// Months — на сколько месяцев вперёд (включая текущий) должны существовать партиции.
	Months int `koanf:"months"`
}

// Standalone — режим без зависимостей: не задан database.url, работают только /healthz, /readyz и фронт.
// Для смоук-теста собранного бинарника в CI; в prod запрещён валидацией.
func (c *Config) Standalone() bool { return c.Database.URL.IsZero() }

// TrustedPrefixes разбирает TrustedProxies (IP или CIDR) в netip.Prefix.
func (h HTTP) TrustedPrefixes() ([]netip.Prefix, error) {
	return parsePrefixes("http.trusted_proxies", h.TrustedProxies)
}

// parsePrefixes — список IP или CIDR ключа name в netip.Prefix (IP — префикс /32 или /128).
func parsePrefixes(name string, list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, fmt.Errorf("%s: %q: %w", name, s, err)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %q: %w", name, s, err)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
