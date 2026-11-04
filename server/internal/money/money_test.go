package money

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"testing"
)

func TestTenge(t *testing.T) {
	tests := []struct {
		in   Tiyn
		want string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{7, "0.07"},
		{50, "0.50"},
		{100, "1.00"},
		{123450, "1234.50"},
		{-1, "-0.01"},
		{-50, "-0.50"},    // Q-004: старый formatCents давал "0.-50"
		{-150, "-1.50"},   // старый formatCents: "-1.-50"
		{-1234, "-12.34"}, // как Friends::formatCents(-1234) (BR-002)
		{MaxAmount, "10000000000000.00"},
		{math.MaxInt64, "92233720368547758.07"},
		{math.MinInt64, "-92233720368547758.08"},
	}
	for _, tt := range tests {
		t.Run(strconv.FormatInt(int64(tt.in), 10), func(t *testing.T) {
			if got := tt.in.Tenge(); got != tt.want {
				t.Errorf("Tenge() = %q, want %q", got, tt.want)
			}
			if got := tt.in.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Q-004: фиксируем НАШЕ поведение для отрицательных и отличие от старого formatCents.
func TestTengeNegativeDiffersFromLegacy(t *testing.T) {
	for _, c := range []int64{-1, -50, -99, -150, -12345} {
		ours, legacy := Tiyn(c).Tenge(), LegacyFormatCents(c)
		if ours == legacy {
			t.Errorf("%d: ожидалось расхождение со старым formatCents, оба %q", c, ours)
		}
		if ours[0] != '-' {
			t.Errorf("%d: %q без знака минус", c, ours)
		}
		back, err := ParseTenge(ours)
		if err != nil || int64(back) != c {
			t.Errorf("ParseTenge(%q) = %d, %v; want %d", ours, back, err, c)
		}
	}
	if got := LegacyFormatCents(-50); got != "0.-50" {
		t.Errorf(`LegacyFormatCents(-50) = %q, want "0.-50" (BR-001)`, got)
	}
}

func TestTiynJSONIsInteger(t *testing.T) {
	// В API суммы — целые тиыны (CLAUDE.md «Деньги»): Tiyn не должен реализовывать TextMarshaler/JSONMarshaler.
	b, err := json.Marshal(struct{ A Tiyn }{12345})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"A":12345}` {
		t.Errorf("json = %s", b)
	}
}

func TestValidCheck(t *testing.T) {
	for _, v := range []Tiyn{0, MaxAmount, -MaxAmount, 1} {
		if !v.Valid() || v.Check() != nil {
			t.Errorf("%d должно быть допустимым", v)
		}
	}
	for _, v := range []Tiyn{MaxAmount + 1, -MaxAmount - 1, math.MaxInt64, math.MinInt64} {
		if v.Valid() || !errors.Is(v.Check(), ErrOutOfRange) {
			t.Errorf("%d должно быть вне диапазона", v)
		}
	}
}

func TestParseTenge(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Tiyn
		err  error
	}{
		{"BR-001/1234.5", "1234.5", 123450, nil},
		{"BR-001/0.07", "0.07", 7, nil},
		{"BR-001/12.999", "12.999", 0, ErrTooManyDecimals},
		{"целое", "1500", 150000, nil},
		{"ноль", "0", 0, nil},
		{"ведущие нули", "0001.10", 110, nil},
		{"минус", "-1.50", -150, nil},
		{"минус ноль", "-0.00", 0, nil},
		{"1.500", "1.500", 0, ErrTooManyDecimals},
		{"MaxAmount", "10000000000000", MaxAmount, nil},
		{"-MaxAmount", "-10000000000000.00", -MaxAmount, nil},
		{"MaxAmount+1", "10000000000000.01", 0, ErrOutOfRange},
		{"15 цифр", "100000000000000", 0, ErrOutOfRange},
		{"много нулей впереди", "000000000000000000001.00", 100, nil},
		{"пусто", "", 0, ErrInvalidAmount},
		{"минус", "-", 0, ErrInvalidAmount},
		{"плюс", "+1", 0, ErrInvalidAmount},
		{"пробел", " 1", 0, ErrInvalidAmount},
		{"запятая", "1,5", 0, ErrInvalidAmount},
		{"точка в конце", "5.", 0, ErrInvalidAmount},
		{"точка в начале", ".5", 0, ErrInvalidAmount},
		{"экспонента", "1e3", 0, ErrInvalidAmount},
		{"двойной минус", "--1", 0, ErrInvalidAmount},
		{"буквы в дроби", "1.a", 0, ErrInvalidAmount},
		{"две точки", "1.2.3", 0, ErrInvalidAmount},
		{"юникод-цифра", "١", 0, ErrInvalidAmount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTenge(tt.in)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Errorf("ParseTenge(%q) = %d, %v; want %d, %v", tt.in, got, err, tt.want, tt.err)
			}
		})
	}
}

// TestParseTengeRoundedMySQL — фактические ответы mysql:8.4.13 (sql_mode по умолчанию, STRICT_TRANS_TABLES)
// на INSERT строки в decimal(12,2); снято 2026-10-30 (см. docs/open-questions/stage1-money.md).
// «ошибка» — MySQL отклонил вставку (1366 Incorrect decimal value / 1264 Out of range).
func TestParseTengeRoundedMySQL(t *testing.T) {
	tests := []struct {
		in   string
		want Tiyn
		err  error
	}{
		{"1.005", 101, nil},
		{"-1.005", -101, nil},
		{"1.0049999", 100, nil},
		{"1.0050000001", 101, nil},
		{"0.004", 0, nil},
		{"0.005", 1, nil},
		{"-0.005", -1, nil},
		{"1e-3", 0, nil},
		{"1.5e2", 15000, nil},
		{"1.2345e1", 1235, nil},
		{"+5.555", 556, nil},
		{" 7.125 ", 713, nil},
		{"7.125\t", 713, nil},
		{"12.999", 1300, nil},
		{"100.005", 10001, nil},
		{"999.995", 100000, nil},
		{"-999.995", -100000, nil},
		{".5", 50, nil},
		{"5.", 500, nil},
		{"1E2", 10000, nil},
		{"0.0000000000000000000000000000000000005", 0, nil},
		{"9999999999.994", 999999999999, nil},
		{"9999999999.99", 999999999999, nil},
		// MySQL: 1264 для decimal(12,2); у нас предел — MaxAmount, колонку проверяет вызывающий.
		{"9999999999.995", 1000000000000, nil},
		{"123456789012.34", 12345678901234, nil},
		// MySQL: 1366 Incorrect decimal value.
		{"1,5", 0, ErrInvalidAmount},
		{"abc", 0, ErrInvalidAmount},
		{"0x1A", 0, ErrInvalidAmount},
		{"", 0, ErrInvalidAmount},
		{"1.5x", 0, ErrInvalidAmount},
		{"1e400", 0, ErrOutOfRange}, // MySQL: 1366
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTengeRounded(tt.in)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Errorf("ParseTengeRounded(%q) = %d, %v; want %d, %v", tt.in, got, err, tt.want, tt.err)
			}
		})
	}
}

func TestParseTengeRounded(t *testing.T) {
	tests := []struct {
		in   string
		want Tiyn
		err  error
	}{
		{"0", 0, nil},
		{"-0", 0, nil},
		{"000", 0, nil},
		{"0.0e5", 0, nil},
		{"1e-10000000000000", 0, nil},
		{"1e+2", 10000, nil},
		{"1.23456789e5", 12345679, nil},
		{"12345678901234.99", 0, ErrOutOfRange}, // 15 цифр целой части
		{"1e13", 1000000000000000, nil},         // ровно MaxAmount
		{"1.00000000000001e13", 1000000000000001, ErrOutOfRange},
		{"-1e13", -1000000000000000, nil},
		{"9999999999999.995", MaxAmount, nil},
		{"10000000000000.005", 0, ErrOutOfRange}, // округление выводит за MaxAmount
		{"\n\v\f\r 3", 300, nil},
		{"+", 0, ErrInvalidAmount},
		{".", 0, ErrInvalidAmount},
		{"-.", 0, ErrInvalidAmount},
		{"1e", 0, ErrInvalidAmount},
		{"1e+", 0, ErrInvalidAmount},
		{"1e-x", 0, ErrInvalidAmount},
		{"e5", 0, ErrInvalidAmount},
		{"1.2.3", 0, ErrInvalidAmount},
		{"1 000", 0, ErrInvalidAmount},
		{"--1", 0, ErrInvalidAmount},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTengeRounded(tt.in)
			if !errors.Is(err, tt.err) || (err == nil && got != tt.want) {
				t.Errorf("ParseTengeRounded(%q) = %d, %v; want %d, %v", tt.in, got, err, tt.want, tt.err)
			}
			// Всё, что принимает ParseTengeRounded, принимает и Laravel `numeric` (is_numeric).
			if err == nil && !PHPIsNumeric(tt.in) {
				t.Errorf("%q принят, но is_numeric = false", tt.in)
			}
		})
	}
}

func TestFromTengeFloat(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want Tiyn
	}{
		{"BR-001/100.005", 100.005, 10001},
		{"BR-001/1.005", 1.005, 100}, // PHP 8.4; в 8.3 было 101
		{"0.145", 0.145, 14},         // PHP 8.4; в 8.3 было 15
		{"0.285", 0.285, 28},         // PHP 8.4; в 8.3 было 29
		{"GS-002/10000.55", 10000.55, 1000055},
		{"0.1+0.2", 0.1 + 0.2, 30},
		{"-12.34", -12.34, -1234},
		{"-1.005", -1.005, -100},
		{"NaN", math.NaN(), 0},
		{"+Inf", math.Inf(1), 0},
		{"-Inf", math.Inf(-1), 0},
		{"1e17 по модулю 2^64", 1e17, Tiyn(PHPIntCast(1e19))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromTengeFloat(tt.in); got != tt.want {
				t.Errorf("FromTengeFloat(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
	// 1.005*100 во float64 = 100.49999999999999: PHP 8.4 (как и math.Round) даёт 100, PHP 8.3 предокруглял до 101.
	if math.Round(mul(1.005, 100)) != 100 || PHPRound83(mul(1.005, 100), 0) != 101 {
		t.Fatal("ожидалось расхождение 8.3 и 8.4 на 1.005*100")
	}
}

func TestFromTengeFloatDecimal(t *testing.T) {
	tests := []struct {
		in   float64
		want Tiyn
		err  error
	}{
		{0.1 + 0.2, 30, nil},
		{100.005, 10001, nil},
		{2.675, 268, nil}, // MySQL: d + 2.675 → 2.68 (см. TestParseTengeRoundedMySQL)
		{1e-5, 0, nil},
		{-7.125, -713, nil},
		{1e25, 0, ErrOutOfRange},
		{math.NaN(), 0, ErrInvalidAmount},
	}
	for _, tt := range tests {
		got, err := FromTengeFloatDecimal(tt.in)
		if !errors.Is(err, tt.err) || (err == nil && got != tt.want) {
			t.Errorf("FromTengeFloatDecimal(%v) = %d, %v; want %d, %v", tt.in, got, err, tt.want, tt.err)
		}
	}
}

func TestFloat(t *testing.T) {
	if got := Tiyn(123450).Float(); got != 1234.5 {
		t.Errorf("Float() = %v", got)
	}
	if got := Tiyn(-7).Float(); got != -0.07 {
		t.Errorf("Float() = %v", got)
	}
}

func TestFormatFloat2(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{1000.055, "1000.06"},
		{1.005, "1.01"}, // наивно math.Round(1.005*100)/100 = 1.00
		{-1.005, "-1.01"},
		{-0.001, "0.00"}, // «-0.00» не бывает
		{math.Copysign(0, -1), "0.00"},
		{1500, "1500.00"},
		{1e20, "100000000000000000000.00"},
		{math.NaN(), "nan"},
		{math.Inf(1), "inf"},
		{math.Inf(-1), "-inf"},
	}
	for _, tt := range tests {
		if got := FormatFloat2(tt.in); got != tt.want {
			t.Errorf("FormatFloat2(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPHPRoundKnown(t *testing.T) {
	tests := []struct {
		x      float64
		places int
		want   float64
	}{
		// docs/business-rules.md BR-001 (проверено php:8.4.27-cli, php -r).
		{12.345, 2, 12.35},
		{mul(100.005, 100), 0, 10001},
		{mul(1.005, 100), 0, 100}, // 8.3: 101
		{mul(0.145, 100), 0, 14},  // 8.3: 15
		{1.005, 2, 1.01},          // наивно math.Round(x*100)/100 = 1.00
		{0.285, 2, 0.29},          // наивно 0.28
		{2.675, 2, 2.68},
		{0.49999999999999994, 0, 0}, // GH-12143 исправлен в 8.4 (8.3: 1)
		{-0.5, 0, -1},
		{0, 2, 0},
		{math.Inf(1), 2, math.Inf(1)},
		{1234.5678, math.MaxInt, 1234.5678},
		{1234.5678, math.MinInt, 0},
		{1234.5678, -2, 1200},
		{math.MaxFloat64, -308, math.MaxFloat64}, // strtod("2e308") → Inf → возвращается исходное значение
	}
	for _, tt := range tests {
		got := PHPRound(tt.x, tt.places)
		if math.Float64bits(got) != math.Float64bits(tt.want) {
			t.Errorf("PHPRound(%v, %d) = %v, want %v", tt.x, tt.places, got, tt.want)
		}
	}
	if !math.IsNaN(PHPRound(math.NaN(), 2)) {
		t.Error("NaN")
	}
	// Те же примеры в PHP 8.3 (PHPRound83, справочно).
	for _, tt := range []struct {
		x      float64
		places int
		want   float64
	}{
		{mul(1.005, 100), 0, 101},
		{mul(0.145, 100), 0, 15},
		{0.49999999999999994, 0, 1},
		{1.005, 2, 1.01},
		{math.Inf(-1), 2, math.Inf(-1)},
	} {
		if got := PHPRound83(tt.x, tt.places); math.Float64bits(got) != math.Float64bits(tt.want) {
			t.Errorf("PHPRound83(%v, %d) = %v, want %v", tt.x, tt.places, got, tt.want)
		}
	}
}

// TestPHPRoundPlacesZeroIsMathRound — в PHP 8.4 round($x) без знаков — это math.Round:
// exponent = 1, целая часть floor/ceil точна, edge = |целая ± 0.5| точна (|x| < 2^52 у нецелых).
// Поэтому формулы (int) round($x * 100) переносятся как math.Round(x*100) — но round($x, 2) нет.
func TestPHPRoundPlacesZeroIsMathRound(t *testing.T) {
	r := rand.New(rand.NewPCG(20261030, 1))
	check := func(x float64) {
		if got, want := PHPRound(x, 0), math.Round(x); math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("PHPRound(%v, 0) = %v, math.Round = %v", x, got, want)
		}
	}
	for i := 0; i < 200000; i++ {
		check(math.Float64frombits(r.Uint64()))
		check(float64(r.Int64N(1_000_000_000_000)) / 1000 * 100)
		check(float64(r.Int64N(1<<53)) + 0.5)
	}
	for _, x := range []float64{0.5, -0.5, 1.5, 2.5, 0.49999999999999994, mul(1.005, 100), 4503599627370495.5, math.Copysign(0, -1)} {
		check(x)
	}
}

func TestPHPIntCast(t *testing.T) {
	tests := []struct {
		in   float64
		want int64
	}{
		{1.9, 1},
		{-1.9, -1},
		{1e19, -8446744073709551616},
		{-1e19, 8446744073709551616},
		{9.3e18, -9146744073709551616},
		{18446744073709551616, 0},
		{-9223372036854775808, math.MinInt64},
		{math.NaN(), 0},
		{math.Inf(-1), 0},
	}
	for _, tt := range tests {
		if got := PHPIntCast(tt.in); got != tt.want {
			t.Errorf("PHPIntCast(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestPHPIntLog10AbsMatchesTable(t *testing.T) {
	// В диапазоне таблицы php_intlog10abs — это «наибольшее e, при котором |v| ≥ 1e<e> (как double)».
	for i, p := range log10Table {
		e := i - 8
		for _, v := range []float64{p, math.Nextafter(p, 0), math.Nextafter(p, math.Inf(1)), 3 * p} {
			want := e
			if v < p {
				want = e - 1
			}
			if v >= 1e-8 && v <= 1e22 {
				if got := phpIntLog10Abs(-v); got != want {
					t.Errorf("phpIntLog10Abs(%v) = %d, want %d", v, got, want)
				}
			}
		}
	}
	if got := phpIntLog10Abs(1e-9); got != -9 {
		t.Errorf("1e-9: %d", got)
	}
	// Вне таблицы — как floor(log10()) из libc (корректно округлённый log10).
	for _, tt := range []struct {
		v    float64
		want int
	}{
		{1e30, 30},     // math.Log10(1e30) = 29.999999999999996 в Go
		{1e23, 23},     // double 1e23 < 10^23, но log10 округляется до 23
		{1e-323, -324}, // субнормальное; math.Log10 даёт заведомо неверную оценку
		{3e-9, -9},
		{math.MaxFloat64, 308},
		{5e-324, -324},
	} {
		if got := phpIntLog10Abs(tt.v); got != tt.want {
			t.Errorf("phpIntLog10Abs(%v) = %d, want %d", tt.v, got, tt.want)
		}
	}
}

func TestPHPIntPow10(t *testing.T) {
	if phpIntPow10(-1) != 0.1 || phpIntPow10(22) != 1e22 || phpIntPow10(24) != 1e24 || !math.IsInf(phpIntPow10(309), 1) {
		t.Error("phpIntPow10")
	}
	// glibc pow(10, n) ≠ корректно округлённому 1e<n> только для 23 и 210 (php:8.4.27-cli: pow(10.0, 23.0) = 1.0000000000000001E+23).
	for _, n := range []int{23, 210} {
		v, _ := strconv.ParseFloat("1e"+strconv.Itoa(n), 64)
		if phpIntPow10(n) != math.Nextafter(v, math.Inf(1)) {
			t.Errorf("phpIntPow10(%d)", n)
		}
	}
}

// mul не даёт компилятору свернуть константное выражение (Go считает константы точно, PHP — во float64).
//
//go:noinline
func mul(a, b float64) float64 { return a * b }

func TestParseTengeViaFloat(t *testing.T) {
	tests := []struct {
		in   string
		want Tiyn
		err  error
	}{
		{"1.005", 101, nil},
		{"1.00499999999999999", 101, nil}, // (float) → 1.005 → "1.005" → 1.01; по строке было бы 1.00
		{" 2500 ", 250000, nil},
		{"10000.55", 1000055, nil},
		{"1,5", 0, ErrInvalidAmount},
		{"1e400", 0, ErrInvalidAmount}, // (float) = INF → "INF" не число для MySQL
	}
	for _, tt := range tests {
		got, err := ParseTengeViaFloat(tt.in)
		if !errors.Is(err, tt.err) || got != tt.want {
			t.Errorf("ParseTengeViaFloat(%q) = %d, %v; want %d, %v", tt.in, got, err, tt.want, tt.err)
		}
	}
	if r, _ := ParseTengeRounded("1.00499999999999999"); r != 100 {
		t.Errorf("ParseTengeRounded по строке = %d, want 100", r)
	}
}
