package migrate

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Нормализация clubs.settings при переносе (Q-023, BQ-3, Q-003; решения S5-M2-3, S5-M2-4). Правило: значение
// приводится к типу схемы так, чтобы КАЖДЫЙ читатель старого кода получил то же, что получал до переноса
// (поведение клуба не меняется); неиспользуемые и неизвестные ключи (черновые tax_rate, loyalty_rules …) — как есть.
//
// Минутные поля секции club читает рантайм старого кода как «is_numeric ? число : значение по умолчанию»
// (ComputerReservations::gapMinutes(), StaffShifts::limit(), GuestFeatures::idleLogoutMinutes(),
// ClubComputerController::afterSessionAction()), а экран настроек — SettingsController::minutes() («30 сек» → 0.5).
// Строка-число → число (все читатели согласны). Нечисловая строка → значение, которое реально действовало
// (умолчание читателя); если экран показывал другое — предупреждение. Поля, которые никто не читает (Q-024:
// session_idle_timeout, session_auto_terminate_when_pc_unavailable), — значение экрана minutes().
//
// Флаги — по читателю: filter_var(… ?? умолчание, FILTER_VALIDATE_BOOLEAN) (GuestFeatures, AgentUpdate) или
// (bool) data_get(…, false) (TopUpBonus, LoyaltyProgram, PaymentRules).
//
// Уровни (finances.loyalty_levels, topup_bonus_levels) → v2 (bonus.EncodeLevels от bonus.Normalize): чтение
// bonus.ParseSettings до и после совпадает (иначе поле остаётся v1 с предупреждением).

// minuteRule — как рантайм читает минутное поле.
type minuteRule struct {
	runtime bool    // читается рантаймом (иначе — только экран настроек, Q-024)
	def     float64 // умолчание рантайма для не-числа
}

var minuteRules = map[string]minuteRule{
	"booking_interval":                           {runtime: true, def: 30},  // ComputerReservations::gapMinutes(): ?? 30, иначе 30
	"agent_idle_logout_minutes":                  {runtime: true, def: 5},   // GuestFeatures::DEFAULT_IDLE_LOGOUT_MINUTES
	"pc_restart_after_session_end":               {runtime: true, def: 0.5}, // afterSessionAction(): иначе 30 с
	"max_cashiers_on_shift":                      {runtime: true, def: 0},   // StaffShifts::limit(): иначе без лимита (0)
	"max_operators_on_shift":                     {runtime: true, def: 0},
	"session_idle_timeout":                       {}, // Q-024: не читается
	"session_auto_terminate_when_pc_unavailable": {}, // Q-024: не читается
}

// boolRule — как читается флаг.
type boolRule struct {
	filter bool // filter_var(FILTER_VALIDATE_BOOLEAN), иначе (bool)
	def    bool // умолчание filter_var при null/отсутствии
}

var boolRules = map[string]map[string]boolRule{
	"club": {
		"agent_shop_enabled": {filter: true, def: true}, // GuestFeatures::shopEnabled()
		"agent_auto_update":  {filter: true, def: true}, // AgentUpdate::clubAllowsAutoUpdate()
	},
	"guests": {
		"show_qr_code_on_login":             {filter: true}, // GuestFeatures::enabled()
		"allow_guest_self_transfer":         {filter: true},
		"allow_adding_friends":              {filter: true},
		"allow_balance_transfer_to_friends": {filter: true},
		"guest_mode_enabled":                {filter: true, def: true}, // GuestFeatures::guestModeEnabled()
	},
	"finances": {
		"loyalty_enabled":     {}, // LoyaltyProgram::enabled(): (bool) data_get(…, false)
		"topup_bonus_enabled": {}, // TopUpBonus::enabled()
		"auto_billing":        {}, // PaymentRules
		"allow_whole_balance": {},
		"allow_mixed_payment": {},
	},
}

// settingsChange — что изменила нормализация (для отчёта; без значений секретов — в настройках их нет).
type settingsChange struct {
	Path, From, To string
	Warning        string // не пусто — предупреждение (экран показывал другое, уровни остались v1)
}

// normalizeSettings возвращает нормализованные настройки (nil — без изменений) и список изменений.
func normalizeSettings(stored []byte) ([]byte, []settingsChange, error) {
	if len(bytes.TrimSpace(stored)) == 0 {
		return nil, nil, nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(stored, &top) != nil || top == nil {
		return nil, nil, nil // не объект — как есть (Q-023 в отчёте ядра)
	}
	var changes []settingsChange
	sections := map[string]map[string]json.RawMessage{}
	for name, body := range top {
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) == nil && fields != nil {
			sections[name] = fields
		}
	}
	if club := sections["club"]; club != nil {
		for key, rule := range minuteRules {
			v, ok := club[key]
			if !ok {
				continue
			}
			out, warn, changed := normalizeMinutes(v, rule)
			if changed {
				club[key] = out
				changes = append(changes, settingsChange{Path: "club." + key, From: string(v), To: string(out), Warning: warn})
			}
		}
	}
	for sec, rules := range boolRules {
		fields := sections[sec]
		if fields == nil {
			continue
		}
		for key, rule := range rules {
			v, ok := fields[key]
			if !ok || isJSONBool(v) {
				continue
			}
			b := castBool(v, rule)
			out := json.RawMessage(strconv.FormatBool(b))
			fields[key] = out
			changes = append(changes, settingsChange{Path: sec + "." + key, From: string(v), To: string(out)})
		}
	}
	if fin := sections["finances"]; fin != nil {
		before, err := bonus.ParseSettings(stored)
		if err != nil {
			return nil, nil, nil //nolint:nilerr // не массив — как есть
		}
		for key, threshold := range levelThresholds {
			v, ok := fin[key]
			if !ok || isLevelsV2JSON(v) {
				continue
			}
			lv, err := bonus.DecodeJSON(v)
			if err != nil {
				continue
			}
			encoded, err := bonus.EncodeLevels(bonus.Normalize(lv, threshold))
			if err != nil {
				return nil, nil, err
			}
			fin[key] = encoded
			changes = append(changes, settingsChange{Path: "finances." + key, From: string(v), To: string(encoded)})
		}
		// Проверка Q-003: уровни, прочитанные сервером, до и после — те же (иначе поле v1 как есть).
		trial, err := marshalSettings(top, sections)
		if err != nil {
			return nil, nil, err
		}
		after, err := bonus.ParseSettings(trial)
		if err != nil {
			return nil, nil, err
		}
		for key, pair := range map[string][2][]bonus.Level{
			"loyalty_levels":     {before.LoyaltyLevels, after.LoyaltyLevels},
			"topup_bonus_levels": {before.TopUpBonusLevels, after.TopUpBonusLevels},
		} {
			if reflect.DeepEqual(pair[0], pair[1]) {
				continue
			}
			orig, _ := origField(stored, "finances", key)
			fin[key] = orig
			for i := range changes {
				if changes[i].Path == "finances."+key {
					changes[i].To = changes[i].From
					changes[i].Warning = "уровни после перевода в v2 читаются иначе — оставлены в формате v1"
				}
			}
		}
	}
	if len(changes) == 0 {
		return nil, nil, nil
	}
	out, err := marshalSettings(top, sections)
	return out, changes, err
}

func marshalSettings(top map[string]json.RawMessage, sections map[string]map[string]json.RawMessage) ([]byte, error) {
	out := make(map[string]json.RawMessage, len(top))
	for name, body := range top {
		out[name] = body
		if fields, ok := sections[name]; ok {
			b, err := json.Marshal(fields)
			if err != nil {
				return nil, err
			}
			out[name] = b
		}
	}
	return json.Marshal(out)
}

func origField(stored []byte, section, key string) (json.RawMessage, bool) {
	var top, sec map[string]json.RawMessage
	if json.Unmarshal(stored, &top) != nil || json.Unmarshal(top[section], &sec) != nil {
		return nil, false
	}
	v, ok := sec[key]
	return v, ok
}

// isLevelsV2JSON — значение уровней уже в формате v2 ({"v": 2, "levels": …}).
func isLevelsV2JSON(v json.RawMessage) bool {
	var obj struct {
		V json.Number `json:"v"`
	}
	t := bytes.TrimSpace(v)
	return len(t) > 0 && t[0] == '{' && json.Unmarshal(t, &obj) == nil && obj.V.String() == "2"
}

// normalizeMinutes — значение минутного поля после нормализации; changed = false — оставить как есть.
func normalizeMinutes(v json.RawMessage, rule minuteRule) (json.RawMessage, string, bool) {
	t := bytes.TrimSpace(v)
	if isJSONNumber(t) {
		return v, "", false
	}
	var s string
	isString := json.Unmarshal(t, &s) == nil
	if isString && money.PHPIsNumeric(s) {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return formatNumber(f), "", true
		}
	}
	screen := screenMinutes(t)
	if !rule.runtime {
		return formatNumber(screen), "", true
	}
	warn := ""
	if screen != rule.def {
		warn = "экран настроек показывал " + strconv.FormatFloat(screen, 'f', -1, 64) +
			", но действовало значение по умолчанию " + strconv.FormatFloat(rule.def, 'f', -1, 64) + " — перенесено действовавшее"
	}
	return formatNumber(rule.def), warn, true
}

func formatNumber(f float64) json.RawMessage {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return json.RawMessage(strconv.FormatInt(int64(f), 10))
	}
	return json.RawMessage(strconv.FormatFloat(f, 'f', -1, 64))
}

var firstNumber = regexp.MustCompile(`[\d]+(?:[.,][\d]+)?`)

// screenMinutes — SettingsController::minutes() (~416–426; новый сервер — settings/values.go minutes()):
// число — как есть; строка — первое число («30 сек» → 30, затем /60, если есть «сек»); прочее — 0.
func screenMinutes(v json.RawMessage) float64 {
	var s string
	if json.Unmarshal(v, &s) != nil {
		if b := bytes.TrimSpace(v); len(b) > 0 && b[0] == 't' {
			s = "1" // (string) true — «1»
		} else {
			s = ""
		}
	}
	amount := 0.0
	if m := firstNumber.FindString(s); m != "" {
		amount, _ = strconv.ParseFloat(strings.ReplaceAll(m, ",", "."), 64)
	}
	if strings.Contains(strings.ToLower(s), "сек") {
		amount /= 60
	}
	return amount
}

// castBool — значение флага так, как его читал старый код.
func castBool(v json.RawMessage, rule boolRule) bool {
	t := bytes.TrimSpace(v)
	if rule.filter {
		if len(t) == 0 || string(t) == "null" {
			return rule.def // ?? умолчание
		}
		return filterValidateBool(t)
	}
	val, err := bonus.DecodeJSON(t)
	if err != nil {
		return false
	}
	return val.Truthy() // (bool)
}

// filterValidateBool — filter_var($v, FILTER_VALIDATE_BOOLEAN) без FILTER_NULL_ON_FAILURE: true для true, 1,
// "1", "true", "on", "yes" (без учёта регистра, с обрезкой пробелов); всё прочее, включая массивы, — false.
func filterValidateBool(v json.RawMessage) bool {
	switch v[0] {
	case 't':
		return true
	case 'f', 'n', '[', '{':
		return false
	case '"':
		var s string
		if json.Unmarshal(v, &s) != nil {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "1", "true", "on", "yes":
			return true
		}
		return false
	}
	// Число: (string) 1 и 1.0 — «1».
	f, err := strconv.ParseFloat(string(v), 64)
	return err == nil && f == 1
}
