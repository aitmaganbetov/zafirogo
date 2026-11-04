package pricing

import (
	"testing"
	"time"
)

func at(t *testing.T, s string, loc *time.Location) time.Time {
	t.Helper()
	if loc == nil {
		loc = time.UTC
	}
	v, err := time.ParseInLocation("2006-01-02 15:04:05", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func period(sd, st, ed, et string) WeeklyPeriod {
	return WeeklyPeriod{StartDay: sd, StartTime: st, EndDay: ed, EndTime: et}
}

// GAP-017, BR-017/пример: период через конец недели, конец включительно до минуты. 2026-10-31 — суббота.
func TestWeeklyPeriods_WrapAndInclusiveEnd(t *testing.T) {
	wrap := WeeklyPeriods{period("sat", "22:00", "mon", "06:00")}
	cases := []struct {
		name    string
		periods WeeklyPeriods
		at      string
		want    bool
	}{
		{"GAP-017/sat_21:59", wrap, "2026-10-31 21:59:00", false},
		{"GAP-017/sat_22:00", wrap, "2026-10-31 22:00:00", true},
		{"GAP-017/sun_03:00", wrap, "2026-11-01 03:00:00", true},
		{"GAP-017/mon_06:00", wrap, "2026-11-02 06:00:00", true},
		{"GAP-017/mon_06:00:59", wrap, "2026-11-02 06:00:59", true},
		{"GAP-017/mon_06:01", wrap, "2026-11-02 06:01:00", false},
		{"GAP-017/unknown_day", WeeklyPeriods{period("xyz", "00:00", "sun", "23:59")}, "2026-10-31 12:00:00", false},
		{"GAP-017/empty", WeeklyPeriods{}, "2026-10-31 12:00:00", true},
		{"GAP-017/null", nil, "2026-10-31 12:00:00", true},
		{"BR-017/пример/вс_03:15", wrap, "2026-11-01 03:15:00", true},
		{"BR-017/пример/пн_06:00:59", wrap, "2026-11-02 06:00:59", true},
		{"BR-017/пример/пн_06:01", wrap, "2026-11-02 06:01:00", false},
		// Обычный период: начало и конец включительно.
		{"BR-017/обычный/пн_08:59", WeeklyPeriods{period("mon", "09:00", "fri", "18:00")}, "2026-10-26 08:59:59", false},
		{"BR-017/обычный/пн_09:00", WeeklyPeriods{period("mon", "09:00", "fri", "18:00")}, "2026-10-26 09:00:00", true},
		{"BR-017/обычный/пт_18:00:59", WeeklyPeriods{period("mon", "09:00", "fri", "18:00")}, "2026-10-30 18:00:59", true},
		{"BR-017/обычный/пт_18:01", WeeklyPeriods{period("mon", "09:00", "fri", "18:00")}, "2026-10-30 18:01:00", false},
		// Неверное время — 0 минут (minutes()): "9:00" → 00:00 дня.
		{"BR-017/время_9:00_как_00:00", WeeklyPeriods{period("wed", "9:00", "wed", "00:30")}, "2026-10-28 00:15:00", true},
		// Один день, start == end — ровно одна минута.
		{"BR-017/одна_минута", WeeklyPeriods{period("wed", "12:00", "wed", "12:00")}, "2026-10-28 12:00:30", true},
		{"BR-017/одна_минута/после", WeeklyPeriods{period("wed", "12:00", "wed", "12:00")}, "2026-10-28 12:01:00", false},
		// Второй период совпадает, первый нет.
		{"BR-017/несколько", WeeklyPeriods{period("mon", "00:00", "mon", "01:00"), period("sat", "10:00", "sun", "20:00")}, "2026-11-01 19:00:00", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.periods.Contains(at(t, c.at, nil)); got != c.want {
				t.Fatalf("Contains(%s) = %v, want %v", c.at, got, c.want)
			}
		})
	}
}

// GS-071: периоды — по местному времени клуба (Asia/Almaty, UTC+5), а не UTC.
// В 19:00 по Алматы (14:00 UTC) тариф «сб 08:00–15:00» не продаётся, хотя по UTC попал бы в период.
func TestTariffPeriods_ClubLocalTimeNotUTC(t *testing.T) {
	almaty := time.FixedZone("UTC+5", 5*3600) // Asia/Almaty с 2024-03-01 — UTC+5 (в тесте старого проекта так же)
	tariff := Tariff{
		Price: 100000, DurationMinutes: 60, PaymentMethod: MainOrBonus, IsActive: true,
		SalePeriods:     WeeklyPeriods{period("sat", "08:00", "sat", "15:00")},
		ValidityPeriods: WeeklyPeriods{period("sat", "08:00", "sat", "18:00")},
	}
	evening := at(t, "2026-10-24 19:00:00", almaty).UTC() // момент хранится в UTC
	noon := at(t, "2026-10-24 12:00:00", almaty).UTC()

	t.Run("GS-071/19:00_Almaty", func(t *testing.T) {
		if got := tariff.Availability("client", 0, evening, almaty); got != NotOnSale {
			t.Fatalf("Availability = %q, want not_on_sale", got)
		}
		if got := tariff.Availability("client", 0, evening, time.UTC); got != Available {
			t.Fatalf("по UTC (14:00) тариф доступен, получили %q — тест не проверяет часовой пояс", got)
		}
		_, err := QuoteTariffPurchase(TariffPurchaseInput{Tariff: tariff, Rules: DefaultPaymentRules(), Role: "owner", UserID: 1,
			Balances: Balances{Main: 1000000}, Now: evening, Location: almaty})
		assertErr(t, err, ErrTariffUnavailable)
	})
	t.Run("GS-071/12:00_Almaty", func(t *testing.T) {
		if got := tariff.Availability("client", 0, noon, almaty); got != Available {
			t.Fatalf("Availability = %q, want available", got)
		}
	})
	t.Run("GS-071/Asia/Almaty_tzdata", func(t *testing.T) {
		loc, err := time.LoadLocation("Asia/Almaty")
		if err != nil {
			t.Skip("нет tzdata:", err)
		}
		if _, off := evening.In(loc).Zone(); off != 5*3600 {
			t.Skipf("tzdata даёт Asia/Almaty смещение %d (старая база)", off)
		}
		if tariff.OnSale(evening, loc) || !tariff.OnSale(noon, loc) {
			t.Fatal("Asia/Almaty: ожидали не в продаже в 19:00 и в продаже в 12:00")
		}
	})
}

// GS-063: тариф продаётся только в дни и часы продажи (агент, касса, покупка).
func TestTariffSalePeriods(t *testing.T) {
	night := Tariff{Price: 50000, DurationMinutes: 60, IsActive: true, SalePeriods: WeeklyPeriods{period("sat", "22:00", "sun", "06:00")}}
	weekdays := Tariff{Price: 50000, DurationMinutes: 60, IsActive: true, SalePeriods: WeeklyPeriods{period("mon", "09:00", "fri", "18:00")}}
	always := Tariff{Price: 50000, DurationMinutes: 60, IsActive: true}
	wed := at(t, "2026-10-28 12:00:00", nil)
	sat := at(t, "2026-10-31 23:00:00", nil)
	cases := []struct {
		name string
		t    Tariff
		now  time.Time
		want bool
	}{
		{"GS-063/среда/Ночь", night, wed, false},
		{"GS-063/среда/Будни", weekdays, wed, true},
		{"GS-063/среда/Всегда", always, wed, true},
		{"GS-063/суббота_23:00/Ночь", night, sat, true},
		{"GS-063/суббота_23:00/Будни", weekdays, sat, false},
		{"GS-063/суббота_23:00/Всегда", always, sat, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.t.Availability("client", 0, c.now, nil) == Available; got != c.want {
				t.Fatalf("доступен = %v, want %v", got, c.want)
			}
		})
	}
	t.Run("GS-063/касса_Ночь_в_среду", func(t *testing.T) {
		pc := GuestComputer{Name: "№1", ZonePricePerHour: tiyn(60000)}
		_, err := QuoteGuestSession(GuestSessionInput{Tariff: &night, Computers: []GuestComputer{pc}, Now: wed})
		assertErr(t, err, ErrGuestTariffUnavailable)
	})
}

// GS-068: причины, по которым тариф не предлагается: продано всё — раньше, чем вне периода действия.
func TestTariffList_ReasonsNotOffered(t *testing.T) {
	limit := int64(2)
	tariff := Tariff{Price: 50000, DurationMinutes: 60, IsActive: true, QuantityLimitEnabled: true, QuantityLimit: &limit,
		ValidityPeriods: WeeklyPeriods{period("mon", "00:00", "tue", "23:59")}}
	sat := at(t, "2026-10-31 16:00:00", nil)
	t.Run("GS-068/продано_2_из_2", func(t *testing.T) {
		if got := tariff.Availability("client", 2, sat, nil); got != SoldOut {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("GS-068/вне_периода_действия", func(t *testing.T) {
		if got := tariff.Availability("client", 1, sat, nil); got != OutsideValidity {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("GS-068/лимит_выключен", func(t *testing.T) {
		off := tariff
		off.QuantityLimitEnabled = false
		if got := off.Availability("client", 100, sat, nil); got != OutsideValidity {
			t.Fatalf("got %q", got)
		}
	})
}

func TestWeeklyPeriods_IsAlways(t *testing.T) {
	cases := []struct {
		name string
		p    WeeklyPeriods
		want bool
	}{
		{"BR-017/isAlways/nil", nil, true},
		{"BR-017/isAlways/вся_неделя", WeeklyPeriods{period("tue", "10:00", "tue", "11:00"), period("mon", "00:00", "sun", "23:59")}, true},
		{"BR-017/isAlways/почти", WeeklyPeriods{period("mon", "00:00", "sun", "23:58")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.IsAlways(); got != c.want {
				t.Fatalf("IsAlways = %v, want %v", got, c.want)
			}
		})
	}
}

// WeeklyPeriods::minutes(): '/^(\d{2}):(\d{2})$/', иначе 0.
func TestPeriodMinutes(t *testing.T) {
	cases := map[string]int{
		"00:00": 0, "06:00": 360, "23:59": 1439, "99:99": 6039, "12:30\n": 750,
		"9:00": 0, "09:0": 0, "09-00": 0, "": 0, "12:30\n\n": 0, " 12:30": 0, "１２:００": 0, "12:30:00": 0,
	}
	for in, want := range cases {
		if got := periodMinutes(in); got != want {
			t.Errorf("periodMinutes(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseWeeklyPeriods(t *testing.T) {
	p, err := ParseWeeklyPeriods([]byte(`[{"start_day":"sat","start_time":"22:00","end_day":"mon","end_time":"06:00"},{"start_day":1,"start_time":null,"end_day":"sun"}]`))
	if err != nil {
		t.Fatal(err)
	}
	want := WeeklyPeriods{period("sat", "22:00", "mon", "06:00"), period("", "", "sun", "")}
	if len(p) != 2 || p[0] != want[0] || p[1] != want[1] {
		t.Fatalf("got %+v", p)
	}
	for _, raw := range []string{"", "null", "[]"} {
		p, err := ParseWeeklyPeriods([]byte(raw))
		if err != nil || len(p) != 0 {
			t.Fatalf("%q: %v %v", raw, p, err)
		}
	}
}
