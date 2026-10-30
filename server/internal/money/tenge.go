package money

import (
	"math"
	"strconv"
	"strings"
)

// Tenge форматирует сумму как PHP number_format($tenge, 2, '.', ”): "1234.50", "-0.50", "0.00".
// Без разделителя тысяч. Для неотрицательных сумм совпадает со старым formatCents()
// (Laravel: app/Services/AgentTariffPurchaseService.php formatCents(), BR-001) и с
// StaffApiController::money() (app/Http/Controllers/Api/Staff/StaffApiController.php).
// Для отрицательных старый formatCents давал "0.-50" (баг, BR-001, §15.1 п. 1); мы его не повторяем
// и пишем "-0.50", как Friends::formatCents() и number_format().
// Допущение Этапа 1: Q-004.
func (t Tiyn) Tenge() string {
	var b [24]byte
	return string(t.appendTenge(b[:0]))
}

// String — то же, что Tenge(): удобно для логов и fmt.
func (t Tiyn) String() string { return t.Tenge() }

func (t Tiyn) appendTenge(b []byte) []byte {
	// Делим со знаком и меняем знак у частного и остатка: так MinInt64 не переполняется.
	whole, frac := int64(t)/100, int64(t)%100
	if t < 0 {
		b = append(b, '-')
		whole, frac = -whole, -frac
	}
	b = strconv.AppendInt(b, whole, 10)
	return append(b, '.', byte('0'+frac/10), byte('0'+frac%10))
}

// ParseTenge — строгий разбор суммы в тенге: необязательный «-», цифры, необязательные «.» и 1–2 цифры.
// Пробелы, «+», запятая, экспонента, «.5», «5.» — ErrInvalidAmount; три и более знака после точки
// (даже «1.500») — ErrTooManyDecimals; |сумма| > MaxAmount — ErrOutOfRange.
// Арифметика целочисленная, без float.
func ParseTenge(s string) (Tiyn, error) {
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if !allDigits(whole) || (hasDot && !allDigits(frac)) {
		return 0, ErrInvalidAmount
	}
	if len(frac) > 2 {
		return 0, ErrTooManyDecimals
	}
	return fromDigits(neg, whole, frac)
}

// fromDigits собирает сумму из целой части и не более чем двух цифр дробной (дополняется нулями).
func fromDigits(neg bool, whole, frac string) (Tiyn, error) {
	whole = strings.TrimLeft(whole, "0")
	// MaxAmount = 1e15 тиынов = 1e13 тенге: целая часть не длиннее 14 цифр, дальше гарантированное переполнение.
	if len(whole) > 14 {
		return 0, ErrOutOfRange
	}
	var v int64
	for i := 0; i < len(whole); i++ {
		v = v*10 + int64(whole[i]-'0')
	}
	v *= 100
	if len(frac) > 0 {
		v += int64(frac[0]-'0') * 10
	}
	if len(frac) > 1 {
		v += int64(frac[1] - '0')
	}
	if neg {
		v = -v
	}
	t := Tiyn(v)
	if !t.Valid() {
		return 0, ErrOutOfRange
	}
	return t, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// FromTengeFloat — PHP (int) round($tenge * 100) в PHP 8.3, с предокруглением PHP (см. PHPRound).
// Так старый код переводил float-суммы в центы:
//   - Laravel: app/Services/TopUpBonus.php TopUpBonus::forAmount() — $cents = (int) round($amount * 100);
//   - Laravel: app/Services/Friends.php Friends::storedCents() — (int) round((float) $amount * 100) (BR-002).
//
// Результат вне MaxAmount не обрезается (как в PHP); проверка — Tiyn.Check() у вызывающего.
// NaN/±Inf → 0, как (int) в PHP.
func FromTengeFloat(f float64) Tiyn {
	return Tiyn(PHPIntCast(PHPRound(f*100, 0)))
}

// Float — сумма в тенге как float64 (только для воспроизведения float-формул старого кода,
// например кассы, где PHP складывал (float) из decimal). Для |t| ≤ MaxAmount значение t точно
// представимо, а деление на 100 даёт ближайший double к десятичной записи — тот же,
// что (float)"1234.50" в PHP.
func (t Tiyn) Float() float64 { return float64(t) / 100 }

// FormatFloat2 — PHP number_format((float) $amount, 2, '.', ”) для произвольного float
// (Laravel: StaffApiController::money(), GuestHistory, CashRegisterService::sell() — webhook 'amount').
// Округление — PHPRound(|x|, 2), печать "%.2F", «-0.00» не бывает (как в
// ext/standard/math.c _php_math_number_format_ex()).
// NaN/Inf печатаются как "nan"/"inf"/"-inf" (PHP: "nan", "inf", "-inf").
func FormatFloat2(d float64) string {
	neg := false
	if d < 0 {
		neg = true
		d = -d
	}
	d = PHPRound(d, 2)
	if d == 0 {
		// PHP-шный "%.*F" (__cvt) печатает -0.0 без знака, а is_negative сбрасывается при d == 0.
		return "0.00"
	}
	if math.IsNaN(d) {
		return "nan"
	}
	if math.IsInf(d, 0) {
		if neg {
			return "-inf"
		}
		return "inf"
	}
	s := strconv.FormatFloat(d, 'f', 2, 64)
	if neg {
		return "-" + s
	}
	return s
}

// FromTengeFloatDecimal — сумма, которую MySQL записал бы в decimal(…,2), получив PHP-float из старого кода:
// PHP превращает float в строку с precision = 14 (PHPFloatString), MySQL округляет эту строку по десятичной
// записи половиной от нуля (ParseTengeRounded). Так ведут себя суммы кассы: CashRegisterService::topUpBalance()
// ($balance->increment($balanceType, $amount)), sell() ('amount' => $total), BR-052…BR-054.
// Пример: 0.1 + 0.2 → "0.3" → 30; 100.005 → "100.005" → 10001.
func FromTengeFloatDecimal(f float64) (Tiyn, error) {
	return ParseTengeRounded(PHPFloatString(f))
}
