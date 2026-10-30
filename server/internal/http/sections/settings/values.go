package settings

// Модель clubs.settings экрана настроек: SettingsController::loadSettings() / defaultSettings() / minutes() /
// number() и запись JSON обратно (весь объект настроек, как `$club->settings = $settings` старого save()).

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Секции настроек (SaveSettingsRequest: section in club, finances, guests).
const (
	sectionClub     = "club"
	sectionFinances = "finances"
	sectionGuests   = "guests"
)

var sectionOrder = []string{sectionClub, sectionFinances, sectionGuests}

// sectionFields — поля секций в порядке SettingsController::save() ($fields).
var sectionFields = map[string][]string{
	sectionClub: {
		"session_idle_timeout", "pc_action_after_session_end", "pc_restart_after_session_end",
		"session_auto_terminate_when_pc_unavailable", "booking_interval", "max_cashiers_on_shift",
		"max_operators_on_shift", "agent_idle_logout_minutes", "agent_shop_enabled", "agent_auto_update",
	},
	sectionFinances: {
		"currency", "loyalty_enabled", "loyalty_levels", "topup_bonus_enabled", "topup_bonus_levels", "discount_scope",
		"bonus_max_percent", "allow_mixed_payment", "allow_whole_balance", "auto_billing", "auto_billing_pause_minutes",
	},
	sectionGuests: {
		"show_qr_code_on_login", "allow_guest_self_transfer", "allow_adding_friends",
		"allow_balance_transfer_to_friends", "guest_mode_enabled",
	},
}

// booleanFields — $booleanFields save(): $request->boolean().
var booleanFields = []string{
	"loyalty_enabled", "topup_bonus_enabled", "allow_mixed_payment", "allow_whole_balance", "auto_billing",
	"show_qr_code_on_login", "allow_guest_self_transfer", "allow_adding_friends", "allow_balance_transfer_to_friends",
	"agent_shop_enabled", "agent_auto_update", "guest_mode_enabled",
}

// minuteFields — $minuteFields save(): number((float) input).
var minuteFields = []string{
	"session_idle_timeout", "pc_restart_after_session_end", "session_auto_terminate_when_pc_unavailable",
	"booking_interval", "max_cashiers_on_shift", "max_operators_on_shift", "agent_idle_logout_minutes",
}

// levelFields — уровни (поле → ключ порога).
var levelFields = map[string]string{
	bonus.FieldLoyaltyLevels:    bonus.ThresholdSpent,
	bonus.FieldTopUpBonusLevels: bonus.ThresholdAmount,
}

// defaults — SettingsController::defaultSettings() (JSON-значения).
var defaults = map[string]map[string]json.RawMessage{
	sectionClub: {
		"session_idle_timeout":                       raw("15"),
		"pc_action_after_session_end":                raw(`"none"`),
		"pc_restart_after_session_end":               raw("0.5"),
		"session_auto_terminate_when_pc_unavailable": raw("5"),
		"booking_interval":                           raw("30"),
		"max_cashiers_on_shift":                      raw("0"),
		"max_operators_on_shift":                     raw("0"),
		"agent_idle_logout_minutes":                  raw("5"), // GuestFeatures::DEFAULT_IDLE_LOGOUT_MINUTES
		"agent_shop_enabled":                         raw("true"),
		"agent_auto_update":                          raw("true"),
	},
	sectionFinances: {
		"currency":                   raw(`"KZT"`),
		"loyalty_enabled":            raw("false"),
		"loyalty_levels":             raw("[]"),
		"topup_bonus_enabled":        raw("false"),
		"topup_bonus_levels":         raw("[]"),
		"discount_scope":             raw(`"tariffs"`),
		"bonus_max_percent":          raw("100"),
		"allow_mixed_payment":        raw("false"),
		"allow_whole_balance":        raw("false"),
		"auto_billing":               raw("false"),
		"auto_billing_pause_minutes": raw("5"),
	},
	sectionGuests: {
		"show_qr_code_on_login":             raw("false"),
		"allow_guest_self_transfer":         raw("false"),
		"allow_adding_friends":              raw("false"),
		"allow_balance_transfer_to_friends": raw("false"),
		"guest_mode_enabled":                raw("true"),
	},
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// clubSettings — clubs.settings: секция → ключ → JSON-значение (порядок ключей в jsonb не важен).
type clubSettings map[string]map[string]json.RawMessage

// loadSettings — loadSettings(): array_replace_recursive(defaultSettings(), сохранённые), затем «минутные» поля
// секции club (все, кроме pc_action_after_session_end, agent_shop_enabled, agent_auto_update) — minutes().
// Секции и ключи, которых нет в defaultSettings(), сохраняются (array_replace_recursive их не теряет).
func loadSettings(stored []byte) clubSettings {
	out := clubSettings{}
	var top map[string]json.RawMessage
	if len(bytes.TrimSpace(stored)) > 0 && json.Unmarshal(stored, &top) != nil {
		top = nil // не объект (в старом — TypeError): как пустые настройки
	}
	for name, sec := range top {
		var fields map[string]json.RawMessage
		if json.Unmarshal(sec, &fields) != nil || fields == nil {
			if !slices.Contains(sectionOrder, name) {
				out[name] = nil // чужое значение верхнего уровня сохраняем как есть (ниже — rawTop)
			}
			continue
		}
		out[name] = fields
	}
	for _, name := range sectionOrder {
		if out[name] == nil {
			out[name] = map[string]json.RawMessage{}
		}
		for k, v := range defaults[name] {
			if _, ok := out[name][k]; !ok {
				out[name][k] = v
			}
		}
	}
	for _, f := range sectionFields[sectionClub] {
		if f == "pc_action_after_session_end" || f == "agent_shop_enabled" || f == "agent_auto_update" {
			continue
		}
		out[sectionClub][f] = encodeNumber(minutes(out[sectionClub][f]))
	}
	return out
}

// marshal — JSON всех секций (как json_encode($settings) при save()); нестандартные значения верхнего уровня
// (не объекты) переносятся из stored как есть.
func (s clubSettings) marshal(stored []byte) ([]byte, error) {
	top := map[string]json.RawMessage{}
	var prev map[string]json.RawMessage
	if json.Unmarshal(stored, &prev) == nil {
		for k, v := range prev {
			if _, ok := s[k]; ok && s[k] == nil {
				top[k] = v
			}
		}
	}
	for name, fields := range s {
		if fields == nil {
			continue
		}
		b, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		top[name] = b
	}
	return json.Marshal(top)
}

// value — JSON-значение как PHP-значение после json_decode(…, true) (нет значения — null).
func value(r json.RawMessage) bonus.Value {
	if len(r) == 0 {
		return bonus.Null()
	}
	v, err := bonus.DecodeJSON(r)
	if err != nil {
		return bonus.Null()
	}
	return v
}

// phpNumber — результат number(): int, если значение целое, иначе float.
type phpNumber struct {
	isFloat bool
	i       int64
	f       float64
}

// number — SettingsController::number(): floor($value) === $value ? (int) $value : $value.
func number(f float64) phpNumber {
	if !math.IsInf(f, 0) && !math.IsNaN(f) && math.Floor(f) == f {
		return phpNumber{i: money.PHPIntCast(f)}
	}
	return phpNumber{isFloat: true, f: f}
}

// encodeNumber — json_encode числа (int — десятично, float — кратчайшая запись PHP).
func encodeNumber(n phpNumber) json.RawMessage {
	var b []byte
	if n.isFloat {
		b, _ = bonus.FloatNumber(n.f).MarshalJSON()
	} else {
		b, _ = bonus.IntNumber(n.i).MarshalJSON()
	}
	return b
}

// String — (string) число PHP (журнал settings.updated).
func (n phpNumber) String() string {
	if n.isFloat {
		return money.PHPFloatString(n.f)
	}
	return strconv.FormatInt(n.i, 10)
}

var firstNumber = regexp.MustCompile(`[\d]+(?:[.,][\d]+)?`)

// minutes — SettingsController::minutes(): число → number(); строка вида «30 сек» / «5,5 мин» — первое число
// (запятая → точка), «сек» — делится на 60.
func minutes(r json.RawMessage) phpNumber {
	if isNumeric(r) {
		return number(toFloat(r))
	}
	s := phpString(r)
	amount := 0.0
	if m := firstNumber.FindString(s); m != "" {
		amount, _ = strconv.ParseFloat(strings.ReplaceAll(m, ",", "."), 64)
	}
	if strings.Contains(strings.ToLower(s), "сек") {
		amount /= 60
	}
	return number(amount)
}

// jsonKind — тип JSON-значения по первому символу: 'n' null, 'b' bool, 's' строка, 'a' массив/объект, 'd' число.
func jsonKind(r json.RawMessage) byte {
	t := bytes.TrimSpace(r)
	if len(t) == 0 {
		return 'n'
	}
	switch t[0] {
	case 'n':
		return 'n'
	case 't', 'f':
		return 'b'
	case '"':
		return 's'
	case '[', '{':
		return 'a'
	}
	return 'd'
}

// isNumeric — is_numeric($v).
func isNumeric(r json.RawMessage) bool {
	switch jsonKind(r) {
	case 'd':
		return true
	case 's':
		return money.PHPIsNumeric(stringOf(r))
	}
	return false
}

// toFloat — (float) $v для скаляров.
func toFloat(r json.RawMessage) float64 {
	switch jsonKind(r) {
	case 'd':
		f, err := strconv.ParseFloat(string(bytes.TrimSpace(r)), 64)
		if err != nil && !math.IsInf(f, 0) {
			return 0
		}
		return f
	case 's':
		return money.PHPStringToFloat(stringOf(r))
	case 'b':
		if truthy(r) {
			return 1
		}
	case 'a':
		if truthy(r) {
			return 1
		}
	}
	return 0
}

// phpString — (string) $v (true — "1", false/null — "", массив — "Array").
func phpString(r json.RawMessage) string {
	switch jsonKind(r) {
	case 'b':
		if truthy(r) {
			return "1"
		}
		return ""
	case 'd':
		v := value(r)
		if v.Kind() == bonus.KindInt {
			return strconv.FormatInt(int64(toFloat(r)), 10)
		}
		return money.PHPFloatString(toFloat(r))
	case 's':
		return stringOf(r)
	case 'a':
		return "Array"
	}
	return ""
}

// stringOf — строковое значение поля (не строка — "").
func stringOf(r json.RawMessage) string {
	var s string
	if json.Unmarshal(r, &s) != nil {
		return ""
	}
	return s
}

// truthy — (bool) значения PHP (@checked($v)).
func truthy(r json.RawMessage) bool { return value(r).Truthy() }

// levelsOf — уровни поля (v1 или v2, решение Q-003) после нормализации.
func levelsOf(r json.RawMessage, threshold string) []bonus.Level {
	key := bonus.FieldTopUpBonusLevels
	if threshold == bonus.ThresholdSpent {
		key = bonus.FieldLoyaltyLevels
	}
	doc, _ := json.Marshal(map[string]map[string]json.RawMessage{sectionFinances: {key: r}})
	s, err := bonus.ParseSettings(doc)
	if err != nil {
		return nil
	}
	if threshold == bonus.ThresholdSpent {
		return s.LoyaltyLevels
	}
	return s.TopUpBonusLevels
}

// levelV2 — уровень формата v2 (EncodeLevels).
type levelV2 struct {
	ThresholdTiyn int64 `json:"threshold_tiyn"`
	PercentBP     int64 `json:"percent_bp"`
}

// encodeLevels — уровни в формате v2 и их список (для ответа и сравнения).
func encodeLevels(levels []bonus.Level) (json.RawMessage, []levelV2, error) {
	b, err := bonus.EncodeLevels(levels)
	if err != nil {
		return nil, nil, err
	}
	var doc struct {
		Levels []levelV2 `json:"levels"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, err
	}
	return b, doc.Levels, nil
}

// readableLevels — «порог: процент%» через «; » (как журнал старого save()), пусто — «—».
func readableLevels(levels []bonus.Level) string {
	parts := make([]string, 0, len(levels))
	for _, l := range levels {
		parts = append(parts, l.Threshold.String()+": "+l.Percent.String()+"%")
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, "; ")
}
