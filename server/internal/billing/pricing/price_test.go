package pricing

import (
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// BR-012: discounted() — базисные пункты PHP round(), скидка половиной вверх, пределы 0…100 %.
func TestDiscounted(t *testing.T) {
	cases := []struct {
		name    string
		price   money.Tiyn
		percent float64
		want    money.Tiyn
	}{
		{"BR-012/пример-1", 100000, 10, 90000},
		{"BR-012/пример-2", 999, 12.5, 874},
		{"BR-012/пример-3", 1000, 33.333, 667},
		{"BR-012/больше_100", 100000, 150, 0},
		{"BR-012/отрицательный", 100000, -5, 100000},
		{"GS-033", 100000, 12.5, 87500},
		{"GAP-011", 100000, 100, 0},
		{"GAP-010/час", 55500, 7.5, 51337},
		// (int) round(x*100) PHP 8.4 (money.PHPRound): 0.145*100 = 14.499999999999998 → 14 bp (PHP 8.3 дал бы 15).
		{"BR-012/php_round/0.145%", 1000000, 0.145, 1000000 - 1400},
		{"BR-012/php_round/1.005%", 1000000, 1.005, 1000000 - 10000}, // 100.49999999999999 → 100 bp
		{"BR-012/ноль", 0, 10, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Discounted(c.price, c.percent)
			assertErr(t, err, nil)
			if got != c.want {
				t.Fatalf("Discounted(%d, %v) = %d, want %d", c.price, c.percent, got, c.want)
			}
		})
	}
	if _, err := Discounted(-1, 0); err == nil {
		t.Fatal("отрицательная цена должна быть ErrInvalidInput")
	}
	if _, err := Discounted(MaxPrice+1, 0); err == nil {
		t.Fatal("цена > MaxPrice должна быть ErrInvalidInput")
	}
}

// GS-041…GS-045, BR-014/пример, GAP-010: minutesPriceInCents() = intdiv(час × минуты + 30, 60).
func TestMinutesPriceInCents(t *testing.T) {
	cases := []struct {
		name    string
		hour    money.Tiyn
		minutes int64
		want    money.Tiyn
	}{
		{"GS-041", 60000, 1, 1000},
		{"GS-042", 60000, 90, 90000},
		{"GS-043", 50000, 17, 14167},
		{"GS-044", 100, 1, 2},
		{"GS-045", 50, 1, 1},
		{"BR-014/пример/540x7", 54000, 7, 6300},
		{"BR-014/пример/500x1", 50000, 1, 833},
		{"GAP-010", 51337, 7, 5989},
		{"GS-051/540x30", 54000, 30, 27000},
		{"GS-053/600x200", 60000, 200, 200000},
		{"BR-038/пример", 50000, 90, 75000},
		{"GAP-030/VIP", 60000, 90, 90000},
		{"GAP-030/Hall", 45000, 90, 67500},
		{"GS-092", 60000, 30, 30000},
		{"BR-014/0_минут", 60000, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := MinutesPrice(c.hour, c.minutes)
			assertErr(t, err, nil)
			if got != c.want {
				t.Fatalf("MinutesPrice(%d, %d) = %d, want %d", c.hour, c.minutes, got, c.want)
			}
		})
	}
	if _, err := MinutesPrice(60000, 721); err == nil {
		t.Fatal("721 минута должна быть ErrInvalidInput")
	}
}

// GS-051 (BR-010/пример), GAP-010: цена тарифа и часа в зависимости от области скидки.
func TestDiscountScope_TariffsHourlyAll(t *testing.T) {
	tariff := Tariff{Price: 100000, DurationMinutes: 120, PaymentMethod: MainOrBonus, DiscountEnabled: true, IsActive: true}
	cases := []struct {
		name              string
		finances          RawFinances
		discount          float64
		wantTariff, wantH money.Tiyn
	}{
		{"GS-051/по_умолчанию", nil, 10, 90000, 60000},
		{"GS-051/hourly", RawFinances{"discount_scope": "hourly"}, 10, 100000, 54000},
		{"GS-051/all", RawFinances{"discount_scope": "all"}, 10, 90000, 54000},
		{"GAP-010/all_7.5%", RawFinances{"discount_scope": "all"}, 7.5, 92500, 51337},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := RulesFromFinances(c.finances)
			hourPrice := money.Tiyn(60000)
			if c.name == "GAP-010/all_7.5%" {
				hourPrice = 55500
			}
			gotT, err := TariffPrice(tariff, r, c.discount)
			assertErr(t, err, nil)
			gotH, err := HourlyPrice(hourPrice, r, c.discount)
			assertErr(t, err, nil)
			if gotT != c.wantTariff || gotH != c.wantH {
				t.Fatalf("тариф %d, час %d; want %d, %d", gotT, gotH, c.wantTariff, c.wantH)
			}
		})
	}
	t.Run("GS-051/тариф_без_скидки", func(t *testing.T) {
		noDiscount := tariff
		noDiscount.DiscountEnabled = false
		got, err := TariffPrice(noDiscount, RulesFromFinances(RawFinances{"discount_scope": "all"}), 10)
		assertErr(t, err, nil)
		if got != 100000 {
			t.Fatalf("got %d", got)
		}
	})
}

// GS-034: цена в списке агента — max(личная, накопительная) скидка передаётся как discountPercent.
func TestTariffOffer_LoyaltyDiscount(t *testing.T) {
	hour := Tariff{Price: 100000, DurationMinutes: 60, PaymentMethod: MainOrBonus, DiscountEnabled: true, IsActive: true}
	noDiscount := Tariff{Price: 100000, DurationMinutes: 30, PaymentMethod: MainOrBonus, IsActive: true}
	r := DefaultPaymentRules()
	cases := []struct {
		name     string
		t        Tariff
		discount float64
		want     money.Tiyn
	}{
		{"GS-034/шаг-1/Час_5%", hour, 5, 95000},
		{"GS-034/шаг-1/Без_скидки", noDiscount, 5, 100000},
		{"GS-034/шаг-4/Час_10%", hour, 10, 90000},
		{"GS-034/шаг-4/Без_скидки", noDiscount, 10, 100000},
		{"GS-033/12.5%", Tariff{Price: 100000, DurationMinutes: 60, DiscountEnabled: true}, 12.5, 87500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := c.t.Offer(r, c.discount)
			assertErr(t, err, nil)
			if o.Price != c.want || o.OriginalPrice != c.t.Price {
				t.Fatalf("offer %+v, want price %d", o, c.want)
			}
		})
	}
}

// GS-049, GS-050, GS-059: поля тарифа в списке агента.
func TestTariffOffer_BonusFields(t *testing.T) {
	pack := Tariff{Price: 100000, DurationMinutes: 120, PaymentMethod: MainOrBonus, DiscountEnabled: true, IsActive: true}
	moneyOnly := Tariff{Price: 100000, DurationMinutes: 120, PaymentMethod: MainBalanceOnly, IsActive: true}
	cases := []struct {
		name     string
		t        Tariff
		rules    PaymentRules
		allowed  bool
		percent  int64
		mixedOut bool
	}{
		{"GS-049/Пакет", pack, RulesFromFinances(RawFinances{"bonus_max_percent": 30, "allow_mixed_payment": true}), true, 30, true},
		{"GS-049/Только_деньги", moneyOnly, RulesFromFinances(RawFinances{"bonus_max_percent": 30, "allow_mixed_payment": true}), false, 0, true},
		{"GS-050/30_без_смешанной", pack, RulesFromFinances(RawFinances{"bonus_max_percent": 30}), false, 0, false},
		{"GS-050/100_без_смешанной", pack, RulesFromFinances(RawFinances{"bonus_max_percent": 100}), true, 100, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := c.t.Offer(c.rules, 0)
			assertErr(t, err, nil)
			if o.BonusAllowed != c.allowed || o.BonusMaxPercent != c.percent || o.MixedPayment != c.mixedOut {
				t.Fatalf("%+v", o)
			}
		})
	}
	t.Run("GS-059/90.00_и_90_мин", func(t *testing.T) {
		night := Tariff{Price: 10000, DurationMinutes: 120, PaymentMethod: MainOrBonus, DiscountEnabled: true,
			UsageLimitEnabled: true, UsageLimitMinutes: i64(90), QuantityLimitEnabled: true, QuantityLimit: i64(1)}
		o, err := night.Offer(RulesFromFinances(RawFinances{"allow_mixed_payment": true}), 10)
		assertErr(t, err, nil)
		if o.Price != 9000 || o.Minutes != 90 || o.UsageLimitMinutes == nil || *o.UsageLimitMinutes != 90 {
			t.Fatalf("%+v", o)
		}
	})
}

// BR-015: эффективные минуты тарифа.
func TestEffectiveMinutes(t *testing.T) {
	cases := []struct {
		name string
		t    Tariff
		want int64
	}{
		{"BR-015/пример-1", Tariff{DurationMinutes: 300, UsageLimitEnabled: true, UsageLimitMinutes: i64(120)}, 120},
		{"BR-015/пример-2", Tariff{DurationMinutes: 120, UsageLimitEnabled: true, UsageLimitMinutes: i64(43200)}, 120},
		{"BR-015/лимит_выключен", Tariff{DurationMinutes: 300, UsageLimitMinutes: i64(120)}, 300},
		{"BR-015/лимит_NULL", Tariff{DurationMinutes: 300, UsageLimitEnabled: true}, 300},
		{"GS-059", Tariff{DurationMinutes: 120, UsageLimitEnabled: true, UsageLimitMinutes: i64(90)}, 90},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.t.EffectiveMinutes(); got != c.want {
				t.Fatalf("got %d, want %d", got, c.want)
			}
		})
	}
}
