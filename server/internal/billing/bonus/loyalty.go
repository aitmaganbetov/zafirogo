package bonus

import "github.com/aitmaganbetov/zafirogo/server/internal/money"

// LoyaltyPercentFor — LoyaltyProgram::percentFor(): процент наибольшего достигнутого уровня
// по накопительным тратам клиента (BR-011, BR-040). levels — уровни в силе (LoyaltyLevelsInForce:
// пусто, если программа выключена). totalSpent — club_memberships.total_spent в тиынах
// (в старом коде amountToCents((string) total_spent) из decimal(12,2) — для неотрицательных
// значений это то же число тиынов).
//
// Laravel: app/Services/LoyaltyProgram.php LoyaltyProgram::percentFor() (~стр. 33–36).
func LoyaltyPercentFor(levels []Level, totalSpent money.Tiyn) float64 {
	return PercentAt(levels, int64(totalSpent))
}

// LoyaltyDiscountFor — LoyaltyProgram::discountFor() (BR-011): скидка клиента в процентах —
// большая из персональной (club_memberships.discount, decimal(5,2)) и накопительной.
// Результат — float, как в PHP; в цену он идёт через discounted() (BR-012, (int) round($percent * 100)).
//
// Laravel: app/Services/LoyaltyProgram.php LoyaltyProgram::discountFor() (~стр. 39–42):
//
//	return max((float) $membership->discount, $this->percentFor($membership));
func LoyaltyDiscountFor(levels []Level, personalDiscount float64, totalSpent money.Tiyn) float64 {
	return phpMax(personalDiscount, LoyaltyPercentFor(levels, totalSpent))
}

// DiscountIsCumulative — флаг discount_cumulative в API сотрудника: накопительная больше персональной
// (BR-011; Laravel: app/Http/Controllers/Api/Staff/CustomerController.php index(), ~стр. 44:
// 'discount_cumulative' => $cumulative > (float) $membership->discount).
func DiscountIsCumulative(levels []Level, personalDiscount float64, totalSpent money.Tiyn) bool {
	return LoyaltyPercentFor(levels, totalSpent) > personalDiscount
}

// AddSpending — новое значение total_spent после LoyaltyProgram::addSpending() (BR-042):
// растёт только на положительную сумму (оплата с основного баланса; бонусы не считаются).
// Траты копятся всегда, даже при выключенной программе.
//
// Laravel: app/Services/LoyaltyProgram.php LoyaltyProgram::addSpending() (~стр. 45–50):
//
//	if ($cents > 0) { ...->increment('total_spent', formatCents($cents)); }
//
// Предел здесь — только общий предел арифметики пакета money: выход за money.MaxAmount → money.ErrOutOfRange.
// Предел колонки (в MySQL decimal(12,2), больше 9 999 999 999.99 — ошибка 1264 и откат) проверяет сервис:
// billing/service MaxTotalSpent (решение SV-7, docs/DECISIONS.md / stage1-service.md).
func AddSpending(totalSpent, cents money.Tiyn) (money.Tiyn, error) {
	if cents <= 0 {
		return totalSpent, nil
	}
	return money.Add(totalSpent, cents)
}

// SubtractSpending — новое значение total_spent после LoyaltyProgram::subtractSpending() (BR-042):
// возврат (отмена заказа, возврат основного баланса при завершении сессии) уменьшает траты,
// но не ниже нуля.
//
// Laravel: app/Services/LoyaltyProgram.php LoyaltyProgram::subtractSpending() (~стр. 53–63):
//
//	if ($cents <= 0) return;
//	if (... where('total_spent', '>=', $amount)->decrement('total_spent', $amount) === 0) { ...update(['total_spent' => 0]); }
func SubtractSpending(totalSpent, cents money.Tiyn) money.Tiyn {
	if cents <= 0 {
		return totalSpent
	}
	if totalSpent >= cents {
		return totalSpent - cents
	}
	return 0
}
