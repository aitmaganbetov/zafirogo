package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// captureDB — store.DBTX, запоминающий текст и аргументы последнего запроса сгенерированного кода sqlc. Так тест
// проверяет имена запросов и позиции аргументов, на которые опирается трассировщик, по настоящему коду store.
type captureDB struct {
	sql  string
	args []any
}

var errCaptured = errors.New("captured")

func (c *captureDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.sql, c.args = sql, args
	return pgconn.CommandTag{}, errCaptured
}

func (c *captureDB) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.sql, c.args = sql, args
	return nil, errCaptured
}

func (c *captureDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	c.sql, c.args = sql, args
	return errRow{}
}

type errRow struct{}

func (errRow) Scan(...any) error { return errCaptured }

func newTestObserver(t *testing.T) *Observer {
	t.Helper()
	o, err := New(context.Background(), Options{Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// run прогоняет запрос через трассировщик как pgx: Start → End с тегом и ошибкой.
func run(t *PgxTracer, conn *pgx.Conn, sql string, args []any, tag string, err error) {
	ctx := t.TraceQueryStart(context.Background(), conn, pgx.TraceQueryStartData{SQL: sql, Args: args})
	t.TraceQueryEnd(ctx, conn, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag(tag), Err: err})
}

func TestQueryName(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"-- name: InsertLedgerTransaction :exec\nINSERT INTO", "InsertLedgerTransaction"},
		{"  \n-- name: Foo :one\nSELECT 1", "Foo"},
		{"-- name: bad name; DROP\n", "bad"},
		{"-- name: 1bad :one", qRaw},
		{"begin", qBegin},
		{"begin isolation level serializable", qBegin},
		{"commit", qCommit},
		{"rollback", qRollback},
		{"SELECT pg_try_advisory_lock(hashtext($1))", qLock},
		{"SELECT pg_advisory_unlock(hashtext($1))", qUnlock},
		{"SELECT * FROM users WHERE phone = '+77001234567'", qRaw},
		{"SELECT set_config('app.club_id', $1, true)", qRaw},
		{"select zafiro_create_monthly_partitions($1, $2::date, $3) -- x", qRaw},
	} {
		if got := queryName(c.sql); got != c.want {
			t.Errorf("queryName(%q) = %q, want %q", c.sql, got, c.want)
		}
	}
}

// TestPgxTracerEvents — события предметной области по запросам сгенерированного кода store.
func TestPgxTracerEvents(t *testing.T) {
	o := newTestObserver(t)
	tr := o.NewPgxTracer("app")
	conn := new(pgx.Conn)
	ctx := context.Background()
	db := &captureDB{}
	q := store.New(db)

	// Ledger: операция в транзакции считается после COMMIT; откат — rolled_back; COMMIT прерванной транзакции
	// (тег ROLLBACK) — тоже откат.
	_ = q.InsertLedgerTransaction(ctx, store.InsertLedgerTransactionParams{Kind: "top_up"})
	ledgerSQL, ledgerArgs := db.sql, db.args
	if queryName(ledgerSQL) != qInsertLedgerTransaction {
		t.Fatalf("InsertLedgerTransaction name = %q", queryName(ledgerSQL))
	}
	run(tr, conn, "begin", nil, "BEGIN", nil)
	run(tr, conn, ledgerSQL, ledgerArgs, "INSERT 0 1", nil)
	if got := testutil.ToFloat64(o.m.ledgerOps.WithLabelValues("top_up", "committed")); got != 0 {
		t.Fatalf("ledger counted before commit: %v", got)
	}
	run(tr, conn, "commit", nil, "COMMIT", nil)
	if got := testutil.ToFloat64(o.m.ledgerOps.WithLabelValues("top_up", "committed")); got != 1 {
		t.Fatalf("ledger committed = %v, want 1", got)
	}
	run(tr, conn, "begin", nil, "BEGIN", nil)
	run(tr, conn, ledgerSQL, ledgerArgs, "INSERT 0 1", nil)
	run(tr, conn, "rollback", nil, "ROLLBACK", nil)
	run(tr, conn, "begin", nil, "BEGIN", nil)
	run(tr, conn, ledgerSQL, ledgerArgs, "INSERT 0 1", nil)
	run(tr, conn, "commit", nil, "ROLLBACK", nil)
	if got := testutil.ToFloat64(o.m.ledgerOps.WithLabelValues("top_up", "rolled_back")); got != 2 {
		t.Fatalf("ledger rolled_back = %v, want 2", got)
	}
	// Неизвестный вид — other (метка не растёт от данных).
	_ = q.InsertLedgerTransaction(ctx, store.InsertLedgerTransactionParams{Kind: "evil-123456"})
	run(tr, conn, db.sql, db.args, "INSERT 0 1", nil)
	if got := testutil.ToFloat64(o.m.ledgerOps.WithLabelValues(labelOther, "committed")); got != 1 {
		t.Fatalf("unknown ledger kind = %v, want other", got)
	}
	// Ошибка ledger: нехватка средств по имени ограничения.
	_, _ = q.InsertLedgerEntries(ctx, store.InsertLedgerEntriesParams{})
	run(tr, conn, db.sql, db.args, "", &pgconn.PgError{Code: "23514", ConstraintName: "ledger_member_balance_non_negative"})
	if got := testutil.ToFloat64(o.m.ledgerErrors.WithLabelValues("InsertLedgerEntries", "ledger_member_balance_non_negative")); got != 1 {
		t.Fatalf("ledger error = %v", got)
	}

	// Команды агентам.
	_ = q.InsertAgentCommand(ctx, store.InsertAgentCommandParams{Kind: "lock"})
	run(tr, conn, db.sql, db.args, "INSERT 0 1", nil)
	_, _ = q.ListPendingAgentCommands(ctx, store.ListPendingAgentCommandsParams{})
	run(tr, conn, db.sql, db.args, "SELECT 2", nil)
	_, _ = q.AcceptAgentCommand(ctx, store.AcceptAgentCommandParams{})
	run(tr, conn, db.sql, db.args, "UPDATE 1", nil)
	_, _ = q.CompleteAgentCommand(ctx, store.CompleteAgentCommandParams{Status: "succeeded"})
	run(tr, conn, db.sql, db.args, "UPDATE 1", nil)
	_, _ = q.ExpireAgentCommands(ctx, store.ExpireAgentCommandsParams{})
	run(tr, conn, db.sql, db.args, "UPDATE 3", nil)
	for _, c := range []struct {
		event, result string
		want          float64
	}{{"issued", "", 1}, {"redelivered", "", 2}, {"accepted", "", 1}, {"completed", "succeeded", 1}, {"expired", "", 3}} {
		if got := testutil.ToFloat64(o.m.agentCommands.WithLabelValues(c.event, c.result)); got != c.want {
			t.Errorf("agent commands %s/%s = %v, want %v", c.event, c.result, got, c.want)
		}
	}

	// Вебхуки и outbox.
	for _, st := range []string{"delivered", "pending", "failed"} {
		_ = q.SaveWebhookAttempt(ctx, store.SaveWebhookAttemptParams{Status: st})
		run(tr, conn, db.sql, db.args, "UPDATE 1", nil)
	}
	_ = q.SkipWebhookDelivery(ctx, store.SkipWebhookDeliveryParams{})
	run(tr, conn, db.sql, db.args, "UPDATE 1", nil)
	for _, r := range []string{"delivered", "retry", "failed", "skipped"} {
		if got := testutil.ToFloat64(o.m.webhookAttempts.WithLabelValues(r)); got != 1 {
			t.Errorf("webhook %s = %v, want 1", r, got)
		}
	}
	_ = q.MarkOutboxAttemptFailed(ctx, store.MarkOutboxAttemptFailedParams{})
	run(tr, conn, db.sql, db.args, "UPDATE 1", nil)
	if got := testutil.ToFloat64(o.m.outboxFailures); got != 1 {
		t.Errorf("outbox failures = %v", got)
	}

	// Запуск задачи под advisory lock (worker/leader): захват → снятие = один запуск.
	run(tr, conn, "SELECT pg_try_advisory_lock(hashtext($1))", []any{"job:subscriptions:expire"}, "SELECT 1", nil)
	time.Sleep(time.Millisecond)
	run(tr, conn, "SELECT pg_advisory_unlock(hashtext($1))", []any{"job:subscriptions:expire"}, "SELECT 1", nil)
	if got := testutil.ToFloat64(o.m.workerRuns.WithLabelValues("subscriptions:expire")); got != 1 {
		t.Errorf("worker runs = %v", got)
	}
	if got := testutil.ToFloat64(o.m.workerLastRun.WithLabelValues("subscriptions:expire")); got < float64(time.Now().Add(-time.Minute).Unix()) {
		t.Errorf("worker last run = %v", got)
	}
	// Снятие без захвата на этом соединении — не запуск.
	run(tr, conn, "SELECT pg_advisory_unlock(hashtext($1))", []any{"job:subscriptions:expire"}, "SELECT 1", nil)
	if got := testutil.ToFloat64(o.m.workerRuns.WithLabelValues("subscriptions:expire")); got != 1 {
		t.Errorf("worker runs after stray unlock = %v", got)
	}

	// Счётчик запросов — по имени sqlc, не по тексту.
	if got := testutil.ToFloat64(o.m.dbQueries.WithLabelValues("app", "InsertAgentCommand")); got != 1 {
		t.Errorf("db queries InsertAgentCommand = %v", got)
	}

	// Закрытие соединения посреди транзакции — откат.
	run(tr, conn, "begin", nil, "BEGIN", nil)
	_ = q.InsertLedgerTransaction(ctx, store.InsertLedgerTransactionParams{Kind: "pos_sale"})
	run(tr, conn, db.sql, db.args, "INSERT 0 1", nil)
	tr.Forget(conn)
	if got := testutil.ToFloat64(o.m.ledgerOps.WithLabelValues("pos_sale", "rolled_back")); got != 1 {
		t.Errorf("ledger after Forget = %v", got)
	}
}
