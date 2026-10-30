package billing

import (
	"errors"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// BR-058: round((float) top_up_amount, 2) и правило numeric|min:0.01|max:10000000.
func TestParseOwnerAdjustmentAmount(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want money.Tiyn
		err  bool
	}{
		{"BR-058/1234.565", "1234.565", 123457, false},
		{"GS-018/500", "500", 50000, false},
		{"GS-018/40.5", "40.5", 4050, false},
		{"GS-024/30", "30", 3000, false},
		{"BR-058/min", "0.01", 1, false},
		{"BR-058/max", "10000000", 1_000_000_000, false},
		{"BR-058/below-min", "0.009", 0, true},
		{"BR-058/above-max", "10000000.01", 0, true},
		{"BR-058/zero", "0", 0, true},
		{"BR-058/not-numeric", "12abc", 0, true},
		{"BR-058/comma", "1,5", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseOwnerAdjustmentAmount(c.in)
			if c.err {
				if !errors.Is(err, ErrBalanceAmountOutOfRange) {
					t.Fatalf("err = %v, want ErrBalanceAmountOutOfRange", err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %d, %v; want %d", got, err, c.want)
			}
		})
	}
}

// number_format($x, 2, '.', ' ') — текст ошибки списания владельцем (BR-058, GS-024).
func TestNumberFormatSpaced(t *testing.T) {
	cases := map[money.Tiyn]string{
		7000:          "70.00",
		0:             "0.00",
		123456789:     "1 234 567.89",
		100000:        "1 000.00",
		99999:         "999.99",
		-123450:       "-1 234.50",
		-5:            "-0.05",
		1_000_000_000: "10 000 000.00",
	}
	for in, want := range cases {
		if got := numberFormatSpaced(in); got != want {
			t.Errorf("numberFormatSpaced(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestBalanceErrorText(t *testing.T) {
	e := &BalanceError{Field: "top_up_amount", Message: msgWriteOffMoreThanFunds, Params: map[string]string{"amount": "70.00"}}
	if got := e.Error(); got != "top_up_amount: На балансе клиента меньше суммы списания: 70.00." {
		t.Fatalf("Error() = %q", got)
	}
}
