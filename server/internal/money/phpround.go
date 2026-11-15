package money

import (
	"math"
	"strconv"
	"strings"
)

// PHPRound — точный эквивалент PHP 8.4 round($value, $places) (режим PHP_ROUND_HALF_UP).
// Старый Laravel работает на PHP ≥ 8.4.1 (его composer.lock: symfony 8, PHPUnit 12), поэтому
// все функции пакета, повторяющие PHP-формулы, считают через PHPRound. Поведение 8.3 — PHPRound83.
//
// Перенос построчно из php-src, тег php-8.4.27 (в ветке PHP-8.5 этот код тот же):
// ext/standard/math.c _php_math_round(), php_round_helper(), php_round_get_basic_edge_case(), php_intpow10().
// Алгоритм 8.4 (RFC https://wiki.php.net/rfc/change_the_edge_case_of_round) не предокругляет до 15 цифр:
//  1. целая часть |value * 10^places| берётся floor/ceil и исправляется на 1, если (целая ± 1) / 10^places == value
//     (так 0.285 * 1e10 = 2849999999.9999995 получает 2850000000);
//  2. «половина» засчитывается, только если |value| ≥ (целая + 0.5) / 10^places во float64, то есть
//     число в десятичной записи действительно не меньше x.5.
//
// Отсюда отличия от 8.3 (и совпадения с ним):
//   - round(1.005, 2) = 1.01 и round(0.285, 2) = 0.29 — как в 8.3 (литерал 1.005 «и есть» 1.005);
//   - round(1.005 * 100) = 100, round(0.145 * 100) = 14, round(0.49999999999999994) = 0 — в 8.3 было 101, 15, 1:
//     произведение во float64 уже меньше половины (100.49999999999999), и 8.4 его не «доокругляет».
//
// Эквивалентность доказана дифференциальным тестом против php 8.4.27
// (testdata/php_round.json, генератор tools/php-oracle/gen-round-fixture.php).
func PHPRound(value float64, places int) float64 {
	// if (!zend_finite(value) || value == 0.0) return value;
	if math.IsInf(value, 0) || math.IsNaN(value) || value == 0 {
		return value
	}
	places = clampCInt(places)
	// if (places == 0 && value == trunc(value)) return value;
	if places == 0 && value == math.Trunc(value) {
		return value
	}
	if places < math.MinInt32+1 {
		places = math.MinInt32 + 1
	}
	exponent := phpIntPow10(absInt(places))

	var tmp, tmp2 float64
	if value >= 0 {
		tmp = math.Floor(scale(value, exponent, places))
		tmp2 = tmp + 1
	} else {
		tmp = math.Ceil(scale(value, exponent, places))
		tmp2 = tmp - 1
	}
	// if ((places > 0 ? tmp_value2 / exponent : tmp_value2 * exponent) == value) — обратное к scale действие
	if unscale(tmp2, exponent, places) == value {
		tmp = tmp2
	}

	// This value is beyond our precision, so rounding it is pointless
	if math.Abs(tmp) >= 1e16 {
		return value
	}

	// php_round_helper(), PHP_ROUND_HALF_UP: edge_case = php_round_get_basic_edge_case()
	if math.Abs(value) >= math.Abs(unscale(tmp+math.Copysign(0.5, tmp), exponent, places)) {
		tmp += math.Copysign(1, tmp)
	}

	if absInt(places) < 23 {
		return unscale(tmp, exponent, places)
	}
	// snprintf(buf, 39, "%15fe%d", tmp_value, -places); tmp_value = zend_strtod(buf, NULL);
	v := PHPStringToFloat(phpSnprintfRoundBuf(tmp, places))
	// couldn't convert to string and back
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return value
	}
	return v
}

// scale — `places > 0 ? v * exponent : v / exponent` из _php_math_round() PHP 8.4.
func scale(v, exponent float64, places int) float64 {
	if places > 0 {
		return v * exponent
	}
	return v / exponent
}

// unscale — обратное действие `places > 0 ? v / exponent : v * exponent`.
func unscale(v, exponent float64, places int) float64 {
	if places > 0 {
		return v / exponent
	}
	return v * exponent
}

// phpSnprintfRoundBuf — snprintf(buf, 39, "%15fe%d", tmp, -places) PHP-шным форматтером (8.3 и 8.4)
// (main/snprintf.c format_converter → php_conv_fp → __cvt):
//   - ±Inf → "INF" (без знака) — zend_strtod такую строку читает как 0, и round() возвращает +0;
//     Inf сюда попадает только в 8.3, когда php_intpow10(precision_places) переполняется (|x| < ~1e-294);
//   - знак ставится только при num < 0, а __cvt для нуля сбрасывает знак: -0.0 → "0.000000";
//   - ширина 15 (пробелы слева).
//
// NaN сюда не попадает (value конечно, множители конечны или +Inf), а обрезка буфера до 38 байт
// не срабатывает: |tmp| < 1e16 даёт ≤ 25 символов, показатель — ≤ 12.
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

var pow10Table = [...]float64{
	1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11,
	1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
}

// phpIntPow10 — php_intpow10(): таблица для 0..22, иначе pow(10, power) из libc.
func phpIntPow10(power int) float64 {
	if power < 0 || power > 22 {
		// glibc pow() не округлён корректно (погрешность до ~0.52 ulp), а math.Pow в Go ошибается чаще
		// (math.Pow(10, 244) отличается на ulp). Берём корректно округлённое 1e<power> (ParseFloat;
		// переполнение → +Inf, как pow) и поправляем степени, где glibc отвечает иначе. Список сверен
		// с PHP 8.4.27 (php:8.4.27-cli, glibc x86_64) для всех power 23..308: только 23 (точная середина
		// между соседними double, glibc уходит вверх) и 210 — на 1 ulp больше.
		v, _ := strconv.ParseFloat("1e"+strconv.Itoa(power), 64)
		if power == 23 || power == 210 {
			v = math.Nextafter(v, math.Inf(1))
		}
		return v
	}
	return pow10Table[power]
}

var log10Table = [...]float64{
	1e-8, 1e-7, 1e-6, 1e-5, 1e-4, 1e-3, 1e-2, 1e-1, 1e0, 1e1, 1e2,
	1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11, 1e12, 1e13,
	1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22,
}

// PHPIntCast — эквивалент PHP (int)$float на 64-битной платформе (Zend/zend_operators.h
// zend_dval_to_lval(), в 8.3 и 8.4 одинаково): NaN и ±Inf → 0; значения вне int64 — по модулю 2^64.
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
