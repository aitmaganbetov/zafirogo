//go:build integration

package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Одна база на пакет (TestMain).
var fixDB *dbtest.DB

func TestMain(m *testing.M) {
	d, cleanup, err := dbtest.Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fixDB = d
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func testDB(t *testing.T) *dbtest.DB {
	t.Helper()
	return fixDB
}

func credit(clubID, userID int64, amount money.Tiyn, key string) ledger.Operation {
	return ledger.Operation{
		ClubID: clubID, Kind: ledger.KindOwnerTopUp, IdempotencyKey: key,
		Entries: []ledger.Entry{
			{Account: ledger.Main(userID), Amount: amount},
			{Account: ledger.ClubAccount(ledger.AccountOwnerAdjustment), Amount: -amount},
		},
	}
}

func debit(clubID, userID int64, amount money.Tiyn, key string) ledger.Operation {
	return ledger.Operation{
		ClubID: clubID, Kind: ledger.KindOwnerWriteOff, IdempotencyKey: key,
		Entries: []ledger.Entry{
			{Account: ledger.Main(userID), Amount: -amount},
			{Account: ledger.ClubAccount(ledger.AccountOwnerAdjustment), Amount: amount},
		},
	}
}

func post(t *testing.T, d *dbtest.DB, op ledger.Operation) (ledger.Result, error) {
	t.Helper()
	var res ledger.Result
	err := db.InTenantTx(context.Background(), d.App, op.ClubID, func(q *store.Queries) error {
		var err error
		res, err = ledger.Post(context.Background(), q, op)
		return err
	})
	return res, err
}

func TestPost_Idempotency(t *testing.T) {
	d := testDB(t)
	club := d.Club(t, "idem")
	user := d.Customer(t, club, "Арман")

	first, err := post(t, d, credit(club, user, 10000, "req-1"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed {
		t.Fatal("first post must not be a replay")
	}
	if got, _ := first.BalanceAfter(ledger.Main(user)); got != 10000 {
		t.Fatalf("balance after = %d, want 10000", got)
	}

	t.Run("same key, same body → previous result", func(t *testing.T) {
		again, err := post(t, d, credit(club, user, 10000, "req-1"))
		if err != nil {
			t.Fatal(err)
		}
		if !again.Replayed || again.TransactionID != first.TransactionID {
			t.Fatalf("replay = %+v, want transaction %d replayed", again, first.TransactionID)
		}
		if b, _ := again.BalanceAfter(ledger.Main(user)); b != 10000 {
			t.Fatalf("replayed balance after = %d", b)
		}
		if main, _ := d.Balances(t, club, user); main != 10000 {
			t.Fatalf("balance = %d, want 10000 (applied once)", main)
		}
	})

	t.Run("same key, different body → conflict", func(t *testing.T) {
		_, err := post(t, d, credit(club, user, 20000, "req-1"))
		if !errors.Is(err, ledger.ErrIdempotencyConflict) {
			t.Fatalf("err = %v, want ErrIdempotencyConflict", err)
		}
	})

	t.Run("entry order does not change the fingerprint", func(t *testing.T) {
		op := credit(club, user, 10000, "req-1")
		op.Entries[0], op.Entries[1] = op.Entries[1], op.Entries[0]
		again, err := post(t, d, op)
		if err != nil || !again.Replayed {
			t.Fatalf("replay with reordered entries: %+v, %v", again, err)
		}
	})

	t.Run("same key in another club is independent", func(t *testing.T) {
		other := d.Club(t, "idem-other")
		u2 := d.Customer(t, other, "Дана")
		res, err := post(t, d, credit(other, u2, 500, "req-1"))
		if err != nil || res.Replayed {
			t.Fatalf("other club: %+v, %v", res, err)
		}
	})

	t.Run("concurrent same key → applied once", func(t *testing.T) {
		var wg sync.WaitGroup
		var replays, applied atomic.Int32
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := post(t, d, credit(club, user, 700, "req-concurrent"))
				if err != nil {
					t.Error(err)
					return
				}
				if res.Replayed {
					replays.Add(1)
				} else {
					applied.Add(1)
				}
			}()
		}
		wg.Wait()
		if applied.Load() != 1 || replays.Load() != 9 {
			t.Fatalf("applied %d, replays %d; want 1 and 9", applied.Load(), replays.Load())
		}
		if main, _ := d.Balances(t, club, user); main != 10700 {
			t.Fatalf("balance = %d, want 10700", main)
		}
	})
}

func TestPost_SumMustBeZero(t *testing.T) {
	d := testDB(t)
	club := d.Club(t, "sum")
	user := d.Customer(t, club, "Арман")

	op := credit(club, user, 1000, "unbalanced")
	op.Entries[1].Amount = -999
	if _, err := post(t, d, op); !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("err = %v, want ErrUnbalanced", err)
	}

	// Тот же инвариант в БД: прямая вставка несбалансированной операции падает на COMMIT
	// (constraint trigger ledger_entries_balanced, DEFERRABLE INITIALLY DEFERRED).
	ctx := context.Background()
	var created time.Time
	err := db.InTenantTx(ctx, d.App, club, func(q *store.Queries) error {
		claim, err := q.ClaimLedgerIdempotencyKey(ctx, store.ClaimLedgerIdempotencyKeyParams{
			ClubID: club, IdempotencyKey: "raw-unbalanced-2", Fingerprint: make([]byte, 32),
		})
		if err != nil {
			return err
		}
		created = claim.TransactionCreatedAt
		if err := q.InsertLedgerTransaction(ctx, store.InsertLedgerTransactionParams{
			ID: claim.TransactionID, ClubID: club, Kind: "owner_top_up", IdempotencyKey: "raw-unbalanced-2", CreatedAt: created,
		}); err != nil {
			return err
		}
		accs, err := q.UpsertLedgerAccounts(ctx, store.UpsertLedgerAccountsParams{ClubID: club, Kinds: []string{"member_main"}, UserIds: []int64{user}})
		if err != nil {
			return err
		}
		_, err = q.InsertLedgerEntries(ctx, store.InsertLedgerEntriesParams{
			ClubID: club, TransactionID: claim.TransactionID, CreatedAt: created,
			AccountIds: []int64{accs[0].ID}, Amounts: []int64{500},
		})
		return err
	})
	if !isSQLState(err, "23514") || !strings.Contains(err.Error(), "unbalanced") {
		t.Fatalf("raw unbalanced commit: err = %v, want check_violation", err)
	}
	if main, _ := d.Balances(t, club, user); main != 0 {
		t.Fatalf("balance = %d, want 0 (rolled back)", main)
	}
}

func TestPost_Validation(t *testing.T) {
	d := testDB(t)
	club := d.Club(t, "validation")
	user := d.Customer(t, club, "Арман")
	cases := map[string]ledger.Operation{
		"no entries":          {ClubID: club, Kind: ledger.KindOwnerTopUp, IdempotencyKey: "v1"},
		"unknown kind":        {ClubID: club, Kind: "gift", IdempotencyKey: "v2", Entries: credit(club, user, 1, "").Entries},
		"empty key":           {ClubID: club, Kind: ledger.KindOwnerTopUp, Entries: credit(club, user, 1, "").Entries},
		"zero amount":         {ClubID: club, Kind: ledger.KindOwnerTopUp, IdempotencyKey: "v3", Entries: []ledger.Entry{{Account: ledger.Main(user)}, {Account: ledger.ClubAccount(ledger.AccountCashDrawer)}}},
		"member without user": {ClubID: club, Kind: ledger.KindOwnerTopUp, IdempotencyKey: "v4", Entries: []ledger.Entry{{Account: ledger.Account{Kind: ledger.AccountMemberMain}, Amount: 1}, {Account: ledger.ClubAccount(ledger.AccountCashDrawer), Amount: -1}}},
		"duplicate account":   {ClubID: club, Kind: ledger.KindOwnerTopUp, IdempotencyKey: "v5", Entries: []ledger.Entry{{Account: ledger.Main(user), Amount: 1}, {Account: ledger.Main(user), Amount: -1}}},
	}
	for name, op := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := post(t, d, op); !errors.Is(err, ledger.ErrInvalidOperation) {
				t.Fatalf("err = %v, want ErrInvalidOperation", err)
			}
		})
	}
}

func TestPost_InsufficientFundsAndMissingBalance(t *testing.T) {
	d := testDB(t)
	club := d.Club(t, "funds")
	user := d.Customer(t, club, "Арман")
	if _, err := post(t, d, credit(club, user, 5000, "seed")); err != nil {
		t.Fatal(err)
	}
	_, err := post(t, d, debit(club, user, 5001, "too-much"))
	var ife *ledger.InsufficientFundsError
	if !errors.As(err, &ife) || ife.Balance != 5000 || ife.Debit != 5001 {
		t.Fatalf("err = %v, want InsufficientFundsError{5000, 5001}", err)
	}
	if _, err := post(t, d, debit(club, user, 5000, "exact")); err != nil {
		t.Fatalf("debit to zero: %v", err)
	}
	if main, _ := d.Balances(t, club, user); main != 0 {
		t.Fatalf("balance = %d, want 0", main)
	}

	// Пользователь без строки member_balances в этом клубе.
	other := d.Club(t, "funds-other")
	stranger := d.Customer(t, other, "Чужой")
	if _, err := post(t, d, credit(club, stranger, 100, "stranger")); !errors.Is(err, ledger.ErrBalanceNotFound) {
		t.Fatalf("err = %v, want ErrBalanceNotFound", err)
	}

	// Входящий остаток переноса может быть отрицательным (Q-004).
	neg := ledger.Operation{
		ClubID: club, Kind: ledger.KindMigrationOpeningBalance, IdempotencyKey: "opening",
		Entries: []ledger.Entry{
			{Account: ledger.Main(user), Amount: -1234},
			{Account: ledger.ClubAccount(ledger.AccountOpeningBalance), Amount: 1234},
		},
	}
	if _, err := post(t, d, neg); err != nil {
		t.Fatalf("negative opening balance: %v", err)
	}
	if main, _ := d.Balances(t, club, user); main != -1234 {
		t.Fatalf("balance = %d, want -1234", main)
	}
	// С отрицательного баланса списать нельзя, начислить — можно.
	if _, err := post(t, d, debit(club, user, 1, "from-negative")); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("err = %v, want ErrInsufficientFunds", err)
	}
	if _, err := post(t, d, credit(club, user, 2000, "to-negative")); err != nil {
		t.Fatal(err)
	}

	// Переполнение decimal(12,2) старой схемы.
	if _, err := post(t, d, credit(club, user, ledger.MaxMemberBalance, "overflow")); !errors.Is(err, ledger.ErrBalanceOverflow) {
		t.Fatalf("err = %v, want ErrBalanceOverflow", err)
	}
}

func TestPost_TenantMismatch(t *testing.T) {
	d := testDB(t)
	a := d.Club(t, "tenant-a")
	b := d.Club(t, "tenant-b")
	user := d.Customer(t, b, "Арман")
	ctx := context.Background()
	err := db.InTenantTx(ctx, d.App, a, func(q *store.Queries) error {
		_, err := ledger.Post(ctx, q, credit(b, user, 100, "x"))
		return err
	})
	if !errors.Is(err, ledger.ErrTenantMismatch) {
		t.Fatalf("other club: err = %v, want ErrTenantMismatch", err)
	}
	err = db.InPlatformTx(ctx, d.App, func(q *store.Queries) error {
		_, err := ledger.Post(ctx, q, credit(b, user, 100, "x"))
		return err
	})
	if !errors.Is(err, ledger.ErrTenantMismatch) {
		t.Fatalf("no club: err = %v, want ErrTenantMismatch", err)
	}
}

func TestPost_MinutesOnly(t *testing.T) {
	d := testDB(t)
	club := d.Club(t, "minutes")
	user := d.Customer(t, club, "Арман")
	op := ledger.Operation{
		ClubID: club, Kind: ledger.KindPackageKept, IdempotencyKey: "session:s1:kept",
		Minutes: []ledger.MinuteEntry{{UserID: user, TimePackageID: 7, Minutes: 90}},
		Refs:    ledger.Refs{SessionID: "s1"},
	}
	res, err := post(t, d, op)
	if err != nil {
		t.Fatal(err)
	}
	again, err := post(t, d, op)
	if err != nil || !again.Replayed || len(again.Minutes) != 1 || again.Minutes[0].Minutes != 90 || again.Refs.SessionID != "s1" {
		t.Fatalf("replay = %+v, %v (first %d)", again, err, res.TransactionID)
	}
}

// Конкурентные списания: 50 горутин списывают по 3.00 с баланса 100.00 — успешных ровно 33,
// баланс 1.00, в минус не уходит, проекция сходится с журналом.
func TestPost_ConcurrentDebits(t *testing.T) {
	d := testDB(t)
	club := d.Club(t, "concurrent")
	user := d.Customer(t, club, "Арман")
	if _, err := post(t, d, credit(club, user, 10000, "seed")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var ok, insufficient atomic.Int32
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := post(t, d, debit(club, user, 300, fmt.Sprintf("debit-%d", i)))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ledger.ErrInsufficientFunds):
				insufficient.Add(1)
			default:
				t.Errorf("debit %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 33 || insufficient.Load() != 17 {
		t.Fatalf("ok %d, insufficient %d; want 33 and 17", ok.Load(), insufficient.Load())
	}
	if main, _ := d.Balances(t, club, user); main != 100 {
		t.Fatalf("balance = %d, want 100", main)
	}
	var drift []store.ListMemberBalanceDriftRow
	if err := db.InTenantTx(context.Background(), d.App, club, func(q *store.Queries) error {
		var err error
		drift, err = q.ListMemberBalanceDrift(context.Background(), club)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(drift) != 0 {
		t.Fatalf("projection drift: %+v", drift)
	}
}

func TestAppendOnly(t *testing.T) {
	d := testDB(t)
	club := d.Club(t, "append-only")
	user := d.Customer(t, club, "Арман")
	if _, err := post(t, d, credit(club, user, 100, "seed")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	appStmts := []string{
		"UPDATE ledger_entries SET amount = amount * 2",
		"DELETE FROM ledger_entries",
		"UPDATE ledger_transactions SET kind = 'top_up'",
		"DELETE FROM ledger_idempotency",
		"TRUNCATE ledger_entries",
		"UPDATE member_balances SET balance = 1000000",
		"INSERT INTO audit_log (club_id, actor_kind, category, action) VALUES (" + fmt.Sprint(club) + ", 'system', 'money', 'money.test')",
		"DELETE FROM audit_log",
	}
	for i, stmt := range appStmts {
		err := execInTenant(ctx, d, club, stmt)
		isInsert := strings.HasPrefix(stmt, "INSERT")
		if isInsert {
			if err != nil {
				t.Fatalf("app insert into audit_log: %v", err)
			}
			continue
		}
		if !isSQLState(err, "42501") {
			t.Fatalf("app stmt %d %q: err = %v, want insufficient_privilege", i, stmt, err)
		}
	}
	// Владелец (суперпользователь) — останавливают триггеры.
	for _, stmt := range []string{
		"UPDATE ledger_entries SET amount = amount * 2",
		"DELETE FROM ledger_transactions",
		"TRUNCATE ledger_entries",
		"UPDATE member_balances SET balance = 1000000",
		"INSERT INTO member_balances (club_id, user_id, balance) VALUES (" + fmt.Sprint(club) + ", 999999, 5)",
		"DELETE FROM audit_log",
	} {
		_, err := d.Owner.Exec(ctx, stmt)
		if !isSQLState(err, "42501") {
			t.Fatalf("owner %q: err = %v, want trigger exception", stmt, err)
		}
	}
	if main, _ := d.Balances(t, club, user); main != 100 {
		t.Fatalf("balance = %d, want 100", main)
	}
}

func execInTenant(ctx context.Context, d *dbtest.DB, club int64, stmt string) error {
	tx, err := d.App.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", fmt.Sprint(club)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, stmt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestRLS(t *testing.T) {
	d := testDB(t)
	a := d.Club(t, "rls-a")
	b := d.Club(t, "rls-b")
	ua := d.Customer(t, a, "А")
	ub := d.Customer(t, b, "Б")
	if _, err := post(t, d, credit(a, ua, 100, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := post(t, d, credit(b, ub, 200, "b")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tables := []string{"member_balances", "club_memberships", "ledger_accounts", "ledger_transactions", "ledger_entries", "ledger_idempotency", "audit_log"}
	t.Run("without app.club_id nothing is visible", func(t *testing.T) {
		for _, table := range tables {
			var n int64
			if err := d.App.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				t.Fatalf("%s: %v", table, err)
			}
			if n != 0 {
				t.Fatalf("%s: %d rows visible without a club", table, n)
			}
		}
	})
	t.Run("a club sees only its rows", func(t *testing.T) {
		err := db.InTenantTx(ctx, d.App, a, func(q *store.Queries) error {
			if _, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: b, UserID: ub}); err == nil {
				return errors.New("club A read a balance of club B by explicit club_id")
			}
			rows, err := q.LockMemberBalances(ctx, store.LockMemberBalancesParams{ClubID: b, UserIds: []int64{ub}})
			if err != nil || len(rows) != 0 {
				return fmt.Errorf("lock of other club's balance: %v rows, %w", len(rows), err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := d.App.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", fmt.Sprint(a)); err != nil {
			t.Fatal(err)
		}
		for _, table := range tables {
			var foreign int64
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE club_id <> $1", a).Scan(&foreign); err != nil {
				t.Fatalf("%s: %v", table, err)
			}
			if foreign != 0 {
				t.Fatalf("%s: %d rows of other clubs visible", table, foreign)
			}
		}
	})
	t.Run("insert with a foreign club_id fails WITH CHECK", func(t *testing.T) {
		err := execInTenant(ctx, d, a, fmt.Sprintf("INSERT INTO todo_tasks (club_id, title, created_at, updated_at) VALUES (%d, 'x', now(), now())", b))
		if !isSQLState(err, "42501") {
			t.Fatalf("err = %v, want RLS violation", err)
		}
	})
	t.Run("platform role sees all clubs", func(t *testing.T) {
		var n int64
		if err := d.Platform.QueryRow(ctx, "SELECT count(DISTINCT club_id) FROM member_balances WHERE club_id IN ($1, $2)", a, b).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("platform sees %d clubs, want 2", n)
		}
	})
	t.Run("own memberships across clubs with app.user_id", func(t *testing.T) {
		d.Join(t, b, ua)
		var n int64
		tx, err := d.App.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', $1, true)", fmt.Sprint(ua)); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM club_memberships").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("own memberships = %d, want 2", n)
		}
	})
	t.Run("every tenant table has forced RLS", func(t *testing.T) {
		rows, err := d.Owner.Query(ctx, "SELECT * FROM zafiro_tables_without_rls()")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var missing []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			missing = append(missing, name)
		}
		if len(missing) > 0 {
			t.Fatalf("tables with club_id but without forced RLS: %v", missing)
		}
	})
	t.Run("partitions are not readable directly", func(t *testing.T) {
		part := "ledger_entries_" + time.Now().UTC().Format("2006_01")
		_, err := d.App.Exec(ctx, "SELECT count(*) FROM "+part)
		if !isSQLState(err, "42501") {
			t.Fatalf("direct partition read: err = %v, want permission denied", err)
		}
	})
}

func TestPartitions(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	countParts := func(parent string) int {
		var n int
		if err := d.Owner.QueryRow(ctx, "SELECT count(*) FROM pg_inherits WHERE inhparent = $1::regclass", parent).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, parent := range db.PartitionedTables {
		if n := countParts(parent); n != 4 {
			t.Fatalf("%s: %d partitions after migration, want 4 (current month + 3)", parent, n)
		}
	}
	if _, err := db.EnsurePartitions(ctx, d.App, time.Now(), 6); !isSQLState(err, "42501") {
		t.Fatalf("app role: err = %v, want permission denied", err)
	}
	created, err := db.EnsurePartitions(ctx, d.Platform, time.Now(), 6)
	if err != nil {
		t.Fatal(err)
	}
	if created != 2*len(db.PartitionedTables) {
		t.Fatalf("created %d partitions, want %d", created, 2*len(db.PartitionedTables))
	}
	again, err := db.EnsurePartitions(ctx, d.Platform, time.Now(), 6)
	if err != nil || again != 0 {
		t.Fatalf("second run created %d, err %v; want 0", again, err)
	}
	// Граница месяца — по UTC.
	var lower string
	next := time.Now().UTC().AddDate(0, 5, 0)
	name := "ledger_entries_" + next.Format("2006_01")
	if err := d.Owner.QueryRow(ctx,
		"SELECT pg_get_expr(c.relpartbound, c.oid) FROM pg_class c WHERE c.relname = $1", name).Scan(&lower); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lower, next.Format("2006-01")+"-01 00:00:00+00") {
		t.Fatalf("partition bound %q is not a UTC month start", lower)
	}
}

func TestAudit_MoneyRecordIsWrittenByCaller(t *testing.T) {
	// Аудит пишет слой операций (billing); здесь — что запись доступна клубу и неизменяема.
	d := testDB(t)
	club := d.Club(t, "audit")
	ctx := context.Background()
	after, _ := json.Marshal(map[string]int{"amount": 1})
	err := db.InTenantTx(ctx, d.App, club, func(q *store.Queries) error {
		return q.InsertAuditLog(ctx, store.InsertAuditLogParams{ClubID: &club, ActorKind: "system", Category: "money", Action: "money.test", After: after})
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.ListClubAuditLogRow
	err = db.InTenantTx(ctx, d.App, club, func(q *store.Queries) error {
		var err error
		rows, err = q.ListClubAuditLog(ctx, store.ListClubAuditLogParams{
			ClubID: club, FromCreatedAt: time.Now().Add(-time.Hour), BeforeCreatedAt: time.Now().Add(time.Hour),
			BeforeID: 1 << 62, PageSize: 10,
		})
		return err
	})
	if err != nil || len(rows) != 1 || rows[0].Action != "money.test" {
		t.Fatalf("audit rows = %+v, %v", rows, err)
	}
	// Платформенное событие (club_id NULL) клуб записать не может.
	err = db.InTenantTx(ctx, d.App, club, func(q *store.Queries) error {
		return q.InsertAuditLog(ctx, store.InsertAuditLogParams{ActorKind: "system", Category: "money", Action: "money.test"})
	})
	if !isSQLState(err, "42501") {
		t.Fatalf("platform audit from club: err = %v, want RLS violation", err)
	}
}

func isSQLState(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

// Решение CSQ-1 (docs/DECISIONS.md): виды операций Go и CHECK ledger_transactions_kind_check совпадают,
// включая cash_shift_opening (0014).
func TestKindsMatchSchemaCheck(t *testing.T) {
	d := testDB(t)
	var def string
	if err := d.Owner.QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		 WHERE conname = 'ledger_transactions_kind_check' AND conrelid = 'ledger_transactions'::regclass`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	inSchema := map[string]bool{}
	for i, part := range strings.Split(def, "'") {
		if i%2 == 1 { // литералы в кавычках
			inSchema[part] = true
		}
	}
	for _, k := range ledger.Kinds() {
		if !inSchema[string(k)] {
			t.Errorf("kind %q is not in the schema CHECK: %s", k, def)
		}
		delete(inSchema, string(k))
	}
	for k := range inSchema {
		t.Errorf("schema kind %q has no Go constant", k)
	}
	if !ledger.KindCashShiftOpening.Valid() {
		t.Fatal("cash_shift_opening is not valid")
	}
}
