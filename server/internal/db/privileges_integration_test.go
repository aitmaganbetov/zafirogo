//go:build integration

package db_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
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
		// Почасовая история здоровья (Этап 7, 0031): пул клуба пишет агрегаты (UPSERT), но не удаляет строки и не меняет
		// их ключ (клуб, ПК, час).
		{"delete hourly health", "DELETE FROM computer_health_hourly WHERE computer_id = $1"},
		{"move hourly health", "UPDATE computer_health_hourly SET hour = hour + interval '1 hour' WHERE computer_id = $1"},
		{"reassign hourly health", "UPDATE computer_health_hourly SET club_id = 0 WHERE computer_id = $1"},
		// Реселлеры и выплаты (0034) — данные платформы: пул клуба их не пишет, выплаты и журнал денег платформы
		// (0038) не видит совсем.
		{"create reseller", "INSERT INTO resellers (user_id, code, name, created_at, updated_at) VALUES ($1, 'abc', 'x', now(), now())"},
		{"reseller commission", "UPDATE resellers SET commission_bp = 10000 WHERE user_id = $1"},
		{"delete reseller", "DELETE FROM resellers WHERE user_id = $1"},
		{"read payouts", "SELECT amount FROM reseller_payouts WHERE created_by = $1"},
		{"add payout", "INSERT INTO reseller_payouts (reseller_id, amount, created_by, created_at) VALUES (1, 100, $1, now())"},
		{"read platform money log", "SELECT amount FROM platform_money_log WHERE actor_user_id = $1"},
		{"write platform money log", "INSERT INTO platform_money_log (kind, idempotency_key, amount, reseller_id, payout_id, actor_user_id) VALUES ('reseller_payout', 'k', 1, 1, 1, $1)"},
		// Счета за подписку (0035): пул клуба не отмечает оплату, не меняет сумму и не удаляет счёт.
		{"invoice paid_at", "UPDATE subscription_invoices SET paid_at = now(), confirmed_by = $1 WHERE created_by = $1"},
		{"invoice amount", "UPDATE subscription_invoices SET amount = 0 WHERE created_by = $1"},
		{"invoice earning", "UPDATE subscription_invoices SET reseller_amount = 0 WHERE created_by = $1"},
		{"delete invoice", "DELETE FROM subscription_invoices WHERE created_by = $1"},
		{"insert paid invoice columns", "INSERT INTO subscription_invoices (club_id, saas_plan_id, months, computers, price_per_computer, amount, status, paid_at, created_by) VALUES (1, 1, 1, 1, 100, 100, 'paid', now(), $1)"},
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
		{"read hourly health", "SELECT count(*) FROM computer_health_hourly WHERE computer_id = $1"},
		{"upsert hourly health", "UPDATE computer_health_hourly SET online_minutes = 60, cpu_temp_max_deci = 800, bugchecks = 1 WHERE computer_id = $1"},
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

// Миграции 0034–0038 (docs/upstream-2026-11.md, Ш0): RLS и права новых таблиц — счета за подписку, реселлеры и
// выплаты, фото клуба, брони из приложения, журнал денег платформы.
func TestUpstreamTablesPrivileges(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	clubA, clubB := d.Club(t, "s0-a"), d.Club(t, "s0-b")
	one := func(q string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := d.Owner.QueryRow(ctx, q, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return id
	}
	code := func(err error) string {
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			return pg.Code
		}
		if err != nil {
			return err.Error()
		}
		return ""
	}
	u1 := one("INSERT INTO users (name, phone, password, created_at, updated_at) VALUES ('r1', '+77010000101', 'x', now(), now()) RETURNING id")
	u2 := one("INSERT INTO users (name, phone, password, created_at, updated_at) VALUES ('r2', '+77010000102', 'x', now(), now()) RETURNING id")
	plan := one("INSERT INTO saas_plans (slug, name, price_monthly, created_at, updated_at) VALUES ('s0', 'S0', 2500000, now(), now()) RETURNING id")

	// RLS включён и принудителен на каждой новой таблице с club_id; resellers — включён (как clubs).
	if got := one("SELECT count(*) FROM zafiro_tables_without_rls()"); got != 0 {
		t.Fatalf("таблицы без RLS: %d", got)
	}
	for _, tbl := range []string{"subscription_invoices", "club_photos", "club_photo_images", "platform_money_log"} {
		if got := one("SELECT count(*) FROM pg_class WHERE oid = to_regclass($1) AND relrowsecurity AND relforcerowsecurity", tbl); got != 1 {
			t.Errorf("%s: RLS не включён или не принудителен", tbl)
		}
	}
	if got := one("SELECT count(*) FROM pg_class WHERE oid = 'resellers'::regclass AND relrowsecurity"); got != 1 {
		t.Error("resellers: RLS не включён")
	}

	// --- Реселлеры: пишет только платформа; код — строчные латинские буквы, цифры, дефис; проценты в пределах ---
	var r1, r2 int64
	if err := d.Platform.QueryRow(ctx, `INSERT INTO resellers (user_id, code, name, commission_bp, discount_percent, created_at, updated_at)
		VALUES ($1, 'astana-pro', 'Astana Pro', 1235, 10, now(), now()) RETURNING id`, u1).Scan(&r1); err != nil {
		t.Fatalf("zafiro_platform resellers: %v", err)
	}
	if err := d.Platform.QueryRow(ctx, `INSERT INTO resellers (user_id, code, name, created_at, updated_at) VALUES ($1, 'other', 'Other', now(), now()) RETURNING id`, u2).Scan(&r2); err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{
		"верхний регистр кода":  "UPDATE resellers SET code = 'Astana' WHERE id = $1",
		"короткий код":          "UPDATE resellers SET code = 'ab' WHERE id = $1",
		"код с дефиса":          "UPDATE resellers SET code = '-abc' WHERE id = $1",
		"процент больше 100 %":  "UPDATE resellers SET commission_bp = 10001 WHERE id = $1",
		"скидка больше 100 %":   "UPDATE resellers SET discount_percent = 101 WHERE id = $1",
		"отрицательный процент": "UPDATE resellers SET commission_bp = -1 WHERE id = $1",
	} {
		if _, err := d.Platform.Exec(ctx, q, r1); code(err) != "23514" {
			t.Errorf("resellers, %s: %v", name, err)
		}
	}
	if _, err := d.Platform.Exec(ctx, "UPDATE resellers SET code = 'other' WHERE id = $1", r1); code(err) != "23505" {
		t.Errorf("resellers: повтор кода: %v", err)
	}

	// Привязку клуба к реселлеру меняет только пул платформы (триггер clubs_reseller_guard).
	inClub := func(pool interface {
		Begin(context.Context) (pgx.Tx, error)
	}, club int64, fn func(tx pgx.Tx)) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(club, 10)); err != nil {
			t.Fatal(err)
		}
		fn(tx)
	}
	inClub(d.App, clubA, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, "UPDATE clubs SET reseller_id = $1, reseller_attached_at = now() WHERE id = $2", r1, clubA); code(err) != "42501" {
			t.Errorf("zafiro_app привязал клуб к реселлеру: %v", err)
		}
	})
	if tag, err := d.Platform.Exec(ctx, "UPDATE clubs SET reseller_id = $1, reseller_attached_at = now() WHERE id = $2 AND reseller_id IS NULL", r1, clubA); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("zafiro_platform привязка клуба: %v", err)
	}
	inClub(d.App, clubA, func(tx pgx.Tx) {
		// Свой клуб по-прежнему меняется (город, адрес, профиль — 0036), реселлер — нет.
		if _, err := tx.Exec(ctx, `UPDATE clubs SET city = 'aktobe', address = 'пр. Абилкайыр хана, 1', profile = '{"phone": "+7 700"}' WHERE id = $1`, clubA); err != nil {
			t.Errorf("zafiro_app город и профиль: %v", err)
		}
		if _, err := tx.Exec(ctx, "SAVEPOINT s"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "UPDATE clubs SET reseller_id = NULL WHERE id = $1", clubA); code(err) != "42501" {
			t.Errorf("zafiro_app отвязал клуб от реселлера: %v", err)
		}
		if _, err := tx.Exec(ctx, "ROLLBACK TO s"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE clubs SET profile = '[]' WHERE id = $1`, clubA); code(err) != "23514" {
			t.Errorf("clubs.profile не объект: %v", err)
		}
	})
	// Пул клуба видит только реселлера своего клуба; клуб без реселлера — никого.
	for club, want := range map[int64]string{clubA: "astana-pro:1235:10", clubB: ""} {
		inClub(d.App, club, func(tx pgx.Tx) {
			var got string
			if err := tx.QueryRow(ctx, "SELECT coalesce(string_agg(code || ':' || commission_bp || ':' || discount_percent, ','), '') FROM resellers").Scan(&got); err != nil || got != want {
				t.Errorf("zafiro_app resellers клуба %d: %q, %v; want %q", club, got, err, want)
			}
		})
	}

	// --- Счета за подписку ---
	insert := `INSERT INTO subscription_invoices (club_id, saas_plan_id, months, computers, price_per_computer, discount_percent, amount,
		reseller_id, reseller_bp, status, created_by, created_at, updated_at) VALUES ($1, $2, $3, 12, 2500000, 10, $4, $5, 1235, $6, $7, now(), now()) RETURNING id`
	var invA int64
	tx, err := d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(clubA, 10)); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, insert, clubA, plan, 3, int64(81000000), r1, "pending", u1).Scan(&invA); err != nil {
		t.Fatalf("zafiro_app выставил счёт: %v", err)
	}
	for name, tc := range map[string]struct {
		want string
		args []any
	}{
		"второй неоплаченный счёт клуба (GAP-046)": {"23505", []any{clubA, plan, 1, int64(27000000), r1, "pending", u1}},
		"счёт сразу оплаченным":                    {"42501", []any{clubA, plan, 1, int64(27000000), r1, "paid", u1}},
		"счёт чужому клубу":                        {"42501", []any{clubB, plan, 1, int64(27000000), nil, "pending", u1}},
		"отменённый счёт сразу":                    {"42501", []any{clubA, plan, 1, int64(27000000), r1, "canceled", u1}},
	} {
		if _, err := tx.Exec(ctx, "SAVEPOINT s"); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := tx.QueryRow(ctx, insert, tc.args...).Scan(&id); code(err) != tc.want {
			t.Errorf("zafiro_app, %s: %v, want %s", name, err, tc.want)
		}
		if _, err := tx.Exec(ctx, "ROLLBACK TO s"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT s"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE subscription_invoices SET status = 'paid', updated_at = now() WHERE id = $1", invA); code(err) != "42501" {
		t.Errorf("zafiro_app отметил счёт оплаченным: %v", err)
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO s"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Значения: срок 1/3/6/12 месяцев, сумма — целые тенге (BR-180), статус из списка.
	for name, args := range map[string][]any{
		"срок не 1/3/6/12 месяцев": {clubB, plan, 2, int64(27000000), nil, "canceled", u1},
		"сумма с тиынами":          {clubB, plan, 1, int64(27000050), nil, "canceled", u1},
		"неизвестный статус":       {clubB, plan, 1, int64(27000000), nil, "open", u1},
	} {
		var id int64
		if err := d.Platform.QueryRow(ctx, insert, args...).Scan(&id); code(err) != "23514" {
			t.Errorf("subscription_invoices, %s: %v", name, err)
		}
	}
	// Чужой клуб счёта не видит и не отменит.
	inClub(d.App, clubB, func(tx pgx.Tx) {
		tag, err := tx.Exec(ctx, "UPDATE subscription_invoices SET status = 'canceled', updated_at = now() WHERE id = $1", invA)
		if err != nil || tag.RowsAffected() != 0 {
			t.Errorf("клуб B отменил счёт клуба A: %v, %d", err, tag.RowsAffected())
		}
	})
	// Оплату отмечает платформа; оплаченный счёт пул клуба уже не тронет (ни отменить, ни «вернуть» в неоплаченные).
	if _, err := d.Platform.Exec(ctx, "UPDATE subscription_invoices SET status = 'paid', updated_at = now() WHERE id = $1", invA); code(err) != "23514" {
		t.Errorf("оплаченный счёт без paid_at: %v", err)
	}
	if _, err := d.Platform.Exec(ctx, "UPDATE subscription_invoices SET reseller_amount = 100 WHERE id = $1", invA); code(err) != "23514" {
		t.Errorf("начисление реселлеру у неоплаченного счёта: %v", err)
	}
	if _, err := d.Platform.Exec(ctx, `UPDATE subscription_invoices SET status = 'paid', paid_at = now(), confirmed_by = $2, period_ends_at = now() + interval '3 months',
		reseller_amount = 10003500, updated_at = now() WHERE id = $1 AND status = 'pending'`, invA, u2); err != nil {
		t.Fatalf("zafiro_platform оплата счёта: %v", err)
	}
	inClub(d.App, clubA, func(tx pgx.Tx) {
		tag, err := tx.Exec(ctx, "UPDATE subscription_invoices SET status = 'canceled', updated_at = now() WHERE id = $1", invA)
		if err != nil || tag.RowsAffected() != 0 {
			t.Errorf("zafiro_app отменил оплаченный счёт: %v, %d", err, tag.RowsAffected())
		}
		var n int64
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM subscription_invoices WHERE status = 'paid'").Scan(&n); err != nil || n != 1 {
			t.Errorf("zafiro_app читает свои счета: %d, %v", n, err)
		}
		// Счёт закрыт — можно выставить новый и отменить его.
		var id int64
		if err := tx.QueryRow(ctx, insert, clubA, plan, 1, int64(27000000), r1, "pending", u1).Scan(&id); err != nil {
			t.Errorf("новый счёт после оплаты: %v", err)
			return
		}
		if tag, err := tx.Exec(ctx, "UPDATE subscription_invoices SET status = 'canceled', updated_at = now() WHERE id = $1 AND status = 'pending'", id); err != nil || tag.RowsAffected() != 1 {
			t.Errorf("zafiro_app отменил свой неоплаченный счёт: %v", err)
		}
	})
	if _, err := d.Platform.Exec(ctx, "DELETE FROM subscription_invoices WHERE id = $1", invA); code(err) != "42501" {
		t.Errorf("zafiro_platform удалил счёт: %v", err)
	}
	// Скидка клуба (0035): целые проценты 0…100.
	one("INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at) VALUES ($1, $2, 'active', now()) RETURNING id", clubA, plan)
	if _, err := d.Platform.Exec(ctx, "UPDATE club_subscriptions SET discount_percent = 100, discount_until = '2026-12-31', discount_note = 'акция' WHERE club_id = $1", clubA); err != nil {
		t.Errorf("скидка клуба: %v", err)
	}
	if _, err := d.Platform.Exec(ctx, "UPDATE club_subscriptions SET discount_percent = 101 WHERE club_id = $1", clubA); code(err) != "23514" {
		t.Errorf("скидка клуба 101 %%: %v", err)
	}

	// --- Выплаты и журнал денег платформы: только добавление ---
	var payout int64
	if err := d.Platform.QueryRow(ctx, `INSERT INTO reseller_payouts (reseller_id, amount, note, created_by, idempotency_key, created_at, updated_at)
		VALUES ($1, 5000000, 'Kaspi', $2, 'form-1', now(), now()) RETURNING id`, r1, u2).Scan(&payout); err != nil {
		t.Fatalf("zafiro_platform выплата: %v", err)
	}
	for name, tc := range map[string]struct{ q, want string }{
		"повтор ключа формы (двойной клик)": {"INSERT INTO reseller_payouts (reseller_id, amount, idempotency_key, created_at) VALUES ($1, 5000000, 'form-1', now())", "23505"},
		"нулевая выплата":                   {"INSERT INTO reseller_payouts (reseller_id, amount, created_at) VALUES ($1, 0, now())", "23514"},
		"исправление выплаты":               {"UPDATE reseller_payouts SET amount = 1 WHERE reseller_id = $1", "42501"},
		"удаление выплаты":                  {"DELETE FROM reseller_payouts WHERE reseller_id = $1", "42501"},
	} {
		if _, err := d.Platform.Exec(ctx, tc.q, r1); code(err) != tc.want {
			t.Errorf("zafiro_platform, %s: %v, want %s", name, err, tc.want)
		}
	}
	logInsert := `INSERT INTO platform_money_log (kind, idempotency_key, amount, club_id, invoice_id, reseller_id, payout_id, actor_user_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	for _, row := range [][]any{
		{"invoice_paid", "invoice_paid:" + strconv.FormatInt(invA, 10), int64(81000000), clubA, invA, r1, nil, u2},
		{"reseller_accrual", "reseller_accrual:" + strconv.FormatInt(invA, 10), int64(10003500), clubA, invA, r1, nil, u2},
		{"reseller_payout", "reseller_payout:" + strconv.FormatInt(payout, 10), int64(5000000), nil, nil, r1, payout, u2},
	} {
		if _, err := d.Platform.Exec(ctx, logInsert, row...); err != nil {
			t.Fatalf("zafiro_platform журнал %v: %v", row[0], err)
		}
	}
	for name, tc := range map[string]struct {
		want string
		row  []any
	}{
		"повтор ключа идемпотентности": {"23505", []any{"invoice_paid", "invoice_paid:" + strconv.FormatInt(invA, 10), int64(1), clubA, invA, nil, nil, u2}},
		"оплата без счёта":             {"23514", []any{"invoice_paid", "k1", int64(1), clubA, nil, nil, nil, u2}},
		"начисление без реселлера":     {"23514", []any{"reseller_accrual", "k2", int64(1), clubA, invA, nil, nil, u2}},
		"выплата без выплаты":          {"23514", []any{"reseller_payout", "k3", int64(1), nil, nil, r1, nil, u2}},
		"нулевая выплата":              {"23514", []any{"reseller_payout", "k4", int64(0), nil, nil, r1, payout, u2}},
		"отрицательная сумма":          {"23514", []any{"invoice_paid", "k5", int64(-1), clubA, invA, nil, nil, u2}},
		"неизвестный вид":              {"23514", []any{"refund", "k6", int64(1), clubA, invA, nil, nil, u2}},
	} {
		if _, err := d.Platform.Exec(ctx, logInsert, tc.row...); code(err) != tc.want {
			t.Errorf("журнал денег платформы, %s: %v, want %s", name, err, tc.want)
		}
	}
	for _, q := range []string{"UPDATE platform_money_log SET amount = 0", "DELETE FROM platform_money_log", "TRUNCATE platform_money_log"} {
		if _, err := d.Platform.Exec(ctx, q); code(err) != "42501" {
			t.Errorf("zafiro_platform %q: %v", q, err)
		}
		// Владельцу схемы (обходит права) мешает триггер «только добавление».
		if _, err := d.Owner.Exec(ctx, q); code(err) != "42501" {
			t.Errorf("владелец %q: %v", q, err)
		}
	}
	// Удаление реселлера (BR-189): выплаты уходят каскадом, клуб отвязан, в счёте реселлера нет, а начисление и
	// журнал остаются (UQ-10).
	if _, err := d.Platform.Exec(ctx, "DELETE FROM resellers WHERE id = $1", r1); err != nil {
		t.Fatalf("zafiro_platform удаление реселлера: %v", err)
	}
	if got := one(`SELECT (SELECT count(*) FROM reseller_payouts) * 1000 + (SELECT count(*) FROM clubs WHERE reseller_id IS NOT NULL) * 100
		+ (SELECT count(*) FROM subscription_invoices WHERE reseller_id IS NOT NULL) * 10 + (SELECT count(*) FROM subscription_invoices WHERE reseller_amount = 10003500)`); got != 1 {
		t.Errorf("после удаления реселлера: %d, want 1", got)
	}
	if got := one("SELECT coalesce(sum(amount), 0)::bigint FROM platform_money_log WHERE reseller_id = $1", r1); got != 81000000+10003500+5000000 {
		t.Errorf("журнал денег платформы после удаления реселлера: %d", got)
	}

	// --- Фото клуба: только свой клуб ---
	inClub(d.App, clubA, func(tx pgx.Tx) {
		var id int64
		if err := tx.QueryRow(ctx, "INSERT INTO club_photos (club_id, path, sort_order, created_at, updated_at) VALUES ($1, 'club-photos/1/a.png', -1, now(), now()) RETURNING id", clubA).Scan(&id); err != nil {
			t.Fatalf("zafiro_app фото: %v", err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO club_photo_images (photo_id, club_id, content_type, data, created_at) VALUES ($1, $2, 'image/webp', '\\x52494646', now())", id, clubA); err != nil {
			t.Fatalf("zafiro_app байты фото: %v", err)
		}
		for name, tc := range map[string]struct{ q, want string }{
			"чужой клуб":            {"INSERT INTO club_photos (club_id, path, created_at) VALUES ($2, 'club-photos/2/b.png', now()) RETURNING $1::bigint + $3::bigint", "42501"},
			"повтор пути":           {"INSERT INTO club_photos (club_id, path, created_at) VALUES ($3, 'club-photos/1/a.png', now()) RETURNING $1::bigint + $2::bigint", "23505"},
			"не картинка":           {"UPDATE club_photo_images SET content_type = 'image/gif' WHERE photo_id = $1 AND $2::bigint + $3::bigint > 0", "23514"},
			"оборудование зон":      {"INSERT INTO club_zones (club_id, name, devices, created_at) VALUES ($3, 'z', '\"cpu\"', now()) RETURNING $1::bigint + $2::bigint", "23514"},
			"бронь с кассы с кодом": {"INSERT INTO computer_reservations (club_id, club_computer_id, starts_at, ends_at, code) VALUES ($3, 0, now(), now(), '123456') RETURNING $1::bigint + $2::bigint", "23514"},
		} {
			if _, err := tx.Exec(ctx, "SAVEPOINT s"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, tc.q, id, clubB, clubA); code(err) != tc.want {
				t.Errorf("zafiro_app, %s: %v, want %s", name, err, tc.want)
			}
			if _, err := tx.Exec(ctx, "ROLLBACK TO s"); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	inClub(d.App, clubB, func(tx pgx.Tx) {
		var n int64
		if err := tx.QueryRow(ctx, "SELECT (SELECT count(*) FROM club_photos) + (SELECT count(*) FROM club_photo_images)").Scan(&n); err != nil || n != 0 {
			t.Errorf("клуб B видит фото клуба A: %d, %v", n, err)
		}
	})
	// Публичная отдача фото и каталог — пул платформы.
	if got := one("SELECT count(*) FROM club_photo_images"); got != 1 {
		t.Fatalf("фото: %d", got)
	}
	var n int64
	if err := d.Platform.QueryRow(ctx, "SELECT count(*) FROM club_photos p JOIN club_photo_images i ON i.photo_id = p.id WHERE p.club_id = $1", clubA).Scan(&n); err != nil || n != 1 {
		t.Errorf("zafiro_platform фото: %d, %v", n, err)
	}
}
