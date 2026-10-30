//go:build integration

package service_test

import (
	"context"
	"sync"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Конкурентность: открытая смена FOR UPDATE сериализует операции кассы клуба, товар FOR UPDATE — остатки (BR-005).
func TestConcurrency(t *testing.T) {
	t.Run("last-unit-two-sales", func(t *testing.T) {
		e := newEnv(t)
		a, b := e.user("Кассир 1"), e.user("Кассир 2")
		e.open(a, "0")
		p := e.product("Последний", 50000, 1)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, who := range []int64{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = e.sale(who, prod(p, 1))
			}()
		}
		wg.Wait()
		ok, short := 0, 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else if ce, isCash := cash.AsError(err); isCash && ce.Message == cash.MsgNotEnoughStock {
				short++
			} else {
				t.Fatalf("unexpected error: %v", err)
			}
		}
		eq(t, "succeeded", ok, 1)
		eq(t, "not enough stock", short, 1)
		eq(t, "stock", e.stockOf(p), 0)
		eq(t, "sales", e.count("SELECT count(*) FROM cash_transactions WHERE club_id = $1", e.club), 1)
		e.checkLedger(50000)
	})
	t.Run("two-opens", func(t *testing.T) {
		e := newEnv(t)
		a := e.user("Кассир")
		errs := make([]error, 4)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = e.tryOpen(a, "100")
			}()
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
				continue
			}
			wantCashErr(t, err, "opening_balance", cash.MsgShiftAlreadyOpen)
		}
		eq(t, "opened", ok, 1)
		e.checkLedger(10000)
	})
	t.Run("same-key-in-parallel", func(t *testing.T) {
		e := newEnv(t)
		a := e.user("Кассир")
		e.open(a, "0")
		p := e.product("Cola", 50000, 10)
		res := make([]service.SaleResult, 3)
		errs := make([]error, 3)
		var wg sync.WaitGroup
		for i := range res {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res[i], errs[i] = e.saleKey(a, "pos-req-parallel", prod(p, 2))
			}()
		}
		wg.Wait()
		replays := 0
		for i := range res {
			e.must(errs[i])
			eq(t, "same sale", res[i].Transaction.ID, res[0].Transaction.ID)
			if res[i].Replayed {
				replays++
			}
		}
		eq(t, "replays", replays, 2)
		eq(t, "stock", e.stockOf(p), 8)
		eq(t, "pos_sale ops", e.count("SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'pos_sale'", e.club), 1)
		e.checkLedger(100000)
	})
}

// Повтор запроса кассы с тем же ключом идемпотентности.
func TestIdempotency(t *testing.T) {
	e := newEnv(t)
	e.topUpBonus(true, [2]float64{1000, 10})
	a := e.user("Кассир")
	c := e.customer("Клиент", 0, 0)
	e.open(a, "0")
	p := e.product("Cola", 50000, 10)

	t.Run("sale", func(t *testing.T) {
		first, err := e.saleKey(a, "sale-1", prod(p, 1))
		e.must(err)
		second, err := e.saleKey(a, "sale-1", prod(p, 1))
		e.must(err)
		eq(t, "replayed", second.Replayed, true)
		eq(t, "same", second.Transaction.ID, first.Transaction.ID)
		eq(t, "amount", second.Sale.Amount, first.Sale.Amount)
		eq(t, "stock", e.stockOf(p), 9)
		_, err = e.saleKey(a, "sale-1", prod(p, 2))
		wantIs(t, err, service.ErrIdempotencyConflict)
	})
	t.Run("top-up", func(t *testing.T) {
		first, err := e.topUpKey(a, c, cash.FieldMain, "2000", cash.PayQR, "topup-1")
		e.must(err)
		second, err := e.topUpKey(a, c, cash.FieldMain, "2000", cash.PayQR, "topup-1")
		e.must(err)
		eq(t, "replayed", second.Replayed, true)
		eq(t, "same", second.Transaction.ID, first.Transaction.ID)
		eq(t, "bonus", second.Bonus, 20000)
		eq(t, "ledger replay", second.Balance.Ledger.TransactionID, first.Balance.Ledger.TransactionID)
		e.wantBalances(c, 200000, 20000)
		_, err = e.topUpKey(a, c, cash.FieldMain, "2001", cash.PayQR, "topup-1")
		wantIs(t, err, service.ErrIdempotencyConflict)
		_, err = e.saleKey(a, "topup-1", prod(p, 1)) // ключ пополнения в продаже
		wantIs(t, err, service.ErrIdempotencyConflict)
	})
	t.Run("transact", func(t *testing.T) {
		first, err := e.transactKey(a, cash.Expense, "100", "Вода", "tr-1")
		e.must(err)
		second, err := e.transactKey(a, cash.Expense, "100", "Вода", "tr-1")
		e.must(err)
		eq(t, "replayed", second.Replayed, true)
		eq(t, "same", second.Transaction.ID, first.Transaction.ID)
		_, err = e.transactKey(a, cash.Income, "100", "Вода", "tr-1")
		wantIs(t, err, service.ErrIdempotencyConflict)
	})
	t.Run("guest", func(t *testing.T) {
		first, err := e.guest(a, 60000, "Гостевая сессия: 60 минут, ПК 1 (QR-код)", cash.PayQR, "guest-1")
		e.must(err)
		second, err := e.guest(a, 60000, "Гостевая сессия: 60 минут, ПК 1 (QR-код)", cash.PayQR, "guest-1")
		e.must(err)
		eq(t, "replayed", second.Replayed, true)
		eq(t, "same", second.Transaction.ID, first.Transaction.ID)
		eq(t, "kind", *first.Transaction.IncomeKind, "guest_session")
		eq(t, "ledger", first.Ledger.Kind, ledger.KindGuestSession)
		_, err = e.guest(a, 70000, "Гостевая сессия: 60 минут, ПК 1 (QR-код)", cash.PayQR, "guest-1")
		wantIs(t, err, service.ErrIdempotencyConflict)
	})
	t.Run("refund-twice", func(t *testing.T) {
		sale, err := e.saleKey(a, "sale-refund", prod(p, 1))
		e.must(err)
		_, err = e.refund(a, sale.Transaction.ID)
		e.must(err)
		_, err = e.refund(a, sale.Transaction.ID)
		wantCashErr(t, err, "refund", cash.MsgAlreadyRefunded)
	})
	sh, totals := currentShift(t, e)
	_ = sh
	e.checkLedger(totals.Balance())
}

func currentShift(t *testing.T, e *env) (*store.CashShift, cash.Totals) {
	t.Helper()
	var (
		sh     *store.CashShift
		totals cash.Totals
	)
	e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		sh, totals, err = svc.CurrentShift(ctx, q, e.club)
		return err
	}))
	return sh, totals
}

// Гостевая сессия без открытой смены и в чужой транзакции.
func TestRecordGuestSession(t *testing.T) {
	e := newEnv(t)
	a := e.user("Кассир")
	_, err := e.guest(a, 60000, "Гостевая сессия", cash.PayCash, "")
	wantCashErr(t, err, "payment_method", service.MsgGuestOpenShiftFirst)
	sh := e.open(a, "0")
	// Сервис гостевых сессий блокирует смену первым и пишет приход в той же транзакции.
	e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
		locked, err := service.LockOpenShift(ctx, q, e.club)
		if err != nil {
			return err
		}
		eq(t, "locked", locked.ID, sh.ID)
		_, err = svc.RecordGuestSession(ctx, q, e.club, a, 120000, "Гостевая сессия: 120 минут, ПК 1, ПК 2 (карта)", cash.PayCard, "")
		return err
	}))
	// Вход интерфейса CashRecorder (поля как billing/service.GuestIncome); тариф за 0 — приход 0.00 без проводок.
	e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
		shiftID, ok, err := svc.LockOpenShift(ctx, q, e.club)
		if err != nil || !ok {
			t.Fatalf("lock: %v %v", ok, err)
		}
		res, err := svc.RecordGuestSessionIncome(ctx, q, service.GuestSessionIncome{
			ClubID: e.club, ShiftID: shiftID, CashierID: a, Amount: 0, PaymentMethod: "cash",
			Description: "Гостевая сессия: тариф «Бесплатно», ПК 3 (наличные)", IdempotencyKey: seq("g"),
			Computers: "ПК 3", Minutes: 60, Tariff: "Бесплатно",
		})
		eq(t, "no ledger", res.LedgerTransactionID, 0)
		eq(t, "transaction", res.CashTransactionID != 0, true)
		if err != nil {
			return err
		}
		_, err = svc.RecordGuestSessionIncome(ctx, q, service.GuestSessionIncome{
			ClubID: e.club, ShiftID: shiftID + 1000, CashierID: a, Amount: 100, PaymentMethod: "cash", Description: "x",
		})
		wantCashErr(t, err, "payment_method", service.MsgGuestOpenShiftFirst)
		return nil
	}))
	r := e.report(cash.ReportX, 0, "Кассир")
	hasLine(t, r, "Гостевые сессии · 2 · 1 200,00 KZT")
	// Вернуть гостевую сессию нельзя (refundKind() = null).
	id := e.count("SELECT id FROM cash_transactions WHERE club_id = $1 AND income_kind = 'guest_session'", e.club)
	_, err = e.refund(a, id)
	wantCashErr(t, err, "refund", cash.MsgNotRefundable)
	e.checkLedger(120000)
}

// RLS: операции клуба A не видят смен, операций и товаров клуба B, даже если передать их id.
func TestTenantIsolation(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	ua, ub := a.user("Кассир A"), b.user("Кассир B")
	a.open(ua, "100")
	shB := b.open(ub, "200")
	pB := b.product("Cola B", 50000, 5)
	saleB := b.mustSale(ub, prod(pB, 1))

	// Возврат чужой продажи — не найдена.
	_, err := a.refund(ua, saleB.Transaction.ID)
	wantIs(t, err, service.ErrNotFound)
	// Продажа чужого товара — товара нет (форма: items.0.product_id).
	_, err = a.sale(ua, prod(pB, 1))
	wantRule(t, err, "items.0.product_id", cash.RuleIn)
	eq(t, "stock B", b.stockOf(pB), 4)
	// Закрытие и Z-отчёт чужой смены — не найдена.
	_, err = a.close(ua, shB.ID, false)
	wantIs(t, err, service.ErrNotFound)
	a.must(a.tx(func(ctx context.Context, q *store.Queries) error {
		_, err := svc.Report(ctx, q, service.ReportParams{ClubID: a.club, Type: cash.ReportZ, ShiftID: shB.ID})
		wantIs(t, err, service.ErrNotFound)
		// Клуб B в транзакции клуба A: RLS прячет его смену («Сначала откройте смену.»), ledger — ErrTenantMismatch.
		_, err = svc.Transact(ctx, q, service.TransactParams{ClubID: b.club, Type: cash.Income,
			Amount: cash.TransactionAmount{Amount: 100}, Description: "x", Actor: actor(ub)})
		wantCashErr(t, err, "amount", cash.MsgOpenShiftFirst)
		_, err = stock.LockProduct(ctx, q, b.club, pB)
		wantIs(t, err, stock.ErrProductNotFound)
		return nil
	}))
	// Без тенанта (платформенная транзакция под ролью приложения) касса не видит ничего.
	a.must(db.InPlatformTx(context.Background(), fixDB.App, func(q *store.Queries) error {
		sh, err := service.LockOpenShift(context.Background(), q, b.club)
		eq(t, "no shift without tenant", sh == nil, true)
		return err
	}))
	b.checkLedger(b.totals(shB.ID).Balance())
}
