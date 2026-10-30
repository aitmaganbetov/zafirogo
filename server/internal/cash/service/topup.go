package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// TopUpParams — пополнение баланса клиента на кассе (StoreBalanceTopUpRequest). Amount — cash.ParseTopUpAmount.
type TopUpParams struct {
	ClubID         int64
	UserID         int64
	Field          cash.BalanceField
	Amount         cash.TopUpAmount
	PaymentMethod  cash.PaymentMethod
	IdempotencyKey string
	Actor          Actor
}

// TopUpResult — пополнение: кассовая операция, бонус и балансы клиента после операции.
type TopUpResult struct {
	Transaction  store.CashTransaction
	Bonus        money.Tiyn // бонус за пополнение (flash «Бонус за пополнение: :amount.», API bonus)
	BonusPercent float64    // процент уровня (журнал bonus_percent); при повторе — 0
	Balance      billing.TopUpResult
	Replayed     bool
}

// TopUp — пополнение основного или бонусного баланса клиента на кассе (BR-053) с бонусом за пополнение (BR-041).
//
// Laravel: app/Http/Requests/StoreBalanceTopUpRequest.php (user_id — член клуба, balance_type, payment_method);
// app/Services/CashRegisterService.php topUpBalance() (~стр. 100–143): открытая смена FOR UPDATE (нет → «Сначала откройте
// смену.», поле amount); member_balances FOR UPDATE firstOrFail (404); баланс += amount; бонус только для основного
// баланса — TopUpBonus::for((float) amount) по настройкам клуба (finances.topup_bonus_*), от (int) round($amount*100)
// (BonusBase, CQ-4); приход смены {amount, «Пополнение :balance: :user (:payment)», receipt_data.top_up}.
// Способ оплаты — в описании и (Q-013) в payment_method. Проводки — billing.TopUp.
func (s *Service) TopUp(ctx context.Context, q *store.Queries, p TopUpParams) (TopUpResult, error) {
	// Форма (порядок полей запроса): user_id — член клуба, amount, balance_type, payment_method.
	names, err := userNames(ctx, q, p.ClubID, p.UserID)
	if err != nil {
		return TopUpResult{}, err
	}
	userName, member := names[p.UserID]
	switch {
	case !member:
		return TopUpResult{}, &cash.Error{Field: "user_id", Rule: cash.RuleIn}
	case p.Amount.Amount <= 0 || p.Amount.Amount > cash.MaxTopUp:
		return TopUpResult{}, &cash.Error{Field: "amount", Rule: cash.RuleGt}
	case !p.Field.Valid():
		return TopUpResult{}, &cash.Error{Field: "balance_type", Rule: cash.RuleIn}
	case !p.PaymentMethod.Valid():
		return TopUpResult{}, &cash.Error{Field: "payment_method", Rule: cash.RuleIn}
	}

	shiftRow, err := LockOpenShift(ctx, q, p.ClubID)
	if err != nil {
		return TopUpResult{}, err
	}
	billingParams := billing.TopUpParams{
		ClubID: p.ClubID, UserID: p.UserID, Field: billing.BalanceField(p.Field), Amount: p.Amount.Amount,
		BonusBase: p.Amount.BonusBase, PaymentMethod: string(p.PaymentMethod), Actor: p.Actor,
	}
	if prev, err := findByKey(ctx, q, p.ClubID, p.IdempotencyKey); err != nil || prev != nil {
		if err != nil {
			return TopUpResult{}, err
		}
		return s.replayTopUp(ctx, q, *prev, p, billingParams)
	}
	shift := ShiftOf(shiftRow)
	if err := cash.RequireOpen(shift, "amount"); err != nil {
		return TopUpResult{}, err
	}
	// MemberBalance::query()->where('user_id', …)->lockForUpdate()->firstOrFail()
	if _, err := ledger.LockBalances(ctx, q, p.ClubID, p.UserID); err != nil {
		if errors.Is(err, ledger.ErrBalanceNotFound) {
			return TopUpResult{}, ErrNotFound
		}
		return TopUpResult{}, err
	}

	var (
		bonusFn cash.BonusFunc
		percent float64
	)
	if p.Field == cash.FieldMain {
		raw, err := q.GetClubSettingsForCash(ctx, p.ClubID)
		if err != nil {
			return TopUpResult{}, fmt.Errorf("cash: club settings: %w", err)
		}
		settings, err := bonus.ParseSettings(raw)
		if err != nil {
			return TopUpResult{}, err
		}
		levels := settings.TopUpLevelsInForce()
		// Laravel: app/Services/TopUpBonus.php TopUpBonus::for() — intdiv($cents * (int) round($percent * 100) + 5000, 10000)
		bonusFn = func(base money.Tiyn) (money.Tiyn, error) {
			b, err := bonus.TopUpForCents(levels, int64(base))
			percent = b.Percent
			return b.Cents, err
		}
	}
	plan, err := cash.PlanTopUp(shift, p.UserID, userName, p.Field, p.Amount, p.PaymentMethod, bonusFn)
	if err != nil {
		return TopUpResult{}, err
	}
	row, err := s.insertTransaction(ctx, q, p.ClubID, newTransaction{
		ShiftID: shift.ID, UserID: p.Actor.UserID, Type: cash.Income, Amount: plan.Amount, Description: plan.Description,
		Receipt: &plan.Receipt, PaymentMethod: plan.PaymentMethod, Kind: plan.Kind, Key: p.IdempotencyKey,
	})
	if err != nil {
		return TopUpResult{}, err
	}
	billingParams.CashShiftID, billingParams.CashTransactionID = shift.ID, row.ID
	billingParams.IdempotencyKey = ledgerKey(p.IdempotencyKey, row.ID)
	planned := plan.Bonus
	billingParams.Bonus = func(base money.Tiyn) (money.Tiyn, error) {
		if bonusFn == nil {
			return 0, nil
		}
		return bonusFn(base)
	}
	res, err := billing.TopUp(ctx, q, billingParams)
	if err != nil {
		return TopUpResult{}, err
	}
	if res.Bonus != planned {
		return TopUpResult{}, fmt.Errorf("cash: top-up bonus %s differs from the receipt %s", res.Bonus, planned)
	}
	return TopUpResult{Transaction: row, Bonus: planned, BonusPercent: percent, Balance: res}, nil
}

// replayTopUp — повтор пополнения с тем же ключом: billing.TopUp с теми же параметрами возвращает записанную операцию.
func (s *Service) replayTopUp(ctx context.Context, q *store.Queries, prev store.CashTransaction, p TopUpParams, bp billing.TopUpParams) (TopUpResult, error) {
	op, err := OperationOf(prev)
	if err != nil {
		return TopUpResult{}, err
	}
	if op.Kind != cash.KindTopUp || op.Receipt == nil || op.Receipt.TopUp == nil ||
		op.Receipt.TopUp.UserID != p.UserID || op.Receipt.TopUp.Field != p.Field ||
		op.Amount != p.Amount.Amount || op.PaymentMethod != p.PaymentMethod {
		return TopUpResult{}, ErrIdempotencyConflict
	}
	bp.CashShiftID, bp.CashTransactionID = prev.CashShiftID, prev.ID
	bp.IdempotencyKey = ledgerKey(p.IdempotencyKey, prev.ID)
	res, err := billing.TopUp(ctx, q, bp)
	if err != nil {
		return TopUpResult{}, err
	}
	if !res.Ledger.Replayed {
		return TopUpResult{}, fmt.Errorf("cash: top-up %d has no ledger operation", prev.ID)
	}
	return TopUpResult{Transaction: prev, Bonus: op.Receipt.TopUp.Bonus, Balance: res, Replayed: true}, nil
}
