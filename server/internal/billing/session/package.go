package session

import (
	"slices"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// TariffUsage — поля тарифа, от которых зависят минуты сессии и срок сохранения остатка.
type TariffUsage struct {
	DurationMinutes   int64
	UsageLimitEnabled bool
	UsageLimitMinutes *int64
}

// EffectiveMinutes — сколько минут получает сессия за тариф (BR-015).
// Laravel: app/Services/AgentTariffPurchaseService.php effectiveMinutes() (стр. 378–383).
func EffectiveMinutes(t TariffUsage) int64 {
	if t.UsageLimitEnabled && t.UsageLimitMinutes != nil {
		return min(t.DurationMinutes, *t.UsageLimitMinutes)
	}
	return t.DurationMinutes
}

// TariffPartExpiresAt — expires_at части за тариф: до него остаток сохраняется пакетом (BR-015).
// nil — тариф без лимита использования (остаток при досрочном завершении сгорает).
// Laravel: AgentTariffPurchaseService::purchase() стр. 85 — now()->addMinutes(usage_limit_minutes),
// сохраняется усечённым до секунды.
func TariffPartExpiresAt(t TariffUsage, now time.Time) *time.Time {
	if !t.UsageLimitEnabled || t.UsageLimitMinutes == nil {
		return nil
	}
	e := StoredTime(now.Add(time.Duration(*t.UsageLimitMinutes) * time.Minute))
	return &e
}

// Package — сохранённое время тарифа (строка time_packages).
type Package struct {
	ID          int64
	UserID      int64
	TariffID    *int64
	Name        string
	MinutesLeft int64
	ExpiresAt   time.Time
}

// PackageTariff — тариф пакета (с мягко удалёнными, withTrashed). nil в StartPackage — тариф
// удалён физически (tariff_id = null): ограничений нет.
type PackageTariff struct {
	ZoneIDs          []int64 // пусто — все зоны
	InValidityPeriod bool    // Tariff::inValidityPeriod() сейчас (WeeklyPeriods, считает вызывающий)
}

// Ошибки запуска пакета (поле package; тексты — ключи t() старого кода).
var (
	ErrPackageExpired       = &Error{Field: "package", Message: "Срок абонемента истёк."}
	ErrPackageWrongZone     = &Error{Field: "package", Message: "Абонемент не действует на этом компьютере."}
	ErrPackageOutsidePeriod = &Error{Field: "package", Message: "Абонемент сейчас не действует: проверьте дни и время тарифа."}
)

// PackageStart — план запуска пакета.
type PackageStart struct {
	Minutes int64 // минут в сессию: min(minutes_left, целых минут до срока)
	// Part — новая часть сессии: вид tariff, 0 тиынов, expires_at = срок пакета, поэтому при
	// досрочном завершении остаток снова сохранится пакетом до того же срока.
	Part Purchase
	// DeletePackage — пакет удаляется целиком, даже если Minutes < MinutesLeft из-за срока.
	DeletePackage bool
	// Extension — новая или продлённая сессия (как при покупке, цена 0).
	Extension Extension
}

// StartPackage — запуск сохранённого пакета на ПК (BR-024, GAP-014, GS-047).
//
// Laravel: app/Services/AgentTariffPurchaseService.php startPackage() (стр. 171–203):
//
//	$minutes = min($package->minutes_left, intdiv(max(0, $package->expires_at->getTimestamp() - now()->getTimestamp()), 60));
//	if ($minutes <= 0) { $package->delete(); throw 'Срок абонемента истёк.' }   // удаление откатывается исключением
//	ensureComputerFor; зоны тарифа; период действия тарифа; sell(price 0, …, $minutes, part); $package->delete();
//
// При ErrPackageExpired старый код пакет не удалял (транзакция откатывалась), его чистит суточная задача
// (BR-034). Решение Q-011 (docs/DECISIONS.md): пакет сжигает вызывающий отдельной транзакцией; здесь только ошибка.
func StartPackage(p Package, tariff *PackageTariff, c Computer, userID int64, now time.Time) (PackageStart, error) {
	minutes := min(p.MinutesLeft, money.IntDiv(max(0, p.ExpiresAt.Unix()-now.Unix()), 60))
	if minutes <= 0 {
		return PackageStart{}, ErrPackageExpired
	}
	if err := EnsureComputerFor(c, userID, now); err != nil {
		return PackageStart{}, err
	}
	if tariff != nil && len(tariff.ZoneIDs) > 0 && !slices.Contains(tariff.ZoneIDs, c.ZoneID) {
		return PackageStart{}, ErrPackageWrongZone
	}
	if tariff != nil && !tariff.InValidityPeriod {
		return PackageStart{}, ErrPackageOutsidePeriod
	}
	expires := StoredTime(p.ExpiresAt)
	return PackageStart{
		Minutes: minutes,
		Part: Purchase{
			UserID:    userID,
			Kind:      KindTariff,
			TariffID:  p.TariffID,
			Name:      p.Name,
			Minutes:   minutes,
			ExpiresAt: &expires,
		},
		DeletePackage: true,
		Extension:     Extend(c, minutes, now),
	}, nil
}

// PackageListed — пакет показывается клиенту в списке: expires_at > now.
// Laravel: AgentTariffPurchaseService::packagesFor() (стр. 153–163).
func PackageListed(p Package, now time.Time) bool { return p.ExpiresAt.After(now) }
