//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// capture — Beginner, запоминающий открытую транзакцию (чтобы спросить её настройки изнутри fn).
type capture struct {
	pool *pgxpool.Pool
	tx   pgx.Tx
}

func (c *capture) BeginTx(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	tx, err := c.pool.BeginTx(ctx, o)
	c.tx = tx
	return tx, err
}

func show(t *testing.T, tx pgx.Tx, name string) string {
	t.Helper()
	var v string
	if err := tx.QueryRow(context.Background(), "SELECT current_setting($1)", name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// PB-17: транзакция клуба ограничена на стороне Postgres (команда — 2 мин, простой в транзакции — 1 мин), только до
// конца транзакции; транзакция без клуба (воркеры, суперадминка, перенос) и NoTimeouts — без ограничений.
func TestTenantTxTimeouts(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	club := d.Club(t, "timeouts")

	app := &capture{pool: d.App}
	if err := db.InTenantTx(ctx, app, club, func(*store.Queries) error {
		if got := show(t, app.tx, "statement_timeout"); got != "2min" {
			t.Errorf("statement_timeout = %q, want 2min", got)
		}
		if got := show(t, app.tx, "idle_in_transaction_session_timeout"); got != "1min" {
			t.Errorf("idle_in_transaction_session_timeout = %q, want 1min", got)
		}
		if got := show(t, app.tx, "lock_timeout"); got != "5s" {
			t.Errorf("lock_timeout = %q, want 5s", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenantTxWith(ctx, app, club, db.Options{NoTimeouts: true}, func(*store.Queries) error {
		if got := show(t, app.tx, "statement_timeout"); got != "0" {
			t.Errorf("NoTimeouts: statement_timeout = %q, want 0", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// После транзакции соединение возвращается в пул без ограничений (set_config(…, true) = SET LOCAL).
	for range 4 {
		var v string
		if err := d.App.QueryRow(ctx, "SELECT current_setting('statement_timeout') || '/' || current_setting('idle_in_transaction_session_timeout')").Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v != "0/0" {
			t.Fatalf("timeouts leaked to the pooled connection: %s", v)
		}
	}
	platform := &capture{pool: d.Platform}
	if err := db.InPlatformTx(ctx, platform, func(*store.Queries) error {
		if got := show(t, platform.tx, "statement_timeout"); got != "0" {
			t.Errorf("platform statement_timeout = %q, want 0", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Долгая команда в транзакции клуба обрывается сервером (проверяем на коротком пределе той же настройки).
	err := db.InTenantTx(ctx, app, club, func(*store.Queries) error {
		if _, err := app.tx.Exec(ctx, "SELECT set_config('statement_timeout', '50ms', true)"); err != nil {
			return err
		}
		_, err := app.tx.Exec(ctx, "SELECT pg_sleep(2)")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "statement timeout") {
		t.Fatalf("pg_sleep under statement_timeout: %v", err)
	}
}

// PB-02: запрос с необязательным фильтром выполняется с планом под значения — и после многих выполнений на одном
// соединении (общий план Postgres выбирает после пятого выполнения именованного оператора).
func TestCustomPlanQueriesRun(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	club := d.Club(t, "custom-plan")
	a := d.Customer(t, club, "Ахметов Тест")
	d.Customer(t, club, "Иванов Тест")
	cfg := d.App.Config().Copy()
	cfg.MaxConns = 1 // одно соединение — один кэш операторов
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for i := range 12 {
		search := ""
		want := int64(2)
		if i%2 == 1 {
			search, want = "%Ахмет%", 1
		}
		err := db.InTenantTx(ctx, pool, club, func(q *store.Queries) error {
			n, err := q.AdminCountCustomers(ctx, store.AdminCountCustomersParams{ClubID: club, Search: search})
			if err != nil {
				return err
			}
			if n != want {
				t.Errorf("run %d (search %q): count %d, want %d", i, search, n, want)
			}
			rows, err := q.CashSearchCustomers(ctx, store.CashSearchCustomersParams{ClubID: club, Search: search, PageSize: 25})
			if err != nil {
				return err
			}
			if int64(len(rows)) != want || (search != "" && rows[0].ID != a) {
				t.Errorf("run %d (search %q): %d rows", i, search, len(rows))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Именованных подготовленных операторов для этих запросов на соединении нет (выполняются безымянным).
	var named int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_prepared_statements WHERE statement LIKE '-- name: AdminCountCustomers %' OR statement LIKE '-- name: CashSearchCustomers %'").Scan(&named); err != nil {
		t.Fatal(err)
	}
	if named != 0 {
		t.Fatalf("%d named prepared statements for custom-plan queries", named)
	}
}
