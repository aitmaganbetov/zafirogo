//go:build integration

package db_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
)

// SEC-005 (docs/security-review.md, миграция 0028): пул клуба zafiro_app не меняет то, что ему не нужно, на
// платформенных таблицах без RLS; zafiro_platform (кабинет, фоновые задачи) — может.
func TestAppRoleLeastPrivilege(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	clubID := d.Club(t, "sec-005")
	var userID int64
	if err := d.Owner.QueryRow(ctx, `INSERT INTO users (name, phone, password, created_at, updated_at)
		VALUES ('u', '+77010000005', 'x', now(), now()) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	denied := []struct{ name, sql string }{
		{"superadmin flag", "UPDATE users SET is_superadmin = true WHERE id = $1"},
		{"unblock account", "UPDATE users SET blocked_at = NULL WHERE id = $1"},
		{"delete user", "DELETE FROM users WHERE id = $1"},
		{"insert superadmin", "INSERT INTO users (name, phone, password, is_superadmin, created_at, updated_at) VALUES ('x', '+77010000006', 'x', $1 > 0, now(), now())"},
		{"delete club", "DELETE FROM clubs WHERE id = $1"},
		{"platform settings", "UPDATE platform_settings SET value = value WHERE $1 > 0"},
		{"saas plans", "DELETE FROM saas_plans WHERE $1 < 0"},
		{"platform log", "DELETE FROM platform_logs WHERE $1 < 0"},
		// Второй фактор суперадмина (Этап 7, 0029) — только пул платформы, пул клуба таблиц не видит.
		{"read totp secret", "SELECT secret FROM superadmin_totp WHERE user_id = $1"},
		{"reset totp", "DELETE FROM superadmin_totp WHERE user_id = $1"},
		{"read recovery codes", "SELECT code_hash FROM superadmin_recovery_codes WHERE user_id = $1"},
		{"add recovery code", "INSERT INTO superadmin_recovery_codes (user_id, code_hash, created_at) VALUES ($1, 'h', now())"},
		// История здоровья ПК (Этап 7, 0030): пул клуба только добавляет и закрывает (cleared_at), не правит и не удаляет.
		{"delete health history", "DELETE FROM computer_health_events WHERE computer_id = $1"},
		{"rewrite health history", "UPDATE computer_health_events SET kind = 'mouse', raised_at = now() WHERE computer_id = $1"},
	}
	for _, tc := range denied {
		_, err := d.App.Exec(ctx, tc.sql, userID)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "42501" {
			t.Errorf("zafiro_app %s: want permission denied, got %v", tc.name, err)
		}
	}

	allowed := []struct{ name, sql string }{
		{"profile", "UPDATE users SET name = 'n', email = NULL, updated_at = now() WHERE id = $1"},
		{"password", "UPDATE users SET password = 'y', updated_at = now() WHERE id = $1"},
		{"register", "INSERT INTO users (name, phone, password, created_at, updated_at) VALUES ('r', '+77010000007', 'x', now(), now()) RETURNING $1::bigint"},
		{"login failed log", "INSERT INTO platform_logs (event, description, ip_address, created_at) SELECT 'auth.test', 'd', '127.0.0.1', now() WHERE $1 > 0"},
		{"close health problem", "UPDATE computer_health_events SET cleared_at = now() WHERE computer_id = $1"},
		{"read health history", "SELECT count(*) FROM computer_health_events WHERE computer_id = $1"},
	}
	for _, tc := range allowed {
		if _, err := d.App.Exec(ctx, tc.sql, userID); err != nil {
			t.Errorf("zafiro_app %s: %v", tc.name, err)
		}
	}
	// clubs: пул клуба меняет только свой клуб (app.club_id), чужой — 0 строк.
	other := d.Club(t, "sec-005-other")
	tx, err := d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(clubID, 10)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   int64
		want int64
	}{{clubID, 1}, {other, 0}} {
		tag, err := tx.Exec(ctx, "UPDATE clubs SET timezone = timezone WHERE id = $1", c.id)
		if err != nil {
			t.Fatal(err)
		}
		if tag.RowsAffected() != c.want {
			t.Errorf("zafiro_app UPDATE club %d: %d rows, want %d", c.id, tag.RowsAffected(), c.want)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if tag, err := d.App.Exec(ctx, "UPDATE clubs SET timezone = timezone WHERE id = $1", clubID); err != nil || tag.RowsAffected() != 0 {
		t.Errorf("zafiro_app UPDATE club without app.club_id: %v, %d rows", err, tag.RowsAffected())
	}
	if _, err := d.Platform.Exec(ctx, "UPDATE users SET is_superadmin = false, blocked_at = NULL WHERE id = $1", userID); err != nil {
		t.Errorf("zafiro_platform users: %v", err)
	}
	for _, sql := range []string{
		"INSERT INTO superadmin_totp (user_id, secret, created_at, updated_at) VALUES ($1, 'v2:k:x', now(), now())",
		"INSERT INTO superadmin_recovery_codes (user_id, code_hash, created_at) VALUES ($1, 'h', now())",
		"UPDATE superadmin_recovery_codes SET used_at = now() WHERE user_id = $1",
		"DELETE FROM superadmin_totp WHERE user_id = $1",
	} {
		if _, err := d.Platform.Exec(ctx, sql, userID); err != nil {
			t.Errorf("zafiro_platform %q: %v", sql, err)
		}
	}
	// Ключ — только шифротекст secretbox.
	if _, err := d.Platform.Exec(ctx, "INSERT INTO superadmin_totp (user_id, secret, created_at, updated_at) VALUES ($1, 'JBSWY3DPEHPK3PXP', now(), now())", userID); err == nil {
		t.Error("plaintext totp secret accepted")
	}
}
