//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Аудит PERF-6 (docs/DECISIONS.md, аудит 22.11.2026): счётчик журнала суперадминки точен до 100 000 записей; больше —
// без фильтров оценка по статистике (не меньше 100 001), с фильтром — 100 001 (count(*) не читает всю таблицу).
func TestSuperadminLogCountsCapped(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	club := d.Club(t, "perf6")
	user := d.Customer(t, club, "Журнал")
	other := d.Club(t, "perf6-other")
	otherUser := d.Customer(t, other, "Другой")
	add := func(clubID, userID int64, event string, n int) {
		t.Helper()
		if _, err := d.Owner.Exec(ctx, `INSERT INTO activity_logs (event, description, user_id, ip_address, created_at, club_id)
			SELECT $1, 'x', $2, '127.0.0.1', now() - (g || ' milliseconds')::interval, $3 FROM generate_series(1, $4) g`,
			event, userID, clubID, n); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Owner.Exec(ctx, `INSERT INTO platform_logs (event, description, club_id, ip_address, created_at, updated_at)
			SELECT $1, 'x', $2, '127.0.0.1', now(), now() FROM generate_series(1, $3)`, event, clubID, n); err != nil {
			t.Fatal(err)
		}
	}
	q := store.New(d.Platform)
	counts := func(clubID *int64, event string) (activity, platform int64) {
		t.Helper()
		a, err := q.SuperadminCountActivityLogs(ctx, store.SuperadminCountActivityLogsParams{ClubID: clubID, Event: event})
		if err != nil {
			t.Fatal(err)
		}
		p, err := q.SuperadminCountPlatformLogs(ctx, store.SuperadminCountPlatformLogsParams{ClubID: clubID, Event: event})
		if err != nil {
			t.Fatal(err)
		}
		return a, p
	}

	add(club, user, "a.small", 150)
	add(other, otherUser, "b.small", 30)
	for name, tc := range map[string]struct {
		club  *int64
		event string
		want  int64
	}{"all": {nil, "", 180}, "club": {&club, "", 150}, "event": {nil, "b.small", 30}, "both": {&other, "a.small", 0}} {
		if a, p := counts(tc.club, tc.event); a != tc.want || p != tc.want {
			t.Errorf("%s: activity %d, platform %d, want %d", name, a, p, tc.want)
		}
	}

	add(club, user, "a.big", 100_050)
	if _, err := d.Owner.Exec(ctx, "ANALYZE activity_logs; ANALYZE platform_logs"); err != nil {
		t.Fatal(err)
	}
	const actual = 100_230
	if a, p := counts(&club, ""); a != 100_001 || p != 100_001 {
		t.Errorf("filtered over the cap: activity %d, platform %d, want 100001", a, p)
	}
	if a, p := counts(nil, "a.big"); a != 100_001 || p != 100_001 {
		t.Errorf("event over the cap: activity %d, platform %d, want 100001", a, p)
	}
	if a, p := counts(nil, ""); a < 100_001 || a > actual*11/10 || p < 100_001 || p > actual*11/10 {
		t.Errorf("unfiltered estimate: activity %d, platform %d, actual %d", a, p, actual)
	}
	if a, p := counts(&other, ""); a != 30 || p != 30 {
		t.Errorf("small club stays exact: activity %d, platform %d", a, p)
	}
}
