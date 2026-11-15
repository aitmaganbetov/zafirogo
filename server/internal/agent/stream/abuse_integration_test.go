//go:build integration

package stream

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// fakeGuests — запись сервера «кто вошёл на ПК» (в бою — токены гостя, guestapi.Hooks.ComputerGuest).
type fakeGuests map[int64]int64

func (f fakeGuests) ComputerGuest(_ context.Context, _, computerID int64) (int64, error) {
	return f[computerID], nil
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
