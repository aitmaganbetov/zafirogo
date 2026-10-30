package ratelimit

import (
	"bytes"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// Значения — docs/parts/20-api.md §1.1 (AppServiceProvider::boot, стр. 47–97).
func TestNamedRuleValues(t *testing.T) {
	type lim struct {
		max    int
		window time.Duration
	}
	want := map[string][]lim{
		ClubComputers:                {{60, time.Minute}},
		AgentEnroll:                  {{5, time.Minute}, {300, time.Minute}},
		AgentExitPin:                 {{5, 5 * time.Minute}, {100, time.Hour}},
		CustomerLogin:                {{5, time.Minute}, {300, time.Minute}},
		PasswordRecovery:             {{10, time.Minute}, {30, time.Minute}},
		StaffApp:                     {{240, time.Minute}},
		ClubResolve:                  {{10, time.Minute}, {300, time.Minute}},
		AgentRegister:                {{5, 10 * time.Minute}, {60, 10 * time.Minute}},
		AgentQrLogin:                 {{20, time.Minute}},
		AgentQrPoll:                  {{60, time.Minute}},
		AgentTokenRotate:             {{10, time.Minute}},
		AgentTranslations:            {{600, time.Minute}},
		ClubComputerRegistration:     {{10, time.Minute}},
		WebLogin:                     {{5, time.Minute}, {300, time.Minute}},
		WebRegister:                  {{5, time.Minute}, {300, time.Minute}},
		"api.guest.profile.password": {{5, time.Minute}},
		"chat.reply":                 {{60, time.Minute}},
		"users.password":             {{20, time.Minute}},
	}
	rules := map[string]Rule{}
	for _, r := range DefaultRules() {
		if _, dup := rules[r.Name]; dup {
			t.Fatalf("duplicate %s", r.Name)
		}
		rules[r.Name] = r
	}
	if len(rules) != 13+2+39 {
		t.Errorf("rules = %d, want 54 (13 named + 2 disabled + 39 throttle:N,M)", len(rules))
	}
	for name, lims := range want {
		r, ok := rules[name]
		if !ok {
			t.Errorf("rule %s missing", name)
			continue
		}
		if len(r.Limits) != len(lims) {
			t.Errorf("%s: %d limits", name, len(r.Limits))
			continue
		}
		for i, l := range lims {
			if r.Limits[i].Max != l.max || r.Limits[i].Window != l.window {
				t.Errorf("%s[%d] = %d/%v, want %d/%v", name, i, r.Limits[i].Max, r.Limits[i].Window, l.max, l.window)
			}
		}
	}
	// Решение Q-052 (docs/DECISIONS.md): лимиты веб-входа и регистрации включены по умолчанию — выключенных нет.
	for name, r := range rules {
		if r.Disabled {
			t.Errorf("%s is disabled by default", name)
		}
	}
}

func TestKeys(t *testing.T) {
	rules := map[string]Rule{}
	for _, r := range DefaultRules() {
		rules[r.Name] = r
	}
	s := Subject{IP: "10.0.0.1", Phone: "+7 (701) 111-22-33"}
	if got := rules[CustomerLogin].Limits[0].Key(s); got != "10.0.0.1|77011112233" {
		t.Errorf("customer-login key %q", got)
	}
	if got := rules[CustomerLogin].Limits[1].Key(s); got != "10.0.0.1" {
		t.Errorf("customer-login ip key %q", got)
	}
	// ПК: id из сертификата, иначе пользователь, иначе IP.
	pc := rules[ClubComputers].Limits[0].Key
	if pc(Subject{IP: "1.1.1.1", ComputerID: 5, UserID: 9}) != "device:5" || pc(Subject{IP: "1.1.1.1", UserID: 9}) != "user:9" || pc(Subject{IP: "1.1.1.1"}) != "1.1.1.1" {
		t.Error("pc key")
	}
	club := rules[AgentExitPin].Limits[1].Key
	if club(Subject{IP: "1.1.1.1", ClubID: 3}) != "club:3" || club(Subject{IP: "1.1.1.1"}) != "1.1.1.1" {
		t.Error("exit pin club key")
	}
	enroll := rules[AgentEnroll].Limits[0].Key
	if enroll(Subject{IP: "1", DeviceID: "ABC"}) != enroll(Subject{IP: "1", DeviceID: "abc"}) {
		t.Error("device id is lowercased")
	}
	thr := rules["chat.reply"].Limits[0].Key
	if thr(Subject{IP: "1.1.1.1", UserID: 4}) != "user:4" || thr(Subject{IP: "1.1.1.1"}) != "ip:1.1.1.1" {
		t.Error("throttle key")
	}
}

func TestSubjectFromRequest(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/auth/login", bytes.NewBufferString(`{"phone":"8 701 111 22 33","password":"x","club_key":"K"}`))
	r.RemoteAddr = "192.0.2.5:4444"
	ctx := principal.WithPrincipal(r.Context(), principal.Principal{UserID: 8})
	ctx = identity.WithIdentity(ctx, identity.Identity{ClubID: 2, ComputerID: 3})
	r = r.WithContext(ctx)
	s := SubjectFromRequest(r, BodyFields{Phone: true, ClubKey: true})
	if s.IP != "192.0.2.5" || s.Phone != "8 701 111 22 33" || s.ClubKey != "K" || s.UserID != 8 || s.ComputerID != 3 || s.ClubID != 2 {
		t.Fatalf("%+v", s)
	}
	body, _ := io.ReadAll(r.Body)
	if string(body) != `{"phone":"8 701 111 22 33","password":"x","club_key":"K"}` {
		t.Fatalf("body must be restored: %s", body)
	}
	// "key" — запасное имя ключа клуба.
	r2 := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"key":"K2"}`))
	if s := SubjectFromRequest(r2, BodyFields{ClubKey: true}); s.ClubKey != "K2" {
		t.Fatalf("key fallback: %+v", s)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for d, want := range map[time.Duration]int64{0: 1, time.Millisecond: 1, time.Second: 1, 1001 * time.Millisecond: 2, time.Minute: 60} {
		if got := RetryAfterSeconds(d); got != want {
			t.Errorf("%v → %d, want %d", d, got, want)
		}
	}
}
