//go:build integration

package reservations_test

// Брони из гостевого приложения на реальном Postgres: сценарии GS-538…GS-551 (docs/golden-scenarios.md,
// Laravel: tests/Feature/GuestBookingTest.php) и GAP-041…045, 049…051, 058. Деньги — до тиына, через ledger.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

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

var (
	seqMu sync.Mutex
	seqN  int
)

func seq() int {
	seqMu.Lock()
	defer seqMu.Unlock()
	seqN++
	return seqN
}

// world — клуб теста: bookingClub() старых тестов (пояс UTC, интервал 10 минут, бронь из приложения включена).
type world struct {
	t    *testing.T
	club int64
	now  time.Time
	svc  *service.Service
}

type clockFn func() time.Time

func (f clockFn) Now() time.Time { return f() }

const bookingSettings = `{"club":{"booking_interval":10,"app_booking_enabled":true}}`

func newWorld(t *testing.T, now time.Time, settings, tz string) *world {
	t.Helper()
	w := &world{t: t, club: fixDB.Club(t, fmt.Sprintf("booking-%d", seq())), now: now}
	exec(t, "UPDATE clubs SET settings = $2::jsonb, timezone = $3 WHERE id = $1", w.club, settings, tz)
	w.svc = service.New(fixDB.App, clockFn(func() time.Time { return w.now }), nil)
	return w
}

func exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := fixDB.Owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func scalar[T any](t *testing.T, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := fixDB.Owner.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

func (w *world) zone(name string, pricePerHour *int64) int64 {
	w.t.Helper()
	return scalar[int64](w.t, "INSERT INTO club_zones (club_id, name, price_per_hour, sort_order, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now()) RETURNING id",
		w.club, name, pricePerHour, seq())
}

func (w *world) computer(zone, number int64) int64 {
	w.t.Helper()
	return scalar[int64](w.t, `INSERT INTO club_computers (club_id, club_zone_id, number, last_seen_at, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'online', now(), now()) RETURNING id`, w.club, zone, number, w.now)
}

// guest — bookingGuest(): гость с балансами (через ledger).
func (w *world) guest(main, bonus money.Tiyn) int64 {
	w.t.Helper()
	id := fixDB.Customer(w.t, w.club, fmt.Sprintf("Гость %d", seq()))
	if main > 0 || bonus > 0 {
		if err := db.InTenantTx(context.Background(), fixDB.App, w.club, func(q *store.Queries) error {
			_, err := billing.SetInitialBalance(context.Background(), q, billing.InitialBalanceParams{ClubID: w.club, UserID: id, Main: main, Bonus: bonus})
			return err
		}); err != nil {
			w.t.Fatal(err)
		}
	}
	return id
}

func (w *world) staff() int64 {
	w.t.Helper()
	id := fixDB.Customer(w.t, w.club, fmt.Sprintf("Админ %d", seq()))
	exec(w.t, "UPDATE club_memberships SET role = 'admin' WHERE club_id = $1 AND user_id = $2", w.club, id)
	return id
}

type tariffSpec struct {
	name          string
	minutes       int64
	price         money.Tiyn
	method        string
	quantityLimit *int64
	usageLimit    *int64
	zones         []int64
	validity      string
}

func (w *world) tariff(s tariffSpec) int64 {
	w.t.Helper()
	if s.method == "" {
		s.method = "main_or_bonus"
	}
	var validity *string
	if s.validity != "" {
		validity = &s.validity
	}
	id := scalar[int64](w.t, `INSERT INTO tariffs (club_id, name, duration_minutes, price, payment_method,
		usage_limit_enabled, usage_limit_minutes, quantity_limit_enabled, quantity_limit, validity_periods, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::bigint IS NOT NULL, $6::bigint, $7::bigint IS NOT NULL, $7::bigint, $8::jsonb, true, now(), now())
		RETURNING id`, w.club, s.name, s.minutes, int64(s.price), s.method, s.usageLimit, s.quantityLimit, validity)
	for _, z := range s.zones {
		exec(w.t, "INSERT INTO club_zone_tariff (club_id, club_zone_id, tariff_id) VALUES ($1, $2, $3)", w.club, z, id)
	}
	return id
}

// tx — тенантная транзакция клуба с данными запроса для журнала.
func (w *world) tx(fn func(ctx context.Context, q *store.Queries) error) error {
	ctx := activity.WithRequestMeta(context.Background(), activity.RequestMeta{IP: "127.0.0.1"})
	return db.InTenantTx(ctx, fixDB.App, w.club, func(q *store.Queries) error { return fn(ctx, q) })
}

func (w *world) env(actor int64, kind string) reservations.Env {
	return reservations.Env{Now: w.now, Actor: billing.BalanceActor{UserID: actor, Kind: kind}}
}

func (w *world) info(ctx context.Context, q *store.Queries) club.Info {
	info, err := club.Load(ctx, q, w.club)
	if err != nil {
		w.t.Fatal(err)
	}
	return info
}

type bookOpt func(*reservations.BookRequest)

func withTariff(id int64) bookOpt {
	return func(r *reservations.BookRequest) { r.TariffID, r.Minutes = &id, nil }
}
func withKey(key string) bookOpt { return func(r *reservations.BookRequest) { r.IdempotencyKey = key } }

// book — POST /api/guest/bookings.
func (w *world) book(user int64, pcs []int64, startsAt time.Time, minutes int64, opts ...bookOpt) (reservations.BookResult, error) {
	req := reservations.BookRequest{ClubID: w.club, UserID: user, ComputerIDs: pcs, StartsAt: startsAt, Minutes: &minutes}
	for _, o := range opts {
		o(&req)
	}
	var out reservations.BookResult
	err := w.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = reservations.Book(ctx, q, req, w.env(user, "guest"))
		return err
	})
	return out, err
}

func (w *world) mustBook(user int64, pcs []int64, startsAt time.Time, minutes int64, opts ...bookOpt) reservations.BookResult {
	w.t.Helper()
	out, err := w.book(user, pcs, startsAt, minutes, opts...)
	if err != nil {
		w.t.Fatalf("бронь: %v", err)
	}
	return out
}

func (w *world) guestCancel(user, id int64) error {
	return w.tx(func(ctx context.Context, q *store.Queries) error {
		return reservations.CancelByGuest(ctx, q, w.club, user, id, w.env(user, "guest"))
	})
}

func (w *world) staffCancel(staff, id int64) error {
	return w.tx(func(ctx context.Context, q *store.Queries) error {
		return reservations.Cancel(ctx, q, w.info(ctx, q), id, w.env(staff, "staff"))
	})
}

func (w *world) staffArrive(staff, id int64) error {
	return w.tx(func(ctx context.Context, q *store.Queries) error {
		return reservations.Arrive(ctx, q, w.info(ctx, q), id, w.env(staff, "staff"))
	})
}

// signIn — гость вошёл на ПК (в старом — heartbeat ПК с токеном клиента).
func (w *world) signIn(pc, user int64) (reservations.Started, bool) {
	w.t.Helper()
	var out reservations.Started
	var ok bool
	if err := w.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		out, ok, err = reservations.StartForSignedIn(ctx, q, w.info(ctx, q), pc, user, w.env(user, "guest"))
		return err
	}); err != nil {
		w.t.Fatalf("вход гостя на ПК: %v", err)
	}
	return out, ok
}

func (w *world) byCode(pc int64, code string) (reservations.Started, error) {
	var out reservations.Started
	err := w.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = reservations.StartByCode(ctx, q, w.info(ctx, q), pc, code, w.env(0, "agent"))
		return err
	})
	return out, err
}

func (w *world) list(user int64) reservations.AppIndex {
	w.t.Helper()
	var out reservations.AppIndex
	if err := w.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = reservations.ListForGuest(ctx, q, w.club, user, w.now)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) group(user, id int64) reservations.AppBooking {
	w.t.Helper()
	var out reservations.AppBooking
	if err := w.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = reservations.Group(ctx, q, w.club, user, id, w.now)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) wantBalances(user int64, main, bonus money.Tiyn) {
	w.t.Helper()
	gotMain, gotBonus := fixDB.Balances(w.t, w.club, user)
	if money.Tiyn(gotMain) != main || money.Tiyn(gotBonus) != bonus {
		w.t.Fatalf("балансы = %s / %s, ожидалось %s / %s", money.Tiyn(gotMain), money.Tiyn(gotBonus), main, bonus)
	}
}

func (w *world) reservationsCount() int64 {
	return scalar[int64](w.t, "SELECT count(*) FROM computer_reservations WHERE club_id = $1", w.club)
}

// noDrift — проекция балансов совпадает с журналом операций (то, что сверяет воркер ledger:reconcile), и каждая
// операция сбалансирована.
func (w *world) noDrift() {
	w.t.Helper()
	if err := w.tx(func(ctx context.Context, q *store.Queries) error {
		rows, err := q.ListMemberBalanceDrift(ctx, w.club)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			return fmt.Errorf("расхождение балансов с журналом: %+v", rows)
		}
		return nil
	}); err != nil {
		w.t.Fatal(err)
	}
	if n := scalar[int64](w.t, `SELECT count(*) FROM (SELECT transaction_id FROM ledger_entries WHERE club_id = $1
		GROUP BY transaction_id HAVING sum(amount) <> 0) x`, w.club); n != 0 {
		w.t.Fatalf("несбалансированных операций: %d", n)
	}
}

type ledgerRow struct {
	Kind, Account string
	Amount        int64
}

// ledgerOf — проводки операций клуба со ссылкой на бронь.
func (w *world) ledgerOf(reservationID int64) []ledgerRow {
	w.t.Helper()
	rows, err := fixDB.Owner.Query(context.Background(), `
		SELECT t.kind, a.kind, e.amount
		FROM ledger_transactions t
		JOIN ledger_entries e ON e.club_id = t.club_id AND e.transaction_id = t.id
		JOIN ledger_accounts a ON a.id = e.account_id
		WHERE t.club_id = $1 AND t.reservation_id = $2
		ORDER BY t.id, a.kind`, w.club, reservationID)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []ledgerRow
	for rows.Next() {
		var r ledgerRow
		if err := rows.Scan(&r.Kind, &r.Account, &r.Amount); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

type resRow struct {
	FromApp     bool
	Main, Bonus int64
	TariffID    *int64
	Code        *string
	StartsAt    time.Time
	EndsAt      time.Time
	ArrivedAt   *time.Time
	CancelledAt *time.Time
}

func (w *world) reservation(id int64) resRow {
	w.t.Helper()
	var r resRow
	if err := fixDB.Owner.QueryRow(context.Background(), `SELECT from_app, paid_main_cents, paid_bonus_cents, tariff_id, code, starts_at, ends_at,
		arrived_at, cancelled_at FROM computer_reservations WHERE club_id = $1 AND id = $2`, w.club, id). //nolint:misspell // колонка старой схемы
		Scan(&r.FromApp, &r.Main, &r.Bonus, &r.TariffID, &r.Code, &r.StartsAt, &r.EndsAt, &r.ArrivedAt, &r.CancelledAt); err != nil {
		w.t.Fatal(err)
	}
	return r
}

type pcState struct {
	SessionID *string
	UserID    *int64
	Guest     bool
	EndsAt    *time.Time
}

func (w *world) pc(id int64) pcState {
	w.t.Helper()
	var s pcState
	if err := fixDB.Owner.QueryRow(context.Background(),
		"SELECT current_session_id, current_session_user_id, session_guest, session_ends_at FROM club_computers WHERE id = $1", id).
		Scan(&s.SessionID, &s.UserID, &s.Guest, &s.EndsAt); err != nil {
		w.t.Fatal(err)
	}
	return s
}

type partRow struct {
	Kind     string
	TariffID *int64
	Name     string
	Minutes  int64
	Main     int64
	Bonus    int64
}

func (w *world) parts(sessionID string) []partRow {
	w.t.Helper()
	rows, err := fixDB.Owner.Query(context.Background(), `SELECT kind, tariff_id, name, minutes, main_cents, bonus_cents
		FROM session_purchases WHERE club_id = $1 AND session_id = $2 ORDER BY id`, w.club, sessionID)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []partRow
	for rows.Next() {
		var p partRow
		if err := rows.Scan(&p.Kind, &p.TariffID, &p.Name, &p.Minutes, &p.Main, &p.Bonus); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func (w *world) lastActivity(event string) (userID int64, details map[string]any) {
	w.t.Helper()
	var raw []byte
	if err := fixDB.Owner.QueryRow(context.Background(),
		"SELECT user_id, details FROM activity_logs WHERE club_id = $1 AND event = $2 ORDER BY id DESC LIMIT 1", w.club, event).
		Scan(&userID, &raw); err != nil {
		w.t.Fatalf("журнал %s: %v", event, err)
	}
	_ = json.Unmarshal(raw, &details)
	return userID, details
}

func (w *world) activityCount(event string) int64 {
	return scalar[int64](w.t, "SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = $2", w.club, event)
}

func wantError(t *testing.T, err error, field, message string) *reservations.Error {
	t.Helper()
	var e *reservations.Error
	if !errors.As(err, &e) || e.Field != field || (message != "" && e.Message != message) {
		t.Fatalf("ошибка = %v, ожидалась ошибка поля %s: %s", err, field, message)
	}
	return e
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, ожидалось %v", what, got, want)
	}
}

func at(day, hour, minute int) time.Time {
	return time.Date(2026, 11, day, hour, minute, 0, 0, time.UTC)
}

func p64(v int64) *int64 { return &v }

const tenge = money.Tiyn(100)

// vip — bookingComputer(): n ПК в зоне «VIP» с ценой часа 600 ₸.
func (w *world) vip(n int) (zone int64, pcs []int64) {
	zone = w.zone("VIP", p64(60000))
	for i := 1; i <= n; i++ {
		pcs = append(pcs, w.computer(zone, int64(i)))
	}
	return zone, pcs
}

// ---------------------------------------------------------------------------
// GS-538 … GS-551
// ---------------------------------------------------------------------------

func TestAppBooking(t *testing.T) {
	t.Run("GS-538 бронь из приложения выключена, пока клуб её не включит", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), `{"club":{"booking_interval":10}}`, "UTC")
		_, pcs := w.vip(1)
		g := w.guest(1000*tenge, 0)
		eq(t, "enabled", w.list(g).Enabled, false)
		_, err := w.book(g, pcs, at(15, 12, 0), 60)
		if !errors.Is(err, reservations.ErrAppDisabled) {
			t.Fatalf("ошибка = %v, ожидалась ErrAppDisabled (403)", err)
		}
		if err := w.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := reservations.AppComputers(ctx, q, w.club, g)
			return err
		}); !errors.Is(err, reservations.ErrAppDisabled) {
			t.Fatalf("computers: ошибка = %v, ожидалась ErrAppDisabled", err)
		}
		eq(t, "броней", w.reservationsCount(), 0)
		w.wantBalances(g, 1000*tenge, 0)
	})

	t.Run("GS-539 гость бронирует свободный ПК и платит цену зоны с баланса", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(2)
		g := w.guest(1000*tenge, 0)
		// starts_at "2026-11-15T17:00:00+05:00" = 12:00 UTC.
		out := w.mustBook(g, pcs[:1], time.Date(2026, 11, 15, 17, 0, 0, 0, time.FixedZone("", 5*3600)), 90)
		b := w.group(g, out.ReservationIDs[0])
		eq(t, "ПК", strings.Join(b.Computers, ", "), "PC-01")
		eq(t, "зона", *b.Zone, "VIP")
		eq(t, "начало", b.StartsAt.UTC(), at(15, 12, 0))
		eq(t, "оплачено", b.Paid, 900*tenge)
		eq(t, "состояние", b.State, "pending")
		r := w.reservation(out.ReservationIDs[0])
		eq(t, "from_app", r.FromApp, true)
		eq(t, "paid_main_cents", r.Main, 90000)
		eq(t, "paid_bonus_cents", r.Bonus, 0)
		eq(t, "ends_at", r.EndsAt.UTC(), at(15, 13, 30))
		w.wantBalances(g, 100*tenge, 0)
		user, details := w.lastActivity("reservation.paid")
		eq(t, "журнал reservation.paid от гостя", user, g)
		eq(t, "amount", details["amount"], any("900.00"))
		eq(t, "main_balance_amount", details["main_balance_amount"], any("900.00"))
		eq(t, "duration_minutes", details["duration_minutes"], any(float64(90)))
		// Ledger: одна операция reservation_payment со ссылкой на бронь.
		rows := w.ledgerOf(out.ReservationIDs[0])
		if len(rows) != 2 || rows[0] != (ledgerRow{"reservation_payment", "member_main", -90000}) ||
			rows[1] != (ledgerRow{"reservation_payment", "revenue_time", 90000}) {
			t.Fatalf("проводки = %+v", rows)
		}
		eq(t, "накопительные траты", scalar[int64](t, "SELECT total_spent FROM club_memberships WHERE club_id = $1 AND user_id = $2", w.club, g), 90000)

		// Далее: options на 13:35, 60 минут — ПК-01 занят (интервал 10 мин), ПК-02 свободен.
		var zones []reservations.AppZone
		var minutes int64
		if err := w.tx(func(ctx context.Context, q *store.Queries) error {
			var err error
			minutes, zones, err = reservations.AppOptions(ctx, q, w.club, g, at(15, 13, 35), p64(60), nil, w.now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		eq(t, "minutes", minutes, 60)
		eq(t, "цена зоны", zones[0].Price, 600*tenge)
		eq(t, "ПК-01 свободен", zones[0].Computers[0].Available, false)
		eq(t, "ПК-02 свободен", zones[0].Computers[1].Available, true)

		// Далее: бронь ПК-02 на 15:00 при балансе 100.00 — не хватает денег.
		_, err := w.book(g, pcs[1:], at(15, 15, 0), 60)
		e := wantError(t, err, "balance", reservations.MsgShort)
		eq(t, "цена в тексте", e.Params["price"], "600.00")
		eq(t, "броней", w.reservationsCount(), 1)
		w.wantBalances(g, 100*tenge, 0)
		w.noDrift()
	})

	t.Run("GS-540 у гостя не больше двух броней впереди; нельзя в прошлое и слишком далеко", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		g := w.guest(10000*tenge, 0)
		_, err := w.book(g, pcs, at(15, 9, 0), 60)
		wantError(t, err, "starts_at", reservations.MsgPast)
		_, err = w.book(g, pcs, at(30, 9, 0), 60)
		wantError(t, err, "starts_at", reservations.MsgTooFar)
		w.mustBook(g, pcs, at(15, 12, 0), 60)
		w.mustBook(g, pcs, at(15, 14, 0), 60)
		_, err = w.book(g, pcs, at(15, 16, 0), 60)
		e := wantError(t, err, "starts_at", reservations.MsgAheadLimit)
		eq(t, "count", e.Params["count"], "2")
		eq(t, "броней", w.reservationsCount(), 2)
		w.wantBalances(g, 8800*tenge, 0)
		// Длительность вне 30…720 минут.
		_, err = w.book(g, pcs, at(15, 18, 0), 29)
		wantError(t, err, "duration_minutes", reservations.MsgDuration)
		_, err = w.book(g, pcs, at(15, 18, 0), 721)
		wantError(t, err, "duration_minutes", reservations.MsgDuration)
	})

	t.Run("GS-541 гость отменяет бронь до начала и получает деньги назад, если клуб разрешает", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		g := w.guest(1000*tenge, 0)
		out := w.mustBook(g, pcs, at(15, 12, 0), 60)
		w.wantBalances(g, 400*tenge, 0)
		eq(t, "can_cancel", w.group(g, out.ReservationIDs[0]).CanCancel, true)
		if err := w.guestCancel(g, out.ReservationIDs[0]); err != nil {
			t.Fatal(err)
		}
		if w.reservation(out.ReservationIDs[0]).CancelledAt == nil {
			t.Fatal("cancelled_at не задан") //nolint:misspell // колонка старой схемы
		}
		w.wantBalances(g, 1000*tenge, 0)
		eq(t, "накопительные траты", scalar[int64](t, "SELECT total_spent FROM club_memberships WHERE club_id = $1 AND user_id = $2", w.club, g), 0)
		user, details := w.lastActivity("reservation.refunded")
		eq(t, "журнал reservation.refunded от гостя", user, g)
		eq(t, "main_balance_amount", details["main_balance_amount"], any("600.00"))
		rows := w.ledgerOf(out.ReservationIDs[0])
		if len(rows) != 4 || rows[2] != (ledgerRow{"reservation_refund", "member_main", 60000}) ||
			rows[3] != (ledgerRow{"reservation_refund", "revenue_time", -60000}) {
			t.Fatalf("проводки = %+v", rows)
		}
		if n := scalar[int64](t, `SELECT count(*) FROM ledger_transactions r JOIN ledger_transactions p ON p.id = r.reverses_transaction_id
			WHERE r.club_id = $1 AND r.kind = 'reservation_refund' AND p.kind = 'reservation_payment'`, w.club); n != 1 {
			t.Fatalf("возврат не ссылается на операцию оплаты")
		}
		// Повторная отмена — бронь уже закрыта, второго возврата нет.
		wantError(t, w.guestCancel(g, out.ReservationIDs[0]), "reservation", reservations.MsgStartedOrClosed)
		w.wantBalances(g, 1000*tenge, 0)

		// app_booking_cancel = false: бронь создаётся, отменить её гость не может.
		exec(t, `UPDATE clubs SET settings = '{"club":{"booking_interval":10,"app_booking_enabled":true,"app_booking_cancel":false}}' WHERE id = $1`, w.club)
		out = w.mustBook(g, pcs, at(15, 14, 0), 60)
		eq(t, "can_cancel", w.group(g, out.ReservationIDs[0]).CanCancel, false)
		wantError(t, w.guestCancel(g, out.ReservationIDs[0]), "reservation", reservations.MsgGuestNoCancel)
		w.wantBalances(g, 400*tenge, 0)
		w.noDrift()
	})

	t.Run("GS-542 гость не может отменить чужую бронь и бронь, которая началась", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		g, other := w.guest(1000*tenge, 0), w.guest(0, 0)
		out := w.mustBook(g, pcs, at(15, 10, 30), 60)
		if err := w.guestCancel(other, out.ReservationIDs[0]); !errors.Is(err, reservations.ErrNotFound) {
			t.Fatalf("чужая бронь: ошибка = %v, ожидалась ErrNotFound (404)", err)
		}
		w.now = at(15, 10, 40)
		wantError(t, w.guestCancel(g, out.ReservationIDs[0]), "reservation", reservations.MsgStartedOrClosed)
		w.wantBalances(g, 400*tenge, 0)
	})

	t.Run("GS-543 сотрудник отменяет оплаченную бронь — деньги возвращаются", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), `{"club":{"booking_interval":10,"app_booking_enabled":true,"app_booking_cancel":false}}`, "UTC")
		_, pcs := w.vip(1)
		g, admin := w.guest(1000*tenge, 0), w.staff()
		out := w.mustBook(g, pcs, at(15, 12, 0), 60)
		w.wantBalances(g, 400*tenge, 0)
		if err := w.staffCancel(admin, out.ReservationIDs[0]); err != nil {
			t.Fatal(err)
		}
		w.wantBalances(g, 1000*tenge, 0)
		user, _ := w.lastActivity("reservation.refunded")
		eq(t, "журнал reservation.refunded от сотрудника", user, admin)
		// Идущую бронь (началась, гость не пришёл) сотрудник тоже отменяет с полным возвратом.
		out = w.mustBook(g, pcs, at(15, 13, 0), 60)
		w.now = at(15, 13, 20)
		if err := w.staffCancel(admin, out.ReservationIDs[0]); err != nil {
			t.Fatal(err)
		}
		w.wantBalances(g, 1000*tenge, 0)
		wantError(t, w.staffCancel(admin, out.ReservationIDs[0]), "reservation", reservations.MsgClosed)
		w.wantBalances(g, 1000*tenge, 0)
		w.noDrift()
	})

	t.Run("GS-544 оплаченное время начинается, когда гость входит на забронированный ПК", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		g, other := w.guest(1000*tenge, 0), w.guest(1000*tenge, 0)
		out := w.mustBook(g, pcs, at(15, 12, 0), 60)
		if _, ok := w.signIn(pcs[0], g); ok {
			t.Fatal("в 10:00 рано: время не должно начаться")
		}
		w.now = at(15, 11, 55)
		if _, ok := w.signIn(pcs[0], other); ok {
			t.Fatal("другой гость: время не должно начаться")
		}
		s, ok := w.signIn(pcs[0], g)
		if !ok {
			t.Fatal("в 11:55 (интервал 10 мин) время должно начаться")
		}
		eq(t, "session.user_id", s.UserID, g)
		eq(t, "session.ends_at", s.EndsAt.UTC(), at(15, 12, 55))
		pc := w.pc(pcs[0])
		eq(t, "ПК: гость сессии", *pc.UserID, g)
		eq(t, "ПК: конец сессии", pc.EndsAt.UTC(), at(15, 12, 55))
		if w.reservation(out.ReservationIDs[0]).ArrivedAt == nil {
			t.Fatal("arrived_at не задан")
		}
		parts := w.parts(s.SessionID)
		if len(parts) != 1 || parts[0] != (partRow{Kind: "minutes", Name: "Бронь", Minutes: 60, Main: 60000}) {
			t.Fatalf("часть сессии = %+v", parts)
		}
		user, _ := w.lastActivity("reservation.arrived")
		eq(t, "журнал reservation.arrived от гостя", user, g)
		// Повторный вход ничего не меняет (запуск идемпотентен по arrived_at).
		if _, ok := w.signIn(pcs[0], g); ok {
			t.Fatal("повторный запуск той же брони")
		}
		eq(t, "частей сессии", len(w.parts(s.SessionID)), 1)
		w.wantBalances(g, 400*tenge, 0) // запуск денег не двигает
		w.noDrift()
	})

	t.Run("GS-545 опоздавший гость получает остаток брони; отметка сотрудника тоже запускает время", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		g, admin := w.guest(1000*tenge, 0), w.staff()
		out := w.mustBook(g, pcs, at(15, 12, 0), 60)
		w.now = at(15, 12, 30)
		if err := w.staffArrive(admin, out.ReservationIDs[0]); err != nil {
			t.Fatal(err)
		}
		pc := w.pc(pcs[0])
		eq(t, "ПК: гость сессии", *pc.UserID, g)
		eq(t, "ПК: конец сессии", pc.EndsAt.UTC(), at(15, 13, 0))
		parts := w.parts(*pc.SessionID)
		if len(parts) != 1 || parts[0].Minutes != 30 || parts[0].Main != 30000 {
			t.Fatalf("часть сессии = %+v, ожидалось 30 минут и 300 ₸", parts)
		}
		w.wantBalances(g, 400*tenge, 0) // пропущенная половина не возвращается
		user, _ := w.lastActivity("reservation.arrived")
		eq(t, "журнал reservation.arrived от сотрудника", user, admin)
	})

	t.Run("GS-546 покупка времени на ПК не закрывает оплаченную бронь гостя", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		g := w.guest(2000*tenge, 0)
		out := w.mustBook(g, pcs, at(15, 11, 0), 60)
		if _, err := w.svc.PurchaseMinutes(context.Background(), service.MinutesPurchaseRequest{
			ClubID: w.club, UserID: g, ComputerID: pcs[0], Minutes: 60, IdempotencyKey: fmt.Sprintf("m-%d", seq()),
			Actor: service.Actor{UserID: g, Kind: "guest"},
		}); err != nil {
			t.Fatalf("покупка минут: %v", err)
		}
		if w.reservation(out.ReservationIDs[0]).ArrivedAt != nil {
			t.Fatal("оплаченная бронь закрыта покупкой времени")
		}
		w.wantBalances(g, 800*tenge, 0)
		// Неоплаченную бронь с кассы покупка времени закрывает, как раньше (markArrivedBy).
		admin := w.staff()
		var cashID int64
		if err := w.tx(func(ctx context.Context, q *store.Queries) error {
			var err error
			cashID, err = reservations.Create(ctx, q, w.info(ctx, q), admin, reservations.Booking{
				ComputerID: pcs[0], UserID: g, CustomerName: "Гость", StartsAt: at(15, 14, 0), EndsAt: at(15, 15, 0),
			}, w.now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		w.now = at(15, 13, 55)
		if _, err := w.svc.PurchaseMinutes(context.Background(), service.MinutesPurchaseRequest{
			ClubID: w.club, UserID: g, ComputerID: pcs[0], Minutes: 30, IdempotencyKey: fmt.Sprintf("m-%d", seq()),
			Actor: service.Actor{UserID: g, Kind: "guest"},
		}); err != nil {
			t.Fatalf("покупка минут: %v", err)
		}
		if w.reservation(cashID).ArrivedAt == nil {
			t.Fatal("неоплаченная бронь не отмечена выполненной")
		}
	})

	t.Run("GS-547 бронь тарифом зоны: цена тарифа, его время, его лимит количества", func(t *testing.T) {
		w := newWorld(t, at(16, 10, 0), bookingSettings, "UTC") // понедельник
		vip := w.zone("VIP", p64(60000))
		common := w.zone("Общий", p64(30000))
		pc1, pc2 := w.computer(vip, 1), w.computer(common, 2)
		g := w.guest(5000*tenge, 0)
		three := w.tariff(tariffSpec{name: "3 часа", minutes: 180, price: 1200 * tenge, zones: []int64{vip}, quantityLimit: p64(1)})
		w.tariff(tariffSpec{name: "Ночь", minutes: 600, price: 2000 * tenge,
			validity: `[{"start_day":"mon","start_time":"22:00","end_day":"tue","end_time":"08:00"}]`})
		tariffs := func(start time.Time, pcs ...int64) []reservations.AppTariff {
			var out []reservations.AppTariff
			if err := w.tx(func(ctx context.Context, q *store.Queries) error {
				var err error
				out, err = reservations.AppTariffs(ctx, q, w.club, g, start, pcs, w.now)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return out
		}
		list := tariffs(at(16, 12, 0))
		if len(list) != 1 || list[0].ID != three || list[0].Minutes != 180 || list[0].Price != 1200*tenge ||
			len(list[0].ZoneIDs) != 1 || list[0].ZoneIDs[0] != vip {
			t.Fatalf("тарифы на 12:00 = %+v", list)
		}
		var night *reservations.AppTariff
		for _, x := range tariffs(at(16, 23, 0)) {
			if x.Name == "Ночь" {
				night = &x
			}
		}
		if night == nil || night.Minutes != 540 {
			t.Fatalf("«Ночь» на 23:00 = %+v, ожидалось 540 минут (обрезан в 08:00)", night)
		}
		var zones []reservations.AppZone
		var minutes int64
		if err := w.tx(func(ctx context.Context, q *store.Queries) error {
			var err error
			minutes, zones, err = reservations.AppOptions(ctx, q, w.club, g, at(16, 12, 0), nil, &three, w.now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if minutes != 180 || len(zones) != 1 || zones[0].Name != "VIP" || zones[0].Price != 1200*tenge {
			t.Fatalf("options = %d мин, %+v", minutes, zones)
		}
		_, err := w.book(g, []int64{pc2}, at(16, 12, 0), 0, withTariff(three))
		wantError(t, err, "club_computer_ids", reservations.MsgCannotBookName)
		out := w.mustBook(g, []int64{pc1}, at(16, 12, 0), 0, withTariff(three))
		b := w.group(g, out.ReservationIDs[0])
		eq(t, "тариф", *b.Tariff, "3 часа")
		eq(t, "оплачено", b.Paid, 1200*tenge)
		eq(t, "конец", b.EndsAt.UTC(), at(16, 15, 0))
		eq(t, "tariff_id", *w.reservation(out.ReservationIDs[0]).TariffID, three)
		w.wantBalances(g, 3800*tenge, 0)
		_, err = w.book(g, []int64{pc1}, at(16, 16, 0), 0, withTariff(three))
		wantError(t, err, "tariff_id", "") // лимит 1 исчерпан
		w.now = at(16, 12, 0)
		s, ok := w.signIn(pc1, g)
		if !ok {
			t.Fatal("время брони не началось")
		}
		eq(t, "session.ends_at", s.EndsAt.UTC(), at(16, 15, 0))
		parts := w.parts(s.SessionID)
		if len(parts) != 1 || parts[0].Kind != "tariff" || parts[0].TariffID == nil || *parts[0].TariffID != three || parts[0].Minutes != 180 ||
			parts[0].Name != "3 часа" || parts[0].Main != 120000 {
			t.Fatalf("часть сессии = %+v", parts)
		}
		w.noDrift()
	})

	t.Run("GS-548 гость сначала выбирает ПК: ПК клуба по зонам, затем только тарифы его зоны", func(t *testing.T) {
		w := newWorld(t, at(16, 10, 0), bookingSettings, "UTC")
		vip := w.zone("VIP", p64(60000))
		common := w.zone("Общий", nil)
		pc1, pc2, pc3 := w.computer(vip, 1), w.computer(common, 2), w.computer(common, 3)
		exec(t, "UPDATE club_computers SET is_reserved = true WHERE id = $1", pc3)
		g := w.guest(1000*tenge, 0)
		w.tariff(tariffSpec{name: "VIP 3 часа", minutes: 180, price: 1200 * tenge, zones: []int64{vip}})
		w.tariff(tariffSpec{name: "Час", minutes: 60, price: 300 * tenge})
		var zones []reservations.AppZone
		if err := w.tx(func(ctx context.Context, q *store.Queries) error {
			var err error
			zones, err = reservations.AppComputers(ctx, q, w.club, g)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(zones) != 2 || zones[0].Name != "VIP" || zones[1].Name != "Общий" || *zones[0].PricePerHour != 600*tenge || zones[1].PricePerHour != nil ||
			len(zones[1].Computers) != 1 || zones[1].Computers[0].ID != pc2 || zones[1].Computers[0].Name != "PC-02" {
			t.Fatalf("computers = %+v", zones)
		}
		names := func(pc int64) string {
			var list []reservations.AppTariff
			if err := w.tx(func(ctx context.Context, q *store.Queries) error {
				var err error
				list, err = reservations.AppTariffs(ctx, q, w.club, g, at(16, 12, 0), []int64{pc}, w.now)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, x := range list {
				out = append(out, x.Name)
			}
			return strings.Join(out, ", ")
		}
		eq(t, "тарифы ПК-02", names(pc2), "Час")
		eq(t, "тарифы ПК-01 (длительность, имя)", names(pc1), "Час, VIP 3 часа")
		// GAP-051: зона без цены часа по часам не бронируется; служебный ПК — тоже.
		_, err := w.book(g, []int64{pc2}, at(16, 12, 0), 60)
		wantError(t, err, "club_computer_ids", reservations.MsgCannotBookName)
		_, err = w.book(g, []int64{pc3}, at(16, 12, 0), 60, withTariff(0))
		wantError(t, err, "tariff_id", reservations.MsgTariffTime)
	})

	t.Run("GS-549 журнал и агент показывают время брони в поясе клуба", func(t *testing.T) {
		w := newWorld(t, at(16, 3, 0), bookingSettings, "Asia/Almaty")
		_, pcs := w.vip(1)
		g, other := w.guest(1000*tenge, 0), w.guest(5000*tenge, 0)
		w.mustBook(g, pcs, at(16, 5, 30), 60)
		_, details := w.lastActivity("reservation.paid")
		eq(t, "reservation.paid starts_at", details["starts_at"], any("16.11.2026 10:30"))
		_, err := w.svc.PurchaseMinutes(context.Background(), service.MinutesPurchaseRequest{
			ClubID: w.club, UserID: other, ComputerID: pcs[0], Minutes: 150, IdempotencyKey: fmt.Sprintf("m-%d", seq()),
			Actor: service.Actor{UserID: other, Kind: "guest"},
		})
		var se *service.Error
		if !errors.As(err, &se) || se.Field != "computer_id" || se.Params["time"] != "10:30" {
			t.Fatalf("покупка другого гостя: ошибка = %v, ожидалась computer_id с временем 10:30", err)
		}
	})

	t.Run("GS-550 несколько ПК вместе: одна оплата, один код, отменяются вместе", func(t *testing.T) {
		w := newWorld(t, at(17, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(2)
		g := w.guest(2000*tenge, 0)
		out := w.mustBook(g, pcs, at(17, 12, 0), 60)
		b := w.group(g, out.ReservationIDs[0])
		eq(t, "ПК", strings.Join(b.Computers, ","), "PC-01,PC-02")
		eq(t, "оплачено", b.Paid, 1200*tenge)
		if len(out.Code) != 6 || strings.Trim(out.Code, "0123456789") != "" {
			t.Fatalf("код = %q, ожидалось 6 цифр", out.Code)
		}
		eq(t, "код второй брони", *w.reservation(out.ReservationIDs[1]).Code, out.Code)
		w.wantBalances(g, 800*tenge, 0)
		eq(t, "броней в списке", len(w.list(g).Bookings), 1)
		eq(t, "операций оплаты", scalar[int64](t, "SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'reservation_payment'", w.club), 1)
		eq(t, "записей reservation.paid", w.activityCount("reservation.paid"), 1)
		eq(t, "записей reservation.created", w.activityCount("reservation.created"), 2)
		if err := w.guestCancel(g, out.ReservationIDs[0]); err != nil {
			t.Fatal(err)
		}
		for _, id := range out.ReservationIDs {
			if w.reservation(id).CancelledAt == nil {
				t.Fatalf("бронь %d не отменена", id)
			}
		}
		w.wantBalances(g, 2000*tenge, 0)
		w.noDrift()
	})

	t.Run("GS-551 код брони открывает только забронированные ПК, с интервала до начала, без аккаунта", func(t *testing.T) {
		w := newWorld(t, at(17, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(3)
		g := w.guest(2000*tenge, 0)
		out := w.mustBook(g, pcs[:2], at(17, 12, 0), 60)
		_, err := w.byCode(pcs[0], out.Code)
		e := wantError(t, err, "code", reservations.MsgCodeEarly)
		eq(t, "время в тексте", e.Params["time"], "17.11 12:00")
		eq(t, "интервал в тексте", e.Params["count"], "10")
		w.now = at(17, 11, 55)
		_, err = w.byCode(pcs[2], out.Code)
		wantError(t, err, "code", reservations.MsgCodeOtherPC)
		wrong := "000000"
		if out.Code == wrong {
			wrong = "000001"
		}
		_, err = w.byCode(pcs[0], wrong)
		wantError(t, err, "code", reservations.MsgCodeNotFound)
		_, err = w.byCode(pcs[0], "12345")
		wantError(t, err, "code", reservations.MsgCodeFormat)
		s, err := w.byCode(pcs[0], out.Code)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "session.guest", s.Guest, true)
		eq(t, "session.user_id", s.UserID, 0)
		eq(t, "session.ends_at", s.EndsAt.UTC(), at(17, 12, 55))
		pc := w.pc(pcs[0])
		if pc.UserID != nil || !pc.Guest || pc.SessionID == nil {
			t.Fatalf("ПК-01 = %+v, ожидалась гостевая сессия без аккаунта", pc)
		}
		eq(t, "частей сессии (по коду их нет)", len(w.parts(*pc.SessionID)), 0)
		s2, err := w.byCode(pcs[1], out.Code)
		if err != nil || !s2.Guest {
			t.Fatalf("ПК-02: %+v, %v", s2, err)
		}
		_, err = w.byCode(pcs[0], out.Code)
		wantError(t, err, "code", "")                                                                                                                                           // повтор кода
		eq(t, "ожидающих броней", scalar[int64](t, "SELECT count(*) FROM computer_reservations WHERE club_id = $1 AND arrived_at IS NULL AND cancelled_at IS NULL", w.club), 0) //nolint:misspell // колонка старой схемы
		if w.pc(pcs[2]).SessionID != nil {
			t.Fatal("на ПК-03 появилась сессия")
		}
		user, details := w.lastActivity("reservation.arrived")
		eq(t, "журнал reservation.arrived от гостя брони", user, g)
		eq(t, "by_code", details["by_code"], any(true))
		w.wantBalances(g, 800*tenge, 0) // по коду денег не двигается
		// Досрочный конец гостевой сессии по коду ничего не возвращает (UQ-15).
		admin := w.staff()
		w.now = at(17, 12, 10)
		if _, err := w.svc.EndSession(context.Background(), service.EndSessionRequest{
			ClubID: w.club, ComputerID: pcs[0], SessionID: s.SessionID, Actor: service.Actor{UserID: admin, Kind: "staff"}, IsStaff: true,
		}); err != nil {
			t.Fatal(err)
		}
		w.wantBalances(g, 800*tenge, 0)
		w.noDrift()
	})
}

// ---------------------------------------------------------------------------
// GAP-041 … GAP-058
// ---------------------------------------------------------------------------

const mixedSettings = `{"club":{"booking_interval":10,"app_booking_enabled":true},"finances":{"allow_mixed_payment":true,"bonus_max_percent":100}}`

func TestAppBooking_Gaps(t *testing.T) {
	t.Run("GAP-041 три ПК, почти всё бонусами: основная часть последнего ПК не отрицательна (UQ-8)", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), mixedSettings, "UTC")
		_, pcs := w.vip(3)
		g := w.guest(1, 5000*tenge) // основной баланс 0,01 ₸
		out := w.mustBook(g, pcs, at(15, 12, 0), 60)
		eq(t, "основная часть", out.Main, 1)
		eq(t, "бонусная часть", out.Bonus, 179999)
		var main, bonus int64
		for i, id := range out.ReservationIDs {
			r := w.reservation(id)
			if r.Main < 0 || r.Bonus < 0 || r.Main+r.Bonus != 60000 {
				t.Fatalf("ПК %d: основная %d, бонусная %d", i, r.Main, r.Bonus)
			}
			main += r.Main
			bonus += r.Bonus
		}
		eq(t, "сумма основных частей по ПК", main, 1)
		eq(t, "сумма бонусных частей по ПК", bonus, 179999)
		w.wantBalances(g, 0, 5000*tenge-179999)
		// Отмена возвращает по ПК ровно то, что списано.
		if err := w.guestCancel(g, out.ReservationIDs[0]); err != nil {
			t.Fatal(err)
		}
		w.wantBalances(g, 1, 5000*tenge)
		w.noDrift()
	})

	t.Run("GAP-041 два ПК разных зон (600 и 300 ₸), бонусы 450 ₸", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), mixedSettings, "UTC")
		vip, common := w.zone("VIP", p64(60000)), w.zone("Общий", p64(30000))
		pc1, pc2 := w.computer(vip, 1), w.computer(common, 2)
		g := w.guest(450*tenge, 1000*tenge)
		out := w.mustBook(g, []int64{pc2, pc1}, at(15, 12, 0), 60) // порядок в запросе не важен: ПК — по id
		r1, r2 := w.reservation(out.ReservationIDs[0]), w.reservation(out.ReservationIDs[1])
		if r1.Main != 30000 || r1.Bonus != 30000 || r2.Main != 15000 || r2.Bonus != 15000 {
			t.Fatalf("части: ПК-01 %d + %d, ПК-02 %d + %d", r1.Main, r1.Bonus, r2.Main, r2.Bonus)
		}
		w.wantBalances(g, 0, 550*tenge)
		_, details := w.lastActivity("reservation.paid")
		eq(t, "зоны в журнале", details["zone"], any("VIP, Общий"))
		eq(t, "bonus_balance_amount", details["bonus_balance_amount"], any("450.00"))
		w.noDrift()
	})

	t.Run("GAP-042 смешанная оплата выключена; тариф только с основного; лимит бонусов от общей суммы", func(t *testing.T) {
		// Без смешанной оплаты: бонусами — вся цена, только если основного не хватает, а бонусов хватает.
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		zone, pcs := w.vip(2)
		g := w.guest(500*tenge, 1300*tenge)
		out := w.mustBook(g, pcs, at(15, 12, 0), 60)
		eq(t, "основная часть", out.Main, 0)
		eq(t, "бонусная часть", out.Bonus, 1200*tenge)
		w.wantBalances(g, 500*tenge, 100*tenge)
		eq(t, "накопительные траты (бонусы не считаются)", scalar[int64](t, "SELECT total_spent FROM club_memberships WHERE club_id = $1 AND user_id = $2", w.club, g), 0)
		// Не хватает ни основного, ни бонусов на всю цену — отказ.
		_, err := w.book(g, pcs, at(15, 15, 0), 60)
		wantError(t, err, "balance", reservations.MsgShort)
		// Тариф «только основной баланс»: бонусы не помогают.
		only := w.tariff(tariffSpec{name: "Только основной", minutes: 60, price: 600 * tenge, method: "main_balance_only", zones: []int64{zone}})
		g2 := w.guest(100*tenge, 5000*tenge)
		_, err = w.book(g2, pcs[:1], at(15, 15, 0), 0, withTariff(only))
		wantError(t, err, "balance", reservations.MsgShort)
		w.wantBalances(g2, 100*tenge, 5000*tenge)

		// Смешанная оплата, бонусами до 30 % ОБЩЕЙ суммы: 2 ПК × 600 = 1200, лимит 360.
		w2 := newWorld(t, at(15, 10, 0), `{"club":{"booking_interval":10,"app_booking_enabled":true},"finances":{"allow_mixed_payment":true,"bonus_max_percent":30}}`, "UTC")
		_, pcs2 := w2.vip(2)
		g3 := w2.guest(900*tenge, 1000*tenge)
		out = w2.mustBook(g3, pcs2, at(15, 12, 0), 60)
		eq(t, "бонусная часть = min(лимит 360, цена − основной 300)", out.Bonus, 300*tenge)
		g4 := w2.guest(800*tenge, 1000*tenge)
		_, err = w2.book(g4, pcs2, at(15, 15, 0), 60) // нужно 400 бонусами, лимит 360
		wantError(t, err, "balance", reservations.MsgShort)
		w.noDrift()
		w2.noDrift()
	})

	t.Run("GAP-043 возврат гостю без строки баланса: строка создаётся, деньги зачисляются (UQ-9)", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		g, admin := w.guest(600*tenge, 0), w.staff()
		out := w.mustBook(g, pcs, at(15, 12, 0), 60)
		w.wantBalances(g, 0, 0)
		exec(t, "DELETE FROM member_balances WHERE club_id = $1 AND user_id = $2", w.club, g)
		if err := w.staffCancel(admin, out.ReservationIDs[0]); err != nil {
			t.Fatal(err)
		}
		w.wantBalances(g, 600*tenge, 0)
	})

	t.Run("GAP-044 на ПК уже идёт своя сессия гостя — время брони добавляется; запуск за секунду до конца", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(2)
		g, admin := w.guest(3000*tenge, 0), w.staff()
		out := w.mustBook(g, pcs[:1], at(15, 11, 0), 60)
		// В 10:25 гость покупает 30 минут на этом же ПК: сессия до 10:55, бронь не закрыта.
		w.now = at(15, 10, 25)
		buy, err := w.svc.PurchaseMinutes(context.Background(), service.MinutesPurchaseRequest{
			ClubID: w.club, UserID: g, ComputerID: pcs[0], Minutes: 30, IdempotencyKey: fmt.Sprintf("m-%d", seq()),
			Actor: service.Actor{UserID: g, Kind: "guest"},
		})
		if err != nil {
			t.Fatal(err)
		}
		// В 10:49 до брони больше интервала (10 мин) — рано; в 10:52 сессия ещё идёт: время брони добавляется к ней.
		w.now = at(15, 10, 49)
		if _, ok := w.signIn(pcs[0], g); ok {
			t.Fatal("в 10:49 рано")
		}
		w.now = at(15, 10, 52)
		s, ok := w.signIn(pcs[0], g)
		if !ok || !s.Extends || s.SessionID != buy.SessionID {
			t.Fatalf("время брони должно добавиться к сессии гостя: %+v", s)
		}
		eq(t, "конец сессии", s.EndsAt.UTC(), at(15, 11, 55))
		eq(t, "частей сессии", len(w.parts(s.SessionID)), 2)
		if w.reservation(out.ReservationIDs[0]).ArrivedAt == nil {
			t.Fatal("arrived_at не задан")
		}
		// Чужая сессия на ПК: «Клиент пришёл» по оплаченной брони — ошибка, бронь остаётся.
		other := w.guest(1000*tenge, 0)
		w.now = at(15, 12, 0)
		out2 := w.mustBook(g, pcs[1:], at(15, 14, 0), 60)
		if _, err := w.svc.PurchaseMinutes(context.Background(), service.MinutesPurchaseRequest{
			ClubID: w.club, UserID: other, ComputerID: pcs[1], Minutes: 60, IdempotencyKey: fmt.Sprintf("m-%d", seq()),
			Actor: service.Actor{UserID: other, Kind: "guest"},
		}); err != nil {
			t.Fatal(err)
		}
		wantError(t, w.staffArrive(admin, out2.ReservationIDs[0]), "reservation", reservations.MsgBusyOther)
		if w.reservation(out2.ReservationIDs[0]).ArrivedAt != nil {
			t.Fatal("бронь закрыта, хотя время не началось")
		}
		// UQ-16: сотрудник отмечает приход задолго до начала (ПК свободен) — время начинается сразу, целиком.
		w.now = at(15, 13, 1)
		if err := w.staffArrive(admin, out2.ReservationIDs[0]); err != nil {
			t.Fatal(err)
		}
		pc := w.pc(pcs[1])
		eq(t, "конец сессии", pc.EndsAt.UTC(), at(15, 14, 1))
		// За секунду до конца брони — одна минута и 1/60 денег.
		w3 := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs3 := w3.vip(1)
		g3 := w3.guest(1000*tenge, 0)
		w3.mustBook(g3, pcs3, at(15, 12, 0), 60)
		w3.now = at(15, 13, 0).Add(-time.Second)
		s3, ok := w3.signIn(pcs3[0], g3)
		if !ok {
			t.Fatal("за секунду до конца брони время должно начаться")
		}
		parts := w3.parts(s3.SessionID)
		if len(parts) != 1 || parts[0].Minutes != 1 || parts[0].Main != 1000 {
			t.Fatalf("часть сессии = %+v, ожидалась 1 минута и 10 ₸", parts)
		}
		w3.now = at(15, 13, 0)
		if _, ok := w3.signIn(pcs3[0], g3); ok {
			t.Fatal("бронь закончилась — запускать нечего")
		}
		w.noDrift()
	})

	t.Run("GAP-045 досрочный конец сессии, начатой бронью с опозданием: возврат от уменьшенной суммы", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		g := w.guest(1000*tenge, 0)
		w.mustBook(g, pcs, at(15, 12, 0), 60)
		w.now = at(15, 12, 30) // опоздал: 30 минут, в части сессии 300 ₸
		s, ok := w.signIn(pcs[0], g)
		if !ok {
			t.Fatal("время брони не началось")
		}
		w.now = at(15, 12, 45) // ушёл через 15 минут: осталось 15 из 30 — возврат 150 ₸ (BR-032)
		res, err := w.svc.EndSession(context.Background(), service.EndSessionRequest{
			ClubID: w.club, ComputerID: pcs[0], SessionID: s.SessionID, Actor: service.Actor{UserID: g, Kind: "guest"},
		})
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "возврат", res.RefundMain, 150*tenge)
		w.wantBalances(g, 550*tenge, 0)
		w.noDrift()
	})

	t.Run("GAP-049 лимит количества тарифа: бронь на 2 ПК при остатке 1; отмена освобождает; бронь + покупка в агенте", func(t *testing.T) {
		w := newWorld(t, at(16, 10, 0), bookingSettings, "UTC")
		zone, pcs := w.vip(3)
		g := w.guest(10000*tenge, 0)
		tariff := w.tariff(tariffSpec{name: "Пакет", minutes: 60, price: 500 * tenge, zones: []int64{zone}, quantityLimit: p64(2)})
		// Покупка в агенте занимает одну из двух.
		if _, err := w.svc.PurchaseTariff(context.Background(), service.TariffPurchaseRequest{
			ClubID: w.club, UserID: g, TariffID: tariff, ComputerID: pcs[2], IdempotencyKey: fmt.Sprintf("t-%d", seq()),
			Actor: service.Actor{UserID: g, Kind: "guest"},
		}); err != nil {
			t.Fatal(err)
		}
		_, err := w.book(g, pcs[:2], at(16, 14, 0), 0, withTariff(tariff))
		e := wantError(t, err, "tariff_id", reservations.MsgTariffLeft)
		eq(t, "осталось", e.Params["count"], "1")
		out := w.mustBook(g, pcs[:1], at(16, 14, 0), 0, withTariff(tariff))
		// Лимит исчерпан и для покупки в агенте (брони считаются, BR-190).
		w.now = at(16, 11, 30)
		_, err = w.svc.PurchaseTariff(context.Background(), service.TariffPurchaseRequest{
			ClubID: w.club, UserID: g, TariffID: tariff, ComputerID: pcs[1], IdempotencyKey: fmt.Sprintf("t-%d", seq()),
			Actor: service.Actor{UserID: g, Kind: "guest"},
		})
		if !errors.Is(err, pricing.ErrTariffUnavailable) {
			t.Fatalf("покупка в агенте при исчерпанном лимите: %v", err)
		}
		// Отменённая бронь лимит освобождает.
		if err := w.guestCancel(g, out.ReservationIDs[0]); err != nil {
			t.Fatal(err)
		}
		w.mustBook(g, pcs[:1], at(16, 16, 0), 0, withTariff(tariff))
		w.noDrift()
	})

	t.Run("GAP-050 сотрудник отменяет один ПК группы — возврат только его части; гость отменяет остальное", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), mixedSettings, "UTC")
		vip, common := w.zone("VIP", p64(60000)), w.zone("Общий", p64(30000))
		pc1, pc2 := w.computer(vip, 1), w.computer(common, 2)
		g, admin := w.guest(450*tenge, 1000*tenge), w.staff()
		out := w.mustBook(g, []int64{pc1, pc2}, at(15, 12, 0), 60) // ПК-01: 300 + 300, ПК-02: 150 + 150
		w.wantBalances(g, 0, 550*tenge)
		if err := w.staffCancel(admin, out.ReservationIDs[1]); err != nil {
			t.Fatal(err)
		}
		w.wantBalances(g, 150*tenge, 700*tenge)
		b := w.group(g, out.ReservationIDs[0])
		eq(t, "состояние группы", b.State, "pending")
		eq(t, "can_cancel (один ПК уже отменён)", b.CanCancel, false)
		// Гость отменяет «остальное»: в группе есть отменённая бронь — отказ (как в старом), деньги на месте.
		wantError(t, w.guestCancel(g, out.ReservationIDs[0]), "reservation", reservations.MsgStartedOrClosed)
		w.wantBalances(g, 150*tenge, 700*tenge)
		// Остаток отменяет сотрудник.
		if err := w.staffCancel(admin, out.ReservationIDs[0]); err != nil {
			t.Fatal(err)
		}
		w.wantBalances(g, 450*tenge, 1000*tenge)
		w.noDrift()
	})

	t.Run("GAP-051 идущая сессия, которая кончится до начала − интервал; 11 ПК; повторы ПК", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(11)
		g, other := w.guest(20000*tenge, 0), w.guest(1000*tenge, 0)
		// Сессия другого гостя до 11:00.
		if _, err := w.svc.PurchaseMinutes(context.Background(), service.MinutesPurchaseRequest{
			ClubID: w.club, UserID: other, ComputerID: pcs[0], Minutes: 60, IdempotencyKey: fmt.Sprintf("m-%d", seq()),
			Actor: service.Actor{UserID: other, Kind: "guest"},
		}); err != nil {
			t.Fatal(err)
		}
		_, err := w.book(g, pcs[:1], at(15, 11, 5), 60) // сессия до 11:00 заходит в интервал (10:55)
		wantError(t, err, "starts_at", reservations.MsgSession)
		w.mustBook(g, pcs[:1], at(15, 11, 10), 60) // 11:00 не позже 11:00 — можно
		_, err = w.book(g, pcs, at(15, 14, 0), 60)
		wantError(t, err, "club_computer_ids", reservations.MsgPickComputers)
		_, err = w.book(g, nil, at(15, 14, 0), 60)
		wantError(t, err, "club_computer_ids", reservations.MsgPickComputers)
		// Повтор ПК в запросе — одна бронь на ПК.
		out := w.mustBook(g, []int64{pcs[1], pcs[1], pcs[2]}, at(15, 14, 0), 60)
		eq(t, "броней в группе", len(out.ReservationIDs), 2)
		// Чужой ПК (другого клуба) — «Этот ПК нельзя забронировать.».
		w2 := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, foreign := w2.vip(1)
		_, err = w.book(g, foreign, at(15, 16, 0), 60)
		wantError(t, err, "club_computer_ids", reservations.MsgCannotBookPC)
		// Пересечение с бронью кассы (± интервал) — ошибка starts_at.
		admin := w.staff()
		if err := w.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := reservations.Create(ctx, q, w.info(ctx, q), admin, reservations.Booking{
				ComputerID: pcs[5], UserID: other, CustomerName: "Клиент", StartsAt: at(15, 18, 0), EndsAt: at(15, 19, 0),
			}, w.now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := w.guestCancel(g, out.ReservationIDs[0]); err != nil { // освобождаем предел «2 брони впереди»
			t.Fatal(err)
		}
		before, _ := fixDB.Balances(t, w.club, g)
		_, err = w.book(g, pcs[5:6], at(15, 19, 5), 60)
		wantError(t, err, "starts_at", reservations.MsgConflict)
		after, _ := fixDB.Balances(t, w.club, g)
		eq(t, "баланс после отказа", after, before)
		w.noDrift()
	})
}

// ---------------------------------------------------------------------------
// Гонки и идемпотентность
// ---------------------------------------------------------------------------

func TestAppBooking_Concurrency(t *testing.T) {
	t.Run("два гостя бронируют один ПК на одно время — бронь одна, списание одно", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		const n = 8
		guests := make([]int64, n)
		for i := range guests {
			guests[i] = w.guest(1000*tenge, 0)
		}
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range guests {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Начала сдвинуты в пределах интервала: все пересекаются.
				_, errs[i] = w.book(guests[i], pcs, at(15, 12, i), 60)
			}()
		}
		wg.Wait()
		okCount := 0
		for i, err := range errs {
			if err == nil {
				okCount++
				w.wantBalances(guests[i], 400*tenge, 0)
				continue
			}
			wantError(t, err, "starts_at", reservations.MsgConflict)
			w.wantBalances(guests[i], 1000*tenge, 0)
		}
		eq(t, "успешных броней", okCount, 1)
		eq(t, "броней в базе", w.reservationsCount(), 1)
		eq(t, "операций оплаты", scalar[int64](t, "SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'reservation_payment'", w.club), 1)
		w.noDrift()
	})

	t.Run("двойной тап: тот же Idempotency-Key — одна бронь, одно списание; без ключа — вторая отклонена пересечением", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		g := w.guest(2000*tenge, 0)
		const n = 6
		outs := make([]reservations.BookResult, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				outs[i], errs[i] = w.book(g, pcs, at(15, 12, 0), 60, withKey("tap-1"))
			}()
		}
		wg.Wait()
		for i := range n {
			if errs[i] != nil {
				t.Fatalf("повтор %d: %v", i, errs[i])
			}
			eq(t, "id брони повтора", outs[i].ReservationIDs[0], outs[0].ReservationIDs[0])
			eq(t, "код повтора", outs[i].Code, outs[0].Code)
		}
		eq(t, "броней", w.reservationsCount(), 1)
		w.wantBalances(g, 1400*tenge, 0)
		// Тот же ключ с другим телом — конфликт, а не новая бронь.
		_, err := w.book(g, pcs, at(15, 15, 0), 60, withKey("tap-1"))
		if !errors.Is(err, service.ErrIdempotencyConflict) {
			t.Fatalf("тот же ключ, другое тело: %v", err)
		}
		// Без ключа повтор запроса упирается в первую бронь.
		_, err = w.book(g, pcs, at(15, 12, 0), 60)
		wantError(t, err, "starts_at", reservations.MsgConflict)
		w.wantBalances(g, 1400*tenge, 0)
		w.noDrift()
	})

	t.Run("один гость с двух устройств: предел «2 брони впереди» не обходится", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(6)
		g := w.guest(10000*tenge, 0)
		errs := make([]error, len(pcs))
		var wg sync.WaitGroup
		for i := range pcs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = w.book(g, pcs[i:i+1], at(15, 12, 0), 60)
			}()
		}
		wg.Wait()
		okCount := 0
		for _, err := range errs {
			if err == nil {
				okCount++
				continue
			}
			wantError(t, err, "starts_at", reservations.MsgAheadLimit)
		}
		eq(t, "успешных броней", okCount, reservations.MaxPending)
		w.wantBalances(g, 10000*tenge-2*600*tenge, 0)
		w.noDrift()
	})

	t.Run("отмена и запуск времени одновременно: либо возврат, либо сессия", func(t *testing.T) {
		for i := range 6 {
			w := newWorld(t, at(15, 11, 55), bookingSettings, "UTC")
			_, pcs := w.vip(1)
			g, admin := w.guest(1000*tenge, 0), w.staff()
			out := w.mustBook(g, pcs, at(15, 12, 0), 60)
			var wg sync.WaitGroup
			var cancelErr error
			var started bool
			wg.Add(2)
			go func() { defer wg.Done(); cancelErr = w.staffCancel(admin, out.ReservationIDs[0]) }()
			go func() { defer wg.Done(); _, started = w.signIn(pcs[0], g) }()
			wg.Wait()
			r := w.reservation(out.ReservationIDs[0])
			main, _ := fixDB.Balances(t, w.club, g)
			switch {
			case started && cancelErr != nil && r.ArrivedAt != nil && r.CancelledAt == nil && main == 40000:
			case !started && cancelErr == nil && r.CancelledAt != nil && r.ArrivedAt == nil && main == 100000:
			default:
				t.Fatalf("прогон %d: запуск %v, отмена %v, бронь %+v, баланс %d", i, started, cancelErr, r, main)
			}
			w.noDrift()
		}
	})

	t.Run("GAP-058 коды броней клуба не повторяются; код по ПК вводят одновременно — сессия одна", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(12)
		const n = 12
		codes := make([]string, n)
		var wg sync.WaitGroup
		for i := range n {
			g := w.guest(1000*tenge, 0)
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, err := w.book(g, pcs[i:i+1], at(15, 12, 0), 60)
				if err != nil {
					t.Errorf("бронь %d: %v", i, err)
					return
				}
				codes[i] = out.Code
			}()
		}
		wg.Wait()
		seen := map[string]bool{}
		for _, c := range codes {
			if seen[c] {
				t.Fatalf("код %s выдан дважды", c)
			}
			seen[c] = true
		}
		// Занятый код не выдаётся повторно: пока бронь не старше суток.
		if taken := scalar[bool](t, `SELECT EXISTS (SELECT 1 FROM computer_reservations WHERE club_id = $1 AND code = $2)`, w.club, codes[0]); !taken {
			t.Fatal("код не записан")
		}
		// Один код одновременно вводят на одном ПК: сессия открывается один раз.
		w.now = at(15, 11, 55)
		errs := make([]error, 5)
		for i := range errs {
			wg.Add(1)
			go func() { defer wg.Done(); _, errs[i] = w.byCode(pcs[0], codes[0]) }()
		}
		wg.Wait()
		okCount := 0
		for _, err := range errs {
			if err == nil {
				okCount++
			}
		}
		eq(t, "открытий по коду", okCount, 1)
	})

	t.Run("ограничение базы: две ожидающие брони одного ПК на одно время не вставить в обход блокировки", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		ins := `INSERT INTO computer_reservations (club_id, club_computer_id, starts_at, ends_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, now(), now())`
		exec(t, ins, w.club, pcs[0], at(15, 12, 0), at(15, 13, 0))
		_, err := fixDB.Owner.Exec(context.Background(), ins, w.club, pcs[0], at(15, 12, 30), at(15, 13, 30))
		if err == nil || !strings.Contains(err.Error(), "computer_reservations_pending_no_overlap") {
			t.Fatalf("пересекающаяся бронь вставлена: %v", err)
		}
		// Вплотную (конец = начало) и после отмены первой — можно.
		exec(t, ins, w.club, pcs[0], at(15, 13, 0), at(15, 14, 0))
		exec(t, "UPDATE computer_reservations SET cancelled_at = now() WHERE club_id = $1 AND starts_at = $2", w.club, at(15, 12, 0)) //nolint:misspell // колонка старой схемы
		exec(t, ins, w.club, pcs[0], at(15, 12, 30), at(15, 13, 0))
	})
}

// TestAppBooking_RandomSplitThroughLedger — свойство «сумма частей по ПК = общей оплате» на случайных входах,
// через настоящую бронь: цены зон, бонусы, смешанная оплата; затем отмена возвращает всё до тиына.
func TestAppBooking_RandomSplitThroughLedger(t *testing.T) {
	r := rand.New(rand.NewPCG(20261115, 7))
	for i := range 40 {
		percent := []int{100, 100, 70, 35}[r.IntN(4)]
		w := newWorld(t, at(15, 10, 0), fmt.Sprintf(`{"club":{"booking_interval":10,"app_booking_enabled":true},"finances":{"allow_mixed_payment":true,"bonus_max_percent":%d}}`, percent), "UTC")
		n := 1 + r.IntN(6)
		var pcs []int64
		for j := range n {
			zone := w.zone(fmt.Sprintf("Зона %d", j), p64(100+r.Int64N(300000)))
			pcs = append(pcs, w.computer(zone, int64(j+1)))
		}
		minutes := int64(30 + r.IntN(691))
		// Баланса с запасом; основной — случайный, чтобы бонусная часть была разной (в т. ч. почти вся цена).
		main := money.Tiyn(r.Int64N(4000000))
		if r.IntN(3) == 0 {
			main = money.Tiyn(r.Int64N(5))
		}
		bonus := money.Tiyn(100000000)
		g := w.guest(main, bonus)
		out, err := w.book(g, pcs, at(15, 12, 0), minutes)
		if err != nil {
			var e *reservations.Error
			if errors.As(err, &e) && e.Field == "balance" {
				w.wantBalances(g, main, bonus)
				continue // лимит бонусов не покрыл нехватку основного — бронь не создана, деньги на месте
			}
			t.Fatalf("прогон %d: %v", i, err)
		}
		var sumMain, sumBonus int64
		for _, id := range out.ReservationIDs {
			row := w.reservation(id)
			if row.Main < 0 || row.Bonus < 0 {
				t.Fatalf("прогон %d: отрицательная часть %+v", i, row)
			}
			sumMain += row.Main
			sumBonus += row.Bonus
		}
		if sumMain != int64(out.Main) || sumBonus != int64(out.Bonus) || sumMain+sumBonus != int64(out.Price) {
			t.Fatalf("прогон %d: части по ПК %d + %d, оплата %d + %d = %d", i, sumMain, sumBonus, out.Main, out.Bonus, out.Price)
		}
		w.wantBalances(g, main-out.Main, bonus-out.Bonus)
		if err := w.guestCancel(g, out.ReservationIDs[0]); err != nil {
			t.Fatalf("прогон %d: отмена: %v", i, err)
		}
		w.wantBalances(g, main, bonus)
		w.noDrift()
	}
}
