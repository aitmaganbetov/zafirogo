package config

import (
	"log/slog"
	"net/url"
	"strings"
)

const redacted = "[REDACTED]"

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
		slog.String("valkey_username", c.Valkey.Username),
		slog.Bool("valkey_tls", c.Valkey.TLS),
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
