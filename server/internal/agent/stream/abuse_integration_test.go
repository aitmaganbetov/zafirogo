//go:build integration

package stream

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
)

// fakeGuests — запись сервера «кто вошёл на ПК» (в бою — токены гостя, guestapi.Hooks.ComputerGuest).
type fakeGuests map[int64]int64

func (f fakeGuests) ComputerGuest(_ context.Context, _, computerID, _ int64, _ bool) (int64, error) {
	return f[computerID], nil
}

// flakyGuests — запись сервера с отказом по требованию (сбой Valkey).
type flakyGuests struct {
	guests fakeGuests
	down   atomic.Bool
}

func (f *flakyGuests) ComputerGuest(ctx context.Context, clubID, computerID, claimed int64, inSession bool) (int64, error) {
	if f.down.Load() {
		return 0, errors.New("valkey is down")
	}
	return f.guests.ComputerGuest(ctx, clubID, computerID, claimed, inSession)
}

// countingPool считает транзакции Postgres, открытые сервисом стрима.
type countingPool struct {
	db.Beginner
	n atomic.Int64
}

func (p *countingPool) BeginTx(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	p.n.Add(1)
	return p.Beginner.BeginTx(ctx, o)
}

type countingResults struct{ n atomic.Int64 }

func (c *countingResults) CommandResult(context.Context, identity.Identity, commands.Kind, *agentv1.CommandResult) {
	c.n.Add(1)
}

func resultMsg(id string, seq uint64, s agentv1.CommandStatus) *agentv1.AgentMessage {
	return &agentv1.AgentMessage{Payload: &agentv1.AgentMessage_CommandResult{CommandResult: &agentv1.CommandResult{
		CommandId: id, Seq: seq, Status: s}}}
}

type countingPresence struct{ n atomic.Int64 }

func (c *countingPresence) PresenceChanged(context.Context, presence.State) { c.n.Add(1) }

type countingLive struct{ n atomic.Int64 }

func (c *countingLive) LiveChanged(presence.State) { c.n.Add(1) }

func pgCount(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := testEnv.db.Owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A-01: ПК заявляет в Status гостя, которого сервер на нём не подтверждает, — в снимок присутствия и индекс
// «гость → ПК» заявление не попадает, BalanceChanged ему не уходит; подтверждённый гость — попадает.
func TestStatusGuestMustMatchServerRecord(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	honest, forger := testEnv.computer(t, club, 31), testEnv.computer(t, club, 32)
	in := testEnv.instance(t, "a", func(o *Options) { o.Guests = fakeGuests{honest: 42} })
	a1, _ := in.connect(t, club, honest, nil)
	// Подделка и в Hello, и в Status.
	h := defaultHello()
	h.Status = &agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_LOCKED, SignedInUserId: 42}
	a2, _ := in.connect(t, club, forger, h)
	signed := func(pc int64) *agentv1.AgentMessage {
		return &agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{
			State: agentv1.ComputerState_COMPUTER_STATE_LOCKED, SignedInUserId: 42, SessionId: "s" + strconv.FormatInt(pc, 10)}}}
	}
	a1.send(signed(honest))
	a2.send(signed(forger))
	ps := in.svc.Presence()
	eventually(t, 5*time.Second, "both statuses applied", func() bool {
		s1, ok1, _ := ps.Get(ctx, club, honest)
		s2, ok2, _ := ps.Get(ctx, club, forger)
		return ok1 && ok2 && s1.SessionID != "" && s2.SessionID != ""
	})
	if st, _, _ := ps.Get(ctx, club, honest); st.SignedInUserID != 42 {
		t.Fatalf("confirmed guest lost: %+v", st)
	}
	if st, _, _ := ps.Get(ctx, club, forger); st.SignedInUserID != 0 {
		t.Fatalf("forged signed_in_user_id reached the presence snapshot: %+v", st)
	}
	if n := in.svc.GuestRejected(); n != 2 {
		t.Fatalf("GuestRejected = %d, want 2 (Hello and Status)", n)
	}
	members, err := testEnv.valkey.Do(ctx, testEnv.valkey.B().Smembers().
		Key(ps.Prefix()+"guest:"+strconv.FormatInt(club, 10)+":42").Build()).AsStrSlice()
	if err != nil || len(members) != 1 || members[0] != strconv.FormatInt(honest, 10) {
		t.Fatalf("guest index = %v, %v; want only pc %d", members, err, honest)
	}
	if err := in.svc.Notifier().BalanceChanged(ctx, club, 42, &agentv1.Balance{MainTiyn: 777}); err != nil {
		t.Fatal(err)
	}
	if c := a1.command(3 * time.Second); c.GetBalanceChanged().GetBalance().GetMainTiyn() != 777 {
		t.Fatalf("honest pc: %v", c)
	}
	if m := a2.next(300 * time.Millisecond); m != nil {
		t.Fatalf("forger got %v", m)
	}
}

// A-02: 1 000 Status подряд с чередованием состояния, гостя и периферии — обработано не больше всплеска (плюс
// пополнение за время теста); записей присутствия, событий панели и строк Postgres — не больше того же потолка.
func TestStatusFloodIsCapped(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 33)
	pres, live := &countingPresence{}, &countingLive{}
	const burst = 5
	in := testEnv.instance(t, "a", func(o *Options) {
		o.MinStatusGap = 2 * time.Second
		o.StatusBurst = burst
		o.StatusFloodDrops = 1 << 30 // закрытие потока проверяет TestStatusFloodClosesStream
		o.StateDebounce = time.Millisecond
		o.Presence, o.Live = pres, live
		o.Guests = fakeGuests{pc: 7}
	})
	a, _ := in.connect(t, club, pc, nil)
	eventually(t, 5*time.Second, "connected", func() bool { return pres.n.Load() >= 1 })
	presBefore, liveBefore := pres.n.Load(), live.n.Load()
	writesBefore := in.svc.Recorder().Writes()
	eventsBefore := pgCount(t, "SELECT count(*) FROM computer_health_events WHERE club_id = $1", club)
	started := time.Now()
	states := []agentv1.ComputerState{agentv1.ComputerState_COMPUTER_STATE_LOCKED, agentv1.ComputerState_COMPUTER_STATE_IDLE,
		agentv1.ComputerState_COMPUTER_STATE_IN_SESSION}
	const sent = 1000
	for i := 0; i < sent; i++ {
		s := &agentv1.Status{State: states[i%3], SignedInUserId: int64(i%2) * 7, SessionId: strconv.Itoa(i % 5),
			Health: &agentv1.ComputerHealth{Peripherals: []*agentv1.Peripheral{{Kind: agentv1.PeripheralKind_PERIPHERAL_KIND_MOUSE,
				Name: "Mouse " + strconv.Itoa(i%4), Connected: true}}}}
		a.send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: s}})
	}
	// Все 1 000 прочитаны сервером: принятые + отброшенные.
	eventually(t, 20*time.Second, "all statuses read", func() bool {
		return in.svc.StatusDropped() >= sent-burst-uint64(time.Since(started)/(2*time.Second))-1
	})
	time.Sleep(300 * time.Millisecond) // дописать хвост (дебаунс состояния)
	limit := int64(burst) + int64(time.Since(started)/(2*time.Second)) + 1
	dropped := int64(in.svc.StatusDropped())
	if processed := sent - dropped; processed > limit {
		t.Fatalf("processed %d of %d Status, the cap is %d", processed, sent, limit)
	}
	if d := pres.n.Load() - presBefore; d > limit {
		t.Fatalf("presence events %d, the cap is %d", d, limit)
	}
	if d := live.n.Load() - liveBefore; d > limit {
		t.Fatalf("live events %d, the cap is %d", d, limit)
	}
	if d := in.svc.Recorder().Writes() - writesBefore; d > limit+1 {
		t.Fatalf("postgres state writes %d, the cap is %d", d, limit+1)
	}
	if d := pgCount(t, "SELECT count(*) FROM computer_health_events WHERE club_id = $1", club) - eventsBefore; d > limit {
		t.Fatalf("health journal rows %d, the cap is %d", d, limit)
	}
	// Поток жив: после паузы Status снова принимается.
	if _, ok, err := in.svc.Presence().Get(ctx, club, pc); err != nil || !ok {
		t.Fatalf("presence: %v %v", ok, err)
	}
	t.Logf("sent %d, processed %d, dropped %d, presence events %d, live %d", sent, sent-dropped, dropped,
		pres.n.Load()-presBefore, live.n.Load()-liveBefore)
}

// A-02: устойчивое превышение частоты закрывает поток RESOURCE_EXHAUSTED.
func TestStatusFloodClosesStream(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 34)
	in := testEnv.instance(t, "a", func(o *Options) {
		o.MinStatusGap = 2 * time.Second
		o.StatusBurst = 3
		o.StatusFloodDrops = 50
	})
	a, _ := in.connect(t, club, pc, nil)
	for i := 0; i < 200; i++ {
		// После закрытия потока сервером Send может вернуть ошибку — это и есть ожидаемый исход.
		if err := a.stream.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{
			State: agentv1.ComputerState_COMPUTER_STATE_IDLE, SessionId: strconv.Itoa(i)}}}); err != nil {
			break
		}
	}
	err := a.closedWith(5 * time.Second)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("stream closed with %v, want RESOURCE_EXHAUSTED", err)
	}
	if in.svc.StatusFloods() != 1 {
		t.Fatalf("StatusFloods = %d", in.svc.StatusFloods())
	}
}

// Ревью 22.11.2026: неверные Status (без состояния) тоже расходуют потолок частоты — их поток закрывает стрим.
func TestInvalidStatusFloodClosesStream(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 35)
	in := testEnv.instance(t, "a", func(o *Options) {
		o.MinStatusGap = 2 * time.Second
		o.StatusBurst = 3
		o.StatusFloodDrops = 50
	})
	a, _ := in.connect(t, club, pc, nil)
	for i := 0; i < 200; i++ {
		if err := a.stream.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{}}}); err != nil {
			break
		}
	}
	if err := a.closedWith(5 * time.Second); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("stream closed with %v, want RESOURCE_EXHAUSTED", err)
	}
	if in.svc.StatusFloods() != 1 {
		t.Fatalf("StatusFloods = %d", in.svc.StatusFloods())
	}
}

// Ревью 22.11.2026: поток CommandResult со случайным command_id не открывает транзакцию Postgres на каждое
// сообщение. seq больше любого отправленного этому ПК — в базу не идёт вовсе; seq в пределах отправленного — не
// больше всплеска (плюс пополнение); панели такие «итоги» не уходят.
func TestUnknownResultFloodIsCapped(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 36)
	pool := &countingPool{Beginner: testEnv.db.App}
	res := &countingResults{}
	const burst = 5
	in := testEnv.instance(t, "a", func(o *Options) {
		o.Pool = pool
		o.Results = res
		o.ResultBurst, o.ResultEvery = burst, 2*time.Second
		o.StatusFloodDrops = 1 << 30
		o.Health.Disabled = true
	})
	// Агент заявляет last_command_seq = 100: итоги с seq <= 100 могли быть от команд прежнего потока.
	h := defaultHello()
	h.LastCommandSeq = 100
	a, _ := in.connect(t, club, pc, h)
	time.Sleep(300 * time.Millisecond) // дописать состояние подключения (дебаунс)
	before := pool.n.Load()
	started := time.Now()
	const sent = 500
	for i := 0; i < sent; i++ {
		a.send(resultMsg(uuid.NewString(), 101+uint64(i), agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED)) // никогда не отправлялись
	}
	for i := 0; i < sent; i++ {
		a.send(resultMsg(uuid.NewString(), 1+uint64(i%100), agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED))
	}
	limit := func() int64 { return int64(burst) + int64(time.Since(started)/(2*time.Second)) + 1 }
	eventually(t, 20*time.Second, "all results read", func() bool {
		return int64(in.svc.ResultsDropped()) >= 2*sent-limit()
	})
	if d := pool.n.Load() - before; d > limit() {
		t.Fatalf("postgres transactions %d for %d forged results, the cap is %d", d, 2*sent, limit())
	}
	if res.n.Load() != 0 {
		t.Fatalf("forged results reached the panel: %d", res.n.Load())
	}
	if in.svc.LocalStreams() != 1 {
		t.Fatal("stream closed")
	}
}

// Устойчивый поток неизвестных CommandResult закрывает стрим RESOURCE_EXHAUSTED.
func TestUnknownResultFloodClosesStream(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 37)
	in := testEnv.instance(t, "a", func(o *Options) {
		o.ResultBurst, o.ResultEvery = 3, 2*time.Second
		o.StatusFloodDrops = 50
	})
	a, _ := in.connect(t, club, pc, nil)
	for i := 0; i < 200; i++ {
		if err := a.stream.Send(resultMsg(uuid.NewString(), 1, agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED)); err != nil {
			break
		}
	}
	if err := a.closedWith(5 * time.Second); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("stream closed with %v, want RESOURCE_EXHAUSTED", err)
	}
	if in.svc.MessageFloods() != 1 {
		t.Fatalf("MessageFloods = %d", in.svc.MessageFloods())
	}
}

// Законные итоги не теряются: итог команды, полученной в прежнем потоке (агент выполнил её и досылает итог после
// переподключения с last_command_seq = seq команды), записывается и уходит панели; повтор ACCEPTED команды этого
// потока второй раз в базу не идёт.
func TestResultOfEarlierStreamIsRecorded(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 38)
	pool := &countingPool{Beginner: testEnv.db.App}
	res := &countingResults{}
	in := testEnv.instance(t, "a", func(o *Options) {
		o.Pool = pool
		o.Results = res
		o.AckTimeout = time.Hour
		o.Health.Disabled = true
	})
	a, _ := in.connect(t, club, pc, nil)
	id1, err := in.svc.Commands().Send(ctx, club, pc, reboot())
	if err != nil {
		t.Fatal(err)
	}
	c1 := a.command(3 * time.Second)
	if c1.GetCommandId() != id1 {
		t.Fatalf("command: %v", c1)
	}
	a.hangUp()
	eventually(t, 2*time.Second, "stream closed", func() bool { return in.svc.LocalStreams() == 0 })

	h := defaultHello()
	h.LastCommandSeq = c1.GetSeq()
	a, _ = in.connect(t, club, pc, h)
	a.result(c1, agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED)
	eventually(t, 3*time.Second, "result of the earlier stream stored", func() bool { return cmdStatus(t, id1) == "succeeded" })
	eventually(t, 3*time.Second, "result reached the panel", func() bool { return res.n.Load() == 1 })
	if in.svc.ResultsDropped() != 0 {
		t.Fatalf("ResultsDropped = %d", in.svc.ResultsDropped())
	}

	// Команда этого потока: ACCEPTED трижды — одна запись.
	id2, err := in.svc.Commands().Send(ctx, club, pc, reboot())
	if err != nil {
		t.Fatal(err)
	}
	c2 := a.command(3 * time.Second)
	if c2.GetCommandId() != id2 {
		t.Fatalf("command: %v", c2)
	}
	time.Sleep(300 * time.Millisecond)
	before := pool.n.Load()
	for i := 0; i < 3; i++ {
		a.result(c2, agentv1.CommandStatus_COMMAND_STATUS_ACCEPTED)
	}
	eventually(t, 3*time.Second, "accepted stored", func() bool { return cmdStatus(t, id2) == "accepted" })
	time.Sleep(300 * time.Millisecond)
	if d := pool.n.Load() - before; d != 1 {
		t.Fatalf("transactions for a repeated ACCEPTED = %d, want 1", d)
	}
	a.result(c2, agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED)
	eventually(t, 3*time.Second, "final stored", func() bool { return cmdStatus(t, id2) == "succeeded" })
}

func eventMsg(id string) *agentv1.AgentMessage {
	return &agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Event{Event: &agentv1.AgentEvent{EventId: id, OccurredAt: timestamppb.Now(),
		Kind: &agentv1.AgentEvent_SessionEnded{SessionEnded: &agentv1.SessionEnded{SessionId: "s-1", Reason: agentv1.SessionEndReason_SESSION_END_REASON_EXPIRED}}}}}
}

// Ревью 22.11.2026: поток AgentEvent со случайным event_id — обработчик и Valkey видят не больше всплеска (плюс
// пополнение); отброшенные события не подтверждаются (агент повторит их), поток жив.
func TestEventFloodIsCapped(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 39)
	ev := &recordingEvents{}
	const burst = 5
	in := testEnv.instance(t, "a", func(o *Options) {
		o.Events = ev
		o.EventBurst, o.EventEvery = burst, 2*time.Second
		o.StatusFloodDrops = 1 << 30
	})
	a, _ := in.connect(t, club, pc, nil)
	started := time.Now()
	const sent = 500
	for i := 0; i < sent; i++ {
		a.send(eventMsg(uuid.NewString()))
	}
	limit := func() int { return burst + int(time.Since(started)/(2*time.Second)) + 1 }
	eventually(t, 20*time.Second, "all events read", func() bool { return int(in.svc.EventsDropped()) >= sent-limit() })
	ev.mu.Lock()
	handled := len(ev.events)
	ev.mu.Unlock()
	if handled > limit() || handled < burst {
		t.Fatalf("handled %d of %d events, want the burst %d..%d", handled, sent, burst, limit())
	}
	keys, err := testEnv.valkey.Do(ctx, testEnv.valkey.B().Keys().Pattern(in.svc.Presence().Prefix()+"evt:*").Build()).AsStrSlice()
	if err != nil || len(keys) > limit() {
		t.Fatalf("dedup keys %d (%v), the cap is %d", len(keys), err, limit())
	}
	acks := 0
	for m := a.next(300 * time.Millisecond); m != nil; m = a.next(300 * time.Millisecond) {
		if m.GetEventAck() != nil {
			acks++
		}
	}
	if acks != handled {
		t.Fatalf("acks %d, handled %d: a dropped event must not be acknowledged", acks, handled)
	}
}

// Устойчивый поток AgentEvent закрывает стрим RESOURCE_EXHAUSTED.
func TestEventFloodClosesStream(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 40)
	in := testEnv.instance(t, "a", func(o *Options) {
		o.EventBurst, o.EventEvery = 3, 2*time.Second
		o.StatusFloodDrops = 50
	})
	a, _ := in.connect(t, club, pc, nil)
	for i := 0; i < 200; i++ {
		if err := a.stream.Send(eventMsg(uuid.NewString())); err != nil {
			break
		}
	}
	if err := a.closedWith(5 * time.Second); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("stream closed with %v, want RESOURCE_EXHAUSTED", err)
	}
	if in.svc.MessageFloods() != 1 {
		t.Fatalf("MessageFloods = %d", in.svc.MessageFloods())
	}
}

// Ревью 22.11.2026: сбой проверки гостя (Valkey) не обнуляет уже подтверждённого гостя — иначе на всех ПК 42 → 0 → 42
// и всплеск записей outbox. Новому заявлению при сбое не верим; явное несовпадение обнуляет.
func TestGuestKeptWhenVerificationFails(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 41)
	guests := &flakyGuests{guests: fakeGuests{pc: 42}}
	pres := &countingPresence{}
	in := testEnv.instance(t, "a", func(o *Options) { o.Guests, o.Presence = guests, pres })
	a, _ := in.connect(t, club, pc, nil)
	ps := in.svc.Presence()
	signed := func(user int64, session string) {
		a.send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{
			State: agentv1.ComputerState_COMPUTER_STATE_LOCKED, SignedInUserId: user, SessionId: session}}})
		eventually(t, 5*time.Second, "status "+session+" applied", func() bool {
			st, ok, _ := ps.Get(ctx, club, pc)
			return ok && st.SessionID == session
		})
	}
	guest := func() int64 {
		st, _, _ := ps.Get(ctx, club, pc)
		return st.SignedInUserID
	}
	signed(42, "s1")
	if guest() != 42 {
		t.Fatalf("confirmed guest = %d", guest())
	}
	guests.down.Store(true)
	events := pres.n.Load()
	// Плановый Status без изменений во время сбоя: гость остаётся, событий присутствия нет.
	signed(42, "s1")
	time.Sleep(100 * time.Millisecond)
	if guest() != 42 || in.svc.GuestRejected() != 0 || pres.n.Load() != events {
		t.Fatalf("guest %d, rejected %d, presence events +%d during the outage", guest(), in.svc.GuestRejected(), pres.n.Load()-events)
	}
	signed(42, "s2")
	if guest() != 42 {
		t.Fatalf("confirmed guest dropped by a failed check: %d", guest())
	}
	// Другой гость во время сбоя — не верим.
	signed(43, "s3")
	if guest() != 0 || in.svc.GuestRejected() != 1 {
		t.Fatalf("unverified new guest = %d, rejected %d", guest(), in.svc.GuestRejected())
	}
	// После сбоя: подтверждённый возвращается, чужой — обнуляется.
	guests.down.Store(false)
	signed(42, "s4")
	if guest() != 42 {
		t.Fatalf("guest after the outage = %d", guest())
	}
	signed(43, "s5")
	if guest() != 0 {
		t.Fatalf("mismatching guest = %d", guest())
	}
}
