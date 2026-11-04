package pricing

import (
	"errors"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

func tiyn(v money.Tiyn) *money.Tiyn { return &v }

func i64(v int64) *int64 { return &v }

// assertErr: want == nil — ошибки быть не должно; иначе errors.Is(err, want).
func assertErr(t *testing.T, err, want error) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
		return
	}
	if !errors.Is(err, want) {
		t.Fatalf("ошибка = %v, want %v", err, want)
	}
}

func mustRules(t *testing.T, json string) PaymentRules {
	t.Helper()
	f, err := ParseFinances([]byte(json))
	if err != nil {
		t.Fatal(err)
	}
	return RulesFromFinances(f)
}

func moneyT(v int64) money.Tiyn { return money.Tiyn(v) }
