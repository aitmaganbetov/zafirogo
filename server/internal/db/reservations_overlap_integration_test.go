//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
)

// Пересечение броней с интервалом (SeatMapBlockedComputers, AppBookingBookedComputerIDs) не читает историю броней
// клуба: 50 тыс. прошедших броней за два года и два десятка будущих — запрос идёт по индексу ожидающих броней
// (club_id, ends_at) и отбрасывает единицы строк (миграция 0044; до неё — все 50 тыс. строк клуба).
func TestReservationOverlapSkipsHistory(t *testing.T) {
	const closedAt = "cancelled_at" //nolint:misspell // колонка старой схемы
	d := dbtest.New(t)
	ctx := context.Background()
	club, other := d.Club(t, "overlap"), d.Club(t, "overlap-other")
	for _, c := range []int64{club, other} {
		if _, err := d.Owner.Exec(ctx, `WITH z AS (INSERT INTO club_zones (club_id, name, created_at) VALUES ($1, 'z', now()) RETURNING id)
			INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at)
			SELECT $1, z.id, n, now(), now() FROM z, generate_series(1, 50) n`, c); err != nil {
			t.Fatal(err)
		}
		// История: брони по 2 часа за 730 дней, почти все закрыты (клиент пришёл или бронь отменена), часть так и
		// осталась «ожидающей» (клиент не пришёл). Будущее: 20 броней на ближайшие сутки.
		if _, err := d.Owner.Exec(ctx, "INSERT INTO computer_reservations (club_id, club_computer_id, starts_at, ends_at, arrived_at, "+closedAt+`, created_at, updated_at)
			SELECT $1, (SELECT id FROM club_computers WHERE club_id = $1 AND number = 1 + n % 50),
			       s, s + interval '2 hours', CASE WHEN n % 10 < 7 THEN s END, CASE WHEN n % 10 = 7 THEN s END, s, s
			FROM generate_series(1, 50000) n, LATERAL (SELECT now() - interval '1 day' - n * interval '21 minutes' AS s) x`, c); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Owner.Exec(ctx, `INSERT INTO computer_reservations (club_id, club_computer_id, starts_at, ends_at, created_at, updated_at)
			SELECT $1, (SELECT id FROM club_computers WHERE club_id = $1 AND number = n),
			       now() + n * interval '1 hour', now() + n * interval '1 hour' + interval '2 hours', now(), now()
			FROM generate_series(1, 20) n`, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Owner.Exec(ctx, "ANALYZE computer_reservations"); err != nil {
		t.Fatal(err)
	}
	// Текст запроса — db/queries/audit_security.sql SeatMapBlockedComputers.
	const query = `SELECT DISTINCT club_computer_id
		FROM computer_reservations
		WHERE club_id = $1
		  AND ` + closedAt + ` IS NULL AND arrived_at IS NULL
		  AND starts_at < now() + interval '150 minutes' AND ends_at > now()
		  AND (user_id IS NULL OR user_id <> 0)`
	rows, err := d.Owner.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+query, club)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + plan.String())
	if !strings.Contains(plan.String(), "computer_reservations_club_id_pending_ends_at_index") {
		t.Errorf("запрос пересечения броней не идёт по индексу (club_id, ends_at):\n%s", plan.String())
	}
	var blocked int64
	if err := d.Owner.QueryRow(ctx, "SELECT count(*) FROM ("+query+") q", club).Scan(&blocked); err != nil || blocked != 2 {
		t.Errorf("ПК с бронью в ближайшие 2,5 часа: %d, %v; want 2", blocked, err)
	}
}
