package migrate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

func fieldOf(t *testing.T, doc []byte, section, key string) string {
	t.Helper()
	var top map[string]json.RawMessage
	var sec map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil || json.Unmarshal(top[section], &sec) != nil {
		t.Fatalf("settings: %v", err)
	}
	return string(sec[key])
}

func TestNormalizeSettings(t *testing.T) {
	in := `{"club":{"session_idle_timeout":"30 сек","booking_interval":"15 мин","agent_idle_logout_minutes":"10",
		"pc_restart_after_session_end":"30 сек","max_cashiers_on_shift":2,"agent_shop_enabled":"no","agent_auto_update":null},
		"finances":{"tax_rate":12,"loyalty_enabled":"1","auto_billing":"0","allow_mixed_payment":"false",
		"loyalty_levels":[{"spent":"50000","percent":"10"},{"spent":10000,"percent":5.5}],
		"topup_bonus_levels":{"a":{"amount":1000,"percent":2}}},
		"guests":{"show_qr_code_on_login":"on","guest_mode_enabled":"yes!"},"extra":5}`
	out, changes, err := normalizeSettings([]byte(in))
	if err != nil || out == nil {
		t.Fatalf("normalize: %v", err)
	}
	for _, c := range []struct{ sec, key, want string }{
		{"club", "session_idle_timeout", "0.5"},         // Q-024: значение экрана minutes()
		{"club", "booking_interval", "30"},              // действовало умолчание gapMinutes()
		{"club", "agent_idle_logout_minutes", "10"},     // строка-число → число
		{"club", "pc_restart_after_session_end", "0.5"}, // действовали 30 с
		{"club", "max_cashiers_on_shift", "2"},
		{"club", "agent_shop_enabled", "false"}, // filter_var("no")
		{"club", "agent_auto_update", "true"},   // null ?? true
		{"finances", "tax_rate", "12"},          // черновой ключ — как есть
		{"finances", "loyalty_enabled", "true"}, // (bool) "1"
		{"finances", "auto_billing", "false"},   // (bool) "0"
		{"finances", "allow_mixed_payment", "true"},
		{"guests", "show_qr_code_on_login", "true"},
		{"guests", "guest_mode_enabled", "false"}, // filter_var("yes!") — false
	} {
		if got := fieldOf(t, out, c.sec, c.key); got != c.want {
			t.Errorf("%s.%s = %s, want %s", c.sec, c.key, got, c.want)
		}
	}
	if !strings.Contains(string(out), `"extra":5`) {
		t.Errorf("top-level extra lost: %s", out)
	}
	if got := fieldOf(t, out, "finances", "loyalty_levels"); got != `{"v":2,"levels":[{"threshold_tiyn":1000000,"percent_bp":550},{"threshold_tiyn":5000000,"percent_bp":1000}]}` {
		t.Errorf("loyalty v2: %s", got)
	}
	before, _ := bonus.ParseSettings([]byte(in))
	after, _ := bonus.ParseSettings(out)
	if !reflect.DeepEqual(before.LoyaltyLevels, after.LoyaltyLevels) || !reflect.DeepEqual(before.TopUpBonusLevels, after.TopUpBonusLevels) {
		t.Fatalf("levels read differently: %+v / %+v", before, after)
	}
	warned := false
	for _, c := range changes {
		if c.Path == "club.booking_interval" && strings.Contains(c.Warning, "15") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning for booking_interval: %+v", changes)
	}
	// Повторная нормализация ничего не меняет (идемпотентность).
	if again, _, err := normalizeSettings(out); err != nil || again != nil {
		t.Fatalf("second pass changed: %s %v", again, err)
	}
	// Без изменений — nil.
	if same, _, _ := normalizeSettings([]byte(`{"club":{"booking_interval":30}}`)); same != nil {
		t.Fatalf("unchanged settings rewritten: %s", same)
	}
	for _, bad := range []string{"", "5", `"x"`, "[]"} {
		if got, _, err := normalizeSettings([]byte(bad)); got != nil || err != nil {
			t.Errorf("%q: %s %v", bad, got, err)
		}
	}
}

func TestFilterValidateBool(t *testing.T) {
	for in, want := range map[string]bool{`true`: true, `false`: false, `1`: true, `1.0`: true, `2`: false, `0`: false,
		`"1"`: true, `" YES "`: true, `"on"`: true, `"false"`: false, `"abc"`: false, `[]`: false, `[1]`: false} {
		if got := filterValidateBool(json.RawMessage(in)); got != want {
			t.Errorf("%s: %v", in, got)
		}
	}
}

func TestEncodeReceiptV2RoundTrip(t *testing.T) {
	for _, raw := range []string{
		`{"items":[{"name":"Coca-Cola 0,5 л","price":450,"total":1350,"quantity":3,"product_id":1},{"name":"Печать","price":"50.5","total":606,"quantity":12,"service_id":1}]}`,
		`{"top_up":{"bonus":12.345,"user_id":9,"balance_type":"balance"}}`,
		`{"items":[],"discount_amount":"1.5"}`,
		`"строка"`,
		`{"items":{"a":1}}`,
	} {
		before, err := cashsvc.DecodeReceipt(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		v2, err := encodeReceiptV2(before)
		if err != nil {
			t.Fatal(err)
		}
		after, err := cashsvc.DecodeReceipt(v2)
		if err != nil || !reflect.DeepEqual(before, after) || !isV2Object(string(v2)) {
			t.Fatalf("%s → %s: %+v / %+v %v", raw, v2, before, after, err)
		}
	}
	r := &cash.Receipt{TopUp: &cash.TopUpReceipt{UserID: 1, Field: "balance", Bonus: money.Tiyn(1235)}}
	v2, _ := encodeReceiptV2(r)
	if !strings.Contains(string(v2), `"bonus_tiyn":1235`) {
		t.Fatalf("v2 top-up: %s", v2)
	}
}
