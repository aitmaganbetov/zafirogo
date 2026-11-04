//go:build integration

package stream

import (
	"context"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// balancer — L4-балансировщик least_conn: новое соединение — на инстанс с наименьшим числом открытых соединений
// (считаются с момента выбора, как TCP-соединения в nginx).
type balancer struct {
	mu    sync.Mutex
	ins   []*instance
	conns map[*instance]int
}

func (b *balancer) add(in *instance) {
	b.mu.Lock()
	b.ins = append(b.ins, in)
	b.mu.Unlock()
}

func (b *balancer) acquire() *instance {
	b.mu.Lock()
	defer b.mu.Unlock()
	var best *instance
	for _, in := range b.ins {
		if best == nil || b.conns[in] < b.conns[best] {
			best = in
		}
	}
	b.conns[best]++
	return best
}

func (b *balancer) release(in *instance) {
	b.mu.Lock()
	b.conns[in]--
	b.mu.Unlock()
}

// roamer — агент, который ведёт себя как настоящий (agent/src/Zafiro.Agent.Core AgentRuntime): на Drain закрывает
// стрим и переподключается через случайное время в [0, reconnect_within) через балансировщик; долговременные
// команды выполняет (результат SUCCEEDED) и сообщает последний выполненный seq в Hello.
type roamer struct {
	club, pc int64
	state    agentv1.ComputerState

	mu      sync.Mutex
	got     map[string]bool
	lastSeq uint64
	drains  int
	on      *instance
}

func (r *roamer) seen(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[id]
}

func (r *roamer) drained() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.drains
}

func (r *roamer) where() *instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.on
}

func (r *roamer) run(ctx context.Context, b *balancer) {
	for ctx.Err() == nil {
		in := b.acquire()
		wait := r.session(ctx, in)
		b.release(in)
		r.mu.Lock()
		r.on = nil
		r.mu.Unlock()
		if wait <= 0 {
			wait = 20 * time.Millisecond // обрыв без Drain: короткий backoff
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// session — один стрим; возвращает задержку перед переподключением (по Drain).
func (r *roamer) session(ctx context.Context, in *instance) time.Duration {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sctx = metadata.AppendToOutgoingContext(sctx, "x-test-club", strconv.FormatInt(r.club, 10),
		"x-test-computer", strconv.FormatInt(r.pc, 10))
	st, err := agentv1.NewAgentServiceClient(in.conn).Connect(sctx)
	if err != nil {
		return 0
	}
	h := defaultHello()
	r.mu.Lock()
	h.LastCommandSeq = r.lastSeq
	r.mu.Unlock()
	h.Status = &agentv1.Status{State: r.state}
	if err := st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Hello{Hello: h}}); err != nil {
		return 0
	}
	for {
		m, err := st.Recv()
		if err != nil {
			return 0
		}
		if m.GetWelcome() != nil {
			r.mu.Lock()
			r.on = in
			r.mu.Unlock()
			continue
		}
		cmd := m.GetCommand()
		if cmd == nil {
			continue
		}
		if d := cmd.GetDrain(); d != nil {
			r.mu.Lock()
			r.drains++
			r.mu.Unlock()
			// Как Backoff.Drain агента: случайно в [0, within).
			return time.Duration(rand.Int64N(int64(d.GetReconnectWithin().AsDuration()) + 1)) // #nosec G404 -- тест
		}
		if cmd.GetSeq() == 0 {
			continue
		}
		r.mu.Lock()
		r.got[cmd.GetCommandId()] = true
		r.lastSeq = max(r.lastSeq, cmd.GetSeq())
		r.mu.Unlock()
		_ = st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_CommandResult{CommandResult: &agentv1.CommandResult{
			CommandId: cmd.GetCommandId(), Seq: cmd.GetSeq(), Status: agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED}}})
	}
}

// S6-O4-12: все потоки на A (B был остановлен), поднимается B — через несколько интервалов потоки распределены
// почти поровну, ни одна долговременная команда не потеряна, ПК с сессией не трогается, «маятника» нет.
// Без перебалансировки (stream.RunRebalance) все потоки остаются на A и тест падает по таймауту.
func TestRebalanceAfterInstanceReturns(t *testing.T) {
	const (
		pcs      = 40
		interval = 300 * time.Millisecond
	)
	tune := func(o *Options) {
		o.AckTimeout = 500 * time.Millisecond
		o.Rebalance = RebalanceOptions{Enabled: true, Interval: interval, Threshold: 1.2, MinExcess: 4, Batch: 5,
			ReconnectWithin: 150 * time.Millisecond}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	club := testEnv.club(t)
	inA := testEnv.instance(t, "a", tune)
	go func() { _ = inA.svc.RunRebalance(ctx) }()

	b := &balancer{conns: map[*instance]int{}}
	b.add(inA)
	roamers := make([]*roamer, pcs)
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	for i := range roamers {
		r := &roamer{club: club, pc: testEnv.computer(t, club, i+1), state: agentv1.ComputerState_COMPUTER_STATE_LOCKED, got: map[string]bool{}}
		if i == 0 {
			r.state = agentv1.ComputerState_COMPUTER_STATE_IN_SESSION // гость за ПК: не трогать
		}
		roamers[i] = r
		wg.Add(1)
		go func() { defer wg.Done(); r.run(ctx, b) }()
	}
	eventually(t, 10*time.Second, "all on A", func() bool { return inA.svc.LocalStreams() == pcs })
	// Один A: перебалансировать не с кем.
	time.Sleep(3 * interval)
	if n := inA.svc.RebalanceRequested(); n != 0 {
		t.Fatalf("single instance requested %d reconnects", n)
	}

	// Команды идут всё время перебалансировки (с обоих инстансов).
	var sent sync.Map // command_id → *roamer
	var nSent atomic.Int64
	stopCmds := make(chan struct{})
	cmdsDone := make(chan struct{})
	inB := testEnv.instance(t, "b", tune)
	go func() {
		defer close(cmdsDone)
		from := []*instance{inA, inB}
		for i := 0; ; i++ {
			select {
			case <-stopCmds:
				return
			case <-time.After(15 * time.Millisecond):
			}
			r := roamers[i%pcs]
			id, err := from[i%2].svc.Commands().Send(ctx, club, r.pc, message("m"+strconv.Itoa(i)))
			if err != nil {
				if ctx.Err() == nil {
					t.Errorf("send: %v", err)
				}
				return
			}
			sent.Store(id, r)
			nSent.Add(1)
		}
	}()
	t.Cleanup(func() { cancel(); <-cmdsDone })
	b.add(inB)
	go func() { _ = inB.svc.RunRebalance(ctx) }()

	eventually(t, 20*time.Second, "streams balanced", func() bool {
		a, bb := inA.svc.LocalStreams(), inB.svc.LocalStreams()
		return a+bb == pcs && a-bb <= 4 && bb-a <= 4
	})
	// Выравнивание может закончиться быстрее, чем наберётся достаточно команд (быстрый CI: 36 за 3,6 с) — команды
	// идут дальше, пока их не станет ≥ 60: проверка «ни одна не потеряна» не должна зависеть от скорости машины.
	eventually(t, 10*time.Second, "enough commands", func() bool { return nSent.Load() >= 60 })
	close(stopCmds)
	<-cmdsDone
	requested := inA.svc.RebalanceRequested()
	if requested < pcs/2-4 || requested > pcs/2+4 {
		t.Errorf("A requested %d reconnects, want ≈ %d", requested, pcs/2)
	}

	// Без маятника: ещё несколько интервалов — никто никого не просит.
	time.Sleep(5 * interval)
	if inA.svc.RebalanceRequested() != requested || inB.svc.RebalanceRequested() != 0 {
		t.Fatalf("pendulum: A %d→%d, B %d", requested, inA.svc.RebalanceRequested(), inB.svc.RebalanceRequested())
	}
	a, bb := inA.svc.LocalStreams(), inB.svc.LocalStreams()
	t.Logf("A=%d B=%d, requested %d, commands %d", a, bb, requested, nSent.Load())
	if a+bb != pcs || a-bb > 4 || bb-a > 4 {
		t.Fatalf("unbalanced after settle: A=%d B=%d", a, bb)
	}

	// ПК с сессией не просили переподключиться, он остался на A.
	if r := roamers[0]; r.drained() != 0 || r.where() != inA {
		t.Fatalf("in-session computer was moved: drains %d", r.drained())
	}

	// Ни одна команда не потеряна: каждый ПК получил все свои, итог записан.
	if nSent.Load() < 50 {
		t.Fatalf("too few commands sent during rebalance: %d", nSent.Load())
	}
	eventually(t, 10*time.Second, "all commands delivered", func() bool {
		ok := true
		sent.Range(func(k, v any) bool {
			if !v.(*roamer).seen(k.(string)) {
				ok = false
			}
			return ok
		})
		return ok
	})
	var pending int
	eventually(t, 5*time.Second, "all results recorded", func() bool {
		_ = testEnv.db.Owner.QueryRow(context.Background(),
			"SELECT count(*) FROM agent_commands WHERE club_id = $1 AND status <> 'succeeded'", club).Scan(&pending)
		return pending == 0
	})
}

// Плановая остановка (Drain) снимает инстанс из списка живых сразу, а не через срок записи.
func TestRebalanceUnpublishOnDrain(t *testing.T) {
	tune := func(o *Options) {
		o.Rebalance = RebalanceOptions{Enabled: true, Interval: time.Hour, ReconnectWithin: 100 * time.Millisecond}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inA := testEnv.instance(t, "a", tune)
	inB := testEnv.instance(t, "b", tune)
	go func() { _ = inA.svc.RunRebalance(ctx) }()
	go func() { _ = inB.svc.RunRebalance(ctx) }()
	vc := testEnv.valkey
	key := inA.svc.streamsKey()
	fields := func() map[string]string {
		m, err := vc.Do(ctx, vc.B().Hgetall().Key(key).Build()).AsStrMap()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	eventually(t, 5*time.Second, "both published", func() bool { return len(fields()) == 2 })
	inA.svc.Drain(time.Second)
	eventually(t, 2*time.Second, "A unpublished", func() bool {
		f := fields()
		_, a := f["a"]
		_, b := f["b"]
		return !a && b
	})
}
