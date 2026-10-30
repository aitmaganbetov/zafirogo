package service

import (
	"context"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// TransactParams — приход или расход кассы вручную (StoreCashTransactionRequest). Amount — cash.ParseTransactionAmount.
type TransactParams struct {
	ClubID         int64
	Type           cash.Type
	Amount         cash.TransactionAmount
	Description    string
	IdempotencyKey string
	Actor          Actor
}

// TransactResult — записанная операция.
type TransactResult struct {
	Transaction store.CashTransaction
	Ledger      *ledger.Result
	Replayed    bool
}

// Transact — приход/расход кассы (BR-054).
//
// Laravel: app/Services/CashRegisterService.php transact() (~стр. 229–253): открытая смена FOR UPDATE (нет → «Сначала
// откройте смену.», поле amount); расход больше остатка смены (сравнение во float, CQ-3) → «В кассе недостаточно средств.»;
// create($data + user_id). Такой приход — «Прочий приход» в отчёте, вернуть его нельзя.
// Проводки: cash_income — cash_drawer −S, cash_other +S; cash_expense — cash_drawer +S, cash_other −S.
func (s *Service) Transact(ctx context.Context, q *store.Queries, p TransactParams) (TransactResult, error) {
	shiftRow, err := LockOpenShift(ctx, q, p.ClubID)
	if err != nil {
		return TransactResult{}, err
	}
	if prev, err := findByKey(ctx, q, p.ClubID, p.IdempotencyKey); err != nil || prev != nil {
		if err != nil {
			return TransactResult{}, err
		}
		if prev.Type != string(p.Type) || prev.Amount != int64(p.Amount.Amount) || prev.Description != p.Description ||
			prev.IncomeKind != nil || prev.RefundOfID != nil || prev.ReceiptData != nil {
			return TransactResult{}, ErrIdempotencyConflict
		}
		return TransactResult{Transaction: *prev, Replayed: true}, nil
	}
	totals, err := shiftTotals(ctx, q, p.ClubID, shiftRow)
	if err != nil {
		return TransactResult{}, err
	}
	shift := ShiftOf(shiftRow)
	op, err := cash.PlanTransaction(shift, totals, p.Type, p.Amount, p.Description, p.Actor.UserID)
	if err != nil {
		return TransactResult{}, err
	}
	row, err := s.insertTransaction(ctx, q, p.ClubID, newTransaction{
		ShiftID: op.ShiftID, UserID: op.UserID, Type: op.Type, Amount: op.Amount, Description: op.Description,
		Key: p.IdempotencyKey,
	})
	if err != nil {
		return TransactResult{}, err
	}
	kind, drawerIn, action := ledger.KindCashIncome, op.Amount, "cash.income"
	if op.Type == cash.Expense {
		kind, drawerIn, action = ledger.KindCashExpense, -op.Amount, "cash.expense"
	}
	res, err := postDrawer(ctx, q, p.ClubID, kind, ledgerKey(p.IdempotencyKey, row.ID), drawerIn, ledger.AccountCashOther,
		ledger.Refs{CashShiftID: op.ShiftID, CashTransactionID: row.ID}, nil, p.Actor)
	if err != nil {
		return TransactResult{}, err
	}
	return TransactResult{Transaction: row, Ledger: res}, audit(ctx, q, p.ClubID, p.Actor, action, "cash_transaction", row.ID, res,
		map[string]any{"amount": int64(op.Amount), "type": string(op.Type)})
}
