package subscription

import "testing"

// SubscriptionBilling::kaspi(): isset($value['url']) — без ссылки оплата не настроена.
func TestParseKaspi(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", `{"recipient": "ИП Zafiro"}`, `{"url": null}`, `{"url": ""}`, `[1]`, `"x"`, `{bad`} {
		if k := ParseKaspi([]byte(raw)); k != nil {
			t.Errorf("%q: %+v, want nil", raw, k)
		}
	}
	k := ParseKaspi([]byte(`{"url": "https://pay.kaspi.kz/pay/zafiro", "recipient": "ИП Zafiro"}`))
	if k == nil || k.URL != "https://pay.kaspi.kz/pay/zafiro" || k.Recipient != "ИП Zafiro" {
		t.Fatalf("%+v", k)
	}
	if k := ParseKaspi([]byte(`{"url": "https://pay.kaspi.kz/pay/zafiro", "recipient": null}`)); k == nil || k.Recipient != "" {
		t.Fatalf("без получателя: %+v", k)
	}
}
