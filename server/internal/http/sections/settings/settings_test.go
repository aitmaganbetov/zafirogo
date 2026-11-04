package settings

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

func mustInput(t *testing.T, body string) input {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	in, err := readInput(m)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// loadSettings — дефолты defaultSettings(), сохранённые значения поверх, minutes() для минутных полей.
func TestLoadSettingsDefaultsAndMinutes(t *testing.T) {
	s := loadSettings([]byte(`{"club":{"pc_restart_after_session_end":"30 сек","booking_interval":"5,5 мин","session_idle_timeout":"20"},"extra":{"x":1}}`))
	c := s[sectionClub]
	for field, want := range map[string]string{
		"pc_restart_after_session_end":               "0.5",
		"booking_interval":                           "5.5",
		"session_idle_timeout":                       "20",
		"session_auto_terminate_when_pc_unavailable": "5",
		"agent_shop_enabled":                         "true",
		"pc_action_after_session_end":                `"none"`,
	} {
		if got := string(c[field]); got != want {
			t.Errorf("%s = %s, want %s", field, got, want)
		}
	}
	if string(s[sectionGuests]["guest_mode_enabled"]) != "true" || string(s[sectionFinances]["currency"]) != `"KZT"` {
		t.Fatalf("defaults: %v %v", s[sectionGuests], s[sectionFinances])
	}
	if s["extra"] == nil {
		t.Fatal("чужая секция потеряна")
	}
	if s := loadSettings([]byte(`5`)); string(s[sectionClub]["booking_interval"]) != "30" {
		t.Fatal("не объект — дефолты")
	}
}

// SaveSettingsRequest: ошибки полей и тексты messages().
func TestValidate(t *testing.T) {
	cases := []struct {
		name, section, body string
		want                map[string]string // поле → первая ошибка (подстрока)
	}{
		{"club ok", sectionClub, `{"session_idle_timeout":"15","pc_restart_after_session_end":"0.5","session_auto_terminate_when_pc_unavailable":"5","booking_interval":"30","max_cashiers_on_shift":"0","max_operators_on_shift":"0","agent_idle_logout_minutes":"5","agent_shop_enabled":"1","agent_auto_update":"0","pc_action_after_session_end":"restart"}`, nil},
		{"club required", sectionClub, `{"session_idle_timeout":""}`, map[string]string{
			"session_idle_timeout": "is required", "booking_interval": "is required",
		}},
		{"club ranges", sectionClub, `{"session_idle_timeout":"10081","pc_restart_after_session_end":"-1","session_auto_terminate_when_pc_unavailable":"x","booking_interval":"30","max_cashiers_on_shift":"1.5","agent_idle_logout_minutes":"1441","pc_action_after_session_end":"sleep","agent_shop_enabled":"yes"}`, map[string]string{
			"session_idle_timeout":                       "not be greater than 10080",
			"pc_restart_after_session_end":               "at least 0",
			"session_auto_terminate_when_pc_unavailable": "must be a number",
			"max_cashiers_on_shift":                      "must be an integer",
			"agent_idle_logout_minutes":                  "not be greater than 1440",
			"pc_action_after_session_end":                "is invalid",
			"agent_shop_enabled":                         "",
		}},
		{"finances levels", sectionFinances, `{"currency":"KZT","loyalty_levels":[{"spent":"","percent":""},{"spent":"1000","percent":"5"},{"spent":"1000.0","percent":"101"},{"spent":"","percent":"3"}],"topup_bonus_levels":[{"amount":"10.125","percent":"5"}]}`, map[string]string{
			"loyalty_levels.0.spent":      "Суммы трат у уровней не должны повторяться.",
			"loyalty_levels.1.percent":    "Скидка — от 0,01 до 100%.",
			"loyalty_levels.2.spent":      "Укажите сумму трат для каждого уровня.",
			"topup_bonus_levels.0.amount": "decimal places",
		}},
		{"finances currency", sectionFinances, `{"currency":"EUR","discount_scope":"x","bonus_max_percent":"101","auto_billing_pause_minutes":"61"}`, map[string]string{
			"currency": "is invalid", "discount_scope": "is invalid", "bonus_max_percent": "not be greater than 100",
			"auto_billing_pause_minutes": "not be greater than 60",
		}},
		{"guests", sectionGuests, `{"guest_mode_enabled":"0","show_qr_code_on_login":"1","allow_adding_friends":null}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, fe := validate(tc.section, mustInput(t, tc.body))
			err := fe.Err()
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("ошибок нет")
			}
			for field, sub := range tc.want {
				if !fe.Has(field) {
					t.Errorf("нет ошибки поля %s (%v)", field, err)
					continue
				}
				if sub != "" && !strings.Contains(errorsText(err), sub) {
					t.Errorf("%s: нет текста %q", field, sub)
				}
			}
		})
	}
}

func errorsText(err error) string {
	var ae *httpx.APIError
	if !errors.As(err, &ae) {
		return err.Error()
	}
	v, _ := ae.Body.(apicommon.ValidationError)
	var out []string
	for k, list := range v.Errors {
		out = append(out, k+": "+strings.Join(list, " | "))
	}
	return strings.Join(out, "\n")
}

// apply — значения как в save(): булевы $request->boolean(), минуты number(), уровни v2, (int) процента.
func TestApply(t *testing.T) {
	s := loadSettings([]byte(`{"finances":{"loyalty_levels":[{"spent":1000,"percent":5}],"bonus_max_percent":100}}`))
	in := mustInput(t, `{"currency":"RUB","loyalty_enabled":"1","loyalty_levels":[{"spent":"5000","percent":"10"},{"spent":"1000","percent":"5"}],"topup_bonus_levels":[],"bonus_max_percent":"50","auto_billing":"0","discount_scope":"all"}`)
	levels, fe := validate(sectionFinances, in)
	if err := fe.Err(); err != nil {
		t.Fatal(err)
	}
	changes, err := apply(s, sectionFinances, in, levels)
	if err != nil {
		t.Fatal(err)
	}
	f := s[sectionFinances]
	if string(f["loyalty_levels"]) != `{"v":2,"levels":[{"threshold_tiyn":100000,"percent_bp":500},{"threshold_tiyn":500000,"percent_bp":1000}]}` {
		t.Fatalf("levels v2: %s", f["loyalty_levels"])
	}
	if string(f["bonus_max_percent"]) != "50" || string(f["loyalty_enabled"]) != "true" || string(f["currency"]) != `"RUB"` {
		t.Fatalf("values: %v", f)
	}
	got := map[string]string{}
	for _, c := range changes {
		got[c.field] = c.readable
	}
	want := map[string]string{
		"currency":          "KZT → RUB",
		"loyalty_enabled":   "нет → да",
		"loyalty_levels":    "1000: 5% → 1000: 5%; 5000: 10%",
		"discount_scope":    "tariffs → all",
		"bonus_max_percent": "100 → 50",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["auto_billing"]; ok {
		t.Error("auto_billing не менялся")
	}
	if _, ok := got["topup_bonus_levels"]; ok {
		t.Error("пустые уровни → пустые: не изменение")
	}
	// Повтор тех же уровней — без изменения (v1 в базе против v2 после сохранения).
	in2 := mustInput(t, `{"loyalty_levels":[{"spent":"1000","percent":"5"},{"spent":"5000","percent":"10"}]}`)
	lv2, _ := validate(sectionFinances, in2)
	ch2, _ := apply(s, sectionFinances, in2, lv2)
	for _, c := range ch2 {
		if c.field == "loyalty_levels" {
			t.Fatal("те же уровни — не изменение")
		}
	}
	// Уровни читаются расчётами (bonus.ParseSettings) из сохранённого JSON.
	doc, err := s.marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	bs, err := bonus.ParseSettings(doc)
	if err != nil || len(bs.LoyaltyLevelsInForce()) != 2 {
		t.Fatalf("ParseSettings: %v %v", bs, err)
	}
}

func TestTimezones(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	kz, all := timezoneOptions(now)
	if len(kz) != 7 || kz[0].Label != "(UTC+05:00) Asia/Almaty" {
		t.Fatalf("kz: %v", kz)
	}
	if len(all) != len(allZones) || !strings.HasPrefix(all[0].Label, "(UTC-") || !strings.HasPrefix(all[len(all)-1].Label, "(UTC+14:00)") {
		t.Fatalf("all: %v … %v", all[0], all[len(all)-1])
	}
	for _, tz := range []string{"Asia/Almaty", "UTC", "America/Argentina/Buenos_Aires"} {
		if !validTimezone(tz) {
			t.Errorf("%s", tz)
		}
	}
	for _, tz := range []string{"", "Local", "Europe/Kiev", "asia/almaty", "../etc/passwd"} {
		if validTimezone(tz) {
			t.Errorf("%s принят", tz)
		}
	}
}

func TestMinutesAndNumber(t *testing.T) {
	for in, want := range map[string]string{`"30 сек"`: "0.5", `"5,5 мин"`: "5.5", `"abc"`: "0", `null`: "0", `true`: "1", `7.0`: "7", `"2.25"`: "2.25"} {
		if got := string(encodeNumber(minutes(json.RawMessage(in)))); got != want {
			t.Errorf("minutes(%s) = %s, want %s", in, got, want)
		}
	}
}
