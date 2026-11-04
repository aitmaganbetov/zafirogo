//go:build integration

package migrate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate/migratetest"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// clubFingerprint — строки клуба во всех таблицах с club_id (под ролью владельца-суперпользователя теста RLS не мешает)
// и суммы ledger: «другой клуб не тронут».
func clubFingerprint(t *testing.T, d *dbtest.DB, club int64) string {
	t.Helper()
	return scalarPG[string](t, d.Owner, `
		SELECT string_agg(format('%s=%s', c.relname,
		         (xpath('/row/n/text()', query_to_xml(format('SELECT count(*) AS n FROM %I WHERE club_id = %s', c.relname, $1::bigint), false, true, '')))[1]::text),
		       ',' ORDER BY c.relname)
		       || ';ledger=' || (SELECT COALESCE(sum(abs(amount)), 0) FROM ledger_entries WHERE club_id = $1)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
		  AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'club_id' AND NOT a.attisdropped)`, club)
}

// clubRows — строк клуба во всех таблицах с club_id (включая партиции ledger/аудита через родителя).
func clubRows(t *testing.T, d *dbtest.DB, club int64) int64 {
	t.Helper()
	return scalarPG[int64](t, d.Owner, `
		SELECT COALESCE(sum((xpath('/row/n/text()', query_to_xml(format('SELECT count(*) AS n FROM %I WHERE club_id = %s', c.relname, $1::bigint), false, true, '')))[1]::text::bigint), 0)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
		  AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'club_id' AND NOT a.attisdropped)`, club)
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// S5-M3-1, S5-M3-2: apply → imported (подписка закрыта) → purge → apply снова → verify без расхождений → go-live
// (подписка открыта, состояние live окончательно) → purge отклоняется. Другой клуб не тронут; сервер и суперадминка
// функцию удаления вызвать не могут; журналы по-прежнему только дополняются.
func TestPurgeAndGoLive(t *testing.T) {
	ctx := context.Background()
	my, _ := migratetest.StartMySQL(t)
	pgdb := dbtest.New(t)
	owner := pgdb.Owner
	now := time.Now().UTC()
	// Общий аккаунт: клиент 6 клуба 1 — ещё и клиент клуба 2 (после удаления клуба 1 должен остаться).
	execMy(t, my, "INSERT INTO club_memberships (club_id, user_id, role, discount, total_spent, created_at, updated_at) VALUES (2, 6, 'client', 0, 0, ?, ?)",
		now.Format(time.DateTime), now.Format(time.DateTime))
	execMy(t, my, "INSERT INTO member_balances (club_id, user_id, balance, bonus_balance, created_at, updated_at) VALUES (2, 6, 10.00, 0, ?, ?)",
		now.Format(time.DateTime), now.Format(time.DateTime))
	// Клуб 1 заморожен в старом Laravel (S5-M2-1).
	execMy(t, my, "UPDATE club_subscriptions SET status = 'suspended', canceled_at = ? WHERE club_id = 1", now.Format(time.DateTime))
	opts := func(club string) migrate.Options {
		return migrate.Options{Club: club, IDReserve: idReserve, Now: now, ThawSubscription: "active"}
	}

	if r, err := migrate.Apply(ctx, my, owner, opts("zafiro-lite")); err != nil {
		t.Fatalf("apply клуба 2: %v\n%s", err, reportText(r))
	}
	if r, err := migrate.GoLive(ctx, owner, "zafiro-lite"); err != nil {
		t.Fatalf("go-live клуба 2: %v\n%s", err, reportText(r))
	}
	if r, err := migrate.Apply(ctx, my, owner, opts("zafiro-arena")); err != nil {
		t.Fatalf("apply клуба 1: %v\n%s", err, reportText(r))
	}
	club2 := clubFingerprint(t, pgdb, 2)

	// imported: клуб закрыт до go-live — подписка suspended (все входы клуба отвечают 402).
	if got := scalarPG[string](t, owner, "SELECT migration_state FROM clubs WHERE id = 1"); got != migrate.StateImported {
		t.Fatalf("состояние после apply: %q", got)
	}
	if got := scalarPG[string](t, owner, "SELECT status FROM club_subscriptions WHERE club_id = 1"); got != "suspended" {
		t.Fatalf("подписка до go-live: %q", got)
	}

	// Функцию удаления не вызвать ни серверу, ни суперадминке; журналы без неё — только добавление.
	for name, pool := range map[string]interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	}{"zafiro_app": pgdb.App, "zafiro_platform": pgdb.Platform} {
		_, err := pool.Exec(ctx, "SELECT zafiro_purge_imported_club(1, 'zafiro-arena', '{}')")
		if pgCode(err) != "42501" {
			t.Fatalf("%s вызвал zafiro_purge_imported_club: %v", name, err)
		}
	}
	if _, err := owner.Exec(ctx, "DELETE FROM ledger_entries WHERE club_id = 1"); pgCode(err) != "42501" {
		t.Fatalf("ledger без purge: %v", err)
	}

	// Подтверждение — slug клуба; не совпало — ничего не удалено.
	before := clubRows(t, pgdb, 1)
	if r, err := migrate.Purge(ctx, owner, "1", "zafiro-lite"); !errors.Is(err, migrate.ErrConfirm) {
		t.Fatalf("purge с чужим slug: %v\n%s", err, reportText(r))
	}
	if got := clubRows(t, pgdb, 1); got != before || got == 0 {
		t.Fatalf("строк клуба 1: %d → %d", before, got)
	}

	purged, err := migrate.Purge(ctx, owner, "zafiro-arena", "zafiro-arena")
	if err != nil {
		t.Fatalf("purge: %v\n%s", err, reportText(purged))
	}
	t.Logf("purge:\n%s", reportText(purged))
	if got := clubRows(t, pgdb, 1); got != 0 {
		t.Fatalf("после purge осталось строк клуба 1: %d", got)
	}
	if scalarPG[bool](t, owner, "SELECT EXISTS (SELECT 1 FROM clubs WHERE id = 1)") {
		t.Fatal("клуб 1 не удалён")
	}
	if got := clubFingerprint(t, pgdb, 2); got != club2 {
		t.Fatalf("клуб 2 изменился:\n%s\n%s", club2, got)
	}
	// Аккаунты только клуба 1 удалены; общий (6) и суперадмин (1) остались.
	if scalarPG[bool](t, owner, "SELECT EXISTS (SELECT 1 FROM users WHERE id = 7)") {
		t.Fatal("аккаунт только клуба 1 не удалён")
	}
	if got := scalarPG[int64](t, owner, "SELECT count(*) FROM users WHERE id = 6"); got != 1 {
		t.Fatal("общий аккаунт 6 удалён")
	}
	if got := scalarPG[int64](t, owner, "SELECT count(*) FROM platform_logs WHERE event = 'migration.club_purged' AND subject_id = 1"); got != 1 {
		t.Fatalf("журнал платформы: %d", got)
	}
	if v, err := migrate.Verify(ctx, my, owner, opts("2")); err != nil {
		t.Fatalf("verify клуба 2 после purge клуба 1: %v\n%s", err, reportText(v))
	}

	// Повторный перенос с чистого листа — сверка без расхождений.
	if r, err := migrate.Apply(ctx, my, owner, opts("zafiro-arena")); err != nil {
		t.Fatalf("повторный apply: %v\n%s", err, reportText(r))
	}
	if v, err := migrate.Verify(ctx, my, owner, opts("1")); err != nil || v.Count(migrate.Blocker) != 0 {
		t.Fatalf("verify после повторного переноса: %v\n%s", err, reportText(v))
	}

	// Клуб «открыли» мимо go-live (операция в ledger) — удалить уже нельзя.
	err = db.InTenantTx(ctx, pgdb.App, 1, func(q *store.Queries) error {
		_, err := billing.Charge(ctx, q, billing.ChargeParams{ClubID: 1, UserID: 9, Kind: ledger.KindTariffPurchase,
			Revenue: ledger.AccountRevenueTime, Main: 1_00, IdempotencyKey: "test:purge-in-use"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := migrate.Purge(ctx, owner, "1", "zafiro-arena"); !errors.Is(err, migrate.ErrClubInUse) {
		t.Fatalf("purge клуба с операцией: %v\n%s", err, reportText(r))
	}

	// go-live: подписка — статус до заморозки, canceled_at — NULL; состояние live.
	live, err := migrate.GoLive(ctx, owner, "1")
	if err != nil {
		t.Fatalf("go-live: %v\n%s", err, reportText(live))
	}
	if got := scalarPG[string](t, owner, "SELECT status || ':' || (canceled_at IS NULL) FROM club_subscriptions WHERE club_id = 1"); got != "active:true" {
		t.Fatalf("подписка после go-live: %q", got)
	}
	if got := scalarPG[string](t, owner, "SELECT migration_state FROM clubs WHERE id = 1"); got != migrate.StateLive {
		t.Fatalf("состояние после go-live: %q", got)
	}
	if got := scalarPG[int64](t, owner, "SELECT count(*) FROM audit_log WHERE club_id = 1 AND action = 'migration.club_live'"); got != 1 {
		t.Fatalf("аудит go-live: %d", got)
	}
	if r, err := migrate.GoLive(ctx, owner, "1"); !errors.Is(err, migrate.ErrClubState) {
		t.Fatalf("повторный go-live: %v\n%s", err, reportText(r))
	}
	// После go-live — навсегда: purge отклоняется, вернуть imported нельзя, функция и триггер журналов отказывают.
	if r, err := migrate.Purge(ctx, owner, "1", "zafiro-arena"); !errors.Is(err, migrate.ErrClubState) {
		t.Fatalf("purge после go-live: %v\n%s", err, reportText(r))
	}
	if _, err := owner.Exec(ctx, "UPDATE clubs SET migration_state = 'imported' WHERE id = 1"); pgCode(err) != "42501" {
		t.Fatalf("live → imported: %v", err)
	}
	if _, err := owner.Exec(ctx, "SELECT zafiro_purge_imported_club(1, 'zafiro-arena', '{}')"); pgCode(err) != "55000" {
		t.Fatalf("функция для клуба live: %v", err)
	}
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('zafiro.purge_club_id', '1', true)"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM ledger_entries WHERE club_id = 1"); pgCode(err) != "42501" {
		t.Fatalf("ledger клуба live с настройкой purge: %v", err)
	}
	if got := scalarPG[int64](t, owner, "SELECT count(*) FROM zafiro_tables_without_rls()"); got != 0 {
		t.Fatalf("таблицы без RLS: %d", got)
	}
}
