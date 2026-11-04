package bonus

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Ключи порога уровней в настройках клуба (BR-040).
const (
	ThresholdAmount = "amount" // бонус за пополнение: finances.topup_bonus_levels
	ThresholdSpent  = "spent"  // накопительная скидка: finances.loyalty_levels
)

// MaxLevels — TopUpBonus::MAX_LEVELS и LoyaltyProgram::MAX_LEVELS (проверяется только при сохранении).
//   - Laravel: app/Services/TopUpBonus.php TopUpBonus::MAX_LEVELS; app/Services/LoyaltyProgram.php LoyaltyProgram::MAX_LEVELS.
const MaxLevels = 20

// Number — число уровня после PercentLevels::normalize(): PHP int|float.
// Laravel: app/Support/PercentLevels.php normalize() — $number = round((float) $v, 2), целое → (int).
// Тип важен: в percentAt() int * 100 остаётся int (а при переполнении становится float), float * 100 — float;
// в JSON настроек 10 и 7.5 пишутся как 10 и 7.5.
type Number struct {
	isFloat bool
	i       int64
	f       float64
}

// IntNumber — PHP int.
func IntNumber(i int64) Number { return Number{i: i} }

// FloatNumber — PHP float.
func FloatNumber(f float64) Number { return Number{isFloat: true, f: f} }

// IsFloat — число хранится как PHP float (иначе int).
func (n Number) IsFloat() bool { return n.isFloat }

// Int — значение int (для float — 0).
func (n Number) Int() int64 {
	if n.isFloat {
		return 0
	}
	return n.i
}

// Float — (float) $n.
func (n Number) Float() float64 {
	if n.isFloat {
		return n.f
	}
	return float64(n.i)
}

// String — (string) $n в PHP (int — десятично, float — precision 14: "5.5", "1.0E+25").
// Так старый журнал settings.updated печатал уровни: «10000: 5.5%; 50000: 10%»
// (Laravel: app/Http/Controllers/SettingsController.php save(), ~стр. 291).
func (n Number) String() string {
	if n.isFloat {
		return money.PHPFloatString(n.f)
	}
	return strconv.FormatInt(n.i, 10)
}

// MarshalJSON — json_encode($n) без флагов (Eloquent-каст 'array' при сохранении настроек):
// int — десятично, float — кратчайшая запись (serialize_precision = -1), целое значение без ".0".
func (n Number) MarshalJSON() ([]byte, error) {
	if !n.isFloat {
		return strconv.AppendInt(nil, n.i, 10), nil
	}
	if math.IsInf(n.f, 0) || math.IsNaN(n.f) {
		// json_encode(INF) — ошибка «Inf and NaN cannot be JSON encoded»; в настройках не встречается.
		return []byte("0"), nil
	}
	return []byte(phpJSONFloat(n.f)), nil
}

// phpJSONFloat — smart_str_append_double(d, precision = 17 (serialize_precision -1), zero_frac = false):
// кратчайшие цифры (zend_gcvt mode 0); экспонента при decpt < -3 или decpt > 17 ("1.0e+25", "1.0e-7").
func phpJSONFloat(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0"
		}
		return "0"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // "-1.2345e+17"
	neg := strings.HasPrefix(e, "-")
	e = strings.TrimPrefix(e, "-")
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)
	decpt := exp + 1
	var s string
	switch {
	case decpt < -3 || decpt > 17:
		m := digits[:1] + "."
		if len(digits) > 1 {
			m += digits[1:]
		} else {
			m += "0"
		}
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		s = m + "e" + sign + strconv.Itoa(exp)
	case decpt <= 0:
		s = "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		s = digits + strings.Repeat("0", decpt-len(digits))
	default:
		s = digits[:decpt] + "." + digits[decpt:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// Level — уровень «порог → процент» (BR-040). Threshold — `amount` (бонус за пополнение) или `spent`
// (накопительная скидка) в тенге; Percent — процент.
type Level struct {
	Threshold Number
	Percent   Number
}

// normalizeNumber — $number из PercentLevels::normalize():
//
//	fn (mixed $value): int|float => floor($rounded = round((float) $value, 2)) === $rounded ? (int) $rounded : $rounded
//
// Laravel: app/Support/PercentLevels.php normalize() (~стр. 18). round() — PHP 8.4+ (money.PHPRound).
func normalizeNumber(v Value) Number {
	r := money.PHPRound(v.ToFloat(), 2)
	if math.Floor(r) == r { // NaN != NaN → float, ±INF → (int) = 0, как в PHP
		return IntNumber(money.PHPIntCast(r))
	}
	return FloatNumber(r)
}

// Normalize — PercentLevels::normalize($levels, $threshold) (BR-040):
// не массив → []; остаются элементы-массивы, где $level[$threshold] и $level['percent'] — is_numeric;
// числа округляются до сотых (целые — int); стабильная сортировка по порогу ↑.
//
// Laravel: app/Support/PercentLevels.php PercentLevels::normalize() (~стр. 12–26);
// TopUpBonus::normalize() — с threshold "amount", LoyaltyProgram::normalize() — "spent".
func Normalize(levels Value, threshold string) []Level {
	if !levels.IsArray() {
		return []Level{}
	}
	out := make([]Level, 0, levels.Len())
	for _, level := range levels.Items() {
		if !level.IsArray() || !level.Get(threshold).IsNumeric() || !level.Get("percent").IsNumeric() {
			continue
		}
		out = append(out, Level{Threshold: normalizeNumber(level.Get(threshold)), Percent: normalizeNumber(level.Get("percent"))})
	}
	// Collection::sortBy($threshold) → asort(SORT_REGULAR): стабильная (PHP ≥ 8.0) сортировка
	// по сравнению int|float (zend_compare).
	slices.SortStableFunc(out, func(a, b Level) int { return compareNumbers(a.Threshold, b.Threshold) })
	return out
}

// compareNumbers — zend_compare() для int|float: int с int — точно, иначе через double
// (ZEND_THREEWAY_COMPARE((double) l, d), PHP 8.3).
func compareNumbers(a, b Number) int {
	if !a.isFloat && !b.isFloat {
		switch {
		case a.i < b.i:
			return -1
		case a.i > b.i:
			return 1
		}
		return 0
	}
	x, y := a.Float(), b.Float()
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	case x == y:
		return 0
	}
	return 1 // NaN: ZEND_THREEWAY_COMPARE даёт 1 (в normalize NaN не возникает)
}

// thresholdCents — (int) round($level[$threshold] * 100) из percentAt().
// int * 100 в PHP остаётся int (переполнение → float); round(int) возвращает float(int);
// (int) float — с переходом по модулю 2^64 вне int64 (money.PHPIntCast).
func thresholdCents(n Number) int64 {
	if !n.isFloat {
		if p, err := money.MulChecked(n.i, 100); err == nil {
			return money.PHPIntCast(float64(p)) // round(int) → (double): точность теряется выше 2^53, как в PHP
		}
		return money.PHPIntCast(money.PHPRound(float64(n.i)*100, 0))
	}
	return money.PHPIntCast(money.PHPRound(n.f*100, 0))
}

// PercentAt — PercentLevels::percentAt($levels, $threshold, $cents) (BR-040): процент «наибольший
// из достигнутых» (не процент последнего достигнутого уровня); порог включительно (cents >= порог·100);
// 0 — ни один не достигнут. Результат — float, как в PHP.
//
// Laravel: app/Support/PercentLevels.php PercentLevels::percentAt() (~стр. 29–39).
func PercentAt(levels []Level, cents int64) float64 {
	percent := 0.0
	for _, level := range levels {
		if cents >= thresholdCents(level.Threshold) {
			percent = phpMax(percent, level.Percent.Float())
		}
	}
	return percent
}

// phpMax — max($a, $b) PHP для двух float: $b берётся, если НЕ ($b <= $a).
func phpMax(a, b float64) float64 {
	if !(b <= a) {
		return b
	}
	return a
}
