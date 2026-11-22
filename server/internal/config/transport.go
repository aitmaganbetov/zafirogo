package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
)

// TLSConfig — tls.Config клиента Valkey (valkey.tls=true; аудит SEC-2/OPS-6): CA из tls_ca_file (пусто — системные
// корневые), имя сервера — tls_server_name или хост из addr, TLS ≥ 1.2. nil — TLS выключен.
func (v Valkey) TLSConfig() (*tls.Config, error) {
	if !v.TLS {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: v.TLSServerName}
	if cfg.ServerName == "" {
		if host, _, err := net.SplitHostPort(v.Addr); err == nil {
			cfg.ServerName = host
		}
	}
	if v.TLSCAFile != "" {
		pem, err := os.ReadFile(v.TLSCAFile)
		if err != nil {
			return nil, errors.New("valkey.tls_ca_file: не удалось прочитать файл")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("valkey.tls_ca_file: в файле нет сертификатов PEM")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// secureSSLModes — sslmode Postgres, при которых канал зашифрован (verify-* ещё и проверяют сертификат сервера).
var secureSSLModes = map[string]bool{"require": true, "verify-ca": true, "verify-full": true}

// postgresSSLMode — sslmode из DSN (url или «ключ=значение»); пусто — не задан (pgx: prefer — без проверки
// сертификата, с откатом на открытый канал).
func postgresSSLMode(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		return u.Query().Get("sslmode")
	}
	for f := range strings.FieldsSeq(dsn) {
		if v, ok := strings.CutPrefix(f, "sslmode="); ok {
			return v
		}
	}
	return ""
}

// Warnings — настройки, допустимые, но небезопасные в prod (аудит OPS-19, SEC-2): журналируются при старте.
// Не ошибки: изолированная сеть (staging, Postgres на том же хосте) — законный случай. Тексты без секретов.
func (c *Config) Warnings() []string {
	if c.Env != "prod" || c.Standalone() {
		return nil
	}
	var out []string
	for _, db := range []struct {
		name string
		dsn  Secret
	}{{"database.url", c.Database.URL}, {"database.platform_url", c.Database.PlatformURL}} {
		name, dsn := db.name, db.dsn
		if mode := postgresSSLMode(dsn.Reveal()); !secureSSLModes[mode] {
			if mode == "" {
				mode = "не задан (prefer)"
			}
			out = append(out, name+": sslmode "+mode+" — канал до Postgres не зашифрован или может откатиться на открытый; "+
				"в общей сети задайте sslmode=verify-full")
		}
		if !strings.Contains(dsn.Reveal(), "pool_max_conns") {
			out = append(out, name+": pool_max_conns не задан — размер пула 4 × GOMAXPROCS (не меньше 16); "+
				"сверьте сумму пулов всех инстансов с max_connections Postgres")
		}
	}
	if !c.Valkey.TLS {
		out = append(out, "valkey.tls: выключен — сессии и пароль Valkey идут открытым текстом; в общей сети включите TLS")
	}
	if u, err := url.Parse(c.NATS.URL.Reveal()); err == nil && u.User == nil {
		out = append(out, "nats.url: без учётных данных — публиковать события клубов может любой в сети NATS")
	}
	if c.Events.Stream.MaxBytes == 0 {
		out = append(out, "events.stream.max_bytes: 0 — поток событий без предела размера; заполнение диска NATS остановит outbox")
	}
	return out
}
