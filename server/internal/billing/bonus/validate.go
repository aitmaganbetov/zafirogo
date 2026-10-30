package bonus

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Поля формы уровней в Settings → Finances.
const (
	FieldTopUpBonusLevels = "topup_bonus_levels" // порог — amount
	FieldLoyaltyLevels    = "loyalty_levels"     // порог — spent
)

// FieldError — ошибка валидации в стиле Laravel: Key — путь поля («loyalty_levels.0.percent»),
// Rules — правила, которые не прошли (по порядку: required, numeric, min, max, distinct, array, max_count).
type FieldError struct {
	Key   string
	Rules []string
}

// ValidateLevels — проверка уровней при сохранении настроек финансов, как SaveSettingsRequest
// (BR-003, BR-040, BR-041; GS-001, GS-028…GS-032):
//
//	'F'           => ['nullable', 'array', 'max:20'],
//	'F.*'         => ['array:T,percent'],
//	'F.*.T'       => ['required', 'numeric', 'min:0.01', 'max:999999999', 'distinct'],
//	'F.*.percent' => ['required', 'numeric', 'min:0.01', 'max:100'],
//
// плюс prepareForValidation(): строки, где и порог, и процент null, отбрасываются до проверки.
// input — значение поля из запроса (null — поля нет или пустая строка: nullable, ошибок нет).
// Строки предварительно обрабатываются как глобальные middleware Laravel TrimStrings и
// ConvertEmptyStringsToNull (форма присылает "" для пустых ячеек).
// Возвращает отфильтрованные уровни (то, что затем уходит в Normalize) и ошибки в порядке Laravel.
//
// Laravel: app/Http/Requests/SaveSettingsRequest.php rules() (~стр. 32–47), prepareForValidation() (~стр. 79–88).
func ValidateLevels(input Value, field string) (Value, []FieldError) {
	threshold := ThresholdAmount
	if field == FieldLoyaltyLevels {
		threshold = ThresholdSpent
	}
	input = trimAndNullStrings(input)
	if input.Kind() == KindNull { // nullable
		return input, nil
	}
	if !input.IsArray() {
		return input, []FieldError{{Key: field, Rules: []string{"array"}}}
	}
	// prepareForValidation(): array_values(array_filter($levels, fn ($l) => ! is_array($l) || $l[T] !== null || $l['percent'] !== null))
	kept := make([]Value, 0, input.Len())
	for _, level := range input.Items() {
		if !level.IsArray() || level.Get(threshold).Kind() != KindNull || level.Get("percent").Kind() != KindNull {
			kept = append(kept, level)
		}
	}
	levels := List(kept...)

	var errs []FieldError
	if len(kept) > MaxLevels {
		errs = append(errs, FieldError{Key: field, Rules: []string{"max"}})
	}
	for i, level := range kept {
		if !level.IsArray() || !onlyKeys(level, threshold, "percent") {
			errs = append(errs, FieldError{Key: field + "." + strconv.Itoa(i), Rules: []string{"array"}})
		}
	}
	thresholds := make([]Value, len(kept))
	for i, level := range kept {
		thresholds[i] = level.Get(threshold)
	}
	for i, v := range thresholds {
		rules := numberRules(v, "999999999")
		if distinctFails(thresholds, i) {
			rules = append(rules, "distinct")
		}
		if len(rules) > 0 {
			errs = append(errs, FieldError{Key: field + "." + strconv.Itoa(i) + "." + threshold, Rules: rules})
		}
	}
	for i, level := range kept {
		if rules := numberRules(level.Get("percent"), "100"); len(rules) > 0 {
			errs = append(errs, FieldError{Key: field + "." + strconv.Itoa(i) + ".percent", Rules: rules})
		}
	}
	return levels, errs
}

// numberRules — required, numeric, min:0.01, max:<maxValue> для одного значения.
func numberRules(v Value, maxValue string) []string {
	var failed []string
	if v.Kind() == KindNull || (v.IsArray() && v.Len() == 0) {
		failed = append(failed, "required")
	}
	if !v.IsNumeric() {
		// numeric не прошёл — min/max для нечисла Laravel сравнивает с длиной строки; суть ошибки та же.
		return append(failed, "numeric")
	}
	size := numericRat(v)
	if size.Cmp(big.NewRat(1, 100)) < 0 {
		failed = append(failed, "min")
	}
	limit, _ := new(big.Rat).SetString(maxValue)
	if size.Cmp(limit) > 0 {
		failed = append(failed, "max")
	}
	return failed
}

// numericRat — точное значение числа для min/max (Laravel ≥ 10 сравнивает через brick/math BigNumber).
func numericRat(v Value) *big.Rat {
	switch v.Kind() {
	case KindInt:
		return new(big.Rat).SetInt64(v.i)
	case KindFloat:
		if math.IsInf(v.f, 0) || math.IsNaN(v.f) {
			return new(big.Rat).SetFloat64(math.Copysign(math.MaxFloat64, v.f))
		}
		return new(big.Rat).SetFloat64(v.f)
	}
	s := strings.Trim(v.s, " \t\n\r\v\f")
	if r, ok := new(big.Rat).SetString(s); ok {
		return r
	}
	f := money.PHPStringToFloat(s) // "5." и т.п.
	if math.IsInf(f, 0) {
		f = math.Copysign(math.MaxFloat64, f)
	}
	return new(big.Rat).SetFloat64(f)
}

// distinctFails — правило distinct: in_array($value, $другие, strict: false).
func distinctFails(values []Value, i int) bool {
	for j, other := range values {
		if j != i && phpLooseEqual(values[i], other) {
			return true
		}
	}
	return false
}

func onlyKeys(v Value, allowed ...string) bool {
	for _, k := range v.arr.keys {
		ok := false
		for _, a := range allowed {
			if k == phpKey(a) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// phpLooseEqual — == PHP 8 для скаляров (массивы считаются неравными).
func phpLooseEqual(a, b Value) bool {
	if a.IsArray() || b.IsArray() {
		return false
	}
	if a.Kind() == KindString && b.Kind() == KindString {
		if a.IsNumeric() && b.IsNumeric() {
			return numericEqual(a, b)
		}
		return a.s == b.s
	}
	if a.Kind() == KindNull && b.Kind() == KindString {
		return b.s == ""
	}
	if b.Kind() == KindNull && a.Kind() == KindString {
		return a.s == ""
	}
	if a.Kind() == KindNull || b.Kind() == KindNull || a.Kind() == KindBool || b.Kind() == KindBool {
		return a.Truthy() == b.Truthy()
	}
	// число с числом или числом-строкой
	if a.Kind() == KindString || b.Kind() == KindString {
		s, n := a, b
		if b.Kind() == KindString {
			s, n = b, a
		}
		if !s.IsNumeric() {
			return s.s == phpNumberString(n)
		}
	}
	return numericEqual(a, b)
}

func numericEqual(a, b Value) bool {
	ai, aInt := asInt(a)
	bi, bInt := asInt(b)
	if aInt && bInt {
		return ai == bi
	}
	return a.ToFloat() == b.ToFloat()
}

func asInt(v Value) (int64, bool) {
	switch v.Kind() {
	case KindInt:
		return v.i, true
	case KindString:
		s := strings.Trim(v.s, " \t\n\r\v\f")
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func phpNumberString(v Value) string {
	if v.Kind() == KindInt {
		return strconv.FormatInt(v.i, 10)
	}
	return money.PHPFloatString(v.f)
}

// trimAndNullStrings — TrimStrings + ConvertEmptyStringsToNull (рекурсивно по массивам).
func trimAndNullStrings(v Value) Value {
	switch v.Kind() {
	case KindString:
		s := strings.TrimFunc(v.s, func(r rune) bool {
			return unicode.IsSpace(r) || r == 0xFEFF || r == 0x200B || r == 0x200E
		})
		if s == "" {
			return Null()
		}
		return String(s)
	case KindArray:
		a := &phpArray{vals: make(map[string]Value, len(v.arr.keys))}
		for _, k := range v.arr.keys {
			a.set(k, trimAndNullStrings(v.arr.vals[k]))
		}
		return Value{kind: KindArray, arr: a}
	default:
		return v
	}
}
