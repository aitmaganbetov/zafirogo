package pricing

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// Время тарифа заканчивается вместе с периодом действия (Zafiro b4f08f6 «Tariffs: time ends with the validity
// period»): сценарии GS-529…GS-533 (docs/golden-scenarios.md).

// almaty — часовой пояс клуба в сценариях (UTC+5, без перехода на летнее время).
var almaty = time.FixedZone("Asia/Almaty", 5*3600)

func monFri(day string, from, to string) WeeklyPeriod { return period(day, from, day, to) }

// GS-532, GS-533: tests/Unit/WeeklyPeriodsTest.php. 2026-10-26 — понедельник.
func TestWeeklyPeriods_EndOf(t *testing.T) {
	wrap := WeeklyPeriods{period("sat", "22:00", "mon", "06:00")}
	cases := []struct {
		name    string
		periods WeeklyPeriods
		at      string
		want    string // "" — конца нет
	}{
		{"GS-532/один_период", WeeklyPeriods{monFri("mon", "08:00", "18:00")}, "2026-10-26 15:00:00", "2026-10-26 18:00"},
		{"GS-532/подряд_как_один", WeeklyPeriods{monFri("mon", "08:00", "18:00"), monFri("mon", "18:00", "22:00")}, "2026-10-26 15:00:00", "2026-10-26 22:00"},
		{"GS-532/23:59_следующая_полночь", WeeklyPeriods{monFri("mon", "08:00", "23:59")}, "2026-10-26 15:00:00", "2026-10-27 00:00"},
		{"GS-532/через_конец_недели/вс", wrap, "2026-10-25 10:00:00", "2026-10-26 06:00"},
		{"GS-532/через_конец_недели/пн", wrap, "2026-10-26 05:00:00", "2026-10-26 06:00"},
		{"GS-533/нет_ограничения", nil, "2026-10-26 15:00:00", ""},
		{"GS-533/вся_неделя", WeeklyPeriods{period("mon", "00:00", "sun", "23:59")}, "2026-10-26 15:00:00", ""},
		{"GS-533/вне_периодов", WeeklyPeriods{monFri("tue", "08:00", "18:00")}, "2026-10-26 15:00:00", ""},
		// По коду: пересекающиеся периоды; цепочка через полночь; секунды момента не влияют.
		{"BR-017/конец/пересечение", WeeklyPeriods{monFri("mon", "08:00", "16:00"), monFri("mon", "12:00", "20:00")}, "2026-10-26 15:00:30", "2026-10-26 20:00"},
		{"BR-017/конец/цепочка_через_полночь", WeeklyPeriods{monFri("mon", "08:00", "23:59"), monFri("tue", "00:00", "03:00")}, "2026-10-26 15:00:00", "2026-10-27 03:00"},
		// Последняя минута периода: Contains включает её, диапазон EndOf — нет → конец = начало этой минуты.
		{"BR-017/конец/последняя_минута", WeeklyPeriods{monFri("mon", "08:00", "18:00")}, "2026-10-26 18:00:40", "2026-10-26 18:00"},
		// Период с концом раньше начала в тот же день — на всю неделю вперёд (end += WEEK).
		{"BR-017/конец/конец_раньше_начала", WeeklyPeriods{monFri("mon", "18:00", "08:00")}, "2026-10-26 19:00:00", "2026-11-02 08:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := c.periods.EndOf(at(t, c.at, almaty))
			if c.want == "" {
				if ok {
					t.Fatalf("конец %v, ожидалось — нет", got)
				}
				return
			}
			if !ok || got.Format("2006-01-02 15:04") != c.want || got.Location() != almaty {
				t.Fatalf("конец %v (%v), ожидалось %s", got, ok, c.want)
			}
		})
	}
}

// Сверка арифметики endOf() с PHP: фикстура tools/php-oracle/gen-weekly-fixture.php (буквальная копия
// WeeklyPeriods::contains()/endOf() старого проекта) — 2000 случайных наборов периодов и моментов.
func TestWeeklyPeriods_EndOfMatchesPHP(t *testing.T) {
	raw, err := os.ReadFile("testdata/php_weekly_endof.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases [][]json.RawMessage `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) < 1000 {
		t.Fatalf("в фикстуре %d кейсов", len(fx.Cases))
	}
	monday := time.Date(2026, 10, 26, 0, 0, 0, 0, almaty)
	for i, c := range fx.Cases {
		var rows [][4]string
		var day, hour, minute int
		var contains bool
		var end *int
		for j, dst := range []any{&rows, &day, &hour, &minute, &contains, &end} {
			if err := json.Unmarshal(c[j], dst); err != nil {
				t.Fatalf("кейс %d: %v", i, err)
			}
		}
		periods := make(WeeklyPeriods, len(rows))
		for j, r := range rows {
			periods[j] = period(r[0], r[1], r[2], r[3])
		}
		now := monday.AddDate(0, 0, day-1).Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute + 17*time.Second)
		if got := periods.Contains(now); got != contains {
			t.Fatalf("кейс %d %v @%d %02d:%02d: Contains = %v, PHP %v", i, rows, day, hour, minute, got, contains)
		}
		got, ok := periods.EndOf(now)
		switch {
		case end == nil && ok:
			t.Fatalf("кейс %d %v @%d %02d:%02d: конец %v, PHP — null", i, rows, day, hour, minute, got)
		case end != nil && (!ok || !got.Equal(monday.Add(time.Duration(*end)*time.Minute))):
			t.Fatalf("кейс %d %v @%d %02d:%02d: конец %v (%v), PHP — пн + %d мин", i, rows, day, hour, minute, got, ok, *end)
		}
	}
}

// GS-529: SessionTimeTest.php — «tariff time ends with its validity period: bought at 15:00 for Mon 08:00–18:00,
// it ends at 18:00». Тариф «День» 600 мин, 1000 ₸; баланс 1000.00.
func TestTariffPurchase_EndsWithValidityPeriod(t *testing.T) {
	day := Tariff{Price: 100000, DurationMinutes: 600, PaymentMethod: MainOrBonus, IsActive: true,
		ValidityPeriods: WeeklyPeriods{monFri("mon", "08:00", "18:00")}}
	now := at(t, "2026-10-26 15:00:00", almaty)
	six := at(t, "2026-10-26 18:00:00", almaty)
	in := TariffPurchaseInput{Tariff: day, Rules: DefaultPaymentRules(), Role: "client", UserID: 1,
		Computer: Computer{ZoneID: i64(1)}, Choice: ChoiceMain, Balances: Balances{100000, 0}, Now: now, Location: almaty}

	t.Run("GS-529/до_18:00_за_ту_же_цену", func(t *testing.T) {
		p, err := QuoteTariffPurchase(in)
		assertErr(t, err, nil)
		if p.Minutes != 180 || p.FullMinutes != 600 || !p.CutAtValidityEnd || !p.SessionEndsAt.Equal(six) ||
			p.Price != 100000 || p.Payment.After != (Balances{0, 0}) || p.PartExpiresAt != nil {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("GS-529/время_оплачено_дальше_18:00", func(t *testing.T) {
		in2 := in
		in2.Computer = Computer{ZoneID: i64(1), PaidSession: true, SessionUserID: i64(1), SessionEndsAt: six.Add(30 * time.Minute)}
		_, err := QuoteTariffPurchase(in2)
		assertErr(t, err, ErrTariffValidUntil)
		var pe *Error
		if !errors.As(err, &pe) || pe.Field != "tariff" || pe.Params["time"] != "18:00" {
			t.Fatalf("%+v", pe)
		}
	})
	// По коду sell(): отказ «оплачено дальше» — раньше проверки денег (баланса не хватает, а ошибка — про период).
	t.Run("GS-529/отказ_раньше_проверки_денег", func(t *testing.T) {
		in2 := in
		in2.Balances = Balances{}
		in2.Computer = Computer{ZoneID: i64(1), PaidSession: true, SessionUserID: i64(1), SessionEndsAt: six}
		_, err := QuoteTariffPurchase(in2)
		assertErr(t, err, ErrTariffValidUntil)
	})
	// Продление своей сессии: минуты — от конца оплаченного времени до конца периода, вверх до минуты.
	t.Run("GS-529/продление_до_конца_периода", func(t *testing.T) {
		in2 := in
		in2.Computer = Computer{ZoneID: i64(1), PaidSession: true, SessionUserID: i64(1), SessionEndsAt: at(t, "2026-10-26 16:30:20", almaty)}
		p, err := QuoteTariffPurchase(in2)
		assertErr(t, err, nil)
		if p.Minutes != 90 || !p.SessionEndsAt.Equal(six) || !p.ExtendsSession || !p.CutAtValidityEnd { // ceil(5380 с / 60) = 90
			t.Fatalf("%+v", p)
		}
	})
	// Доли секунды now отбрасываются (getTimestamp()): 15:00:00.9 → ceil(10800 / 60) = 180.
	t.Run("GS-529/доли_секунды", func(t *testing.T) {
		in2 := in
		in2.Now = now.Add(900 * time.Millisecond)
		p, err := QuoteTariffPurchase(in2)
		assertErr(t, err, nil)
		if p.Minutes != 180 || !p.SessionEndsAt.Equal(six) {
			t.Fatalf("%+v", p)
		}
		in2.Now = now.Add(time.Second)
		p, err = QuoteTariffPurchase(in2)
		assertErr(t, err, nil)
		if p.Minutes != 180 { // ceil(10799 / 60) = 180
			t.Fatalf("%+v", p)
		}
	})
	// Тариф короче остатка периода не урезается; без периода действия — как раньше.
	t.Run("GS-529/короткий_тариф_не_урезается", func(t *testing.T) {
		in2 := in
		in2.Tariff.DurationMinutes = 180
		p, err := QuoteTariffPurchase(in2)
		assertErr(t, err, nil)
		if p.Minutes != 180 || p.CutAtValidityEnd || !p.SessionEndsAt.Equal(six) {
			t.Fatalf("%+v", p)
		}
		in2.Tariff.DurationMinutes, in2.Tariff.ValidityPeriods = 600, nil
		p, err = QuoteTariffPurchase(in2)
		assertErr(t, err, nil)
		if p.Minutes != 600 || p.FullMinutes != 600 || p.CutAtValidityEnd || !p.SessionEndsAt.Equal(now.Add(10*time.Hour)) {
			t.Fatalf("%+v", p)
		}
	})
}

// GS-530: SessionTimeTest.php — «a usage limit keeps the time past the validity period; the package starts again
// only up to its end». Тариф 120 мин, лимит использования 30 дней, пн и вт 08:00–18:00.
func TestTariffPurchase_UsageLimitKeepsTimePastValidity(t *testing.T) {
	periods := WeeklyPeriods{monFri("mon", "08:00", "18:00"), monFri("tue", "08:00", "18:00")}
	pack := Tariff{Price: 100000, DurationMinutes: 120, PaymentMethod: MainOrBonus, IsActive: true,
		UsageLimitEnabled: true, UsageLimitMinutes: i64(30 * 24 * 60), ValidityPeriods: periods}
	mon17 := at(t, "2026-10-26 17:00:00", almaty)
	expires := mon17.Add(30 * 24 * time.Hour)

	t.Run("GS-530/покупка_в_17:00", func(t *testing.T) {
		p, err := QuoteTariffPurchase(TariffPurchaseInput{Tariff: pack, Rules: DefaultPaymentRules(), Role: "client", UserID: 1,
			Computer: Computer{ZoneID: i64(1)}, Choice: ChoiceMain, Balances: Balances{100000, 0}, Now: mon17, Location: almaty})
		assertErr(t, err, nil)
		// minutes_added 60; остальные 60 остаются пакетом до expires_at части.
		if p.Minutes != 60 || p.FullMinutes-p.Minutes != 60 || p.PartExpiresAt == nil || !p.PartExpiresAt.Equal(expires) ||
			!p.SessionEndsAt.Equal(at(t, "2026-10-26 18:00:00", almaty)) {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("GS-530/запуск_пакета_во_вт_17:30", func(t *testing.T) {
		tue := at(t, "2026-10-27 17:30:00", almaty)
		p, err := QuoteStartPackage(PackageStartInput{MinutesLeft: 60, ExpiresAt: expires, Tariff: &pack, UserID: 1,
			Computer: Computer{ZoneID: i64(1)}, Now: tue, Location: almaty})
		assertErr(t, err, nil)
		// minutes_added 30, в пакете остаётся 30; срок части — срок пакета.
		if p.Minutes != 30 || p.FullMinutes != 60 || !p.CutAtValidityEnd || !p.SessionEndsAt.Equal(at(t, "2026-10-27 18:00:00", almaty)) ||
			p.PartExpiresAt == nil || !p.PartExpiresAt.Equal(expires) {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("GS-530/пакет_целиком_до_конца_периода", func(t *testing.T) {
		p, err := QuoteStartPackage(PackageStartInput{MinutesLeft: 60, ExpiresAt: expires, Tariff: &pack, UserID: 1,
			Computer: Computer{ZoneID: i64(1)}, Now: at(t, "2026-10-27 10:00:00", almaty), Location: almaty})
		assertErr(t, err, nil)
		if p.Minutes != 60 || p.FullMinutes != 60 || p.CutAtValidityEnd {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("GS-530/пакет/время_оплачено_дальше", func(t *testing.T) {
		tue := at(t, "2026-10-27 17:30:00", almaty)
		_, err := QuoteStartPackage(PackageStartInput{MinutesLeft: 60, ExpiresAt: expires, Tariff: &pack, UserID: 1,
			Computer: Computer{ZoneID: i64(1), PaidSession: true, SessionUserID: i64(1), SessionEndsAt: tue.Add(time.Hour)},
			Now:      tue, Location: almaty})
		assertErr(t, err, ErrTariffValidUntil)
	})
	// Тариф пакета удалён физически — ограничений нет (как раньше).
	t.Run("GS-530/пакет_без_тарифа", func(t *testing.T) {
		p, err := QuoteStartPackage(PackageStartInput{MinutesLeft: 60, ExpiresAt: expires, UserID: 1,
			Computer: Computer{ZoneID: i64(1)}, Now: at(t, "2026-10-27 17:30:00", almaty), Location: almaty})
		assertErr(t, err, nil)
		if p.Minutes != 60 || p.CutAtValidityEnd {
			t.Fatalf("%+v", p)
		}
	})
}

// GS-531: GuestSessionTest.php — «a guest tariff ends with its validity period, at the same price».
func TestGuestSession_TariffEndsWithValidityPeriod(t *testing.T) {
	day := Tariff{Price: 200000, DurationMinutes: 600, PaymentMethod: MainOrBonus, IsActive: true,
		ValidityPeriods: WeeklyPeriods{monFri("mon", "08:00", "18:00")}}
	now := at(t, "2026-10-26 15:00:00", almaty)
	six := at(t, "2026-10-26 18:00:00", almaty)
	hall := int64(1)
	pc := GuestComputer{Name: "1", Computer: Computer{ZoneID: &hall}}

	t.Run("GS-531/до_18:00_за_ту_же_цену", func(t *testing.T) {
		q, err := QuoteGuestSession(GuestSessionInput{Tariff: &day, Computers: []GuestComputer{pc}, Now: now, Location: almaty})
		assertErr(t, err, nil)
		if q.Total != 200000 || q.Minutes != 180 || !q.Computers[0].SessionEndsAt.Equal(six) {
			t.Fatalf("%+v", q)
		}
	})
	// По коду: продление гостевой сессии — конец не дальше конца периода; оплачено до него или дальше — отказ.
	t.Run("GS-531/продление_до_конца_периода", func(t *testing.T) {
		ext := GuestComputer{Name: "2", Computer: Computer{ZoneID: &hall, PaidSession: true, SessionGuest: true, SessionEndsAt: now.Add(time.Hour)}}
		var checked time.Time
		q, err := QuoteGuestSession(GuestSessionInput{Tariff: &day, Computers: []GuestComputer{pc, ext}, Now: now, Location: almaty,
			CheckBooking: func(i int, _ GuestComputer, endsAt time.Time) error {
				if i == 1 {
					checked = endsAt
				}
				return nil
			}})
		assertErr(t, err, nil)
		if q.Total != 400000 || q.Minutes != 180 || !q.Computers[1].SessionEndsAt.Equal(six) || !q.Computers[1].Extends || !checked.Equal(six) {
			t.Fatalf("%+v (бронь до %v)", q, checked)
		}
	})
	t.Run("GS-531/время_оплачено_дальше", func(t *testing.T) {
		paid := GuestComputer{Name: "PC-07", Computer: Computer{ZoneID: &hall, PaidSession: true, SessionGuest: true, SessionEndsAt: six}}
		_, err := QuoteGuestSession(GuestSessionInput{Tariff: &day, Computers: []GuestComputer{paid}, Now: now, Location: almaty})
		assertErr(t, err, ErrGuestTariffValidUntil)
		var pe *Error
		if !errors.As(err, &pe) || pe.Field != "tariff_id" || pe.Params["time"] != "18:00" || pe.Params["computer"] != "PC-07" {
			t.Fatalf("%+v", pe)
		}
	})
	// Минуты (без тарифа) периодом не ограничены.
	t.Run("GS-531/минуты_не_урезаются", func(t *testing.T) {
		byHour := GuestComputer{Name: "1", Computer: Computer{ZoneID: &hall}, ZonePricePerHour: tiyn(60000)}
		q, err := QuoteGuestSession(GuestSessionInput{Minutes: 600, Computers: []GuestComputer{byHour}, Now: now, Location: almaty})
		assertErr(t, err, nil)
		if q.Minutes != 600 || !q.Computers[0].SessionEndsAt.Equal(now.Add(10*time.Hour)) {
			t.Fatalf("%+v", q)
		}
	})
}
