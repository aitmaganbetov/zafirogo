package legacy

import "testing"

// BC-06: старый мобильный API отдаёт finances.currency буквально, как data_get() в Laravel — пустую строку и
// не-строку не заменяет на KZT (совместимость мобильных приложений).
func TestClubSettingCurrencyLiteral(t *testing.T) {
	for settings, want := range map[string]string{
		`{}`:                                 "KZT",
		`{"finances":{"currency":null}}`:     "KZT",
		`{"finances":{"currency":"RUB"}}`:    "RUB",
		`{"finances":{"currency":""}}`:       "",
		`{"finances":{"currency":5}}`:        "5",
		`{"finances":{"currency":true}}`:     "true",
		`{"finances":{"currency":["USD"]}}`:  `["USD"]`,
		`{"finances":{"currency":"RUB"}`:     "KZT",
		`{"finances":5,"club":{"name":"x"}}`: "KZT",
	} {
		if got := parseClubSetting([]byte(settings)).currency(); got != want {
			t.Errorf("%s: %q, want %q", settings, got, want)
		}
	}
}
