package customers

import (
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
)

func TestNormalizePhone(t *testing.T) {
	// PhoneNumber::normalize() (Q-033): ведущая 7 или 8 у 11 цифр отрезается.
	for in, want := range map[string]string{
		"+7 700 123-45-67": "+77001234567", "87001234567": "+77001234567", "7001234567": "+77001234567",
		"": "", "12": "+712",
	} {
		if got := NormalizePhone(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestPercentFormat(t *testing.T) {
	// number_format($x, 2, '.', ' ') — округление PHP (половина от нуля).
	for x, want := range map[float64]string{0: "0.00", 5: "5.00", 12.5: "12.50", 1.005: "1.01", 99.995: "100.00", 1234.5: "1 234.50"} {
		if got := PercentFormat(x); got != want {
			t.Errorf("%v: %q, want %q", x, got, want)
		}
	}
}

func TestCustomerViewLoyalty(t *testing.T) {
	raw, err := bonus.DecodeJSON([]byte(`[{"spent": 10000, "percent": 7}]`))
	if err != nil {
		t.Fatal(err)
	}
	levels := bonus.Normalize(raw, "spent")
	v := viewCtx{levels: levels, locked: map[int64]bool{2: true}}
	c := v.customer(customerRow{UserID: 2, Discount: "5.00", TotalSpent: 1_000_000})
	if c.DiscountEffective != "7.00" || !c.DiscountCumulative || !c.AccountLocked {
		t.Fatalf("%+v", c)
	}
	c = v.customer(customerRow{UserID: 3, Discount: "10.00", TotalSpent: 1_000_000})
	if c.DiscountEffective != "10.00" || c.DiscountCumulative || c.AccountLocked {
		t.Fatalf("%+v", c)
	}
}

func TestNewPassword(t *testing.T) {
	for range 200 {
		pw, err := NewPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(pw) != 8 || !strings.ContainsAny(pw, passwordDigits) || !strings.ContainsAny(pw, passwordLetters) ||
			strings.Trim(pw, passwordLetters+passwordDigits) != "" {
			t.Fatalf("bad password %q", pw)
		}
	}
}
