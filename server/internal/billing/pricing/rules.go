package pricing

import "github.com/aitmaganbetov/zafirogo/server/internal/money"

// DiscountScope — где действует скидка клиента (finances.discount_scope, BR-010).
type DiscountScope string

// Области скидки — PaymentRules::DISCOUNT_SCOPES.
const (
	ScopeTariffs DiscountScope = "tariffs" // тарифы с «Применять скидку» (по умолчанию)
	ScopeHourly  DiscountScope = "hourly"  // только поминутное время по цене часа зоны
	ScopeAll     DiscountScope = "all"     // и то и другое
)

// Значения по умолчанию на чтении (PaymentRules, BR-003).
const (
	DefaultBonusMaxPercent         int64 = 100
	DefaultAutoBillingPauseMinutes int64 = 5
)

// PaymentRules — правила оплаты времени клуба (Settings → Finances), уже приведённые так, как их
// читает Laravel: app/Services/PaymentRules.php. Получать через RulesFromFinances (или
// DefaultPaymentRules); если заполнять поля руками, значения должны быть в тех же пределах
// (BonusMaxPercent 0…100, AutoBillingPauseMinutes 0…60, DiscountScope — одна из областей).
type PaymentRules struct {
	DiscountScope           DiscountScope
	BonusMaxPercent         int64 // до какого % цены платят бонусы (0 — бонусы время не оплачивают)
	MixedPayment            bool  // finances.allow_mixed_payment
	WholeBalance            bool  // finances.allow_whole_balance («игра на весь баланс»)
	AutoBilling             bool  // finances.auto_billing
	AutoBillingPauseMinutes int64 // finances.auto_billing_pause_minutes
}

// DefaultPaymentRules — правила клуба без настроек finances.
func DefaultPaymentRules() PaymentRules {
	return RulesFromFinances(nil)
}

// RulesFromFinances читает правила из сырого раздела finances, буквально как PaymentRules.php:
//
//	discountScope(): in_array($scope, DISCOUNT_SCOPES, true) ? $scope : 'tariffs'          (BR-010)
//	bonusMaxPercent(): is_numeric($p) ? max(0, min(100, (int) $p)) : 100                  (BR-018, GAP-009)
//	autoBillingEnabled(): (bool) data_get(..., false)                                      (BR-023)
//	autoBillingPauseMinutes(): is_numeric($m) ? max(0, min(60, (int) $m)) : 5             (BR-023, GAP-009)
//	wholeBalanceAllowed(): (bool) data_get(..., false)                                     (BR-022)
//	mixedPaymentAllowed(): (bool) data_get(..., false)                                     (BR-018)
//
// Laravel: app/Services/PaymentRules.php (стр. 17–70). Отсутствующий ключ и null дают одно и то же.
func RulesFromFinances(f RawFinances) PaymentRules {
	r := PaymentRules{
		DiscountScope:           ScopeTariffs,
		BonusMaxPercent:         DefaultBonusMaxPercent,
		AutoBillingPauseMinutes: DefaultAutoBillingPauseMinutes,
	}
	// Laravel: PaymentRules::discountScope() — строгое сравнение, только строки.
	if s, ok := f["discount_scope"].(string); ok {
		switch DiscountScope(s) {
		case ScopeTariffs, ScopeHourly, ScopeAll:
			r.DiscountScope = DiscountScope(s)
		}
	}
	// Laravel: PaymentRules::bonusMaxPercent()
	if v, ok := f["bonus_max_percent"]; ok && phpIsNumeric(v) {
		r.BonusMaxPercent = max(0, min(100, phpToInt(v)))
	}
	// Laravel: PaymentRules::autoBillingPauseMinutes()
	if v, ok := f["auto_billing_pause_minutes"]; ok && phpIsNumeric(v) {
		r.AutoBillingPauseMinutes = max(0, min(60, phpToInt(v)))
	}
	r.AutoBilling = phpToBool(f["auto_billing"])         // Laravel: PaymentRules::autoBillingEnabled()
	r.WholeBalance = phpToBool(f["allow_whole_balance"]) // Laravel: PaymentRules::wholeBalanceAllowed()
	r.MixedPayment = phpToBool(f["allow_mixed_payment"]) // Laravel: PaymentRules::mixedPaymentAllowed()
	return r
}

// DiscountOnTariffs — скидка действует на тарифы: область не hourly.
// Laravel: app/Services/PaymentRules.php discountOnTariffs() (BR-010).
func (r PaymentRules) DiscountOnTariffs() bool { return r.DiscountScope != ScopeHourly }

// DiscountOnHourly — скидка действует на поминутное время: область не tariffs.
// Laravel: app/Services/PaymentRules.php discountOnHourly() (BR-010).
func (r PaymentRules) DiscountOnHourly() bool { return r.DiscountScope != ScopeTariffs }

// BonusPercentOffered — какой % агент предлагает оплатить бонусами (0 — не спрашивает): без смешанной
// оплаты бонусы должны покрыть всю цену, поэтому лимит меньше 100 % ничего не предлагает.
// Laravel: app/Services/PaymentRules.php bonusPercentOffered() (BR-019).
func (r PaymentRules) BonusPercentOffered(mainBalanceOnly bool) int64 {
	percent := r.BonusMaxPercent
	if mainBalanceOnly {
		percent = 0
	}
	if percent < 100 && !r.MixedPayment {
		return 0
	}
	return percent
}

// BonusLimit — часть цены, которую могут оплатить бонусы: intdiv($price * bonusMaxPercent, 100), вниз.
// Laravel: app/Services/PaymentRules.php bonusLimitCents() (BR-018, GAP-008).
// |price| ≤ money.MaxAmount (произведение ≤ 1e17 помещается в int64).
func (r PaymentRules) BonusLimit(price money.Tiyn) money.Tiyn {
	return money.Tiyn(money.IntDiv(int64(price)*r.BonusMaxPercent, 100))
}
