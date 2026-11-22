package config

import (
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

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
	explicitEnv := lookup(environ, EnvPrefix+"ENV") != ""
	if path != "" {
		fk := koanf.New(".")
		if err := fk.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("config: file %q: %w", path, err)
		}
		explicitEnv = explicitEnv || fk.String("env") != ""
		if err := k.Merge(fk); err != nil {
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
	// Аудит OPS-18: env по умолчанию — dev (случайные ключи, dev-CA агентов). Бинарник, запущенный вне образа
	// (systemd, вручную) без ZAFIRO_ENV против настоящей базы, не должен молча стартовать в dev.
	if !explicitEnv && !cfg.Standalone() && !localPostgres(cfg.Database.URL.Reveal()) {
		return nil, fmt.Errorf("config: env: не задан явно (%sENV или env в файле), а database.url указывает не на "+
			"локальный хост — задайте %sENV=prod (для разработки с удалённой базой — dev или test)", EnvPrefix, EnvPrefix)
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

// localPostgres — все хосты DSN локальные: localhost, loopback, unix-сокет. DSN разбирается как pgx (URL или
// «ключ=значение», с PGHOST и запасными хостами), чтобы `host=db.internal …` не считался локальным (повторное
// ревью аудита, OPS-18). Некорректный DSN — не локальный (ошибку формата сообщит Validate, если env задан явно).
func localPostgres(dsn string) bool {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return false
	}
	hosts := []string{cfg.Host}
	for _, fb := range cfg.Fallbacks {
		hosts = append(hosts, fb.Host)
	}
	for _, h := range hosts {
		if h == "" || strings.HasPrefix(h, "/") {
			continue // unix-сокет
		}
		h = strings.Trim(h, "[]")
		if strings.EqualFold(h, "localhost") {
			continue
		}
		if ip, err := netip.ParseAddr(h); err == nil && ip.IsLoopback() {
			continue
		}
		return false
	}
	return true
}
