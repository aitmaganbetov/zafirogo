package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
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
			mv, ok, err := returnToStock(ctx, q, p.ClubID, p.Actor.UserID, ret, now)
			if err != nil {
				return RefundResult{}, err
			}
			if ok {
				out.Movements = append(out.Movements, mv)
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
	switch kind {
	case cash.RefundSale:
		if out.Ledger, err = postDrawer(ctx, q, p.ClubID, ledger.KindPOSRefund, "pos_refund:"+incomeKey, -income.Amount,
			ledger.AccountRevenuePOS, ledger.Refs{CashShiftID: shift.ID, CashTransactionID: out.Refund.ID}, nil, p.Actor); err != nil {
			return RefundResult{}, err
		}
	case cash.RefundTopUp:
		top := income.Receipt.TopUp
		field := billing.FieldMain
		if top.Field == cash.FieldBonus {
			field = billing.FieldBonus
		}
		res, err := billing.RefundTopUp(ctx, q, billing.RefundTopUpParams{
			ClubID: p.ClubID, UserID: top.UserID, Field: field, Amount: income.Amount, Bonus: top.Bonus,
			IncomeCashTransactionID: income.ID, CashShiftID: shift.ID, RefundCashTransactionID: out.Refund.ID,
			Actor: p.Actor,
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
	}
	return out, audit(ctx, q, p.ClubID, p.Actor, "cash.refund", "cash_transaction", out.Refund.ID, out.Ledger, details)
}

// returnToStock — CashRegisterService::returnToStock(): товар по product_id или (старый чек) по имени, FOR UPDATE;
// не найден или удалён — пропуск (ok = false); иначе quantity +=, движение receipt «Возврат POS».
func returnToStock(ctx context.Context, q *store.Queries, clubID, userID int64, ret cash.StockReturn, now time.Time) (store.StockMovement, bool, error) {
	var (
		product store.Product
		err     error
	)
	if ret.ProductID != 0 {
		product, err = stock.LockProduct(ctx, q, clubID, ret.ProductID)
		if errors.Is(err, stock.ErrProductNotFound) {
			return store.StockMovement{}, false, nil
		}
	} else {
		product, err = q.LockProductByName(ctx, store.LockProductByNameParams{ClubID: clubID, Name: ret.Name})
		if errors.Is(err, pgx.ErrNoRows) {
			return store.StockMovement{}, false, nil
		}
	}
	if err != nil {
		return store.StockMovement{}, false, fmt.Errorf("cash: return to stock: %w", err)
	}
	m, err := stock.Adjust(product.ID, product.Quantity, stock.Receipt, ret.Quantity, 0, cash.RefundNote)
	if err != nil {
		return store.StockMovement{}, false, err
	}
	mv, err := stock.ApplyMovement(ctx, q, clubID, userID, m, now)
	return mv, err == nil, err
}
