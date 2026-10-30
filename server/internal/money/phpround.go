package money

import (
	"math"
	"math/big"
	"strconv"
	"strings"
)

// PHPRound — точный эквивалент PHP 8.3 round($value, $places) (режим PHP_ROUND_HALF_UP).
//
// Перенос построчно из php-src, ветка PHP-8.3 (проверено на теге php-8.3.6):
// ext/standard/math.c _php_math_round(), php_intlog10abs(), php_intpow10(), php_round_helper().
// В 8.3 значение сначала «предокругляется» до 15 значащих цифр (precision_places = 14 - floor(log10|x|)),
// поэтому round(1.005, 2) = 1.01 и round(100.005*100) = 10001, а наивный math.Round(x*100) даёт 100 и 10000.
// В PHP 8.4 алгоритм заменён (предокругления нет) — см. docs/open-questions/stage1-money.md.
//
// Эквивалентность доказана дифференциальным тестом против php 8.3.6
// (testdata/php_round.json, генератор tools/php-oracle/gen-round-fixture.php).
func PHPRound(value float64, places int) float64 {
	// if (!zend_finite(value) || value == 0.0) return value;
	if math.IsInf(value, 0) || math.IsNaN(value) || value == 0 {
		return value
	}
	// places = places < INT_MIN+1 ? INT_MIN+1 : places;  (в Go int — 64 бита, PHP-аргумент
	// уже приведён к C int в PHP_FUNCTION(round); здесь ограничиваем так же.)
	places = clampCInt(places)
	if places < math.MinInt32+1 {
		places = math.MinInt32 + 1
	}
	precisionPlaces := 14 - phpIntLog10Abs(value)
	f1 := phpIntPow10(absInt(places))

	var tmp float64
	if precisionPlaces > places && precisionPlaces-15 < places {
		usePrecision := precisionPlaces
		f2 := phpIntPow10(absInt(usePrecision))
		if usePrecision >= 0 {
			tmp = value * f2
		} else {
			tmp = value / f2
		}
		// preround the result
		tmp = phpRoundHelper(tmp)
		usePrecision = places - precisionPlaces
		f2 = phpIntPow10(absInt(usePrecision))
		// because places < precision_places
		tmp /= f2
	} else {
		if places >= 0 {
			tmp = value * f1
		} else {
			tmp = value / f1
		}
		// This value is beyond our precision, so rounding it is pointless
		if math.Abs(tmp) >= 1e15 {
			return value
		}
	}

	tmp = phpRoundHelper(tmp)

	if absInt(places) < 23 {
		if places > 0 {
			tmp /= f1
		} else {
			tmp *= f1
		}
	} else {
		// snprintf(buf, 39, "%15fe%d", tmp_value, -places); tmp_value = zend_strtod(buf, NULL);
		v := PHPStringToFloat(phpSnprintfRoundBuf(tmp, places))
		// couldn't convert to string and back
		if math.IsInf(v, 0) || math.IsNaN(v) {
			v = value
		}
		tmp = v
	}
	return tmp
}

// phpSnprintfRoundBuf — snprintf(buf, 39, "%15fe%d", tmp, -places) PHP-шным форматтером
// (main/snprintf.c format_converter → php_conv_fp → __cvt):
//   - ±Inf → "INF" (без знака) — zend_strtod такую строку читает как 0, и round() возвращает +0;
//     Inf сюда попадает, когда php_intpow10(precision_places) переполняется (|x| < ~1e-294);
//   - знак ставится только при num < 0, а __cvt для нуля сбрасывает знак: -0.0 → "0.000000";
//   - ширина 15 (пробелы слева).
//
// NaN сюда не попадает (value конечно, множители конечны или +Inf), а обрезка буфера до 38 байт
// не срабатывает: |tmp| < 1e15 даёт ≤ 23 символа, показатель — ≤ 12.
func phpSnprintfRoundBuf(tmp float64, places int) string {
	num := "INF"
	if !math.IsInf(tmp, 0) {
		if tmp == 0 {
			tmp = 0 // -0 → +0
		}
		num = strconv.FormatFloat(tmp, 'f', 6, 64)
	}
	if len(num) < 15 {
		num = strings.Repeat(" ", 15-len(num)) + num
	}
	return num + "e" + strconv.Itoa(-places)
}

// clampCInt приводит значение к диапазону C int, как PHP_FUNCTION(round)
// (ZEND_LONG_INT_OVFL → INT_MAX, ZEND_LONG_INT_UDFL → INT_MIN).
func clampCInt(v int) int {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return v
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// phpRoundHelper — php_round_helper() для PHP_ROUND_HALF_UP.
func phpRoundHelper(value float64) float64 {
	if value >= 0 {
		return math.Floor(value + 0.5)
	}
	return math.Ceil(value - 0.5)
}

var pow10Table = [...]float64{
	1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11,
	1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
}

// phpIntPow10 — php_intpow10(): таблица для 0..22, иначе pow(10, power).
func phpIntPow10(power int) float64 {
	if power < 0 || power > 22 {
		// glibc pow(10, n) для целых n даёт ближайший double к 10^n, а math.Pow в Go — нет
		// (math.Pow(10, 244) отличается на ulp). ParseFloat округляет корректно; переполнение → +Inf, как pow.
		v, _ := strconv.ParseFloat("1e"+strconv.Itoa(power), 64)
		return v
	}
	return pow10Table[power]
}

var log10Table = [...]float64{
	1e-8, 1e-7, 1e-6, 1e-5, 1e-4, 1e-3, 1e-2, 1e-1, 1e0, 1e1, 1e2,
	1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11, 1e12, 1e13,
	1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
}

// phpIntLog10Abs — php_intlog10abs(): floor(log10(|value|)) бинарным поиском по таблице.
func phpIntLog10Abs(value float64) int {
	value = math.Abs(value)
	if value < 1e-8 || value > 1e22 {
		return libcFloorLog10(value)
	}
	result := 15
	for _, step := range [...]int{8, 4, 2} {
		if value < log10Table[result] {
			result -= step
		} else {
			result += step
		}
	}
	if value < log10Table[result] {
		result--
	} else {
		result++
	}
	if value < log10Table[result] {
		result--
	}
	return result - 8
}

// libcFloorLog10 — (int)floor(log10(value)) так, как это считает libc: log10 округлён к ближайшему double,
// поэтому для значения чуть меньше 10^m (например, double 1e30 < 10^30) результат — m, а не m-1.
// math.Log10 в Go не округлён корректно (math.Log10(1e30) = 29.999999999999996), поэтому считаем
// точный floor через big.Rat и отдельно проверяем, не округлился бы log10 вверх до целого.
// Вызывается только для |v| < 1e-8 или > 1e22 — вне денежных сумм.
func libcFloorLog10(v float64) int {
	x := new(big.Rat).SetFloat64(v)
	e := int(math.Floor(math.Log10(v)))
	for x.Cmp(ratPow10(e)) < 0 {
		e--
	}
	for x.Cmp(ratPow10(e+1)) >= 0 {
		e++
	}
	// log10(v) = (e+1) + log10(v / 10^(e+1)) ≈ (e+1) - r/ln10, r = 1 - v/10^(e+1) ∈ (0, 0.9].
	next := ratPow10(e + 1)
	r, _ := new(big.Rat).Quo(new(big.Rat).Sub(next, x), next).Float64()
	delta := r / math.Ln10
	m := float64(e + 1)
	gap := m - math.Nextafter(m, math.Inf(-1)) // расстояние до соседнего double снизу
	if delta < gap/2 {
		return e + 1
	}
	return e
}

// ratPow10 — точное 10^n.
func ratPow10(n int) *big.Rat {
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(absInt(n))), nil)
	if n >= 0 {
		return new(big.Rat).SetInt(p)
	}
	return new(big.Rat).SetFrac(big.NewInt(1), p)
}

// PHPIntCast — эквивалент PHP (int)$float на 64-битной платформе (Zend/zend_operators.h
// zend_dval_to_lval(), PHP 8.3): NaN и ±Inf → 0; значения вне int64 — по модулю 2^64.
func PHPIntCast(d float64) int64 {
	if math.IsInf(d, 0) || math.IsNaN(d) {
		return 0
	}
	// ZEND_DOUBLE_FITS_LONG(d): !(d >= (double)ZEND_LONG_MAX || d < (double)ZEND_LONG_MIN)
	if d < twoPow63 && d >= -twoPow63 {
		return int64(d)
	}
	return dvalToLvalModular(d)
}

const (
	twoPow63 = 9223372036854775808.0
	twoPow64 = 18446744073709551616.0
)

// dvalToLvalModular — zend_dval_to_lval_slow() (Zend/zend_operators.c, 64-битная ветка):
// dmod = fmod(d, 2^64); if (dmod < 0) dmod += 2^64; return (zend_long)(zend_ulong)dmod.
// d здесь всегда целое (|d| ≥ 2^63), поэтому dmod тоже целое и сложение точное.
func dvalToLvalModular(d float64) int64 {
	dmod := math.Mod(d, twoPow64)
	if dmod < 0 {
		dmod += twoPow64
	}
	// (zend_long)(zend_ulong)dmod: значения ≥ 2^63 переходят в отрицательные (dmod целое, вычитание точное).
	if dmod >= twoPow63 {
		dmod -= twoPow64
	}
	return int64(dmod)
}
