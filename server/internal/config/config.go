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
// auth.signing_keys — "kid:секрет,kid2:секрет2".
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
	Realtime        Realtime      `koanf:"realtime"`
	Workers         Workers       `koanf:"workers"`
	Superadmin      Superadmin    `koanf:"superadmin"`
	Dev             Dev           `koanf:"dev"`

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
}

// AgentPKI — CA агентов и серверный сертификат gRPC (server/internal/pki).
type AgentPKI struct {
	CACertFile string `koanf:"ca_cert_file"`
	// CAKeyFile — путь к ключу CA (сам путь не секрет, файл — секрет вне репозитория).
	CAKeyFile      string `koanf:"ca_key_file"`
	ServerCertFile string `koanf:"server_cert_file"`
	ServerKeyFile  string `koanf:"server_key_file"`
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

// Realtime — WebSocket админки (/ws).
type Realtime struct {
	MaxConnsPerUser int           `koanf:"max_conns_per_user"`
	SendBuffer      int           `koanf:"send_buffer"`
	PingInterval    time.Duration `koanf:"ping_interval"`
	PingTimeout     time.Duration `koanf:"ping_timeout"`
	MaxLifetime     time.Duration `koanf:"max_lifetime"`
	OriginPatterns  []string      `koanf:"origin_patterns"`
}

// Dev — только для dev/test, в prod запрещено валидацией.
type Dev struct {
	// FixedNow — «сейчас» приложения (RFC 3339, например 2026-10-15T12:00:00+05:00) вместо системных часов:
	// визуальное сравнение со старым Laravel на одних демо-данных (tools/visual). Пусто — системные часы.
	// Время в SQL (now() в запросах), сроки токенов и сессий не меняются.
	FixedNow string `koanf:"fixed_now"`
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

// Superadmin — кабинет суперадмина (Этап 3, группа 6).
type Superadmin struct {
	// Domain — отдельный хост кабинета (SUPERADMIN_DOMAIN старого проекта, решение Q-069): маршруты кабинета
	// отвечают только на нём (с корня), на нём же — вход только для суперадминов. Пусто — кабинет по /superadmin.
	// В dev — поддомен localhost (например admin.localhost).
	Domain string `koanf:"domain"`
	// SecretKey — ключ шифрования секретов платформы в базе (токены Telegram, как Crypt::encryptString старого
	// кода). ≥ 32 байт; в dev/test без значения — случайный (после перезапуска сохранённые токены не читаются).
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

		"agent.instance_id":                   "",
		"agent.grpc.listen":                   ":8443",
		"agent.grpc.endpoint":                 "localhost:8443",
		"agent.pki.ca_cert_file":              "",
		"agent.pki.ca_key_file":               "",
		"agent.pki.server_cert_file":          "",
		"agent.pki.server_key_file":           "",
		"agent.pki.dev_dir":                   "",
		"agent.pki.dev_hosts":                 []string{},
		"agent.enroll.code_pepper":            "",
		"agent.enroll.code_ttl":               "15m",
		"agent.enroll.diskless_cert_lifetime": "24h",
		"agent.update.public_keys":            []string{},
		"agent.cert.lifetime":                 "720h",
		"agent.cert_cache.refresh":            "5s",
		"agent.cert_cache.full_reload":        "10m",
		"agent.stream.presence_ttl":           "90s",
		"agent.stream.offline_grace":          "15s",
		"agent.stream.hello_timeout":          "10s",
		"agent.stream.status_interval":        "60s",
		"agent.stream.state_debounce":         "5s",
		"agent.stream.ack_timeout":            "30s",
		"agent.stream.key_prefix":             "zafiro:agent:",
		"agent.stream.subject_prefix":         "zafiro.agent",
		"agent.stream.drain_within":           "10s",
		"agent.guest.token_ttl":               "0s",
		"agent.guest.idempotency_ttl":         "0s",
		"agent.guest.qr_base_url":             "",
		"agent.guest.public_base_url":         "",
		"agent.guest.qr_poll_every":           "0s",
		"agent.sweeper.interval":              "5s",
		"agent.sweeper.reconcile_every":       "5m",
		"agent.sweeper.command_retention":     "168h",

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

		"realtime.max_conns_per_user": 5,
		"realtime.send_buffer":        64,
		"realtime.ping_interval":      "25s",
		"realtime.ping_timeout":       "10s",
		"realtime.max_lifetime":       "0s",
		"realtime.origin_patterns":    []string{},

		"workers.expire_sessions.interval": "1m",
		"workers.partitions.interval":      "24h",
		"workers.partitions.months":        3,

		"superadmin.domain":      "",
		"superadmin.secret_key":  "",
		"superadmin.backup_path": "",
		"superadmin.ssl_hosts":   []string{},

		"dev.fixed_now": "",
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
			if e.key == "auth.signing_keys" {
				keys, err := parseSigningKeys(v)
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
func parseSigningKeys(v string) ([]any, error) {
	out := []any{}
	for i, part := range splitList(v) {
		id, secret, ok := strings.Cut(part, ":")
		if !ok || id == "" || secret == "" {
			return nil, fmt.Errorf("auth.signing_keys: элемент %d: ожидается kid:секрет", i+1)
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
	if c.Superadmin.SecretKey.IsZero() {
		c.Superadmin.SecretKey = randomSecret(32)
		c.Generated = append(c.Generated, "superadmin.secret_key")
	}
}

var instanceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var keyIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,16}$`)

// updateKeyIDRe — key_id ключа подписи обновлений агента (UpdateAvailable.key_id, например "2026-10").
var updateKeyIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

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
	}
	for name, d := range positive {
		if d <= 0 {
			add("%s: должен быть > 0", name)
		}
	}
	if c.Agent.Stream.DrainWithin < 0 {
		add("agent.stream.drain_within: должен быть ≥ 0")
	}
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
	pki := c.Agent.PKI
	files := pki.CACertFile != "" || pki.CAKeyFile != "" || pki.ServerCertFile != "" || pki.ServerKeyFile != ""
	if files && (pki.CACertFile == "" || pki.CAKeyFile == "" || pki.ServerCertFile == "" || pki.ServerKeyFile == "") {
		add("agent.pki: нужны все четыре файла: ca_cert_file, ca_key_file, server_cert_file, server_key_file")
	}
	if c.Dev.FixedNow != "" {
		if prod {
			add("dev.fixed_now: запрещён в prod")
		} else if _, err := time.Parse(time.RFC3339, c.Dev.FixedNow); err != nil {
			add("dev.fixed_now: ожидается RFC 3339 (2026-10-15T12:00:00+05:00), получено %q", c.Dev.FixedNow)
		}
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
	if c.Realtime.MaxConnsPerUser < 1 || c.Realtime.SendBuffer < 1 {
		add("realtime.max_conns_per_user и realtime.send_buffer: должны быть ≥ 1")
	}
	if c.Workers.Partitions.Months < 1 || c.Workers.Partitions.Months > 24 {
		add("workers.partitions.months: ожидается 1..24")
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
	out := make([]netip.Prefix, 0, len(h.TrustedProxies))
	for _, s := range h.TrustedProxies {
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, fmt.Errorf("http.trusted_proxies: %q: %w", s, err)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("http.trusted_proxies: %q: %w", s, err)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// RedactURL убирает пароль из URL. Некорректный URL заменяется целиком.
func RedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return redacted
	}
	return u.Redacted()
}

// LogValue реализует slog.LogValuer: секреты замаскированы, у DSN виден только хост и база.
func (c *Config) LogValue() slog.Value {
	keyIDs := make([]string, 0, len(c.Auth.SigningKeys))
	for _, k := range c.Auth.SigningKeys {
		keyIDs = append(keyIDs, k.ID)
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
		slog.Group("agent",
			slog.String("instance_id", c.Agent.InstanceID),
			slog.String("grpc_listen", c.Agent.GRPC.Listen),
			slog.String("grpc_endpoint", c.Agent.GRPC.Endpoint),
			slog.String("pki_ca_cert_file", c.Agent.PKI.CACertFile),
			slog.String("pki_server_cert_file", c.Agent.PKI.ServerCertFile),
			slog.String("pki_dev_dir", c.Agent.PKI.DevDir),
			slog.Any("enroll_code_pepper", c.Agent.Enroll.CodePepper),
		),
		slog.Any("generated_secrets", c.Generated),
		slog.String("dev_fixed_now", c.Dev.FixedNow),
	)
}

// String не раскрывает секреты (на случай fmt.Print(cfg)).
func (c *Config) String() string {
	return c.LogValue().String()
}
