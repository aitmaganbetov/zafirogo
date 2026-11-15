package bonus

import (
	"errors"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// ErrOverflow — промежуточное произведение вышло за int64. В PHP intdiv() получил бы float вне
// диапазона int и бросил TypeError (пополнение не прошло бы). При проверенных настройках
// (процент ≤ 100, сумма ≤ 9 999 999 999.99) недостижимо.
var ErrOverflow = errors.New("bonus: переполнение при расчёте бонуса")

// TopUpBonus — результат TopUpBonus::for(): процент уровня (float, как в PHP: идёт в журнал
// `bonus_percent`) и бонус в тиынах.
type TopUpBonus struct {
	Percent float64
	Cents   money.Tiyn
}

// TopUpForCents — TopUpBonus::for(float $amount) (BR-041): бонус за пополнение основного баланса, начиная с уже
// посчитанных центов. levels — уровни в силе (TopUpLevelsInForce: пусто, если бонус выключен).
//
// Laravel: app/Services/TopUpBonus.php TopUpBonus::for() (~стр. 29–35):
//
//	$cents = (int) round($amount * 100);
//	$percent = PercentLevels::percentAt($this->levels(), 'amount', $cents);
//	return ['percent' => $percent, 'cents' => intdiv($cents * (int) round($percent * 100) + 5000, 10000)];
//
// $cents приходит от кассы: cash.ParseTopUpAmount → TopUpAmount.BonusBase (решение Q-002: ввод не больше двух
// знаков после точки, для него (int) round($amount * 100) равно сумме точно — cash/rules_test.go).
// Бонус — «половина вверх» до тиына: intdiv($cents * bp + 5000, 10000), bp = (int) round($percent * 100)
// без ограничения 0…10000 (в отличие от discounted(), BR-012).
func TopUpForCents(levels []Level, cents int64) (TopUpBonus, error) {
	percent := PercentAt(levels, cents)
	// Laravel: app/Services/TopUpBonus.php TopUpBonus::for() — intdiv($cents * (int) round($percent * 100) + 5000, 10000)
	bonus, err := money.PercentOfHalfUp(money.Tiyn(cents), money.BasisPoints(percent))
	if err != nil {
		return TopUpBonus{}, ErrOverflow
	}
	return TopUpBonus{Percent: percent, Cents: bonus}, nil
}
