package settings

// Сохранение секции настроек: SaveSettingsRequest (правила и сообщения) + SettingsController::save().

import (
	"bytes"
	"encoding/json"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// input — поля запроса после TrimStrings + ConvertEmptyStringsToNull (только верхний уровень; уровни
// обрабатывает bonus.ValidateLevels).
type input map[string]json.RawMessage

func readInput(body map[string]any) (input, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var in input
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	for k, v := range in {
		if jsonKind(v) == 's' {
			s := strings.TrimFunc(stringOf(v), func(r rune) bool {
				return unicode.IsSpace(r) || r == 0xFEFF || r == 0x200B || r == 0x200E
			})
			if s == "" {
				in[k] = raw("null")
			} else {
				in[k], _ = json.Marshal(s)
			}
		}
	}
	return in, nil
}

// has — $request->has($field): ключ есть в запросе (в том числе со значением null).
func (in input) has(field string) bool {
	_, ok := in[field]
	return ok
}

func (in input) get(field string) json.RawMessage {
	if v, ok := in[field]; ok {
		return v
	}
	return raw("null")
}

// numberRule — numeric|min:lo|max:hi (Laravel: числовой размер).
type numberRule struct {
	required bool // required_if:section,<секция>
	integer  bool // integer вместо numeric
	min, max string
}

// rules — SaveSettingsRequest::rules() (кроме уровней, enum и булевых).
var rules = map[string]numberRule{
	"session_idle_timeout":                       {required: true, min: "0", max: "10080"},
	"pc_restart_after_session_end":               {required: true, min: "0", max: "10080"},
	"session_auto_terminate_when_pc_unavailable": {required: true, min: "0", max: "10080"},
	"booking_interval":                           {required: true, min: "0", max: "10080"},
	"max_cashiers_on_shift":                      {integer: true, min: "0", max: "100"},
	"max_operators_on_shift":                     {integer: true, min: "0", max: "100"},
	"agent_idle_logout_minutes":                  {min: "0", max: "1440"},
	"bonus_max_percent":                          {integer: true, min: "0", max: "100"},
	"auto_billing_pause_minutes":                 {integer: true, min: "0", max: "60"},
}

// enums — Rule::in(...).
var enums = map[string][]string{
	"pc_action_after_session_end": {"none", "restart", "shutdown"},
	"currency":                    {"KZT", "RUB", "$"},
	"discount_scope":              {"tariffs", "hourly", "all"}, // PaymentRules::DISCOUNT_SCOPES
}

// validate — ошибки полей секции (все сразу, как FormRequest). Возвращает уровни после prepareForValidation().
func validate(section string, in input) (map[string]bonus.Value, *httpx.FormErrors) {
	fe := &httpx.FormErrors{}
	levels := map[string]bonus.Value{}
	for _, field := range sectionFields[section] {
		v := in.get(field)
		present := in.has(field)
		if r, ok := rules[field]; ok {
			validateNumber(fe, field, v, present, r)
			continue
		}
		if allowed, ok := enums[field]; ok {
			// currency: required_if:section,finances; остальные — sometimes.
			if field == "currency" && jsonKind(v) == 'n' {
				fe.Add(field, httpx.RuleMessage(field, cash.RuleRequired, ""))
				continue
			}
			if present && jsonKind(v) != 'n' && !slices.Contains(allowed, phpString(v)) || present && jsonKind(v) == 'a' {
				fe.Add(field, httpx.RuleMessage(field, cash.RuleIn, ""))
			}
			continue
		}
		if threshold, ok := levelFields[field]; ok {
			if !present {
				continue
			}
			lv, err := bonus.DecodeJSON(v)
			if err != nil {
				lv = bonus.Null()
			}
			kept, errs := bonus.ValidateLevels(lv, field)
			levels[field] = kept
			addLevelErrors(fe, field, threshold, kept, errs)
			continue
		}
		if slices.Contains(booleanFields, field) && present && !isBooleanInput(v) {
			fe.Add(field, httpx.RuleMessage(field, "boolean", ""))
		}
	}
	return levels, fe
}

// validateNumber — required_if / sometimes, numeric | integer, min, max. Пустое значение необязательного поля
// не проверяется (правило не implicit — Validator::isValidatable()).
func validateNumber(fe *httpx.FormErrors, field string, v json.RawMessage, present bool, r numberRule) {
	if jsonKind(v) == 'n' || (jsonKind(v) == 'a' && !truthy(v)) {
		if r.required {
			fe.Add(field, httpx.RuleMessage(field, cash.RuleRequired, ""))
		}
		return
	}
	if !present {
		return
	}
	if r.integer {
		if !isIntegerInput(v) {
			fe.Add(field, httpx.RuleMessage(field, cash.RuleInteger, ""))
		}
	} else if !isNumeric(v) {
		fe.Add(field, httpx.RuleMessage(field, cash.RuleNumeric, ""))
	}
	if !isNumeric(v) {
		return
	}
	n := exactNumber(v)
	lo, _ := new(big.Rat).SetString(r.min)
	hi, _ := new(big.Rat).SetString(r.max)
	if n.Cmp(lo) < 0 {
		fe.Add(field, httpx.RuleMessage(field, cash.RuleMin, r.min))
	}
	if n.Cmp(hi) > 0 {
		fe.Add(field, httpx.RuleMessage(field, cash.RuleMax, r.max))
	}
}

// exactNumber — точное значение числа (Laravel ≥ 10 сравнивает min/max через BigNumber).
func exactNumber(v json.RawMessage) *big.Rat {
	s := strings.TrimSpace(string(bytes.TrimSpace(v)))
	if jsonKind(v) == 's' {
		s = strings.TrimSpace(stringOf(v))
	}
	if r, ok := new(big.Rat).SetString(s); ok {
		return r
	}
	return new(big.Rat).SetFloat64(toFloat(v))
}

// isIntegerInput — правило integer: filter_var($v, FILTER_VALIDATE_INT) !== false.
func isIntegerInput(v json.RawMessage) bool {
	switch jsonKind(v) {
	case 'd':
		r := exactNumber(v)
		return r.IsInt()
	case 's':
		s := strings.TrimSpace(stringOf(v))
		if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
			s = s[1:]
		}
		if s == "" || (len(s) > 1 && s[0] == '0') {
			return false
		}
		_, err := strconv.ParseInt(s, 10, 64)
		return err == nil
	case 'b':
		return truthy(v) // (string) true = "1"
	}
	return false
}

// isBooleanInput — правило boolean: true, false, 0, 1, "0", "1".
func isBooleanInput(v json.RawMessage) bool {
	switch jsonKind(v) {
	case 'n', 'b':
		return true
	case 'd':
		t := string(bytes.TrimSpace(v))
		return t == "0" || t == "1"
	case 's':
		s := stringOf(v)
		return s == "0" || s == "1"
	}
	return false
}

// boolInput — $request->boolean(): filter_var($v, FILTER_VALIDATE_BOOLEAN) ("1", "true", "on", "yes" — да).
func boolInput(v json.RawMessage) bool {
	switch jsonKind(v) {
	case 'b':
		return truthy(v)
	case 'd':
		return toFloat(v) == 1
	case 's':
		switch strings.ToLower(strings.TrimSpace(stringOf(v))) {
		case "1", "true", "on", "yes":
			return true
		}
	}
	return false
}

// Сообщения SaveSettingsRequest::messages() (ключи перевода).
var levelMessages = map[string]struct{ tooMany, thrRequired, thrDistinct, thrOther, pctRequired, pctOther string }{
	bonus.FieldLoyaltyLevels: {
		tooMany:     "Уровней скидки — не больше :max.",
		thrRequired: "Укажите сумму трат для каждого уровня.",
		thrDistinct: "Суммы трат у уровней не должны повторяться.",
		thrOther:    "Сумма трат — число больше нуля.",
		pctRequired: "Укажите скидку для каждого уровня.",
		pctOther:    "Скидка — от 0,01 до 100%.",
	},
	bonus.FieldTopUpBonusLevels: {
		tooMany:     "Уровней бонуса — не больше :max.",
		thrRequired: "Укажите сумму пополнения для каждого уровня.",
		thrDistinct: "Суммы пополнения у уровней не должны повторяться.",
		thrOther:    "Сумма пополнения — число больше нуля.",
		pctRequired: "Укажите бонус для каждого уровня.",
		pctOther:    "Бонус — от 0,01 до 100%.",
	},
}

// addLevelErrors — ошибки bonus.ValidateLevels с текстами messages() (первый подходящий ключ, шаблоны — в конце)
// плюс решение Q-002: порог — сумма в тенге, больше 2 знаков после точки — ошибка decimal.
func addLevelErrors(fe *httpx.FormErrors, field, threshold string, kept bonus.Value, errs []bonus.FieldError) {
	msg := levelMessages[field]
	for _, e := range errs {
		rs := e.Rules
		if slices.Contains(rs, "required") {
			rs = []string{"required"} // implicit-правило не прошло — остальные Laravel не проверяет
		}
		switch {
		case e.Key == field:
			for _, r := range rs {
				if r == "max" {
					fe.AddMsg(field, msg.tooMany, map[string]string{"max": strconv.Itoa(bonus.MaxLevels)})
				} else {
					fe.Add(field, httpx.RuleMessage(field, r, ""))
				}
			}
		case strings.HasSuffix(e.Key, "."+threshold):
			for _, r := range rs {
				switch r {
				case "required":
					fe.Add(e.Key, msg.thrRequired)
				case "distinct":
					fe.Add(e.Key, msg.thrDistinct)
				default:
					fe.Add(e.Key, msg.thrOther)
				}
			}
		case strings.HasSuffix(e.Key, ".percent"):
			for _, r := range rs {
				if r == "required" {
					fe.Add(e.Key, msg.pctRequired)
				} else {
					fe.Add(e.Key, msg.pctOther)
				}
			}
		default: // F.N: array:T,percent
			for _, r := range rs {
				fe.Add(e.Key, httpx.RuleMessage(e.Key, r, ""))
			}
		}
	}
	// Решение Q-002 (docs/DECISIONS.md): сумма с > 2 знаками после точки отклоняется, а не округляется.
	for i, level := range kept.Items() {
		key := field + "." + strconv.Itoa(i) + "." + threshold
		if fe.Has(key) {
			continue
		}
		if t := level.Get(threshold); t.IsNumeric() && !twoDecimals(t) {
			fe.Add(key, httpx.RuleMessage(key, cash.RuleDecimal, ""))
		}
	}
}

// twoDecimals — число (или числовая строка) не больше чем с 2 знаками после точки.
func twoDecimals(v bonus.Value) bool {
	var s string
	switch v.Kind() {
	case bonus.KindInt:
		return true
	case bonus.KindFloat:
		s = strconv.FormatFloat(v.ToFloat(), 'f', -1, 64)
	default:
		b := []byte(levelString(v))
		s = strings.TrimSpace(string(b))
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return true // экспонента и т. п. — проверяется как число
	}
	return new(big.Rat).Mul(r, big.NewRat(100, 1)).IsInt()
}

// levelString — строка значения уровня (числовые строки: bonus.Value не отдаёт текст — восстанавливаем через
// точное значение float с 17 знаками; для решения «> 2 знаков» этого достаточно).
func levelString(v bonus.Value) string {
	return strconv.FormatFloat(v.ToFloat(), 'f', -1, 64)
}
