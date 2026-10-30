package config

import (
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
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("shutdown_timeout = %v, want 15s", cfg.ShutdownTimeout)
	}
	if cfg.HTTP.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("read_header_timeout = %v", cfg.HTTP.ReadHeaderTimeout)
	}
	if cfg.Log.Level != "info" || cfg.Log.Format != "json" {
		t.Errorf("log = %+v", cfg.Log)
	}
	if cfg.Database.URL != "" {
		t.Errorf("database.url must be empty by default")
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	cfg, err := Load("", environ(
		"ZAFIRO_HTTP_ADDR=127.0.0.1:9090",
		"ZAFIRO_HTTP_READ_HEADER_TIMEOUT=2s",
		"ZAFIRO_HTTP_TRUSTED_PROXIES=10.0.0.0/8, 127.0.0.1",
		"ZAFIRO_DATABASE_URL=postgres://u:secret@localhost:5432/zafiro",
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
	if cfg.Database.URL != "postgres://u:secret@localhost:5432/zafiro" {
		t.Errorf("database.url not loaded")
	}
	if cfg.Valkey.Addr != "valkey:6379" || cfg.NATS.URL != "nats://nats:4222" {
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
		"ZAFIRO_VALKEY_PASSWORD=valkeysecret",
	))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.String()
	for _, secret := range []string{"supersecret", "valkeysecret"} {
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
