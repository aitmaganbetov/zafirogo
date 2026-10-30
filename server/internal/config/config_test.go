package config

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func environ(kv ...string) func() []string {
	return func() []string { return kv }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("", environ())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("http.addr = %q, want :8080", cfg.HTTP.Addr)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("shutdown_timeout = %v, want 30s", cfg.ShutdownTimeout)
	}
	if cfg.HTTP.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("read_header_timeout = %v", cfg.HTTP.ReadHeaderTimeout)
	}
	if cfg.Log.Level != "info" || cfg.Log.Format != "json" {
		t.Errorf("log = %+v", cfg.Log)
	}
	if !cfg.Database.URL.IsZero() || !cfg.Standalone() {
		t.Errorf("database.url must be empty by default (standalone mode)")
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	cfg, err := Load("", environ(
		"ZAFIRO_HTTP_ADDR=127.0.0.1:9090",
		"ZAFIRO_HTTP_READ_HEADER_TIMEOUT=2s",
		"ZAFIRO_HTTP_TRUSTED_PROXIES=10.0.0.0/8, 127.0.0.1",
		"ZAFIRO_DATABASE_URL=postgres://u:secret@localhost:5432/zafiro",
		"ZAFIRO_DATABASE_PLATFORM_URL=postgres://p:secret@localhost:5432/zafiro",
		"ZAFIRO_VALKEY_ADDR=valkey:6379",
		"ZAFIRO_NATS_URL=nats://nats:4222",
		"ZAFIRO_LOG_LEVEL=debug",
		"ZAFIRO_LOG_FORMAT=text",
		"ZAFIRO_SHUTDOWN_TIMEOUT=3s",
		"OTHER_HTTP_ADDR=:1",
	))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Addr != "127.0.0.1:9090" {
		t.Errorf("http.addr = %q", cfg.HTTP.Addr)
	}
	if cfg.HTTP.ReadHeaderTimeout != 2*time.Second {
		t.Errorf("read_header_timeout = %v", cfg.HTTP.ReadHeaderTimeout)
	}
	if got := strings.Join(cfg.HTTP.TrustedProxies, ","); got != "10.0.0.0/8,127.0.0.1" {
		t.Errorf("trusted_proxies = %q", got)
	}
	if cfg.Database.URL.Reveal() != "postgres://u:secret@localhost:5432/zafiro" {
		t.Errorf("database.url not loaded")
	}
	if cfg.Valkey.Addr != "valkey:6379" || cfg.NATS.URL.Reveal() != "nats://nats:4222" {
		t.Errorf("valkey/nats = %q %q", cfg.Valkey.Addr, cfg.NATS.URL)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "text" {
		t.Errorf("log = %+v", cfg.Log)
	}
	if cfg.ShutdownTimeout != 3*time.Second {
		t.Errorf("shutdown_timeout = %v", cfg.ShutdownTimeout)
	}
}

func TestLoadFileThenEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zafiro.yaml")
	data := "http:\n  addr: \":7070\"\nlog:\n  level: warn\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("", environ(FileEnv+"="+path, "ZAFIRO_LOG_LEVEL=error"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Addr != ":7070" {
		t.Errorf("file value not applied: %q", cfg.HTTP.Addr)
	}
	if cfg.Log.Level != "error" {
		t.Errorf("env must override file: %q", cfg.Log.Level)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]string{
		"bad level":     "ZAFIRO_LOG_LEVEL=verbose",
		"bad format":    "ZAFIRO_LOG_FORMAT=xml",
		"bad env":       "ZAFIRO_ENV=staging",
		"bad addr":      "ZAFIRO_HTTP_ADDR=8080",
		"zero timeout":  "ZAFIRO_SHUTDOWN_TIMEOUT=0s",
		"bad proxy":     "ZAFIRO_HTTP_TRUSTED_PROXIES=not-an-ip",
		"bad db scheme": "ZAFIRO_DATABASE_URL=mysql://root:topsecret@localhost/zafiro",
	}
	for name, kv := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load("", environ(kv))
			if err == nil {
				t.Fatal("expected validation error")
			}
			if strings.Contains(err.Error(), "topsecret") {
				t.Fatalf("error leaks secret: %v", err)
			}
		})
	}
}

func TestLogValueRedactsSecrets(t *testing.T) {
	cfg, err := Load("", environ(
		"ZAFIRO_DATABASE_URL=postgres://zafiro:supersecret@db:5432/zafiro",
		"ZAFIRO_DATABASE_PLATFORM_URL=postgres://platform:platformsecret@db:5432/zafiro",
		"ZAFIRO_VALKEY_PASSWORD=valkeysecret",
		"ZAFIRO_NATS_URL=nats://n:natssecret@nats:4222",
		"ZAFIRO_AUTH_SIGNING_KEYS=k1:signingsecret-0123456789abcdef0123456789",
		"ZAFIRO_AGENT_ENROLL_CODE_PEPPER=peppersecret-0123456789abcdef0123456789",
	))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.String()
	for _, secret := range []string{"supersecret", "platformsecret", "valkeysecret", "natssecret", "signingsecret", "peppersecret"} {
		if strings.Contains(s, secret) {
			t.Errorf("secret %q leaked: %s", secret, s)
		}
	}
	if !strings.Contains(s, "db:5432") {
		t.Errorf("non-secret part of DSN must stay visible: %s", s)
	}
}

func TestTrustedPrefixes(t *testing.T) {
	h := HTTP{TrustedProxies: []string{"10.1.2.3/8", "::ffff:127.0.0.1", "2001:db8::1"}}
	ps, err := h.TrustedPrefixes()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "127.0.0.1/32", "2001:db8::1/128"}
	for i, p := range ps {
		if p.String() != want[i] {
			t.Errorf("prefix[%d] = %s, want %s", i, p, want[i])
		}
	}
}

func TestLoadNestedEnvKeys(t *testing.T) {
	cfg, err := Load("", environ(
		"ZAFIRO_AGENT_GRPC_LISTEN=127.0.0.1:9443",
		"ZAFIRO_AGENT_CERT_LIFETIME=48h",
		"ZAFIRO_AGENT_CERT_CACHE_REFRESH=7s",
		"ZAFIRO_AGENT_STREAM_KEY_PREFIX=t:agent:",
		"ZAFIRO_AGENT_INSTANCE_ID=node-1",
		"ZAFIRO_WEBHOOKS_BACKOFF=1s, 2m",
		"ZAFIRO_WEBHOOKS_REQUIRE_HTTPS=false",
		"ZAFIRO_REALTIME_ORIGIN_PATTERNS=admin.example.kz,*.example.kz",
		"ZAFIRO_RATELIMIT_ENABLE=web-login",
		"ZAFIRO_PASSWORD_MEMORY_KIB=65536",
		"ZAFIRO_AUTH_SIGNING_KEYS=new:0123456789abcdef0123456789abcdef, old:fedcba9876543210fedcba9876543210",
		"ZAFIRO_UNKNOWN_KEY=1",
	))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.GRPC.Listen != "127.0.0.1:9443" || cfg.Agent.Cert.Lifetime != 48*time.Hour ||
		cfg.Agent.CertCache.Refresh != 7*time.Second || cfg.Agent.Stream.KeyPrefix != "t:agent:" ||
		cfg.Agent.InstanceID != "node-1" {
		t.Errorf("agent = %+v", cfg.Agent)
	}
	if len(cfg.Webhooks.Backoff) != 2 || cfg.Webhooks.Backoff[0] != time.Second || cfg.Webhooks.Backoff[1] != 2*time.Minute {
		t.Errorf("webhooks.backoff = %v", cfg.Webhooks.Backoff)
	}
	if cfg.Webhooks.RequireHTTPS {
		t.Error("webhooks.require_https must be false")
	}
	if got := strings.Join(cfg.Realtime.OriginPatterns, "|"); got != "admin.example.kz|*.example.kz" {
		t.Errorf("realtime.origin_patterns = %q", got)
	}
	if len(cfg.RateLimit.Enable) != 1 || cfg.Password.MemoryKiB != 65536 {
		t.Errorf("ratelimit/password = %+v %+v", cfg.RateLimit, cfg.Password)
	}
	if len(cfg.Auth.SigningKeys) != 2 || cfg.Auth.SigningKeys[0].ID != "new" ||
		cfg.Auth.SigningKeys[1].Secret.Reveal() != "fedcba9876543210fedcba9876543210" {
		t.Errorf("auth.signing_keys = %v", cfg.Auth.SigningKeys)
	}
	if len(cfg.Generated) != 1 || cfg.Generated[0] != "agent.enroll.code_pepper" {
		t.Errorf("generated = %v", cfg.Generated)
	}
}

func TestLoadDefaultsAgent(t *testing.T) {
	cfg, err := Load("", environ())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.PKI.DevDir != DevPKIDir {
		t.Errorf("dev pki dir = %q", cfg.Agent.PKI.DevDir)
	}
	if !instanceRe.MatchString(cfg.Agent.InstanceID) {
		t.Errorf("instance id = %q", cfg.Agent.InstanceID)
	}
	if cfg.Agent.Stream.PresenceTTL != 90*time.Second || cfg.Agent.Sweeper.CommandRetention != 168*time.Hour {
		t.Errorf("stream = %+v sweeper = %+v", cfg.Agent.Stream, cfg.Agent.Sweeper)
	}
	if len(cfg.Webhooks.Backoff) != 5 || cfg.Webhooks.Backoff[4] != 2*time.Hour || !cfg.Webhooks.RequireHTTPS {
		t.Errorf("webhooks = %+v", cfg.Webhooks)
	}
	if len(cfg.Auth.SigningKeys) != 1 || len(cfg.Auth.SigningKeys[0].Secret.Reveal()) < 32 {
		t.Errorf("dev signing key must be generated")
	}
}

func TestLoadSigningKeysFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zafiro.yaml")
	data := "auth:\n  signing_keys:\n    - id: k1\n      secret: \"0123456789abcdef0123456789abcdef\"\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, environ())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Auth.SigningKeys) != 1 || cfg.Auth.SigningKeys[0].ID != "k1" {
		t.Errorf("signing keys = %v", cfg.Auth.SigningKeys)
	}
}

func TestValidateProd(t *testing.T) {
	base := []string{
		"ZAFIRO_ENV=prod",
		"ZAFIRO_DATABASE_URL=postgres://app:topsecret@db/zafiro",
		"ZAFIRO_DATABASE_PLATFORM_URL=postgres://platform:topsecret@db/zafiro",
		"ZAFIRO_AUTH_SIGNING_KEYS=k1:topsecret-0123456789abcdef0123456789",
		"ZAFIRO_AGENT_ENROLL_CODE_PEPPER=topsecret-0123456789abcdef0123456789",
		"ZAFIRO_AGENT_PKI_CA_CERT_FILE=/etc/zafiro/ca.pem",
		"ZAFIRO_AGENT_PKI_CA_KEY_FILE=/etc/zafiro/ca.key",
		"ZAFIRO_AGENT_PKI_SERVER_CERT_FILE=/etc/zafiro/server.pem",
		"ZAFIRO_AGENT_PKI_SERVER_KEY_FILE=/etc/zafiro/server.key",
	}
	cfg, err := Load("", environ(base...))
	if err != nil {
		t.Fatalf("valid prod config: %v", err)
	}
	if len(cfg.Generated) != 0 || cfg.Agent.PKI.DevDir != "" {
		t.Errorf("prod must not generate secrets or dev PKI: %v %q", cfg.Generated, cfg.Agent.PKI.DevDir)
	}
	drop := func(prefix string) []string {
		var out []string
		for _, kv := range base {
			if !strings.HasPrefix(kv, prefix) {
				out = append(out, kv)
			}
		}
		return out
	}
	cases := map[string][]string{
		"no database":        drop("ZAFIRO_DATABASE_URL="),
		"no platform dsn":    drop("ZAFIRO_DATABASE_PLATFORM_URL="),
		"no signing keys":    drop("ZAFIRO_AUTH_SIGNING_KEYS="),
		"no pepper":          drop("ZAFIRO_AGENT_ENROLL_CODE_PEPPER="),
		"no server key":      drop("ZAFIRO_AGENT_PKI_SERVER_KEY_FILE="),
		"short signing key":  append(drop("ZAFIRO_AUTH_SIGNING_KEYS="), "ZAFIRO_AUTH_SIGNING_KEYS=k1:topsecret"),
		"bad signing format": append(drop("ZAFIRO_AUTH_SIGNING_KEYS="), "ZAFIRO_AUTH_SIGNING_KEYS=topsecret-0123456789abcdef0123456789"),
		"dev pki in prod":    append(base, "ZAFIRO_AGENT_PKI_DEV_DIR=/tmp/pki"),
		"same dsn":           append(drop("ZAFIRO_DATABASE_PLATFORM_URL="), "ZAFIRO_DATABASE_PLATFORM_URL=postgres://app:topsecret@db/zafiro"),
		"bad instance":       append(base, "ZAFIRO_AGENT_INSTANCE_ID=node.1"),
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load("", environ(env...))
			if err == nil {
				t.Fatal("expected validation error")
			}
			if strings.Contains(err.Error(), "topsecret") {
				t.Fatalf("error leaks secret: %v", err)
			}
		})
	}
}

func TestSecretFormatting(t *testing.T) {
	s := Secret("hunter2")
	for _, got := range []string{
		fmt.Sprint(s), fmt.Sprintf("%v %s %+v %#v %q", s, s, s, s, s),
		fmt.Sprintf("%+v", SigningKey{ID: "k", Secret: s}),
		fmt.Sprintf("%#v", SigningKey{ID: "k", Secret: s}),
	} {
		if strings.Contains(got, "hunter2") {
			t.Errorf("secret leaked via fmt: %s", got)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "s", s, "k", SigningKey{ID: "k", Secret: s})
	if strings.Contains(buf.String(), "hunter2") {
		t.Errorf("secret leaked via slog: %s", buf.String())
	}
	b, _ := json.Marshal(map[string]any{"s": s})
	if strings.Contains(string(b), "hunter2") {
		t.Errorf("secret leaked via json: %s", b)
	}
	if s.Reveal() != "hunter2" || Secret("").String() != "" {
		t.Error("Reveal/String")
	}
}

// Решения Q-037 и «Обновления агента» (docs/DECISIONS.md): срок сертификата бездискового ПК и открытые ключи
// подписи обновлений агента.
func TestAgentUpdateKeysAndDiskless(t *testing.T) {
	cfg, err := Load("", environ())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Enroll.DisklessCertLifetime != 24*time.Hour || len(cfg.Agent.Update.PublicKeys) != 0 {
		t.Fatalf("defaults: %v %v", cfg.Agent.Enroll.DisklessCertLifetime, cfg.Agent.Update.PublicKeys)
	}
	pub := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, ed25519.PublicKeySize))
	cfg, err = Load("", environ("ZAFIRO_AGENT_UPDATE_PUBLIC_KEYS=2026-10:"+pub+", 2027-01:"+pub,
		"ZAFIRO_AGENT_ENROLL_DISKLESS_CERT_LIFETIME=12h"))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := cfg.Agent.Update.UpdatePublicKeys()
	if err != nil || len(keys) != 2 || len(keys["2026-10"]) != ed25519.PublicKeySize || cfg.Agent.Enroll.DisklessCertLifetime != 12*time.Hour {
		t.Fatalf("keys = %v, %v; diskless = %v", keys, err, cfg.Agent.Enroll.DisklessCertLifetime)
	}
	for _, bad := range []string{
		"ZAFIRO_AGENT_UPDATE_PUBLIC_KEYS=" + pub,                 // без key_id
		"ZAFIRO_AGENT_UPDATE_PUBLIC_KEYS=k:AAAA",                 // не 32 байта
		"ZAFIRO_AGENT_UPDATE_PUBLIC_KEYS=k:" + pub + ",k:" + pub, // повтор
		"ZAFIRO_AGENT_ENROLL_DISKLESS_CERT_LIFETIME=0s",          // ≤ 0
		"ZAFIRO_AGENT_ENROLL_DISKLESS_CERT_LIFETIME=1000h",       // больше agent.cert.lifetime
	} {
		if _, err := Load("", environ(bad)); err == nil {
			t.Errorf("%s: expected validation error", bad)
		}
	}
}
