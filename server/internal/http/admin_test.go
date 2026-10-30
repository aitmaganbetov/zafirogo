package httpserver

import (
	"reflect"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/http/api"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Каждая операция REST либо служебная/входа, либо закрыта правом маршрута Laravel (withAdminAuth).
func TestEveryAdminOperationHasRight(t *testing.T) {
	public := map[string]bool{
		"GetHealthz": true, "GetReadyz": true, "Login": true, "Logout": true, "LogoutAll": true, "GetMe": true,
		"RefreshSession": true, "SelectClub": true, "SuperadminViewClub": true, "SuperadminLeaveClub": true,
	}
	it := reflect.TypeFor[api.StrictServerInterface]()
	for i := range it.NumMethod() {
		name := it.Method(i).Name
		if public[name] {
			continue
		}
		if _, ok := routeRights[name]; !ok {
			t.Errorf("operation %s has no route right", name)
		}
	}
	for op := range adminRoute {
		if _, ok := it.MethodByName(op); !ok {
			t.Errorf("adminRoute: unknown operation %s", op)
		}
	}
}

func TestRuleMessage(t *testing.T) {
	cases := []struct{ field, rule, param, want string }{
		{"amount", "gt", "", "The amount field must be greater than 0."},
		{"items.0.quantity", "min", "", "The items.0.quantity field must be at least 1."},
		{"sku", "max", "", "The sku field must not be greater than 100 characters."},
		{"quantity", "min", "0", "The quantity field must be at least 0."},
		{"user_id", "in", "", "The selected user id is invalid."},
	}
	for _, c := range cases {
		if got := ruleMessage(c.field, c.rule, c.param); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.field, c.rule, got, c.want)
		}
	}
}

func TestNumberFormat(t *testing.T) {
	for in, want := range map[int64]string{210000: "2 100,00", 12345: "123,45", -100000000: "-1 000 000,00", 5: "0,05"} {
		if got := numberFormat(moneyTiyn(in), ","); got != want {
			t.Errorf("%d: %q, want %q", in, got, want)
		}
	}
}

func moneyTiyn(v int64) money.Tiyn { return money.Tiyn(v) }
