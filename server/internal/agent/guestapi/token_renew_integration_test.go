//go:build integration

package guestapi_test

import (
	"context"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/guestapi"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
)

// Ревью 22.11.2026: гость, сидящий дольше срока токена (12 ч), не выпадает из присутствия и не получает выход: пока
// ПК сообщает его в Status идущей сессии и по записи сервера идёт его сессия на этом ПК, срок токена продлевается
// (docs/agent-protocol.md §4). Без сессии — нет.
func TestGuestTokenRenewedWhileInSession(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	phone := uniqPhone()
	user := h.customer("Дамир", phone, "password", 100000, 0)
	p := h.enrollPC()
	s, err := p.login(phone, "password")
	if err != nil {
		t.Fatal(err)
	}
	token, exp := s.GetToken().GetAccessToken(), s.GetToken().GetExpiresAt().AsTime()
	a := p.connect("boot-renew", user)
	h.waitPresence(p.id, user)
	ctx := context.Background()
	send := func(state agentv1.ComputerState, session string) {
		t.Helper()
		if err := a.st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{
			State: state, SignedInUserId: user, SessionId: session}}}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			st, _, err := h.stream.Presence().Get(ctx, h.club, p.id)
			if err == nil && st.SessionID == session {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("status %q not applied: %+v %v", session, st, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Через 7 часов (осталось меньше половины срока) гость на экране блокировки без сессии: срок прежний.
	h.clock.advance(7 * time.Hour)
	send(agentv1.ComputerState_COMPUTER_STATE_LOCKED, "")
	send(agentv1.ComputerState_COMPUTER_STATE_LOCKED, "idle")
	if g, err := h.srv.Tokens().Lookup(ctx, token); err != nil || !g.ExpiresAt.Equal(exp) {
		t.Fatalf("token without a session: %+v, %v; want expiry %v", g, err, exp)
	}
	// SEC-6 (аудит 22.11.2026): ПК заявляет идущую сессию, а по записи сервера её нет (или она чужая, гостевая с
	// кассы, кончилась) — не продлевается: одного заявления ПК мало.
	notRenewed := func(what string) {
		t.Helper()
		if g, err := h.srv.Tokens().Lookup(ctx, token); err != nil || !g.ExpiresAt.Equal(exp) {
			t.Fatalf("%s: %+v, %v; want expiry %v", what, g, err, exp)
		}
	}
	send(agentv1.ComputerState_COMPUTER_STATE_IN_SESSION, "claimed-1")
	notRenewed("no session on the server")
	other := h.customer("Другой", uniqPhone(), "password", 0, 0)
	h.abSetSession(p.id, other, false, h.clock.Now().Add(time.Hour))
	send(agentv1.ComputerState_COMPUTER_STATE_IN_SESSION, "claimed-2")
	notRenewed("session of another customer")
	h.abSetSession(p.id, 0, true, h.clock.Now().Add(time.Hour))
	send(agentv1.ComputerState_COMPUTER_STATE_IN_SESSION, "claimed-3")
	notRenewed("cashier guest session")
	h.abSetSession(p.id, user, false, h.clock.Now().Add(-time.Minute))
	send(agentv1.ComputerState_COMPUTER_STATE_IN_SESSION, "claimed-4")
	notRenewed("session already over")

	// Идёт сессия этого гостя — срок продлён на полный от текущего времени.
	h.abSetSession(p.id, user, false, h.clock.Now().Add(10*time.Hour))
	send(agentv1.ComputerState_COMPUTER_STATE_IN_SESSION, "s-1")
	g, err := h.srv.Tokens().Lookup(ctx, token)
	if err != nil || !g.ExpiresAt.Equal(h.clock.Now().Add(12*time.Hour)) {
		t.Fatalf("renewed token: %+v, %v; want expiry %v", g, err, h.clock.Now().Add(12*time.Hour))
	}
	// 13 часов после входа: токен жив, гость остаётся в присутствии.
	h.clock.advance(6 * time.Hour)
	h.abSetSession(p.id, user, false, h.clock.Now().Add(time.Hour))
	send(agentv1.ComputerState_COMPUTER_STATE_IN_SESSION, "s-2")
	if _, err := h.srv.Tokens().Lookup(ctx, token); err != nil {
		t.Fatalf("token 13 hours after sign-in: %v", err)
	}
	if st, _, _ := h.stream.Presence().Get(ctx, h.club, p.id); st.SignedInUserID != user {
		t.Fatalf("guest dropped from presence: %+v", st)
	}
	if n := h.stream.GuestRejected(); n != 0 {
		t.Fatalf("GuestRejected = %d", n)
	}
}

// Продление токена: только своему гостю, не чаще раза в полсрока, вместе с сопутствующим ключом, не дольше 30 дней
// от выдачи; отозванный токен не продлевается.
func TestGuestTokenRenewRules(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	now := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	prefix := uniq("renew") + ":"
	const ttl = 12 * time.Hour
	tokens := guestapi.NewTokens(env.valkey, prefix, ttl, clock)
	id := identity.Identity{ClubID: 7, ComputerID: 70}
	token, exp, err := tokens.Issue(ctx, id, 42)
	if err != nil {
		t.Fatal(err)
	}
	born := clock()
	also := prefix + "ab:70"
	if err := env.valkey.Do(ctx, env.valkey.B().Set().Key(also).Value("1").Px(ttl).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	pttl := func(key string) time.Duration {
		ms, err := env.valkey.Do(ctx, env.valkey.B().Pttl().Key(key).Build()).AsInt64()
		if err != nil {
			t.Fatal(err)
		}
		return time.Duration(ms) * time.Millisecond
	}
	expiry := func() time.Time {
		g, err := tokens.ComputerUser(ctx, 70)
		if err != nil || g.UserID != 42 {
			t.Fatalf("ComputerUser = %+v, %v", g, err)
		}
		return g.ExpiresAt
	}
	// Больше половины срока впереди — не продлевается.
	advance(5 * time.Hour)
	if g, err := tokens.ComputerUserRenew(ctx, 70, 42, also); err != nil || !g.ExpiresAt.Equal(exp) {
		t.Fatalf("early renew: %+v, %v", g, err)
	}
	advance(2 * time.Hour)
	// Чужому гостю и без запроса продления — нет.
	if g, err := tokens.ComputerUserRenew(ctx, 70, 43, also); err != nil || g.UserID != 42 || !g.ExpiresAt.Equal(exp) {
		t.Fatalf("renew for another user: %+v, %v", g, err)
	}
	if !expiry().Equal(exp) {
		t.Fatal("ComputerUser renewed the token")
	}
	g, err := tokens.ComputerUserRenew(ctx, 70, 42, also)
	if err != nil || !g.ExpiresAt.Equal(clock().Add(ttl)) {
		t.Fatalf("renew: %+v, %v; want %v", g, err, clock().Add(ttl))
	}
	// Ключи Valkey живут до нового срока (часы Valkey настоящие: сравнение с запасом).
	for _, key := range []string{also, prefix + "pc:70", prefix + "user:42"} {
		if left := pttl(key); left < ttl-time.Minute || left > ttl {
			t.Fatalf("ttl of %s = %v, want about %v", key, left, ttl)
		}
	}
	if lg, err := tokens.Lookup(ctx, token); err != nil || !lg.ExpiresAt.Equal(g.ExpiresAt) {
		t.Fatalf("Lookup after the renewal: %+v, %v", lg, err)
	}
	// Предел — 30 дней от выдачи.
	limit := born.Add(30 * 24 * time.Hour)
	for clock().Before(limit.Add(-7 * time.Hour)) {
		advance(7 * time.Hour)
		g, err := tokens.ComputerUserRenew(ctx, 70, 42, also)
		if err != nil || g.UserID != 42 || g.ExpiresAt.After(limit) {
			t.Fatalf("renew at %v: %+v, %v (limit %v)", clock(), g, err, limit)
		}
	}
	if !expiry().Equal(limit) {
		t.Fatalf("expiry = %v, want the limit %v", expiry(), limit)
	}
	advance(7 * time.Hour)
	if g, err := tokens.ComputerUserRenew(ctx, 70, 42, also); err != nil || g.UserID != 0 {
		t.Fatalf("token after the limit: %+v, %v", g, err)
	}
	// Отозванный токен не продлевается.
	if _, _, err := tokens.Issue(ctx, id, 42); err != nil {
		t.Fatal(err)
	}
	if err := tokens.RevokeComputer(ctx, 70, 42); err != nil {
		t.Fatal(err)
	}
	advance(7 * time.Hour)
	if g, err := tokens.ComputerUserRenew(ctx, 70, 42, also); err != nil || g.UserID != 0 {
		t.Fatalf("revoked token: %+v, %v", g, err)
	}
}
