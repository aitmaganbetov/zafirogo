package service

import "encoding/json"

// Настройки клуба, которые нужны экранам панели (Этап 3): те же правила, что у расчётов.

// BookingGapMinutes — интервал между бронями клуба (ComputerReservations::gapMinutes()).
func BookingGapMinutes(settings []byte) int64 { return bookingGapMinutes(settings) }

// DefaultCurrency — валюта клуба по умолчанию: data_get($settings, 'finances.currency', 'KZT').
const DefaultCurrency = "KZT"

// ClubCurrency — валюта клуба из clubs.settings для панели, агента, журнала и событий: непустая строка
// finances.currency, иначе KZT. Единственная реализация (BC-06); все места сервера, кроме старого мобильного API,
// зовут её.
func ClubCurrency(settings []byte) string { return clubCurrency(settings) }

func clubCurrency(settings []byte) string {
	var root struct {
		Finances struct {
			Currency any `json:"currency"`
		} `json:"finances"`
	}
	if json.Unmarshal(settings, &root) != nil {
		return DefaultCurrency
	}
	return CurrencyOf(root.Finances.Currency, false)
}

// CurrencyOf — валюта по значению finances.currency (как его разобрал encoding/json; nil — ключа нет или null).
//
// literal = false — правило нового сервера: только непустая строка, иначе KZT.
//
// literal = true — буквально data_get($settings, 'finances.currency', 'KZT') старого кода, как его отдаёт старый
// мобильный API (http/legacy): умолчание только для отсутствующего ключа и null; пустая строка остаётся пустой,
// не-строка (число, bool, массив) отдаётся своей JSON-записью. Оставлено ради совместимости мобильных приложений.
func CurrencyOf(v any, literal bool) string {
	if s, ok := v.(string); ok {
		if s != "" || literal {
			return s
		}
		return DefaultCurrency
	}
	if v == nil || !literal {
		return DefaultCurrency
	}
	b, _ := json.Marshal(v)
	return string(b)
}
