package subscription

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type oracle struct {
	PHP       string     `json:"php"`
	Amount    [][5]int64 `json:"amount"`
	Monthly   [][3]int64 `json:"monthly"`
	Earning   [][3]int64 `json:"earning"`
	AddMonths [][3]any   `json:"add_months"`
}

func loadOracle(t *testing.T) oracle {
	t.Helper()
	raw, err := os.ReadFile("testdata/php_subscription.json")
	if err != nil {
		t.Fatal(err)
	}
	var o oracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(o.PHP, "8.4.") {
		t.Fatalf("фикстура снята на PHP %s, нужен 8.4 (прод старого Laravel)", o.PHP)
	}
	return o
}

// Эталонные сценарии и пробелы покрытия: числа — из тестов Laravel (SubscriptionPaymentTest, SuperadminCabinetTest)
// и docs/golden-scenarios.md.
func TestAmount(t *testing.T) {
	for _, tc := range []struct {
		name             string
		price, computers int64
		months, discount int
		want             int64
	}{
		{"GS-552 1000 ₸ × 3 ПК × 3 мес − 10 %", 100000, 3, 3, 10, 810000},
		{"GS-556 1000 ₸ × 2 ПК × 6 мес", 100000, 2, 6, 0, 1200000},
		{"GS-559 1000 ₸ × 2 ПК × 1 мес", 100000, 2, 1, 0, 200000},
		{"GAP-037 3187,5 ₸ → 3188 ₸", 125000, 3, 1, 15, 318800},
		{"GAP-037 цена 999,99 ₸ × 3 ПК", 99999, 3, 1, 0, 300000},
		{"GAP-037 цена 999,99 ₸ × 1 ПК", 99999, 1, 1, 0, 100000},
		{"GAP-037 скидка 100 %", 100000, 5, 12, 100, 0},
		{"GAP-037 0 ПК считается как 1", 100000, Computers(0), 1, 0, 100000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Amount(tc.price, tc.computers, tc.months, tc.discount); got != tc.want {
				t.Fatalf("Amount = %d, want %d", got, tc.want)
			}
		})
	}
}

// Сверка с PHP 8.4 (BR-180, GAP-037): при цене в целых тенге — всегда равно; при цене с тиынами — равно, кроме сумм
// ровно на половине тенге, где float PHP может дать на 1 ₸ меньше точного округления (UB-1). Сумма — целые тенге.
func TestAmountMatchesPHP(t *testing.T) {
	o := loadOracle(t)
	var whole, halves, lower int
	for _, c := range o.Amount {
		price, computers, months, discount, php := c[0], c[1], int(c[2]), int(c[3]), c[4]
		got := Amount(price, computers, months, discount)
		if got%100 != 0 {
			t.Fatalf("Amount(%d, %d, %d, %d) = %d — не целые тенге", price, computers, months, discount, got)
		}
		half := price*computers*int64(months)*int64(100-discount)%10000 == 5000
		switch {
		case got == php:
		case half && price%100 != 0 && got == php+100:
			lower++
		default:
			t.Fatalf("Amount(%d, %d, %d, %d) = %d, PHP %d", price, computers, months, discount, got, php)
		}
		if price%100 == 0 {
			whole++
		}
		if half {
			halves++
		}
	}
	if whole < 5000 || halves < 1500 {
		t.Fatalf("фикстура неполная: цен в целых тенге %d, половин %d", whole, halves)
	}
	t.Logf("кейсов %d, на половине тенге %d, из них PHP на 1 ₸ меньше: %d", len(o.Amount), halves, lower)
}

func TestMonthlyPrice(t *testing.T) {
	for _, tc := range []struct {
		name     string
		price    int64
		discount int
		want     int64
	}{
		{"GS-536 50 000 ₸ − 20 % = 40 000 ₸", 5000000, 20, 4000000},
		{"GS-536 скидка кончилась — 50 000 ₸", 5000000, 0, 5000000},
		{"GAP-038 третий знак: 1005 ₸ − 50 % = 502,5 ₸", 100500, 50, 50250},
		{"GAP-038 половина тиына: 0,35 ₸ − 10 % = 0,315 → 0,32 ₸ (PHP: 0,31)", 35, 10, 32},
		{"скидка 100 %", 100000, 100, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MonthlyPrice(tc.price, tc.discount); got != tc.want {
				t.Fatalf("MonthlyPrice = %d, want %d", got, tc.want)
			}
		})
	}
}

// Сверка с PHP 8.4 (BR-182, GAP-038): при цене в целых тенге — всегда равно; расхождение возможно только на
// половине тиына при цене с тиынами (float PHP даёт на тиын меньше).
func TestMonthlyPriceMatchesPHP(t *testing.T) {
	o := loadOracle(t)
	var lower int
	for _, c := range o.Monthly {
		price, discount, php := c[0], int(c[1]), c[2]
		got := MonthlyPrice(price, discount)
		half := price*int64(100-discount)%100 == 50
		switch {
		case got == php:
		case half && price%100 != 0 && got == php+1:
			lower++
		default:
			t.Fatalf("MonthlyPrice(%d, %d) = %d, PHP %d", price, discount, got, php)
		}
	}
	t.Logf("кейсов %d, PHP на тиын меньше на половине: %d", len(o.Monthly), lower)
}

// GAP-038: «N ₸ за K ПК» на экране — произведение уже округлённой цены на число ПК; сумма счёта на 1 месяц
// округляет итог. При цене с тиынами они расходятся на тенге: 1000,05 ₸ − 15 % = 850,0425 → 850,04 ₸ за ПК,
// за 250 ПК на экране 212 510 ₸, а счёт — 212 510,625 → 212 511 ₸.
func TestGAP038MonthlyTotalDiffersFromInvoice(t *testing.T) {
	const price, computers, discount = 100005, 250, 15
	shown := MonthlyPrice(price, discount) * computers
	invoice := Amount(price, computers, 1, discount)
	if shown != 21251000 || invoice != 21251100 {
		t.Fatalf("на экране %d, счёт %d", shown, invoice)
	}
	// При цене в целых тенге расхождения нет.
	if shown, invoice := MonthlyPrice(5000000, 20)*12, Amount(5000000, 12, 1, 20); shown != invoice {
		t.Fatalf("целые тенге: на экране %d, счёт %d", shown, invoice)
	}
}

func TestResellerEarning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		amount int64
		bp     int
		want   int64
	}{
		{"GAP-040 4810 ₸ × 12,35 % = 594,035 → 594,04 ₸", 481000, 1235, 59404},
		{"GAP-040 процент 0", 481000, 0, 0},
		{"GAP-040 процент 100", 481000, 10000, 481000},
		{"8100 ₸ × 10 %", 810000, 1000, 81000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResellerEarning(tc.amount, tc.bp); got != tc.want {
				t.Fatalf("ResellerEarning = %d, want %d", got, tc.want)
			}
		})
	}
}

// Сверка с PHP 8.4 (BR-186, GAP-040): расхождение возможно только на половине тиына (float PHP — на тиын меньше).
func TestResellerEarningMatchesPHP(t *testing.T) {
	o := loadOracle(t)
	var lower int
	for _, c := range o.Earning {
		amount, bp, php := c[0], int(c[1]), c[2]
		got := ResellerEarning(amount, bp)
		half := amount*int64(bp)%10000 == 5000
		switch {
		case got == php:
		case half && got == php+1:
			lower++
		default:
			t.Fatalf("ResellerEarning(%d, %d) = %d, PHP %d", amount, bp, got, php)
		}
	}
	t.Logf("кейсов %d, PHP на тиын меньше на половине: %d", len(o.Earning), lower)
}

func TestAddMonthsNoOverflowMatchesPHP(t *testing.T) {
	o := loadOracle(t)
	if len(o.AddMonths) == 0 {
		t.Fatal("нет кейсов")
	}
	for _, c := range o.AddMonths {
		from, _ := time.Parse(time.RFC3339, c[0].(string))
		months := int(c[1].(float64))
		want, _ := time.Parse(time.RFC3339, c[2].(string))
		if got := AddMonthsNoOverflow(from, months); !got.Equal(want) {
			t.Fatalf("AddMonthsNoOverflow(%s, %d) = %s, PHP %s", c[0], months, got.Format(time.RFC3339), c[2])
		}
	}
}

func TestPeriodEnd(t *testing.T) {
	now := time.Date(2026, 11, 15, 7, 0, 0, 0, time.UTC)
	at := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return &v
	}
	for _, tc := range []struct {
		name          string
		status        string
		trial, period *time.Time
		months        int
		want          string
	}{
		{"GS-556 активна до +10 дней: от конца периода", "active", nil, at("2026-11-25T07:00:00Z"), 6, "2027-05-25T07:00:00Z"},
		{"GS-557 истекла месяц назад: от сегодня", "expired", nil, at("2026-10-15T07:00:00Z"), 1, "2026-12-15T07:00:00Z"},
		{"GAP-039 пробный период: от его конца", "trialing", at("2026-11-20T12:00:00Z"), nil, 1, "2026-12-20T12:00:00Z"},
		{"GAP-039 пробный период кончился: от сегодня", "trialing", at("2026-11-01T12:00:00Z"), nil, 3, "2027-02-15T07:00:00Z"},
		{"GAP-039 бессрочная активная получает дату", "active", nil, nil, 12, "2027-11-15T07:00:00Z"},
		{"GAP-039 активная с прошедшим сроком: от сегодня", "active", nil, at("2026-11-01T00:00:00Z"), 1, "2026-12-15T07:00:00Z"},
		{"GAP-039 приостановлена: от сегодня, старый срок не учитывается", "suspended", nil, at("2027-03-01T00:00:00Z"), 1, "2026-12-15T07:00:00Z"},
		{"GAP-039 подписки нет: от сегодня", "", nil, nil, 6, "2027-05-15T07:00:00Z"},
		{"GAP-039 конец 31.01 + 1 месяц = 28.02", "active", nil, at("2027-01-31T10:00:00Z"), 1, "2027-02-28T10:00:00Z"},
		{"GAP-039 конец 31.01 високосного года + 1 месяц = 29.02", "active", nil, at("2028-01-31T10:00:00Z"), 1, "2028-02-29T10:00:00Z"},
		{"пробный период не считается у активной", "active", at("2027-06-01T00:00:00Z"), at("2026-11-25T07:00:00Z"), 1, "2026-12-25T07:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PeriodEnd(now, tc.status, tc.trial, tc.period, tc.months).Format(time.RFC3339); got != tc.want {
				t.Fatalf("PeriodEnd = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestOwnDiscount(t *testing.T) {
	almaty := time.FixedZone("+05", 5*3600)
	until := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		until *time.Time
		now   time.Time
		loc   *time.Location
		want  int
	}{
		{"GS-536 бессрочная", nil, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), almaty, 20},
		{"GS-536 за минуту до конца последнего дня (время клуба)", &until, time.Date(2026, 11, 25, 23, 59, 0, 0, almaty), almaty, 20},
		{"GS-536 через два дня", &until, time.Date(2026, 11, 27, 23, 59, 0, 0, almaty), almaty, 0},
		{"GAP-055 полночь клуба после последнего дня", &until, time.Date(2026, 11, 26, 0, 0, 0, 0, almaty), almaty, 0},
		{"GAP-055 в UTC ещё последний день, в клубе уже следующий", &until, time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC), almaty, 0},
		{"GAP-055 тот же момент по UTC — скидка ещё действует", &until, time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC), time.UTC, 20},
		{"без пояса — UTC", &until, time.Date(2026, 11, 25, 23, 59, 59, 0, time.UTC), nil, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := OwnDiscount(20, tc.until, tc.now, tc.loc); got != tc.want {
				t.Fatalf("OwnDiscount = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDiscountTakesTheLarger(t *testing.T) {
	for _, tc := range [][3]int{{10, 0, 10}, {0, 15, 15}, {10, 15, 15}, {20, 15, 20}, {0, 0, 0}} {
		if got := Discount(tc[0], tc[1]); got != tc[2] {
			t.Fatalf("Discount(%d, %d) = %d, want %d", tc[0], tc[1], got, tc[2])
		}
	}
}

func TestNumberAndMonths(t *testing.T) {
	for id, want := range map[int64]string{1: "ZF-00001", 42: "ZF-00042", 99999: "ZF-99999", 123456: "ZF-123456"} {
		if got := Number(id); got != want {
			t.Fatalf("Number(%d) = %q, want %q", id, got, want)
		}
	}
	for m := 0; m <= 13; m++ {
		if want := m == 1 || m == 3 || m == 6 || m == 12; ValidMonths(m) != want {
			t.Fatalf("ValidMonths(%d) = %v", m, !want)
		}
	}
}
