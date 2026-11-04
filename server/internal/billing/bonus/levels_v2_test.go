package bonus

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Решение Q-003 (docs/DECISIONS.md): уровни v2 ({"v":2,"levels":[{threshold_tiyn, percent_bp}]}) читаются
// ParseSettings наравне с форматом старого кода.
func TestParseSettingsLevelsV2(t *testing.T) {
	js := `{"finances":{"topup_bonus_enabled":true,"loyalty_enabled":true,
		"topup_bonus_levels":{"v":2,"levels":[{"threshold_tiyn":1000000,"percent_bp":1000},{"threshold_tiyn":200000,"percent_bp":550},
			{"threshold_tiyn":"5","percent_bp":1},{"threshold_tiyn":7.5,"percent_bp":1},{"percent_bp":1}]},
		"loyalty_levels":{"v":2,"levels":[{"threshold_tiyn":150050,"percent_bp":1000}]}}}`
	s, err := ParseSettings([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	// Нецелые/строковые/пропущенные значения v2 пропускаются; порядок — по порогу.
	if got, want := levelsString(s.TopUpLevelsInForce()), "2000i:5.5f; 10000i:10i"; got != want {
		t.Fatalf("topup levels = %q, want %q", got, want)
	}
	if got, want := levelsString(s.LoyaltyLevelsInForce()), "1500.5f:10i"; got != want {
		t.Fatalf("loyalty levels = %q, want %q", got, want)
	}
	// GS-002 по уровням v2: 10000.55 → бонус 1000.06 (10 %).
	b, err := TopUpForCents(s.TopUpLevelsInForce(), 1000055)
	if err != nil || b.Cents != 100006 || b.Percent != 10 {
		t.Fatalf("bonus = %+v, %v", b, err)
	}
	// Объект без "v": 2 — формат старого кода (уровней нет), как читал бы Laravel.
	s, _ = ParseSettings([]byte(`{"finances":{"topup_bonus_enabled":true,"topup_bonus_levels":{"v":3,"levels":[{"threshold_tiyn":1,"percent_bp":1}]}}}`))
	if len(s.TopUpBonusLevels) != 0 {
		t.Fatalf("v3 levels = %q", levelsString(s.TopUpBonusLevels))
	}
}

// Решение Q-003: EncodeLevels пишет v2; уровни, прочитанные из v2, дают те же бонусы и скидки до тиына, что
// уровни старого формата, из которых они получены (числа не меняются).
func TestEncodeLevelsMatchesV1(t *testing.T) {
	raw, err := EncodeLevels([]Level{lv(2000, 5.5), lv(10000, 10)})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"v":2,"levels":[{"threshold_tiyn":200000,"percent_bp":550},{"threshold_tiyn":1000000,"percent_bp":1000}]}`; string(raw) != want {
		t.Fatalf("v2 = %s\nwant %s", raw, want)
	}

	rng := rand.New(rand.NewPCG(3, 2026))
	num := func() string {
		switch rng.IntN(4) {
		case 0:
			return strconv.Itoa(rng.IntN(100000))
		case 1:
			return strconv.Itoa(rng.IntN(100000)) + "." + strconv.Itoa(rng.IntN(100))
		case 2:
			return `"` + strconv.Itoa(rng.IntN(1000)) + "." + strconv.Itoa(rng.IntN(1000)) + `"` // 3 знака: normalize() округлит
		default:
			return strconv.FormatFloat(rng.Float64()*1e9, 'f', -1, 64)
		}
	}
	pct := func() string {
		return strconv.FormatFloat(float64(rng.IntN(10001))/100, 'f', -1, 64)
	}
	for i := range 2000 {
		var top, loyal []string
		for range rng.IntN(6) {
			top = append(top, `{"amount":`+num()+`,"percent":`+pct()+`}`)
			loyal = append(loyal, `{"spent":`+num()+`,"percent":`+pct()+`}`)
		}
		v1 := `{"finances":{"topup_bonus_enabled":true,"loyalty_enabled":true,"topup_bonus_levels":[` + strings.Join(top, ",") +
			`],"loyalty_levels":[` + strings.Join(loyal, ",") + `]}}`
		s1, err := ParseSettings([]byte(v1))
		if err != nil {
			t.Fatal(err)
		}
		topV2, err := EncodeLevels(s1.TopUpBonusLevels)
		if err != nil {
			t.Fatal(err)
		}
		loyalV2, err := EncodeLevels(s1.LoyaltyLevels)
		if err != nil {
			t.Fatal(err)
		}
		v2 := `{"finances":{"topup_bonus_enabled":true,"loyalty_enabled":true,"topup_bonus_levels":` + string(topV2) +
			`,"loyalty_levels":` + string(loyalV2) + `}}`
		s2, err := ParseSettings([]byte(v2))
		if err != nil {
			t.Fatal(err)
		}
		if a, b := levelsString(s1.TopUpBonusLevels), levelsString(s2.TopUpBonusLevels); a != b {
			t.Fatalf("#%d topup levels v1 %q, v2 %q\n%s", i, a, b, v1)
		}
		if a, b := levelsString(s1.LoyaltyLevels), levelsString(s2.LoyaltyLevels); a != b {
			t.Fatalf("#%d loyalty levels v1 %q, v2 %q\n%s", i, a, b, v1)
		}
		for range 50 {
			cents := rng.Int64N(1_000_000_000_00)
			b1, err1 := TopUpForCents(s1.TopUpLevelsInForce(), cents)
			b2, err2 := TopUpForCents(s2.TopUpLevelsInForce(), cents)
			if b1 != b2 || (err1 == nil) != (err2 == nil) {
				t.Fatalf("#%d bonus(%d): v1 %+v %v, v2 %+v %v\n%s", i, cents, b1, err1, b2, err2, v1)
			}
			if p1, p2 := LoyaltyPercentFor(s1.LoyaltyLevelsInForce(), money.Tiyn(cents)), LoyaltyPercentFor(s2.LoyaltyLevelsInForce(), money.Tiyn(cents)); p1 != p2 {
				t.Fatalf("#%d loyalty(%d): v1 %v, v2 %v", i, cents, p1, p2)
			}
		}
	}
}
