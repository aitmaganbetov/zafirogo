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
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// EnvPrefix — префикс переменных окружения.
const EnvPrefix = "ZAFIRO_"

// FileEnv — переменная окружения с путём к YAML-файлу конфигурации.
const FileEnv = EnvPrefix + "CONFIG_FILE"

const redacted = "[REDACTED]"

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
}

// Database — подключения к PostgreSQL. Два логина (db/migrations/0004_rls.sql):
// URL — член роли zafiro_app (RLS по клубу), PlatformURL — член роли zafiro_platform
// (фоновые задачи и суперадминка по всем клубам). Владелец схемы (миграции) серверу не нужен.
type Database struct {
	// URL — DSN pgx логина zafiro_app. Пусто — режим без зависимостей (только /healthz и фронт;
	// не для prod).
	URL Secret `koanf:"url"`
	// PlatformURL — DSN pgx логина zafiro_platform. Обязателен, если задан URL.
	PlatformURL Secret `koanf:"platform_url"`
}

// Valkey — подключение к Valkey.
type Valkey struct {
	Addr     string `koanf:"addr"`
	Password Secret `koanf:"password"`
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
}

// AgentUpdate — обновления агента. Решение «Обновления агента» (docs/DECISIONS.md): подпись Ed25519 обязательна,
// ключ подписи — только в CI-секретах. Серверу нужен лишь открытый ключ: перед рассылкой UpdateAvailable сервер
// проверяет подпись CI и не отправляет на ПК неподписанное (agent/commands.VerifyUpdate).
type AgentUpdate struct {
	// PublicKeys — открытые ключи Ed25519 "key_id:base64(32 байта)" (не секрет). Несколько — на время ротации.
	// Пусто — обновления не рассылаются. В env — через запятую: ZAFIRO_AGENT_UPDATE_PUBLIC_KEYS=2026-10:MCowBQ….
	PublicKeys []string `koanf:"public_keys"`
}

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

// Defaults возвращает значения по умолчанию (плоские ключи koanf). Каждый ключ конфигурации обязан быть
// здесь: по этому списку имена переменных окружения сопоставляются ключам.
func Defaults() map[string]any {
	return map[string]any{
		"env":                      "dev",
		"shutdown_timeout":         "30s",
		"http.addr":                ":8080",
		"http.read_header_timeout": "5s",
		"http.read_timeout":        "30s",
		"http.write_timeout":       "60s",
		"http.idle_timeout":        "120s",
		"http.request_timeout":     "30s",
		"http.max_header_bytes":    1 << 20,
		"http.trusted_proxies":     []string{},
		"http.wiki_host":           "",
		"database.url":             "",
		"database.platform_url":    "",
		"valkey.addr":              "localhost:6379",
		"valkey.password":          "",
		"nats.url":                 "nats://localhost:4222",
		"log.level":                "info",
		"log.format":               "json",

		"auth.signing_keys":        []any{},
		"auth.access_ttl":          "10m",
		"auth.idle_ttl":            "120m",
		"auth.remember_ttl":        "720h",
		"auth.max_lifetime":        "720h",
		"auth.refresh_reuse_grace": "10s",
		"auth.touch_every":         "1m",

		"password.memory_kib":     19456,
		"password.iterations":     2,
		"password.parallelism":    1,
		"password.max_concurrent": 0,

		"ratelimit.enable":    []string{},
		"ratelimit.disable":   []string{},
		"ratelimit.fail_open": false,

		"agent.instance_id":                       "",
		"agent.grpc.listen":                       ":8443",
		"agent.grpc.endpoint":                     "localhost:8443",
		"agent.grpc.proxy_trusted":                []string{},
		"agent.grpc.proxy_header_timeout":         "5s",
		"agent.pki.ca_cert_file":                  "",
		"agent.pki.ca_key_file":                   "",
		"agent.pki.server_cert_file":              "",
		"agent.pki.server_key_file":               "",
		"agent.pki.extra_trust_files":             []string{},
		"agent.pki.dev_dir":                       "",
		"agent.pki.dev_hosts":                     []string{},
		"agent.enroll.code_pepper":                "",
		"agent.enroll.code_ttl":                   "15m",
		"agent.enroll.diskless_cert_lifetime":     "24h",
		"agent.update.public_keys":                []string{},
		"agent.cert.lifetime":                     "720h",
		"agent.cert_cache.refresh":                "5s",
		"agent.cert_cache.full_reload":            "10m",
		"agent.stream.presence_ttl":               "90s",
		"agent.stream.offline_grace":              "15s",
		"agent.stream.hello_timeout":              "10s",
		"agent.stream.status_interval":            "60s",
		"agent.stream.state_debounce":             "5s",
		"agent.stream.ack_timeout":                "30s",
		"agent.stream.key_prefix":                 "zafiro:agent:",
		"agent.stream.subject_prefix":             "zafiro.agent",
		"agent.stream.drain_within":               "10s",
		"agent.stream.rebalance_enabled":          true,
		"agent.stream.rebalance_interval":         "15s",
		"agent.stream.rebalance_threshold":        1.2,
		"agent.stream.rebalance_min_excess":       50,
		"agent.stream.rebalance_batch":            100,
		"agent.stream.rebalance_reconnect_within": "3s",
		"agent.guest.token_ttl":                   "0s",
		"agent.guest.idempotency_ttl":             "0s",
		"agent.guest.qr_base_url":                 "",
		"agent.guest.public_base_url":             "",
		"agent.guest.qr_poll_every":               "0s",
		"agent.sweeper.interval":                  "5s",
		"agent.sweeper.reconcile_every":           "5m",
		"agent.sweeper.command_retention":         "168h",
		"agent.health.enabled":                    true,
		"agent.health.cpu_temp_c":                 90,
		"agent.health.gpu_temp_c":                 85,
		"agent.health.temp_hysteresis_c":          5,
		"agent.health.system_disk_free_percent":   10,
		"agent.health.system_disk_free_gb":        10,
		"agent.health.other_disk_free_percent":    5,
		"agent.health.link_full_mbps":             1000,
		"agent.health.link_min_mbps":              100,
		"agent.health.unstable_shutdowns":         3,
		"agent.health.unstable_bugchecks":         1,
		"agent.health.raise_after":                "2m",
		"agent.health.clear_after":                "5m",
		"agent.health.event_retention":            "2160h",
		"agent.health.live_min_gap":               "15s",
		"agent.health.live_load_step_percent":     5,
		"agent.health.live_temp_step_c":           2,
		"agent.health.ping_interval":              "60s",

		"events.stream.max_age":    "72h",
		"events.stream.duplicates": "10m",
		"events.stream.replicas":   1,

		"outbox.batch_size": 100,
		"outbox.interval":   "2s",
		"outbox.retention":  "24h",

		"webhooks.require_https":       true,
		"webhooks.allow_private_hosts": false,
		"webhooks.timeout":             "5s",
		"webhooks.connect_timeout":     "3s",
		"webhooks.backoff":             []string{"10s", "60s", "300s", "1800s", "7200s"},
		"webhooks.disable_after":       20,
		"webhooks.workers":             8,
		"webhooks.poll_interval":       "5s",

		"integrations.key_rate_limit":         120,
		"integrations.key_rate_limit_max":     1000,
		"integrations.club_rate_limit":        600,
		"integrations.failed_auth_per_minute": 20,
		"integrations.max_request_kb":         256,
		"integrations.max_bulk_items":         50,
		"integrations.idempotency_retention":  "24h",
		"integrations.request_log_retention":  "2160h",
		"integrations.legacy_upstream":        "",
		"integrations.legacy_upstream_host":   "",

		"realtime.max_conns_per_user": 5,
		"realtime.send_buffer":        64,
		"realtime.ping_interval":      "25s",
		"realtime.ping_timeout":       "10s",
		"realtime.max_lifetime":       "0s",
		"realtime.origin_patterns":    []string{},

		"workers.expire_sessions.interval": "1m",
		"workers.partitions.interval":      "24h",
		"workers.partitions.months":        3,
		"workers.log_retention.interval":   "1h",

		"security.data_keys": []any{},

		"mobile.enabled":       true,
		"mobile.upstream":      "",
		"mobile.upstream_host": "",
		"mobile.token_ttl":     "720h",

		"superadmin.domain":      "",
		"superadmin.secret_key":  "",
		"superadmin.backup_path": "",
		"superadmin.ssl_hosts":   []string{},

		"observability.metrics_listen":        "",
		"observability.otlp_endpoint":         "",
		"observability.trace_sample_ratio":    0.1,
		"observability.log_sample_initial":    100,
		"observability.log_sample_thereafter": 100,

		"dev.fixed_now":         "",
		"dev.disable_workers":   []string{},
		"dev.totp_allow_replay": false,
	}
}

// envKeys — ZAFIRO_<КЛЮЧ> → ключ koanf и признак списка.
type envKey struct {
	key  string
	list bool
}

func envIndex() map[string]envKey {
	idx := map[string]envKey{}
	for k, v := range Defaults() {
		name := strings.ToUpper(strings.ReplaceAll(k, ".", "_"))
		_, list := v.([]string)
		idx[name] = envKey{key: k, list: list}
	}
	return idx
}

// Load собирает конфигурацию. path — необязательный путь к YAML-файлу
// (если пуст, берётся из ZAFIRO_CONFIG_FILE). environ — источник окружения
// (nil — os.Environ), нужен для тестов.
func Load(path string, environ func() []string) (*Config, error) {
	if environ == nil {
		environ = os.Environ
	}

	k := koanf.New(".")
	if err := k.Load(confmap.Provider(Defaults(), "."), nil); err != nil {
		return nil, fmt.Errorf("config: defaults: %w", err)
	}

	if path == "" {
		path = lookup(environ, FileEnv)
	}
	if path != "" {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("config: file %q: %w", path, err)
		}
	}

	idx := envIndex()
	var envErr error
	envProvider := env.Provider(".", env.Opt{
		Prefix: EnvPrefix,
		TransformFunc: func(name, v string) (string, any) {
			e, ok := idx[strings.TrimPrefix(name, EnvPrefix)]
			if !ok {
				return "", nil // неизвестная переменная (в т.ч. ZAFIRO_CONFIG_FILE, ZAFIRO_TEST_*)
			}
			if e.key == "auth.signing_keys" || e.key == "security.data_keys" {
				keys, err := parseSigningKeys(e.key, v)
				if err != nil {
					envErr = err
					return "", nil
				}
				return e.key, keys
			}
			if e.list {
				return e.key, splitList(v)
			}
			return e.key, v
		},
		EnvironFunc: environ,
	})
	if err := k.Load(envProvider, nil); err != nil {
		return nil, fmt.Errorf("config: env: %w", err)
	}
	if envErr != nil {
		return nil, fmt.Errorf("config: %w", envErr)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("config: decode: %w", err)
	}
	cfg.applyDerived()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// parseSigningKeys разбирает "kid:секрет,kid2:секрет2". Текст ошибки не содержит секретов.
func parseSigningKeys(name, v string) ([]any, error) {
	out := []any{}
	for i, part := range splitList(v) {
		id, secret, ok := strings.Cut(part, ":")
		if !ok || id == "" || secret == "" {
			return nil, fmt.Errorf("%s: элемент %d: ожидается kid:секрет", name, i+1)
		}
		out = append(out, map[string]any{"id": id, "secret": secret})
	}
	return out, nil
}

// applyDerived заполняет значения, зависящие от окружения: id инстанса, dev-PKI и случайные секреты
// для dev/test.
func (c *Config) applyDerived() {
	if c.Agent.InstanceID == "" {
		c.Agent.InstanceID = defaultInstanceID()
	}
	if c.Env == "prod" {
		return
	}
	if c.Agent.PKI.DevDir == "" && c.Agent.PKI.CACertFile == "" && c.Agent.PKI.ServerCertFile == "" {
		c.Agent.PKI.DevDir = DevPKIDir
	}
	if len(c.Auth.SigningKeys) == 0 {
		c.Auth.SigningKeys = []SigningKey{{ID: "dev", Secret: randomSecret(32)}}
		c.Generated = append(c.Generated, "auth.signing_keys")
	}
	if c.Agent.Enroll.CodePepper.IsZero() {
		c.Agent.Enroll.CodePepper = randomSecret(32)
		c.Generated = append(c.Generated, "agent.enroll.code_pepper")
	}
	if len(c.Security.DataKeys) == 0 {
		c.Security.DataKeys = []SigningKey{{ID: "dev", Secret: randomSecret(32)}}
		c.Generated = append(c.Generated, "security.data_keys")
	}
}

var instanceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var keyIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,16}$`)

// updateKeyIDRe — key_id ключа подписи обновлений агента (UpdateAvailable.key_id, например "2026-10").
var updateKeyIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// validateHealth — agent.health.*: пороги в разумных пределах (опечатка не должна ни засыпать историю ложными
// проблемами, ни выключить их молча).
func (c *Config) validateHealth(add func(format string, args ...any)) {
	h := c.Agent.Health
	if h.CPUTempC < 40 || h.CPUTempC > 130 || h.GPUTempC < 40 || h.GPUTempC > 130 {
		add("agent.health.cpu_temp_c, gpu_temp_c: ожидается 40..130 °C")
	}
	if h.TempHysteresisC < 0 || h.TempHysteresisC > 30 {
		add("agent.health.temp_hysteresis_c: ожидается 0..30 °C")
	}
	if h.SystemDiskFreePercent > 90 || h.OtherDiskFreePercent > 90 || h.SystemDiskFreeGB > 1024 {
		add("agent.health.*_disk_free_percent: ожидается 0..90; system_disk_free_gb: 0..1024")
	}
	if h.LinkMinMbps > h.LinkFullMbps || h.LinkFullMbps > 100000 {
		add("agent.health.link_min_mbps: должен быть ≤ link_full_mbps (≤ 100000)")
	}
	if h.RaiseAfter < 0 || h.ClearAfter < 0 || h.RaiseAfter > 24*time.Hour || h.ClearAfter > 24*time.Hour {
		add("agent.health.raise_after, clear_after: ожидается 0..24h")
	}
	if h.EventRetention < 24*time.Hour {
		add("agent.health.event_retention: должен быть ≥ 24h")
	}
	if h.LiveMinGap < time.Second || h.LiveLoadStepPercent < 1 || h.LiveLoadStepPercent > 100 || h.LiveTempStepC < 1 {
		add("agent.health.live_min_gap ≥ 1s, live_load_step_percent 1..100, live_temp_step_c ≥ 1")
	}
	if h.PingInterval < 0 || (h.PingInterval > 0 && h.PingInterval < 5*time.Second) {
		add("agent.health.ping_interval: 0 (не мерить) или ≥ 5s")
	}
}

// validateRebalance — agent.stream.rebalance_* (проверяются и при выключенной перебалансировке: включить её
// можно одной переменной, и ошибка не должна всплыть только тогда).
func (c *Config) validateRebalance(add func(format string, args ...any)) {
	st := c.Agent.Stream
	if st.RebalanceInterval < time.Second {
		add("agent.stream.rebalance_interval: должен быть ≥ 1s")
	}
	if !(st.RebalanceThreshold >= 1.05 && st.RebalanceThreshold <= 10) { // !(…) ловит и NaN
		add("agent.stream.rebalance_threshold: ожидается 1.05..10 (доля от среднего числа потоков)")
	}
	if st.RebalanceMinExcess < 1 {
		add("agent.stream.rebalance_min_excess: должен быть ≥ 1")
	}
	if st.RebalanceBatch < 1 || st.RebalanceBatch > 10000 {
		add("agent.stream.rebalance_batch: ожидается 1..10000")
	}
	// ПК в окне переподключения не должен успеть стать «офлайн» (offline_grace) и вернуться до следующего решения.
	if st.RebalanceReconnectWithin <= 0 || st.RebalanceReconnectWithin >= st.OfflineGrace ||
		st.RebalanceReconnectWithin >= st.RebalanceInterval {
		add("agent.stream.rebalance_reconnect_within: должен быть > 0 и меньше agent.stream.offline_grace и agent.stream.rebalance_interval")
	}
}

// defaultInstanceID — имя хоста, приведённое к [A-Za-z0-9_-]{1,64}.
func defaultInstanceID() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		h = "zafiro"
	}
	b := []byte(h)
	for i, ch := range b {
		ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-'
		if !ok {
			b[i] = '-'
		}
	}
	if len(b) > 64 {
		b = b[:64]
	}
	return string(b)
}

func lookup(environ func() []string, key string) string {
	for _, kv := range environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func splitList(v string) []string {
	out := []string{}
	for p := range strings.SplitSeq(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Standalone — режим без зависимостей: не задан database.url, работают только /healthz, /readyz и фронт.
// Для смоук-теста собранного бинарника в CI; в prod запрещён валидацией.
func (c *Config) Standalone() bool { return c.Database.URL.IsZero() }

// Validate проверяет значения. Сообщения об ошибках не содержат секретов.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	prod := c.Env == "prod"
	switch c.Env {
	case "dev", "test", "prod":
	default:
		add("env: ожидается dev|test|prod, получено %q", c.Env)
	}
	if c.ShutdownTimeout <= 0 {
		add("shutdown_timeout: должен быть > 0")
	}
	if c.HTTP.Addr == "" {
		add("http.addr: пусто")
	} else if _, _, err := net.SplitHostPort(c.HTTP.Addr); err != nil {
		add("http.addr: %w", err)
	}
	positive := map[string]time.Duration{
		"http.read_header_timeout":            c.HTTP.ReadHeaderTimeout,
		"http.read_timeout":                   c.HTTP.ReadTimeout,
		"http.write_timeout":                  c.HTTP.WriteTimeout,
		"http.idle_timeout":                   c.HTTP.IdleTimeout,
		"http.request_timeout":                c.HTTP.RequestTimeout,
		"auth.access_ttl":                     c.Auth.AccessTTL,
		"auth.idle_ttl":                       c.Auth.IdleTTL,
		"auth.remember_ttl":                   c.Auth.RememberTTL,
		"auth.max_lifetime":                   c.Auth.MaxLifetime,
		"auth.refresh_reuse_grace":            c.Auth.RefreshReuseGrace,
		"auth.touch_every":                    c.Auth.TouchEvery,
		"agent.enroll.code_ttl":               c.Agent.Enroll.CodeTTL,
		"agent.grpc.proxy_header_timeout":     c.Agent.GRPC.ProxyHeaderTimeout,
		"agent.enroll.diskless_cert_lifetime": c.Agent.Enroll.DisklessCertLifetime,
		"agent.cert.lifetime":                 c.Agent.Cert.Lifetime,
		"agent.cert_cache.refresh":            c.Agent.CertCache.Refresh,
		"agent.cert_cache.full_reload":        c.Agent.CertCache.FullReload,
		"agent.stream.presence_ttl":           c.Agent.Stream.PresenceTTL,
		"agent.stream.offline_grace":          c.Agent.Stream.OfflineGrace,
		"agent.stream.hello_timeout":          c.Agent.Stream.HelloTimeout,
		"agent.stream.status_interval":        c.Agent.Stream.StatusInterval,
		"agent.stream.state_debounce":         c.Agent.Stream.StateDebounce,
		"agent.stream.ack_timeout":            c.Agent.Stream.AckTimeout,
		"agent.sweeper.interval":              c.Agent.Sweeper.Interval,
		"agent.sweeper.reconcile_every":       c.Agent.Sweeper.ReconcileEvery,
		"agent.sweeper.command_retention":     c.Agent.Sweeper.CommandRetention,
		"events.stream.max_age":               c.Events.Stream.MaxAge,
		"events.stream.duplicates":            c.Events.Stream.Duplicates,
		"outbox.interval":                     c.Outbox.Interval,
		"outbox.retention":                    c.Outbox.Retention,
		"webhooks.timeout":                    c.Webhooks.Timeout,
		"webhooks.connect_timeout":            c.Webhooks.ConnectTimeout,
		"webhooks.poll_interval":              c.Webhooks.PollInterval,
		"realtime.ping_interval":              c.Realtime.PingInterval,
		"realtime.ping_timeout":               c.Realtime.PingTimeout,
		"workers.expire_sessions.interval":    c.Workers.ExpireSessions.Interval,
		"workers.partitions.interval":         c.Workers.Partitions.Interval,
		"workers.log_retention.interval":      c.Workers.LogRetention.Interval,
		"integrations.request_log_retention":  c.Integrations.RequestLogRetention,
	}
	for name, d := range positive {
		if d <= 0 {
			add("%s: должен быть > 0", name)
		}
	}
	if c.Agent.Stream.DrainWithin < 0 {
		add("agent.stream.drain_within: должен быть ≥ 0")
	}
	c.validateRebalance(add)
	c.validateHealth(add)
	if c.Realtime.MaxLifetime < 0 {
		add("realtime.max_lifetime: должен быть ≥ 0")
	}
	if c.Auth.AccessTTL > 0 && c.Auth.IdleTTL > 0 && c.Auth.AccessTTL > c.Auth.IdleTTL {
		add("auth.access_ttl: должен быть ≤ auth.idle_ttl")
	}
	if c.HTTP.MaxHeaderBytes <= 0 {
		add("http.max_header_bytes: должен быть > 0")
	}
	if _, err := c.HTTP.TrustedPrefixes(); err != nil {
		errs = append(errs, err)
	}

	// База: два DSN (zafiro_app и zafiro_platform).
	if c.Database.URL.IsZero() {
		if prod {
			add("database.url: обязателен в prod")
		}
	} else {
		if !validPostgresURL(c.Database.URL) {
			// Не включаем ошибку разбора: она может содержать DSN с паролем.
			add("database.url: ожидается postgres://… или postgresql://…")
		}
		if c.Database.PlatformURL.IsZero() {
			add("database.platform_url: обязателен вместе с database.url (логин роли zafiro_platform)")
		} else if !validPostgresURL(c.Database.PlatformURL) {
			add("database.platform_url: ожидается postgres://… или postgresql://…")
		} else if c.Database.PlatformURL == c.Database.URL {
			add("database.platform_url: должен быть другим логином, чем database.url (роли zafiro_platform и zafiro_app)")
		}
	}
	if c.Valkey.Addr == "" {
		add("valkey.addr: пусто")
	} else if _, _, err := net.SplitHostPort(c.Valkey.Addr); err != nil {
		add("valkey.addr: %w", err)
	}
	if !c.NATS.URL.IsZero() {
		if u, err := url.Parse(c.NATS.URL.Reveal()); err != nil || u.Host == "" {
			add("nats.url: некорректный URL")
		}
	} else if !c.Standalone() {
		add("nats.url: пусто")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level: ожидается debug|info|warn|error, получено %q", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add("log.format: ожидается json|text, получено %q", c.Log.Format)
	}

	// Авторизация.
	if len(c.Auth.SigningKeys) == 0 {
		add("auth.signing_keys: нужен хотя бы один ключ (ZAFIRO_AUTH_SIGNING_KEYS=kid:секрет)")
	}
	seen := map[string]bool{}
	for i, k := range c.Auth.SigningKeys {
		if !keyIDRe.MatchString(k.ID) {
			add("auth.signing_keys[%d].id: ожидается [A-Za-z0-9_-]{1,16}", i)
		}
		if seen[k.ID] {
			add("auth.signing_keys[%d].id: повтор %q", i, k.ID)
		}
		seen[k.ID] = true
		if len(k.Secret.Reveal()) < 32 {
			add("auth.signing_keys[%d].secret: нужно не меньше 32 байт", i)
		}
	}
	if c.Password.Iterations < 1 {
		add("password.iterations: должен быть ≥ 1")
	}
	if c.Password.Parallelism < 1 {
		add("password.parallelism: должен быть ≥ 1")
	}
	if c.Password.MemoryKiB < 8*uint32(c.Password.Parallelism) {
		add("password.memory_kib: должен быть ≥ 8 × parallelism")
	}
	if c.Password.MaxConcurrent < 0 {
		add("password.max_concurrent: должен быть ≥ 0")
	}

	// Агент.
	if !instanceRe.MatchString(c.Agent.InstanceID) {
		add("agent.instance_id: ожидается [A-Za-z0-9_-]{1,64}, получено %q", c.Agent.InstanceID)
	}
	if c.Agent.GRPC.Listen == "" {
		add("agent.grpc.listen: пусто")
	} else if _, _, err := net.SplitHostPort(c.Agent.GRPC.Listen); err != nil {
		add("agent.grpc.listen: %w", err)
	}
	if c.Agent.GRPC.Endpoint == "" {
		add("agent.grpc.endpoint: пусто")
	}
	if _, err := c.Agent.GRPC.ProxyTrustedPrefixes(); err != nil {
		errs = append(errs, err)
	}
	pki := c.Agent.PKI
	files := pki.CACertFile != "" || pki.CAKeyFile != "" || pki.ServerCertFile != "" || pki.ServerKeyFile != ""
	if files && (pki.CACertFile == "" || pki.CAKeyFile == "" || pki.ServerCertFile == "" || pki.ServerKeyFile == "") {
		add("agent.pki: нужны все четыре файла: ca_cert_file, ca_key_file, server_cert_file, server_key_file")
	}
	for _, f := range pki.ExtraTrustFiles {
		if strings.TrimSpace(f) == "" {
			add("agent.pki.extra_trust_files: пустой путь")
		} else if f == pki.CACertFile {
			add("agent.pki.extra_trust_files: совпадает с ca_cert_file (там прежний CA, а не текущий)")
		}
	}
	if c.Dev.FixedNow != "" {
		if prod {
			add("dev.fixed_now: запрещён в prod")
		} else if _, err := time.Parse(time.RFC3339, c.Dev.FixedNow); err != nil {
			add("dev.fixed_now: ожидается RFC 3339 (2026-10-15T12:00:00+05:00), получено %q", c.Dev.FixedNow)
		}
	}
	if prod && len(c.Dev.DisableWorkers) > 0 {
		add("dev.disable_workers: запрещён в prod")
	}
	if prod && c.Dev.TOTPAllowReplay {
		add("dev.totp_allow_replay: запрещён в prod")
	}
	if prod && pki.DevDir != "" {
		add("agent.pki.dev_dir: запрещён в prod — задайте файлы CA и серверного сертификата")
	}
	if prod && !files {
		add("agent.pki: в prod нужны ca_cert_file, ca_key_file, server_cert_file, server_key_file")
	}
	if len(c.Agent.Enroll.CodePepper.Reveal()) < 32 {
		add("agent.enroll.code_pepper: нужно не меньше 32 байт")
	}
	if c.Agent.Enroll.DisklessCertLifetime > 0 && c.Agent.Cert.Lifetime > 0 && c.Agent.Enroll.DisklessCertLifetime > c.Agent.Cert.Lifetime {
		add("agent.enroll.diskless_cert_lifetime: должен быть ≤ agent.cert.lifetime")
	}
	if k := c.Superadmin.SecretKey; !k.IsZero() && len(k.Reveal()) < 32 {
		add("superadmin.secret_key: нужно не меньше 32 байт")
	}
	if len(c.Security.DataKeys) == 0 {
		add("security.data_keys: нужен хотя бы один ключ (ZAFIRO_SECURITY_DATA_KEYS=kid:секрет)")
	}
	seenData := map[string]bool{}
	for i, k := range c.Security.DataKeys {
		if !keyIDRe.MatchString(k.ID) {
			add("security.data_keys[%d].id: ожидается [A-Za-z0-9_-]{1,16}", i)
		}
		if seenData[k.ID] {
			add("security.data_keys[%d].id: повтор %q", i, k.ID)
		}
		seenData[k.ID] = true
		if len(k.Secret.Reveal()) < 32 {
			add("security.data_keys[%d].secret: нужно не меньше 32 байт", i)
		}
	}
	if d := c.Superadmin.Domain; d != "" && (strings.ContainsAny(d, "/:@ ") || strings.HasPrefix(d, ".")) {
		add("superadmin.domain: ожидается имя хоста без схемы и порта, получено %q", d)
	}
	if _, err := c.Agent.Update.UpdatePublicKeys(); err != nil {
		errs = append(errs, err)
	}
	if c.Agent.Guest.TokenTTL < 0 || c.Agent.Guest.IdempotencyTTL < 0 || c.Agent.Guest.QRPollEvery < 0 {
		add("agent.guest.*: длительности должны быть ≥ 0")
	}
	for name, raw := range map[string]string{"agent.guest.qr_base_url": c.Agent.Guest.QRBaseURL, "agent.guest.public_base_url": c.Agent.Guest.PublicBaseURL} {
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			add("%s: ожидается http(s)://хост", name)
		}
	}
	for name, raw := range map[string]string{"mobile.upstream": c.Mobile.Upstream, "integrations.legacy_upstream": c.Integrations.LegacyUpstream} {
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			add("%s: ожидается http(s)://хост", name)
		}
	}
	if c.Mobile.TokenTTL <= 0 {
		add("mobile.token_ttl: должен быть > 0")
	}
	if c.Agent.Stream.KeyPrefix == "" || c.Agent.Stream.SubjectPrefix == "" {
		add("agent.stream.key_prefix и agent.stream.subject_prefix: не должны быть пустыми")
	}

	// События и вебхуки.
	if c.Events.Stream.Replicas < 1 || c.Events.Stream.Replicas > 5 {
		add("events.stream.replicas: ожидается 1..5")
	}
	if c.Outbox.BatchSize < 1 {
		add("outbox.batch_size: должен быть ≥ 1")
	}
	if len(c.Webhooks.Backoff) == 0 {
		add("webhooks.backoff: пусто")
	}
	for i, d := range c.Webhooks.Backoff {
		if d <= 0 {
			add("webhooks.backoff[%d]: должен быть > 0", i)
		}
	}
	if c.Webhooks.DisableAfter < 1 || c.Webhooks.Workers < 1 {
		add("webhooks.disable_after и webhooks.workers: должны быть ≥ 1")
	}
	if i := c.Integrations; i.KeyRateLimit < 1 || i.KeyRateLimitMax < 1 || i.ClubRateLimit < 1 || i.FailedAuthPerMinute < 1 ||
		i.MaxRequestKB < 1 || i.MaxBulkItems < 1 || i.IdempotencyRetention <= 0 {
		add("integrations.*: лимиты должны быть ≥ 1, idempotency_retention > 0")
	}
	if c.Realtime.MaxConnsPerUser < 1 || c.Realtime.SendBuffer < 1 {
		add("realtime.max_conns_per_user и realtime.send_buffer: должны быть ≥ 1")
	}
	if c.Workers.Partitions.Months < 1 || c.Workers.Partitions.Months > 24 {
		add("workers.partitions.months: ожидается 1..24")
	}
	if ob := c.Observability; ob.MetricsListen != "" {
		if _, port, err := net.SplitHostPort(ob.MetricsListen); err != nil {
			add("observability.metrics_listen: %w", err)
		} else if port != "0" && (ob.MetricsListen == c.HTTP.Addr || ob.MetricsListen == c.Agent.GRPC.Listen) {
			add("observability.metrics_listen: должен отличаться от http.addr и agent.grpc.listen")
		}
	}
	if ep := c.Observability.OTLPEndpoint; ep != "" {
		if u, err := url.Parse(ep); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			add("observability.otlp_endpoint: ожидается http(s)://host:port без логина и пароля")
		}
	}
	if r := c.Observability.TraceSampleRatio; r < 0 || r > 1 {
		add("observability.trace_sample_ratio: ожидается 0..1")
	}
	if c.Observability.LogSampleInitial < 0 || c.Observability.LogSampleThereafter < 0 {
		add("observability.log_sample_*: должны быть ≥ 0")
	}

	if len(errs) > 0 {
		return fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return nil
}

func validPostgresURL(s Secret) bool {
	u, err := url.Parse(s.Reveal())
	return err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql")
}

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

// RedactURL убирает пароль из URL. Некорректный URL заменяется целиком. Пароль и ключи бывают и в параметрах
// (pgx: postgres://host/db?password=…&sslpassword=…), их значения тоже маскируются (SEC-021).
func RedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return redacted
	}
	if u.RawQuery != "" {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return redacted
		}
		for k := range q {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "pass") || strings.Contains(lk, "secret") || strings.Contains(lk, "token") ||
				strings.Contains(lk, "key") {
				q[k] = []string{"xxxxx"}
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.Redacted()
}

// LogValue реализует slog.LogValuer: секреты замаскированы, у DSN виден только хост и база.
func (c *Config) LogValue() slog.Value {
	keyIDs := make([]string, 0, len(c.Auth.SigningKeys))
	for _, k := range c.Auth.SigningKeys {
		keyIDs = append(keyIDs, k.ID)
	}
	dataKeyIDs := make([]string, 0, len(c.Security.DataKeys))
	for _, k := range c.Security.DataKeys {
		dataKeyIDs = append(dataKeyIDs, k.ID)
	}
	return slog.GroupValue(
		slog.String("env", c.Env),
		slog.Duration("shutdown_timeout", c.ShutdownTimeout),
		slog.Group("http",
			slog.String("addr", c.HTTP.Addr),
			slog.Duration("read_header_timeout", c.HTTP.ReadHeaderTimeout),
			slog.Duration("request_timeout", c.HTTP.RequestTimeout),
			slog.Any("trusted_proxies", c.HTTP.TrustedProxies),
		),
		slog.String("database_url", RedactURL(c.Database.URL.Reveal())),
		slog.String("database_platform_url", RedactURL(c.Database.PlatformURL.Reveal())),
		slog.String("valkey_addr", c.Valkey.Addr),
		slog.Any("valkey_password", c.Valkey.Password),
		slog.String("nats_url", RedactURL(c.NATS.URL.Reveal())),
		slog.String("log_level", c.Log.Level),
		slog.String("log_format", c.Log.Format),
		slog.Any("auth_signing_key_ids", keyIDs),
		slog.Any("security_data_key_ids", dataKeyIDs),
		slog.Group("agent",
			slog.String("instance_id", c.Agent.InstanceID),
			slog.String("grpc_listen", c.Agent.GRPC.Listen),
			slog.String("grpc_endpoint", c.Agent.GRPC.Endpoint),
			slog.Any("grpc_proxy_trusted", c.Agent.GRPC.ProxyTrusted),
			slog.String("pki_ca_cert_file", c.Agent.PKI.CACertFile),
			slog.String("pki_server_cert_file", c.Agent.PKI.ServerCertFile),
			slog.Any("pki_extra_trust_files", c.Agent.PKI.ExtraTrustFiles),
			slog.String("pki_dev_dir", c.Agent.PKI.DevDir),
			slog.Any("enroll_code_pepper", c.Agent.Enroll.CodePepper),
		),
		slog.Any("generated_secrets", c.Generated),
		slog.String("observability_metrics_listen", c.Observability.MetricsListen),
		slog.String("observability_otlp_endpoint", RedactURL(c.Observability.OTLPEndpoint)),
		slog.String("dev_fixed_now", c.Dev.FixedNow),
		slog.Any("dev_disable_workers", c.Dev.DisableWorkers),
		slog.Bool("dev_totp_allow_replay", c.Dev.TOTPAllowReplay),
	)
}

// String не раскрывает секреты (на случай fmt.Print(cfg)).
func (c *Config) String() string {
	return c.LogValue().String()
}
