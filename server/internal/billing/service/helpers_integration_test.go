//go:build integration

package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
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

func key(prefix string) string { return fmt.Sprintf("%s-%d", prefix, seq()) }

// clock — часы теста (Carbon::setTestNow / travel).
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *clock) travel(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeCash — кассовый слой для тестов: открытая смена в памяти, приход — операцией ledger guest_session.
type fakeCash struct {
	mu      sync.Mutex
	shiftID int64 // 0 — смены нет
	nextTx  int64
	records []service.GuestIncome
}

func (f *fakeCash) LockOpenShift(_ context.Context, _ *store.Queries, _ int64) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shiftID, f.shiftID != 0, nil
}

func (f *fakeCash) RecordGuestSessionIncome(ctx context.Context, q *store.Queries, in service.GuestIncome) (service.GuestIncomeResult, error) {
	f.mu.Lock()
	f.nextTx++
	txID := f.nextTx
	f.mu.Unlock()
	out := service.GuestIncomeResult{CashTransactionID: txID}
	if in.Amount > 0 {
		res, err := ledger.Post(ctx, q, ledger.Operation{
			ClubID: in.ClubID, Kind: ledger.KindGuestSession, IdempotencyKey: in.IdempotencyKey,
			Entries: []ledger.Entry{
				{Account: ledger.ClubAccount(ledger.AccountCashDrawer), Amount: -in.Amount},
				{Account: ledger.ClubAccount(ledger.AccountRevenueTime), Amount: in.Amount},
			},
			Refs: ledger.Refs{CashShiftID: in.ShiftID, CashTransactionID: txID},
		})
		if err != nil {
			return service.GuestIncomeResult{}, err
		}
		out.LedgerTransactionID = res.TransactionID
	}
	f.mu.Lock()
	f.records = append(f.records, in)
	f.mu.Unlock()
	return out, nil
}

// env — клуб теста со своими часами и сервисом (база общая на пакет).
type env struct {
	t     *testing.T
	club  int64
	clock *clock
	cash  *fakeCash
	svc   *service.Service
}

var t0 = time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

func newEnv(t *testing.T, settings string) *env {
	t.Helper()
	club := fixDB.Club(t, fmt.Sprintf("club-%d", seq()))
	if settings != "" {
		exec(t, "UPDATE clubs SET settings = $2::jsonb, timezone = 'UTC' WHERE id = $1", club, settings)
	} else {
		exec(t, "UPDATE clubs SET timezone = 'UTC' WHERE id = $1", club)
	}
	c := &clock{t: t0}
	cash := &fakeCash{}
	return &env{t: t, club: club, clock: c, cash: cash, svc: service.New(fixDB.App, c, cash)}
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

func ptr[T any](v T) *T { return &v }

func (e *env) zone(name string, pricePerHour *int64) int64 {
	e.t.Helper()
	return scalar[int64](e.t, "INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, $2, $3, now(), now()) RETURNING id",
		e.club, name, pricePerHour)
}

// computer — ПК в зоне, онлайн (last_seen_at = сейчас по часам теста).
func (e *env) computer(zone, number int64) int64 {
	e.t.Helper()
	return scalar[int64](e.t, `INSERT INTO club_computers (club_id, club_zone_id, number, last_seen_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), now()) RETURNING id`, e.club, zone, number, e.clock.Now())
}

type tariffSpec struct {
	name          string
	minutes       int64
	price         money.Tiyn
	method        string // main_or_bonus по умолчанию
	discount      bool
	usageLimit    *int64
	quantityLimit *int64
	zones         []int64
	userRoles     string // JSON
	sale          string // JSON
	validity      string // JSON
}

func (e *env) tariff(s tariffSpec) int64 {
	e.t.Helper()
	if s.method == "" {
		s.method = "main_or_bonus"
	}
	nullJSON := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	id := scalar[int64](e.t, `INSERT INTO tariffs (club_id, name, duration_minutes, price, payment_method, discount_enabled,
		usage_limit_enabled, usage_limit_minutes, quantity_limit_enabled, quantity_limit, user_roles, sale_periods, validity_periods,
		is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::bigint IS NOT NULL, $7::bigint, $8::bigint IS NOT NULL, $8::bigint, $9::jsonb, $10::jsonb, $11::jsonb, true, now(), now())
		RETURNING id`,
		e.club, s.name, s.minutes, int64(s.price), s.method, s.discount, s.usageLimit, s.quantityLimit,
		nullJSON(s.userRoles), nullJSON(s.sale), nullJSON(s.validity))
	for _, z := range s.zones {
		exec(e.t, "INSERT INTO club_zone_tariff (club_id, club_zone_id, tariff_id) VALUES ($1, $2, $3)", e.club, z, id)
	}
	return id
}

// customer — клиент с начальными балансами (только через billing.SetInitialBalance — ledger).
func (e *env) customer(name string, main, bonus money.Tiyn) int64 {
	e.t.Helper()
	id := fixDB.Customer(e.t, e.club, name)
	if err := db.InTenantTx(context.Background(), fixDB.App, e.club, func(q *store.Queries) error {
		_, err := billing.SetInitialBalance(context.Background(), q, billing.InitialBalanceParams{ClubID: e.club, UserID: id, Main: main, Bonus: bonus})
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) wantBalances(user int64, main, bonus money.Tiyn) {
	e.t.Helper()
	gotMain, gotBonus := fixDB.Balances(e.t, e.club, user)
	if money.Tiyn(gotMain) != main || money.Tiyn(gotBonus) != bonus {
		e.t.Fatalf("balances = %s / %s, want %s / %s", money.Tiyn(gotMain), money.Tiyn(gotBonus), main, bonus)
	}
}

func (e *env) totalSpent(user int64) money.Tiyn {
	e.t.Helper()
	return money.Tiyn(scalar[int64](e.t, "SELECT total_spent FROM club_memberships WHERE club_id = $1 AND user_id = $2", e.club, user))
}

func (e *env) wantTotalSpent(user int64, want money.Tiyn) {
	e.t.Helper()
	if got := e.totalSpent(user); got != want {
		e.t.Fatalf("total_spent = %s, want %s", got, want)
	}
}

type pcState struct {
	SessionID *string
	UserID    *int64
	Guest     bool
	EndsAt    *time.Time
}

func (e *env) pc(id int64) pcState {
	e.t.Helper()
	var s pcState
	if err := fixDB.Owner.QueryRow(context.Background(),
		"SELECT current_session_id, current_session_user_id, session_guest, session_ends_at FROM club_computers WHERE id = $1", id).
		Scan(&s.SessionID, &s.UserID, &s.Guest, &s.EndsAt); err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) count(table string) int64 {
	e.t.Helper()
	return scalar[int64](e.t, "SELECT count(*) FROM "+table+" WHERE club_id = $1", e.club)
}

type ledgerRow struct {
	Kind    string
	Account string
	UserID  int64
	Amount  int64
}

// ledgerOf — проводки операций клуба с этой сессией (по ledger_transactions.session_id), по виду и счёту.
func (e *env) ledgerOf(sessionID string) []ledgerRow {
	e.t.Helper()
	rows, err := fixDB.Owner.Query(context.Background(), `
		SELECT t.kind, a.kind, COALESCE(a.user_id, 0), e.amount
		FROM ledger_transactions t
		JOIN ledger_entries e ON e.club_id = t.club_id AND e.transaction_id = t.id
		JOIN ledger_accounts a ON a.id = e.account_id
		WHERE t.club_id = $1 AND t.session_id = $2
		ORDER BY t.id, a.kind`, e.club, sessionID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []ledgerRow
	for rows.Next() {
		var r ledgerRow
		if err := rows.Scan(&r.Kind, &r.Account, &r.UserID, &r.Amount); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// minutesOf — сумма минут ledger минут по пакету.
func (e *env) minutesOf(packageID int64) int64 {
	e.t.Helper()
	return scalar[int64](e.t, "SELECT COALESCE(sum(minutes), 0)::bigint FROM ledger_minute_entries WHERE club_id = $1 AND time_package_id = $2", e.club, packageID)
}

// noDrift — проекция member_balances совпадает с журналом.
func (e *env) noDrift() {
	e.t.Helper()
	if err := db.InTenantTx(context.Background(), fixDB.App, e.club, func(q *store.Queries) error {
		rows, err := q.ListMemberBalanceDrift(context.Background(), e.club)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			return fmt.Errorf("drift: %+v", rows)
		}
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) buyTariff(user, tariff, pc int64, choice pricing.BalanceChoice) (service.PurchaseResult, error) {
	return e.svc.PurchaseTariff(context.Background(), service.TariffPurchaseRequest{
		ClubID: e.club, UserID: user, TariffID: tariff, ComputerID: pc, Balance: choice, IdempotencyKey: key("tariff"),
		Actor: service.Actor{UserID: user, Kind: "guest"},
	})
}

func (e *env) buyMinutes(user, pc, minutes int64, choice pricing.BalanceChoice) (service.PurchaseResult, error) {
	return e.svc.PurchaseMinutes(context.Background(), service.MinutesPurchaseRequest{
		ClubID: e.club, UserID: user, ComputerID: pc, Minutes: minutes, Balance: choice, IdempotencyKey: key("minutes"),
		Actor: service.Actor{UserID: user, Kind: "guest"},
	})
}

func (e *env) end(pc int64, sessionID string, actor int64, staff bool) (service.EndSessionResult, error) {
	return e.svc.EndSession(context.Background(), service.EndSessionRequest{
		ClubID: e.club, ComputerID: pc, SessionID: sessionID, Actor: service.Actor{UserID: actor, Kind: "guest"}, IsStaff: staff,
	})
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// wantFieldError — ошибка проверки с полем и текстом (pricing / session / service).
func wantFieldError(t *testing.T, err error, field, message string) {
	t.Helper()
	var pe *pricing.Error
	var se *session.Error
	var ve *service.Error
	switch {
	case errors.As(err, &pe):
		if pe.Field == field && pe.Message == message {
			return
		}
	case errors.As(err, &se):
		if se.Field == field && se.Message == message {
			return
		}
	case errors.As(err, &ve):
		if ve.Field == field && ve.Message == message {
			return
		}
	}
	t.Fatalf("err = %v, want %s: %s", err, field, message)
}

const tenge = money.Tiyn(100)

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func asErr[T error](err error, target *T) bool { return errors.As(err, target) }

func isNotFound(err error) bool { return errors.Is(err, service.ErrNotFound) }
