package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
)

func TestNormalizeMAC(t *testing.T) {
	// BR-127: MacAddress::normalize().
	cases := map[string]string{
		"aa-bb-cc-dd-ee-ff":   "AA:BB:CC:DD:EE:FF", // BR-127/пример
		"AABBCCDDEEFF":        "AA:BB:CC:DD:EE:FF",
		"aa:bb:cc:dd:ee:ff":   "AA:BB:CC:DD:EE:FF",
		"00:00:00:00:00:00":   "",
		"FF-FF-FF-FF-FF-FF":   "",
		"":                    "",
		"AA:BB:CC:DD:EE":      "",
		"AA:BB:CC:DD:EE:FF:0": "",
		"zz:aa:bb:cc:dd:ee:f": "",                  // z отбрасывается → 11 hex
		"01:00:00:00:00:00":   "01:00:00:00:00:00", // два разных символа — годится
	}
	for in, want := range cases {
		if got := NormalizeMAC(in); got != want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
	if got := MACDeviceID("AA:BB:CC:DD:EE:FF"); got != "MAC-AABBCCDDEEFF" {
		t.Errorf("MACDeviceID = %q", got)
	}
}

func TestSamePC(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct {
		name         string
		devID, mac   *string
		uuid, reqMAC string
		want         bool
	}{
		{"карточка без идентификаторов", nil, nil, "X", "", true},
		{"UUID без учёта регистра", s("ABC-123"), nil, "abc-123", "", true},
		{"MAC карточки", nil, s("AA:BB:CC:DD:EE:FF"), "other", "aa-bb-cc-dd-ee-ff", true},
		{"device_id = MAC-форма", s("MAC-AABBCCDDEEFF"), nil, "other", "AA:BB:CC:DD:EE:FF", true},
		{"чужой ПК", s("ABC"), s("AA:BB:CC:DD:EE:FF"), "XYZ", "11:22:33:44:55:66", false},
	}
	for _, c := range cases {
		if got := samePC(c.devID, c.mac, c.uuid, c.reqMAC); got != c.want {
			t.Errorf("%s: samePC = %v", c.name, got)
		}
	}
}

func TestCodes(t *testing.T) {
	seen := map[string]bool{}
	for range 500 {
		c, err := newCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != CodeLength || strings.ContainsAny(c, "IO01") {
			t.Fatalf("code %q", c)
		}
		if NormalizeCode(FormatCode(c)) != c || NormalizeCode(strings.ToLower(c)) != c {
			t.Fatalf("normalize %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 490 {
		t.Fatalf("too many duplicates: %d unique", len(seen))
	}
	for _, bad := range []string{"", "ABCD-EFG", "ABCD-EFGHJ", "ABCD-EFG0", "ABCD-EFGI", "АБВГ-ДЕЖЗ"} {
		if NormalizeCode(bad) != "" {
			t.Errorf("NormalizeCode(%q) accepted", bad)
		}
	}
	a, b := hashCode([]byte("pepper-1"), "ABCDEFGH"), hashCode([]byte("pepper-2"), "ABCDEFGH")
	if len(a) != 32 || string(a) == string(b) {
		t.Fatal("hash must depend on pepper")
	}
}

func TestSubscriptionActive(t *testing.T) {
	// BR-130: ClubSubscription::isActive().
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	cases := []struct {
		status        string
		trial, period *time.Time
		want          bool
	}{
		{"active", nil, nil, true},
		{"active", nil, &future, true},
		{"active", nil, &past, false},
		{"trialing", &future, nil, true},
		{"trialing", &past, nil, false},
		{"trialing", nil, nil, false},
		{"expired", nil, &future, false},
		{"suspended", nil, nil, false},
	}
	for _, c := range cases {
		if got := subscriptionActive(c.status, c.trial, c.period, now); got != c.want {
			t.Errorf("%s trial=%v period=%v: %v", c.status, c.trial, c.period, got)
		}
	}
}

func TestMaxComputers(t *testing.T) {
	// BR-134: data_get(features, 'max_computers') + is_numeric + (int).
	cases := map[string]struct {
		n  int64
		ok bool
	}{
		`{"max_computers": 10}`:     {10, true},
		`{"max_computers": "12"}`:   {12, true},
		`{"max_computers": 7.9}`:    {7, true},
		`{"max_computers": "abc"}`:  {0, false},
		`{"max_computers": null}`:   {0, false},
		`["До 50 ПК", "Поддержка"]`: {0, false},
		`null`:                      {0, false},
		``:                          {0, false},
	}
	for in, want := range cases {
		n, ok := maxComputers(json.RawMessage(in))
		if n != want.n || ok != want.ok {
			t.Errorf("%s: %d %v", in, n, ok)
		}
	}
}

func TestCanRegister(t *testing.T) {
	for role, want := range map[string]bool{"owner": true, "admin": true, "manager": true, "operator": true,
		"cashier": false, "client": false, "": false} {
		if got := CanRegister(principal.Principal{ClubID: 1, Role: role}); got != want {
			t.Errorf("%s: %v", role, got)
		}
	}
	if CanRegister(principal.Principal{ClubID: 1, Role: "owner", ReadOnly: true}) {
		t.Error("read-only view must not register PCs")
	}
	if CanRegister(principal.Principal{Role: "owner"}) {
		t.Error("no club selected")
	}
}

func TestMemoryLimiter(t *testing.T) {
	l := NewMemoryLimiter()
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	ctx := context.Background()
	for i := range 5 {
		if ok, _, _ := l.Hit(ctx, "k", 5, time.Minute); !ok {
			t.Fatalf("hit %d rejected", i)
		}
	}
	ok, retry, _ := l.Hit(ctx, "k", 5, time.Minute)
	if ok || retry != time.Minute {
		t.Fatalf("6th hit: ok=%v retry=%v", ok, retry)
	}
	if ex, _, _ := l.Exceeded(ctx, "k", 5, time.Minute); !ex {
		t.Fatal("Exceeded")
	}
	now = now.Add(61 * time.Second)
	if ok, _, _ := l.Hit(ctx, "k", 5, time.Minute); !ok {
		t.Fatal("window did not reset")
	}
}

func TestToGRPC(t *testing.T) {
	cases := []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{ErrCodeInvalid, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_INVALID"},
		{ErrCodeExpired, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_EXPIRED"},
		{ErrCSRInvalid, codes.InvalidArgument, "ERROR_CODE_CSR_INVALID"},
		{ErrSubscriptionInactive, codes.FailedPrecondition, "ERROR_CODE_SUBSCRIPTION_INACTIVE"},
		{ErrComputerLimit, codes.FailedPrecondition, "ERROR_CODE_COMPUTER_LIMIT_REACHED"},
		{ErrDeviceMismatch, codes.PermissionDenied, "ERROR_CODE_DEVICE_MISMATCH"},
		{ErrZoneFull, codes.InvalidArgument, "ERROR_CODE_VALIDATION_FAILED"},
		{rateLimited(30 * time.Second), codes.ResourceExhausted, "ERROR_CODE_RATE_LIMITED"},
		{errors.New("db down"), codes.Internal, "ERROR_CODE_INTERNAL"},
	}
	for _, c := range cases {
		err := ToGRPC(c.err)
		if status.Code(err) != c.code || grpcserver.ReasonOf(err) != c.reason {
			t.Errorf("%v → %v (%s)", c.err, err, grpcserver.ReasonOf(err))
		}
	}
	if st := status.Convert(ToGRPC(errors.New("secret dsn"))); strings.Contains(st.Message(), "secret") {
		t.Error("internal error text leaked")
	}
}
