package session

import (
	"errors"
	"testing"
	"time"
)

func str(s string) *string { return &s }

func sessionPC(id, zone int64, user *int64, ends *time.Time) Computer {
	return Computer{ID: id, ZoneID: zone, LastSeenAt: at(0), SessionID: str("sess-1"), SessionUserID: user, SessionEndsAt: ends}
}

// GS-087 «a paid session ends when its time is over: on heartbeat and by the scheduler»;
// GAP-005 — истечение не вызывает settle(), части остаются (BR-033).
func TestExpiry(t *testing.T) {
	now := t0.Add(31 * time.Minute)
	pc1 := sessionPC(1, 1, i64(customer), at(30*time.Minute))
	pc2 := sessionPC(2, 1, i64(customer), at(10*time.Minute))
	notYet := sessionPC(3, 1, i64(customer), at(40*time.Minute))
	exactly := sessionPC(4, 1, i64(customer), at(31*time.Minute))
	legacy := Computer{ID: 5, SessionID: str("old-agent")} // ends = null: не истекает

	t.Run("GS-087", func(t *testing.T) {
		for _, c := range []Computer{pc1, pc2} {
			if !IsDue(c, now) || c.HasPaidSession(now) {
				t.Errorf("ПК %d должен истечь", c.ID)
			}
		}
		if IsDue(notYet, now) || !notYet.HasPaidSession(now) {
			t.Error("ПК 3 ещё не истёк")
		}
		if !IsDue(exactly, now) {
			t.Error("ends == now — истекает (session_ends_at <= now)")
		}
		if IsDue(exactly, now.Add(-time.Nanosecond)) || IsDue(exactly, now.Add(-time.Second)) {
			t.Error("до конца сессия не истекает")
		}
		if IsDue(legacy, now) {
			t.Error("сессия без конца (старый агент) не истекает")
		}
	})
	t.Run("GAP-005", func(t *testing.T) {
		e, ok := ExpiryFor(pc1, now)
		want := Expiry{ClearSession: true, AgentStateIdle: true}
		if !ok || e != want {
			t.Fatalf("ExpiryFor = %+v, %v; want %+v (без settle, без удаления частей, без журнала)", e, ok, want)
		}
		if _, ok := ExpiryFor(notYet, now); ok {
			t.Error("не истёкшая сессия")
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
	t.Run("ensureComputerFor", func(t *testing.T) {
		if err := EnsureComputerFor(Computer{IsReserved: true}, customer, t0); !errors.Is(err, ErrComputerReserved) {
			t.Errorf("reserved: %v", err)
		}
		other := sessionPC(1, 1, i64(99), at(time.Hour))
		if err := EnsureComputerFor(other, customer, t0); !errors.Is(err, ErrComputerBusy) {
			t.Errorf("чужая сессия: %v", err)
		}
		guest := Computer{SessionID: str("g"), SessionGuest: true, SessionEndsAt: at(time.Hour)}
		if err := EnsureComputerFor(guest, customer, t0); !errors.Is(err, ErrComputerBusy) {
			t.Errorf("гостевая сессия: %v", err)
		}
		if err := EnsureComputerFor(sessionPC(1, 1, i64(customer), at(time.Hour)), customer, t0); err != nil {
			t.Errorf("своя сессия: %v", err)
		}
		if err := EnsureComputerFor(other, customer, t0.Add(time.Hour)); err != nil {
			t.Errorf("чужая истёкшая сессия не мешает: %v", err)
		}
	})
}

// BR-015 — эффективные минуты тарифа и срок части.
func TestTariffUsage(t *testing.T) {
	tests := []struct {
		name    string
		t       TariffUsage
		minutes int64
		expires *time.Time
	}{
		{"BR-015/300-лимит-120", TariffUsage{300, true, i64(120)}, 120, at(120 * time.Minute)},
		{"BR-015/120-лимит-30-дней", TariffUsage{120, true, i64(43200)}, 120, at(43200 * time.Minute)},
		{"BR-015/без-лимита", TariffUsage{60, false, i64(30)}, 60, nil},
		{"BR-015/лимит-включён-без-минут", TariffUsage{60, true, nil}, 60, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveMinutes(tc.t); got != tc.minutes {
				t.Errorf("EffectiveMinutes = %d, want %d", got, tc.minutes)
			}
			got := TariffPartExpiresAt(tc.t, t0.Add(400*time.Millisecond))
			if (got == nil) != (tc.expires == nil) || (got != nil && !got.Equal(*tc.expires)) {
				t.Errorf("expires = %v, want %v", got, tc.expires)
			}
		})
	}
}
