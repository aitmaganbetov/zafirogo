package httpx

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

func TestRuleMessage(t *testing.T) {
	cases := []struct{ field, rule, param, want string }{
		{"amount", "gt", "", "The amount field must be greater than 0."},
		{"items.0.quantity", "min", "", "The items.0.quantity field must be at least 1."},
		{"sku", "max", "", "The sku field must not be greater than 100 characters."},
		{"quantity", "min", "0", "The quantity field must be at least 0."},
		{"user_id", "in", "", "The selected user id is invalid."},
		// Решение Q-002 (docs/DECISIONS.md): > 2 знаков после точки.
		{"top_up_amount", "decimal", "", "The top up amount field must have 0-2 decimal places."},
	}
	for _, c := range cases {
		if got := RuleMessage(c.field, c.rule, c.param); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.field, c.rule, got, c.want)
		}
	}
}

func TestNumberFormat(t *testing.T) {
	for in, want := range map[int64]string{210000: "2 100,00", 12345: "123,45", -100000000: "-1 000 000,00", 5: "0,05"} {
		if got := NumberFormat(money.Tiyn(in), ","); got != want {
			t.Errorf("%d: %q, want %q", in, got, want)
		}
	}
}

// RequiredHeaderError — как в сгенерированных пакетах разделов.
type RequiredHeaderError struct {
	ParamName string
	Err       error
}

func (e *RequiredHeaderError) Error() string { return "header " + e.ParamName }
func (e *RequiredHeaderError) Unwrap() error { return e.Err }

func TestMissingHeader(t *testing.T) {
	err := fmt.Errorf("wrap: %w", &RequiredHeaderError{ParamName: "Idempotency-Key"})
	if !missingHeader(err, "Idempotency-Key") {
		t.Fatal("not detected")
	}
	if missingHeader(&RequiredHeaderError{ParamName: "X"}, "Idempotency-Key") || missingHeader(errors.New("x"), "Idempotency-Key") {
		t.Fatal("false positive")
	}
}

type fakeAPI interface {
	A()
	B()
	C()
}

func TestAccessUncovered(t *testing.T) {
	a := Access{Routes: map[string]string{"A": "cash-register.index", "Z": "cash-register.index"}, Public: []string{"B"}}
	got := a.Uncovered(reflect.TypeFor[fakeAPI]())
	want := []string{"no right: C", "unknown operation in Routes: Z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}
