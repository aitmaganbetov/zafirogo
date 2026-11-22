package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPGOptions(t *testing.T) {
	for _, tc := range []struct{ existing, want string }{
		{"", "-c lock_timeout=5s -c statement_timeout=30min"},
		{"-c search_path=public", "-c search_path=public -c lock_timeout=5s -c statement_timeout=30min"},
		// Заданное оператором не переопределяется.
		{"-c lock_timeout=1s", "-c lock_timeout=1s -c statement_timeout=30min"},
		{"-c statement_timeout=0 -c lock_timeout=2s", "-c statement_timeout=0 -c lock_timeout=2s"},
	} {
		if got := pgOptions(tc.existing, "5s", "30min"); got != tc.want {
			t.Errorf("pgOptions(%q) = %q, want %q", tc.existing, got, tc.want)
		}
	}
}

func TestLockNotAvailable(t *testing.T) {
	// Формат ошибки goose v3 + pgx.
	out := []byte(`2026/11/22 goose run: ERROR 0045_x.sql: failed to run SQL migration: failed to execute SQL query "ALTER TABLE clubs ADD COLUMN x int": ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)`)
	if !lockNotAvailable(out) {
		t.Fatal("55P03 не распознан")
	}
	if lockNotAvailable([]byte(`ERROR: canceling statement due to statement timeout (SQLSTATE 57014)`)) {
		t.Fatal("statement_timeout не повторяется")
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "dsn")
	if err := os.WriteFile(secret, []byte("postgres://owner:s3cret@db/zafiro\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GOOSE_DBSTRING_FILE": secret, "GOOSE_DBSTRING": "postgres://wrong"}
	cfg, err := loadConfig(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dsn != "postgres://owner:s3cret@db/zafiro" {
		t.Errorf("dsn из файла: %q", cfg.dsn)
	}
	if len(cfg.args) != 1 || cfg.args[0] != "up" || cfg.lockWait != 10*time.Minute || cfg.retries != 5 {
		t.Errorf("умолчания: %+v", cfg)
	}
	if _, err := loadConfig(nil, func(string) string { return "" }); err == nil {
		t.Error("без DSN — ошибка")
	}
	env = map[string]string{"GOOSE_DBSTRING": "postgres://x", "ZAFIRO_MIGRATE_LOCK_TIMEOUT": "5s -c role=postgres"}
	if _, err := loadConfig(nil, func(k string) string { return env[k] }); err == nil {
		t.Error("пробел в таймауте — ошибка (подстановка параметров в PGOPTIONS)")
	}
}
