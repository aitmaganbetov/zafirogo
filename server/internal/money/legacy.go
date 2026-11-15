package money

import (
	"math"
	"strconv"
	"strings"
)

// ParseTransferAmount — Friends::amountToCents(): сумма перевода другу
// (Laravel: app/Services/Friends.php amountToCents(), стр. 176–186; BR-002):
// trim, запятая → точка, затем /^\d{1,10}(\.\d{1,2})?$/. Не подходит — 0 (как в старом коде;
// вызывающий отвечает ошибкой «Сумма перевода — от 0,01 до 1 000 000.»).
// PHP trim() убирает " \t\n\r\0\x0B".
func ParseTransferAmount(amount string) Tiyn {
	s := strings.ReplaceAll(strings.Trim(amount, " \t\n\r\x00\x0B"), ",", ".")
	whole, frac, hasDot := strings.Cut(s, ".")
	if len(whole) > 10 || !allDigits(whole) || (hasDot && (len(frac) > 2 || !allDigits(frac))) {
		return 0
	}
	// PCRE '$' совпадает и перед завершающим "\n", но trim() его уже убрал.
	t, _ := fromDigits(false, whole, frac) // ≤ 10 цифр — всегда в пределах MaxAmount
	return t
}

// PHPStringToInt — PHP (int)"строка" в PHP 8.3 и 8.4 (Zend/zend_operators.c zval_get_long_func(), ветка IS_STRING,
// через _is_numeric_string_ex(allow_errors = true)): ведущие пробельные символы " \t\n\r\v\f", знак,
// цифры; хвост игнорируется ("12abc" → 12, "abc" → 0). Если в числе есть точка или экспонента
// ("1.9", "1e3") или целое не помещается в int64, число читается как float и приводится с насыщением
// (zend_dval_to_lval_cap: "9999999999999999999" → MaxInt64, "1.9" → 1).
func PHPStringToInt(s string) int64 {
	num, isFloat := phpNumericPrefix(s)
	if num == "" {
		return 0
	}
	if !isFloat {
		if v, err := strconv.ParseInt(num, 10, 64); err == nil {
			return v
		}
	}
	return phpDvalToLvalCap(parseFloatPrefix(num))
}

// PHPStringToFloat — PHP (float)"строка" (Zend: zval_get_double() → zend_strtod() по ведущему
// числовому префиксу; "abc" → 0, " 1.5x" → 1.5, ".5" → 0.5).
func PHPStringToFloat(s string) float64 {
	num, _ := phpNumericPrefix(s)
	if num == "" {
		return 0
	}
	return parseFloatPrefix(num)
}

// phpNumericPrefix выделяет ведущее десятичное число: [пробелы][±]цифры[.цифры][e[±]цифры]
// или [пробелы][±].цифры[e[±]цифры]. isFloat — есть точка или экспонента.
func phpNumericPrefix(s string) (num string, isFloat bool) {
	s = strings.TrimLeft(s, " \t\n\r\v\f")
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	start := i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	intDigits := i - start
	if i < len(s) && s[i] == '.' && i+1 < len(s) && isDigit(s[i+1]) || (intDigits > 0 && i < len(s) && s[i] == '.') {
		isFloat = true
		i++
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	if intDigits == 0 && !isFloat {
		return "", false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < len(s) && isDigit(s[j]) {
			isFloat = true
			for j < len(s) && isDigit(s[j]) {
				j++
			}
			i = j
		}
	}
	return s[:i], isFloat
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// parseFloatPrefix — zend_strtod для уже выделенного числа: корректное округление к ближайшему double,
// переполнение → ±Inf, «потеря значимости» → 0 (как zend_strtod).
func parseFloatPrefix(num string) float64 {
	num = strings.TrimSuffix(num, ".")
	v, _ := strconv.ParseFloat(num, 64) // ошибка возможна только ErrRange: v уже ±Inf/0, как в zend_strtod
	return v
}

// phpDvalToLvalCap — zend_dval_to_lval_cap() (Zend/zend_operators.h): NaN/Inf → 0, вне int64 — насыщение.
func phpDvalToLvalCap(d float64) int64 {
	if math.IsInf(d, 0) || math.IsNaN(d) {
		return 0
	}
	if d >= twoPow63 || d < -twoPow63 {
		if d > 0 {
			return math.MaxInt64
		}
		return math.MinInt64
	}
	return int64(d)
}

// PHPIsNumeric — PHP is_numeric() для строки (Zend/zend_operators.c _is_numeric_string_ex(allow_errors = false)):
// [пробелы][±](цифры[.цифры*] | .цифры)[e[±]цифры][пробелы]. Это правило Laravel `numeric` для строк.
func PHPIsNumeric(s string) bool {
	trimmed := strings.TrimLeft(s, " \t\n\r\v\f")
	num, _ := phpNumericPrefix(trimmed)
	return num != "" && strings.Trim(trimmed[len(num):], " \t\n\r\v\f") == ""
}

// PHPFloatString — PHP (string)$float при precision = 14 (по умолчанию; Zend: smart_str_append_double() →
// zend_gcvt(value, 14, '.', 'E')): 14 значащих цифр без хвостовых нулей, экспонента при decpt < -3
// или decpt > 14 ("1.0E+25", "1.0E-5"), "-0", "INF", "-INF", "NAN".
// Так PHP передавал float в SQL (PDO-параметр PARAM_STR, Query\Builder::increment()), поэтому MySQL
// округлял именно эту строку — см. FromTengeFloatDecimal.
func PHPFloatString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NAN"
	case math.IsInf(f, 1):
		return "INF"
	case math.IsInf(f, -1):
		return "-INF"
	case f == 0:
		if math.Signbit(f) {
			return "-0"
		}
		return "0"
	}
	// d.ddddddddddddde±XX — 14 значащих цифр, корректное округление (как zend_dtoa mode 2).
	e := strconv.FormatFloat(f, 'e', 13, 64)
	sign := ""
	if e[0] == '-' {
		sign, e = "-", e[1:]
	}
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr) // формат strconv гарантирует число
	digits := strings.TrimRight(strings.Replace(mant, ".", "", 1), "0")
	decpt := exp + 1
	if decpt < -3 || decpt > 14 {
		// zend_gcvt: экспоненциальная запись, минимум "d.0".
		frac := digits[1:]
		if frac == "" {
			frac = "0"
		}
		esign := "+"
		if exp < 0 {
			esign, exp = "-", -exp
		}
		return sign + digits[:1] + "." + frac + "E" + esign + strconv.Itoa(exp)
	}
	switch {
	case decpt <= 0:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		return sign + digits + strings.Repeat("0", decpt-len(digits))
	default:
		return sign + digits[:decpt] + "." + digits[decpt:]
	}
}
