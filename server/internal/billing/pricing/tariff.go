package pricing

import (
	"slices"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// PaymentMethod — tariffs.payment_method.
type PaymentMethod string

// Способы оплаты тарифа (StoreTariffRequest).
const (
	MainOrBonus     PaymentMethod = "main_or_bonus"
	MainBalanceOnly PaymentMethod = "main_balance_only"
)

// Tariff — поля тарифа, от которых зависят цена и доступность (таблица tariffs).
type Tariff struct {
	Price                money.Tiyn // tariffs.price
	DurationMinutes      int64
	PaymentMethod        PaymentMethod
	DiscountEnabled      bool
	IsActive             bool
	UsageLimitEnabled    bool
	UsageLimitMinutes    *int64 // NULL — нет значения
	QuantityLimitEnabled bool
	QuantityLimit        *int64
	UserRoles            []string // jsonb; nil/[] — без ограничения
	SalePeriods          WeeklyPeriods
	ValidityPeriods      WeeklyPeriods
	ZoneIDs              []int64 // зоны тарифа (club_zone_tariff); пусто — во всех зонах
}

// EffectiveMinutes — сколько минут даёт тариф: с лимитом использования — min(длительность, лимит).
// Laravel: app/Services/AgentTariffPurchaseService.php effectiveMinutes() (стр. 378–383) (BR-015).
func (t Tariff) EffectiveMinutes() int64 {
	if t.UsageLimitEnabled && t.UsageLimitMinutes != nil {
		return min(t.DurationMinutes, *t.UsageLimitMinutes)
	}
	return t.DurationMinutes
}

// UsageExpiresAt — когда сгорает неиспользованное время купленного тарифа: now + usage_limit_minutes,
// только у тарифа с лимитом использования; иначе nil.
// Laravel: app/Services/AgentTariffPurchaseService.php purchase() (стр. 81–86) (BR-015).
func (t Tariff) UsageExpiresAt(now time.Time) *time.Time {
	if !t.UsageLimitEnabled || t.UsageLimitMinutes == nil {
		return nil
	}
	at := now.Add(time.Duration(*t.UsageLimitMinutes) * time.Minute)
	return &at
}

// OnSale — сейчас (время клуба) в днях и часах продажи. Laravel: app/Models/Tariff.php onSale() (BR-016).
func (t Tariff) OnSale(now time.Time, loc *time.Location) bool {
	return t.SalePeriods.ContainsAt(now, loc)
}

// InValidityPeriod — сейчас (время клуба) в периоде действия. Laravel: app/Models/Tariff.php inValidityPeriod() (BR-016).
func (t Tariff) InValidityPeriod(now time.Time, loc *time.Location) bool {
	return t.ValidityPeriods.ContainsAt(now, loc)
}

// InZone — тариф действует в зоне: у тарифа нет зон или зона в списке. zoneID nil — ПК без зоны
// (club_zone_id NULL): только тарифы без зон (whereKey(null) ничего не находит).
// Laravel: AgentTariffPurchaseService::availableFor() (фильтр), purchase() (стр. 75–77), GuestSessions::priceFor().
func (t Tariff) InZone(zoneID *int64) bool {
	if len(t.ZoneIDs) == 0 {
		return true
	}
	return zoneID != nil && slices.Contains(t.ZoneIDs, *zoneID)
}

// Unavailable — почему тариф сейчас не продаётся клиенту в агенте (порядок проверок — как в isAvailable()).
type Unavailable string

// Причины недоступности.
const (
	Available       Unavailable = ""
	NotOnSale       Unavailable = "not_on_sale"      // вне дней/часов продажи
	SoldOut         Unavailable = "sold_out"         // лимит количества исчерпан
	RoleNotAllowed  Unavailable = "role_not_allowed" // роль клиента не в user_roles
	OutsideValidity Unavailable = "outside_validity" // вне периода действия
)

// Availability — доступность тарифа клиенту с ролью role (club_memberships.role):
//
//	!onSale() → нет
//	quantity_limit_enabled && quantity_limit !== null && $purchaseCount >= quantity_limit → нет
//	user_roles — непустой массив && !in_array($membership->role, user_roles, true) → нет
//	return inValidityPeriod()
//
// purchaseCount — число продаж тарифа в агенте за всё время (все клиенты клуба); кассовые гостевые
// продажи не считаются. Решение Q-007 (docs/DECISIONS.md) — счётчик считает вызывающий по таблице покупок,
// а не по журналу действий; здесь только сравнение.
// Активность тарифа (is_active) и удаление проверяет запрос списка, как в старом коде.
// Laravel: app/Services/AgentTariffPurchaseService.php isAvailable() (стр. 313–329) (BR-016, GAP-015).
func (t Tariff) Availability(role string, purchaseCount int64, now time.Time, loc *time.Location) Unavailable {
	if !t.OnSale(now, loc) {
		return NotOnSale
	}
	if t.QuantityLimitEnabled && t.QuantityLimit != nil && purchaseCount >= *t.QuantityLimit {
		return SoldOut
	}
	if len(t.UserRoles) > 0 && !slices.Contains(t.UserRoles, role) {
		return RoleNotAllowed
	}
	if !t.InValidityPeriod(now, loc) {
		return OutsideValidity
	}
	return Available
}

// TariffOffer — расчётная часть тарифа в списке агента (AgentTariffPurchaseService::representation()).
type TariffOffer struct {
	Minutes           int64      // effectiveMinutes()
	Price             money.Tiyn // priceInCents() — со скидкой
	OriginalPrice     money.Tiyn // tariffs.price
	BonusAllowed      bool       // bonusPercentOffered(main_balance_only) > 0
	BonusMaxPercent   int64      // bonusPercentOffered(main_balance_only)
	MixedPayment      bool
	UsageLimitMinutes *int64 // только при usage_limit_enabled
}

// Offer — тариф в списке агента.
// Laravel: app/Services/AgentTariffPurchaseService.php representation() (стр. 341–359) (BR-013, BR-019).
func (t Tariff) Offer(rules PaymentRules, discountPercent float64) (TariffOffer, error) {
	price, err := TariffPrice(t, rules, discountPercent)
	if err != nil {
		return TariffOffer{}, err
	}
	offered := rules.BonusPercentOffered(t.PaymentMethod == MainBalanceOnly)
	o := TariffOffer{
		Minutes:         t.EffectiveMinutes(),
		Price:           price,
		OriginalPrice:   t.Price,
		BonusAllowed:    offered > 0,
		BonusMaxPercent: offered,
		MixedPayment:    rules.MixedPayment,
	}
	if t.UsageLimitEnabled {
		o.UsageLimitMinutes = t.UsageLimitMinutes
	}
	return o, nil
}
