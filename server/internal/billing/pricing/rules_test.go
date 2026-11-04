package pricing

import (
	"testing"
)

// GAP-009: пределы значений настроек; GS-055: умолчания автобиллинга; GS-052: сохранённые значения читаются.
func TestPaymentRules_Clamps(t *testing.T) {
	cases := []struct {
		name     string
		finances string // JSON всего clubs.settings
		check    func(PaymentRules) any
		want     any
	}{
		{"GAP-009/bonus_max_percent/150", `{"finances":{"bonus_max_percent":150}}`, bmp, int64(100)},
		{"GAP-009/bonus_max_percent/-5", `{"finances":{"bonus_max_percent":-5}}`, bmp, int64(0)},
		{"GAP-009/bonus_max_percent/abc", `{"finances":{"bonus_max_percent":"abc"}}`, bmp, int64(100)},
		{"GAP-009/bonus_max_percent/30.7", `{"finances":{"bonus_max_percent":30.7}}`, bmp, int64(30)},
		{"GAP-009/bonus_max_percent/missing", `{"finances":{}}`, bmp, int64(100)},
		{"GAP-009/auto_billing_pause_minutes/90", `{"finances":{"auto_billing_pause_minutes":90}}`, pause, int64(60)},
		{"GAP-009/auto_billing_pause_minutes/-1", `{"finances":{"auto_billing_pause_minutes":-1}}`, pause, int64(0)},
		{"GAP-009/auto_billing_pause_minutes/missing", `{}`, pause, int64(5)},
		{"GAP-009/discount_scope/never", `{"finances":{"discount_scope":"never"}}`, scope, ScopeTariffs},
		{"GAP-009/offered/30_no_mixed", `{"finances":{"bonus_max_percent":30}}`, offered(false), int64(0)},
		{"GAP-009/offered/100_no_mixed", `{"finances":{"bonus_max_percent":100}}`, offered(false), int64(100)},
		{"GAP-009/offered/100_main_only", `{"finances":{"bonus_max_percent":100}}`, offered(true), int64(0)},
		{"GS-055/по_умолчанию/auto_billing", `{"finances":{"currency":"KZT"}}`, func(r PaymentRules) any { return r.AutoBilling }, false},
		{"GS-055/по_умолчанию/pause", `{"finances":{"currency":"KZT"}}`, pause, int64(5)},
		{"GS-055/включено/auto_billing", `{"finances":{"auto_billing":true,"auto_billing_pause_minutes":3}}`, func(r PaymentRules) any { return r.AutoBilling }, true},
		{"GS-055/включено/pause", `{"finances":{"auto_billing":true,"auto_billing_pause_minutes":3}}`, pause, int64(3)},
		{"GS-052/hourly_40_mixed", `{"finances":{"discount_scope":"hourly","bonus_max_percent":40,"allow_mixed_payment":true}}`,
			func(r PaymentRules) any { return [3]any{r.DiscountScope, r.BonusMaxPercent, r.MixedPayment} }, [3]any{ScopeHourly, int64(40), true}},
		{"GS-053/whole_balance/по_умолчанию", `null`, func(r PaymentRules) any { return r.WholeBalance }, false},
		{"GS-053/whole_balance/включено", `{"finances":{"allow_whole_balance":true}}`, func(r PaymentRules) any { return r.WholeBalance }, true},
		// Приведения PHP (is_numeric / (int) / (bool)) к значениям, которые могли попасть в JSON не через форму.
		{"PHP/bonus_max_percent/строка_50", `{"finances":{"bonus_max_percent":" 50 "}}`, bmp, int64(50)},
		{"PHP/bonus_max_percent/строка_1e1", `{"finances":{"bonus_max_percent":"1e1"}}`, bmp, int64(10)},
		{"PHP/bonus_max_percent/1e1", `{"finances":{"bonus_max_percent":1e1}}`, bmp, int64(10)},
		{"PHP/bonus_max_percent/true", `{"finances":{"bonus_max_percent":true}}`, bmp, int64(100)},
		{"PHP/bonus_max_percent/null", `{"finances":{"bonus_max_percent":null}}`, bmp, int64(100)},
		{"PHP/bonus_max_percent/огромное", `{"finances":{"bonus_max_percent":1e30}}`, bmp, int64(100)}, // (int)1e30 = 5076964154930102272 (PHP 8.4)
		{"PHP/bonus_max_percent/12abc", `{"finances":{"bonus_max_percent":"12abc"}}`, bmp, int64(100)},
		{"PHP/allow_mixed_payment/строка_0", `{"finances":{"allow_mixed_payment":"0"}}`, mixed, false},
		{"PHP/allow_mixed_payment/строка_false", `{"finances":{"allow_mixed_payment":"false"}}`, mixed, true},
		{"PHP/allow_mixed_payment/0.0", `{"finances":{"allow_mixed_payment":0.0}}`, mixed, false},
		{"PHP/allow_mixed_payment/пустой_объект", `{"finances":{"allow_mixed_payment":{}}}`, mixed, false},
		{"PHP/allow_mixed_payment/1", `{"finances":{"allow_mixed_payment":1}}`, mixed, true},
		{"PHP/discount_scope/не_строка", `{"finances":{"discount_scope":["all"]}}`, scope, ScopeTariffs},
		{"PHP/discount_scope/регистр", `{"finances":{"discount_scope":"ALL"}}`, scope, ScopeTariffs},
		{"PHP/finances_не_объект", `{"finances":"x"}`, bmp, int64(100)},
		{"PHP/settings_массив", `[1,2]`, bmp, int64(100)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.check(mustRules(t, c.finances)); got != c.want {
				t.Fatalf("got %v (%T), want %v (%T)", got, got, c.want, c.want)
			}
		})
	}
}

func bmp(r PaymentRules) any   { return r.BonusMaxPercent }
func pause(r PaymentRules) any { return r.AutoBillingPauseMinutes }
func scope(r PaymentRules) any { return r.DiscountScope }
func mixed(r PaymentRules) any { return r.MixedPayment }
func offered(mainOnly bool) func(PaymentRules) any {
	return func(r PaymentRules) any { return r.BonusPercentOffered(mainOnly) }
}

// BR-010/пример (GS-051): где действует скидка.
func TestDiscountScope(t *testing.T) {
	cases := []struct {
		scope           string
		tariffs, hourly bool
	}{
		{"tariffs", true, false}, {"hourly", false, true}, {"all", true, true}, {"", true, false},
	}
	for _, c := range cases {
		t.Run("BR-010/"+c.scope, func(t *testing.T) {
			r := RulesFromFinances(RawFinances{"discount_scope": c.scope})
			if r.DiscountOnTariffs() != c.tariffs || r.DiscountOnHourly() != c.hourly {
				t.Fatalf("%+v", r)
			}
		})
	}
}

// BR-019: что агент предлагает оплатить бонусами.
func TestBonusPercentOffered(t *testing.T) {
	cases := []struct {
		name     string
		percent  int64
		mixed    bool
		mainOnly bool
		want     int64
	}{
		{"BR-019/30_mixed", 30, true, false, 30},
		{"BR-019/30_no_mixed", 30, false, false, 0},
		{"BR-019/100_no_mixed", 100, false, false, 100},
		{"BR-019/0_mixed", 0, true, false, 0},
		{"BR-019/main_only_mixed", 100, true, true, 0},
		{"GS-049/30_mixed", 30, true, false, 30},
		{"GS-050/30_no_mixed", 30, false, false, 0},
		{"GS-050/100_no_mixed", 100, false, false, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := PaymentRules{DiscountScope: ScopeTariffs, BonusMaxPercent: c.percent, MixedPayment: c.mixed}
			if got := r.BonusPercentOffered(c.mainOnly); got != c.want {
				t.Fatalf("got %d, want %d", got, c.want)
			}
		})
	}
}

// GAP-008: bonusLimitCents() = intdiv(price × percent, 100) — вниз.
func TestBonusLimit(t *testing.T) {
	cases := []struct {
		name    string
		price   int64
		percent int64
		want    int64
	}{
		{"GAP-008", 99999, 30, 29999},
		{"GS-049", 100000, 30, 30000},
		{"BR-018/пример-4", 60000, 50, 30000},
		{"BR-018/1_тиын_50%", 1, 50, 0},
		{"BR-018/0%", 100000, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := PaymentRules{BonusMaxPercent: c.percent}
			if got := r.BonusLimit(moneyT(c.price)); int64(got) != c.want {
				t.Fatalf("got %d, want %d", got, c.want)
			}
		})
	}
}
