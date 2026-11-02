//go:build integration

package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker"
)

// Срок хранения журналов (H1-Q1): api_request_logs старше 90 дней (PruneIntegrationData) удаляются целыми
// месячными партициями; ledger, audit_log и activity_logs не трогаются никогда.
func TestLogRetention(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	now := time.Now().UTC()
	keep := 90 * 24 * time.Hour
	cutoff := now.Add(-keep)

	countParts := func(parent string) int {
		t.Helper()
		var n int
		if err := d.Owner.QueryRow(ctx, "SELECT count(*) FROM pg_inherits WHERE inhparent = $1::regclass", parent).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := map[string]int{}
	for _, table := range db.PartitionedTables {
		before[table] = countParts(table)
	}
	// Ожидаемое число: партиции dbtest (PartitionMonthsBack назад), весь месяц которых раньше cutoff.
	want := 0
	for off := -dbtest.PartitionMonthsBack; off <= 0; off++ {
		end := time.Date(now.Year(), now.Month()+time.Month(off)+1, 1, 0, 0, 0, 0, time.UTC)
		if !end.After(cutoff) {
			want++
		}
	}
	if want == 0 {
		t.Fatal("test setup: no expired partitions")
	}

	insert := func(at time.Time) int64 {
		t.Helper()
		var id int64
		if err := d.Owner.QueryRow(ctx, `INSERT INTO api_request_logs (method, endpoint, status, duration_ms, ip, created_at)
			VALUES ('GET', 'api/v1/products', 200, 1, '127.0.0.1', $1) RETURNING id`, at).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldID := insert(now.AddDate(0, 0, -200))
	recentID := insert(now.AddDate(0, 0, -80))
	freshID := insert(now)
	// Записи «вечных» журналов в тех же старых месяцах.
	if _, err := d.Owner.Exec(ctx, `INSERT INTO audit_log (actor_kind, category, action, created_at) VALUES ('system', 'money', 'money.test', $1)`,
		now.AddDate(0, 0, -200)); err != nil {
		t.Fatal(err)
	}

	w := &worker.LogRetention{Platform: d.Platform, Retention: map[string]time.Duration{"api_request_logs": keep},
		Now: func() time.Time { return now }}
	ran, dropped, err := w.Tick(ctx)
	if err != nil || !ran {
		t.Fatalf("tick: ran=%v err=%v", ran, err)
	}
	if dropped != want {
		t.Fatalf("dropped %d partitions, want %d", dropped, want)
	}
	if got := countParts("api_request_logs"); got != before["api_request_logs"]-want {
		t.Fatalf("api_request_logs partitions = %d, want %d", got, before["api_request_logs"]-want)
	}
	for _, table := range db.PartitionedTables {
		if table != "api_request_logs" && countParts(table) != before[table] {
			t.Fatalf("%s: partitions %d → %d, must never be dropped", table, before[table], countParts(table))
		}
	}
	exists := func(id int64) bool {
		var ok bool
		if err := d.Owner.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM api_request_logs WHERE id = $1)", id).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if exists(oldID) || !exists(recentID) || !exists(freshID) {
		t.Fatalf("rows after retention: old=%v recent=%v fresh=%v; want false true true", exists(oldID), exists(recentID), exists(freshID))
	}
	var audits int
	if err := d.Owner.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action = 'money.test'").Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit_log rows = %d (%v), want 1", audits, err)
	}

	// Повтор — ничего не удаляет.
	if _, again, err := w.Tick(ctx); err != nil || again != 0 {
		t.Fatalf("second tick dropped %d, err %v", again, err)
	}

	// Журналы без срока — отказ и в Go, и в SQL (белый список функции), и у роли приложения нет права.
	for _, table := range []string{"ledger_entries", "ledger_transactions", "ledger_minute_entries", "audit_log", "activity_logs"} {
		if _, err := db.DropPartitionsBefore(ctx, d.Platform, table, now); err == nil {
			t.Fatalf("%s: DropPartitionsBefore must refuse", table)
		}
		if _, err := d.Platform.Exec(ctx, "SELECT zafiro_drop_monthly_partitions($1, now())", table); err == nil {
			t.Fatalf("%s: zafiro_drop_monthly_partitions must refuse", table)
		}
	}
	var pgErr *pgconn.PgError
	if _, err := db.DropPartitionsBefore(ctx, d.App, "api_request_logs", now); !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("app role: err = %v, want permission denied", err)
	}
}
