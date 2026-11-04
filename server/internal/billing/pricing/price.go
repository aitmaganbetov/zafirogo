package pricing

import (
	"fmt"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// MaxPrice — верхняя граница цены тарифа/часа зоны: StoreTariffRequest 'price' max:9999999999.99
// (StoreClubZoneRequest 'price_per_hour' — max:1000000). С ней все произведения формул ниже
// (цена × 10 000, цена × 720) помещаются в int64.
const MaxPrice money.Tiyn = 999_999_999_999

// MaxMinutes — GuestMinutesController::MAX_MINUTES: поминутно в агенте и гостевая сессия на кассе — 1…720.
const MaxMinutes int64 = 720

func checkPrice(p money.Tiyn) error {
	if p < 0 || p > MaxPrice {
		return fmt.Errorf("%w: цена %d вне 0…%d", ErrInvalidInput, p, MaxPrice)
	}
	return nil
}

// DiscountBasisPoints — процент скидки в базисных пунктах: min(10000, max(0, (int) round($percent * 100))).
// Laravel: app/Services/AgentTariffPurchaseService.php discounted() (стр. 371–376) (BR-012).
func DiscountBasisPoints(percent float64) int64 {
	return min(10000, max(0, money.BasisPoints(percent)))
}

// Discounted — цена минус процент, скидка округляется половиной вверх:
//
//	$bp = min(10000, max(0, (int) round($percent * 100)));
//	return max(0, $price - intdiv($price * $bp + 5000, 10000));
//
// Laravel: app/Services/AgentTariffPurchaseService.php discounted() (стр. 371–376) (BR-012, GAP-011).
// percent — скидка клиента как её отдаёт LoyaltyProgram::discountFor() (float, %); > 100 → цена 0,
// < 0 → без скидки. Цена 0…MaxPrice (иначе ErrInvalidInput).
func Discounted(price money.Tiyn, percent float64) (money.Tiyn, error) {
	if err := checkPrice(price); err != nil {
		return 0, err
	}
	bp := DiscountBasisPoints(percent)
	off := money.IntDiv(int64(price)*bp+5000, 10000) // ≤ 1e12 * 1e4 — без переполнения
	return money.Tiyn(max(0, int64(price)-off)), nil
}

// TariffPrice — цена тарифа для клиента: со скидкой, если у тарифа «Применять скидку» и область
// скидки включает тарифы.
//
//	$price = amountToCents($tariff->price);
//	return $tariff->discount_enabled && discountOnTariffs() ? discounted($price, discountFor($membership)) : $price;
//
// Laravel: app/Services/AgentTariffPurchaseService.php priceInCents() (стр. 361–368) (BR-013).
// Цена не зависит от зоны и дня недели: зоны и периоды только ограничивают доступность (BR-016, BR-017).
func TariffPrice(t Tariff, rules PaymentRules, discountPercent float64) (money.Tiyn, error) {
	if t.DiscountEnabled && rules.DiscountOnTariffs() {
		return Discounted(t.Price, discountPercent)
	}
	if err := checkPrice(t.Price); err != nil {
		return 0, err
	}
	return t.Price, nil
}

// HourlyPrice — цена часа зоны для клиента: со скидкой, если область скидки включает поминутное время.
// Результат уже округлён до тиына (formatCents), от него считается цена минут — двойное округление
// (GAP-010). Без членства в клубе ($membership === null) скидки нет: передайте discountPercent = 0
// (discounted($p, 0) = $p).
// Laravel: app/Services/AgentTariffPurchaseService.php hourlyPrice() (стр. 141–150) (BR-014).
func HourlyPrice(zonePricePerHour money.Tiyn, rules PaymentRules, discountPercent float64) (money.Tiyn, error) {
	if !rules.DiscountOnHourly() {
		if err := checkPrice(zonePricePerHour); err != nil {
			return 0, err
		}
		return zonePricePerHour, nil
	}
	return Discounted(zonePricePerHour, discountPercent)
}

// MinutesPrice — цена минут по цене часа, до тиына половиной вверх: intdiv(amountToCents($pricePerHour) * $minutes + 30, 60).
// Laravel: app/Services/AgentTariffPurchaseService.php minutesPriceInCents() (стр. 206–209) (BR-014, GS-041…GS-045).
// Агент повторял её: Math.floor((hourCents * minutes + 30) / 60) (WindowsAgent/Frontend/main.js minutesPrice()).
// Цена часа 0…MaxPrice, минуты 0…MaxMinutes.
func MinutesPrice(pricePerHour money.Tiyn, minutes int64) (money.Tiyn, error) {
	if err := checkPrice(pricePerHour); err != nil {
		return 0, err
	}
	if minutes < 0 || minutes > MaxMinutes {
		return 0, fmt.Errorf("%w: минуты %d вне 0…%d", ErrInvalidInput, minutes, MaxMinutes)
	}
	return money.Tiyn(money.IntDiv(int64(pricePerHour)*minutes+30, 60)), nil
}
