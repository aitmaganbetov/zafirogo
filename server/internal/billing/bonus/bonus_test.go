package bonus

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// lv — уровень с целыми порогом и процентом (как после normalize() для "2000" / "5").
func lv(threshold int64, percent float64) Level {
	p := FloatNumber(percent)
	if percent == math.Trunc(percent) {
		p = IntNumber(int64(percent))
	}
	return Level{Threshold: IntNumber(threshold), Percent: p}
}

// mustJSON — уровни/настройки из JSON-литерала, как их декодировал бы PHP.
func mustJSON(t *testing.T, s string) Value {
	t.Helper()
	v, err := DecodeJSON([]byte(s))
	if err != nil {
		t.Fatalf("DecodeJSON(%s): %v", s, err)
	}
	return v
}

func levelsString(levels []Level) string {
	parts := make([]string, len(levels))
	for i, l := range levels {
		kind := func(n Number) string {
			if n.IsFloat() {
				return "f"
			}
			return "i"
		}
		parts[i] = l.Threshold.String() + kind(l.Threshold) + ":" + l.Percent.String() + kind(l.Percent)
	}
	return strings.Join(parts, "; ")
}

// cents — тиыны из десятичной строки в тестах.
func cents(t *testing.T, s string) int64 {
	t.Helper()
	v, err := money.ParseTenge(s)
	if err != nil {
		t.Fatalf("ParseTenge(%q): %v", s, err)
	}
	return int64(v)
}

// mul — x*y во время выполнения (константное выражение Go вычислилось бы точно, без float64).
//
//go:noinline
func mul(x, y float64) float64 { return x * y }

// TestTopUpFor — BR-041 и сценарии GS/GAP бонуса за пополнение (TopUpBonus::for()).
func TestTopUpFor(t *testing.T) {
	gs002 := []Level{lv(2000, 5), lv(10000, 10)}
	tests := []struct {
		name        string
		levels      []Level
		amount      float64 // (float) $data['amount']
		wantPercent float64
		wantCents   money.Tiyn
	}{
		// GS-002: {2000 → 5%}, {10000 → 10%}: 1000 — ниже порога; 2500 → 125.00; 10000.55 → 1000.055 → 1000.06.
		{"GS-002/1000_ниже_порога", gs002, 1000, 0, 0},
		{"GS-002/2500_5%", gs002, 2500, 5, 12500},
		{"GS-002/10000.55_10%_half_up", gs002, 10000.55, 10, 100006},
		// GS-003: бонус выключен → уровни в силе пусты → бонуса нет.
		{"GS-003/выключен", nil, 1000, 0, 0},
		// GS-005 и GS-277: {1000 → 10%}, пополнение 2000 → 200.00.
		{"GS-005/2000_10%", []Level{lv(1000, 10)}, 2000, 10, 20000},
		{"GS-277/2000_картой_10%", []Level{lv(1000, 10)}, 2000, 10, 20000},
		// BR-041: 5000.00 при 7.5 % → 375.00; 333.33 при 7.5 % → 25.00 (точно 24.99975).
		{"BR-041/5000_7.5%", []Level{{IntNumber(5000), FloatNumber(7.5)}}, 5000, 7.5, 37500},
		{"BR-041/333.33_7.5%", []Level{{IntNumber(1), FloatNumber(7.5)}}, 333.33, 7.5, 2500},
		// BR-040 (пример для пополнения): [{1000 → 5}, {5000 → 7.5}]: 5000.00 → 7.5; 4999.99 → 5.
		{"BR-040/5000_порог_включительно", []Level{lv(1000, 5), {IntNumber(5000), FloatNumber(7.5)}}, 5000, 7.5, 37500},
		{"BR-040/4999.99", []Level{lv(1000, 5), {IntNumber(5000), FloatNumber(7.5)}}, 4999.99, 5, 25000},
		// GAP-018: {500 → 2.5%}, 999.99 → intdiv(99999 × 250 + 5000, 10000) = 2500.
		{"GAP-018/дробный_процент_half_up", []Level{{IntNumber(500), FloatNumber(2.5)}}, 999.99, 2.5, 2500},
		// GAP-019: {2000 → 5%}, {5000 → 3%}: 1999.99 → 0; 2000.00 → 100.00; 6000.00 → максимум достигнутых 5% = 300.00.
		{"GAP-019/1999.99", []Level{lv(2000, 5), lv(5000, 3)}, 1999.99, 0, 0},
		{"GAP-019/2000_порог_включительно", []Level{lv(2000, 5), lv(5000, 3)}, 2000, 5, 10000},
		{"GAP-019/6000_максимум_достигнутых", []Level{lv(2000, 5), lv(5000, 3)}, 6000, 5, 30000},
		// Граничные: 100 % → бонус равен сумме; процент 0.01 → bp 1.
		{"edge/100%", []Level{lv(1, 100)}, 123.45, 100, 12345},
		{"edge/0.01%_half_up", []Level{{IntNumber(1), FloatNumber(0.01)}}, 50, 0.01, 1}, // 5000*1+5000 = 10000 → 1
		{"edge/0.01%_вниз", []Level{{IntNumber(1), FloatNumber(0.01)}}, 49.99, 0.01, 0}, // 4999+5000 = 9999 → 0
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TopUpFor(tt.levels, tt.amount)
			if err != nil {
				t.Fatal(err)
			}
			if got.Percent != tt.wantPercent || got.Cents != tt.wantCents {
				t.Fatalf("TopUpFor(%v) = {%v, %d}, want {%v, %d}", tt.amount, got.Percent, got.Cents, tt.wantPercent, tt.wantCents)
			}
		})
	}
}

// TestTopUpForPHPRounding — перевод суммы в центы — (int) round($amount * 100) PHP 8.3 (BR-001, BR-041).
func TestTopUpForPHPRounding(t *testing.T) {
	levels := []Level{{Threshold: FloatNumber(100.01), Percent: IntNumber(10)}}
	// BR-041: ввод 100.005 → round(10000.4999…) = 10001 (PHP), т.е. порог/бонус считаются от 100.01.
	got, err := TopUpFor(levels, 100.005)
	if err != nil {
		t.Fatal(err)
	}
	if got.Percent != 10 || got.Cents != 1000 { // intdiv(10001*1000+5000, 10000) = 1000
		t.Fatalf("BR-041/100.005: %+v", got)
	}
	// Строка из запроса: (float) "100.005".
	if s, _ := TopUpForTengeString(levels, "100.005"); s != got {
		t.Fatalf("TopUpForTengeString = %+v, want %+v", s, got)
	}
	// MQ-2: 1.005 → 100 центов в PHP 8.4+ (round() без знаков = math.Round; в PHP 8.3 было 101).
	if c := money.FromTengeFloat(mul(1.005, 1)); c != 100 {
		t.Fatalf("FromTengeFloat(1.005) = %d", c)
	}
	// Переполнение int64 в intdiv() — в PHP TypeError, здесь ErrOverflow.
	if _, err := TopUpForCents([]Level{lv(1, 1e6)}, math.MaxInt64/100); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow: err = %v", err)
	}
}

// TestPercentAt — BR-040: наибольший из достигнутых, порог включительно.
func TestPercentAt(t *testing.T) {
	tests := []struct {
		name   string
		levels []Level
		cents  int64
		want   float64
	}{
		{"BR-040/5000.00_7.5", []Level{lv(1000, 5), {IntNumber(5000), FloatNumber(7.5)}}, 500000, 7.5},
		{"BR-040/4999.99_5", []Level{lv(1000, 5), {IntNumber(5000), FloatNumber(7.5)}}, 499999, 5},
		{"BR-040/немонотонные_6000_10", []Level{lv(1000, 10), lv(5000, 5)}, 600000, 10},
		{"BR-040/ничего_не_достигнуто", []Level{lv(1000, 10)}, 99999, 0},
		{"BR-040/пусто", nil, 1 << 40, 0},
		// Дробный порог (normalize оставляет float): 1000.56 → (int) round(100056.0) = 100056.
		{"BR-040/дробный_порог_включительно", []Level{{FloatNumber(1000.56), IntNumber(3)}}, 100056, 3},
		{"BR-040/дробный_порог_минус_тиын", []Level{{FloatNumber(1000.56), IntNumber(3)}}, 100055, 0},
		// Отрицательный процент (сырые настройки без проверки): max(0.0, -5) = 0.
		{"edge/отрицательный_процент", []Level{lv(1, -5)}, 100, 0},
		// int-порог × 100 переполняет int64 → в PHP float → (int) по модулю 2^64; здесь так же.
		{"edge/огромный_порог", []Level{lv(math.MaxInt64/10, 7)}, math.MaxInt64, phpOverflowReached(math.MaxInt64/10, math.MaxInt64, 7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PercentAt(tt.levels, tt.cents); got != tt.want {
				t.Fatalf("PercentAt = %v, want %v", got, tt.want)
			}
		})
	}
}

// phpOverflowReached — ожидание для порога, у которого int * 100 переполняется (PHP: float → (int) mod 2^64).
func phpOverflowReached(threshold, cents int64, percent float64) float64 {
	if cents >= money.PHPIntCast(money.PHPRound(float64(threshold)*100, 0)) {
		return percent
	}
	return 0
}

// TestNormalize — PercentLevels::normalize() (BR-040, GS-001, GS-027, GAP-021).
func TestNormalize(t *testing.T) {
	tests := []struct {
		name      string
		json      string
		threshold string
		want      string // levelsString: значение + тип (i — int, f — float)
	}{
		// GS-001: пустая строка отброшена, сортировка по сумме, числа целые.
		{"GS-001", `[{"amount":"10000","percent":"10"},{"amount":"","percent":""},{"amount":"2000","percent":"5"}]`, ThresholdAmount, "2000i:5i; 10000i:10i"},
		// GS-027: проценты 5.5 остаются float, 10 — int.
		{"GS-027", `[{"spent":"50000","percent":"10"},{"spent":"","percent":""},{"spent":"10000","percent":"5.5"}]`, ThresholdSpent, "10000i:5.5f; 50000i:10i"},
		{"GS-027/пустая_строка_уровней", `""`, ThresholdSpent, ""},
		{"GS-027/null", `null`, ThresholdSpent, ""},
		// GAP-021: "1000.555" → 1000.56 (PHP round по десятичной записи), "10" → 10.
		{"GAP-021", `[{"amount":"1000.555","percent":"10"}]`, ThresholdAmount, "1000.56f:10i"},
		// BR-040: round(12.345, 2) = 12.35.
		{"BR-040/round_12.345", `[{"spent":12.345,"percent":"7.50"}]`, ThresholdSpent, "12.35f:7.5f"},
		{"BR-040/10.00_целое", `[{"spent":"10.00","percent":10.0}]`, ThresholdSpent, "10i:10i"},
		{"BR-040/не_тот_ключ", `[{"amount":1000,"percent":5}]`, ThresholdSpent, ""},
		{"BR-040/не_массивы_и_нечисла", `[5, "x", null, {"amount":"abc","percent":5}, {"amount":true,"percent":5}, {"amount":1,"percent":[1]}, {"amount":" 7 ","percent":"1e1"}]`, ThresholdAmount, "7i:10i"},
		// Стабильная сортировка: равные пороги в исходном порядке; int и float сравниваются как числа.
		{"BR-040/стабильность", `[{"amount":"5","percent":1},{"amount":5.001,"percent":2},{"amount":5,"percent":3},{"amount":"4.99","percent":4}]`, ThresholdAmount, "4.99f:4i; 5i:1i; 5i:2i; 5i:3i"},
		// Объект вместо списка — PHP array, порядок ключей сохраняется.
		{"BR-040/объект", `{"b":{"amount":3,"percent":1},"a":{"amount":1,"percent":2}}`, ThresholdAmount, "1i:2i; 3i:1i"},
		{"BR-040/не_массив", `"[]"`, ThresholdAmount, ""},
		{"edge/1e400_INF_в_int_0", `[{"amount":1e400,"percent":1}]`, ThresholdAmount, "0i:1i"},
		{"edge/большое_целое_во_float", `[{"amount":99999999999999999999,"percent":1}]`, ThresholdAmount, "7766279631452241920i:1i"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := levelsString(Normalize(mustJSON(t, tt.json), tt.threshold))
			if got != tt.want {
				t.Fatalf("Normalize(%s) = %q, want %q", tt.json, got, tt.want)
			}
		})
	}
}

// TestLevelsLogFormat — GS-027: журнал settings.updated «10000: 5.5%; 50000: 10%» и JSON сохранённых уровней.
func TestLevelsLogFormat(t *testing.T) {
	levels := Normalize(mustJSON(t, `[{"spent":"50000","percent":"10"},{"spent":"10000","percent":"5.5"}]`), ThresholdSpent)
	parts := make([]string, len(levels))
	for i, l := range levels {
		parts[i] = l.Threshold.String() + ": " + l.Percent.String() + "%"
	}
	if got := strings.Join(parts, "; "); got != "10000: 5.5%; 50000: 10%" {
		t.Fatalf("GS-027 журнал: %q", got)
	}
	b, err := json.Marshal(map[string]any{"spent": levels[0].Threshold, "percent": levels[0].Percent})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"percent":5.5,"spent":10000}` {
		t.Fatalf("GS-027 JSON: %s", b)
	}
	for f, want := range map[float64]string{1000.56: "1000.56", 1e25: "1.0e+25", 1e-7: "1.0e-7", 0.0001: "0.0001", 1.2345678901234568e17: "1.2345678901234568e+17", 1e15: "1000000000000000", -2.5: "-2.5"} {
		if got := phpJSONFloat(f); got != want {
			t.Errorf("phpJSONFloat(%v) = %q, want %q", f, got, want)
		}
	}
}

// TestLoyalty — BR-011, BR-042 и сценарии GS/GAP лояльности.
func TestLoyalty(t *testing.T) {
	gs034 := []Level{lv(1500, 10), lv(3000, 20)}
	discount := []struct {
		name     string
		levels   []Level
		personal float64
		spent    string
		want     float64
		wantCum  bool
	}{
		// BR-011: персональная 5 %, [{10000 → 7}], траты 12000.00 → 7 %.
		{"BR-011", []Level{lv(10000, 7)}, 5, "12000.00", 7, true},
		// GS-033: программа выключена (уровни в силе пусты) → только личная 12.5 %.
		{"GS-033/лояльность_выключена", nil, 12.5, "100000.00", 12.5, false},
		// GS-034: личная 5 %, {1500 → 10}, {3000 → 20}.
		{"GS-034/шаг1_0", gs034, 5, "0.00", 5, false},
		{"GS-034/шаг2_950", gs034, 5, "950.00", 5, false},
		{"GS-034/шаг3_1550_уровень_10", gs034, 5, "1550.00", 10, true},
		{"GS-034/шаг5_2450", gs034, 5, "2450.00", 10, true},
		// GAP-012: {1500 → 10}, {3000 → 5}, личная 0: 1499.99 → 0; 1500.00 → 10; 3000.00 → 10 (максимум достигнутых).
		{"GAP-012/1499.99", []Level{lv(1500, 10), lv(3000, 5)}, 0, "1499.99", 0, false},
		{"GAP-012/1500.00_порог_включительно", []Level{lv(1500, 10), lv(3000, 5)}, 0, "1500.00", 10, true},
		{"GAP-012/3000.00_максимум_достигнутых", []Level{lv(1500, 10), lv(3000, 5)}, 0, "3000.00", 10, true},
		// Равные — флаг «накопительная» не ставится (строго больше).
		{"BR-011/равные", []Level{lv(100, 7.5)}, 7.5, "100.00", 7.5, false},
	}
	for _, tt := range discount {
		t.Run(tt.name, func(t *testing.T) {
			spent := money.Tiyn(cents(t, tt.spent))
			if got := LoyaltyDiscountFor(tt.levels, tt.personal, spent); got != tt.want {
				t.Fatalf("LoyaltyDiscountFor = %v, want %v", got, tt.want)
			}
			if got := DiscountIsCumulative(tt.levels, tt.personal, spent); got != tt.wantCum {
				t.Fatalf("DiscountIsCumulative = %v, want %v", got, tt.wantCum)
			}
		})
	}
	// GS-034 в цене: discounted(100000, bp) — скидка 10 % → 900.00, 5 % → 950.00 (BR-012, формула пакета цен).
	// GAP-012: тариф 1000 → 900.00. Проверяем только процент → bp, саму цену считает пакет тарифов.
	if bp := money.BasisPoints(LoyaltyDiscountFor(gs034, 5, 155000)); bp != 1000 {
		t.Fatalf("GS-034/bp = %d", bp)
	}
	if bp := money.BasisPoints(LoyaltyDiscountFor(nil, 12.5, 10000000)); bp != 1250 {
		t.Fatalf("GS-033/bp = %d", bp)
	}
}

// TestSpending — BR-042: траты растут на оплату с основного и уменьшаются на возврат, не ниже нуля.
func TestSpending(t *testing.T) {
	type step struct {
		add   bool
		cents money.Tiyn
		want  money.Tiyn
	}
	tests := []struct {
		name  string
		start money.Tiyn
		steps []step
	}{
		// BR-042: тариф 900.00 основным (+200 бонусами не считаются) → +900.00; возврат 150.00 → −150.00.
		{"BR-042", 0, []step{{true, 90000, 90000}, {false, 15000, 75000}}},
		// GS-034: 950.00 → +600.00 (поминутно) → +900.00 = 2450.00.
		{"GS-034", 0, []step{{true, 95000, 95000}, {true, 60000, 155000}, {true, 90000, 245000}}},
		// GS-035: смешанная оплата — в траты идёт только основная часть 400.00.
		{"GS-035", 0, []step{{true, 40000, 40000}}},
		// GS-036: заказ 700.00 → отмена → 0.00.
		{"GS-036", 0, []step{{true, 70000, 70000}, {false, 70000, 0}}},
		// GAP-002: покупка 600.00 основным → завершение через 30 мин с возвратом 300.00 → 300.00.
		{"GAP-002", 0, []step{{true, 60000, 60000}, {false, 30000, 30000}}},
		// GAP-011: цена 0 (скидка 100 %) — траты не меняются.
		{"GAP-011", 12345, []step{{true, 0, 12345}}},
		// GAP-013: 200.00 − 350.00 → 0.00 (не −150).
		{"GAP-013", 20000, []step{{false, 35000, 0}}},
		// Неположительные суммы игнорируются (if ($cents <= 0) return).
		{"BR-042/неположительные", 500, []step{{true, -100, 500}, {false, -100, 500}, {false, 0, 500}}},
		{"BR-042/ровно_до_нуля", 500, []step{{false, 500, 0}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := tt.start
			for i, s := range tt.steps {
				if s.add {
					var err error
					if total, err = AddSpending(total, s.cents); err != nil {
						t.Fatal(err)
					}
				} else {
					total = SubtractSpending(total, s.cents)
				}
				if total != s.want {
					t.Fatalf("шаг %d: total_spent = %d, want %d", i+1, total, s.want)
				}
			}
		})
	}
	if _, err := AddSpending(money.MaxAmount, 1); err == nil {
		t.Fatal("AddSpending выше MaxAmount: нет ошибки")
	}
	// Решение SV-7 (docs/DECISIONS.md): AddSpending ограничен только money.MaxAmount; предел decimal(12,2)
	// (999 999 999 999 тиынов) проверяет billing/service MaxTotalSpent, а не чистая арифметика.
	const decimal12x2 money.Tiyn = 999_999_999_999
	if got, err := AddSpending(decimal12x2, 1); err != nil || got != decimal12x2+1 {
		t.Fatalf("AddSpending за пределом decimal(12,2) = %d, %v; want %d, nil", got, err, decimal12x2+1)
	}
	if got, err := AddSpending(money.MaxAmount-1, 1); err != nil || got != money.MaxAmount {
		t.Fatalf("AddSpending до MaxAmount = %d, %v", got, err)
	}
}

// TestParseSettings — чтение finances.* из clubs.settings (BR-003, Q-003): умолчания, (bool), data_get.
func TestParseSettings(t *testing.T) {
	tests := []struct {
		name                 string
		json                 string
		wantTopUp, wantLoyal bool
		wantTopUpLevels      string // уровни в силе
		wantLoyaltyLevels    string
		wantRawTopUp         string // нормализованные уровни независимо от флага
	}{
		{"BR-003/пусто", ``, false, false, "", "", ""},
		{"BR-003/null", `null`, false, false, "", "", ""},
		{"BR-003/битый_JSON", `{"finances":`, false, false, "", "", ""},
		{"BR-003/нет_finances", `{"club":{"booking_interval":30}}`, false, false, "", "", ""},
		{"BR-003/finances_не_массив", `{"finances":"x"}`, false, false, "", "", ""},
		{"GS-002/включено", `{"finances":{"topup_bonus_enabled":true,"topup_bonus_levels":[{"amount":10000,"percent":10},{"amount":2000,"percent":5}]}}`, true, false, "2000i:5i; 10000i:10i", "", "2000i:5i; 10000i:10i"},
		{"GS-003/выключено", `{"finances":{"topup_bonus_enabled":false,"topup_bonus_levels":[{"amount":100,"percent":50}]}}`, false, false, "", "", "100i:50i"},
		{"GS-033/лояльность_выключена", `{"finances":{"loyalty_enabled":false,"loyalty_levels":[{"spent":1000,"percent":50}]}}`, false, false, "", "", ""},
		{"GS-034/лояльность", `{"finances":{"loyalty_enabled":"1","loyalty_levels":[{"spent":3000,"percent":20},{"spent":1500,"percent":10}]}}`, false, true, "", "1500i:10i; 3000i:20i", ""},
		// (bool) PHP: "0", "", 0, [] — false; "false", "no", 0.1 — true.
		{"BR-003/bool_строка_0", `{"finances":{"topup_bonus_enabled":"0","loyalty_enabled":""}}`, false, false, "", "", ""},
		{"BR-003/bool_строка_false_это_true", `{"finances":{"topup_bonus_enabled":"false","loyalty_enabled":0.1}}`, true, true, "", "", ""},
		{"BR-003/bool_пустой_массив", `{"finances":{"topup_bonus_enabled":[],"loyalty_enabled":[0]}}`, false, true, "", "", ""},
		{"BR-003/уровни_null", `{"finances":{"topup_bonus_enabled":true,"topup_bonus_levels":null}}`, true, false, "", "", ""},
		// Больше 20 уровней на чтении не обрезаются (ограничение только при сохранении).
		{"BR-003/21_уровень_на_чтении", `{"finances":{"topup_bonus_enabled":1,"topup_bonus_levels":[` + strings.Repeat(`{"amount":1,"percent":1},`, 20) + `{"amount":2,"percent":2}]}}`, true, false, strings.Repeat("1i:1i; ", 20) + "2i:2i", "", strings.Repeat("1i:1i; ", 20) + "2i:2i"},
		// Повтор ключа в JSON: последний выигрывает (json_decode).
		{"BR-003/повтор_ключа", `{"finances":{"topup_bonus_enabled":false,"topup_bonus_enabled":true,"topup_bonus_levels":[{"amount":1,"percent":2}]}}`, true, false, "1i:2i", "", "1i:2i"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseSettings([]byte(tt.json))
			if err != nil {
				t.Fatal(err)
			}
			if s.TopUpBonusEnabled != tt.wantTopUp || s.LoyaltyEnabled != tt.wantLoyal {
				t.Fatalf("enabled = %v/%v, want %v/%v", s.TopUpBonusEnabled, s.LoyaltyEnabled, tt.wantTopUp, tt.wantLoyal)
			}
			if got := levelsString(s.TopUpLevelsInForce()); got != tt.wantTopUpLevels {
				t.Fatalf("TopUpLevelsInForce = %q, want %q", got, tt.wantTopUpLevels)
			}
			if got := levelsString(s.LoyaltyLevelsInForce()); got != tt.wantLoyaltyLevels {
				t.Fatalf("LoyaltyLevelsInForce = %q, want %q", got, tt.wantLoyaltyLevels)
			}
			if got := levelsString(s.TopUpBonusLevels); got != tt.wantRawTopUp {
				t.Fatalf("TopUpBonusLevels = %q, want %q", got, tt.wantRawTopUp)
			}
		})
	}
	if _, err := ParseSettings([]byte(`5`)); !errors.Is(err, ErrSettingsNotArray) {
		t.Fatalf("settings = 5: err = %v", err)
	}
	// GS-002 через настройки: 10000.55 → 1000.06.
	s, _ := ParseSettings([]byte(`{"finances":{"topup_bonus_enabled":true,"topup_bonus_levels":[{"amount":"2000","percent":"5"},{"amount":"10000","percent":"10"}]}}`))
	if b, _ := TopUpForTengeString(s.TopUpLevelsInForce(), "10000.55"); b.Cents != 100006 || b.Percent != 10 {
		t.Fatalf("GS-002 через настройки: %+v", b)
	}
}

// TestValidateLevels — SaveSettingsRequest для уровней (GS-001, GS-027…GS-032, GAP-021).
func TestValidateLevels(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		json     string
		wantKeys string // ключи ошибок через запятую
		wantKept int    // уровней после prepareForValidation()
	}{
		{"GS-001/без_ошибок", FieldTopUpBonusLevels, `[{"amount":"10000","percent":"10"},{"amount":"","percent":""},{"amount":"2000","percent":"5"}]`, "", 2},
		{"GS-001/процент_150", FieldTopUpBonusLevels, `[{"amount":"1000","percent":"150"}]`, "topup_bonus_levels.0.percent", 1},
		{"GS-027/без_ошибок", FieldLoyaltyLevels, `[{"spent":"50000","percent":"10"},{"spent":"","percent":""},{"spent":"10000","percent":"5.5"}]`, "", 2},
		{"GS-027/пустая_строка_nullable", FieldLoyaltyLevels, `""`, "", 0},
		{"GS-028/percent_over_100", FieldLoyaltyLevels, `[{"spent":"1000","percent":"101"}]`, "loyalty_levels.0.percent", 1},
		{"GS-029/no_percent", FieldLoyaltyLevels, `[{"spent":"1000","percent":""}]`, "loyalty_levels.0.percent", 1},
		{"GS-030/zero_spent", FieldLoyaltyLevels, `[{"spent":"0","percent":"5"}]`, "loyalty_levels.0.spent", 1},
		{"GS-031/same_spent_twice", FieldLoyaltyLevels, `[{"spent":"1000","percent":"5"},{"spent":"1000","percent":"7"}]`, "loyalty_levels.0.spent,loyalty_levels.1.spent", 2},
		{"GS-031/same_spent_loose_1000.0", FieldLoyaltyLevels, `[{"spent":"1000","percent":"5"},{"spent":"1000.0","percent":"7"}]`, "loyalty_levels.0.spent,loyalty_levels.1.spent", 2},
		{"GS-032/too_many", FieldLoyaltyLevels, levelsJSON("spent", 21), "loyalty_levels", 21},
		{"GAP-021/21_уровень_пополнения", FieldTopUpBonusLevels, levelsJSON("amount", 21), "topup_bonus_levels", 21},
		{"GAP-021/1000.555_проходит", FieldTopUpBonusLevels, `[{"amount":"1000.555","percent":"10"}]`, "", 1},
		{"BR-003/20_уровней_можно", FieldTopUpBonusLevels, levelsJSON("amount", 20), "", 20},
		{"BR-003/лишний_ключ", FieldTopUpBonusLevels, `[{"amount":"1","percent":"1","x":1}]`, "topup_bonus_levels.0", 1},
		{"BR-003/max_порога", FieldTopUpBonusLevels, `[{"amount":"999999999.01","percent":"0.01"}]`, "topup_bonus_levels.0.amount", 1},
		{"BR-003/min_процента", FieldTopUpBonusLevels, `[{"amount":"999999999","percent":"0.009"}]`, "topup_bonus_levels.0.percent", 1},
		{"BR-003/нечисло", FieldTopUpBonusLevels, `[{"amount":"1 000","percent":"5"}]`, "topup_bonus_levels.0.amount", 1},
		{"BR-003/не_массив", FieldTopUpBonusLevels, `"abc"`, "topup_bonus_levels", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, errs := ValidateLevels(mustJSON(t, tt.json), tt.field)
			keys := make([]string, len(errs))
			for i, e := range errs {
				keys[i] = e.Key
			}
			if got := strings.Join(keys, ","); got != tt.wantKeys {
				t.Fatalf("ошибки = %q (%+v), want %q", got, errs, tt.wantKeys)
			}
			if kept.Len() != tt.wantKept {
				t.Fatalf("уровней после фильтра = %d, want %d", kept.Len(), tt.wantKept)
			}
		})
	}
}

// levelsJSON — n уровней с разными порогами 1…n и процентом 1 (как 21 уровень в GS-032).
func levelsJSON(threshold string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"` + threshold + `":"` + strconv.Itoa(i+1) + `","percent":"1"}`
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// TestDecodeJSON — json_decode($s, true): типы и порядок ключей как в PHP.
func TestDecodeJSON(t *testing.T) {
	v := mustJSON(t, `{"a":1,"0":2,"a":3,"x":1e400,"y":99999999999999999999,"z":-0,"f":1.0}`)
	if got := strings.Join(v.Keys(), ","); got != "a,0,x,y,z,f" {
		t.Fatalf("ключи: %s", got)
	}
	checks := []struct {
		key  string
		kind Kind
		f    float64
	}{{"a", KindInt, 3}, {"0", KindInt, 2}, {"x", KindFloat, math.Inf(1)}, {"y", KindFloat, 1e20}, {"z", KindInt, 0}, {"f", KindFloat, 1}}
	for _, c := range checks {
		if g := v.Get(c.key); g.Kind() != c.kind || g.ToFloat() != c.f {
			t.Errorf("%s: kind %d value %v", c.key, g.Kind(), g.ToFloat())
		}
	}
	for _, bad := range []string{``, `{`, `[1,]`, `{} {}`, `nul`} {
		if _, err := DecodeJSON([]byte(bad)); err == nil {
			t.Errorf("DecodeJSON(%q): нет ошибки", bad)
		}
	}
}
