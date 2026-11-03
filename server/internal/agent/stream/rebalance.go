package stream

import (
	"context"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// Перебалансировка потоков между инстансами (S6-O4-12, docs/agent-protocol.md §13.6).
//
// L4-балансировщик (nginx least_conn) выравнивает только новые соединения: после перезапуска или добавления
// инстанса потоки остаются там, куда переподключились. Каждый инстанс раз в интервал публикует в Valkey число
// своих потоков; инстанс, у которого их заметно больше средней доли, просит небольшую пачку простаивающих ПК
// переподключиться тем же сообщением, что и при плановой остановке (Command.drain). Агент закрывает стрим и
// подключается заново через случайное время в [0, reconnect_within) — балансировщик ведёт его на инстанс
// с наименьшим числом соединений.

// Умолчания перебалансировки (config: agent.stream.rebalance_*).
const (
	DefaultRebalanceInterval        = 15 * time.Second
	DefaultRebalanceThreshold       = 1.2
	DefaultRebalanceMinExcess       = 50
	DefaultRebalanceBatch           = 100
	DefaultRebalanceReconnectWithin = 3 * time.Second
)

// RebalanceOptions — настройки перебалансировки. Нулевые числа — умолчания; Enabled = false — выключена.
type RebalanceOptions struct {
	Enabled bool
	// Interval — как часто публиковать число потоков и принимать решение (с jitter ±20 %).
	Interval time.Duration
	// Threshold — начать, когда потоков больше Threshold × среднего по живым инстансам.
	Threshold float64
	// MinExcess — и при этом превышение над средним не меньше MinExcess потоков; остановиться, когда превышение
	// не больше MinExcess/2 (гистерезис — без «маятника» на шуме подключений).
	MinExcess int
	// Batch — сколько ПК попросить переподключиться за один интервал (ограничение скорости).
	Batch int
	// ReconnectWithin — Drain.reconnect_within для отпускаемых ПК (агент ждёт случайно в [0, within)).
	ReconnectWithin time.Duration
}

func (o *RebalanceOptions) defaults() {
	if o.Interval <= 0 {
		o.Interval = DefaultRebalanceInterval
	}
	if o.Threshold <= 1 {
		o.Threshold = DefaultRebalanceThreshold
	}
	if o.MinExcess <= 0 {
		o.MinExcess = DefaultRebalanceMinExcess
	}
	if o.Batch <= 0 {
		o.Batch = DefaultRebalanceBatch
	}
	if o.ReconnectWithin <= 0 {
		o.ReconnectWithin = DefaultRebalanceReconnectWithin
	}
}

// rebalanceRelease решает, сколько потоков этого инстанса отпустить в этот интервал.
// counts — потоки всех живых инстансов, включая этот (local); active — перебалансировка уже идёт (гистерезис).
// Начинается, когда local > Threshold × среднего и превышение над ceil(среднего) ≥ MinExcess; идёт, пока
// превышение > MinExcess/2; за интервал — не больше Batch и не больше превышения (ниже средней доли не опускаемся).
func rebalanceRelease(local int, counts []int, active bool, o RebalanceOptions) (release int, stillActive bool) {
	n := len(counts)
	if n < 2 || local <= 0 {
		return 0, false
	}
	total := 0
	for _, c := range counts {
		total += max(c, 0)
	}
	target := (total + n - 1) / n // ceil: при нечётном числе этот инстанс не опускается ниже доли
	excess := local - target
	if excess <= o.MinExcess/2 {
		return 0, false
	}
	if !active && (excess < o.MinExcess || float64(local)*float64(n) <= o.Threshold*float64(total)) {
		return 0, false
	}
	return min(excess, o.Batch), true
}

// streamsKey — hash Valkey «инстанс → число потоков|срок (Unix мс)»: живые инстансы и их нагрузка.
func (s *Service) streamsKey() string { return s.store.Prefix() + "streams" }

// publishStreams записывает число потоков этого инстанса (срок — 3 интервала) и возвращает потоки живых
// инстансов (этот — по текущему local). Просроченные записи (упавшие инстансы) удаляются.
func (s *Service) publishStreams(ctx context.Context, local int, ttl time.Duration) ([]int, error) {
	vc := s.store.Client()
	now := s.o.Now()
	val := strconv.Itoa(local) + "|" + strconv.FormatInt(now.Add(ttl).UnixMilli(), 10)
	if err := vc.Do(ctx, vc.B().Hset().Key(s.streamsKey()).FieldValue().FieldValue(s.o.Instance, val).Build()).Error(); err != nil {
		return nil, err
	}
	all, err := vc.Do(ctx, vc.B().Hgetall().Key(s.streamsKey()).Build()).AsStrMap()
	if err != nil {
		return nil, err
	}
	counts := []int{local}
	var expired []string
	for inst, v := range all {
		if inst == s.o.Instance {
			continue
		}
		cnt, until, ok := parseStreamsValue(v)
		if !ok || now.UnixMilli() >= until {
			expired = append(expired, inst)
			continue
		}
		counts = append(counts, cnt)
	}
	if len(expired) > 0 {
		_ = vc.Do(ctx, vc.B().Hdel().Key(s.streamsKey()).Field(expired...).Build()).Error()
	}
	return counts, nil
}

func parseStreamsValue(v string) (count int, untilMs int64, ok bool) {
	c, u, found := strings.Cut(v, "|")
	if !found {
		return 0, 0, false
	}
	n, err1 := strconv.Atoi(c)
	t, err2 := strconv.ParseInt(u, 10, 64)
	if err1 != nil || err2 != nil || n < 0 {
		return 0, 0, false
	}
	return n, t, true
}

// unpublishStreams убирает инстанс из списка живых (Drain/Close): остальные сразу считают долю без него.
func (s *Service) unpublishStreams() {
	if !s.o.Rebalance.Enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	vc := s.store.Client()
	if err := vc.Do(ctx, vc.B().Hdel().Key(s.streamsKey()).Field(s.o.Instance).Build()).Error(); err != nil {
		s.o.Log.Warn("agent streams unpublish failed", "err", err)
	}
}

// RebalanceRequested — сколько ПК этот инстанс попросил переподключиться ради перебалансировки (метрика).
func (s *Service) RebalanceRequested() uint64 { return s.rebalanced.Load() }

// RunRebalance — фоновая задача инстанса (на каждом инстансе своя, это не «один исполнитель на кластер»):
// публикация числа потоков и перебалансировка. Выключена (agent.stream.rebalance_enabled = false) — ждёт ctx.
func (s *Service) RunRebalance(ctx context.Context) error {
	o := s.o.Rebalance
	if !o.Enabled {
		<-ctx.Done()
		return nil
	}
	defer s.unpublishStreams()
	active := false
	t := time.NewTimer(0) // сразу: новый инстанс сразу виден остальным (с 0 потоков)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stop:
			return nil
		case <-t.C:
		}
		active = s.rebalanceOnce(ctx, active)
		// Jitter ±20 %: инстансы не решают в одну и ту же секунду по одинаково устаревшим данным.
		t.Reset(time.Duration(float64(o.Interval) * (0.8 + 0.4*rand.Float64()))) // #nosec G404 -- jitter, не секрет
	}
}

// rebalanceOnce — один интервал: публикация, решение, Drain выбранным ПК. Возвращает новое значение active.
func (s *Service) rebalanceOnce(ctx context.Context, active bool) bool {
	if s.closing.Load() {
		return false // идёт плановая остановка: свой Drain уже разослан, запись снята
	}
	o := s.o.Rebalance
	local := s.LocalStreams()
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	counts, err := s.publishStreams(pctx, local, 3*o.Interval)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			s.o.Log.Warn("agent streams publish failed", "err", err)
		}
		return false
	}
	if s.closing.Load() {
		s.unpublishStreams() // Drain начался во время записи: запись не должна пережить его
		return false
	}
	release, active := rebalanceRelease(local, counts, active, o)
	if release == 0 {
		return active
	}
	picked := s.pickMovable(release, s.o.Now(), o.Interval)
	if len(picked) == 0 {
		return active
	}
	msg := &agentv1.ServerMessage{Payload: &agentv1.ServerMessage_Command{Command: &agentv1.Command{
		Kind: &agentv1.Command_Drain{Drain: &agentv1.Drain{ReconnectWithin: durationpb.New(o.ReconnectWithin)}},
	}}}
	sent := 0
	for _, c := range picked {
		if c.enqueue(msg) {
			sent++
		}
	}
	s.rebalanced.Add(uint64(sent)) // #nosec G115 -- sent ≥ 0
	total := 0
	for _, c := range counts {
		total += c
	}
	s.o.Log.Info("agent streams rebalance", "streams", local, "instances", len(counts), "total", total,
		"release", release, "requested", sent)
	return active
}

// pickMovable выбирает до n ПК, которых можно незаметно переподключить: стрим старше minAge, ПК простаивает
// (idle/locked, без гостя и сессии), нет неподтверждённых команд, очереди отправки и досылки. Выбор случайный —
// не перекашивать по клубам. Отмеченный ПК второй раз не просим (если агент не ушёл — берём другие).
func (s *Service) pickMovable(n int, now time.Time, minAge time.Duration) []*conn {
	all := s.snapshot()
	rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] }) // #nosec G404 -- выбор, не секрет
	out := make([]*conn, 0, n)
	for _, c := range all {
		if len(out) == n {
			break
		}
		if c.markMovable(now, minAge) {
			out = append(out, c)
		}
	}
	return out
}

// markMovable проверяет, можно ли переподключить ПК, и если да — отмечает его (под c.mu).
func (c *conn) markMovable(now time.Time, minAge time.Duration) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rebalanced || c.resync || len(c.queue) > 0 || len(c.inflight) > 0 {
		return false
	}
	if c.st.SignedInUserID != 0 || c.st.SessionID != "" {
		return false
	}
	if c.st.AgentState != presence.StateIdle && c.st.AgentState != presence.StateLocked {
		return false
	}
	if now.Sub(c.st.ConnectedAt) < minAge {
		return false
	}
	c.rebalanced = true
	return true
}
