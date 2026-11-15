//go:build integration

package worker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker"
)

type sessionRecorder struct {
	mu      sync.Mutex
	started []*agentv1.ActiveSession
}

func (n *sessionRecorder) SessionStarted(_ context.Context, _, _ int64, s *agentv1.ActiveSession, _ int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.started = append(n.started, proto.Clone(s).(*agentv1.ActiveSession))
	return nil
}
func (n *sessionRecorder) SessionEnded(context.Context, int64, int64, *agentv1.EndSession, int64) error {
	return nil
}
func (n *sessionRecorder) BalanceChanged(context.Context, int64, int64, *agentv1.Balance) error {
	return nil
}

// UQ-11: оплаченное время брони начинается для гостя, который уже сидит за забронированным ПК, — по присутствию и
// времени начала (GS-544 без heartbeat): рано — нет; гость на другом ПК — нет; в интервале до начала — да, один раз.
func TestStartAppBookings(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	club := d.Club(t, "app-bookings-worker")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := d.Owner.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	exec(`UPDATE clubs SET timezone = 'UTC', settings = '{"club": {"booking_interval": 10, "app_booking_enabled": true}}' WHERE id = $1`, club)
	zone := scalar("INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, 'VIP', 60000, now(), now()) RETURNING id", club)
	pc := func(n int) int64 {
		return scalar(`INSERT INTO club_computers (club_id, club_zone_id, number, status, created_at, updated_at)
			VALUES ($1, $2, $3, 'online', now(), now()) RETURNING id`, club, zone, n)
	}
	pc1, pc2 := pc(1), pc(2)
	guest := d.Customer(t, club, "Гость")
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	var booking reservations.BookResult
	if err := db.InTenantTx(activity.WithRequestMeta(ctx, activity.RequestMeta{IP: "127.0.0.1"}), d.App, club, func(q *store.Queries) error {
		if _, err := billing.SetInitialBalance(ctx, q, billing.InitialBalanceParams{ClubID: club, UserID: guest, Main: 1000_00}); err != nil {
			return err
		}
		minutes := int64(60)
		var err error
		booking, err = reservations.Book(activity.WithRequestMeta(ctx, activity.RequestMeta{IP: "127.0.0.1"}), q, reservations.BookRequest{
			ClubID: club, UserID: guest, ComputerIDs: []int64{pc1}, StartsAt: now.Add(2 * time.Hour), Minutes: &minutes,
		}, reservations.Env{Now: now, Actor: billing.BalanceActor{UserID: guest, Kind: "guest"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	signedIn := map[int64][]int64{} // гость → ПК, где он вошёл
	var lookups int
	rec := &sessionRecorder{}
	w := &worker.StartAppBookings{Platform: d.Platform, App: d.App, Notifier: rec, Now: func() time.Time { return now },
		SignedIn: func(_ context.Context, clubID, userID int64) ([]int64, error) {
			lookups++
			if clubID != club {
				t.Errorf("присутствие запрошено для клуба %d", clubID)
			}
			return signedIn[userID], nil
		}}
	tick := func() int {
		t.Helper()
		ran, n, err := w.Tick(ctx)
		if err != nil || !ran {
			t.Fatalf("tick: ran=%v err=%v", ran, err)
		}
		return n
	}
	arrived := func() bool {
		return scalar("SELECT count(*) FROM computer_reservations WHERE id = $1 AND arrived_at IS NOT NULL", booking.ReservationIDs[0]) == 1
	}

	// 10:00, бронь на 12:00: рано — присутствие даже не спрашивается.
	signedIn[guest] = []int64{pc1}
	if n := tick(); n != 0 || lookups != 0 || arrived() {
		t.Fatalf("рано: запущено %d, запросов присутствия %d", n, lookups)
	}
	// 11:55 (интервал 10 мин), гость сидит за другим ПК — не запускается.
	now = time.Date(2026, 11, 15, 11, 55, 0, 0, time.UTC)
	signedIn[guest] = []int64{pc2}
	if n := tick(); n != 0 || lookups != 1 || arrived() {
		t.Fatalf("гость на другом ПК: запущено %d, запросов присутствия %d", n, lookups)
	}
	// Гость не вошёл нигде.
	signedIn[guest] = nil
	if n := tick(); n != 0 || arrived() {
		t.Fatalf("гость не вошёл: запущено %d", n)
	}
	// Гость за забронированным ПК: время начинается, агент получает StartSession после коммита.
	signedIn[guest] = []int64{pc1}
	if n := tick(); n != 1 || !arrived() {
		t.Fatalf("гость за своим ПК: запущено %d", n)
	}
	var sessionUser int64
	var endsAt time.Time
	if err := d.Owner.QueryRow(ctx, "SELECT current_session_user_id, session_ends_at FROM club_computers WHERE id = $1", pc1).Scan(&sessionUser, &endsAt); err != nil {
		t.Fatal(err)
	}
	if sessionUser != guest || !endsAt.Equal(time.Date(2026, 11, 15, 12, 55, 0, 0, time.UTC)) {
		t.Fatalf("сессия ПК: гость %d, конец %s", sessionUser, endsAt)
	}
	if len(rec.started) != 1 || rec.started[0].GetUserId() != guest || !rec.started[0].GetEndsAt().AsTime().Equal(endsAt) {
		t.Fatalf("StartSession агенту = %v", rec.started)
	}
	if n := scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'reservation.arrived' AND user_id = $2", club, guest); n != 1 {
		t.Fatalf("журнал reservation.arrived от гостя: %d", n)
	}
	// Следующий тик: брони среди кандидатов уже нет — ни запросов присутствия, ни второго запуска.
	before := lookups
	if n := tick(); n != 0 || lookups != before || len(rec.started) != 1 {
		t.Fatalf("повторный тик: запущено %d, запросов присутствия %d", n, lookups-before)
	}
	if main, _ := d.Balances(t, club, guest); main != 400_00 {
		t.Fatalf("баланс гостя = %d: запуск времени денег не двигает", main)
	}

	// Сверка балансов с журналом (воркер ledger:reconcile, полный проход) с новыми видами операций —
	// reservation_payment и reservation_refund: расхождений нет.
	tctx := activity.WithRequestMeta(ctx, activity.RequestMeta{IP: "127.0.0.1"})
	if err := db.InTenantTx(tctx, d.App, club, func(q *store.Queries) error {
		minutes := int64(30)
		out, err := reservations.Book(tctx, q, reservations.BookRequest{
			ClubID: club, UserID: guest, ComputerIDs: []int64{pc2}, StartsAt: now.Add(3 * time.Hour), Minutes: &minutes,
		}, reservations.Env{Now: now, Actor: billing.BalanceActor{UserID: guest, Kind: "guest"}})
		if err != nil {
			return err
		}
		return reservations.CancelByGuest(tctx, q, club, guest, out.ReservationIDs[0],
			reservations.Env{Now: now, Actor: billing.BalanceActor{UserID: guest, Kind: "guest"}})
	}); err != nil {
		t.Fatal(err)
	}
	if n := scalar("SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind IN ('reservation_payment', 'reservation_refund')", club); n != 3 {
		t.Fatalf("операций ledger по броням = %d, ожидалось 3", n)
	}
	rw := &worker.LedgerReconcile{Platform: d.Platform, App: d.App, Now: func() time.Time { return now }}
	res, err := rw.Tick(ctx)
	if err != nil || !res.Ran {
		t.Fatalf("ledger:reconcile: %+v, %v", res, err)
	}
	if n := scalar("SELECT count(*) FROM ledger_reconcile_findings"); n != 0 {
		t.Fatalf("сверка нашла расхождения после оплаты и возврата брони: %d", n)
	}
}
