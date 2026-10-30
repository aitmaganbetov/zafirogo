package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// RefundParams — возврат прихода по чеку. Право cash.delete проверяет вызывающий (403, cash.MsgNoRefundRight).
type RefundParams struct {
	ClubID   int64
	IncomeID int64
	Actor    Actor
}

// RefundResult — записанный возврат.
type RefundResult struct {
	Refund    store.CashTransaction // расход-возврат
	Kind      cash.RefundKind
	Movements []store.StockMovement      // товары, вернувшиеся на склад (продажа)
	TopUp     *billing.RefundTopUpResult // балансы клиента после возврата пополнения
	Ledger    *ledger.Result             // pos_refund (nil — нулевая сумма или пополнение: см. TopUp.Ledger)
}

// Refund — возврат прихода открытой смены (BR-055): продажа возвращает товары на склад, пополнение снимает сумму
// (и оставшийся бонус за него) с баланса клиента; деньги уходят из кассы расходом смены.
//
// Laravel: app/Services/CashRegisterService.php refund() (~стр. 150–205): открытая смена FOR UPDATE; приход FOR UPDATE
// (findOrFail); проверки по порядку (cash.CheckRefund): вид возврата, смена прихода открыта, ещё не возвращён, в кассе
// хватает (float, CQ-3); продажа — returnToStock() по строкам чека (товар FOR UPDATE по id или, в старых чеках, по имени;
// удалённый/ненайденный — пропуск; услуги — пропуск); пополнение — баланс FOR UPDATE, «Клиент уже потратил часть
// пополнения…», −amount, −min(bonus, bonus_balance) (billing.RefundTopUp); расход {refund_of_id, «Возврат по чеку №id»}.
// Проводки: pos_refund — cash_drawer +S, revenue_pos −S; top_up_refund — billing.RefundTopUp.
func (s *Service) Refund(ctx context.Context, q *store.Queries, p RefundParams) (RefundResult, error) {
	shiftRow, err := LockOpenShift(ctx, q, p.ClubID)
	if err != nil {
		return RefundResult{}, err
	}
	incomeRow, err := q.LockCashTransaction(ctx, store.LockCashTransactionParams{ClubID: p.ClubID, ID: p.IncomeID})
	if errors.Is(err, pgx.ErrNoRows) {
		return RefundResult{}, ErrNotFound
	}
	if err != nil {
		return RefundResult{}, fmt.Errorf("cash: lock income: %w", err)
	}
	income, err := OperationOf(incomeRow)
	if err != nil {
		return RefundResult{}, err
	}
	refunded := true
	if _, err := q.GetCashRefundOf(ctx, store.GetCashRefundOfParams{ClubID: p.ClubID, RefundOfID: &incomeRow.ID}); errors.Is(err, pgx.ErrNoRows) {
		refunded = false
	} else if err != nil {
		return RefundResult{}, fmt.Errorf("cash: refund of: %w", err)
	}
	totals, err := shiftTotals(ctx, q, p.ClubID, shiftRow)
	if err != nil {
		return RefundResult{}, err
	}
	shift := ShiftOf(shiftRow)
	kind, err := cash.CheckRefund(income, shift, refunded, totals)
	if err != nil {
		return RefundResult{}, err
	}

	out := RefundResult{Kind: kind}
	now := s.now()
	if kind == cash.RefundSale {
		for _, ret := range cash.SaleStockReturns(income.Receipt.Items) {
			mv, product, ok, err := returnToStock(ctx, q, p.ClubID, p.Actor.UserID, ret, now)
			if err != nil {
				return RefundResult{}, err
			}
			if ok {
				out.Movements = append(out.Movements, mv)
				if err := stockMoved(ctx, q, p.ClubID, p.Actor.UserID, product, mv, false); err != nil {
					return RefundResult{}, err
				}
			}
		}
	}
	ref := cash.RefundOperation(income, shift, p.Actor.UserID)
	out.Refund, err = s.insertTransaction(ctx, q, p.ClubID, newTransaction{
		ShiftID: ref.ShiftID, RefundOfID: ref.RefundOfID, UserID: ref.UserID, Type: ref.Type, Amount: ref.Amount,
		Description: ref.Description,
	})
	if err != nil {
		return RefundResult{}, err
	}

	incomeKey := strconv.FormatInt(income.ID, 10)
	details := map[string]any{"amount": int64(income.Amount), "receipt": income.ID, "kind": string(kind)}
	// details журнала — как в старом коде: сумма float, номер чека.
	journal := map[string]any{"amount": billingsvc.PHPFloat(income.Amount), "receipt": income.ID}
	switch kind {
	case cash.RefundSale:
		// Решение CSQ-6 (docs/DECISIONS.md): возврат ссылается на исходную операцию pos_sale.
		original, err := ledgerTxOf(ctx, q, p.ClubID, income.ID, ledger.KindPOSSale)
		if err != nil {
			return RefundResult{}, err
		}
		if out.Ledger, err = postDrawer(ctx, q, p.ClubID, ledger.KindPOSRefund, "pos_refund:"+incomeKey, -income.Amount,
			ledger.AccountRevenuePOS, ledger.Refs{CashShiftID: shift.ID, CashTransactionID: out.Refund.ID, ReversesTransactionID: original},
			nil, p.Actor); err != nil {
			return RefundResult{}, err
		}
	case cash.RefundTopUp:
		// Решение CSQ-6 (docs/DECISIONS.md): так же и возврат пополнения — на исходную операцию top_up.
		original, err := ledgerTxOf(ctx, q, p.ClubID, income.ID, ledger.KindTopUp)
		if err != nil {
			return RefundResult{}, err
		}
		top := income.Receipt.TopUp
		field := billing.FieldMain
		if top.Field == cash.FieldBonus {
			field = billing.FieldBonus
		}
		res, err := billing.RefundTopUp(ctx, q, billing.RefundTopUpParams{
			ClubID: p.ClubID, UserID: top.UserID, Field: field, Amount: income.Amount, Bonus: top.Bonus,
			IncomeCashTransactionID: income.ID, CashShiftID: shift.ID, RefundCashTransactionID: out.Refund.ID,
			ReversesTransactionID: original, Actor: p.Actor,
		})
		var be *billing.BalanceError
		if errors.As(err, &be) {
			return RefundResult{}, &cash.Error{Field: be.Field, Message: be.Message, Params: be.Params}
		}
		if err != nil {
			return RefundResult{}, err
		}
		out.TopUp = &res
		details["balance_field"], details["new_balance"], details["bonus_taken_back"] = string(field), int64(res.NewBalance), int64(res.BonusTakenBack)
		// Laravel: refund() (стр. 187–190) — журнал user.balance_refunded, если клиент есть.
		journal["balance_field"], journal["new_balance"], journal["bonus_taken_back"] =
			string(field), billingsvc.PHPFloat(res.NewBalance), billingsvc.PHPFloat(res.BonusTakenBack)
		names, err := userNames(ctx, q, p.ClubID, top.UserID)
		if err != nil {
			return RefundResult{}, err
		}
		if name, ok := names[top.UserID]; ok {
			if err := record(ctx, q, activity.Entry{
				ClubID: p.ClubID, Event: "user.balance_refunded", Description: "Возврат пополнения: " + name,
				UserID: p.Actor.UserID, SubjectType: billingsvc.SubjectUser, SubjectID: top.UserID, Details: journal,
			}); err != nil {
				return RefundResult{}, err
			}
		}
		if err := s.balanceChanged(ctx, q, p.ClubID, top.UserID); err != nil {
			return RefundResult{}, err
		}
	}
	// Laravel: refund() (стр. 200) — журнал cash.refund.
	if err := record(ctx, q, activity.Entry{
		ClubID: p.ClubID, Event: "cash.refund", Description: out.Refund.Description, UserID: p.Actor.UserID,
		SubjectType: billingsvc.SubjectCashTransaction, SubjectID: out.Refund.ID, Details: journal,
	}); err != nil {
		return RefundResult{}, err
	}
	if err := cashTransactionEvent(ctx, q, p.ClubID, out.Refund, txRefund); err != nil {
		return RefundResult{}, err
	}
	return out, audit(ctx, q, p.ClubID, p.Actor, "cash.refund", "cash_transaction", out.Refund.ID, out.Ledger, details)
}

// returnToStock — CashRegisterService::returnToStock(): товар по product_id или (старый чек) по имени, FOR UPDATE;
// не найден или удалён — пропуск (ok = false); иначе quantity +=, движение receipt «Возврат POS».
func returnToStock(ctx context.Context, q *store.Queries, clubID, userID int64, ret cash.StockReturn, now time.Time) (store.StockMovement, store.Product, bool, error) {
	var (
		product store.Product
		err     error
	)
	if ret.ProductID != 0 {
		product, err = stock.LockProduct(ctx, q, clubID, ret.ProductID)
		if errors.Is(err, stock.ErrProductNotFound) {
			return store.StockMovement{}, product, false, nil
		}
	} else {
		product, err = q.LockProductByName(ctx, store.LockProductByNameParams{ClubID: clubID, Name: ret.Name})
		if errors.Is(err, pgx.ErrNoRows) {
			return store.StockMovement{}, product, false, nil
		}
	}
	if err != nil {
		return store.StockMovement{}, product, false, fmt.Errorf("cash: return to stock: %w", err)
	}
	m, err := stock.Adjust(product.ID, product.Quantity, stock.Receipt, ret.Quantity, 0, cash.RefundNote)
	if err != nil {
		return store.StockMovement{}, product, false, err
	}
	mv, err := stock.ApplyMovement(ctx, q, clubID, userID, m, now)
	return mv, product, err == nil, err
}

// ledgerTxOf — id ledger-операции вида kind кассового документа cashTransactionID (0 — нет: нулевая сумма,
// перенесённые данные). Решение CSQ-6 (docs/DECISIONS.md).
func ledgerTxOf(ctx context.Context, q *store.Queries, clubID, cashTransactionID int64, kind ledger.Kind) (int64, error) {
	row, err := q.GetLedgerTransactionByCashTransaction(ctx, store.GetLedgerTransactionByCashTransactionParams{
		ClubID: clubID, CashTransactionID: &cashTransactionID, Kind: string(kind),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("cash: ledger operation of %d: %w", cashTransactionID, err)
	}
	return row.ID, nil
}
