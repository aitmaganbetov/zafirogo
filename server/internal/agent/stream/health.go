package stream

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// Здоровье и загрузка ПК для админки (Этап 7, docs/agent-protocol.md §16): проблемы — health.Monitor на соединение,
// живые изменения — LiveListener с ограничением частоты (без outbox и Postgres на каждый Status), время ответа агента —
// по мгновенной команде Ping раз в PingInterval.

// Умолчания живых обновлений (agent.health.live_*).
const (
	DefaultLiveMinGap       = 15 * time.Second
	DefaultLiveLoadStepBP   = 500 // ±5 п.п. загрузки CPU/ОЗУ
	DefaultLiveTempStepDeci = 20  // ±2 °C
	DefaultPingInterval     = 60 * time.Second
)

// HealthOptions — проблемы здоровья, живые обновления карты и пинг.
type HealthOptions struct {
	// Thresholds — пороги проблем (нули — health.Defaults()).
	Thresholds health.Thresholds
	// Disabled — не считать проблемы и не писать журнал (health в снимке всё равно хранится).
	Disabled bool
	// LiveMinGap — не чаще одного живого обновления ПК за это время (смена набора проблем — сразу).
	LiveMinGap time.Duration
	// LiveLoadStepBP — заметное изменение загрузки CPU/ОЗУ, сотые доли процента.
	LiveLoadStepBP uint32
	// LiveTempStepDeci — заметное изменение температуры CPU/GPU, десятые доли °C.
	LiveTempStepDeci int32
	// PingInterval — как часто мерить время ответа агента (Ping по стриму); < 0 — не мерить.
	PingInterval time.Duration
}

func (o *HealthOptions) defaults() {
	if o.Thresholds == (health.Thresholds{}) {
		o.Thresholds = health.Defaults()
	}
	if o.LiveMinGap <= 0 {
		o.LiveMinGap = DefaultLiveMinGap
	}
	if o.LiveLoadStepBP == 0 {
		o.LiveLoadStepBP = DefaultLiveLoadStepBP
	}
	if o.LiveTempStepDeci <= 0 {
		o.LiveTempStepDeci = DefaultLiveTempStepDeci
	}
	if o.PingInterval == 0 {
		o.PingInterval = DefaultPingInterval
	}
}

// LiveListener — заметное изменение загрузки или здоровья ПК (карта клуба по WebSocket). Вызывается из горутины
// чтения стрима: реализация не должна блокироваться (публикация в NATS без подтверждения).
type LiveListener interface {
	LiveChanged(st presence.State)
}

// liveMark — что ушло в последнем живом обновлении ПК.
type liveMark struct {
	at               time.Time
	sent             bool
	hasLoad          bool
	cpu, ram         uint32
	cpuTemp, gpuTemp int32
	hasHealth        bool
	problems         []string
	health           *presence.Health
}

func markOf(st presence.State, now time.Time) liveMark {
	m := liveMark{at: now, sent: true, hasLoad: st.HasLoad, cpu: st.Load.CPUBasisPoints, ram: st.Load.RAMBasisPoints,
		hasHealth: st.Health != nil, problems: st.Problems, health: st.Health}
	if st.Health != nil {
		m.cpuTemp, m.gpuTemp = st.Health.CPUTempDeci, st.Health.GPUTempDeci
	}
	return m
}

func diffU(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

func diffI(a, b int32) int32 {
	if a > b {
		return a - b
	}
	return b - a
}

// liveDue — пора ли отправить живое обновление: смена набора проблем, периферии или SMART — сразу (S7-HA-2); заметное изменение загрузки (±step п.п.),
// температуры (±step °C) или появление данных — не чаще LiveMinGap.
func (o HealthOptions) liveDue(prev liveMark, st presence.State, now time.Time) bool {
	cur := markOf(st, now)
	if !slices.Equal(prev.problems, cur.problems) || (prev.sent && presence.HealthChanged(prev.health, cur.health)) {
		return true
	}
	if prev.sent && now.Sub(prev.at) < o.LiveMinGap {
		return false
	}
	return cur.hasLoad != prev.hasLoad || cur.hasHealth != prev.hasHealth ||
		(cur.hasLoad && (diffU(cur.cpu, prev.cpu) >= o.LiveLoadStepBP || diffU(cur.ram, prev.ram) >= o.LiveLoadStepBP)) ||
		(cur.hasHealth && (diffI(cur.cpuTemp, prev.cpuTemp) >= o.LiveTempStepDeci || diffI(cur.gpuTemp, prev.gpuTemp) >= o.LiveTempStepDeci))
}

// maybeLive отправляет живое обновление, если оно заметно и не слишком часто (горутина чтения стрима).
func (c *conn) maybeLive(now time.Time) {
	l := c.svc.o.Live
	if l == nil {
		return
	}
	c.mu.Lock()
	st := c.st
	due := c.svc.o.Health.liveDue(c.live, st, now)
	if due {
		c.live = markOf(st, now)
	}
	c.mu.Unlock()
	if due {
		c.svc.livePublished.Add(1)
		l.LiveChanged(st)
	}
}

// observeHealth — проблемы по новому здоровью (горутина чтения стрима; журнал — только переходы).
func (c *conn) observeHealth(ctx context.Context, h *presence.Health, now time.Time) {
	if c.monitor == nil || h == nil {
		return
	}
	problems, changed := c.monitor.Observe(ctx, h, now)
	if changed {
		c.mu.Lock()
		c.st.Problems = problems
		c.mu.Unlock()
	}
}

// wantsPing — пора мерить время ответа агента (тикер инстанса). Неотвеченный Ping старше интервала забывается.
func (c *conn) wantsPing(now time.Time, every time.Duration) bool {
	if every <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return now.Sub(c.pingSent) >= every
}

// sendPing ставит мгновенный Ping (agent.proto: агент отвечает CommandResult SUCCEEDED; заодно сверяет часы).
func (c *conn) sendPing(now time.Time) {
	id := uuid.NewString()
	c.mu.Lock()
	c.pingID, c.pingSent = id, now
	c.mu.Unlock()
	c.enqueue(cmdMsg(&agentv1.Command{CommandId: id, IssuedAt: timestamppb.New(now),
		Kind: &agentv1.Command_Ping{Ping: &agentv1.Ping{ServerTime: timestamppb.New(now)}}}))
}

// pingResult — ответ на наш Ping: время ответа в снимок (уйдёт в Valkey с ближайшей записью присутствия).
func (c *conn) pingResult(res *agentv1.CommandResult, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pingID == "" || res.GetCommandId() != c.pingID {
		return false
	}
	ms := max(now.Sub(c.pingSent).Milliseconds(), 1)
	c.st.RTTMillis = uint32(min(ms, 1<<31)) // #nosec G115 -- ограничено
	c.pingID = ""
	return true
}
