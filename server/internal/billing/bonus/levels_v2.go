package bonus

import (
	"encoding/json"
	"slices"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// LevelsVersion — версия формата уровней в clubs.settings, который пишет EncodeLevels.
//
// Решение Q-003 (docs/DECISIONS.md): новые записи настроек уровней — в тиынах и базисных пунктах с полем версии:
//
//	finances.topup_bonus_levels / finances.loyalty_levels =
//	  {"v": 2, "levels": [{"threshold_tiyn": 1000000, "percent_bp": 550}, …]}
//
// Формат старого кода (v1) — список [{"amount"|"spent": 10000, "percent": 5.5}] в тенге и процентах; его читает
// Normalize. Порог v2 — один ключ threshold_tiyn для обоих видов уровней (вид задаёт ключ настроек).
const LevelsVersion = 2

// levelV2 — уровень формата v2.
type levelV2 struct {
	ThresholdTiyn int64 `json:"threshold_tiyn"`
	PercentBP     int64 `json:"percent_bp"`
}

type levelsV2 struct {
	V      int       `json:"v"`
	Levels []levelV2 `json:"levels"`
}

// EncodeLevels — значение finances.topup_bonus_levels / finances.loyalty_levels в формате v2 (решение Q-003) для
// уровней после Normalize (ValidateLevels + Normalize при сохранении настроек). Порог — (int) round(порог·100)
// как в PercentLevels::percentAt(), процент — (int) round(процент·100) как в TopUpBonus::for() и discounted():
// расчёты по прочитанным обратно уровням дают те же тиыны, что по уровням v1.
func EncodeLevels(levels []Level) (json.RawMessage, error) {
	out := levelsV2{V: LevelsVersion, Levels: make([]levelV2, len(levels))}
	for i, l := range levels {
		out.Levels[i] = levelV2{ThresholdTiyn: thresholdCents(l.Threshold), PercentBP: money.BasisPoints(l.Percent.Float())}
	}
	return json.Marshal(out)
}

// isLevelsV2 — значение уровней в формате v2 (массив с ключом "v" = 2).
func isLevelsV2(v Value) bool {
	ver := v.Get("v")
	return v.IsArray() && ver.kind == KindInt && ver.i == LevelsVersion
}

// levelsFromV2 — уровни из формата v2 (решение Q-003). Элементы без целых threshold_tiyn и percent_bp пропускаются
// (как Normalize пропускает нечисловые уровни v1). Порог и процент переводятся в числа v1 (тенге и проценты:
// целое — int, иначе float — как normalize()), поэтому PercentAt и бонус считаются теми же формулами PHP.
func levelsFromV2(v Value) []Level {
	list := v.Get("levels")
	if !list.IsArray() {
		return []Level{}
	}
	out := make([]Level, 0, list.Len())
	for _, item := range list.Items() {
		t, p := item.Get("threshold_tiyn"), item.Get("percent_bp")
		if !item.IsArray() || t.kind != KindInt || p.kind != KindInt {
			continue
		}
		out = append(out, Level{Threshold: hundredths(t.i), Percent: hundredths(p.i)})
	}
	slices.SortStableFunc(out, func(a, b Level) int { return compareNumbers(a.Threshold, b.Threshold) })
	return out
}

// hundredths — n/100 числом normalize(): целое — int, иначе float (ближайший double к десятичной записи,
// как round((float) "12.34", 2)).
func hundredths(n int64) Number {
	if n%100 == 0 {
		return IntNumber(n / 100)
	}
	return FloatNumber(float64(n) / 100)
}

// levelsFrom — уровни из значения настроек: v2 (решение Q-003) или формат старого кода (Normalize).
func levelsFrom(v Value, threshold string) []Level {
	if isLevelsV2(v) {
		return levelsFromV2(v)
	}
	return Normalize(v, threshold)
}
