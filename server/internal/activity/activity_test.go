package activity

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestIsSecretField(t *testing.T) {
	secret := []string{"password", "new_password", "Password", "secret", "webhook_secret", "token", "access_token",
		"bot_token", "agent_token", "pin", "exit_pin", "pin_hash", "agentPin", "otp", "recovery_code", "installation_key"}
	public := []string{"amount", "api_key", "prefix", "code", "pinned", "spinner", "opinion", "user", "delivery", "events"}
	for _, s := range secret {
		if !IsSecretField(s) {
			t.Errorf("%q must be secret", s)
		}
	}
	for _, s := range public {
		if IsSecretField(s) {
			t.Errorf("%q must not be secret", s)
		}
	}
}

func TestRedact(t *testing.T) {
	in := map[string]any{
		"amount":   "100.00",
		"password": "x",
		"nested":   map[string]any{"token": "t", "ok": 1},
		"items":    []any{map[string]any{"name": "Cola", "secret": "s"}},
	}
	out, removed := Redact(in)
	slices.Sort(removed)
	if want := []string{"items.secret", "nested.token", "password"}; !slices.Equal(removed, want) {
		t.Fatalf("removed = %v, want %v", removed, want)
	}
	b, _ := json.Marshal(out)
	if string(b) != `{"amount":"100.00","items":[{"name":"Cola"}],"nested":{"ok":1}}` {
		t.Fatalf("out = %s", b)
	}
	if _, ok := in["password"]; !ok {
		t.Fatal("input must not be modified")
	}
}

func TestBuild(t *testing.T) {
	ctx := WithRequestMeta(context.Background(), RequestMeta{IP: "10.0.0.1", UserAgent: "UA", APIKeyPrefix: "zfk_ab12"})
	p, err := build(ctx, Entry{ClubID: 1, Event: "cash.income", Description: "Приход", UserID: 2,
		SubjectType: `App\Models\CashTransaction`, SubjectID: 3, Details: map[string]any{"amount": "10.00", "pin": "1234"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.IpAddress != "10.0.0.1" || *p.UserAgent != "UA" || *p.SubjectType != `App\Models\CashTransaction` || *p.SubjectID != 3 {
		t.Fatalf("params = %+v", p)
	}
	if string(p.Details) != `{"amount":"10.00","api_key":"zfk_ab12"}` {
		t.Fatalf("details = %s", p.Details)
	}

	// Пустые details — NULL ($details ?: null); без предмета — NULL.
	p, err = build(context.Background(), Entry{ClubID: 1, Event: "auth.login", Description: "Вход", UserID: 2, Meta: &RequestMeta{IP: "::1"}})
	if err != nil || p.Details != nil || p.SubjectType != nil || p.UserAgent != nil {
		t.Fatalf("p = %+v, err = %v", p, err)
	}

	if _, err := build(context.Background(), Entry{ClubID: 1, Event: "x.y", Description: "d", UserID: 1}); !errors.Is(err, ErrNoIP) {
		t.Fatalf("no IP: %v", err)
	}
	if _, err := build(ctx, Entry{ClubID: 0, Event: "x.y", Description: "d", UserID: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no club: %v", err)
	}
}
