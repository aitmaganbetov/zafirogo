package money

import (
	"errors"
	"math"
)

// ErrDivisionByZero — значение паники IntDiv при b == 0 (как DivisionByZeroError в PHP).
var ErrDivisionByZero = errors.New("money: деление на ноль")

// ErrIntDivOverflow — значение паники IntDiv(MinInt64, -1) (как ArithmeticError в PHP intdiv()).
var ErrIntDivOverflow = errors.New("money: переполнение intdiv")

// IntDiv — PHP intdiv($a, $b): частное с усечением к нулю.
// b == 0 → panic(ErrDivisionByZero) (PHP: DivisionByZeroError «Division by zero»);
// a == MinInt64, b == -1 → panic(ErrIntDivOverflow) (PHP: ArithmeticError).
// Деление Go для int64 и так усекает к нулю, функция нужна, чтобы формулы читались как в Laravel
// и чтобы ошибки были явными.
func IntDiv(a, b int64) int64 {
	if b == 0 {
		panic(ErrDivisionByZero)
	}
	if b == -1 && a == math.MinInt64 {
		panic(ErrIntDivOverflow)
	}
	return a / b
}

// PHPMod — PHP $a % $b: знак результата как у делимого (как в Go). b == 0 → panic(ErrDivisionByZero)
// (PHP: DivisionByZeroError «Modulo by zero»). PHP для b == -1 всегда возвращает 0.
func PHPMod(a, b int64) int64 {
	if b == 0 {
		panic(ErrDivisionByZero)
	}
	if b == -1 {
		return 0
	}
	return a % b
}

// MulChecked — a*b с проверкой переполнения int64.
func MulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	c := a * b
	if c/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, ErrOutOfRange
	}
	return c, nil
}

// AddChecked — a+b с проверкой переполнения int64.
func AddChecked(a, b int64) (int64, error) {
	c := a + b
	if (c > a) != (b > 0) {
		return 0, ErrOutOfRange
	}
	return c, nil
}

// Add складывает суммы и проверяет, что результат в пределах MaxAmount.
func Add(a, b Tiyn) (Tiyn, error) {
	c, err := AddChecked(int64(a), int64(b))
	if err != nil {
		return 0, err
	}
	return Tiyn(c), Tiyn(c).Check()
}

// Sub вычитает суммы и проверяет, что результат в пределах MaxAmount.
func Sub(a, b Tiyn) (Tiyn, error) {
	if b == math.MinInt64 {
		return 0, ErrOutOfRange
	}
	return Add(a, -b)
}

// MulDivRound — intdiv($a * $mul + $half, $div) с проверкой переполнения: общий вид
// целочисленных формул старого кода «+5000 / 10000», «+30 / 60», «* $seconds / $total».
//   - Laravel: app/Services/AgentTariffPurchaseService.php discounted() — intdiv($price * $bp + 5000, 10000) (BR-012);
//   - Laravel: app/Services/TopUpBonus.php TopUpBonus::forAmount() — intdiv($cents * $bp + 5000, 10000) (BR-041);
//   - Laravel: app/Services/AgentTariffPurchaseService.php — intdiv(amountToCents($pricePerHour) * $minutes + 30, 60) (BR-014);
//   - Laravel: app/Services/SessionSettlement.php settle() — intdiv($part->main_cents * $seconds, $total) (BR-032, add = 0).
//
// Округление — как у intdiv: к нулю ПОСЛЕ прибавления add (для отрицательных это не «половина вверх» —
// так же, как в старом коде). div == 0 → panic(ErrDivisionByZero).
func MulDivRound(a, mul, add, div int64) (int64, error) {
	p, err := MulChecked(a, mul)
	if err != nil {
		return 0, err
	}
	s, err := AddChecked(p, add)
	if err != nil {
		return 0, err
	}
	return IntDiv(s, div), nil
}

// BasisPoints — (int) round($percent * 100): процент (float, как из decimal(5,2)/JSON настроек)
// в базисные пункты с PHP-округлением.
//   - Laravel: app/Services/AgentTariffPurchaseService.php discounted() — (int) round($percent * 100) (BR-012);
//   - Laravel: app/Services/TopUpBonus.php TopUpBonus::forAmount() — (int) round($percent * 100) (BR-041).
//
// Ограничение 0…10000 (min/max в discounted()) — забота вызывающего: в TopUpBonus его нет.
func BasisPoints(percent float64) int64 {
	return PHPIntCast(PHPRound(percent*100, 0))
}

// PercentOfHalfUp — intdiv($amount * $bp + 5000, 10000): доля суммы в базисных пунктах,
// «половина вверх» для неотрицательных значений (BR-012, BR-041; ссылки — в MulDivRound).
func PercentOfHalfUp(amount Tiyn, bp int64) (Tiyn, error) {
	v, err := MulDivRound(int64(amount), bp, 5000, 10000)
	if err != nil {
		return 0, err
	}
	return Tiyn(v), nil
}
