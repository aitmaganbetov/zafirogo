//go:build integration

package guestapi_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/chat"
)

// Аудит безопасности A-01: ПК без входа гостя заявляет в Hello и в Status чужой user_id — не получает ни ответ
// сотрудника в чате, ни баланс гостя; ПК, где гость вошёл по-настоящему (токен гостя), получает оба.
func TestForgedSignedInUserGetsNoChatNoBalance(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings})
	notifier := h.srv.WrapNotifier(h.stream.Notifier()) // как в app: автобиллинг → команды агенту
	phone := uniqPhone()
	user := h.customer("Дамир", phone, "password", 100000, 0)
	honest, forger := h.enrollPC(), h.enrollPC()
	honest.mustLogin(phone, "password")
	a1 := honest.connect("boot-honest", user)
	a2 := forger.connect("boot-forger", user) // заявление в Hello
	a2.status(user)                           // и в Status
	h.waitPresence(honest.id, user)
	ctx := context.Background()
	ps := h.stream.Presence()
	deadline := time.Now().Add(10 * time.Second)
	for h.stream.GuestRejected() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("GuestRejected = %d, want 2", h.stream.GuestRejected())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st, ok, err := ps.Get(ctx, h.club, forger.id); err != nil || !ok || st.SignedInUserID != 0 {
		t.Fatalf("forger snapshot: %+v %v %v", st, ok, err)
	}
	nothing := func(a *agentStream, window time.Duration) {
		t.Helper()
		timeout := time.After(window)
		for {
			select {
			case c, ok := <-a.cmds:
				if !ok {
					return
				}
				if c.GetChatMessageReceived() != nil || c.GetBalanceChanged() != nil {
					t.Fatalf("forger received %+v", c)
				}
			case <-timeout:
				return
			}
		}
	}
	// Оба пути поиска ПК гостя: пока индекс прогревается (List клуба) и по индексу «гость → ПК».
	for i, ready := range []bool{false, true} {
		if ready {
			h.indexReady()
		}
		id := int64(i + 1)
		chat.Deliver(ctx, slog.Default(), h.stream.Notifier().Sender, ps, h.club, user, chat.AgentMessage(id, "Админ", "Секрет", h.clock.Now()))
		if err := notifier.BalanceChanged(ctx, h.club, user, &agentv1.Balance{MainTiyn: 100 + id}); err != nil {
			t.Fatal(err)
		}
		c := a1.waitCommand(func(c *agentv1.Command) bool { return c.GetChatMessageReceived() != nil })
		if c.GetChatMessageReceived().GetMessage().GetMessageId() != id {
			t.Fatalf("honest pc chat: %+v", c)
		}
		nothing(a2, 300*time.Millisecond)
	}
}
