package service

import (
	"encoding/json"
	"testing"
)

// BC-06: одна реализация валюты клуба, два явных режима. strict — панель, агент, журнал, события; legacy —
// буквальный data_get() старого мобильного API (http/legacy): пустая строка и не-строка отдаются как есть.
// Таблица снята со всех пяти прежних реализаций до их объединения.
var bc06Cases = []struct{ settings, strict, legacy string }{
	{``, "KZT", "KZT"},
	{`null`, "KZT", "KZT"},
	{`{}`, "KZT", "KZT"},
	{`[]`, "KZT", "KZT"},
	{`{"finances":null}`, "KZT", "KZT"},
	{`{"finances":{}}`, "KZT", "KZT"},
	{`{"finances":[]}`, "KZT", "KZT"},
	{`{"finances":5}`, "KZT", "KZT"},
	{`{"finances":{"currency":null}}`, "KZT", "KZT"},
	{`{"finances":{"currency":"KZT"}}`, "KZT", "KZT"},
	{`{"finances":{"currency":"RUB"}}`, "RUB", "RUB"},
	{`{"finances":{"currency":"$"}}`, "$", "$"},
	{`{"finances":{"currency":" "}}`, " ", " "},
	{`{"finances":{"currency":""}}`, "KZT", ""},
	{`{"finances":{"currency":5}}`, "KZT", "5"},
	{`{"finances":{"currency":5.5}}`, "KZT", "5.5"},
	{`{"finances":{"currency":true}}`, "KZT", "true"},
	{`{"finances":{"currency":false}}`, "KZT", "false"},
	{`{"finances":{"currency":["USD"]}}`, "KZT", `["USD"]`},
	{`{"finances":{"currency":{"code":"USD"}}}`, "KZT", `{"code":"USD"}`},
	{`{"finances":{"currency":"RUB"},"club":5}`, "RUB", "RUB"},
	{`{"finances":{"currency":"RUB"}`, "KZT", "KZT"},
	{`не JSON`, "KZT", "KZT"},
}

func TestClubCurrency(t *testing.T) {
	for _, c := range bc06Cases {
		if got := ClubCurrency([]byte(c.settings)); got != c.strict {
			t.Errorf("ClubCurrency(%s) = %q, want %q", c.settings, got, c.strict)
		}
		// literal: значение — как его видит http/legacy (clubs.settings → map[раздел]map[ключ]any).
		var raw map[string]map[string]any
		_ = json.Unmarshal([]byte(c.settings), &raw)
		if got := CurrencyOf(raw["finances"]["currency"], true); got != c.legacy {
			t.Errorf("CurrencyOf(%s, literal) = %q, want %q", c.settings, got, c.legacy)
		}
		if got := CurrencyOf(raw["finances"]["currency"], false); got != c.strict {
			t.Errorf("CurrencyOf(%s, strict) = %q, want %q", c.settings, got, c.strict)
		}
	}
}
