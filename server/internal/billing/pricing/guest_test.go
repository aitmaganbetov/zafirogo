package pricing

import (
	"errors"
	"testing"
	"time"
)

var t10 = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC) // пятница

// GS-091: тариф на двух ПК гостю, приход в смену; повторная продажа продлевает гостевую сессию.
func TestGuestSession_TariffOnPCsPaidIntoShift(t *testing.T) {
	hall := int64(1)
	tariff := Tariff{Price: 70000, DurationMinutes: 60, IsActive: true}
	pc1 := GuestComputer{Name: "1", Computer: Computer{ZoneID: &hall}}
	pc2 := GuestComputer{Name: "2", Computer: Computer{ZoneID: &hall}}
	q, err := QuoteGuestSession(GuestSessionInput{Tariff: &tariff, Computers: []GuestComputer{pc1, pc2}, Now: t10})
	t.Run("GS-091/два_ПК", func(t *testing.T) {
		assertErr(t, err, nil)
		if q.Total != 140000 || q.Minutes != 60 || len(q.Computers) != 2 {
			t.Fatalf("%+v", q)
		}
		for _, c := range q.Computers {
			if !c.SessionEndsAt.Equal(t10.Add(time.Hour)) || c.Extends || c.Price != 70000 {
				t.Fatalf("%+v", c)
			}
		}
	})
	t.Run("GS-091/продление_до_12:00", func(t *testing.T) {
		pc1.Computer.PaidSession, pc1.Computer.SessionGuest, pc1.Computer.SessionEndsAt = true, true, q.Computers[0].SessionEndsAt
		q2, err := QuoteGuestSession(GuestSessionInput{Tariff: &tariff, Computers: []GuestComputer{pc1}, Now: t10})
		assertErr(t, err, nil)
		if !q2.Computers[0].SessionEndsAt.Equal(t10.Add(2*time.Hour)) || !q2.Computers[0].Extends || q2.Total != 70000 {
			t.Fatalf("%+v", q2)
		}
	})
	t.Run("BR-038/тариф_без_скидки_и_лимитов", func(t *testing.T) {
		// Скидка, лимит количества и роли для гостя не проверяются; минуты — effectiveMinutes().
		limited := Tariff{Price: 70000, DurationMinutes: 300, DiscountEnabled: true, UsageLimitEnabled: true, UsageLimitMinutes: i64(120),
			QuantityLimitEnabled: true, QuantityLimit: i64(0), UserRoles: []string{"vip"}}
		q, err := QuoteGuestSession(GuestSessionInput{Tariff: &limited, Computers: []GuestComputer{pc2}, Now: t10})
		assertErr(t, err, nil)
		if q.Total != 70000 || q.Minutes != 120 {
			t.Fatalf("%+v", q)
		}
	})
}

// GS-092: минуты по цене зоны; занятые ПК, брони и зоны без цены — отказ.
func TestGuestSession_MinutesByZoneAndRejections(t *testing.T) {
	vip := int64(1)
	free := GuestComputer{Name: "1", Computer: Computer{ZoneID: &vip}, ZonePricePerHour: tiyn(60000)}
	plain := GuestComputer{Name: "2", Computer: Computer{ZoneID: i64(2)}}
	busy := GuestComputer{Name: "3", Computer: Computer{ZoneID: &vip, PaidSession: true, SessionUserID: i64(7), SessionEndsAt: t10.Add(time.Hour)}, ZonePricePerHour: tiyn(60000)}
	booked := GuestComputer{Name: "4", Computer: Computer{ZoneID: &vip}, ZonePricePerHour: tiyn(60000)}
	bookingStart := t10.Add(20 * time.Minute)
	errBooked := errors.New("ПК 4 забронирован с 10:20")
	check := func(_ int, pc GuestComputer, endsAt time.Time) error {
		if pc.Name == "4" && endsAt.After(bookingStart) {
			return errBooked
		}
		return nil
	}
	sell := func(pc GuestComputer) (GuestSessionQuote, error) {
		return QuoteGuestSession(GuestSessionInput{Minutes: 30, Computers: []GuestComputer{pc}, Now: t10, CheckBooking: check})
	}
	t.Run("GS-092/30_мин_VIP", func(t *testing.T) {
		q, err := sell(free)
		assertErr(t, err, nil)
		if q.Total != 30000 || !q.Computers[0].SessionEndsAt.Equal(t10.Add(30*time.Minute)) {
			t.Fatalf("%+v", q)
		}
	})
	t.Run("GS-092/зона_без_цены", func(t *testing.T) {
		_, err := sell(plain)
		assertErr(t, err, ErrGuestNoHourlyPrice)
		var e *Error
		if !errors.As(err, &e) || e.Field != "minutes" || e.Error() != "minutes: В зоне ПК 2 время поминутно не продаётся." {
			t.Fatalf("%v", err)
		}
	})
	t.Run("GS-092/занят_клиентом", func(t *testing.T) {
		_, err := sell(busy)
		assertErr(t, err, ErrGuestComputerBusy)
	})
	t.Run("GS-092/бронь", func(t *testing.T) {
		_, err := sell(booked)
		if !errors.Is(err, errBooked) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("BR-038/резерв", func(t *testing.T) {
		r := free
		r.Computer.Reserved = true
		_, err := sell(r)
		assertErr(t, err, ErrGuestComputerBusy)
	})
	t.Run("BR-038/минуты_вне_1…720", func(t *testing.T) {
		_, err := QuoteGuestSession(GuestSessionInput{Minutes: 721, Computers: []GuestComputer{free}, Now: t10})
		assertErr(t, err, ErrInvalidInput)
	})
}

// GS-094: тариф зоны Hall на ПК из VIP — отказ tariff_id.
func TestGuestSession_TariffZones(t *testing.T) {
	hall, vip := int64(1), int64(2)
	hallTariff := Tariff{Price: 70000, DurationMinutes: 60, IsActive: true, ZoneIDs: []int64{hall}}
	everywhere := Tariff{Price: 50000, DurationMinutes: 60, IsActive: true}
	pc := GuestComputer{Name: "1", Computer: Computer{ZoneID: &vip}}
	t.Run("GS-094/Зальный_на_VIP", func(t *testing.T) {
		_, err := QuoteGuestSession(GuestSessionInput{Tariff: &hallTariff, Computers: []GuestComputer{pc}, Now: t10})
		assertErr(t, err, ErrGuestTariffZone)
		var e *Error
		if !errors.As(err, &e) || e.Field != "tariff_id" {
			t.Fatalf("%v", err)
		}
	})
	t.Run("GS-094/Везде_на_VIP", func(t *testing.T) {
		q, err := QuoteGuestSession(GuestSessionInput{Tariff: &everywhere, Computers: []GuestComputer{pc}, Now: t10})
		assertErr(t, err, nil)
		if q.Total != 50000 {
			t.Fatalf("%+v", q)
		}
	})
}

// GS-066: касса не запускает тариф вне периода действия; GS-063 — вне времени продажи (в TestTariffSalePeriods).
func TestGuestSession_TariffOutsideValidityRejected(t *testing.T) {
	day := Tariff{Price: 50000, DurationMinutes: 60, IsActive: true, ValidityPeriods: WeeklyPeriods{period("wed", "10:00", "wed", "17:00")}}
	wed23 := time.Date(2026, 10, 28, 23, 0, 0, 0, time.UTC)
	pc := GuestComputer{Name: "1"}
	t.Run("GS-066", func(t *testing.T) {
		_, err := QuoteGuestSession(GuestSessionInput{Tariff: &day, Computers: []GuestComputer{pc}, Now: wed23})
		assertErr(t, err, ErrGuestTariffUnavailable)
	})
	t.Run("GS-066/днём_можно", func(t *testing.T) {
		_, err := QuoteGuestSession(GuestSessionInput{Tariff: &day, Computers: []GuestComputer{pc}, Now: wed23.Add(-10 * time.Hour)})
		assertErr(t, err, nil)
	})
	t.Run("BR-038/тариф_не_найден", func(t *testing.T) {
		_, err := QuoteGuestSession(GuestSessionInput{TariffMissing: true, Computers: []GuestComputer{pc}, Now: wed23})
		assertErr(t, err, ErrGuestTariffUnavailable)
	})
}

// GAP-030: два ПК в разных зонах, 90 мин — сумма одной записью; BR-038/пример — 2 ПК по 500 ₸/ч.
func TestGuestSession_MinutesTwoZonesSum(t *testing.T) {
	cases := []struct {
		name  string
		pcs   []GuestComputer
		total int64
	}{
		{"GAP-030", []GuestComputer{{Name: "1", ZonePricePerHour: tiyn(60000)}, {Name: "2", ZonePricePerHour: tiyn(45000)}}, 157500},
		{"BR-038/пример", []GuestComputer{{Name: "1", ZonePricePerHour: tiyn(50000)}, {Name: "2", ZonePricePerHour: tiyn(50000)}}, 150000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q, err := QuoteGuestSession(GuestSessionInput{Minutes: 90, Computers: c.pcs, Now: t10})
			assertErr(t, err, nil)
			if int64(q.Total) != c.total {
				t.Fatalf("%+v", q)
			}
		})
	}
}

// GAP-031: на ПК сессия клиента с аккаунтом — гостевую не продать; ошибка с именем ПК.
func TestGuestSession_RefusedOnAccountSession(t *testing.T) {
	pc := GuestComputer{Name: "№7", Computer: Computer{PaidSession: true, SessionUserID: i64(3), SessionEndsAt: t10.Add(30 * time.Minute)}, ZonePricePerHour: tiyn(60000)}
	_, err := QuoteGuestSession(GuestSessionInput{Minutes: 30, Computers: []GuestComputer{pc}, Now: t10})
	t.Run("GAP-031", func(t *testing.T) {
		var e *Error
		if !errors.As(err, &e) || e.Field != "computer_ids" || e.Error() != "computer_ids: ПК №7 занят или забронирован." {
			t.Fatalf("%v", err)
		}
	})
	t.Run("BR-038/порядок/цена_раньше_занятости", func(t *testing.T) {
		noPrice := pc
		noPrice.ZonePricePerHour = nil
		_, err := QuoteGuestSession(GuestSessionInput{Minutes: 30, Computers: []GuestComputer{noPrice}, Now: t10})
		assertErr(t, err, ErrGuestNoHourlyPrice)
	})
}
