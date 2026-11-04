//go:build integration

package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var fixDB *dbtest.DB

// clock — внедряемые часы сервиса (opened_at, created_at операций): тесты не зависят от текущего времени.
var clock = time.Date(2026, 10, 30, 15, 4, 5, 0, time.UTC)

var svc = &service.Service{Now: func() time.Time { return clock }}

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

func seq(prefix string) string {
	seqMu.Lock()
	defer seqMu.Unlock()
	seqN++
	return fmt.Sprintf("%s-%d", prefix, seqN)
}

// env — клуб теста и обёртки операций кассы (каждая — своя транзакция клуба, как HTTP-запрос).
type env struct {
	t        *testing.T
	club     int64
	category int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	slug := strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	return &env{t: t, club: fixDB.Club(t, seq(slug))}
}

func (e *env) tx(fn func(ctx context.Context, q *store.Queries) error) error {
	ctx := context.Background()
	return db.InTenantTx(ctx, fixDB.App, e.club, func(q *store.Queries) error { return fn(ctx, q) })
}

func (e *env) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func actor(id int64) service.Actor { return service.Actor{UserID: id, Kind: "staff"} }

// user — сотрудник или клиент клуба (членство + нулевой баланс).
func (e *env) user(name string) int64 { return fixDB.Customer(e.t, e.club, name) }

// customer — клиент с начальными балансами (только через ledger: billing.SetInitialBalance).
func (e *env) customer(name string, main, bonus money.Tiyn) int64 {
	e.t.Helper()
	id := e.user(name)
	e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
		_, err := billing.SetInitialBalance(ctx, q, billing.InitialBalanceParams{ClubID: e.club, UserID: id, Main: main, Bonus: bonus})
		return err
	}))
	return id
}

func (e *env) settings(js string) {
	e.t.Helper()
	if _, err := fixDB.Owner.Exec(context.Background(), "UPDATE clubs SET settings = $2::jsonb WHERE id = $1", e.club, js); err != nil {
		e.t.Fatal(err)
	}
}

// topUpBonus — finances.topup_bonus_* клуба: уровни {сумма ₸ → %}.
func (e *env) topUpBonus(enabled bool, levels ...[2]float64) {
	var parts []string
	for _, l := range levels {
		parts = append(parts, fmt.Sprintf(`{"amount": %v, "percent": %v}`, l[0], l[1]))
	}
	e.settings(fmt.Sprintf(`{"finances": {"topup_bonus_enabled": %v, "topup_bonus_levels": [%s]}}`, enabled, strings.Join(parts, ",")))
}

// product — товар через stock.CreateProduct (начальный остаток — движение receipt).
func (e *env) product(name string, price money.Tiyn, qty int64) int64 {
	e.t.Helper()
	if e.category == 0 {
		if err := fixDB.Owner.QueryRow(context.Background(),
			"INSERT INTO product_categories (club_id, name, created_at, updated_at) VALUES ($1, 'Напитки', now(), now()) RETURNING id",
			e.club).Scan(&e.category); err != nil {
			e.t.Fatal(err)
		}
	}
	var id int64
	e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
		res, err := stock.CreateProduct(ctx, q, stock.CreateProductParams{
			ClubID: e.club, SKU: seq("SKU"), Name: name, CategoryID: e.category, Price: price, Quantity: qty, Now: clock,
		})
		id = res.Product.ID
		return err
	}))
	return id
}

func (e *env) service(name string, price money.Tiyn, active bool) int64 {
	e.t.Helper()
	var id int64
	if err := fixDB.Owner.QueryRow(context.Background(),
		"INSERT INTO club_services (club_id, name, price, is_active, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now()) RETURNING id",
		e.club, name, int64(price), active).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) open(by int64, opening string) store.CashShift {
	e.t.Helper()
	sh, err := e.tryOpen(by, opening)
	e.must(err)
	return sh
}

func (e *env) tryOpen(by int64, opening string) (store.CashShift, error) {
	e.t.Helper()
	amount, err := cash.ParseOpeningBalance(opening)
	e.must(err)
	var res service.OpenShiftResult
	err = e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = svc.OpenShift(ctx, q, service.OpenShiftParams{ClubID: e.club, OpeningBalance: amount, Actor: actor(by)})
		return err
	})
	return res.Shift, err
}

func (e *env) close(by, shiftID int64, requireZ bool) (service.CloseShiftResult, error) {
	var res service.CloseShiftResult
	err := e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = svc.CloseShift(ctx, q, service.CloseShiftParams{ClubID: e.club, ShiftID: shiftID, Actor: actor(by), RequireZReport: requireZ})
		return err
	})
	return res, err
}

func (e *env) saleKey(by int64, key string, items ...cash.SaleItem) (service.SaleResult, error) {
	var res service.SaleResult
	err := e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = svc.Sale(ctx, q, service.SaleParams{ClubID: e.club, Items: items, IdempotencyKey: key, Actor: actor(by)})
		return err
	})
	return res, err
}

func (e *env) sale(by int64, items ...cash.SaleItem) (service.SaleResult, error) {
	return e.saleKey(by, seq("sale"), items...)
}

func (e *env) mustSale(by int64, items ...cash.SaleItem) service.SaleResult {
	e.t.Helper()
	res, err := e.sale(by, items...)
	e.must(err)
	return res
}

func prod(id, qty int64) cash.SaleItem { return cash.SaleItem{ProductID: id, Quantity: qty} }
func serv(id, qty int64) cash.SaleItem { return cash.SaleItem{ServiceID: id, Quantity: qty} }

func (e *env) topUpKey(by, user int64, field cash.BalanceField, amount string, method cash.PaymentMethod, key string) (service.TopUpResult, error) {
	e.t.Helper()
	a, err := cash.ParseTopUpAmount(amount)
	e.must(err)
	var res service.TopUpResult
	err = e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = svc.TopUp(ctx, q, service.TopUpParams{
			ClubID: e.club, UserID: user, Field: field, Amount: a, PaymentMethod: method, IdempotencyKey: key, Actor: actor(by),
		})
		return err
	})
	return res, err
}

func (e *env) topUp(by, user int64, field cash.BalanceField, amount string, method cash.PaymentMethod) (service.TopUpResult, error) {
	e.t.Helper()
	return e.topUpKey(by, user, field, amount, method, seq("topup"))
}

func (e *env) mustTopUp(by, user int64, field cash.BalanceField, amount string, method cash.PaymentMethod) service.TopUpResult {
	e.t.Helper()
	res, err := e.topUp(by, user, field, amount, method)
	e.must(err)
	return res
}

func (e *env) transactKey(by int64, typ cash.Type, amount, description, key string) (service.TransactResult, error) {
	e.t.Helper()
	a, err := cash.ParseTransactionAmount(amount)
	e.must(err)
	var res service.TransactResult
	err = e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = svc.Transact(ctx, q, service.TransactParams{
			ClubID: e.club, Type: typ, Amount: a, Description: description, IdempotencyKey: key, Actor: actor(by),
		})
		return err
	})
	return res, err
}

func (e *env) transact(by int64, typ cash.Type, amount, description string) (service.TransactResult, error) {
	e.t.Helper()
	return e.transactKey(by, typ, amount, description, seq("tr"))
}

func (e *env) mustTransact(by int64, typ cash.Type, amount, description string) service.TransactResult {
	e.t.Helper()
	res, err := e.transact(by, typ, amount, description)
	e.must(err)
	return res
}

func (e *env) refund(by, incomeID int64) (service.RefundResult, error) {
	var res service.RefundResult
	err := e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = svc.Refund(ctx, q, service.RefundParams{ClubID: e.club, IncomeID: incomeID, Actor: actor(by)})
		return err
	})
	return res, err
}

func (e *env) guest(by int64, amount money.Tiyn, description string, method cash.PaymentMethod, key string) (service.GuestSessionRecorded, error) {
	var res service.GuestSessionRecorded
	err := e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = svc.RecordGuestSession(ctx, q, e.club, by, amount, description, method, key)
		return err
	})
	return res, err
}

func (e *env) report(typ cash.ReportType, shiftID int64, operator string) service.ReportResult {
	e.t.Helper()
	var res service.ReportResult
	e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = svc.Report(ctx, q, service.ReportParams{ClubID: e.club, Type: typ, ShiftID: shiftID, OperatorName: operator})
		return err
	}))
	return res
}

func (e *env) totals(shiftID int64) cash.Totals {
	e.t.Helper()
	var t cash.Totals
	e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		t, err = svc.ShiftTotals(ctx, q, e.club, shiftID)
		return err
	}))
	return t
}

func (e *env) stockOf(productID int64) int64 {
	e.t.Helper()
	var q int64
	if err := fixDB.Owner.QueryRow(context.Background(), "SELECT quantity FROM products WHERE id = $1", productID).Scan(&q); err != nil {
		e.t.Fatal(err)
	}
	return q
}

func (e *env) count(sql string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := fixDB.Owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) balances(user int64) (money.Tiyn, money.Tiyn) {
	e.t.Helper()
	m, b := fixDB.Balances(e.t, e.club, user)
	return money.Tiyn(m), money.Tiyn(b)
}

func (e *env) wantBalances(user int64, main, bonus money.Tiyn) {
	e.t.Helper()
	m, b := e.balances(user)
	if m != main || b != bonus {
		e.t.Fatalf("balances = %s / %s, want %s / %s", m, b, main, bonus)
	}
}

// account — остаток счёта клуба по журналу: SUM(amount) (> 0 кредит); касса — e.drawer().
func (e *env) account(kind ledger.AccountKind) money.Tiyn {
	e.t.Helper()
	return money.Tiyn(e.count(`SELECT COALESCE(sum(e.amount), 0)::bigint FROM ledger_entries e
		JOIN ledger_accounts a ON a.club_id = e.club_id AND a.id = e.account_id
		WHERE a.club_id = $1 AND a.kind = $2 AND a.user_id IS NULL`, e.club, string(kind)))
}

// drawer — остаток кассы по журналу (актив: −SUM).
func (e *env) drawer() money.Tiyn {
	e.t.Helper()
	var b int64
	e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		b, err = q.CashDrawerLedgerBalance(ctx, e.club)
		return err
	}))
	return money.Tiyn(b)
}

// checkLedger — инварианты после сценария: проекция балансов совпадает с журналом; остаток кассы по журналу равен
// остатку открытой (или последней) смены.
func (e *env) checkLedger(shiftBalance money.Tiyn) {
	e.t.Helper()
	var drift []store.ListMemberBalanceDriftRow
	e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		drift, err = q.ListMemberBalanceDrift(ctx, e.club)
		return err
	}))
	if len(drift) > 0 {
		e.t.Fatalf("member balance drift: %+v", drift)
	}
	if got := e.drawer(); got != shiftBalance {
		e.t.Fatalf("cash drawer by ledger = %s, want %s", got, shiftBalance)
	}
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func wantCashErr(t *testing.T, err error, field, message string) {
	t.Helper()
	e, ok := cash.AsError(err)
	if !ok || e.Field != field || e.Message != message {
		t.Fatalf("err = %v, want %s: %s", err, field, message)
	}
}

func wantRule(t *testing.T, err error, field, rule string) {
	t.Helper()
	e, ok := cash.AsError(err)
	if !ok || e.Field != field || e.Rule != rule {
		t.Fatalf("err = %v, want %s: %s", err, field, rule)
	}
}

func wantIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

func lines(r service.ReportResult) []string {
	out := make([]string, len(r.Lines))
	for i, l := range r.Lines {
		out[i] = l.String()
	}
	return out
}

func hasLine(t *testing.T, r service.ReportResult, want ...string) {
	t.Helper()
	got := lines(r)
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("report has no line %q:\n%s", w, strings.Join(got, "\n"))
		}
	}
}

func noLine(t *testing.T, r service.ReportResult, prefix string) {
	t.Helper()
	for _, g := range lines(r) {
		if strings.HasPrefix(g, prefix) {
			t.Fatalf("report has line %q", g)
		}
	}
}

// allOps — все операции смены (для сверки отчёта из SQL с cash.BuildReport по полному списку).
func (e *env) allOps(shiftID int64) []cash.Operation {
	e.t.Helper()
	var ops []cash.Operation
	e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
		rows, err := q.ListCashShiftTransactions(ctx, store.ListCashShiftTransactionsParams{ClubID: e.club, CashShiftID: shiftID, AfterID: 0, PageSize: 10000})
		if err != nil {
			return err
		}
		for _, r := range rows {
			op, err := service.OperationOf(r)
			if err != nil {
				return err
			}
			ops = append(ops, op)
		}
		return nil
	}))
	return ops
}
