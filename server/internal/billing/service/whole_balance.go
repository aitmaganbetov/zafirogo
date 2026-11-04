package service

import (
	"context"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Операции billing_requests: «на весь баланс» и автобиллинг — свои операции, чтобы ключ одной не совпал с телом другой.
const (
	opWholeBalance = "minutes_whole_balance"
	opAutoBilling  = "minutes_auto_billing"
)

// ErrWholeBalanceDisabled — клуб не разрешил «игру на весь баланс» (finances.whole_balance; кнопки в старом агенте
// не было — renderWholeBalance() скрывал её, текст — GuestFeatures::ensure()).
var ErrWholeBalanceDisabled = &Error{Code: "whole_balance_disabled", Field: "minutes", Message: "Эта возможность отключена в клубе."}

// WholeBalanceRequest — «играть на весь баланс» или шаг автобиллинга: минуты считает сервер.
type WholeBalanceRequest struct {
	ClubID         int64
	UserID         int64
	ComputerID     int64
	IdempotencyKey string
	Actor          Actor
	// AutoBilling — покупка автобиллинга (pricing.AutoBilling): не требует finances.whole_balance, а
	// MinutesHint — минуты решения автобиллинга (пересчитываются по балансу под блокировкой; берётся меньшее).
	AutoBilling bool
	MinutesHint int64
}

// PurchaseWholeBalance — покупка минут на весь основной баланс (решение Q-008: «весь баланс» и автобиллинг
// считает сервер). Минуты — pricing.WholeBalanceMinutes (автобиллинг — pricing.AffordableMinutes) по основному
// балансу ПОД блокировкой баланса, поэтому параллельное списание не превратит покупку в «недостаточно средств»;
// дальше — обычная покупка минут с основного баланса (PurchaseMinutes: тот же ledger minutes_purchase, журнал
// time.purchased, сессия, уведомления агенту).
//
// Источник: WindowsAgent/Frontend/main.js affordableMinutes(), renderWholeBalance(), обработчик minuteWholeBuy и
// startAutoBilling() → purchaseMinutes(minutes, 'main') (BR-022, BR-023; GS-053, GS-055).
func (s *Service) PurchaseWholeBalance(ctx context.Context, req WholeBalanceRequest) (PurchaseResult, error) {
	op := opWholeBalance
	if req.AutoBilling {
		op = opAutoBilling
	}
	mreq := MinutesPurchaseRequest{
		ClubID: req.ClubID, UserID: req.UserID, ComputerID: req.ComputerID, Balance: pricing.ChoiceMain,
		IdempotencyKey: req.IdempotencyKey, Actor: req.Actor,
	}
	body := struct {
		UserID, ComputerID int64
		Auto               bool
		Hint               int64
	}{req.UserID, req.ComputerID, req.AutoBilling, req.MinutesHint}
	return s.purchaseMinutes(ctx, mreq, op, body, func(zonePrice *money.Tiyn, rules pricing.PaymentRules, discount float64, b pricing.Balances) (int64, error) {
		if !req.AutoBilling && !rules.WholeBalance {
			return 0, ErrWholeBalanceDisabled
		}
		if zonePrice == nil {
			return 1, nil // QuoteMinutesPurchase: ErrNoHourlyPrice, как у покупки минут
		}
		o, err := pricing.MinuteOfferFor(zonePrice, rules, discount)
		if err != nil {
			return 0, err
		}
		minutes := pricing.AffordableMinutes(o, b.Main)
		if !req.AutoBilling {
			minutes = pricing.WholeBalanceMinutes(o, b.Main)
		} else if req.MinutesHint > 0 {
			minutes = min(minutes, req.MinutesHint)
		}
		if minutes < 1 {
			return 0, pricing.ErrInsufficientFunds
		}
		return minutes, nil
	})
}
