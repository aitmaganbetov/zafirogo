package money

import (
	"errors"
	"math/big"
	"strings"
	"testing"
)

// FuzzParseTenge — круговое преобразование: строка → Tiyn → Tenge() → Tiyn, и согласованность
// строгого и округляющего парсеров. Запуск: go test -fuzz=FuzzParseTenge ./internal/money/
func FuzzParseTenge(f *testing.F) {
	for _, s := range []string{"0", "1234.5", "0.07", "-1.50", "12.999", "10000000000000", "-0.00", "1.", ".5", "1e3", " 1", "0001.10"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := ParseTenge(s)
		if err != nil {
			if !errors.Is(err, ErrInvalidAmount) && !errors.Is(err, ErrTooManyDecimals) && !errors.Is(err, ErrOutOfRange) {
				t.Fatalf("ParseTenge(%q): неизвестная ошибка %v", s, err)
			}
			return
		}
		if !v.Valid() {
			t.Fatalf("ParseTenge(%q) = %d вне MaxAmount", s, v)
		}
		str := v.Tenge()
		back, err := ParseTenge(str)
		if err != nil || back != v {
			t.Fatalf("круг: %q → %d → %q → %d, %v", s, v, str, back, err)
		}
		// Строгий формат — подмножество формата MySQL/is_numeric, и без лишних знаков округлять нечего.
		r, err := ParseTengeRounded(s)
		if err != nil || r != v {
			t.Fatalf("ParseTengeRounded(%q) = %d, %v; ParseTenge = %d", s, r, err, v)
		}
		if !PHPIsNumeric(s) {
			t.Fatalf("%q принят ParseTenge, но is_numeric = false", s)
		}
	})
}

// FuzzTengeRoundTrip — любое допустимое число тиынов печатается и читается обратно без потерь.
func FuzzTengeRoundTrip(f *testing.F) {
	for _, v := range []int64{0, 1, -1, 50, -50, 123450, int64(MaxAmount), -int64(MaxAmount)} {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, v int64) {
		tv := Tiyn(v)
		back, err := ParseTenge(tv.Tenge())
		if !tv.Valid() {
			if !errors.Is(err, ErrOutOfRange) {
				t.Fatalf("%d вне MaxAmount, а ParseTenge(%q) = %d, %v", v, tv.Tenge(), back, err)
			}
			return
		}
		if err != nil || back != tv {
			t.Fatalf("%d → %q → %d, %v", v, tv.Tenge(), back, err)
		}
	})
}

// FuzzParseTengeRounded сверяет округление с эталоном на big.Rat (половина от нуля до 0.01).
func FuzzParseTengeRounded(f *testing.F) {
	for _, s := range []string{"1.005", "-1.005", "1.0049999", "1e-3", "1.5e2", " +5.555 ", ".5", "5.", "9999999999.995", "1e13", "1e400"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ParseTengeRounded(s)
		if err != nil {
			if errors.Is(err, ErrInvalidAmount) && PHPIsNumeric(s) && !strings.ContainsAny(s, "eE") {
				t.Fatalf("ParseTengeRounded(%q): %v, хотя is_numeric", s, err)
			}
			return
		}
		if !PHPIsNumeric(s) {
			t.Fatalf("ParseTengeRounded(%q) принят, но is_numeric = false", s)
		}
		want, ok := refRound(s)
		if ok && want != got {
			t.Fatalf("ParseTengeRounded(%q) = %d, эталон %d", s, got, want)
		}
	})
}

// refRound — эталон: точное рациональное значение × 100, половина от нуля. Экспоненты > 400 по модулю
// не проверяем (эталон был бы слишком дорогим).
func refRound(s string) (Tiyn, bool) {
	s = strings.Trim(s, " \t\n\r\v\f")
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp := strings.TrimLeft(s[i+1:], "+-")
		if len(exp) > 3 {
			return 0, false
		}
	}
	s = strings.TrimPrefix(s, "+")
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, false
	}
	r.Mul(r, big.NewRat(100, 1))
	neg := r.Sign() < 0
	r.Abs(r)
	q := new(big.Int).Quo(r.Num(), r.Denom())
	rem := new(big.Rat).Sub(r, new(big.Rat).SetInt(q))
	if rem.Cmp(big.NewRat(1, 2)) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if neg {
		q.Neg(q)
	}
	if !q.IsInt64() {
		return 0, false
	}
	return Tiyn(q.Int64()), true
}
