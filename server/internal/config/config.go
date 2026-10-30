// Package config загружает конфигурацию сервера zafiro.
//
// Источники (каждый следующий перекрывает предыдущий):
//  1. значения по умолчанию (Defaults);
//  2. необязательный YAML-файл (путь из флага -config или ZAFIRO_CONFIG_FILE);
//  3. переменные окружения с префиксом ZAFIRO_.
//
// Имена переменных: ZAFIRO_<СЕКЦИЯ>_<КЛЮЧ>, например ZAFIRO_HTTP_ADDR → http.addr,
// ZAFIRO_DATABASE_URL → database.url. Ключи верхнего уровня без секции
// (ZAFIRO_ENV, ZAFIRO_SHUTDOWN_TIMEOUT) отображаются как есть.
//
// Секреты (пароль в DATABASE_URL, пароль Valkey) никогда не попадают в логи:
// Config реализует slog.LogValuer и отдаёт только замаскированные значения.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
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

// sections — секции, у которых первый "_" в имени переменной окружения означает вложенность.
var sections = []string{"http", "database", "valkey", "nats", "log"}

// Config — полная конфигурация процесса.
type Config struct {
	// Env — окружение: dev | test | prod.
	Env             string        `koanf:"env"`
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
	HTTP            HTTP          `koanf:"http"`
	Database        Database      `koanf:"database"`
	Valkey          Valkey        `koanf:"valkey"`
	NATS            NATS          `koanf:"nats"`
	Log             Log           `koanf:"log"`
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
}

// Database — подключение к PostgreSQL.
type Database struct {
	// URL — DSN pgx, например postgres://user:pass@localhost:5432/zafiro?sslmode=disable.
	// Пусто — база не используется (readyz её не проверяет).
	URL string `koanf:"url"`
}

// Valkey — подключение к Valkey.
type Valkey struct {
	Addr     string `koanf:"addr"`
	Password string `koanf:"password"`
}

// NATS — подключение к NATS JetStream.
type NATS struct {
	URL string `koanf:"url"`
}

// Log — настройки журналирования.
type Log struct {
	// Level — debug | info | warn | error.
	Level string `koanf:"level"`
	// Format — json | text.
	Format string `koanf:"format"`
}

// Defaults возвращает значения по умолчанию (плоские ключи koanf).
func Defaults() map[string]any {
	return map[string]any{
		"env":                      "dev",
		"shutdown_timeout":         "15s",
		"http.addr":                ":8080",
		"http.read_header_timeout": "5s",
		"http.read_timeout":        "30s",
		"http.write_timeout":       "60s",
		"http.idle_timeout":        "120s",
		"http.request_timeout":     "30s",
		"http.max_header_bytes":    1 << 20,
		"http.trusted_proxies":     []string{},
		"database.url":             "",
		"valkey.addr":              "localhost:6379",
		"valkey.password":          "",
		"nats.url":                 "nats://localhost:4222",
		"log.level":                "info",
		"log.format":               "json",
	}
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

	envProvider := env.Provider(".", env.Opt{
		Prefix:        EnvPrefix,
		TransformFunc: transformEnv,
		EnvironFunc:   environ,
	})
	if err := k.Load(envProvider, nil); err != nil {
		return nil, fmt.Errorf("config: env: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("config: decode: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func lookup(environ func() []string, key string) string {
	for _, kv := range environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// transformEnv переводит ZAFIRO_HTTP_READ_TIMEOUT → http.read_timeout.
func transformEnv(k, v string) (string, any) {
	key := strings.ToLower(strings.TrimPrefix(k, EnvPrefix))
	if key == "config_file" {
		return "", nil
	}
	for _, s := range sections {
		if rest, ok := strings.CutPrefix(key, s+"_"); ok {
			key = s + "." + rest
			break
		}
	}
	if key == "http.trusted_proxies" {
		return key, splitList(v)
	}
	return key, v
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

// Validate проверяет значения. Сообщения об ошибках не содержат секретов.
func (c *Config) Validate() error {
	var errs []error
	switch c.Env {
	case "dev", "test", "prod":
	default:
		errs = append(errs, fmt.Errorf("env: ожидается dev|test|prod, получено %q", c.Env))
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("shutdown_timeout: должен быть > 0"))
	}
	if c.HTTP.Addr == "" {
		errs = append(errs, errors.New("http.addr: пусто"))
	} else if _, _, err := net.SplitHostPort(c.HTTP.Addr); err != nil {
		errs = append(errs, fmt.Errorf("http.addr: %w", err))
	}
	for name, d := range map[string]time.Duration{
		"http.read_header_timeout": c.HTTP.ReadHeaderTimeout,
		"http.read_timeout":        c.HTTP.ReadTimeout,
		"http.write_timeout":       c.HTTP.WriteTimeout,
		"http.idle_timeout":        c.HTTP.IdleTimeout,
		"http.request_timeout":     c.HTTP.RequestTimeout,
	} {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s: должен быть > 0", name))
		}
	}
	if c.HTTP.MaxHeaderBytes <= 0 {
		errs = append(errs, errors.New("http.max_header_bytes: должен быть > 0"))
	}
	if _, err := c.HTTP.TrustedPrefixes(); err != nil {
		errs = append(errs, err)
	}
	if c.Database.URL != "" {
		u, err := url.Parse(c.Database.URL)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			// Не включаем err: он может содержать DSN с паролем.
			errs = append(errs, errors.New("database.url: ожидается postgres://… или postgresql://…"))
		}
	}
	if c.NATS.URL != "" {
		if u, err := url.Parse(c.NATS.URL); err != nil || u.Host == "" {
			errs = append(errs, errors.New("nats.url: некорректный URL"))
		}
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log.level: ожидается debug|info|warn|error, получено %q", c.Log.Level))
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("log.format: ожидается json|text, получено %q", c.Log.Format))
	}
	if len(errs) > 0 {
		return fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return nil
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

func redactSecret(s string) string {
	if s == "" {
		return ""
	}
	return redacted
}

// LogValue реализует slog.LogValuer: секреты замаскированы.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("env", c.Env),
		slog.Duration("shutdown_timeout", c.ShutdownTimeout),
		slog.Group("http",
			slog.String("addr", c.HTTP.Addr),
			slog.Duration("read_header_timeout", c.HTTP.ReadHeaderTimeout),
			slog.Duration("request_timeout", c.HTTP.RequestTimeout),
			slog.Any("trusted_proxies", c.HTTP.TrustedProxies),
		),
		slog.String("database_url", RedactURL(c.Database.URL)),
		slog.String("valkey_addr", c.Valkey.Addr),
		slog.String("valkey_password", redactSecret(c.Valkey.Password)),
		slog.String("nats_url", RedactURL(c.NATS.URL)),
		slog.String("log_level", c.Log.Level),
		slog.String("log_format", c.Log.Format),
	)
}

// String не раскрывает секреты (на случай fmt.Print(cfg)).
func (c *Config) String() string {
	return c.LogValue().String()
}
