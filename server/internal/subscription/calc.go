// Package subscription — оплата подписки платформы: клуб выставляет себе счёт (цена тарифа за ПК × число ПК × месяцы
// со скидкой), платит по ссылке Kaspi с номером счёта в комментарии, суперадмин отмечает оплату — подписка
// продлевается. Laravel: app/Services/SubscriptionBilling.php, app/Models/SubscriptionInvoice.php,
// app/Models/ClubSubscription.php (docs/business-rules.md BR-180…BR-184, BR-186).
//
// Деньги — int64 в тиынах, проценты — целые (скидка) и базисные пункты (процент реселлера). В старом коде эти
// формулы считались во float; здесь — целочисленно (решение UQ-7), совпадение с PHP 8.4 сверено фикстурой
// tools/php-oracle/gen-subscription-fixture.php (calc_test.go).
package subscription

import (
	"fmt"
	"time"
)

// Months — сроки, на которые выставляется счёт. Laravel: SubscriptionInvoice::MONTHS.
var Months = []int{1, 3, 6, 12}

// ValidMonths — Rule::in(SubscriptionInvoice::MONTHS).
func ValidMonths(months int) bool {
	for _, m := range Months {
		if m == months {
			return true
		}
	}
	return false
}

// Number — номер счёта, который клуб пишет в комментарии к платежу: «ZF-» + id с нулями до 5 знаков.
// Laravel: app/Models/SubscriptionInvoice.php SubscriptionInvoice::number().
func Number(id int64) string { return fmt.Sprintf("ZF-%05d", id) }

// Computers — ПК, за которые платит клуб: все зарегистрированные, минимум один.
// Laravel: app/Services/SubscriptionBilling.php SubscriptionBilling::computers().
func Computers(registered int64) int64 { return max(1, registered) }

// Amount — сумма счёта в тиынах: цена за ПК × ПК × месяцы × (100 − скидка) / 100, округление итога до целых тенге
// (половина — вверх). BR-180.
//
//	return round($pricePerComputer * $computers * $months * (100 - $discountPercent) / 100);
//
// Laravel: app/Services/SubscriptionBilling.php SubscriptionBilling::amount(). В PHP это float; в тиынах:
// ((P × C × M × (100 − d) + 5000) / 10000) × 100. При цене в целых тенге результат совпадает с PHP всегда; при цене
// с тиынами — тоже, кроме сумм ровно на «…,5 ₸», где float PHP иногда даёт на 1 ₸ меньше точного округления
// (docs/open-questions/upstream-billing.md UB-1).
func Amount(pricePerComputer, computers int64, months int, discountPercent int) int64 {
	return (pricePerComputer*computers*int64(months)*int64(100-discountPercent) + 5000) / 10000 * 100
}

// MonthlyPrice — цена тарифа за ПК в месяц со скидкой, тиыны (только показ, в базу не пишется). BR-182.
//
//	return $price === null ? null : round((float) $price * (100 - $this->discountPercent()) / 100, 2);
//
// Laravel: app/Models/ClubSubscription.php ClubSubscription::monthlyPrice(). В тиынах: (P × (100 − d) + 50) / 100.
func MonthlyPrice(price int64, discountPercent int) int64 {
	return (price*int64(100-discountPercent) + 50) / 100
}

// ResellerEarning — начисление реселлеру с оплаченного счёта, тиыны: сумма × процент (базисные пункты). BR-186.
//
//	round((float) $invoice->amount * (float) $invoice->reseller_percent / 100, 2)
//
// Laravel: app/Services/SubscriptionBilling.php SubscriptionBilling::markPaid(). В целых: (A × bp + 5000) / 10000.
func ResellerEarning(amount int64, resellerBP int) int64 {
	return (amount*int64(resellerBP) + 5000) / 10000
}

// OwnDiscount — индивидуальная скидка клуба, целые проценты: действует бессрочно (until == nil) или по последний
// день until включительно — до конца этого дня по часам loc; после — 0. BR-181.
//
//	return $this->discount_until === null || ! $this->discount_until->copy()->endOfDay()->isPast() ? $this->discount_percent : 0;
//
// Laravel: app/Models/ClubSubscription.php ClubSubscription::discountPercent(). until — календарная дата (колонка
// date); loc — часовой пояс клуба (UsesClubTimezone; GAP-055 — docs/open-questions/upstream-billing.md UB-2).
func OwnDiscount(percent int, until *time.Time, now time.Time, loc *time.Location) int {
	if until == nil {
		return percent
	}
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := until.Date()
	if !now.Before(time.Date(y, m, d+1, 0, 0, 0, 0, loc)) {
		return 0
	}
	return percent
}

// Discount — скидка клуба на подписку: своя или реселлера (пока он активен), какая больше; не складываются. BR-181.
//
//	return max($subscription?->discountPercent() ?? 0, $reseller?->discount_percent ?? 0);
//
// Laravel: app/Services/SubscriptionBilling.php SubscriptionBilling::discountFor().
func Discount(own, reseller int) int { return max(own, reseller) }

// AddMonthsNoOverflow — Carbon addMonthsNoOverflow(): тот же день через N месяцев, а если такого дня в месяце нет —
// его последний день (31 января + 1 месяц = 28/29 февраля). Время суток сохраняется; считается в UTC
// (config app.timezone старого проекта).
func AddMonthsNoOverflow(t time.Time, months int) time.Time {
	t = t.UTC()
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	if last := first.AddDate(0, 1, -1).Day(); d > last {
		d = last
	}
	return first.AddDate(0, 0, d-1)
}

// PeriodEnd — до какого момента оплаченный счёт продлевает подписку: N месяцев от конца текущего оплаченного
// периода (status = active) или пробного (trialing), но не раньше, чем от сейчас. Истёкшая, приостановленная,
// бессрочная активная и отсутствующая подписка — от сейчас. BR-183.
//
//	$from = collect([now(),
//	    $subscription->status === 'active' ? $subscription->current_period_ends_at : null,
//	    $subscription->status === 'trialing' ? $subscription->trial_ends_at : null,
//	])->filter()->max();
//	$until = $from->copy()->addMonthsNoOverflow($invoice->months);
//
// Laravel: app/Services/SubscriptionBilling.php SubscriptionBilling::markPaid().
func PeriodEnd(now time.Time, status string, trialEndsAt, currentPeriodEndsAt *time.Time, months int) time.Time {
	from := now
	switch {
	case status == "active" && currentPeriodEndsAt != nil && currentPeriodEndsAt.After(from):
		from = *currentPeriodEndsAt
	case status == "trialing" && trialEndsAt != nil && trialEndsAt.After(from):
		from = *trialEndsAt
	}
	return AddMonthsNoOverflow(from, months)
}
