package pricing

import (
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

var t0 = time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC) // пятница

// GS-059: скидка 10 %, лимит использования 90 мин, лимит количества 1, смешанная оплата, 80.00 / 20.00.
func TestAgentTariffPurchase_DiscountUsageLimitQuantityLimit(t *testing.T) {
	zone := int64(7)
	night := Tariff{Price: 10000, DurationMinutes: 120, PaymentMethod: MainOrBonus, DiscountEnabled: true, IsActive: true,
		UsageLimitEnabled: true, UsageLimitMinutes: i64(90), QuantityLimitEnabled: true, QuantityLimit: i64(1)}
	in := TariffPurchaseInput{
		Tariff: night, Rules: RulesFromFinances(RawFinances{"allow_mixed_payment": true}), DiscountPercent: 10,
		Role: "client", PurchaseCount: 0, UserID: 1, Computer: Computer{ZoneID: &zone},
		Balances: Balances{8000, 2000}, Now: t0,
	}
	t.Run("GS-059/покупка", func(t *testing.T) {
		p, err := QuoteTariffPurchase(in)
		assertErr(t, err, nil)
		if p.Price != 9000 || p.Payment.After != (Balances{0, 1000}) || p.Minutes != 90 ||
			!p.SessionEndsAt.Equal(t0.Add(90*time.Minute)) || p.ExtendsSession {
			t.Fatalf("%+v", p)
		}
		if p.PartExpiresAt == nil || !p.PartExpiresAt.Equal(t0.Add(90*time.Minute)) {
			t.Fatalf("expires_at %v", p.PartExpiresAt)
		}
		if p.Payment.Main != 8000 || p.Payment.Bonus != 1000 {
			t.Fatalf("%+v", p.Payment)
		}
	})
	t.Run("GS-059/лимит_количества_исчерпан", func(t *testing.T) {
		in2 := in
		in2.PurchaseCount = 1
		_, err := QuoteTariffPurchase(in2)
		assertErr(t, err, ErrTariffUnavailable)
	})
}

// BR-020/пример, GS-034 (шаги 2, 3, 5), GS-048: последовательные покупки, балансы и минуты.
func TestPurchase_Sequences(t *testing.T) {
	r := DefaultPaymentRules()
	hour := Tariff{Price: 100000, DurationMinutes: 60, PaymentMethod: MainOrBonus, DiscountEnabled: true, IsActive: true}
	pc := Computer{ZoneID: i64(1)}

	t.Run("BR-020/пример", func(t *testing.T) {
		p, err := QuoteTariffPurchase(TariffPurchaseInput{Tariff: hour, Rules: r, DiscountPercent: 10, Role: "client", UserID: 1,
			Computer: pc, Choice: ChoiceMain, Balances: Balances{150000, 0}, Now: t0})
		assertErr(t, err, nil)
		if p.Price != 90000 || p.Payment.After.Main != 60000 || p.Payment.Main != 90000 || !p.SessionEndsAt.Equal(t0.Add(time.Hour)) {
			t.Fatalf("%+v", p)
		}
	})

	t.Run("GS-034", func(t *testing.T) {
		b := Balances{500000, 0}
		// шаг 2: «Час» со скидкой 5 % → 4050.00
		p, err := QuoteTariffPurchase(TariffPurchaseInput{Tariff: hour, Rules: r, DiscountPercent: 5, Role: "client", UserID: 1, Computer: pc, Balances: b, Now: t0})
		assertErr(t, err, nil)
		if p.Payment.After.Main != 405000 {
			t.Fatalf("шаг 2: %+v", p)
		}
		// шаг 3: 60 мин по 600 ₸/ч; область tariffs — на поминутку скидка не действует → 3450.00
		m, err := QuoteMinutesPurchase(MinutesPurchaseInput{ZonePricePerHour: tiyn(60000), Rules: r, DiscountPercent: 5, UserID: 1,
			Computer: pc, Minutes: 60, Balances: p.Payment.After, Now: t0})
		assertErr(t, err, nil)
		if m.Price != 60000 || m.Payment.After.Main != 345000 {
			t.Fatalf("шаг 3: %+v", m)
		}
		// шаг 5: «Час» со скидкой 10 % → 2550.00
		p, err = QuoteTariffPurchase(TariffPurchaseInput{Tariff: hour, Rules: r, DiscountPercent: 10, Role: "client", UserID: 1, Computer: pc, Balances: m.Payment.After, Now: t0})
		assertErr(t, err, nil)
		if p.Payment.After.Main != 255000 {
			t.Fatalf("шаг 5: %+v", p)
		}
	})

	t.Run("GS-048", func(t *testing.T) {
		tariff := Tariff{Price: 50000, DurationMinutes: 60, PaymentMethod: MainOrBonus, IsActive: true}
		p, err := QuoteTariffPurchase(TariffPurchaseInput{Tariff: tariff, Rules: r, Role: "client", UserID: 1, Computer: pc, Balances: Balances{200000, 0}, Now: t0})
		assertErr(t, err, nil)
		if p.PartExpiresAt != nil {
			t.Fatal("тариф без лимита использования: expires_at NULL")
		}
		active := pc
		active.PaidSession, active.SessionUserID, active.SessionEndsAt = true, i64(1), p.SessionEndsAt
		m, err := QuoteMinutesPurchase(MinutesPurchaseInput{ZonePricePerHour: tiyn(60000), Rules: r, UserID: 1, Computer: active, Minutes: 30, Balances: p.Payment.After, Now: t0})
		assertErr(t, err, nil)
		if m.Price != 30000 || m.Payment.After.Main != 120000 || !m.SessionEndsAt.Equal(t0.Add(90*time.Minute)) || !m.ExtendsSession {
			t.Fatalf("%+v", m)
		}
	})
}

// GAP-016: покупка при идущей сессии прибавляет минуты к её концу (тот же session_id).
func TestPurchase_ExtendsActiveSession(t *testing.T) {
	r := DefaultPaymentRules()
	pc := Computer{ZoneID: i64(1)}
	first, err := QuoteMinutesPurchase(MinutesPurchaseInput{ZonePricePerHour: tiyn(60000), Rules: r, UserID: 5, Computer: pc, Minutes: 60, Balances: Balances{200000, 0}, Now: t0})
	assertErr(t, err, nil)
	pc.PaidSession, pc.SessionUserID, pc.SessionEndsAt = true, i64(5), first.SessionEndsAt
	second, err := QuoteMinutesPurchase(MinutesPurchaseInput{ZonePricePerHour: tiyn(60000), Rules: r, UserID: 5, Computer: pc, Minutes: 30, Balances: first.Payment.After, Now: t0})
	assertErr(t, err, nil)
	t.Run("GAP-016", func(t *testing.T) {
		if first.SessionEndsAt.Format("15:04") != "11:00" || first.ExtendsSession {
			t.Fatalf("первая: %+v", first)
		}
		if second.SessionEndsAt.Format("15:04") != "11:30" || !second.ExtendsSession {
			t.Fatalf("вторая: %+v", second)
		}
	})
	t.Run("BR-020/истёкшая_сессия_не_продлевается", func(t *testing.T) {
		// hasPaidSession() = false (конец в прошлом) — вызывающий передаёт PaidSession=false: новая сессия от now.
		expired := Computer{ZoneID: i64(1), SessionUserID: i64(5), SessionEndsAt: t0.Add(-time.Minute)}
		p, err := QuoteMinutesPurchase(MinutesPurchaseInput{ZonePricePerHour: tiyn(60000), Rules: r, UserID: 5, Computer: expired, Minutes: 30, Balances: Balances{100000, 0}, Now: t0})
		assertErr(t, err, nil)
		if !p.SessionEndsAt.Equal(t0.Add(30*time.Minute)) || p.ExtendsSession {
			t.Fatalf("%+v", p)
		}
	})
}

// GAP-015: тариф только для роли client.
func TestTariff_UserRolesRestriction(t *testing.T) {
	tariff := Tariff{Price: 50000, DurationMinutes: 60, PaymentMethod: MainOrBonus, IsActive: true, UserRoles: []string{"client"}}
	base := TariffPurchaseInput{Tariff: tariff, Rules: DefaultPaymentRules(), UserID: 1, Computer: Computer{ZoneID: i64(1)}, Balances: Balances{200000, 0}, Now: t0}
	t.Run("GAP-015/client", func(t *testing.T) {
		in := base
		in.Role = "client"
		if tariff.Availability("client", 0, t0, nil) != Available {
			t.Fatal("клиенту тариф в списке")
		}
		_, err := QuoteTariffPurchase(in)
		assertErr(t, err, nil)
	})
	t.Run("GAP-015/cashier", func(t *testing.T) {
		in := base
		in.Role = "cashier"
		if tariff.Availability("cashier", 0, t0, nil) != RoleNotAllowed {
			t.Fatal("кассиру тарифа нет в списке")
		}
		_, err := QuoteTariffPurchase(in)
		assertErr(t, err, ErrTariffUnavailable)
	})
	t.Run("BR-016/user_roles_пустой", func(t *testing.T) {
		open := tariff
		open.UserRoles = []string{}
		if open.Availability("cashier", 0, t0, nil) != Available {
			t.Fatal("пустой user_roles — без ограничения")
		}
	})
}

// GS-040, GS-060, BR-020: отказы покупки — зона без цены часа, чужая сессия, резерв, зона тарифа.
func TestPurchase_Rejections(t *testing.T) {
	r := DefaultPaymentRules()
	other := Computer{ZoneID: i64(2), PaidSession: true, SessionUserID: i64(99), SessionEndsAt: t0.Add(time.Hour)}
	guest := Computer{ZoneID: i64(2), PaidSession: true, SessionGuest: true, SessionEndsAt: t0.Add(time.Hour)}
	b := Balances{1000000, 0}
	minutes := func(zone *money.Tiyn, pc Computer, m int64) error {
		_, err := QuoteMinutesPurchase(MinutesPurchaseInput{ZonePricePerHour: zone, Rules: r, UserID: 1, Computer: pc, Minutes: m, Balances: b, Now: t0})
		return err
	}
	t.Run("GS-040/зона_без_цены", func(t *testing.T) { assertErr(t, minutes(nil, Computer{ZoneID: i64(1)}, 30), ErrNoHourlyPrice) })
	t.Run("GS-040/0_мин", func(t *testing.T) { assertErr(t, minutes(tiyn(60000), Computer{}, 0), ErrInvalidInput) })
	t.Run("GS-040/721_мин", func(t *testing.T) { assertErr(t, minutes(tiyn(60000), Computer{}, 721), ErrInvalidInput) })
	t.Run("GS-040/чужая_сессия", func(t *testing.T) { assertErr(t, minutes(tiyn(60000), other, 30), ErrComputerBusy) })
	t.Run("GS-040/720_мин_можно", func(t *testing.T) { assertErr(t, minutes(tiyn(60000), Computer{}, 720), nil) })
	t.Run("BR-020/гостевая_сессия_чужая", func(t *testing.T) { assertErr(t, minutes(tiyn(60000), guest, 30), ErrComputerBusy) })
	t.Run("BR-020/резерв", func(t *testing.T) {
		assertErr(t, minutes(tiyn(60000), Computer{Reserved: true}, 30), ErrComputerReserved)
	})

	hall, vip := int64(1), int64(2)
	hallTariff := Tariff{Price: 50000, DurationMinutes: 60, PaymentMethod: MainOrBonus, IsActive: true, ZoneIDs: []int64{hall}}
	everywhere := Tariff{Price: 50000, DurationMinutes: 60, PaymentMethod: MainOrBonus, IsActive: true}
	t.Run("GS-060/фильтр_зоны", func(t *testing.T) {
		if !hallTariff.InZone(&hall) || hallTariff.InZone(&vip) || !everywhere.InZone(&vip) || hallTariff.InZone(nil) || !everywhere.InZone(nil) {
			t.Fatal("InZone")
		}
	})
	t.Run("BR-020/тариф_не_для_этого_ПК", func(t *testing.T) {
		_, err := QuoteTariffPurchase(TariffPurchaseInput{Tariff: hallTariff, Rules: r, Role: "client", UserID: 1, Computer: Computer{ZoneID: &vip}, Balances: b, Now: t0})
		assertErr(t, err, ErrTariffNotForComputer)
	})
	t.Run("BR-020/порядок_проверок/ПК_раньше_денег", func(t *testing.T) {
		_, err := QuoteTariffPurchase(TariffPurchaseInput{Tariff: everywhere, Rules: r, Role: "client", UserID: 1, Computer: other, Balances: Balances{}, Now: t0})
		assertErr(t, err, ErrComputerBusy)
	})
}

// GAP-014, BR-024/пример, GS-047: запуск пакета — остаток, но не дальше срока.
func TestPackageStart_CappedByExpiry(t *testing.T) {
	tariff := Tariff{Price: 100000, DurationMinutes: 120, UsageLimitEnabled: true, UsageLimitMinutes: i64(43200)}
	cases := []struct {
		name    string
		left    int64
		expires time.Time
		want    int64
		wantErr error
	}{
		{"GAP-014/40_мин_30_с", 90, t0.Add(40*time.Minute + 30*time.Second), 40, nil},
		{"GAP-014/истёк", 60, t0.Add(-time.Minute), 0, ErrPackageExpired},
		{"BR-024/пример", 90, t0.Add(50*time.Minute + 30*time.Second), 50, nil},
		{"GS-047/старт_90", 90, t0.Add(30*24*time.Hour - 30*time.Minute), 90, nil},
		{"GAP-014/59_секунд", 90, t0.Add(59 * time.Second), 0, ErrPackageExpired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := QuoteStartPackage(PackageStartInput{MinutesLeft: c.left, ExpiresAt: c.expires, Tariff: &tariff, UserID: 1, Now: t0})
			assertErr(t, err, c.wantErr)
			if c.wantErr != nil {
				return
			}
			if p.Minutes != c.want || p.Price != 0 || p.Payment.Main != 0 || p.Payment.Bonus != 0 ||
				!p.SessionEndsAt.Equal(t0.Add(time.Duration(c.want)*time.Minute)) || p.PartExpiresAt == nil || !p.PartExpiresAt.Equal(c.expires) {
				t.Fatalf("%+v", p)
			}
		})
	}
	t.Run("BR-024/зона_тарифа", func(t *testing.T) {
		zoned := tariff
		zoned.ZoneIDs = []int64{3}
		_, err := QuoteStartPackage(PackageStartInput{MinutesLeft: 30, ExpiresAt: t0.Add(time.Hour), Tariff: &zoned, UserID: 1, Computer: Computer{ZoneID: i64(4)}, Now: t0})
		assertErr(t, err, ErrPackageNotForPC)
	})
	t.Run("BR-024/вне_периода", func(t *testing.T) {
		limited := tariff
		limited.ValidityPeriods = WeeklyPeriods{period("mon", "00:00", "mon", "23:59")}
		_, err := QuoteStartPackage(PackageStartInput{MinutesLeft: 30, ExpiresAt: t0.Add(time.Hour), Tariff: &limited, UserID: 1, Now: t0})
		assertErr(t, err, ErrPackageOutOfPeriod)
	})
	t.Run("BR-024/тариф_удалён_физически", func(t *testing.T) {
		_, err := QuoteStartPackage(PackageStartInput{MinutesLeft: 30, ExpiresAt: t0.Add(time.Hour), UserID: 1, Computer: Computer{ZoneID: i64(4)}, Now: t0})
		assertErr(t, err, nil)
	})
}

// GAP-011: скидка 100 % — тариф бесплатный, покупка проходит с нулевыми балансами.
func TestDiscount_100PercentFreeTariff(t *testing.T) {
	tariff := Tariff{Price: 100000, DurationMinutes: 60, PaymentMethod: MainOrBonus, DiscountEnabled: true, IsActive: true}
	p, err := QuoteTariffPurchase(TariffPurchaseInput{Tariff: tariff, Rules: DefaultPaymentRules(), DiscountPercent: 100, Role: "client", UserID: 1, Now: t0})
	t.Run("GAP-011", func(t *testing.T) {
		assertErr(t, err, nil)
		if p.Price != 0 || p.Payment.Main != 0 || p.Payment.After != (Balances{}) || p.Minutes != 60 {
			t.Fatalf("%+v", p)
		}
	})
}
