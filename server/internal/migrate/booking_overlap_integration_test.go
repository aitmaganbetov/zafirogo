//go:build integration

package migrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate/migratetest"
)

// Ожидающие брони одного ПК на пересекающееся время (в MySQL ограничения нет, в Postgres — 0040
// computer_reservations_pending_no_overlap): plan показывает блокер с номерами броней, apply ничего не пишет;
// после исправления в источнике блокера нет. Отменённая, с пришедшим клиентом, соседняя встык и бронь другого ПК —
// не пересечения (docs/open-questions/upstream-bookings.md §4.3).
func TestPlanBlocksOverlappingPendingReservations(t *testing.T) {
	ctx := context.Background()
	my, _ := migratetest.StartMySQL(t)
	owner := dbtest.New(t).Owner
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).UTC().Format(time.DateTime) }
	ins := func(id, pc int, from, to time.Duration, extra string) {
		t.Helper()
		canceled, arrived := any(nil), any(nil)
		switch extra {
		case "canceled":
			canceled = at(-time.Minute)
		case "arrived":
			arrived = at(-time.Minute)
		}
		//nolint:misspell // cancelled_at — колонка старой схемы
		execMy(t, my, `INSERT INTO computer_reservations (id, club_id, club_computer_id, user_id, starts_at, ends_at, cancelled_at, arrived_at,
				created_by, created_at, updated_at) VALUES (?, 1, ?, 6, ?, ?, ?, ?, 2, ?, ?)`,
			id, pc, at(from), at(to), canceled, arrived, at(-time.Hour), at(-time.Hour))
	}
	ins(901, 1, 24*time.Hour, 26*time.Hour, "")
	ins(902, 1, 25*time.Hour, 27*time.Hour, "")         // пересекается с 901
	ins(903, 1, 26*time.Hour, 27*time.Hour, "canceled") // отменена
	ins(904, 1, 24*time.Hour, 25*time.Hour, "arrived")  // клиент пришёл
	ins(905, 1, 27*time.Hour, 28*time.Hour, "")         // встык с 902
	ins(906, 2, 24*time.Hour, 26*time.Hour, "")         // другой ПК
	ins(907, 1, 25*time.Hour, 25*time.Hour, "")         // пустой интервал — как в ограничении, не пересечение

	opts := migrate.Options{Club: "zafiro-arena", IDReserve: idReserve, Now: now}
	plan, err := migrate.Plan(ctx, my, owner, opts)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	requireFinding(t, plan, migrate.Blocker, migrate.CodeBookingOverlap)
	text := reportText(plan)
	if !strings.Contains(text, "ожидающие брони №901 и №902 одного ПК (id 1) пересекаются по времени") {
		t.Fatalf("нет понятного текста блокера:\n%s", text)
	}
	if n := strings.Count(text, "пересекаются по времени"); n != 1 {
		t.Fatalf("пар в отчёте %d, ожидалась одна (901 и 902):\n%s", n, text)
	}
	if rep, err := migrate.Apply(ctx, my, owner, opts); !errors.Is(err, migrate.ErrBlocked) {
		t.Fatalf("apply: %v, want ErrBlocked\n%s", err, reportText(rep))
	}
	if scalarPG[bool](t, owner, "SELECT EXISTS (SELECT 1 FROM clubs WHERE id = 1)") {
		t.Fatal("apply с блокером записал клуб")
	}

	// Исправлено в источнике (бронь отменена в старой панели) — блокера нет.
	execMy(t, my, "UPDATE computer_reservations SET cancelled_at = ? WHERE id = 902", at(0)) //nolint:misspell // колонка старой схемы
	plan, err = migrate.Plan(ctx, my, owner, opts)
	if err != nil {
		t.Fatalf("plan после исправления: %v", err)
	}
	if plan.Has(migrate.Blocker, migrate.CodeBookingOverlap) {
		t.Fatalf("блокер остался:\n%s", reportText(plan))
	}
}
