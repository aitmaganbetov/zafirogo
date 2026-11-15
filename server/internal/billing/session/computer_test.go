package session

import (
	"testing"
	"time"
)

func str(s string) *string { return &s }

func sessionPC(id, zone int64, user *int64, ends *time.Time) Computer {
	return Computer{ID: id, ZoneID: zone, LastSeenAt: at(0), SessionID: str("sess-1"), SessionUserID: user, SessionEndsAt: ends}
}

// GS-087 — у истёкшей сессии hasPaidSession() ложно (BR-030); само снятие — SQL service.ExpireDueSessions
// (session_ends_at <= now, billing/service/session_integration_test.go GS-087/GAP-005 и «граница»).
func TestHasPaidSessionAtExpiry(t *testing.T) {
	now := t0.Add(31 * time.Minute)
	t.Run("GS-087", func(t *testing.T) {
		for _, c := range []Computer{sessionPC(1, 1, i64(customer), at(30*time.Minute)), sessionPC(2, 1, i64(customer), at(10*time.Minute)),
			sessionPC(4, 1, i64(customer), at(31*time.Minute))} {
			if c.HasPaidSession(now) {
				t.Errorf("ПК %d: сессия истекла (ends <= now)", c.ID)
			}
		}
		if !sessionPC(3, 1, i64(customer), at(40*time.Minute)).HasPaidSession(now) {
			t.Error("ПК 3 ещё не истёк")
		}
		if (Computer{ID: 5, SessionID: str("old-agent")}).HasPaidSession(now) {
			t.Error("сессия без конца и клиента (старый агент) — не оплаченная")
		}
	})
}

// GAP-016 — покупка продлевает активную сессию: конец 11:00 + 30 мин = 11:30, тот же session_id.
func TestExtend(t *testing.T) {
	t.Run("GAP-016", func(t *testing.T) {
		free := Computer{ID: 1}
		first := Extend(free, 60, t0)
		if first.Extends || !first.EndsAt.Equal(t0.Add(time.Hour)) {
			t.Fatalf("первая покупка = %+v", first)
		}
		pc := sessionPC(1, 1, i64(customer), &first.EndsAt)
		second := Extend(pc, 30, t0)
		if !second.Extends || second.SessionID != "sess-1" || !second.EndsAt.Equal(t0.Add(90*time.Minute)) {
			t.Fatalf("продление = %+v", second)
		}
	})
	t.Run("истёкшая-не-продлевается", func(t *testing.T) {
		pc := sessionPC(1, 1, i64(customer), at(10*time.Minute))
		e := Extend(pc, 30, t0.Add(10*time.Minute))
		if e.Extends || !e.EndsAt.Equal(t0.Add(40*time.Minute)) {
			t.Fatalf("= %+v; want новая сессия от now", e)
		}
	})
	t.Run("конец-усечён-до-секунды", func(t *testing.T) {
		e := Extend(Computer{ID: 1}, 1, t0.Add(1500*time.Millisecond))
		if !e.EndsAt.Equal(t0.Add(61 * time.Second)) {
			t.Fatalf("EndsAt = %s", e.EndsAt)
		}
	})
}

// BR-015 — срок части за тариф (эффективные минуты — pricing.Tariff.EffectiveMinutes, billing/pricing/price_test.go).
func TestTariffUsage(t *testing.T) {
	tests := []struct {
		name    string
		t       TariffUsage
		expires *time.Time
	}{
		{"BR-015/300-лимит-120", TariffUsage{300, true, i64(120)}, at(120 * time.Minute)},
		{"BR-015/120-лимит-30-дней", TariffUsage{120, true, i64(43200)}, at(43200 * time.Minute)},
		{"BR-015/без-лимита", TariffUsage{60, false, i64(30)}, nil},
		{"BR-015/лимит-включён-без-минут", TariffUsage{60, true, nil}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TariffPartExpiresAt(tc.t, t0.Add(400*time.Millisecond))
			if (got == nil) != (tc.expires == nil) || (got != nil && !got.Equal(*tc.expires)) {
				t.Errorf("expires = %v, want %v", got, tc.expires)
			}
		})
	}
}
