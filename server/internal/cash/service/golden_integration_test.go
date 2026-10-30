//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Сквозные эталонные сценарии кассы (docs/golden-scenarios.md): от входа сервиса до балансов, проводок,
// cash_transactions, остатков склада и строк X/Z. Имя подтеста = ID сценария.

func TestGoldenTopUpBonus(t *testing.T) {
	t.Run("GS-002", func(t *testing.T) {
		e := newEnv(t)
		e.topUpBonus(true, [2]float64{2000, 5}, [2]float64{10000, 10})
		cashier := e.user("Кассир")
		c := e.customer("Клиент бонус", 10000, 2500)
		sh := e.open(cashier, "0")

		r := e.mustTopUp(cashier, c, cash.FieldMain, "1000", cash.PayCash)
		eq(t, "bonus 1000", r.Bonus, 0)
		e.wantBalances(c, 110000, 2500)

		r = e.mustTopUp(cashier, c, cash.FieldMain, "2500", cash.PayCash)
		eq(t, "bonus 2500", r.Bonus, 12500)
		eq(t, "percent", r.BonusPercent, 5.0)
		e.wantBalances(c, 360000, 15000)

		r = e.mustTopUp(cashier, c, cash.FieldMain, "10000.55", cash.PayCash)
		eq(t, "bonus 10000.55", r.Bonus, 100006) // intdiv(1000055×1000+5000, 10000)
		eq(t, "new bonus balance", r.Balance.NewBonusBalance, 115006)
		e.wantBalances(c, 1360055, 115006)
		// Касса получает только деньги: 1000 + 2500 + 10000.55.
		eq(t, "cash income", e.totals(sh.ID).Income, 1350055)
		eq(t, "bonus fund", e.account(ledger.AccountBonusFund), -(100006 + 12500))
		e.checkLedger(1350055)

		// Решение Q-003 (docs/DECISIONS.md): чек v2 — бонус в тиынах (в старом коде "bonus": 1000.06 тенге).
		var receipt struct {
			V     int `json:"v"`
			TopUp struct {
				BonusTiyn int64 `json:"bonus_tiyn"`
			} `json:"top_up"`
		}
		e.must(json.Unmarshal(r.Transaction.ReceiptData, &receipt))
		eq(t, "receipt version", receipt.V, 2)
		eq(t, "receipt bonus", receipt.TopUp.BonusTiyn, 100006)
		eq(t, "income kind", *r.Transaction.IncomeKind, "top_up")
	})
	t.Run("GS-003", func(t *testing.T) {
		e := newEnv(t)
		e.topUpBonus(false, [2]float64{100, 50})
		cashier := e.user("Кассир")
		c := e.customer("Клиент бонус", 10000, 2500)
		e.open(cashier, "0")
		e.mustTopUp(cashier, c, cash.FieldMain, "1000", cash.PayCash)
		e.wantBalances(c, 110000, 2500)
		e.topUpBonus(true, [2]float64{100, 50})
		r := e.mustTopUp(cashier, c, cash.FieldBonus, "1000", cash.PayCash)
		eq(t, "bonus", r.Bonus, 0)
		e.wantBalances(c, 110000, 102500)
		e.checkLedger(200000)
	})
	t.Run("GAP-018", func(t *testing.T) {
		e := newEnv(t)
		e.topUpBonus(true, [2]float64{500, 2.5})
		cashier := e.user("Кассир")
		c := e.customer("Клиент", 0, 0)
		e.open(cashier, "0")
		r := e.mustTopUp(cashier, c, cash.FieldMain, "999.99", cash.PayCash)
		eq(t, "bonus", r.Bonus, 2500) // intdiv(99999 × 250 + 5000, 10000)
		e.wantBalances(c, 99999, 2500)
	})
	t.Run("GAP-019", func(t *testing.T) {
		e := newEnv(t)
		e.topUpBonus(true, [2]float64{2000, 5}, [2]float64{5000, 3})
		cashier := e.user("Кассир")
		c := e.customer("Клиент", 0, 0)
		e.open(cashier, "0")
		for _, c2 := range []struct {
			amount string
			bonus  money.Tiyn
		}{{"1999.99", 0}, {"2000.00", 10000}, {"6000.00", 30000}} {
			r := e.mustTopUp(cashier, c, cash.FieldMain, c2.amount, cash.PayCash)
			eq(t, "bonus "+c2.amount, r.Bonus, c2.bonus)
		}
	})
	t.Run("CQ-4", func(t *testing.T) {
		// Решение Q-002 (docs/DECISIONS.md): было — "0.0149999999999999" давал баланс +0.02 и бонус от 1 тиына;
		// стало — такой ввод отклоняется (decimal), а для ввода с ≤ 2 знаками база бонуса равна сумме.
		if _, err := cash.ParseTopUpAmount("0.0149999999999999"); err == nil {
			t.Fatal("Q-002: > 2 знаков после точки должно отклоняться")
		}
		e := newEnv(t)
		e.topUpBonus(true, [2]float64{0, 100})
		cashier := e.user("Кассир")
		c := e.customer("Клиент", 0, 0)
		e.open(cashier, "0")
		r := e.mustTopUp(cashier, c, cash.FieldMain, "0.02", cash.PayCash)
		eq(t, "amount", money.Tiyn(r.Transaction.Amount), 2)
		eq(t, "bonus", r.Bonus, 2)
		e.wantBalances(c, 2, 2)
	})
}

func TestGoldenRefund(t *testing.T) {
	t.Run("GS-004", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		sh := e.open(owner, "1000")
		cola := e.product("Coca Cola", 70000, 10)
		sale := e.mustSale(owner, prod(cola, 3))
		eq(t, "sale", sale.Transaction.Amount, int64(210000))
		eq(t, "stock after sale", e.stockOf(cola), 7)

		ref, err := e.refund(owner, sale.Transaction.ID)
		e.must(err)
		eq(t, "refund_of_id", *ref.Refund.RefundOfID, sale.Transaction.ID)
		eq(t, "refund type", ref.Refund.Type, "expense")
		eq(t, "refund amount", ref.Refund.Amount, int64(210000))
		eq(t, "refund description", ref.Refund.Description, cash.RefundDescription(sale.Transaction.ID))
		eq(t, "stock after refund", e.stockOf(cola), 10)
		eq(t, "receipt movements", e.count("SELECT count(*) FROM stock_movements WHERE product_id = $1 AND type = 'receipt' AND note = 'Возврат POS' AND quantity = 3", cola), 1)
		eq(t, "balance", e.totals(sh.ID).Balance(), 100000)
		eq(t, "revenue pos", e.account(ledger.AccountRevenuePOS), 0)
		// Решение CSQ-6 (docs/DECISIONS.md): pos_refund ссылается на исходную операцию pos_sale.
		eq(t, "reverses pos_sale", ref.Ledger.Refs.ReversesTransactionID, sale.Ledger.TransactionID)
		eq(t, "reverses_transaction_id in db", e.count(`SELECT reverses_transaction_id FROM ledger_transactions
			WHERE club_id = $1 AND kind = 'pos_refund' AND id = $2`, e.club, ref.Ledger.TransactionID), sale.Ledger.TransactionID)
		e.checkLedger(100000)

		_, err = e.refund(owner, sale.Transaction.ID)
		wantCashErr(t, err, "refund", cash.MsgAlreadyRefunded)

		x := e.report(cash.ReportX, 0, "Владелец")
		hasLine(t, x, cash.LabelNoGoods, "Возвраты (расход) · 1 · − 2 100,00 KZT")
	})
	t.Run("GS-005", func(t *testing.T) {
		e := newEnv(t)
		e.topUpBonus(true, [2]float64{1000, 10})
		owner := e.user("Владелец")
		c := e.customer("Клиент", 0, 0)
		e.open(owner, "0")
		top := e.mustTopUp(owner, c, cash.FieldMain, "2000", cash.PayCash)
		e.wantBalances(c, 200000, 20000)

		// Клиент потратил 500 с основного (покупка) — на балансе 1500 < 2000.
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := billing.Charge(ctx, q, billing.ChargeParams{ClubID: e.club, UserID: c, Kind: ledger.KindTariffPurchase,
				Revenue: ledger.AccountRevenueTime, Main: 50000, IdempotencyKey: seq("spend")})
			return err
		}))
		_, err := e.refund(owner, top.Transaction.ID)
		wantCashErr(t, err, "refund", cash.MsgTopUpSpent)
		e.wantBalances(c, 150000, 20000)

		// Владелец довнёс 1000 без кассы — 2500.
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := billing.OwnerAdjust(ctx, q, billing.OwnerAdjustParams{ClubID: e.club, UserID: c, Field: billing.FieldMain,
				Amount: 100000, IdempotencyKey: seq("adj")})
			return err
		}))
		ref, err := e.refund(owner, top.Transaction.ID)
		e.must(err)
		e.wantBalances(c, 50000, 0)
		eq(t, "bonus taken back", ref.TopUp.BonusTakenBack, 20000)
		eq(t, "refund amount", ref.Refund.Amount, int64(200000))
		// Решение CSQ-6: возврат пополнения ссылается на исходную операцию top_up.
		eq(t, "reverses top_up", ref.TopUp.Ledger.Refs.ReversesTransactionID, top.Balance.Ledger.TransactionID)
		e.checkLedger(0)
	})
	t.Run("GS-006", func(t *testing.T) {
		// Прочий приход вернуть нельзя; возврат продажи пишет кассира в user_id (право cash.delete — у HTTP-слоя).
		e := newEnv(t)
		owner := e.user("Владелец")
		cashier := e.user("Кассир")
		e.open(owner, "5000")
		p := e.product("Товар", 10000, 5)
		sale := e.mustSale(owner, prod(p, 1))
		other := e.mustTransact(owner, cash.Income, "300", "Разное")
		_, err := e.refund(cashier, other.Transaction.ID)
		wantCashErr(t, err, "refund", cash.MsgNotRefundable)
		ref, err := e.refund(cashier, sale.Transaction.ID)
		e.must(err)
		eq(t, "refund user", *ref.Refund.UserID, cashier)
	})
	t.Run("GS-142", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		e.open(owner, "1000")
		printing := e.service("Распечатка", 5000, true)
		cola := e.product("Cola", 50000, 10)
		sale := e.mustSale(owner, serv(printing, 10), prod(cola, 2))
		eq(t, "sale", sale.Transaction.Amount, int64(150000))
		eq(t, "stock", e.stockOf(cola), 8)
		ref, err := e.refund(owner, sale.Transaction.ID)
		e.must(err)
		eq(t, "stock back", e.stockOf(cola), 10)
		eq(t, "refund amount", ref.Refund.Amount, int64(150000))
		eq(t, "movements", len(ref.Movements), 1)
		e.checkLedger(100000)
	})
	t.Run("GAP-022", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		c := e.customer("Клиент", 0, 0)
		sh := e.open(owner, "0")
		top := e.mustTopUp(owner, c, cash.FieldMain, "1000", cash.PayCash)
		e.mustTransact(owner, cash.Expense, "800", "Расход")
		_, err := e.refund(owner, top.Transaction.ID)
		wantCashErr(t, err, "refund", cash.MsgNotEnoughCash)
		e.wantBalances(c, 100000, 0)
		eq(t, "balance", e.totals(sh.ID).Balance(), 20000)
		e.checkLedger(20000)
	})
	t.Run("GAP-023", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		sh1 := e.open(owner, "0")
		p := e.product("Cola", 50000, 10)
		sale := e.mustSale(owner, prod(p, 1))
		_, err := e.close(owner, sh1.ID, false)
		e.must(err)
		e.open(owner, "500")
		_, err = e.refund(owner, sale.Transaction.ID)
		wantCashErr(t, err, "refund", cash.MsgRefundOpenShift)
		eq(t, "stock", e.stockOf(p), 9)
		// Без открытой смены — та же ошибка.
		e2 := newEnv(t)
		o2 := e2.user("Владелец")
		s2 := e2.open(o2, "0")
		p2 := e2.product("Cola", 50000, 10)
		sale2 := e2.mustSale(o2, prod(p2, 1))
		_, err = e2.close(o2, s2.ID, false)
		e2.must(err)
		_, err = e2.refund(o2, sale2.Transaction.ID)
		wantCashErr(t, err, "refund", cash.MsgRefundOpenShift)
	})
	t.Run("GAP-024", func(t *testing.T) {
		e := newEnv(t)
		e.topUpBonus(true, [2]float64{1000, 10})
		owner := e.user("Владелец")
		c := e.customer("Клиент", 0, 0)
		e.open(owner, "0")
		top := e.mustTopUp(owner, c, cash.FieldMain, "2000", cash.PayCash)
		e.wantBalances(c, 200000, 20000)
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error { // потратил 150 бонусов
			_, err := billing.Charge(ctx, q, billing.ChargeParams{ClubID: e.club, UserID: c, Kind: ledger.KindTariffPurchase,
				Revenue: ledger.AccountRevenueTime, Bonus: 15000, IdempotencyKey: seq("spend")})
			return err
		}))
		ref, err := e.refund(owner, top.Transaction.ID)
		e.must(err)
		e.wantBalances(c, 0, 0)
		eq(t, "bonus taken back", ref.TopUp.BonusTakenBack, 5000)
		eq(t, "refund", ref.Refund.Amount, int64(200000))
		e.checkLedger(0)
	})
	t.Run("GAP-025", func(t *testing.T) {
		e := newEnv(t)
		e.topUpBonus(true, [2]float64{1, 50})
		owner := e.user("Владелец")
		c := e.customer("Клиент", 10000, 0)
		e.open(owner, "0")
		top := e.mustTopUp(owner, c, cash.FieldBonus, "500", cash.PayCash)
		eq(t, "bonus for bonus balance", top.Bonus, 0)
		e.wantBalances(c, 10000, 50000)
		// Бонусный 499.99 < 500 → нельзя.
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := billing.Charge(ctx, q, billing.ChargeParams{ClubID: e.club, UserID: c, Kind: ledger.KindTariffPurchase,
				Revenue: ledger.AccountRevenueTime, Bonus: 1, IdempotencyKey: seq("spend")})
			return err
		}))
		_, err := e.refund(owner, top.Transaction.ID)
		wantCashErr(t, err, "refund", cash.MsgTopUpSpent)
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := billing.OwnerAdjust(ctx, q, billing.OwnerAdjustParams{ClubID: e.club, UserID: c, Field: billing.FieldBonus,
				Amount: 1, IdempotencyKey: seq("adj")})
			return err
		}))
		_, err = e.refund(owner, top.Transaction.ID)
		e.must(err)
		e.wantBalances(c, 10000, 0)
		e.checkLedger(0)
	})
}

func TestGoldenShift(t *testing.T) {
	t.Run("GS-007", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		sh := e.open(owner, "10000")
		eq(t, "opened_at", sh.OpenedAt.Equal(clock), true)
		_, err := e.tryOpen(owner, "5000")
		wantCashErr(t, err, "opening_balance", cash.MsgShiftAlreadyOpen)
		eq(t, "open shifts", e.count("SELECT count(*) FROM cash_shifts WHERE club_id = $1 AND status = 'open'", e.club), 1)
		// Остаток кассы по журналу = начальному остатку (проводка opening_balance).
		e.checkLedger(1000000)
		eq(t, "opening_balance account", e.account(ledger.AccountOpeningBalance), 1000000)
	})
	t.Run("GS-008", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		sh := e.open(owner, "10000")
		e.mustTransact(owner, cash.Income, "3000", "Продажа")
		e.mustTransact(owner, cash.Expense, "1000", "Расход")
		eq(t, "transactions", e.count("SELECT count(*) FROM cash_transactions WHERE cash_shift_id = $1", sh.ID), 2)
		x := e.report(cash.ReportX, 0, "Владелец")
		eq(t, "balance", x.Report.Totals.Balance(), 1200000)
		hasLine(t, x, "Итог в кассе · 12 000,00 KZT")
		eq(t, "cash_other", e.account(ledger.AccountCashOther), 200000)
		e.checkLedger(1200000)
	})
	t.Run("GS-009", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		sh := e.open(owner, "5000")
		e.mustTransact(owner, cash.Income, "2000", "Приход")
		_, err := e.close(owner, sh.ID, true)
		wantCashErr(t, err, "shift", cash.MsgPrintZFirst)
		eq(t, "status", e.count("SELECT count(*) FROM cash_shifts WHERE id = $1 AND status = 'open'", sh.ID), 1)

		var nonce string
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			var err error
			nonce, err = svc.IssueZReportNonce(ctx, q, e.club, sh.ID)
			return err
		}))
		eq(t, "nonce length", len(nonce), 40)
		mark := func(n string) bool {
			var ok bool
			e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
				var err error
				ok, err = svc.MarkZReportPrinted(ctx, q, e.club, sh.ID, n, actor(owner))
				return err
			}))
			return ok
		}
		// Неверный nonce гасит выданный (session()->pull): верный после этого уже не подходит.
		eq(t, "wrong nonce", mark("x"), false)
		eq(t, "spent nonce", mark(nonce), false)
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			var err error
			nonce, err = svc.IssueZReportNonce(ctx, q, e.club, sh.ID)
			return err
		}))
		eq(t, "printed", mark(nonce), true)
		eq(t, "nonce is one-time", mark(nonce), false)

		res, err := e.close(owner, sh.ID, true)
		e.must(err)
		eq(t, "status", res.Shift.Status, "closed")
		eq(t, "closing_balance", *res.Shift.ClosingBalance, int64(700000))
		eq(t, "open_marker", res.Shift.OpenMarker == nil, true)
		eq(t, "closed_at", res.Shift.ClosedAt.Equal(clock), true)
		e.checkLedger(700000)

		// Отметка печати для закрытой смены — 422, повторное закрытие — «Смена уже закрыта.».
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := svc.MarkZReportPrinted(ctx, q, e.club, sh.ID, nonce, actor(owner))
			wantIs(t, err, service.ErrShiftNotOpen)
			return nil
		}))
		_, err = e.close(owner, sh.ID, false)
		wantCashErr(t, err, "shift", cash.MsgShiftAlreadyClose)
	})
	t.Run("GAP-027", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		c := e.customer("Клиент", 0, 0)
		sh := e.open(owner, "1000")
		cola := e.product("Cola", 70000, 10)
		sale := e.mustSale(owner, prod(cola, 3))
		e.mustTopUp(owner, c, cash.FieldMain, "5000", cash.PayCard)
		e.mustTransact(owner, cash.Expense, "300", "Расход")
		_, err := e.refund(owner, sale.Transaction.ID)
		e.must(err)
		res, err := e.close(owner, sh.ID, false)
		e.must(err)
		eq(t, "income", res.Totals.Income, 710000)
		eq(t, "expense", res.Totals.Expense, 240000)
		eq(t, "closing", *res.Shift.ClosingBalance, int64(570000))
		e.checkLedger(570000)
	})
	t.Run("shift-opening-drawer", func(t *testing.T) {
		// Между сменами из кассы изъяли 700: проводка выравнивания cash_shift_opening / opening_balance
		// (решение CSQ-1, docs/DECISIONS.md).
		e := newEnv(t)
		owner := e.user("Владелец")
		sh := e.open(owner, "1000")
		e.mustTransact(owner, cash.Income, "700", "Приход")
		_, err := e.close(owner, sh.ID, false)
		e.must(err)
		e.checkLedger(170000)
		e.open(owner, "1000")
		e.checkLedger(100000)
		eq(t, "opening_balance account", e.account(ledger.AccountOpeningBalance), 100000-70000)
		eq(t, "opening ops", e.count("SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'cash_shift_opening'", e.club), 2)
		eq(t, "opening op of shift 2", e.count(`SELECT -e.amount FROM ledger_transactions t
			JOIN ledger_entries e ON e.club_id = t.club_id AND e.transaction_id = t.id AND e.created_at = t.created_at
			JOIN ledger_accounts a ON a.club_id = e.club_id AND a.id = e.account_id AND a.kind = 'cash_drawer'
			WHERE t.club_id = $1 AND t.kind = 'cash_shift_opening' AND t.idempotency_key <> $2`, e.club, "cash_shift_open:"+strconv.FormatInt(sh.ID, 10)), -70000)
		eq(t, "no cash_income/expense for opening", e.count("SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind IN ('cash_income', 'cash_expense') AND cash_transaction_id IS NULL", e.club), 0)
	})
	t.Run("drawer-snapshot", func(t *testing.T) {
		// Решение CSQ-7 (docs/DECISIONS.md): закрытие смены запоминает снимок остатка кассы по журналу;
		// открытие следующей — снимок + проводки после него.
		e := newEnv(t)
		owner := e.user("Владелец")
		sh := e.open(owner, "1000")
		e.mustTransact(owner, cash.Income, "700", "Приход")
		res, err := e.close(owner, sh.ID, false)
		e.must(err)
		eq(t, "snapshot balance", *res.Shift.DrawerSnapshotBalance, int64(170000))
		eq(t, "snapshot entry", *res.Shift.DrawerSnapshotEntryID, e.count(`SELECT max(e.id) FROM ledger_entries e
			JOIN ledger_accounts a ON a.club_id = e.club_id AND a.id = e.account_id
			WHERE a.club_id = $1 AND a.kind = 'cash_drawer'`, e.club))
		eq(t, "snapshot at", res.Shift.DrawerSnapshotAt != nil, true)

		// Проводка кассы вне смены после снимка (входящий остаток переноса) — учитывается.
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := ledger.Post(ctx, q, ledger.Operation{
				ClubID: e.club, Kind: ledger.KindMigrationOpeningBalance, IdempotencyKey: "drawer-after-snapshot",
				Entries: []ledger.Entry{
					{Account: ledger.ClubAccount(ledger.AccountCashDrawer), Amount: -5000},
					{Account: ledger.ClubAccount(ledger.AccountOpeningBalance), Amount: 5000},
				},
			})
			return err
		}))
		eq(t, "drawer", e.drawer(), 175000)

		// Снимок действительно используется: подменённый остаток снимка сдвигает выравнивание на ту же разницу.
		_, err = fixDB.Owner.Exec(context.Background(),
			"UPDATE cash_shifts SET drawer_snapshot_balance = drawer_snapshot_balance + 100 WHERE club_id = $1 AND id = $2", e.club, sh.ID)
		e.must(err)
		sh2 := e.open(owner, "2000")
		eq(t, "opening adjustment", e.count(`SELECT -e.amount FROM ledger_transactions t
			JOIN ledger_entries e ON e.club_id = t.club_id AND e.transaction_id = t.id AND e.created_at = t.created_at
			JOIN ledger_accounts a ON a.club_id = e.club_id AND a.id = e.account_id AND a.kind = 'cash_drawer'
			WHERE t.club_id = $1 AND t.idempotency_key = $2`, e.club, "cash_shift_open:"+strconv.FormatInt(sh2.ID, 10)),
			200000-175000-100)
		_, err = fixDB.Owner.Exec(context.Background(),
			"UPDATE cash_shifts SET drawer_snapshot_balance = drawer_snapshot_balance - 100 WHERE club_id = $1 AND id = $2", e.club, sh.ID)
		e.must(err)
		e.checkLedger(200000 - 100)

		// Вторая смена: снимок = снимок первой + проводки после него.
		e.mustTransact(owner, cash.Expense, "50", "Расход")
		res2, err := e.close(owner, sh2.ID, false)
		e.must(err)
		eq(t, "snapshot 2", *res2.Shift.DrawerSnapshotBalance, e.count(`SELECT -sum(e.amount)::bigint FROM ledger_entries e
			JOIN ledger_accounts a ON a.club_id = e.club_id AND a.id = e.account_id
			WHERE a.club_id = $1 AND a.kind = 'cash_drawer'`, e.club))
		eq(t, "closing", *res2.Shift.ClosingBalance, int64(195000))
	})
}

func TestGoldenSale(t *testing.T) {
	t.Run("GS-010", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		sh := e.open(owner, "0")
		p := e.product("POS Product", 75000, 5)
		sale := e.mustSale(owner, prod(p, 2))
		eq(t, "stock", e.stockOf(p), 3)
		eq(t, "amount", sale.Transaction.Amount, int64(150000))
		eq(t, "description", sale.Transaction.Description, cash.POSSale)
		eq(t, "payment_method", sale.Transaction.PaymentMethod == nil, true)
		eq(t, "created_at", sale.Transaction.CreatedAt.Equal(clock), true)
		// Решение Q-003 (docs/DECISIONS.md): чек v2 — суммы в тиынах (в старом коде "total": 1500.0 тенге).
		var receipt struct {
			V     int `json:"v"`
			Items []struct {
				ProductID int64 `json:"product_id"`
				Quantity  int64 `json:"quantity"`
				PriceTiyn int64 `json:"price_tiyn"`
				TotalTiyn int64 `json:"total_tiyn"`
			} `json:"items"`
		}
		e.must(json.Unmarshal(sale.Transaction.ReceiptData, &receipt))
		eq(t, "receipt version", receipt.V, 2)
		eq(t, "receipt quantity", receipt.Items[0].Quantity, 2)
		eq(t, "receipt price", receipt.Items[0].PriceTiyn, 75000)
		eq(t, "receipt total", receipt.Items[0].TotalTiyn, 150000)
		eq(t, "movements", len(sale.Movements), 1)
		eq(t, "balance_after", sale.Movements[0].BalanceAfter, 3)
		eq(t, "note", *sale.Movements[0].Note, cash.SaleNote)
		eq(t, "ledger kind", sale.Ledger.Kind, ledger.KindPOSSale)
		eq(t, "revenue pos", e.account(ledger.AccountRevenuePOS), 150000)
		eq(t, "audit", e.count("SELECT count(*) FROM audit_log WHERE club_id = $1 AND action = 'cash.pos_sale' AND subject_id = $2", e.club, sale.Transaction.ID), 1)
		e.checkLedger(e.totals(sh.ID).Balance())
	})
	t.Run("GS-141", func(t *testing.T) {
		e := newEnv(t)
		cashier := e.user("Кассир")
		ps5 := e.service("PS5 — 1 час", 150000, true)
		old := e.service("Старое", 1000, false)
		e.open(cashier, "0")
		cola := e.product("Cola", 50000, 10)
		sale := e.mustSale(cashier, serv(ps5, 2), prod(cola, 1))
		eq(t, "amount", sale.Transaction.Amount, int64(350000))
		eq(t, "cola", e.stockOf(cola), 9)
		x := e.report(cash.ReportX, 0, "Кассир")
		hasLine(t, x, "PS5 — 1 час · 2 · —", "Cola · 1 · 9")
		_, err := e.sale(cashier, serv(old, 1))
		wantRule(t, err, "items.0.service_id", cash.RuleIn)
	})
	t.Run("GAP-026", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		p := e.product("Cola", 50000, 2)
		_, err := e.sale(owner, prod(p, 1))
		wantCashErr(t, err, "items", cash.MsgOpenShiftFirst)
		sh := e.open(owner, "0")
		_, err = e.sale(owner, prod(p, 3))
		wantCashErr(t, err, "items", cash.MsgNotEnoughStock)
		ce, _ := cash.AsError(err)
		eq(t, "text", ce.Text(), "Недостаточно товара: Cola.")
		eq(t, "stock", e.stockOf(p), 2)
		eq(t, "write-offs", e.count("SELECT count(*) FROM stock_movements WHERE product_id = $1 AND type = 'write_off'", p), 0)
		eq(t, "transactions", e.count("SELECT count(*) FROM cash_transactions WHERE cash_shift_id = $1", sh.ID), 0)
		// Удалённый товар не продаётся (Rule::exists … whereNull('deleted_at')).
		_, err = fixDB.Owner.Exec(context.Background(), "UPDATE products SET deleted_at = now() WHERE id = $1", p)
		e.must(err)
		_, err = e.sale(owner, prod(p, 1))
		wantRule(t, err, "items.0.product_id", cash.RuleIn)
	})
}

func TestGoldenTopUp(t *testing.T) {
	t.Run("GS-012", func(t *testing.T) {
		e := newEnv(t)
		cashier := e.user("Кассир")
		c := e.customer("Клиент POS", 10000, 2500)
		e.open(cashier, "500")
		r := e.mustTopUp(cashier, c, cash.FieldMain, "750", cash.PayCard)
		e.wantBalances(c, 85000, 2500)
		eq(t, "type", r.Transaction.Type, "income")
		eq(t, "amount", r.Transaction.Amount, int64(75000))
		eq(t, "description", r.Transaction.Description, "Пополнение основной баланс: Клиент POS (карта)")
		eq(t, "payment_method", *r.Transaction.PaymentMethod, "card")
		eq(t, "operator", *r.Transaction.UserID, cashier)
		eq(t, "audit", e.count("SELECT count(*) FROM audit_log WHERE club_id = $1 AND action = 'money.top_up' AND subject_id = $2", e.club, c), 1)
		e.checkLedger(125000)
	})
	t.Run("GS-013", func(t *testing.T) {
		e := newEnv(t)
		cashier := e.user("Кассир")
		c := e.customer("Клиент", 0, 0)
		other := newEnv(t).customer("Чужой", 0, 0)
		e.open(cashier, "0")
		_, err := e.topUp(cashier, 999999, cash.FieldMain, "100", cash.PayCash)
		wantRule(t, err, "user_id", cash.RuleIn)
		_, err = e.topUp(cashier, other, cash.FieldMain, "100", cash.PayCash) // клиент другого клуба
		wantRule(t, err, "user_id", cash.RuleIn)
		_, err = cash.ParseTopUpAmount("0")
		wantRule(t, err, "amount", cash.RuleGt)
		_, err = e.topUp(cashier, c, "unknown", "100", cash.PayCash)
		wantRule(t, err, "balance_type", cash.RuleIn)
		_, err = e.topUp(cashier, c, cash.FieldMain, "100", "unknown")
		wantRule(t, err, "payment_method", cash.RuleIn)
		eq(t, "transactions", e.count("SELECT count(*) FROM cash_transactions WHERE club_id = $1", e.club), 0)
	})
}

func TestGoldenReport(t *testing.T) {
	t.Run("GS-015", func(t *testing.T) {
		e := newEnv(t)
		cashier := e.user("Айгерим")
		c := e.customer("Клиент", 0, 0)
		sh := e.open(cashier, "1000")
		cola := e.product("Coca Cola", 70000, 20)
		chips := e.product("Чипсы", 50000, 20)
		e.mustSale(cashier, prod(cola, 3), prod(chips, 1))
		e.mustSale(cashier, prod(cola, 2))
		e.mustTopUp(cashier, c, cash.FieldMain, "5000", cash.PayCard)
		_, err := e.guest(cashier, 60000, "Гостевая сессия: 60 минут, ПК 1 (наличные)", cash.PayCash, seq("guest"))
		e.must(err)
		e.mustTransact(cashier, cash.Expense, "300", "Вода для кулера")

		for _, r := range []service.ReportResult{e.report(cash.ReportX, 0, "Айгерим"), e.report(cash.ReportZ, sh.ID, "Айгерим")} {
			hasLine(t, r,
				"Продажа товаров · 2 · 4 000,00 KZT",
				"Пополнение балансов · 1 · 5 000,00 KZT",
				"Гостевые сессии · 1 · 600,00 KZT",
				"Coca Cola · 5 · 15",
				"Чипсы · 1 · 19",
				"Всего · 6",
				"Айгерим · 5 · 9 600,00 KZT · 300,00 KZT",
			)
			noLine(t, r, cash.LabelKindOther)
		}
		e.checkLedger(e.totals(sh.ID).Balance())
		eq(t, "revenue_time", e.account(ledger.AccountRevenueTime), 60000)
		compareWithBuildReport(t, e, sh)
	})
	t.Run("GS-016", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		sh := e.open(owner, "0")
		z := e.report(cash.ReportZ, sh.ID, "Владелец")
		hasLine(t, z, cash.LabelNoGoods)
	})
	t.Run("GS-017", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Азат")
		cashier := e.user("Айгерим")
		sh := e.open(cashier, "0")
		x := e.report(cash.ReportX, 0, "Азат")
		got := lines(x)[:3]
		want := []string{"Номер смены · #" + itoa(sh.ID), "Кассир · Айгерим", "Оператор · Азат"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("header = %q, want %q", got, want)
		}
		_, err := e.close(owner, sh.ID, false)
		e.must(err)
		z := e.report(cash.ReportZ, sh.ID, "Азат")
		hasLine(t, z, "Кассир · Айгерим", "Закрыл смену · Азат", "Оператор · Азат")
		// X-отчёт без открытой смены — 404.
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := svc.Report(ctx, q, service.ReportParams{ClubID: e.club, Type: cash.ReportX})
			wantIs(t, err, service.ErrNotFound)
			return nil
		}))
	})
	t.Run("GAP-028", func(t *testing.T) {
		e := newEnv(t)
		owner := e.user("Владелец")
		sh := e.open(owner, "0")
		cola := e.product("Cola", 70000, 10)
		sale := e.mustSale(owner, prod(cola, 2))
		e.mustTransact(owner, cash.Income, "300", "Разное")
		_, err := e.guest(owner, 60000, "Гостевая сессия: 60 минут, ПК 1 (наличные)", cash.PayCash, "")
		e.must(err)
		_, err = e.refund(owner, sale.Transaction.ID)
		e.must(err)
		r := e.report(cash.ReportZ, sh.ID, "Владелец").Report
		want := []cash.KindRow{
			{Label: cash.LabelKindGoods, Count: 1, Amount: 140000},
			{Label: cash.LabelKindTopUps, Count: 0, Amount: 0},
			{Label: cash.LabelKindGuests, Count: 1, Amount: 60000},
			{Label: cash.LabelKindOther, Count: 1, Amount: 30000},
		}
		if !reflect.DeepEqual(r.Kinds, want) {
			t.Fatalf("kinds = %+v", r.Kinds)
		}
		eq(t, "items", len(r.Items), 0)
		eq(t, "refunds", r.Refunds, cash.RefundsRow{Count: 1, Amount: 140000})
		eq(t, "staff", len(r.Staff), 1)
		eq(t, "staff row", r.Staff[0], cash.StaffRow{UserID: owner, Name: "Владелец", Count: 4, Income: 230000, Expense: 140000})
		compareWithBuildReport(t, e, sh)
		e.checkLedger(90000)
	})
	t.Run("GAP-029", func(t *testing.T) {
		e := newEnv(t)
		a := e.user("А")
		b := e.user("Б")
		sh := e.open(a, "0")
		e.mustTransact(a, cash.Income, "500", "Приход А")
		e.mustTransact(b, cash.Income, "1500", "Приход Б")
		e.mustTransact(b, cash.Expense, "100", "Расход Б")
		r := e.report(cash.ReportZ, sh.ID, "А").Report
		want := []cash.StaffRow{
			{UserID: b, Name: "Б", Count: 2, Income: 150000, Expense: 10000},
			{UserID: a, Name: "А", Count: 1, Income: 50000},
		}
		if !reflect.DeepEqual(r.Staff, want) {
			t.Fatalf("staff = %+v", r.Staff)
		}
		eq(t, "discount", r.Discount, 0)
		compareWithBuildReport(t, e, sh)
	})
}

// compareWithBuildReport — отчёт из SQL-агрегатов равен cash.BuildReport по полному списку операций смены.
func compareWithBuildReport(t *testing.T, e *env, sh store.CashShift) {
	t.Helper()
	got := e.report(cash.ReportZ, sh.ID, "—").Report
	stockLeft := map[int64]int64{}
	names := map[int64]string{}
	ops := e.allOps(sh.ID)
	for _, op := range ops {
		if op.Receipt != nil {
			for _, l := range op.Receipt.Items {
				if l.ProductID != 0 {
					stockLeft[l.ProductID] = e.stockOf(l.ProductID)
				}
			}
		}
		if op.UserID != 0 {
			var n string
			e.must(fixDB.Owner.QueryRow(context.Background(), "SELECT name FROM users WHERE id = $1", op.UserID).Scan(&n))
			names[op.UserID] = n
		}
	}
	want := cash.BuildReport(cash.ReportInput{Opening: money.Tiyn(sh.OpeningBalance), Operations: ops, Stock: stockLeft, Names: names})
	if !reflect.DeepEqual(normalize(got), normalize(want)) {
		t.Fatalf("SQL report differs from cash.BuildReport:\n got %+v\nwant %+v", got, want)
	}
}

func normalize(r cash.Report) cash.Report {
	if len(r.Items) == 0 {
		r.Items = nil
	}
	if len(r.Staff) == 0 {
		r.Staff = nil
	}
	return r
}

func TestGoldenStaffAPI(t *testing.T) {
	t.Run("GS-277", func(t *testing.T) {
		e := newEnv(t)
		e.topUpBonus(true, [2]float64{1000, 10})
		cashier := e.user("Кассир Асель")
		guest := e.customer("Гость", 0, 0)
		cola := e.product("Cola", 35000, 10)

		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			sh, _, err := svc.CurrentShift(ctx, q, e.club)
			eq(t, "no shift", sh == nil, true)
			return err
		}))
		_, err := e.topUp(cashier, guest, cash.FieldMain, "2000", cash.PayCard)
		wantCashErr(t, err, "amount", cash.MsgOpenShiftFirst)

		sh := e.open(cashier, "5000")
		top := e.mustTopUp(cashier, guest, cash.FieldMain, "2000", cash.PayCard)
		eq(t, "bonus", top.Bonus.Tenge(), "200.00")
		eq(t, "balance", top.Balance.NewBalance.Tenge(), "2000.00")
		eq(t, "bonus_balance", top.Balance.NewBonusBalance.Tenge(), "200.00")

		sale := e.mustSale(cashier, prod(cola, 2))
		eq(t, "sale amount", money.Tiyn(sale.Transaction.Amount).Tenge(), "700.00")
		eq(t, "line total", sale.Sale.Lines[0].Total.Tenge(), "700.00")

		e.mustTransact(cashier, cash.Expense, "100", "Вода")
		_, err = e.transact(cashier, cash.Expense, "99999", "Много")
		wantCashErr(t, err, "amount", cash.MsgNotEnoughCash)

		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			cur, totals, err := svc.CurrentShift(ctx, q, e.club)
			eq(t, "shift", cur.ID, sh.ID)
			eq(t, "income", totals.Income.Tenge(), "2700.00")
			eq(t, "expense", totals.Expense.Tenge(), "100.00")
			eq(t, "balance", totals.Balance().Tenge(), "7600.00")
			return err
		}))
		x := e.report(cash.ReportX, 0, "Кассир Асель")
		eq(t, "items.0.name", x.Report.Items[0].Name, "Cola")
		eq(t, "transactions", e.count("SELECT count(*) FROM cash_transactions WHERE cash_shift_id = $1", sh.ID), 3)

		closed, err := e.close(cashier, sh.ID, false) // приложение сотрудника — без печати Z
		e.must(err)
		eq(t, "closing", money.Tiyn(*closed.Shift.ClosingBalance).Tenge(), "7600.00")
		z := e.report(cash.ReportZ, sh.ID, "Кассир Асель")
		eq(t, "staff.0.name", z.Report.Staff[0].Name, "Кассир Асель")
		e.must(e.tx(func(ctx context.Context, q *store.Queries) error {
			cur, _, err := svc.CurrentShift(ctx, q, e.club)
			eq(t, "shift closed", cur == nil, true)
			return err
		}))
		e.checkLedger(760000)
	})
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// Решение CSQ-9/PD-6 (docs/DECISIONS.md): описание гостевой сессии с длинным списком ПК записывается целиком
// (cash_transactions.description — text; в старом varchar(255) — ошибка MySQL 500).
func TestGuestSessionLongDescription(t *testing.T) {
	e := newEnv(t)
	owner := e.user("Владелец")
	e.open(owner, "0")
	pcs := make([]string, 0, 120)
	for i := 1; i <= 120; i++ {
		pcs = append(pcs, "PC-"+strconv.Itoa(i))
	}
	description := "Гостевая сессия: 60 минут, ПК " + strings.Join(pcs, ", ") + " (Наличные)"
	if n := utf8.RuneCountInString(description); n <= 255 {
		t.Fatalf("description is too short for the test: %d", n)
	}
	rec, err := e.guest(owner, 6000000, description, cash.PayCash, "guest-long")
	e.must(err)
	eq(t, "stored", e.count("SELECT count(*) FROM cash_transactions WHERE club_id = $1 AND id = $2 AND description = $3",
		e.club, rec.Transaction.ID, description), 1)
	eq(t, "returned", rec.Transaction.Description, description)
}

// Решение Q-003 (docs/DECISIONS.md): новые чеки — v2 (тиыны, "v": 2); перенесённые чеки v1 (тенге) в той же
// смене читаются X/Z-отчётом, чеком и возвратом так же, как новые.
func TestReceiptV1AndV2InOneShift(t *testing.T) {
	e := newEnv(t)
	owner := e.user("Владелец")
	sh := e.open(owner, "0")
	cola := e.product("Cola", 70050, 10)
	chips := e.product("Chips", 30000, 10)

	sale := e.mustSale(owner, prod(cola, 2))
	eq(t, "v2 written", e.count(`SELECT count(*) FROM cash_transactions WHERE club_id = $1 AND id = $2
		AND receipt_data->>'v' = '2' AND receipt_data->'items'->0->>'price_tiyn' = '70050'`, e.club, sale.Transaction.ID), 1)

	// Чек старого формата (как его писал Laravel): 2 × 300.00 ₸; без ledger — в журнале кассы его нет,
	// поэтому инварианты кассы здесь не проверяются.
	legacy := e.count(`INSERT INTO cash_transactions (club_id, cash_shift_id, user_id, type, amount, description, receipt_data, created_at, updated_at)
		VALUES ($1, $2, $3, 'income', 60000, 'Продажа POS', $4::jsonb, now(), now()) RETURNING id`,
		e.club, sh.ID, owner, `{"items":[{"product_id":`+strconv.FormatInt(chips, 10)+`,"name":"Chips","quantity":2,"price":300.0,"total":600.0}]}`)
	_, err := fixDB.Owner.Exec(context.Background(), "UPDATE products SET quantity = 8 WHERE id = $1", chips)
	e.must(err)

	x := e.report(cash.ReportX, 0, "Владелец")
	eq(t, "items", len(x.Report.Items), 2)
	eq(t, "cola first (больше сумма)", x.Report.Items[0].Name+"/"+strconv.FormatInt(int64(x.Report.Items[0].Amount), 10), "Cola/140100")
	eq(t, "chips v1", x.Report.Items[1].Name+"/"+strconv.FormatInt(int64(x.Report.Items[1].Amount), 10), "Chips/60000")
	eq(t, "items total", x.Report.ItemsTotal, 200100)

	// Возврат чека v1: товары — на склад, деньги — из кассы.
	ref, err := e.refund(owner, legacy)
	e.must(err)
	eq(t, "refund amount", ref.Refund.Amount, int64(60000))
	eq(t, "chips back", e.stockOf(chips), 10)
	ref2, err := e.refund(owner, sale.Transaction.ID)
	e.must(err)
	eq(t, "refund v2", ref2.Refund.Amount, int64(140100))
	eq(t, "cola back", e.stockOf(cola), 10)
}
