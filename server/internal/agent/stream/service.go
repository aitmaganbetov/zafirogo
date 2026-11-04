// Package stream — AgentService.Connect: двунаправленный стрим службы агента ПК (proto/agent/v1/agent.proto).
//
// Один стрим на ПК во всём кластере: новое соединение берёт владение в Valkey (presence.Store.Acquire) с
// бóльшим поколением, прежнее закрывается (ABORTED "replaced") — локально или через NATS на инстансе, где оно
// открыто. Команды приходят на инстанс через NATS (subject инстанса, commands.Envelope) и отправляются в
// стрим; долговременные перед этим уже лежат в Postgres и досылаются после reconnect по last_command_seq.
//
// Горутины на стрим: обработчик gRPC (отправка) + чтение. Продление присутствия в Valkey и таймауты ack
// обслуживает один тикер инстанса (пачкой), а не таймер на каждый ПК.
package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
)

// Умолчания (docs/agent-protocol.md, agent.proto п. 7–9).
const (
	DefaultPresenceTTL    = 90 * time.Second // ClubComputer::HEARTBEAT_TIMEOUT_SECONDS = 90 в Laravel
	DefaultOfflineGrace   = 15 * time.Second
	DefaultHelloTimeout   = 10 * time.Second
	DefaultStatusInterval = 60 * time.Second
	DefaultAckTimeout     = 30 * time.Second
	DefaultMinStatusGap   = 2 * time.Second
	DefaultTickEvery      = 5 * time.Second
	maxSendAttempts       = 3
)

// Options — зависимости и настройки сервиса.
type Options struct {
	// Instance — id этого инстанса сервера (уникален в кластере, [A-Za-z0-9_-]{1,64}).
	Instance string
	// Pool — пул логина zafiro_app (тенантные транзакции).
	Pool   db.Beginner
	Valkey valkey.Client
	NATS   *nats.Conn
	// KeyPrefix — префикс ключей Valkey (пусто — presence.DefaultPrefix).
	KeyPrefix string
	// SubjectPrefix — префикс subject NATS (пусто — commands.DefaultSubjectPrefix).
	SubjectPrefix string
	// UpdateKeys — открытые ключи Ed25519 подписи обновлений агента (commands.SenderOptions.UpdateKeys).
	UpdateKeys commands.UpdateKeys

	PresenceTTL    time.Duration
	OfflineGrace   time.Duration
	HelloTimeout   time.Duration
	StatusInterval time.Duration
	StateDebounce  time.Duration
	AckTimeout     time.Duration
	MinStatusGap   time.Duration
	TickEvery      time.Duration

	Events   EventHandler
	Welcome  WelcomeProvider
	Results  ResultHandler
	Presence PresenceListener
	// Live — живые обновления загрузки и здоровья для карты клуба (без outbox, с ограничением частоты, health.go).
	Live LiveListener

	// Health — проблемы здоровья ПК, живые обновления и пинг (Этап 7, docs/agent-protocol.md §16).
	Health HealthOptions

	// Rebalance — автоматическая перебалансировка потоков между инстансами (rebalance.go, S6-O4-12).
	Rebalance RebalanceOptions

	Now func() time.Time
	Log *slog.Logger
}

func (o *Options) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	set(&o.PresenceTTL, DefaultPresenceTTL)
	set(&o.OfflineGrace, DefaultOfflineGrace)
	set(&o.HelloTimeout, DefaultHelloTimeout)
	set(&o.StatusInterval, DefaultStatusInterval)
	set(&o.StateDebounce, presence.DefaultStateDebounce)
	set(&o.AckTimeout, DefaultAckTimeout)
	set(&o.MinStatusGap, DefaultMinStatusGap)
	set(&o.TickEvery, DefaultTickEvery)
	o.Rebalance.defaults()
	o.Health.defaults()
	if o.SubjectPrefix == "" {
		o.SubjectPrefix = commands.DefaultSubjectPrefix
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
}

// Service — реализация AgentService.Connect. GetTranslations и VerifyStaffPin — Unimplemented
// (их реализуют другие модули; оркестратор собирает AgentServiceServer встраиванием *Service).
type Service struct {
	agentv1.UnimplementedAgentServiceServer

	o      Options
	store  *presence.Store
	rec    *presence.Recorder
	sender *commands.Sender
	queue  *commands.Queue
	health *health.Journal
	sub    *nats.Subscription

	mu    sync.RWMutex
	conns map[int64]*conn

	closing    atomic.Bool
	rebalanced atomic.Uint64 // ПК, которых попросили переподключиться ради перебалансировки
	// livePublished — живых обновлений карты отправлено; healthDropped — Status с отброшенным неверным health.
	livePublished, healthDropped atomic.Uint64
	stop                         chan struct{}
	wg                           sync.WaitGroup
}

// New создаёт сервис, подписывается на subject инстанса в NATS и запускает тикер присутствия.
func New(o Options) (*Service, error) {
	o.defaults()
	if !commands.ValidInstance(o.Instance) {
		return nil, fmt.Errorf("stream: invalid instance id %q", o.Instance)
	}
	if o.Pool == nil || o.Valkey == nil || o.NATS == nil {
		return nil, errors.New("stream: Pool, Valkey and NATS are required")
	}
	st := presence.NewStore(o.Valkey, o.KeyPrefix, o.PresenceTTL)
	s := &Service{
		o:     o,
		store: st,
		rec:   presence.NewRecorder(o.Pool, o.StateDebounce, o.Now, o.Log),
		sender: commands.NewSender(commands.SenderOptions{
			Pool: o.Pool, Presence: st, NATS: o.NATS, SubjectPrefix: o.SubjectPrefix, UpdateKeys: o.UpdateKeys,
			Now: o.Now, Log: o.Log,
		}),
		health: health.NewJournal(o.Pool),
		conns:  map[int64]*conn{},
		stop:   make(chan struct{}),
	}
	s.queue = s.sender.Queue()
	sub, err := o.NATS.Subscribe(commands.InstanceSubject(o.SubjectPrefix, o.Instance), s.onEnvelope)
	if err != nil {
		return nil, fmt.Errorf("stream: subscribe: %w", err)
	}
	// Без лимита в сообщениях: команды редки, но при массовой рассылке клубу не терять их в буфере подписки.
	_ = sub.SetPendingLimits(-1, 64<<20)
	s.sub = sub
	s.wg.Add(1)
	go s.tickLoop()
	return s, nil
}

// Register регистрирует сервис в gRPC-сервере (server/internal/grpc: grpc.NewServer(opts, register)).
func Register(gs *grpc.Server, svc *Service) { agentv1.RegisterAgentServiceServer(gs, svc) }

// Commands — Commands.Send для остального сервера.
func (s *Service) Commands() *commands.Sender { return s.sender }

// Presence — Presence.Get/List для остального сервера (карта клуба, WebSocket).
func (s *Service) Presence() *presence.Store { return s.store }

// Recorder — запись изменений в Postgres (метрика Writes()).
func (s *Service) Recorder() *presence.Recorder { return s.rec }

// Notifier — хук для billing (SessionStarted/SessionEnded/BalanceChanged после коммита).
func (s *Service) Notifier() *commands.Notifier {
	return &commands.Notifier{Sender: s.sender, Presence: s.store}
}

// NewSweeper — фоновая проверка присутствия; platformPool — пул логина zafiro_platform.
func (s *Service) NewSweeper(platformPool db.Beginner) *presence.Sweeper {
	return &presence.Sweeper{Store: s.store, Pool: platformPool, Token: s.o.Instance, Now: s.o.Now, Log: s.o.Log}
}

// HealthJournal — история проблем здоровья (метрика Writes()).
func (s *Service) HealthJournal() *health.Journal { return s.health }

// LivePublished — сколько живых обновлений загрузки/здоровья отправил этот инстанс (метрика, нагрузочный тест).
func (s *Service) LivePublished() uint64 { return s.livePublished.Load() }

// HealthDropped — сколько Status пришло с неверным health (health отброшен, Status принят).
func (s *Service) HealthDropped() uint64 { return s.healthDropped.Load() }

// LocalStreams — сколько стримов открыто на этом инстансе.
func (s *Service) LocalStreams() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.conns)
}

// Kick закрывает стрим ПК, где бы он ни был открыт (отзыв сертификата: codes.Unauthenticated +
// ERROR_CODE_CERTIFICATE_REVOKED). Новые подключения отклоняет проверка сертификата при рукопожатии.
// certSerial — серийный номер отозванного сертификата: поток закрывается, только если открыт им (поток на
// продлённом сертификате переживает отзыв прежнего, superseded); пусто — поток ПК закрывается безусловно.
// «Отключить ПК», «Перерегистрировать», удаление карточки, повторный Enroll и отзыв ключа образа отзывают все
// неотозванные сертификаты ПК, поэтому сертификат открытого потока всегда среди отозванных.
func (s *Service) Kick(ctx context.Context, clubID, computerID int64, certSerial string, code codes.Code, ec agentv1.ErrorCode, reason string) error {
	own, ok, err := s.store.OwnerOf(ctx, computerID)
	if err != nil || !ok {
		return err
	}
	env := commands.Envelope{Type: commands.EnvelopeKick, ClubID: clubID, ComputerID: computerID, CertSerial: certSerial,
		Code: uint32(code), ErrorCode: int32(ec), Reason: reason} // #nosec G115 -- codes.Code < 17
	if own.Instance == s.o.Instance {
		s.handleKick(env)
		return nil
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return s.o.NATS.Publish(commands.InstanceSubject(s.o.SubjectPrefix, own.Instance), b)
}

// Drain просит все ПК этого инстанса переподключиться в случайный момент окна within (плановый
// перезапуск, agent.proto п. 8). Затем вызывающий делает GracefulStop gRPC-сервера и Close.
func (s *Service) Drain(within time.Duration) {
	s.closing.Store(true)
	s.unpublishStreams() // остальные инстансы перестают считать этот живым (перебалансировка)
	msg := &agentv1.ServerMessage{Payload: &agentv1.ServerMessage_Command{Command: &agentv1.Command{
		Kind: &agentv1.Command_Drain{Drain: &agentv1.Drain{ReconnectWithin: durationpb.New(within)}},
	}}}
	for _, c := range s.snapshot() {
		c.enqueue(msg)
	}
}

// Close останавливает тикер и подписку и закрывает оставшиеся стримы (UNAVAILABLE).
func (s *Service) Close() {
	if !s.closing.Swap(true) {
		s.unpublishStreams()
	}
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	if s.sub != nil {
		_ = s.sub.Unsubscribe()
	}
	for _, c := range s.snapshot() {
		c.close(statusErr(codes.Unavailable, agentv1.ErrorCode_ERROR_CODE_UNSPECIFIED, "server is shutting down"))
	}
	s.wg.Wait()
}

func (s *Service) snapshot() []*conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*conn, 0, len(s.conns))
	for _, c := range s.conns {
		out = append(out, c)
	}
	return out
}

func (s *Service) local(computerID int64) *conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conns[computerID]
}

// attach регистрирует соединение; прежнее соединение того же ПК на этом инстансе вытесняется.
func (s *Service) attach(c *conn) {
	s.mu.Lock()
	old := s.conns[c.id.ComputerID]
	s.conns[c.id.ComputerID] = c
	s.mu.Unlock()
	if old != nil && old.own.Gen < c.own.Gen {
		old.close(replacedErr())
	}
}

func (s *Service) detach(c *conn) {
	s.mu.Lock()
	if s.conns[c.id.ComputerID] == c {
		delete(s.conns, c.id.ComputerID)
	}
	s.mu.Unlock()
}

func replacedErr() error {
	return statusErr(codes.Aborted, agentv1.ErrorCode_ERROR_CODE_UNSPECIFIED, "replaced")
}

// onEnvelope — сообщение NATS для этого инстанса (одна горутина подписки).
func (s *Service) onEnvelope(m *nats.Msg) {
	var env commands.Envelope
	if err := json.Unmarshal(m.Data, &env); err != nil {
		s.o.Log.Warn("agent envelope decode failed", "err", err)
		return
	}
	switch env.Type {
	case commands.EnvelopeKick:
		s.handleKick(env)
	case commands.EnvelopeCommand:
		c := s.local(env.ComputerID)
		if c == nil || c.id.ClubID != env.ClubID {
			return // ПК уже не здесь: долговременная команда дождётся Hello в Postgres
		}
		cmd := &agentv1.Command{}
		if err := proto.Unmarshal(env.Command, cmd); err != nil {
			s.o.Log.Warn("agent command decode failed", "err", err)
			return
		}
		c.deliverAsync(cmd)
	}
}

func (s *Service) handleKick(env commands.Envelope) {
	c := s.local(env.ComputerID)
	if c == nil || c.id.ClubID != env.ClubID {
		return
	}
	if env.CertSerial != "" && c.id.CertSerial != env.CertSerial {
		return // отозван другой сертификат ПК (например, прежний после продления) — этот поток не трогаем
	}
	if env.Gen > 0 {
		if c.own.Gen < env.Gen {
			c.close(replacedErr())
		}
		return
	}
	code := codes.Code(env.Code)
	if code == codes.OK {
		code = codes.Unauthenticated
	}
	c.close(statusErr(code, agentv1.ErrorCode(env.ErrorCode), env.Reason))
}

// tickLoop — продление присутствия пачкой и «тычок» соединениям с неподтверждёнными командами.
func (s *Service) tickLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.o.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.tick()
		}
	}
}

func (s *Service) tick() {
	now := s.o.Now()
	refreshEvery := s.o.PresenceTTL / 3
	var items []presence.RefreshItem
	var owners []*conn
	for _, c := range s.snapshot() {
		if c.needsRefresh(now, refreshEvery) {
			items = append(items, presence.RefreshItem{Owner: c.own, ClubID: c.id.ClubID, ComputerID: c.id.ComputerID})
			owners = append(owners, c)
		}
		if c.wantsPoke(now, s.o.AckTimeout) {
			c.poke()
		}
		if c.wantsPing(now, s.o.Health.PingInterval) {
			c.sendPing(now)
		}
	}
	if len(items) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, err := range s.store.RefreshMany(ctx, items) {
		switch {
		case errors.Is(err, presence.ErrNotOwner):
			owners[i].close(replacedErr())
		case err != nil:
			s.o.Log.Warn("agent presence refresh failed", "computer_id", items[i].ComputerID, "err", err)
		default:
			owners[i].refreshed(now)
		}
	}
}
