//go:build integration

package activity_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// section — секция goose Up или Down миграции 0011 (разметка goose — комментарии, Postgres их пропускает).
func section(t *testing.T, up bool) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dbtest.MigrationsDir(), "0011_events.sql"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "-- +goose Down")
	if up {
		return s[strings.Index(s, "-- +goose Up"):i]
	}
	return s[i:]
}

func exec(t *testing.T, d *dbtest.DB, sql string) {
	t.Helper()
	if _, err := d.Owner.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

func scalar[T any](t *testing.T, d *dbtest.DB, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := d.Owner.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// Миграция 0011 переводит activity_logs в партиции с сохранением строк; Down — обратно, тоже без потерь.
func TestPartitionMigrationKeepsData(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	club := d.Club(t, "act-mig")
	user := d.Customer(t, club, "Клиент")

	exec(t, d, section(t, false)) // назад к обычной таблице 0001
	if kind := scalar[string](t, d, "SELECT relkind::text FROM pg_class WHERE relname = 'activity_logs'"); kind != "r" {
		t.Fatalf("relkind after down = %s", kind)
	}
	if _, err := d.Owner.Exec(ctx, `INSERT INTO activity_logs (id, event, description, user_id, details, ip_address, user_agent, created_at, updated_at, club_id) VALUES
		(10, 'auth.login', 'Вход', $2, '{"x": 1}', '1.1.1.1', 'UA', '2019-03-04 10:00+00', NULL, $1),
		(11, 'cash.income', 'Приход', $2, NULL, '::1', NULL, NULL, '2025-01-31 23:59:59+00', $1),
		(12, 'auth.logout', 'Выход', $2, NULL, '1.1.1.1', NULL, now(), now(), $1)`, club, user); err != nil {
		t.Fatal(err)
	}

	exec(t, d, section(t, true))
	if kind := scalar[string](t, d, "SELECT relkind::text FROM pg_class WHERE relname = 'activity_logs'"); kind != "p" {
		t.Fatalf("relkind after up = %s", kind)
	}
	rows, err := d.Owner.Query(ctx, "SELECT id, tableoid::regclass::text, created_at FROM activity_logs ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id int64
		var part string
		var at time.Time
		if err := rows.Scan(&id, &part, &at); err != nil {
			t.Fatal(err)
		}
		got = append(got, part)
	}
	rows.Close()
	cur := "activity_logs_" + time.Now().UTC().Format("2006_01")
	if strings.Join(got, ",") != "activity_logs_2019_03,activity_logs_2025_01,"+cur {
		t.Fatalf("partitions of rows = %v", got)
	}
	// Партиции до текущего месяца + 3 вперёд; новый id — после перенесённых.
	next := "activity_logs_" + time.Now().UTC().AddDate(0, 3, 0).Format("2006_01")
	if scalar[int](t, d, "SELECT count(*) FROM pg_class WHERE relname = $1", next) != 1 {
		t.Fatalf("partition %s missing", next)
	}
	if empty := scalar[string](t, d, "SELECT coalesce(string_agg(t, ','), '') FROM zafiro_tables_without_rls() t"); empty != "" {
		t.Fatalf("tables without RLS: %s", empty)
	}
	var id int64
	if err := db.InTenantTx(ctx, d.App, club, func(q *store.Queries) error {
		var err error
		id, err = activity.Record(ctx, q, activity.Entry{ClubID: club, Event: "auth.login", Description: "Вход", UserID: user,
			Meta: &activity.RequestMeta{IP: "1.1.1.1"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if id != 13 {
		t.Fatalf("new id = %d, want 13", id)
	}
	// Партиции напрямую ролям приложения недоступны (только через родителя с RLS).
	if _, err := d.App.Exec(ctx, "SELECT 1 FROM "+cur); err == nil {
		t.Fatal("zafiro_app must not read a partition directly")
	}

	exec(t, d, section(t, false))
	if n := scalar[int](t, d, "SELECT count(*) FROM activity_logs"); n != 4 {
		t.Fatalf("rows after down = %d", n)
	}
	if s := scalar[string](t, d, "SELECT details::text FROM activity_logs WHERE id = 10"); s != `{"x": 1}` {
		t.Fatalf("details after down = %s", s)
	}
	exec(t, d, section(t, true))
	if n := scalar[int](t, d, "SELECT count(*) FROM activity_logs"); n != 4 {
		t.Fatalf("rows after second up = %d", n)
	}
}

func TestRecordInTransactionAndRLS(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	a, b := d.Club(t, "act-a"), d.Club(t, "act-b")
	ua, ub := d.Customer(t, a, "A"), d.Customer(t, b, "B")
	reqCtx := activity.WithRequestMeta(ctx, activity.RequestMeta{IP: "203.0.113.5", UserAgent: "Mozilla/5.0"})

	if err := db.InTenantTx(reqCtx, d.App, a, func(q *store.Queries) error {
		_, err := activity.Record(reqCtx, q, activity.Entry{ClubID: a, Event: "settings.updated", Description: "Изменены настройки",
			UserID: ua, SubjectType: `App\Models\Club`, SubjectID: a,
			Details: map[string]any{"changed": []any{"name"}, "agent_exit_pin": "1234", "token": "t"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Откат действия откатывает запись журнала.
	boom := errors.New("boom")
	if err := db.InTenantTx(reqCtx, d.App, a, func(q *store.Queries) error {
		if _, err := activity.Record(reqCtx, q, activity.Entry{ClubID: a, Event: "cash.income", Description: "x", UserID: ua}); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	// Запись в чужой клуб — отказ RLS.
	if err := db.InTenantTx(reqCtx, d.App, a, func(q *store.Queries) error {
		_, err := activity.Record(reqCtx, q, activity.Entry{ClubID: b, Event: "cash.income", Description: "x", UserID: ub})
		return err
	}); err == nil {
		t.Fatal("RLS must reject a row of another club")
	}

	list := func(club int64) []store.ListClubActivityLogsRow {
		var rows []store.ListClubActivityLogsRow
		if err := db.InTenantTx(ctx, d.App, club, func(q *store.Queries) error {
			var err error
			rows, err = q.ListClubActivityLogs(ctx, store.ListClubActivityLogsParams{
				ClubID: club, FromCreatedAt: time.Now().Add(-time.Hour), BeforeCreatedAt: time.Now().Add(time.Hour),
				BeforeID: 1 << 62, PageSize: 50,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := list(a)
	if len(rows) != 1 {
		t.Fatalf("club A rows = %d, want 1", len(rows))
	}
	r := rows[0]
	var details map[string]any
	_ = json.Unmarshal(r.Details, &details)
	if r.Event != "settings.updated" || r.IpAddress != "203.0.113.5" || *r.UserAgent != "Mozilla/5.0" ||
		*r.SubjectType != `App\Models\Club` || len(details) != 1 || details["changed"] == nil {
		t.Fatalf("row = %+v details %s", r, r.Details)
	}
	if len(list(b)) != 0 {
		t.Fatal("club B sees rows of club A")
	}

	// recordFailedLogin: одна запись в каждом клубе аккаунта (пул zafiro_platform).
	if err := db.InPlatformTx(reqCtx, d.Platform, func(q *store.Queries) error {
		d.Join(t, b, ua)
		return activity.RecordInClubs(reqCtx, q, []int64{a, b}, activity.Entry{Event: "auth.login_failed",
			Description: "Неудачный вход: A", UserID: ua, Details: map[string]any{"reason": "password"}})
	}); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, d, "SELECT count(*) FROM activity_logs WHERE event = 'auth.login_failed'"); n != 2 {
		t.Fatalf("failed login rows = %d", n)
	}
}
