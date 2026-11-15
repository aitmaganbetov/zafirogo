package service

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
)

// ComputerReservations::gapMinutes(): is_numeric ? max(0, (int) round((float) $value)) : 30.
func TestBookingGapMinutes(t *testing.T) {
	for _, c := range []struct {
		settings string
		want     int64
	}{
		{``, 30},
		{`null`, 30},
		{`{}`, 30},
		{`{"club": {}}`, 30},
		{`{"club": {"booking_interval": null}}`, 30},
		{`{"club": {"booking_interval": 10}}`, 10},
		{`{"club": {"booking_interval": "15"}}`, 15},
		{`{"club": {"booking_interval": " 15 "}}`, 15},
		{`{"club": {"booking_interval": 12.5}}`, 13}, // round half up
		{`{"club": {"booking_interval": "12.49"}}`, 12},
		{`{"club": {"booking_interval": -5}}`, 0},
		{`{"club": {"booking_interval": "abc"}}`, 30},
		{`{"club": {"booking_interval": "0x1A"}}`, 30},
		{`{"club": {"booking_interval": true}}`, 30},
		{`{"club": {"booking_interval": [1]}}`, 30},
	} {
		if got := bookingGapMinutes([]byte(c.settings)); got != c.want {
			t.Errorf("bookingGapMinutes(%s) = %d, want %d", c.settings, got, c.want)
		}
	}
}

// trans_choice(':count минута|:count минуты|:count минут', n) для ru.
func TestMinutesRu(t *testing.T) {
	for n, want := range map[int64]string{
		1: "1 минута", 2: "2 минуты", 4: "4 минуты", 5: "5 минут", 11: "11 минут", 12: "12 минут",
		21: "21 минута", 22: "22 минуты", 30: "30 минут", 90: "90 минут", 111: "111 минут", 112: "112 минут", 122: "122 минуты",
	} {
		if got := minutesRu(n); got != want {
			t.Errorf("minutesRu(%d) = %q, want %q", n, got, want)
		}
	}
}

// tariffs.user_roles: is_array($roles) && $roles !== [] && !in_array($role, $roles, true).
func TestUserRoles(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want []string
	}{
		{``, nil},
		{`null`, nil},
		{`"client"`, nil},
		{`[]`, []string{}},
		{`["client", "vip"]`, []string{"client", "vip"}},
		{`[5]`, []string{nonStringRole}},
		{`{"a": "client"}`, []string{"client"}},
	} {
		if got := userRoles([]byte(c.raw)); !slices.Equal(got, c.want) || (got == nil) != (c.want == nil) {
			t.Errorf("userRoles(%s) = %#v, want %#v", c.raw, got, c.want)
		}
	}
}

func TestLedgerKey(t *testing.T) {
	if got := ledgerKey("session_end", "abc"); got != "session_end:abc" {
		t.Fatal(got)
	}
	long := ledgerKey("session_end", string(make([]byte, 300)))
	if len(long) > MaxIdempotencyKeyLen {
		t.Fatalf("len = %d", len(long))
	}
}

func TestNewUUIDv4(t *testing.T) {
	id := newUUIDv4()
	if len(id) != 36 || id[14] != '4' || !slices.Contains([]byte("89ab"), id[19]) {
		t.Fatalf("uuid = %s", id)
	}
}

// BR-020, BR-030: ensureComputerFor() на строке club_computers — так, как её видит покупка (lockComputer →
// computerRow.pricing(now) → CheckForCustomer). Laravel: AgentTariffPurchaseService::ensureComputerFor()
// (стр. 212–221), ClubComputer::hasPaidSession() (стр. 97–102).
func TestComputerRowCheckForCustomer(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	const customer = int64(7)
	p64 := func(v int64) *int64 { return &v }
	sid := "sess-1"
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	cases := []struct {
		name string
		row  computerRow
		now  time.Time
		want error
	}{
		{"BR-020/резерв", computerRow{IsReserved: true}, now, pricing.ErrComputerReserved},
		{"BR-020/чужая_сессия", computerRow{CurrentSessionID: &sid, CurrentSessionUserID: p64(99), SessionEndsAt: at(time.Hour)}, now, pricing.ErrComputerBusy},
		{"BR-020/гостевая_сессия", computerRow{CurrentSessionID: &sid, SessionGuest: true, SessionEndsAt: at(time.Hour)}, now, pricing.ErrComputerBusy},
		{"BR-020/своя_сессия", computerRow{CurrentSessionID: &sid, CurrentSessionUserID: p64(customer), SessionEndsAt: at(time.Hour)}, now, nil},
		{"BR-020/чужая_истёкшая_не_мешает", computerRow{CurrentSessionID: &sid, CurrentSessionUserID: p64(99), SessionEndsAt: at(time.Hour)}, now.Add(time.Hour), nil},
		{"BR-030/конец_ровно_сейчас", computerRow{CurrentSessionID: &sid, CurrentSessionUserID: p64(99), SessionEndsAt: at(0)}, now, nil},
		{"BR-030/за_наносекунду_до_конца", computerRow{CurrentSessionID: &sid, CurrentSessionUserID: p64(99), SessionEndsAt: at(time.Nanosecond)}, now, pricing.ErrComputerBusy},
		{"BR-030/без_клиента_и_не_гостевая", computerRow{CurrentSessionID: &sid, SessionEndsAt: at(time.Hour)}, now, nil},
		{"BR-030/без_конца", computerRow{CurrentSessionID: &sid, CurrentSessionUserID: p64(99)}, now, nil},
		{"BR-020/резерв_раньше_чужой_сессии", computerRow{IsReserved: true, CurrentSessionID: &sid, CurrentSessionUserID: p64(99), SessionEndsAt: at(time.Hour)}, now, pricing.ErrComputerReserved},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.row.pricing(c.now).CheckForCustomer(customer)
			if (c.want == nil) != (err == nil) || (c.want != nil && !errors.Is(err, c.want)) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}
