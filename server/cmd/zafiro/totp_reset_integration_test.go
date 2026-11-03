//go:build integration

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
)

// Аварийный сброс второго фактора командой сервера (Этап 7): ключ и коды восстановления удаляются, сеансы
// завершаются, журнал платформы и аудит; телефон — в любом виде; без ключа — «менять нечего».
func TestTOTPResetCommand(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	var id int64
	if err := d.Owner.QueryRow(ctx, `INSERT INTO users (name, phone, password, is_superadmin, created_at, updated_at)
		VALUES ('Владелец платформы', '+77011234567', 'x', true, now(), now()) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Owner.Exec(ctx, `INSERT INTO superadmin_totp (user_id, secret, confirmed_at, last_step, created_at, updated_at)
		VALUES ($1, 'v2:k:AAAA', now(), 7, now(), now())`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Owner.Exec(ctx, `INSERT INTO superadmin_recovery_codes (user_id, code_hash, created_at) VALUES ($1, 'h1', now()), ($1, 'h2', now())`, id); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var revoked []int64
	deps := totpResetDeps{platform: d.Platform, out: &out, revoke: func(_ context.Context, uid int64) error {
		revoked = append(revoked, uid)
		return nil
	}}

	if err := totpReset(ctx, deps, "+7 (701) 123-45-67"); err != nil {
		t.Fatal(err)
	}
	count := func(sql string) int64 {
		t.Helper()
		var n int64
		if err := d.Owner.QueryRow(ctx, sql, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("SELECT count(*) FROM superadmin_totp WHERE user_id = $1") != 0 || count("SELECT count(*) FROM superadmin_recovery_codes WHERE user_id = $1") != 0 {
		t.Fatal("second factor left after reset")
	}
	if len(revoked) != 1 || revoked[0] != id || !strings.Contains(out.String(), "сброшен") {
		t.Fatalf("revoked %v, out %q", revoked, out.String())
	}
	if count("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.totp_reset' AND subject_id = $1 AND user_id IS NULL") != 1 ||
		count("SELECT count(*) FROM audit_log WHERE action = 'superadmin.totp_reset' AND subject_id = $1 AND actor_kind = 'system'") != 1 {
		t.Fatal("reset not logged")
	}

	out.Reset()
	if err := totpReset(ctx, deps, "+77011234567"); err != nil || !strings.Contains(out.String(), "не настроен") || len(revoked) != 1 {
		t.Fatalf("second reset: %v %q", err, out.String())
	}
	if err := totpReset(ctx, deps, "+77019999999"); err == nil || !strings.Contains(err.Error(), "не найден") {
		t.Fatalf("unknown phone: %v", err)
	}
	if err := totpReset(ctx, deps, ""); err == nil {
		t.Fatal("empty phone accepted")
	}
}
