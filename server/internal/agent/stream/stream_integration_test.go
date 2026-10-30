//go:build integration

package stream

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

func reboot() *agentv1.Command {
	return &agentv1.Command{Kind: &agentv1.Command_Reboot{Reboot: &agentv1.Reboot{}}}
}

func message(text string) *agentv1.Command {
	return &agentv1.Command{Kind: &agentv1.Command_ShowMessage{ShowMessage: &agentv1.ShowMessage{Text: text}}}
}

func pgStatus(t *testing.T, pc int64) (status string, agentState *string, updated time.Time) {
	t.Helper()
	if err := testEnv.db.Owner.QueryRow(context.Background(),
		"SELECT status, agent_state, updated_at FROM club_computers WHERE id = $1", pc).Scan(&status, &agentState, &updated); err != nil {
		t.Fatal(err)
	}
	return
}

func cmdStatus(t *testing.T, commandID string) string {
	t.Helper()
	var s string
	if err := testEnv.db.Owner.QueryRow(context.Background(),
		"SELECT status FROM agent_commands WHERE command_id = $1", commandID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWelcomeAndNoWritesForSameStatus(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 7)
	in := testEnv.instance(t, "a", nil)

	a, w := in.connect(t, club, pc, nil)
	if w.GetComputerId() != pc || w.GetClubId() != club || w.GetComputer().GetNumber() != "7" ||
		w.GetComputer().GetDisplayName() != "DESKTOP-1" || w.GetLatestCommandSeq() != 0 ||
		w.GetMinProtocolVersion() != 1 || w.GetMaxProtocolVersion() != 1 || w.GetSession() != nil {
		t.Fatalf("unexpected welcome: %v", w)
	}
	// Hello: офлайн → онлайн, железо, загрузка Windows — одна запись; состояние idle — вторая (после дебаунса).
	eventually(t, 3*time.Second, "agent_state idle", func() bool {
		_, st, _ := pgStatus(t, pc)
		return st != nil && *st == "idle"
	})
	s, _, updated := pgStatus(t, pc)
	if s != "online" {
		t.Fatalf("status = %q", s)
	}
	writes := in.svc.Recorder().Writes()

	// 20 одинаковых статусов (меняется только загрузка) — ни одной записи в Postgres.
	for i := 0; i < 20; i++ {
		a.status(agentv1.ComputerState_COMPUTER_STATE_IDLE, uint32(i*100))
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if got := in.svc.Recorder().Writes(); got != writes {
		t.Fatalf("same status wrote to Postgres: %d → %d", writes, got)
	}
	if _, _, u2 := pgStatus(t, pc); !u2.Equal(updated) {
		t.Fatalf("club_computers.updated_at changed: %v → %v", updated, u2)
	}
	// Живое состояние — в Valkey.
	eventually(t, 2*time.Second, "presence load", func() bool {
		st, ok, err := in.svc.Presence().Get(context.Background(), club, pc)
		return err == nil && ok && st.Online && st.Load.CPUBasisPoints == 1900
	})

	// Смена состояния idle → locked → idle за время дебаунса: итог тот же — записи нет.
	a.status(agentv1.ComputerState_COMPUTER_STATE_LOCKED, 0)
	a.status(agentv1.ComputerState_COMPUTER_STATE_IDLE, 0)
	time.Sleep(300 * time.Millisecond)
	if got := in.svc.Recorder().Writes(); got != writes {
		t.Fatalf("flapping state wrote to Postgres: %d → %d", writes, got)
	}
	// Настоящая смена — одна запись.
	a.status(agentv1.ComputerState_COMPUTER_STATE_IN_SESSION, 0)
	eventually(t, 3*time.Second, "agent_state in_session", func() bool {
		_, st, _ := pgStatus(t, pc)
		return st != nil && *st == "in_session"
	})
	if got := in.svc.Recorder().Writes(); got != writes+1 {
		t.Fatalf("state change: writes %d → %d, want +1", writes, got)
	}

	// Переподключение с теми же данными к ПК в сети — без записи.
	a.hangUp()
	a2, _ := in.connect(t, club, pc, &agentv1.Hello{ProtocolVersion: 1, AgentVersion: "2.0.0", BootId: "boot-1",
		Status: &agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_IN_SESSION}})
	time.Sleep(300 * time.Millisecond)
	if got := in.svc.Recorder().Writes(); got != writes+1 {
		t.Fatalf("reconnect wrote to Postgres: %d → %d", writes+1, got)
	}
	a2.hangUp()
}

func TestHelloRules(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 1)
	in := testEnv.instance(t, "a", func(o *Options) { o.HelloTimeout = 200 * time.Millisecond })

	// Неподдерживаемая версия протокола.
	st, cancel := in.dial(t, club, pc)
	defer cancel()
	_ = st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Hello{Hello: &agentv1.Hello{ProtocolVersion: 2, AgentVersion: "3.0.0"}}})
	_, err := st.Recv()
	if code, reason := reasonOf(t, err); code != codes.FailedPrecondition || reason != "ERROR_CODE_PROTOCOL_VERSION_UNSUPPORTED" {
		t.Fatalf("protocol: %v %q (%v)", code, reason, err)
	}

	// Первое сообщение не Hello.
	st, cancel2 := in.dial(t, club, pc)
	defer cancel2()
	_ = st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{State: 1}}})
	if _, err := st.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status first: %v", err)
	}

	// Нет Hello за HelloTimeout.
	st, cancel3 := in.dial(t, club, pc)
	defer cancel3()
	if _, err := st.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("hello timeout: %v", err)
	}

	// ПК не из этого клуба.
	other := testEnv.club(t)
	st, cancel4 := in.dial(t, other, pc)
	defer cancel4()
	_ = st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Hello{Hello: defaultHello()}})
	if _, err := st.Recv(); status.Code(err) != codes.NotFound {
		t.Fatalf("foreign computer: %v", err)
	}

	// Без идентичности (нет сертификата).
	st, cancel5 := in.dial(t, 0, 0)
	defer cancel5()
	if _, err := st.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no identity: %v", err)
	}
}

func TestReconnectRedeliversUnacknowledged(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 2)
	in := testEnv.instance(t, "a", func(o *Options) { o.AckTimeout = time.Hour })

	a, _ := in.connect(t, club, pc, nil)
	id1, err := in.svc.Commands().Send(ctx, club, pc, reboot())
	if err != nil {
		t.Fatal(err)
	}
	c1 := a.command(3 * time.Second)
	if c1 == nil || c1.GetCommandId() != id1 || c1.GetSeq() != 1 || c1.GetReboot() == nil || c1.GetExpiresAt() == nil {
		t.Fatalf("first command: %v", c1)
	}
	// Агент отключился, не подтвердив. Пока его нет — ещё одна команда.
	a.hangUp()
	eventually(t, 2*time.Second, "stream closed", func() bool { return in.svc.LocalStreams() == 0 })
	id2, err := in.svc.Commands().Send(ctx, club, pc, message("Привет"))
	if err != nil {
		t.Fatal(err)
	}

	// Reconnect с last_command_seq = 0: досылка 1 и 2 по порядку.
	a, w := in.connect(t, club, pc, nil)
	if w.GetLatestCommandSeq() != 2 {
		t.Fatalf("latest seq = %d", w.GetLatestCommandSeq())
	}
	r1, r2 := a.command(3*time.Second), a.command(3*time.Second)
	if r1.GetCommandId() != id1 || r1.GetSeq() != 1 || r2.GetCommandId() != id2 || r2.GetSeq() != 2 {
		t.Fatalf("redelivery: %v / %v", r1, r2)
	}
	a.result(r1, agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED)
	eventually(t, 2*time.Second, "result stored", func() bool { return cmdStatus(t, id1) == "succeeded" })
	a.hangUp()
	eventually(t, 2*time.Second, "stream closed", func() bool { return in.svc.LocalStreams() == 0 })

	// Reconnect с last_command_seq = 1 (агент выполнил 1): досылается только 2.
	h := defaultHello()
	h.LastCommandSeq = 1
	a, _ = in.connect(t, club, pc, h)
	r := a.command(3 * time.Second)
	if r.GetCommandId() != id2 {
		t.Fatalf("expected only seq 2, got %v", r)
	}
	if extra := a.next(300 * time.Millisecond); extra != nil {
		t.Fatalf("unexpected message: %v", extra)
	}
	a.result(r, agentv1.CommandStatus_COMMAND_STATUS_ACCEPTED)
	eventually(t, 2*time.Second, "accepted", func() bool { return cmdStatus(t, id2) == "accepted" })
	a.result(r, agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED)
	eventually(t, 2*time.Second, "succeeded", func() bool { return cmdStatus(t, id2) == "succeeded" })
	// Повторный итог не перезаписывает первый.
	a.result(r, agentv1.CommandStatus_COMMAND_STATUS_FAILED)
	time.Sleep(200 * time.Millisecond)
	if s := cmdStatus(t, id2); s != "succeeded" {
		t.Fatalf("second result overwrote the first: %s", s)
	}
}

func TestAckTimeoutResends(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 3)
	in := testEnv.instance(t, "a", nil) // AckTimeout 300 мс
	a, _ := in.connect(t, club, pc, nil)
	id, err := in.svc.Commands().Send(context.Background(), club, pc, reboot())
	if err != nil {
		t.Fatal(err)
	}
	first, again := a.command(3*time.Second), a.command(3*time.Second)
	if first.GetCommandId() != id || again.GetCommandId() != id {
		t.Fatalf("expected resend of the same command: %v / %v", first, again)
	}
	a.result(again, agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED)
	// Повтор, ушедший до ack, допустим; после ack — тишина.
	time.Sleep(500 * time.Millisecond)
	for m := a.next(10 * time.Millisecond); m != nil; m = a.next(10 * time.Millisecond) {
		if m.GetCommand().GetCommandId() != id {
			t.Fatalf("unexpected message: %v", m)
		}
	}
	if m := a.next(800 * time.Millisecond); m != nil {
		t.Fatalf("command resent after ack: %v", m)
	}
}

func TestSecondStreamReplacesFirst(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 4)
	inA := testEnv.instance(t, "a", nil)
	inB := testEnv.instance(t, "b", nil)

	// Тот же инстанс.
	a1, _ := inA.connect(t, club, pc, nil)
	a2, _ := inA.connect(t, club, pc, nil)
	err := a1.closedWith(3 * time.Second)
	if status.Code(err) != codes.Aborted || status.Convert(err).Message() != "replaced" {
		t.Fatalf("first stream: %v", err)
	}
	// Другой инстанс (через NATS).
	b1, _ := inB.connect(t, club, pc, nil)
	if err := a2.closedWith(3 * time.Second); status.Code(err) != codes.Aborted {
		t.Fatalf("stream on the other instance: %v", err)
	}
	eventually(t, 2*time.Second, "A has no streams", func() bool { return inA.svc.LocalStreams() == 0 })
	own, ok, err := inB.svc.Presence().OwnerOf(context.Background(), pc)
	if err != nil || !ok || own.Instance != "b" {
		t.Fatalf("owner = %v %v %v", own, ok, err)
	}
	// Старое соединение не снимает присутствие нового.
	st, ok, _ := inB.svc.Presence().Get(context.Background(), club, pc)
	if !ok || !st.Online || st.Instance != "b" {
		t.Fatalf("presence after replace: %+v %v", st, ok)
	}
	b1.hangUp()
}

func TestCommandRoutedToOtherInstance(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 5)
	inA := testEnv.instance(t, "a", nil)
	inB := testEnv.instance(t, "b", nil)

	b, _ := inB.connect(t, club, pc, nil)
	id, err := inA.svc.Commands().Send(ctx, club, pc, message("Через NATS"))
	if err != nil {
		t.Fatal(err)
	}
	c := b.command(3 * time.Second)
	if c.GetCommandId() != id || c.GetShowMessage().GetText() != "Через NATS" {
		t.Fatalf("got %v", c)
	}
	b.result(c, agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED)
	eventually(t, 2*time.Second, "result", func() bool { return cmdStatus(t, id) == "succeeded" })

	// Мгновенная команда — тоже через NATS (без Postgres).
	pid, err := inA.svc.Commands().Send(ctx, club, pc, &agentv1.Command{Kind: &agentv1.Command_Ping{Ping: &agentv1.Ping{ServerTime: timestamppb.Now()}}})
	if err != nil {
		t.Fatal(err)
	}
	p := b.command(3 * time.Second)
	if p.GetCommandId() != pid || p.GetSeq() != 0 || p.GetPing() == nil {
		t.Fatalf("ping: %v", p)
	}
	var n int
	_ = testEnv.db.Owner.QueryRow(ctx, "SELECT count(*) FROM agent_commands WHERE command_id = $1", pid).Scan(&n)
	if n != 0 {
		t.Fatal("instant command must not be stored")
	}
}

func TestOfflineOnCloseAndCoalescedBalance(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 6)
	in := testEnv.instance(t, "a", nil) // OfflineGrace 200 мс
	sweeper := in.svc.NewSweeper(testEnv.db.Platform)

	a, _ := in.connect(t, club, pc, nil)
	if s, _, _ := pgStatus(t, pc); s != "online" {
		t.Fatalf("status = %s", s)
	}
	a.hangUp()
	eventually(t, 2*time.Second, "stream closed", func() bool { return in.svc.LocalStreams() == 0 })
	// В пределах grace — ещё online.
	if n, err := sweeper.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("tick within grace: %d %v", n, err)
	}
	if st, ok, _ := in.svc.Presence().Get(ctx, club, pc); !ok || st.Online {
		t.Fatalf("presence while waiting for reconnect: %+v %v", st, ok)
	}
	time.Sleep(300 * time.Millisecond)
	if n, err := sweeper.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick after grace: %d %v", n, err)
	}
	if s, _, _ := pgStatus(t, pc); s != "offline" {
		t.Fatalf("status = %s", s)
	}
	if _, ok, _ := in.svc.Presence().Get(ctx, club, pc); ok {
		t.Fatal("presence must be gone")
	}

	// Баланс изменился, пока ПК не в сети: последнее значение придёт после Welcome.
	for _, v := range []int64{100, 250} {
		if _, err := in.svc.Commands().Send(ctx, club, pc, &agentv1.Command{Kind: &agentv1.Command_BalanceChanged{
			BalanceChanged: &agentv1.BalanceChanged{UserId: 9, Balance: &agentv1.Balance{MainTiyn: v}}}}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ = in.connect(t, club, pc, nil)
	c := a.command(3 * time.Second)
	if c.GetBalanceChanged().GetBalance().GetMainTiyn() != 250 {
		t.Fatalf("coalesced balance: %v", c)
	}
	if s, _, _ := pgStatus(t, pc); s != "online" {
		t.Fatalf("status after reconnect = %s", s)
	}

	// Короткий разрыв (reconnect в пределах grace) — офлайн не пишется.
	a.hangUp()
	eventually(t, 2*time.Second, "stream closed", func() bool { return in.svc.LocalStreams() == 0 })
	a, _ = in.connect(t, club, pc, nil)
	time.Sleep(300 * time.Millisecond)
	if n, err := sweeper.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("quick reconnect: %d %v", n, err)
	}
	if s, _, _ := pgStatus(t, pc); s != "online" {
		t.Fatalf("status = %s", s)
	}
	a.hangUp()
}

func TestOfflineByTTLAfterInstanceCrash(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 8)
	pc2 := testEnv.computer(t, club, 9)
	// «Упавший инстанс»: владение взято и больше не продлевается, стрим не закрывается штатно.
	store := presence.NewStore(testEnv.valkey, "t:"+t.Name()+":", 500*time.Millisecond)
	if _, _, _, err := store.Acquire(ctx, club, pc, "dead"); err != nil {
		t.Fatal(err)
	}
	if _, err := testEnv.db.Owner.Exec(ctx, "UPDATE club_computers SET status = 'online' WHERE id = ANY($1)", []int64{pc, pc2}); err != nil {
		t.Fatal(err)
	}
	var offline []int64
	var mu sync.Mutex
	sw := &presence.Sweeper{Store: store, Pool: testEnv.db.Platform, Token: "t", OnOffline: func(_ context.Context, _, id int64) {
		mu.Lock()
		offline = append(offline, id)
		mu.Unlock()
	}}
	if n, _ := sw.Tick(ctx); n != 0 {
		t.Fatal("must stay online within TTL")
	}
	time.Sleep(700 * time.Millisecond)
	if n, err := sw.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick after TTL: %d %v", n, err)
	}
	if s, _, _ := pgStatus(t, pc); s != "offline" {
		t.Fatalf("status = %s", s)
	}
	// pc2: online в Postgres, в Valkey ничего (потеря Valkey) — офлайн со второй сверки.
	if n, err := sw.Reconcile(ctx); err != nil || n != 0 {
		t.Fatalf("first reconcile: %d %v", n, err)
	}
	// (база общая для пакета: вторая сверка снимает и ПК, оставленные online другими тестами)
	if n, err := sw.Reconcile(ctx); err != nil || n < 1 {
		t.Fatalf("second reconcile: %d %v", n, err)
	}
	if s, _, _ := pgStatus(t, pc2); s != "offline" {
		t.Fatalf("status = %s", s)
	}
	mu.Lock()
	defer mu.Unlock()
	got := map[int64]bool{}
	for _, id := range offline {
		got[id] = true
	}
	if !got[pc] || !got[pc2] {
		t.Fatalf("OnOffline calls: %v", offline)
	}
}

type recordingEvents struct {
	mu     sync.Mutex
	events []*agentv1.AgentEvent
	boots  atomic.Int32
}

func (r *recordingEvents) HandleEvent(_ context.Context, _ identity.Identity, ev *agentv1.AgentEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingEvents) FreshBoot(context.Context, identity.Identity) error {
	r.boots.Add(1)
	return nil
}

func TestEventsAckDedupAndFreshBoot(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 10)
	ev := &recordingEvents{}
	in := testEnv.instance(t, "a", func(o *Options) { o.Events = ev })

	a, _ := in.connect(t, club, pc, nil)
	if ev.boots.Load() != 1 {
		t.Fatalf("first boot: %d", ev.boots.Load())
	}
	e := &agentv1.AgentEvent{EventId: "0199f0a4-7b6e-7c3a-9d2e-1f2a3b4c5d6e", OccurredAt: timestamppb.Now(),
		Kind: &agentv1.AgentEvent_SessionEnded{SessionEnded: &agentv1.SessionEnded{SessionId: "s-1", Reason: agentv1.SessionEndReason_SESSION_END_REASON_EXPIRED}}}
	for i := 0; i < 2; i++ {
		a.send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Event{Event: e}})
		m := a.next(3 * time.Second)
		if m.GetEventAck().GetEventId() != e.GetEventId() {
			t.Fatalf("ack %d: %v", i, m)
		}
	}
	// Испорченное событие подтверждается, но не передаётся обработчику.
	a.send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Event{Event: &agentv1.AgentEvent{EventId: "bad"}}})
	if m := a.next(3 * time.Second); m.GetEventAck().GetEventId() != "bad" {
		t.Fatalf("bad event ack: %v", m)
	}
	ev.mu.Lock()
	n := len(ev.events)
	ev.mu.Unlock()
	if n != 1 {
		t.Fatalf("handler calls = %d, want 1 (dedup by event_id)", n)
	}
	a.hangUp()

	// Тот же boot_id — не свежая загрузка; новый — свежая.
	eventually(t, 2*time.Second, "closed", func() bool { return in.svc.LocalStreams() == 0 })
	a, _ = in.connect(t, club, pc, nil)
	a.hangUp()
	eventually(t, 2*time.Second, "closed", func() bool { return in.svc.LocalStreams() == 0 })
	h := defaultHello()
	h.BootId = "boot-2"
	a, _ = in.connect(t, club, pc, h)
	if ev.boots.Load() != 2 {
		t.Fatalf("boots = %d, want 2", ev.boots.Load())
	}
	a.hangUp()
}

func TestUnsupportedCommandIsNotSent(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 11)
	in := testEnv.instance(t, "a", nil)
	h := defaultHello()
	h.Capabilities = &agentv1.AgentCapabilities{}
	a, _ := in.connect(t, club, pc, h)
	id, err := in.svc.Commands().Send(ctx, club, pc, reboot())
	if err != nil {
		t.Fatal(err)
	}
	if m := a.next(500 * time.Millisecond); m != nil {
		t.Fatalf("unsupported command delivered: %v", m)
	}
	eventually(t, 2*time.Second, "failed", func() bool { return cmdStatus(t, id) == "failed" })
	var code int32
	_ = testEnv.db.Owner.QueryRow(ctx, "SELECT error_code FROM agent_commands WHERE command_id = $1", id).Scan(&code)
	if agentv1.ErrorCode(code) != agentv1.ErrorCode_ERROR_CODE_COMMAND_UNSUPPORTED {
		t.Fatalf("error_code = %d", code)
	}
}

func TestRevokeKick(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 12)
	inA := testEnv.instance(t, "a", nil)
	inB := testEnv.instance(t, "b", nil)
	a, _ := inA.connect(t, club, pc, nil)
	if err := inB.svc.Kick(context.Background(), club, pc, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_CERTIFICATE_REVOKED, "certificate revoked"); err != nil {
		t.Fatal(err)
	}
	err := a.closedWith(3 * time.Second)
	if code, reason := reasonOf(t, err); code != codes.Unauthenticated || reason != "ERROR_CODE_CERTIFICATE_REVOKED" {
		t.Fatalf("kick: %v %q", code, reason)
	}
}

func TestBalanceNotifierFindsSignedInGuest(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc1, pc2 := testEnv.computer(t, club, 13), testEnv.computer(t, club, 14)
	in := testEnv.instance(t, "a", nil)
	h := defaultHello()
	h.Status = &agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_LOCKED, SignedInUserId: 42}
	a1, _ := in.connect(t, club, pc1, h)
	a2, _ := in.connect(t, club, pc2, nil)
	if err := in.svc.Notifier().BalanceChanged(ctx, club, 42, &agentv1.Balance{MainTiyn: 12345, Currency: "KZT"}); err != nil {
		t.Fatal(err)
	}
	if c := a1.command(3 * time.Second); c.GetBalanceChanged().GetBalance().GetMainTiyn() != 12345 {
		t.Fatalf("pc1: %v", c)
	}
	if m := a2.next(300 * time.Millisecond); m != nil {
		t.Fatalf("pc2 must not get another guest's balance: %v", m)
	}
}

func TestSeqOrderUnderConcurrentSends(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 15)
	inA := testEnv.instance(t, "a", func(o *Options) { o.AckTimeout = time.Hour })
	inB := testEnv.instance(t, "b", func(o *Options) { o.AckTimeout = time.Hour })
	a, _ := inB.connect(t, club, pc, nil)
	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := inA.svc.Commands().Send(ctx, club, pc, message("x")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for want := uint64(1); want <= n; want++ {
		c := a.command(3 * time.Second)
		if c == nil || c.GetSeq() != want {
			t.Fatalf("want seq %d, got %v", want, c)
		}
	}
}
