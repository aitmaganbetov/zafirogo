package pricing

import (
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// MinuteOffer — поминутная оплата на ПК клиента (ответ GuestMinutesController::show()).
type MinuteOffer struct {
	Available               bool       // у зоны ПК есть цена часа
	PricePerHour            money.Tiyn // hourlyPrice(): со скидкой, если она действует на поминутное время
	OriginalPricePerHour    money.Tiyn
	BonusMaxPercent         int64 // bonusPercentOffered(): 0 — агент не предлагает бонусы
	MixedPayment            bool
	MinMinutes, MaxMinutes  int64
	WholeBalance            bool // «игра на весь баланс»
	AutoBilling             bool
	AutoBillingPauseMinutes int64
}

// MinuteOfferFor — поминутная оплата для клиента. zonePricePerHour nil — у зоны ПК нет цены часа.
// Laravel: app/Http/Controllers/Api/GuestMinutesController.php show() (стр. 20–43) (BR-014, BR-019, BR-022, BR-023;
// GS-049, GS-053, GS-055).
func MinuteOfferFor(zonePricePerHour *money.Tiyn, rules PaymentRules, discountPercent float64) (MinuteOffer, error) {
	o := MinuteOffer{
		Available:               zonePricePerHour != nil,
		BonusMaxPercent:         rules.BonusPercentOffered(false),
		MixedPayment:            rules.MixedPayment,
		MinMinutes:              1,
		MaxMinutes:              MaxMinutes,
		WholeBalance:            rules.WholeBalance,
		AutoBilling:             rules.AutoBilling,
		AutoBillingPauseMinutes: rules.AutoBillingPauseMinutes,
	}
	if zonePricePerHour != nil {
		hour, err := HourlyPrice(*zonePricePerHour, rules, discountPercent)
		if err != nil {
			return MinuteOffer{}, err
		}
		o.PricePerHour, o.OriginalPricePerHour = hour, *zonePricePerHour
	}
	return o, nil
}

// AffordableMinutes — сколько минут оплачивает основной баланс по цене часа (для клиента — со скидкой):
//
//	if (!available || !(hourCents > 0) || !(balanceCents > 0)) return 0;
//	let minutes = Math.min(max_minutes || 720, Math.floor(balanceCents * 60 / hourCents));
//	while (minutes > 0 && minutesPrice(hourCents, minutes) > balanceCents) minutes--;
//
// Допущение Этапа 1: Q-008 — формула агента переносится на сервер, числа те же.
// Агент считал во float (Math.floor(balance*60/hour)). Пока balance*60 < 2^53 (баланс < 1,5e14 тиынов),
// числитель и знаменатель точны, а частное IEEE округляется до целого вверх, только если оно больше 2^53, —
// значит, целочисленное деление даёт то же число. Цикл всё равно не даёт цене превысить баланс.
// Источник: WindowsAgent/Frontend/main.js affordableMinutes() (~стр. 972–979) (BR-022, GS-053).
func AffordableMinutes(o MinuteOffer, mainBalance money.Tiyn) int64 {
	hour := int64(o.PricePerHour)
	balance := int64(mainBalance)
	if !o.Available || hour <= 0 || hour > int64(MaxPrice) || balance <= 0 || balance > int64(money.MaxAmount) {
		return 0
	}
	maxMinutes := o.MaxMinutes
	if maxMinutes <= 0 { // Number(minuteOffer.max_minutes) || 720
		maxMinutes = MaxMinutes
	}
	minutes := min(maxMinutes, money.IntDiv(balance*60, hour))
	for minutes > 0 && money.IntDiv(hour*minutes+30, 60) > balance {
		minutes--
	}
	return minutes
}

// WholeBalanceMinutes — «На весь баланс»: минуты, если клуб разрешил игру на весь баланс, иначе 0.
// Покупаются обычной покупкой минут с выбором main (QuoteMinutesPurchase с ChoiceMain).
// Источник: WindowsAgent/Frontend/main.js renderWholeBalance(), обработчик minuteWholeBuy (BR-022).
func WholeBalanceMinutes(o MinuteOffer, mainBalance money.Tiyn) int64 {
	if !o.WholeBalance {
		return 0
	}
	return AffordableMinutes(o, mainBalance)
}

// AutoBillingState — состояние клиента на ПК для автобиллинга (то, что агент держал в переменных).
type AutoBillingState struct {
	SignedIn         bool // userName !== null
	HasActiveSession bool
	// Клиент сам завершил сессию или её завершили в клубе: до нового входа не начинать (autoStopped).
	Stopped bool
	// Время тарифа вышло, клиент ещё в аккаунте: покупать сразу, без паузы (autoAfterTariff).
	AfterTariff bool
	// Начало паузы на выбор тарифа — первый момент «вошёл, сессии нет» (autoPauseStart); нулевое — ещё не начата.
	PauseStartedAt time.Time
}

// AutoBillingDecision — что делать сейчас.
type AutoBillingDecision struct {
	Waiting     bool      // автобиллинг ждёт (в это время не выводить клиента по бездействию)
	PauseEndsAt time.Time // когда кончится пауза (при Waiting и не AfterTariff)
	BuyMinutes  int64     // > 0 — купить столько минут с основного баланса сейчас
}

// AutoBilling — шаг автобиллинга (агент делал его раз в секунду):
//
//	autoBillingWaits = signedIn && !hasActiveSession && !autoStopped && auto_billing && available && affordableMinutes() > 0
//	нет — ничего (пауза сбрасывается); afterTariff — купить сразу;
//	иначе пауза auto_billing_pause_minutes от autoPauseStart, затем купить affordableMinutes() с основного баланса.
//
// Сброс PauseStartedAt (вышел из аккаунта, ушёл в «не ждёт») — забота вызывающего, как в autoBillingTick().
// Допущение Этапа 1: Q-008 — логика агента переносится на сервер.
// Источник: WindowsAgent/Frontend/main.js autoBillingOn/autoBillingWaits/startAutoBilling/autoBillingTick
// (~стр. 1001–1046) (BR-023, GS-055).
func AutoBilling(o MinuteOffer, st AutoBillingState, mainBalance money.Tiyn, now time.Time) AutoBillingDecision {
	minutes := AffordableMinutes(o, mainBalance)
	if !st.SignedIn || st.HasActiveSession || st.Stopped || !o.AutoBilling || !o.Available || minutes <= 0 {
		return AutoBillingDecision{}
	}
	if st.AfterTariff {
		return AutoBillingDecision{Waiting: true, BuyMinutes: minutes}
	}
	start := st.PauseStartedAt
	if start.IsZero() {
		start = now
	}
	ends := start.Add(time.Duration(max(0, o.AutoBillingPauseMinutes)) * time.Minute)
	if !now.Before(ends) { // left <= 0
		return AutoBillingDecision{Waiting: true, PauseEndsAt: ends, BuyMinutes: minutes}
	}
	return AutoBillingDecision{Waiting: true, PauseEndsAt: ends}
}
