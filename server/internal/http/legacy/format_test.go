package legacy

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMoneyFormats(t *testing.T) {
	for _, c := range []struct {
		tiyn                 int64
		plain, spaced, comma string
		float                string
	}{
		{0, "0.00", "0.00", "0,00", "0.0"},
		{150050, "1500.50", "1 500.50", "1 500,50", "1500.5"},
		{-15000, "-150.00", "-150.00", "-150,00", "-150.0"},
		{123456789, "1234567.89", "1 234 567.89", "1 234 567,89", "1234567.89"},
	} {
		if got := moneyStr(c.tiyn); got != c.plain {
			t.Errorf("moneyStr(%d) = %q", c.tiyn, got)
		}
		if got := moneySpaced(c.tiyn); got != c.spaced {
			t.Errorf("moneySpaced(%d) = %q", c.tiyn, got)
		}
		if got := moneyComma(c.tiyn); got != c.comma {
			t.Errorf("moneyComma(%d) = %q", c.tiyn, got)
		}
		b, _ := json.Marshal(tenge(c.tiyn))
		if string(b) != c.float {
			t.Errorf("tenge(%d) = %s", c.tiyn, b)
		}
	}
}

func TestTokenFormat(t *testing.T) {
	p, err := newPlainToken()
	if err != nil || len(p) != 48 || !sanctumRe.MatchString("12|"+p) {
		t.Fatalf("token %q %v", p, err)
	}
}

func TestMatch(t *testing.T) {
	a := &API{}
	cases := []struct {
		method, path, auth, body string
		want                     bool
	}{
		{"GET", "/api/staff/me", "", "", true},
		{"GET", "/api/guest/news", "", "", true},
		{"POST", "/api/auth/password/code", "", "", true},
		{"GET", "/api/auth/me", "Bearer 15|abc", "", true},
		{"GET", "/api/auth/me", "Bearer eyJhbGciOi.x.y", "", false},
		{"POST", "/api/auth/login", "", `{"phone":"+77001234567","password":"x","device_name":"mobileclient"}`, true},
		{"POST", "/api/auth/login", "", `{"phone":"+77001234567","password":"x"}`, false},
		{"GET", "/api/cash/shift", "", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		if c.auth != "" {
			r.Header.Set("Authorization", c.auth)
		}
		if got := a.Match(r); got != c.want {
			t.Errorf("%s %s: %v", c.method, c.path, got)
		}
	}
}

func TestSummarize(t *testing.T) {
	if s := summarize("A", map[string][]string{"a": {"A"}, "b": {"B", "C"}}); s != "A (and 2 more errors)" {
		t.Fatal(s)
	}
}
