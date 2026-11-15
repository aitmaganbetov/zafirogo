package money

import (
	"math"
	"math/big"
)

// Справочные реализации PHP, которых нет в боевом коде (аудит BC-13): исторический эталон PHP 8.3 для round()
// (docs/business-rules.md §2, BR-001: «PHPRound83 — только для справки»). Расчёты идут через PHPRound (PHP 8.4);
// эти функции живут в тестовом файле, чтобы сверять фикстуру testdata/php83_round.json и показывать, чем 8.4
// отличается от 8.3, — и не попадать в бинарник.

// PHPRound83 — эквивалент PHP 8.3 round($value, $places) (PHP_ROUND_HALF_UP). Только для справки и
// сравнения в тестах: прод старого Laravel — PHP 8.4+, расчёты пакета идут через PHPRound.
//
// Перенос построчно из php-src, ветка PHP-8.3 (проверено на теге php-8.3.6):
// ext/standard/math.c _php_math_round(), php_intlog10abs(), php_intpow10(), php_round_helper().
// В 8.3 значение сначала «предокругляется» до 15 значащих цифр (precision_places = 14 - floor(log10|x|)),
// поэтому round(1.005*100) = 101 и round(0.49999999999999994) = 1 (ошибка GH-12143).
// Проверено на фикстуре testdata/php83_round.json (снята PHP 8.3.6 тем же генератором).
func PHPRound83(value float64, places int) float64 {
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
		tmp = phpRoundHelper83(tmp)
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

	tmp = phpRoundHelper83(tmp)

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

// phpRoundHelper83 — php_round_helper() PHP 8.3 для PHP_ROUND_HALF_UP.
func phpRoundHelper83(value float64) float64 {
	if value >= 0 {
		return math.Floor(value + 0.5)
	}
	return math.Ceil(value - 0.5)
}

// phpIntLog10Abs — php_intlog10abs() (только 8.3): floor(log10(|value|)) бинарным поиском по таблице.
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
