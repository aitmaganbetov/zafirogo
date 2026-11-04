package settings

// SettingsController::save(): новые значения полей секции, дифф «было → стало» для журнала settings.updated.

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
)

// change — изменённое поле: было/стало (JSON) и текст для журнала.
type change struct {
	field         string
	before, after json.RawMessage
	readable      string
}

// apply меняет поля секции, присутствующие в запросе, и возвращает изменения в порядке полей секции.
func apply(s clubSettings, section string, in input, levels map[string]bonus.Value) ([]change, error) {
	sec := s[section]
	var changes []change
	for _, field := range sectionFields[section] {
		if !in.has(field) {
			continue
		}
		v := in.get(field)
		before := sec[field]
		var after json.RawMessage
		var same bool
		var readable string
		switch {
		case slices.Contains(booleanFields, field):
			after = encodeBool(boolInput(v))
		case slices.Contains(minuteFields, field):
			after = encodeNumber(number(toFloat(v)))
		case levelFields[field] != "":
			threshold := levelFields[field]
			lv := bonus.Normalize(levels[field], threshold)
			enc, list, err := encodeLevels(lv)
			if err != nil {
				return nil, err
			}
			after = enc
			prev := levelsOf(before, threshold)
			_, prevList, err := encodeLevels(prev)
			if err != nil {
				return nil, err
			}
			same = slices.Equal(list, prevList)
			readable = readableLevels(prev) + " → " + readableLevels(lv)
		case field == "bonus_max_percent" || field == "auto_billing_pause_minutes":
			after = encodeNumber(phpNumber{i: phpIntCast(v)})
		default: // currency, pc_action_after_session_end, discount_scope — как пришло
			after = v
		}
		sec[field] = after
		if levelFields[field] == "" {
			same = bytes.Equal(bytes.TrimSpace(before), bytes.TrimSpace(after))
			readable = readableValue(before) + " → " + readableValue(after)
		}
		if !same {
			changes = append(changes, change{field: field, before: before, after: after, readable: readable})
		}
	}
	return changes, nil
}

func encodeBool(b bool) json.RawMessage {
	if b {
		return raw("true")
	}
	return raw("false")
}

// phpIntCast — (int) $v.
func phpIntCast(v json.RawMessage) int64 {
	if jsonKind(v) == 'd' && value(v).Kind() == bonus.KindInt {
		return int64(toFloat(v))
	}
	f := toFloat(v)
	if f != f || f > 9.2e18 || f < -9.2e18 {
		return 0
	}
	return int64(f)
}

// readableValue — $readable старого save(): bool — «да»/«нет», null — «—», остальное — (string).
// Слова «да»/«нет» пишутся ключами перевода (русский текст, W-3).
func readableValue(r json.RawMessage) string {
	switch jsonKind(r) {
	case 'b':
		if truthy(r) {
			return "да"
		}
		return "нет"
	case 'n':
		return "—"
	}
	return phpString(r)
}
