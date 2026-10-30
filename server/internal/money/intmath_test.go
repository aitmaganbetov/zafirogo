package money

import (
	"errors"
	"math"
	"testing"
)

func expectPanic(t *testing.T, want error, f func()) {
	t.Helper()
	defer func() {
		r := recover()
		if err, ok := r.(error); !ok || !errors.Is(err, want) {
			t.Errorf("panic = %v, want %v", r, want)
		}
	}()
	f()
}

func TestIntDiv(t *testing.T) {
	tests := []struct{ a, b, want int64 }{
		{7, 2, 3},
		{-7, 2, -3}, // PHP intdiv(-7, 2) = -3 (усечение к нулю)
		{7, -2, -3},
		{-7, -2, 3},
		{math.MinInt64, 1, math.MinInt64},
	}
	for _, tt := range tests {
		if got := IntDiv(tt.a, tt.b); got != tt.want {
			t.Errorf("IntDiv(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
	expectPanic(t, ErrDivisionByZero, func() { IntDiv(1, 0) })
	expectPanic(t, ErrIntDivOverflow, func() { IntDiv(math.MinInt64, -1) })
}

func TestPHPMod(t *testing.T) {
	tests := []struct{ a, b, want int64 }{
		{7, 3, 1},
		{-7, 3, -1}, // знак делимого, как в PHP
		{7, -3, 1},
		{math.MinInt64, -1, 0},
		{-50, 100, -50},
	}
	for _, tt := range tests {
		if got := PHPMod(tt.a, tt.b); got != tt.want {
			t.Errorf("PHPMod(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
	expectPanic(t, ErrDivisionByZero, func() { PHPMod(1, 0) })
}

func TestCheckedArithmetic(t *testing.T) {
	mul := []struct {
		a, b int64
		ok   bool
	}{
		{0, math.MaxInt64, true},
		{math.MaxInt64, 0, true},
		{3, 4, true},
		{-3, 4, true},
		{math.MaxInt64, 2, false},
		{math.MinInt64, -1, false},
		{-1, math.MinInt64, false},
		{1 << 32, 1 << 31, false},
		{-(1 << 31), 1 << 32, true},
	}
	for _, tt := range mul {
		got, err := MulChecked(tt.a, tt.b)
		if (err == nil) != tt.ok || (tt.ok && got != tt.a*tt.b) {
			t.Errorf("MulChecked(%d, %d) = %d, %v", tt.a, tt.b, got, err)
		}
	}
	add := []struct {
		a, b int64
		ok   bool
	}{
		{1, 2, true},
		{-1, -2, true},
		{math.MaxInt64, 0, true},
		{math.MaxInt64, 1, false},
		{math.MinInt64, -1, false},
		{math.MinInt64, math.MaxInt64, true},
	}
	for _, tt := range add {
		got, err := AddChecked(tt.a, tt.b)
		if (err == nil) != tt.ok || (tt.ok && got != tt.a+tt.b) {
			t.Errorf("AddChecked(%d, %d) = %d, %v", tt.a, tt.b, got, err)
		}
	}
}

func TestAddSub(t *testing.T) {
	if got, err := Add(150, 250); got != 400 || err != nil {
		t.Errorf("Add = %d, %v", got, err)
	}
	if _, err := Add(MaxAmount, 1); !errors.Is(err, ErrOutOfRange) {
		t.Errorf("Add(MaxAmount, 1): %v", err)
	}
	if _, err := Add(math.MaxInt64, 1); !errors.Is(err, ErrOutOfRange) {
		t.Errorf("Add(MaxInt64, 1): %v", err)
	}
	if got, err := Sub(100, 250); got != -150 || err != nil {
		t.Errorf("Sub = %d, %v", got, err)
	}
	if _, err := Sub(0, math.MinInt64); !errors.Is(err, ErrOutOfRange) {
		t.Errorf("Sub(0, MinInt64): %v", err)
	}
}

func TestMulDivRound(t *testing.T) {
	tests := []struct {
		name             string
		a, mul, add, div int64
		want             int64
		err              error
	}{
		// BR-014: поминутная цена intdiv(cents * minutes + 30, 60): 1000 ₸/ч × 7 мин = 116.67 ₸.
		{"BR-014/1000₸×7мин", 100000, 7, 30, 60, 11667, nil},
		// BR-012: скидка 10% с 1500.00 ₸: intdiv(150000*1000+5000, 10000) = 15000.
		{"BR-012/10%", 150000, 1000, 5000, 10000, 15000, nil},
		// BR-032: возврат части intdiv(main_cents * seconds, total).
		{"BR-032/возврат", 50000, 1799, 0, 3600, 24986, nil},
		// Отрицательные: усечение к нулю после прибавления (как в PHP, не «половина вверх»).
		{"отрицательное", -5, 1, 5000, 10000, 0, nil},
		{"переполнение умножения", math.MaxInt64, 2, 0, 1, 0, ErrOutOfRange},
		{"переполнение сложения", math.MaxInt64, 1, 1, 1, 0, ErrOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MulDivRound(tt.a, tt.mul, tt.add, tt.div)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Errorf("MulDivRound = %d, %v; want %d, %v", got, err, tt.want, tt.err)
			}
		})
	}
}

func TestBasisPointsAndPercent(t *testing.T) {
	tests := []struct {
		name    string
		amount  Tiyn
		percent float64
		bp      int64
		want    Tiyn
	}{
		// GS-002: 10000.55 ₸ × 10% = 1000.055 → 1000.06 (Laravel: TopUpBonus::for()).
		{"GS-002/10000.55×10%", FromTengeFloat(10000.55), 10, 1000, 100006},
		{"GS-002/2500×5%", FromTengeFloat(2500), 5, 500, 12500},
		{"BR-012/12.5%", 99999, 12.5, 1250, 12500},
		{"процент 0.145", 10000, 0.145, 14, 14}, // PHP 8.4: round(14.499999999999998) = 14 (8.3 предокруглял до 15)
		{"процент 33.33", 100, 33.33, 3333, 33},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bp := BasisPoints(tt.percent)
			if bp != tt.bp {
				t.Fatalf("BasisPoints(%v) = %d, want %d", tt.percent, bp, tt.bp)
			}
			got, err := PercentOfHalfUp(tt.amount, bp)
			if err != nil || got != tt.want {
				t.Errorf("PercentOfHalfUp(%d, %d) = %d, %v; want %d", tt.amount, bp, got, err, tt.want)
			}
		})
	}
	if _, err := PercentOfHalfUp(math.MaxInt64, 10000); !errors.Is(err, ErrOutOfRange) {
		t.Errorf("переполнение: %v", err)
	}
}
