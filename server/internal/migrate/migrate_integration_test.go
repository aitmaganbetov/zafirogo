//go:build integration

package migrate_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate/migratetest"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const idReserve = 1_000_000_000

func execMy(t *testing.T, my *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := my.Exec(q, args...); err != nil {
		t.Fatalf("mysql: %s: %v", q, err)
	}
}

func scalarMy[T any](t *testing.T, my *sql.DB, q string, args ...any) T {
	t.Helper()
	var v T
	if err := my.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("mysql: %s: %v", q, err)
	}
	return v
}

func scalarPG[T any](t *testing.T, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, query string, args ...any) T {
	t.Helper()
	var v T
	if err := q.QueryRow(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("pg: %s: %v", query, err)
	}
	return v
}

func requireFinding(t *testing.T, r *migrate.Report, sev migrate.Severity, code string) {
	t.Helper()
	if !r.Has(sev, code) {
		var b []byte
		buf := &bytesWriter{b: &b}
		_ = r.WriteText(buf)
		t.Fatalf("в отчёте нет находки %s [%s]:\n%s", sev, code, b)
	}
}

type bytesWriter struct{ b *[]byte }

func (w *bytesWriter) Write(p []byte) (int, error) { *w.b = append(*w.b, p...); return len(p), nil }

func reportText(r *migrate.Report) string {
	var b []byte
	_ = r.WriteText(&bytesWriter{b: &b})
	return string(b)
}

// injectProblems — данные, которых нет в демо-фикстуре, но бывают в проде (решения Q-004 … Q-030).
func injectProblems(t *testing.T, my *sql.DB, now time.Time) {
	utc := func(d time.Duration) string { return now.Add(d).UTC().Format(time.DateTime) }
	// Клуб 1: настройки вне формата (Q-023), старая роль (Q-027), журнал 2023 года (партиции), created_at NULL
	// (Q-030), журнал API в сроке хранения и старше его, пакет минут.
	execMy(t, my, `UPDATE clubs SET settings = '{"club":{"session_idle_timeout":"30 сек","booking_interval":30},"finances":{"tax_rate":12,"loyalty_enabled":true}}' WHERE id = 1`)
	execMy(t, my, "UPDATE club_memberships SET role = 'Пользователь' WHERE club_id = 1 AND user_id = 13")
	execMy(t, my, `INSERT INTO activity_logs (event, description, user_id, ip_address, created_at, updated_at, club_id)
		VALUES ('auth.login', 'Вход в систему', 2, '127.0.0.1', '2023-05-10 10:00:00', '2023-05-10 10:00:00', 1)`)
	execMy(t, my, `INSERT INTO activity_logs (event, description, user_id, ip_address, created_at, updated_at, club_id)
		VALUES ('auth.login', 'Вход в систему', 2, '127.0.0.1', NULL, '2026-10-01 00:00:00', 1)`)
	execMy(t, my, `INSERT INTO api_request_logs (club_id, method, endpoint, status, duration_ms, ip, created_at)
		VALUES (1, 'GET', '/api/v1/customers', 200, 12, '10.0.0.1', ?), (1, 'GET', '/api/v1/customers', 200, 15, '10.0.0.1', ?)`,
		utc(-24*time.Hour), utc(-200*24*time.Hour))
	execMy(t, my, `INSERT INTO time_packages (club_id, user_id, tariff_id, name, minutes_left, expires_at, created_at, updated_at)
		VALUES (1, 6, NULL, 'Ночь', 90, ?, ?, ?)`, utc(72*time.Hour), utc(-time.Hour), utc(-time.Hour))

	// Клуб 2: общий аккаунт 6 с отрицательным балансом (Q-004), зона и ПК со статусом вне CHECK (Q-026).
	execMy(t, my, "INSERT INTO club_memberships (club_id, user_id, role, discount, total_spent, created_at, updated_at) VALUES (2, 6, 'client', 0, 1500.25, ?, ?)",
		utc(-time.Hour), utc(-time.Hour))
	execMy(t, my, "INSERT INTO member_balances (club_id, user_id, balance, bonus_balance, created_at, updated_at) VALUES (2, 6, -150.50, 20.05, ?, ?)",
		utc(-time.Hour), utc(-time.Hour))
	execMy(t, my, `INSERT INTO club_zones (id, club_id, name, icon, theme, status, sort_order, position_x, position_y, width, height, price_per_hour, created_at, updated_at)
		VALUES (100, 2, 'Общий зал', 'desktop', 'neutral', 'available', 0, 20, 20, 320, 180, 400.50, ?, ?)`, utc(-time.Hour), utc(-time.Hour))
	execMy(t, my, `INSERT INTO club_computers (id, club_id, club_zone_id, number, status, position_x, position_y, created_at, updated_at)
		VALUES (100, 2, 100, 1, 'busy', 5, 5, ?, ?)`, utc(-time.Hour), utc(-time.Hour))
}

func TestMigrateDemoClubs(t *testing.T) {
	ctx := context.Background()
	my, _ := migratetest.StartMySQL(t)
	pgdb := dbtest.New(t)
	owner := pgdb.Owner
	now := time.Now().UTC()
	injectProblems(t, my, now)
	opts := func(club string) migrate.Options {
		return migrate.Options{Club: club, IDReserve: idReserve, BatchSize: 3, Now: now}
	}

	// --- Клуб 1: plan → apply → verify ---
	plan, err := migrate.Plan(ctx, my, owner, opts("zafiro-arena"))
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, reportText(plan))
	}
	if n := plan.Count(migrate.Blocker); n != 0 {
		t.Fatalf("plan клуба 1: блокеров %d\n%s", n, reportText(plan))
	}
	requireFinding(t, plan, migrate.Warning, migrate.CodeOldRole)
	requireFinding(t, plan, migrate.Warning, migrate.CodeSettingsFormat)
	requireFinding(t, plan, migrate.Warning, migrate.CodeNullCreatedAt)
	requireFinding(t, plan, migrate.Info, migrate.CodeRetention)
	requireFinding(t, plan, migrate.Info, migrate.CodeSkippedTable) // agent_qr_logins
	if got := plan.Stat("products").Converted["price"]; got != "тиыны ×100" {
		t.Fatalf("products.price: %q", got)
	}
	if got := plan.Stat("club_computers").Source; got != scalarMy[int64](t, my, "SELECT COUNT(*) FROM club_computers WHERE club_id = 1") {
		t.Fatalf("club_computers в плане: %d", got)
	}
	if scalarPG[bool](t, owner, "SELECT EXISTS (SELECT 1 FROM clubs WHERE id = 1)") {
		t.Fatal("plan записал данные")
	}

	applied, err := migrate.Apply(ctx, my, owner, opts("1"))
	if err != nil {
		t.Fatalf("apply клуба 1: %v\n%s", err, reportText(applied))
	}
	if !scalarPG[bool](t, owner, "SELECT to_regclass('public.activity_logs_2023_05') IS NOT NULL") {
		t.Fatal("нет партиции activity_logs_2023_05 под старый журнал")
	}
	verified, err := migrate.Verify(ctx, my, owner, opts("zafiro-arena"))
	if err != nil {
		t.Fatalf("verify клуба 1: %v\n%s", err, reportText(verified))
	}
	t.Logf("сверка клуба 1:\n%s", reportText(verified))

	// Деньги: decimal × 100 точно; балансы — через ledger (входящие остатки), проекция = ledger.
	club1Balances := checkBalances(t, my, pgdb, 1)
	if got := scalarPG[int64](t, owner, "SELECT price FROM products WHERE club_id = 1 ORDER BY id LIMIT 1"); got !=
		tiyn(t, scalarMy[string](t, my, "SELECT price FROM products WHERE club_id = 1 ORDER BY id LIMIT 1")) {
		t.Fatalf("products.price = %d", got)
	}
	if got := scalarPG[int64](t, owner,
		"SELECT count(*) FROM ledger_transactions WHERE club_id = 1 AND kind = 'migration_opening_balance' AND idempotency_key LIKE 'opening_balance:%'"); got !=
		scalarMy[int64](t, my, "SELECT COUNT(*) FROM member_balances WHERE club_id = 1 AND (balance <> 0 OR bonus_balance <> 0)") {
		t.Fatalf("входящих остатков: %d", got)
	}
	// Время: строки UTC Laravel → timestamptz.
	if got := scalarPG[time.Time](t, owner, "SELECT opened_at FROM cash_shifts WHERE id = 3"); !got.Equal(time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("cash_shifts.opened_at = %s", got)
	}
	// Q-027: старая роль → client; Q-050: ключ установки не перенесён; журнал API старше срока — не перенесён.
	if got := scalarPG[string](t, owner, "SELECT role FROM club_memberships WHERE club_id = 1 AND user_id = 13"); got != "client" {
		t.Fatalf("роль: %q", got)
	}
	if got := scalarPG[int64](t, owner, "SELECT count(*) FROM api_request_logs WHERE club_id = 1"); got != 1 {
		t.Fatalf("api_request_logs: %d", got)
	}
	// Касса: остаток открытой смены = входящий остаток cash_drawer.
	if got := scalarPG[int64](t, owner, `SELECT COALESCE(-sum(e.amount), 0)::bigint FROM ledger_entries e
		JOIN ledger_accounts a ON a.club_id = e.club_id AND a.id = e.account_id WHERE a.club_id = 1 AND a.kind = 'cash_drawer'`); got <= 0 {
		t.Fatalf("cash_drawer = %d", got)
	}
	// Пакет минут — входящий остаток в ledger минут.
	if got := scalarPG[int64](t, owner, "SELECT COALESCE(sum(minutes), 0)::bigint FROM ledger_minute_entries WHERE club_id = 1"); got != 90 {
		t.Fatalf("минуты: %d", got)
	}
	// Аудит переноса.
	if got := scalarPG[int64](t, owner, "SELECT count(*) FROM audit_log WHERE club_id = 1 AND action = 'migration.club_imported'"); got != 1 {
		t.Fatalf("аудит: %d", got)
	}

	// --- Повторный apply: отказ, ничего не меняется ---
	before := tableCounts(t, owner)
	again, err := migrate.Apply(ctx, my, owner, opts("zafiro-arena"))
	if !errors.Is(err, migrate.ErrAlreadyMigrated) {
		t.Fatalf("повторный apply: %v\n%s", err, reportText(again))
	}
	if after := tableCounts(t, owner); after != before {
		t.Fatalf("повторный apply изменил данные: %s → %s", before, after)
	}

	// --- Клуб 2: блокеры (Q-026 в MySQL, Q-025 — slug занят клубом zafirogo) → откат; исправили → перенос ---
	if _, err := owner.Exec(ctx, "INSERT INTO clubs (id, name, slug, created_at) VALUES (900000000001, 'Новый клуб', 'Zafiro-Lite', now())"); err != nil {
		t.Fatal(err)
	}
	plan2, err := migrate.Plan(ctx, my, owner, opts("zafiro-lite"))
	if err != nil {
		t.Fatalf("plan клуба 2: %v", err)
	}
	t.Logf("план клуба 2:\n%s", reportText(plan2))
	requireFinding(t, plan2, migrate.Blocker, migrate.CodeEnumOutsideCheck)
	requireFinding(t, plan2, migrate.Blocker, migrate.CodeCaseConflict)
	requireFinding(t, plan2, migrate.Warning, migrate.CodeNegativeBalance)
	requireFinding(t, plan2, migrate.Info, migrate.CodeUserKept) // аккаунт 6 уже перенесён с клубом 1
	blocked, err := migrate.Apply(ctx, my, owner, opts("zafiro-lite"))
	if !errors.Is(err, migrate.ErrBlocked) {
		t.Fatalf("apply с блокерами: %v\n%s", err, reportText(blocked))
	}
	if scalarPG[bool](t, owner, "SELECT EXISTS (SELECT 1 FROM clubs WHERE id = 2)") {
		t.Fatal("apply с блокерами записал клуб")
	}
	execMy(t, my, "UPDATE club_computers SET status = 'available' WHERE id = 100")
	if _, err := owner.Exec(ctx, "DELETE FROM clubs WHERE id = 900000000001"); err != nil {
		t.Fatal(err)
	}
	applied2, err := migrate.Apply(ctx, my, owner, opts("zafiro-lite"))
	if err != nil {
		t.Fatalf("apply клуба 2: %v\n%s", err, reportText(applied2))
	}
	requireFinding(t, applied2, migrate.Warning, migrate.CodeNegativeBalance)
	if v, err := migrate.Verify(ctx, my, owner, opts("2")); err != nil {
		t.Fatalf("verify клуба 2: %v\n%s", err, reportText(v))
	}
	// Перенос второго клуба не задел первый: сверка клуба 1 по-прежнему чистая, балансы те же.
	if v, err := migrate.Verify(ctx, my, owner, opts("1")); err != nil {
		t.Fatalf("verify клуба 1 после клуба 2: %v\n%s", err, reportText(v))
	}
	if got := checkBalances(t, my, pgdb, 1); !maps.Equal(got, club1Balances) {
		t.Fatalf("балансы клуба 1 изменились: %v → %v", club1Balances, got)
	}
	checkBalances(t, my, pgdb, 2)
	// Общий аккаунт — один, в двух клубах; отрицательный остаток — входящим остатком (Q-004).
	if got := scalarPG[int64](t, owner, "SELECT count(*) FROM users WHERE id = 6"); got != 1 {
		t.Fatalf("аккаунт 6: %d строк", got)
	}
	if got := scalarPG[int64](t, owner, "SELECT balance FROM member_balances WHERE club_id = 2 AND user_id = 6"); got != -15050 {
		t.Fatalf("отрицательный баланс: %d", got)
	}
	if got := scalarPG[int64](t, owner, "SELECT price_per_hour FROM club_zones WHERE id = 100"); got != 40050 {
		t.Fatalf("price_per_hour: %d", got)
	}

	// --- Новый сервер на перенесённых данных (логин приложения, RLS) ---
	if got := scalarPG[int64](t, owner, "SELECT count(*) FROM zafiro_tables_without_rls()"); got != 0 {
		t.Fatalf("таблицы без RLS: %d", got)
	}
	countPCs := func(club int64) int64 {
		var n int64
		err := pgx.BeginTxFunc(ctx, pgdb.App, pgx.TxOptions{}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(club, 10)); err != nil {
				return err
			}
			return tx.QueryRow(ctx, "SELECT count(*) FROM club_computers").Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	seen1, seen2 := countPCs(1), countPCs(2)
	if seen1 != scalarMy[int64](t, my, "SELECT COUNT(*) FROM club_computers WHERE club_id = 1") || seen2 != 1 {
		t.Fatalf("RLS: клуб 1 видит %d ПК, клуб 2 — %d", seen1, seen2)
	}
	// Списание через billing (ledger) на перенесённом балансе и запись журнала: id новых строк — выше id MySQL.
	maxLog := scalarMy[int64](t, my, "SELECT MAX(id) FROM activity_logs")
	err = db.InTenantTx(ctx, pgdb.App, 1, func(q *store.Queries) error {
		res, err := billing.Charge(ctx, q, billing.ChargeParams{
			ClubID: 1, UserID: 9, Kind: ledger.KindTariffPurchase, Revenue: ledger.AccountRevenueTime,
			Main: 100_00, IdempotencyKey: "test:after-migration",
		})
		if err != nil {
			return err
		}
		if want := club1Balances[9][0] - 100_00; int64(res.NewBalance) != want {
			return fmt.Errorf("баланс после списания %d, ожидали %d", res.NewBalance, want)
		}
		id, err := activity.Record(ctx, q, activity.Entry{ClubID: 1, Event: "tariff.purchased", Description: "Покупка тарифа", UserID: 9,
			Meta: &activity.RequestMeta{IP: "127.0.0.1"}})
		if err != nil {
			return err
		}
		if id <= maxLog+idReserve {
			return fmt.Errorf("id новой записи журнала %d не выше id MySQL %d + запас", id, maxLog)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Списание сверх перенесённого остатка ledger не пропускает.
	err = db.InTenantTx(ctx, pgdb.App, 2, func(q *store.Queries) error {
		_, err := billing.Charge(ctx, q, billing.ChargeParams{
			ClubID: 2, UserID: 6, Kind: ledger.KindTariffPurchase, Revenue: ledger.AccountRevenueTime,
			Main: 1, IdempotencyKey: "test:negative",
		})
		return err
	})
	var insufficient *ledger.InsufficientFundsError
	if !errors.As(err, &insufficient) {
		t.Fatalf("списание с отрицательного баланса: %v", err)
	}
}

// checkBalances — баланс каждого клиента в Postgres (через ledger и проекцию) = MySQL × 100.
func checkBalances(t *testing.T, my *sql.DB, d *dbtest.DB, club int64) map[int64][2]int64 {
	t.Helper()
	rows, err := my.Query("SELECT user_id, balance, bonus_balance FROM member_balances WHERE club_id = ?", club)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64][2]int64{}
	for rows.Next() {
		var uid int64
		var main, bonus string
		if err := rows.Scan(&uid, &main, &bonus); err != nil {
			t.Fatal(err)
		}
		out[uid] = [2]int64{tiyn(t, main), tiyn(t, bonus)}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	err = db.InTenantTx(context.Background(), d.App, club, func(q *store.Queries) error {
		for uid, want := range out {
			b, err := q.GetMemberBalance(context.Background(), store.GetMemberBalanceParams{ClubID: club, UserID: uid})
			if err != nil {
				return fmt.Errorf("user %d: %w", uid, err)
			}
			if b.Balance != want[0] || b.BonusBalance != want[1] {
				return fmt.Errorf("user %d: Postgres %d/%d, MySQL×100 %d/%d", uid, b.Balance, b.BonusBalance, want[0], want[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func tiyn(t *testing.T, s string) int64 {
	t.Helper()
	v, err := money.ParseTenge(s)
	if err != nil {
		t.Fatalf("сумма %q: %v", s, err)
	}
	return int64(v)
}

// tableCounts — отпечаток данных для проверки «повторный apply ничего не меняет».
func tableCounts(t *testing.T, owner interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) string {
	t.Helper()
	return scalarPG[string](t, owner, `SELECT concat_ws(',',
		(SELECT count(*) FROM clubs), (SELECT count(*) FROM users), (SELECT count(*) FROM club_memberships),
		(SELECT count(*) FROM ledger_transactions), (SELECT count(*) FROM ledger_entries), (SELECT count(*) FROM activity_logs),
		(SELECT count(*) FROM audit_log), (SELECT COALESCE(sum(balance), 0) FROM member_balances))`)
}
