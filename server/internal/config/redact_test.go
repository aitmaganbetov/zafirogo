package config

import (
	"strings"
	"testing"
)

// SEC-021: пароль в параметрах DSN (pgx понимает ?password=) не попадает в журнал запуска.
func TestRedactURLQuerySecrets(t *testing.T) {
	for _, raw := range []string{
		"postgres://app@db:5432/zafiro?sslmode=verify-full&password=s3cret",
		"postgres://app:s3cret@db/zafiro",
		"postgres://db/zafiro?sslpassword=s3cret&sslkey=/run/s3cret.key",
		"nats://u:s3cret@nats:4222?token=s3cret",
	} {
		got := RedactURL(raw)
		if strings.Contains(got, "s3cret") {
			t.Errorf("%s → %s", raw, got)
		}
	}
	if got := RedactURL("postgres://app@db/zafiro?sslmode=require"); !strings.Contains(got, "sslmode=require") {
		t.Errorf("harmless params dropped: %s", got)
	}
}
