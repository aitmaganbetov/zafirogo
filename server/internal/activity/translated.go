package activity

// Описание записи журнала ключом перевода (решение W-3, docs/DECISIONS.md): в activity_logs.description пишется
// русский текст с подстановками (как сохранял `__()` старый код — его читают отчёты, поиск и перенесённые строки),
// а в details["_t"] — ключ (русский текст в формате Laravel, `:name`) и параметры. Экран «Логи действий» и
// колокольчик переводят описание на язык клуба при показе (Text), а не при записи.

import (
	"encoding/json"

	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

// TextKey — ключ details с ключом перевода описания.
const TextKey = "_t"

// Text — ключ перевода описания и подстановки (details._t).
type Text struct {
	Key    string            `json:"key"`
	Params map[string]string `json:"params,omitempty"`
}

// Translated заполняет описание записи: Description — русский текст (ключ с подстановками), details._t —
// ключ и параметры для перевода при показе.
func (e Entry) Translated(key string, params map[string]string) Entry {
	e.Description = i18n.Render(i18n.SourceLocale, key, params)
	details := make(map[string]any, len(e.Details)+1)
	for k, v := range e.Details {
		details[k] = v
	}
	details[TextKey] = Text{Key: key, Params: params}
	e.Details = details
	return e
}

// TextOf — ключ перевода описания из details записи (nil — описание без ключа: старые и перенесённые строки).
func TextOf(details json.RawMessage) *Text {
	if len(details) == 0 {
		return nil
	}
	var d struct {
		T *Text `json:"_t"`
	}
	if json.Unmarshal(details, &d) != nil || d.T == nil || d.T.Key == "" {
		return nil
	}
	return d.T
}
