//go:build integration

package guestapi_test

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// status — Status агента (вход/выход гостя на ПК приходит в нём, как в жизни).
func (a *agentStream) status(signedIn int64) {
	a.t.Helper()
	state := agentv1.ComputerState_COMPUTER_STATE_IDLE
	if signedIn > 0 {
		state = agentv1.ComputerState_COMPUTER_STATE_LOCKED
	}
	if err := a.st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{
		State: state, SignedInUserId: signedIn}}}); err != nil {
		a.t.Fatal(err)
	}
}

// waitPresence ждёт, пока снимок ПК покажет гостя user (0 — никого).
func (h *harness) waitPresence(pcID, user int64) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, ok, err := h.stream.Presence().Get(context.Background(), h.club, pcID)
		if err != nil {
			h.t.Fatal(err)
		}
		if ok && st.SignedInUserID == user {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("presence of pc %d: user %d not seen", pcID, user)
}

// noBalanceChanged — за window на ПК не пришло BalanceChanged.
func (a *agentStream) noBalanceChanged(window time.Duration) {
	a.t.Helper()
	timeout := time.After(window)
	for {
		select {
		case c, ok := <-a.cmds:
			if !ok {
				return
			}
			if c.GetBalanceChanged() != nil {
				a.t.Fatalf("unexpected BalanceChanged: %+v", c)
			}
		case <-timeout:
			return
		}
	}
}

// indexReady — метка индекса «гость → ПК» старше прогрева (как через 10 минут после выкладки).
func (h *harness) indexReady() {
	h.t.Helper()
	old := strconv.FormatInt(time.Now().Add(-time.Hour).UnixMilli(), 10)
	if err := env.valkey.Do(context.Background(), env.valkey.B().Set().Key(h.prefix+"agent:guests:since").Value(old).Build()).Error(); err != nil {
		h.t.Fatal(err)
	}
}

// S6-O4-11: BalanceChanged находит ПК гостя по индексу «гость → ПК» (без чтения снимков всего клуба): доходит
// до ПК, где гость вошёл; после выхода и входа на другом ПК — только до нового; ПК, где гостя больше нет, убран из
// индекса. Пока индекс прогревается — прежний путь через List (доставка та же).
func TestBalanceChangedByGuestIndex(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings})
	notifier := h.srv.WrapNotifier(h.stream.Notifier()) // как в app: автобиллинг → команды агенту
	phone := uniqPhone()
	user := h.customer("Дамир", phone, "password", 100000, 0)
	p1, p2 := h.enrollPC(), h.enrollPC()
	tok1 := p1.mustLogin(phone, "password")
	a1 := p1.connect("boot-1", user)
	a2 := p2.connect("boot-2", 0)
	h.waitPresence(p1.id, user)
	ps := h.stream.Presence()
	ctx := context.Background()
	balance := func(main int64) {
		t.Helper()
		if err := notifier.BalanceChanged(ctx, h.club, user, &agentv1.Balance{MainTiyn: main}); err != nil {
			t.Fatal(err)
		}
	}
	wantBalance := func(a *agentStream, main int64) {
		t.Helper()
		c := a.waitCommand(func(c *agentv1.Command) bool { return c.GetBalanceChanged() != nil })
		if c.GetBalanceChanged().GetUserId() != user || c.GetBalanceChanged().GetBalance().GetMainTiyn() != main {
			t.Fatalf("BalanceChanged = %+v", c)
		}
	}

	// Индекс ещё прогревается (метка свежая) — прежний путь: List клуба, доставка та же.
	lists := ps.ListCalls()
	balance(1)
	wantBalance(a1, 1)
	if ps.ListCalls() == lists {
		t.Fatal("warm-up: expected fallback to List")
	}

	// Индекс готов: List не вызывается ни автобиллингом, ни уведомлением агенту.
	h.indexReady()
	lists = ps.ListCalls()
	balance(2)
	wantBalance(a1, 2)
	a2.noBalanceChanged(300 * time.Millisecond)
	if d := ps.ListCalls() - lists; d != 0 {
		t.Fatalf("BalanceChanged read the whole club %d times", d)
	}

	// Гость вышел на ПК 1 и вошёл на ПК 2 — уведомление только на ПК 2; ПК 1 убран из индекса гостя.
	if _, err := p1.authc().Logout(withToken(tok1), &agentv1.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	a1.status(0)
	h.waitPresence(p1.id, 0)
	p2.mustLogin(phone, "password")
	a2.status(user)
	h.waitPresence(p2.id, user)
	balance(3)
	wantBalance(a2, 3)
	a1.noBalanceChanged(300 * time.Millisecond)
	if d := ps.ListCalls() - lists; d != 0 {
		t.Fatalf("BalanceChanged read the whole club %d times", d)
	}
	members, err := env.valkey.Do(ctx, env.valkey.B().Smembers().
		Key(h.prefix+"agent:guest:"+strconv.FormatInt(h.club, 10)+":"+strconv.FormatInt(user, 10)).Build()).AsStrSlice()
	if err != nil || !slices.Equal(members, []string{strconv.FormatInt(p2.id, 10)}) {
		t.Fatalf("guest index = %v, %v; want [%d]", members, err, p2.id)
	}

	// Стрим ПК 2 закрыт (гость в снимке остаётся до конца окна переподключения, Online = false): автобиллинг
	// видит ПК (как List), уведомление агенту — нет (оффлайн), и List по-прежнему не нужен.
	a2.cancel()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, ok, err := ps.Get(ctx, h.club, p2.id)
		if err != nil {
			t.Fatal(err)
		}
		if ok && !st.Online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pc 2 still online")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pcs, ready, err := ps.SignedIn(ctx, h.club, user)
	if err != nil || !ready || len(pcs) != 1 || pcs[0].ComputerID != p2.id || pcs[0].Online {
		t.Fatalf("SignedIn after close = %+v, %v, %v", pcs, ready, err)
	}
	balance(4)
	a1.noBalanceChanged(300 * time.Millisecond)
	if d := ps.ListCalls() - lists; d != 0 {
		t.Fatalf("BalanceChanged read the whole club %d times", d)
	}
}
