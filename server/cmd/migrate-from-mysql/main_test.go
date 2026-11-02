package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunUsage(t *testing.T) {
	t.Setenv("ZAFIRO_MIGRATE_MYSQL_DSN", "")
	t.Setenv("ZAFIRO_MIGRATE_PG_DSN", "")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "usage"},
		{[]string{"migrate"}, "usage"},
		{[]string{"plan"}, "нужны --club"},
		{[]string{"apply", "--club", "1"}, "нужны --club"},
		{[]string{"plan", "--club", "1", "--mysql", "x", "--pg", "y", "--source-tz", "Mars/Olympus"}, "--source-tz"},
		{[]string{"platform"}, "usage"},
		{[]string{"platform", "migrate"}, "usage"},
		{[]string{"platform", "plan"}, "нужны"},
		{[]string{"plan", "--club", "1", "--mysql", "x", "--pg", "y", "--laravel-storage", "/nonexistent/zafiro"}, "--laravel-storage"},
	} {
		var out, errOut bytes.Buffer
		if code := run(tc.args, &out, &errOut); code != exitUsage {
			t.Errorf("%v: код %d, ожидали %d", tc.args, code, exitUsage)
		}
		if !strings.Contains(errOut.String(), tc.want) {
			t.Errorf("%v: %q без %q", tc.args, errOut.String(), tc.want)
		}
	}
}

func TestSecretKeysFromEnv(t *testing.T) {
	for _, tc := range []struct {
		appKey, dataKeys, want string
	}{
		{"base64:short", "", "ZAFIRO_MIGRATE_LARAVEL_APP_KEY"},
		{"", "nokey", "ZAFIRO_SECURITY_DATA_KEYS"},
		{"", "k1:short", "ZAFIRO_SECURITY_DATA_KEYS"},
	} {
		t.Setenv("ZAFIRO_MIGRATE_LARAVEL_APP_KEY", tc.appKey)
		t.Setenv("ZAFIRO_MIGRATE_LARAVEL_PREVIOUS_KEYS", "")
		t.Setenv("ZAFIRO_SECURITY_DATA_KEYS", tc.dataKeys)
		_, _, err := secretKeys()
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "short") {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	t.Setenv("ZAFIRO_MIGRATE_LARAVEL_APP_KEY", "base64:AQEBAQEBAQEBAQEBAQEBAQICAgICAgICAgICAgICAgI=")
	t.Setenv("ZAFIRO_SECURITY_DATA_KEYS", "k1:"+strings.Repeat("x", 32)+", k0:"+strings.Repeat("y", 32))
	laravel, box, err := secretKeys()
	if err != nil || laravel == nil || box == nil || box.ActiveKeyID() != "k1" {
		t.Fatalf("keys: %v", err)
	}
}
