package reservations

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// splitLaravel — буквальная формула старого кода (с багом GAP-041), только для сравнения.
// Laravel: app/Services/GuestBookings.php GuestBookings::book().
func splitLaravel(prices []int64, bonus int64) (mains, bonuses []int64) {
	var price int64
	for _, p := range prices {
		price += p
	}
	left := bonus
	for i, p := range prices {
		var pc int64
		switch {
		case i == len(prices)-1:
			pc = left
		case price != 0:
			pc = money.IntDiv(bonus*p, price)
		}
		left -= pc
		mains = append(mains, p-pc)
		bonuses = append(bonuses, pc)
	}
	return mains, bonuses
}

func tiyns(v []int64) []money.Tiyn {
	out := make([]money.Tiyn, len(v))
	for i, x := range v {
		out[i] = money.Tiyn(x)
	}
	return out
}

// TestSplitBonus_GAP041_Characterization — баг старой формулы: три ПК по 600 ₸, бонусами 1799,99 ₸ (основной баланс
// 0,01 ₸) — последнему ПК достаётся 600,01 ₸ бонусов, его основная часть −0,01 ₸ (MySQL отклонял запись).
func TestSplitBonus_GAP041_Characterization(t *testing.T) {
	mains, bonuses := splitLaravel([]int64{60000, 60000, 60000}, 179999)
	if got, want := bonuses, []int64{59999, 59999, 60001}; !equal(got, want) {
		t.Fatalf("старая формула: бонусы %v, ожидалось %v", got, want)
	}
	if mains[2] != -1 {
		t.Fatalf("старая формула: основная часть последнего ПК %d, ожидалось −1 (баг GAP-041)", mains[2])
	}
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSplitBonus — BR-176: делёж бонусной части по ПК; UQ-8 — исправленный случай GAP-041.
func TestSplitBonus(t *testing.T) {
	cases := []struct {
		name   string
		prices []int64
		bonus  int64
		want   []int64
	}{
		{"GAP-041 два ПК разных зон 600 и 300, бонусы 450", []int64{60000, 30000}, 45000, []int64{30000, 15000}},
		{"GAP-041 три ПК по 600, бонусы 1799.99 (исправлено UQ-8)", []int64{60000, 60000, 60000}, 179999, []int64{59999, 60000, 60000}},
		{"всё бонусами", []int64{60000, 60000, 60000}, 180000, []int64{60000, 60000, 60000}},
		{"один тиын бонусов — последнему", []int64{60000, 60000, 60000}, 1, []int64{0, 0, 1}},
		{"один ПК", []int64{90000}, 12345, []int64{12345}},
		{"без бонусов", []int64{120000, 120000}, 0, []int64{0, 0}},
		{"цена 0", []int64{0, 0}, 0, []int64{0, 0}},
		{"десять ПК по тиыну, 9 бонусов", []int64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, 9, []int64{0, 1, 1, 1, 1, 1, 1, 1, 1, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := SplitBonus(tiyns(c.prices), money.Tiyn(c.bonus))
			if !ok {
				t.Fatal("SplitBonus отклонил вход")
			}
			for i := range c.want {
				if int64(got[i]) != c.want[i] {
					t.Fatalf("доли %v, ожидалось %v", got, c.want)
				}
			}
		})
	}
	for _, bad := range []struct {
		prices []int64
		bonus  int64
	}{{nil, 0}, {[]int64{100}, 101}, {[]int64{100}, -1}, {[]int64{-1, 5}, 1}} {
		if _, ok := SplitBonus(tiyns(bad.prices), money.Tiyn(bad.bonus)); ok {
			t.Errorf("SplitBonus(%v, %d) должен отклонить вход", bad.prices, bad.bonus)
		}
	}
}

// checkSplit — свойства дележа: сумма долей = бонусной части, 0 ≤ доля ≤ цена ПК (основная часть не отрицательна),
// сумма основных частей = оплате с основного баланса; где старая формула работала — числа те же.
func checkSplit(t *testing.T, prices []int64, bonus int64) {
	t.Helper()
	got, ok := SplitBonus(tiyns(prices), money.Tiyn(bonus))
	if !ok {
		t.Fatalf("SplitBonus(%v, %d) отклонил вход", prices, bonus)
	}
	var total, sumBonus, sumMain int64
	for i, p := range prices {
		total += p
		if got[i] < 0 || int64(got[i]) > p {
			t.Fatalf("SplitBonus(%v, %d) = %v: доля ПК %d вне 0…%d", prices, bonus, got, i, p)
		}
		sumBonus += int64(got[i])
		sumMain += p - int64(got[i])
	}
	if sumBonus != bonus || sumMain != total-bonus || sumMain+sumBonus != total {
		t.Fatalf("SplitBonus(%v, %d) = %v: сумма долей %d, основных частей %d при оплате %d", prices, bonus, got, sumBonus, sumMain, total)
	}
	mains, old := splitLaravel(prices, bonus)
	valid := true
	for _, m := range mains {
		valid = valid && m >= 0
	}
	if valid {
		for i := range old {
			if old[i] != int64(got[i]) {
				t.Fatalf("SplitBonus(%v, %d) = %v, старая формула (без ошибки) давала %v", prices, bonus, got, old)
			}
		}
	}
}

// TestSplitBonus_Property — случайные входы: сумма частей по ПК равна общей оплате до тиына.
func TestSplitBonus_Property(t *testing.T) {
	r := rand.New(rand.NewPCG(20261115, 4))
	for range 200000 {
		n := 1 + r.IntN(10)
		prices := make([]int64, n)
		var total int64
		for i := range prices {
			switch r.IntN(4) {
			case 0:
				prices[i] = r.Int64N(8)
			case 1:
				prices[i] = 1 + r.Int64N(999_999_999_999) // до pricing.MaxPrice
			default:
				prices[i] = 1 + r.Int64N(3_000_000)
			}
			total += prices[i]
		}
		var bonus int64
		switch r.IntN(4) {
		case 0:
			bonus = total
		case 1:
			bonus = max(0, total-r.Int64N(13))
		default:
			bonus = r.Int64N(total + 1)
		}
		if total > 3_000_000_000 {
			// Произведение bonus × цена в старой формуле вышло бы за int64 (в PHP — float и TypeError intdiv) —
			// сверяем только свойства.
			got, ok := SplitBonus(tiyns(prices), money.Tiyn(bonus))
			if !ok {
				t.Fatalf("SplitBonus(%v, %d) отклонил вход", prices, bonus)
			}
			var sum int64
			for i, s := range got {
				if s < 0 || int64(s) > prices[i] {
					t.Fatalf("SplitBonus(%v, %d) = %v: доля вне цены", prices, bonus, got)
				}
				sum += int64(s)
			}
			if sum != bonus {
				t.Fatalf("SplitBonus(%v, %d) = %v: сумма %d", prices, bonus, got, sum)
			}
			continue
		}
		checkSplit(t, prices, bonus)
	}
}

type bookingFixture struct {
	PHP      string            `json:"php"`
	Duration [][]int64         `json:"duration"`
	Start    [][]int64         `json:"start"`
	RawSplit []json.RawMessage `json:"split"`
	Booked   [][]int64         `json:"booked"`
}

func loadBookingFixture(t *testing.T) bookingFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/php_booking.json")
	if err != nil {
		t.Fatal(err)
	}
	var f bookingFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.PHP, "8.4.") {
		t.Fatalf("фикстура снята на PHP %s, нужен 8.4.x (tools/php-oracle/gen-booking-fixture.php)", f.PHP)
	}
	return f
}

// TestBookingMatchesPHP — формулы брони против эталона PHP 8.4 (tools/php-oracle/gen-booking-fixture.php):
// durationMinutes() (round от float), startPaidSession() (минуты и суммы части), bookedMinutes() (ceil от float),
// делёж бонусов (там, где старая формула не давала отрицательной основной части).
func TestBookingMatchesPHP(t *testing.T) {
	f := loadBookingFixture(t)
	at := func(s int64) time.Time { return time.Unix(s, 0).UTC() }
	for _, c := range f.Duration {
		if got := DurationMinutes(at(c[0]), at(c[1])); got != c[2] {
			t.Errorf("DurationMinutes(%d с) = %d, PHP %d", c[1]-c[0], got, c[2])
		}
	}
	for _, c := range f.Start {
		p := PlanStart(at(c[0]), at(c[1]), at(c[2]), money.Tiyn(c[3]), money.Tiyn(c[4]))
		if p.Duration != c[5] || p.Minutes != c[6] || int64(p.Main) != c[7] || int64(p.Bonus) != c[8] {
			t.Errorf("PlanStart(длина %d с, опоздание %d с, %d + %d) = %+v, PHP %v", c[1]-c[0], c[2]-c[0], c[3], c[4], p, c[5:])
		}
	}
	for _, c := range f.Booked {
		if got := BookedMinutes(c[0], at(c[1]), at(c[2]), true); got != c[3] {
			t.Errorf("BookedMinutes(%d мин, до конца периода %d с) = %d, PHP %d", c[0], c[2]-c[1], got, c[3])
		}
	}
	negative := 0
	for _, raw := range f.RawSplit {
		var row []json.RawMessage
		var prices []int64
		var bonus int64
		var parts [][2]int64
		if json.Unmarshal(raw, &row) != nil || len(row) != 3 || json.Unmarshal(row[0], &prices) != nil ||
			json.Unmarshal(row[1], &bonus) != nil || json.Unmarshal(row[2], &parts) != nil {
			t.Fatalf("строка фикстуры split: %s", raw)
		}
		// Go-копия старой формулы совпадает с PHP — значит, сравнение со «старой формулой» в свойствах честное.
		mains, bonuses := splitLaravel(prices, bonus)
		valid := true
		for i, p := range parts {
			if mains[i] != p[0] || bonuses[i] != p[1] {
				t.Fatalf("splitLaravel(%v, %d) = %v/%v, PHP %v", prices, bonus, mains, bonuses, parts)
			}
			valid = valid && p[0] >= 0
		}
		if !valid {
			negative++
		}
		checkSplit(t, prices, bonus)
	}
	if negative == 0 {
		t.Error("в фикстуре нет случая GAP-041 (отрицательная основная часть в старой формуле)")
	}
}

// TestPlanStart — BR-178 на числах сценариев.
func TestPlanStart(t *testing.T) {
	start := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	cases := []struct {
		name          string
		now           time.Time
		minutes, main int64
	}{
		{"GS-544 пришёл за 5 минут до начала — вся длительность", start.Add(-5 * time.Minute), 60, 60000},
		{"UQ-16 сотрудник отметил приход за сутки", start.Add(-24 * time.Hour), 60, 60000},
		{"GS-545 опоздал на 30 минут — половина времени и денег", start.Add(30 * time.Minute), 30, 30000},
		{"GAP-044 за секунду до конца — одна минута", end.Add(-time.Second), 1, 1000},
		{"GAP-044 опоздал на 30 минут и секунду — округление вверх", start.Add(30*time.Minute + time.Second), 30, 30000},
		{"бронь закончилась — запускать нечего", end, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := PlanStart(start, end, c.now, 60000, 0)
			if p.Minutes != c.minutes || int64(p.Main) != c.main || p.Bonus != 0 || p.Duration != 60 {
				t.Fatalf("PlanStart = %+v, ожидалось %d мин, %d тиын", p, c.minutes, c.main)
			}
		})
	}
}
