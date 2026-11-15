//go:build integration

package monitoring

import (
	"context"
	"testing"
	"time"
)

// PB-14: счётчики клубов сбрасываются одной командой — строки складываются с уже записанными (другой инстанс,
// прошлый сброс), несколько минут и клубов — за один сброс, несуществующий клуб пропускается, как раньше.
func TestFlushClubRequestsBatch(t *testing.T) {
	ctx := context.Background()
	if _, err := tdb.Owner.Exec(ctx, "DELETE FROM club_request_metrics"); err != nil {
		t.Fatal(err)
	}
	a, b := tdb.Club(t, "flush-a"), tdb.Club(t, "flush-b")
	const gone = int64(987654321) // клуба нет
	now := time.Unix(1_800_000_000, 0)
	clock := now
	w := &Workers{Platform: tdb.Platform, Recorder: NewRecorder(func() time.Time { return clock })}
	record := func() {
		w.Recorder.Record(ChannelPanel, a, 30*time.Millisecond, 200)
		w.Recorder.Record(ChannelAgent, a, 10*time.Millisecond, 500)
		w.Recorder.Record(ChannelAPI, b, 5*time.Millisecond, 200)
		w.Recorder.Record(ChannelGuest, gone, 5*time.Millisecond, 200)
	}
	record()
	clock = now.Add(time.Minute)
	record()
	clock = now.Add(2 * time.Minute)
	if err := w.Flush(ctx, clock); err != nil {
		t.Fatal(err)
	}
	// Второй сброс тех же минут (как второй инстанс) — счётчики складываются.
	clock = now
	record()
	clock = now.Add(2 * time.Minute)
	if err := w.Flush(ctx, clock); err != nil {
		t.Fatal(err)
	}
	type row struct{ agent, api, panel, guest, errs, ms int64 }
	get := func(club, minute int64) (r row, ok bool) {
		err := tdb.Owner.QueryRow(ctx, `SELECT agent_requests, api_requests, panel_requests, guest_requests, errors, duration_ms
			FROM club_request_metrics WHERE club_id = $1 AND minute = $2`, club, minute).Scan(&r.agent, &r.api, &r.panel, &r.guest, &r.errs, &r.ms)
		return r, err == nil
	}
	m0, m1 := now.Unix()/60, now.Unix()/60+1
	for _, c := range []struct {
		club, minute int64
		want         row
	}{
		{a, m0, row{agent: 2, panel: 2, errs: 2, ms: 80}},
		{a, m1, row{agent: 1, panel: 1, errs: 1, ms: 40}},
		{b, m0, row{api: 2, ms: 10}},
		{b, m1, row{api: 1, ms: 5}},
	} {
		if got, ok := get(c.club, c.minute); !ok || got != c.want {
			t.Errorf("club %d minute %d: %+v (found %v), want %+v", c.club, c.minute, got, ok, c.want)
		}
	}
	var n int
	if err := tdb.Owner.QueryRow(ctx, "SELECT count(*) FROM club_request_metrics").Scan(&n); err != nil || n != 4 {
		t.Fatalf("rows = %d (%v), want 4: the missing club must be skipped", n, err)
	}
}
