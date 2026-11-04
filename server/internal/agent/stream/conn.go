package stream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// eventDedupTTL — сколько помнить event_id (дедупликация повторов после reconnect).
const eventDedupTTL = 24 * time.Hour

type inflight struct {
	cmd      *agentv1.Command
	sentAt   time.Time
	attempts int
	accepted bool
}

// maxQueued — сколько сообщений может ждать отправки; больше — агент не читает стрим, соединение закрывается.
const maxQueued = 4096

// conn — один открытый стрим ПК на этом инстансе.
//
// Порядок: всё, что уходит агенту после Welcome, проходит через очередь c.queue (срез под c.mu), которую
// разбирает одна горутина (обработчик gRPC). Учёт seq (prepareLocked) и постановка в очередь делаются под
// одной блокировкой, поэтому долговременные команды попадают в стрим строго по возрастанию seq, откуда бы
// они ни пришли (NATS, досылка из Postgres, повтор после AckTimeout).
type conn struct {
	svc  *Service
	id   identity.Identity
	own  presence.Owner
	sig  chan struct{}
	pk   chan struct{}
	done chan struct{}

	closeOnce sync.Once
	closeErr  error

	tracker *presence.StateTracker
	caps    presence.Capabilities
	// monitor — проблемы здоровья (nil — выключено); только горутина чтения.
	monitor *health.Monitor

	mu           sync.Mutex
	queue        []*agentv1.ServerMessage
	st           presence.State
	lastStatusAt time.Time
	lastRefresh  time.Time
	maxSent      uint64
	resync       bool
	rebalanced   bool // уже попросили переподключиться ради перебалансировки (rebalance.go)
	inflight     map[string]*inflight
	live         liveMark  // последнее живое обновление карты (health.go)
	pingID       string    // command_id неотвеченного Ping
	pingSent     time.Time // когда отправлен последний Ping
	hourlyAt     time.Time // последний Status, учтённый в почасовой истории (health.go recordHourly)
}

func (c *conn) close(err error) {
	c.closeOnce.Do(func() {
		c.closeErr = err
		close(c.done)
	})
}

func (c *conn) poke() {
	select {
	case c.pk <- struct{}{}:
	default:
	}
}

func (c *conn) signal() {
	select {
	case c.sig <- struct{}{}:
	default:
	}
}

// pushLocked добавляет сообщение в очередь (под c.mu).
func (c *conn) pushLocked(m *agentv1.ServerMessage) bool {
	if len(c.queue) >= maxQueued {
		return false
	}
	c.queue = append(c.queue, m)
	return true
}

// enqueue ставит сообщение в очередь отправки; false — стрим закрыт или переполнен.
func (c *conn) enqueue(m *agentv1.ServerMessage) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	c.mu.Lock()
	ok := c.pushLocked(m)
	c.mu.Unlock()
	if !ok {
		c.close(statusErr(codes.ResourceExhausted, agentv1.ErrorCode_ERROR_CODE_UNSPECIFIED, "agent does not read the stream"))
		return false
	}
	c.signal()
	return true
}

func (c *conn) takeQueue() []*agentv1.ServerMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.queue
	c.queue = nil
	return q
}

func (c *conn) needsRefresh(now time.Time, every time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return now.Sub(c.lastRefresh) >= every
}

func (c *conn) refreshed(now time.Time) {
	c.mu.Lock()
	if now.After(c.lastRefresh) {
		c.lastRefresh = now
	}
	c.mu.Unlock()
}

func (c *conn) wantsPoke(now time.Time, ackTimeout time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resync {
		return true
	}
	for _, f := range c.inflight {
		if !f.accepted && now.Sub(f.sentAt) >= ackTimeout {
			return true
		}
	}
	return false
}

func cmdMsg(cmd *agentv1.Command) *agentv1.ServerMessage {
	return &agentv1.ServerMessage{Payload: &agentv1.ServerMessage_Command{Command: cmd}}
}

// prepareLocked (под c.mu) решает, отправлять ли команду, и ведёт учёт seq. fromNATS = true — команда пришла
// не из Postgres: при пропуске seq (N+2 раньше N+1) — досылка из Postgres по порядку (resync).
// unsupported = true — долговременная команда, которую агент не умеет: её надо отметить FAILED.
func (c *conn) prepareLocked(cmd *agentv1.Command, fromNATS bool, now time.Time) (send, unsupported bool) {
	if e := cmd.GetExpiresAt(); e != nil && !e.AsTime().After(now) {
		return false, false
	}
	kind := commands.KindOf(cmd)
	if kind == "" {
		return false, false
	}
	supported := commands.Supported(kind, c.caps)
	if cmd.GetSeq() == 0 {
		return supported, false
	}
	switch {
	case cmd.GetSeq() <= c.maxSent:
		return false, false
	case fromNATS && cmd.GetSeq() > c.maxSent+1:
		c.resync = true
		return false, false
	}
	c.maxSent = cmd.GetSeq()
	if !supported {
		return false, true
	}
	c.inflight[cmd.GetCommandId()] = &inflight{cmd: cmd, sentAt: now, attempts: 1}
	return true, false
}

// enqueueCommands ставит команды в очередь в данном порядке (учёт seq — под той же блокировкой).
func (c *conn) enqueueCommands(ctx context.Context, cmds []*agentv1.Command, fromNATS bool) {
	now := c.svc.o.Now()
	var unsupported []*agentv1.Command
	overflow, queued, resync := false, false, false
	c.mu.Lock()
	for _, cmd := range cmds {
		send, unsup := c.prepareLocked(cmd, fromNATS, now)
		if unsup {
			unsupported = append(unsupported, cmd)
		}
		if !send {
			continue
		}
		if !c.pushLocked(cmdMsg(cmd)) {
			overflow = true
			break
		}
		queued = true
	}
	resync = c.resync
	c.mu.Unlock()
	if overflow {
		c.close(statusErr(codes.ResourceExhausted, agentv1.ErrorCode_ERROR_CODE_UNSPECIFIED, "agent does not read the stream"))
		return
	}
	if queued {
		c.signal()
	}
	if resync && fromNATS {
		c.poke()
	}
	for _, cmd := range unsupported {
		// Агент не умеет — не отправляем (как ComputerPower::carriesOut() по версии агента в Laravel).
		res := &agentv1.CommandResult{CommandId: cmd.GetCommandId(), Seq: cmd.GetSeq(),
			Status: agentv1.CommandStatus_COMMAND_STATUS_FAILED, ErrorCode: agentv1.ErrorCode_ERROR_CODE_COMMAND_UNSUPPORTED}
		if _, err := c.svc.queue.Record(ctx, c.id.ClubID, c.id.ComputerID, res, now.UTC()); err != nil {
			c.svc.o.Log.Warn("agent command result not recorded", "computer_id", c.id.ComputerID, "err", err)
		}
		if c.svc.o.Results != nil {
			c.svc.o.Results.CommandResult(ctx, c.id, commands.KindOf(cmd), res)
		}
	}
}

// deliverAsync — команда из NATS (горутина подписки).
func (c *conn) deliverAsync(cmd *agentv1.Command) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.enqueueCommands(ctx, []*agentv1.Command{cmd}, true)
}

// Connect — стрим службы агента. Первое сообщение — Hello (не позже HelloTimeout).
func (s *Service) Connect(st agentv1.AgentService_ConnectServer) error {
	ctx := st.Context()
	id, ok := identity.FromContext(ctx)
	if !ok || id.ClubID <= 0 || id.ComputerID <= 0 {
		return status.Error(codes.Unauthenticated, "client certificate is required")
	}
	if s.closing.Load() {
		return statusErr(codes.Unavailable, agentv1.ErrorCode_ERROR_CODE_UNSPECIFIED, "server is shutting down")
	}

	helloCh := make(chan *agentv1.AgentMessage, 1)
	recvErr := make(chan error, 1)
	connCh := make(chan *conn, 1)
	defer close(connCh)
	go func() {
		m, err := st.Recv()
		if err != nil {
			recvErr <- err
			return
		}
		helloCh <- m
		c, ok := <-connCh
		if !ok {
			return
		}
		c.recvLoop(st)
	}()

	timer := time.NewTimer(s.o.HelloTimeout)
	defer timer.Stop()
	var hello *agentv1.Hello
	select {
	case m := <-helloCh:
		hello = m.GetHello()
		if hello == nil {
			return validationErr("hello", "the first message must be Hello")
		}
	case err := <-recvErr:
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	case <-timer.C:
		return validationErr("hello", "Hello was not received in time")
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := validateHello(hello); err != nil {
		return err
	}

	// Владение в Valkey — ДО чтения очереди команд и записи в Postgres (см. presence.Sweeper.markOffline и
	// commands.Sender.Send: так ни одна команда и ни один офлайн не теряются в гонке с reconnect).
	own, prev, hadPrev, err := s.store.Acquire(ctx, id.ClubID, id.ComputerID, s.o.Instance)
	if err != nil {
		s.o.Log.Error("agent hello: acquire presence", "computer_id", id.ComputerID, "err", err)
		return statusErr(codes.Unavailable, agentv1.ErrorCode_ERROR_CODE_INTERNAL, "presence unavailable")
	}
	// Карточка (и последний seq) — после взятия владения.
	row, err := s.rec.Load(ctx, id.ClubID, id.ComputerID)
	if err != nil {
		relCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, _ = s.store.Release(relCtx, own, presence.State{ClubID: id.ClubID, ComputerID: id.ComputerID, LastSeenAt: s.o.Now().UTC()}, 0)
		cancel()
		if errors.Is(err, presence.ErrComputerNotFound) {
			return statusErr(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_COMPUTER_NOT_FOUND, "computer not found")
		}
		s.o.Log.Error("agent hello: load computer", "club_id", id.ClubID, "computer_id", id.ComputerID, "err", err)
		return statusErr(codes.Internal, agentv1.ErrorCode_ERROR_CODE_INTERNAL, "internal error")
	}
	now := s.o.Now()
	c := &conn{
		svc: s, id: id, own: own,
		sig:      make(chan struct{}, 1),
		pk:       make(chan struct{}, 1),
		done:     make(chan struct{}),
		caps:     presence.CapabilitiesFrom(hello.GetCapabilities()),
		maxSent:  hello.GetLastCommandSeq(),
		inflight: map[string]*inflight{},
		tracker:  s.rec.Track(id.ClubID, id.ComputerID, row.AgentState),
	}
	if !s.o.Health.Disabled {
		c.monitor = health.NewMonitor(s.o.Health.Thresholds, s.health, id.ClubID, id.ComputerID, s.o.Log)
	}
	c.st = presence.State{
		ClubID: id.ClubID, ComputerID: id.ComputerID, Online: true, Instance: s.o.Instance, Gen: own.Gen,
		ProtocolVersion: hello.GetProtocolVersion(), AgentVersion: hello.GetAgentVersion(), Capabilities: c.caps,
		RemoteIP: remoteIP(ctx), LocalIP: hello.GetDevice().GetLocalIp(), ConnectedAt: now.UTC(), LastSeenAt: now.UTC(),
	}
	if hs := hello.GetStatus(); hs != nil {
		c.dropInvalidHealth(hs)
		c.applyStatus(hs, now)
		c.tracker.Observe(c.st.AgentState)
		c.observeHealth(ctx, c.st.Health, now)
		c.recordHourly(ctx, hs, nil, c.st, now)
	}
	// Подключение и так перечитывает ПК на карте (computer.state) — живое обновление считается отправленным.
	c.live = markOf(c.st, now)
	// Первый Ping — через PingInterval после подключения (не добавлять сообщений в шторм переподключений).
	c.pingSent = now
	s.attach(c)
	defer c.finish()
	if hadPrev && prev.Instance != s.o.Instance {
		s.kickRemote(prev.Instance, id, own.Gen)
	}

	freshBoot, err := s.rec.RecordHello(ctx, row, presence.HelloFacts{
		AgentVersion: hello.GetAgentVersion(), BootID: hello.GetBootId(), RemoteIP: c.st.RemoteIP, Device: hello.GetDevice()})
	if err != nil {
		s.o.Log.Error("agent hello: record", "computer_id", id.ComputerID, "err", err)
		return statusErr(codes.Internal, agentv1.ErrorCode_ERROR_CODE_INTERNAL, "internal error")
	}
	if h := hello.GetDevice().GetHostname(); h != "" {
		row.DeviceName = &h // уже записано RecordHello (displayName в Welcome)
	}
	if freshBoot && s.o.Events != nil {
		if err := s.o.Events.FreshBoot(ctx, id); err != nil {
			s.o.Log.Warn("agent fresh boot hook failed", "computer_id", id.ComputerID, "err", err)
		} else if row, err = s.rec.Load(ctx, id.ClubID, id.ComputerID); err != nil {
			return statusErr(codes.Internal, agentv1.ErrorCode_ERROR_CODE_INTERNAL, "internal error")
		}
	}

	latest := uint64(max(row.LastCommandSeq, 0)) // #nosec G115 -- неотрицательное после max
	if hello.GetLastCommandSeq() > latest {
		if err := s.queue.RaiseSeq(ctx, id.ClubID, id.ComputerID, hello.GetLastCommandSeq()); err != nil {
			return statusErr(codes.Internal, agentv1.ErrorCode_ERROR_CODE_INTERNAL, "internal error")
		}
		latest = hello.GetLastCommandSeq()
	}

	w := s.welcome(row, latest, now)
	if s.o.Welcome != nil {
		if err := s.o.Welcome.CompleteWelcome(ctx, id, w); err != nil {
			s.o.Log.Error("agent welcome provider failed", "computer_id", id.ComputerID, "err", err)
			return statusErr(codes.Internal, agentv1.ErrorCode_ERROR_CODE_INTERNAL, "internal error")
		}
	}
	if err := st.Send(&agentv1.ServerMessage{Payload: &agentv1.ServerMessage_Welcome{Welcome: w}}); err != nil {
		return err
	}
	if err := c.writePresence(ctx); err != nil && !errors.Is(err, presence.ErrNotOwner) {
		s.o.Log.Warn("agent presence write failed", "computer_id", id.ComputerID, "err", err)
	}
	if s.o.Presence != nil {
		s.o.Presence.PresenceChanged(ctx, c.state())
	}

	// Досылка: долговременные с seq > last_command_seq, затем последние «заменяемые» мгновенные.
	if latest > hello.GetLastCommandSeq() {
		// Есть команды новее выполненных агентом — досылка из Postgres. Обычный reconnect (всё выполнено)
		// обходится без этого запроса.
		c.resyncFromQueue(ctx)
	}
	if co, err := s.sender.Coalesced(ctx, id.ComputerID); err == nil {
		c.enqueueCommands(ctx, co, false)
	}

	connCh <- c
	return c.sendLoop(ctx, st)
}

func remoteIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return ""
	}
	return host
}

func (s *Service) kickRemote(instance string, id identity.Identity, gen int64) {
	env := commands.Envelope{Type: commands.EnvelopeKick, ClubID: id.ClubID, ComputerID: id.ComputerID, Gen: gen}
	b, err := json.Marshal(env)
	if err == nil {
		err = s.o.NATS.Publish(commands.InstanceSubject(s.o.SubjectPrefix, instance), b)
	}
	if err != nil {
		s.o.Log.Warn("agent kick not published", "computer_id", id.ComputerID, "instance", instance, "err", err)
	}
}

// welcome — базовый снимок из карточки ПК.
func (s *Service) welcome(row store.GetAgentComputerRow, latest uint64, now time.Time) *agentv1.Welcome {
	w := &agentv1.Welcome{
		ComputerId: row.ID,
		ClubId:     row.ClubID,
		Computer: &agentv1.ComputerRef{
			ComputerId: row.ID, Number: strconv.FormatInt(row.Number, 10), DisplayName: displayName(row), ZoneName: row.ZoneName,
		},
		ServerTime:         timestamppb.New(now),
		StatusInterval:     durationpb.New(s.o.StatusInterval),
		MinProtocolVersion: MinProtocolVersion,
		MaxProtocolVersion: MaxProtocolVersion,
		LatestCommandSeq:   latest,
		Config: &agentv1.ClubConfig{
			ClubName:     row.ClubName,
			StaffMinutes: uint32(max(row.AgentStaffMinutes, 0)), // #nosec G115 -- неотрицательное после max
		},
	}
	if row.ClubLocale != nil {
		w.Config.DefaultLocale = *row.ClubLocale
	}
	w.Session = activeSession(row, now)
	return w
}

// displayName — Laravel: app/Models/ClubComputer.php displayName(): display_name ?: device_name ?: sprintf('PC-%02d', number).
func displayName(row store.GetAgentComputerRow) string {
	if row.DisplayName != nil && *row.DisplayName != "" {
		return *row.DisplayName
	}
	if row.DeviceName != nil && *row.DeviceName != "" {
		return *row.DeviceName
	}
	n := strconv.FormatInt(row.Number, 10)
	if len(n) < 2 {
		n = "0" + n
	}
	return "PC-" + n
}

// activeSession — Laravel: app/Models/ClubComputer.php hasPaidSession() + ClubComputerSessions::sessionPayload():
// сессия есть, если current_session_id задан, есть клиент или это гостевая сессия с кассы, и время не вышло.
func activeSession(row store.GetAgentComputerRow, now time.Time) *agentv1.ActiveSession {
	if row.CurrentSessionID == nil || (row.CurrentSessionUserID == nil && !row.SessionGuest) ||
		row.SessionEndsAt == nil || !row.SessionEndsAt.After(now) {
		return nil
	}
	s := &agentv1.ActiveSession{SessionId: *row.CurrentSessionID, EndsAt: timestamppb.New(*row.SessionEndsAt), Guest: row.SessionGuest}
	if row.CurrentSessionUserID != nil {
		s.UserId = *row.CurrentSessionUserID
	}
	return s
}

func (c *conn) state() presence.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

// applyStatus обновляет снимок (под c.mu или до публикации соединения). significant — изменилось не только время/загрузка.
// Status без load или health прежние значения не стирает (agent.proto, Status.health).
func (c *conn) applyStatus(s *agentv1.Status, now time.Time) (significant bool) {
	prev := c.st
	c.st.AgentState = presence.StateName(s.GetState())
	c.st.SessionID = s.GetSessionId()
	c.st.SignedInUserID = s.GetSignedInUserId()
	if s.GetLoad() != nil {
		c.st.Load = presence.LoadFrom(s.GetLoad())
		c.st.HasLoad = true
	}
	if s.GetHealth() != nil {
		c.st.Health = presence.HealthFrom(s.GetHealth(), now)
	}
	c.st.LastSeenAt = now.UTC()
	return c.st.Significant(prev)
}

// dropInvalidHealth отбрасывает неверное здоровье: Status принимается без него, поток не закрывается.
func (c *conn) dropInvalidHealth(s *agentv1.Status) {
	if err := validateHealth(s.GetHealth()); err != nil {
		c.svc.o.Log.Debug("agent health ignored", "computer_id", c.id.ComputerID, "err", err)
		c.svc.healthDropped.Add(1)
		s.Health = nil
	}
}

func (c *conn) writePresence(ctx context.Context) error {
	st := c.state()
	err := c.svc.store.Refresh(ctx, presence.RefreshItem{Owner: c.own, State: &st, ClubID: c.id.ClubID, ComputerID: c.id.ComputerID})
	if errors.Is(err, presence.ErrNotOwner) {
		c.close(replacedErr())
		return err
	}
	if err == nil {
		c.refreshed(c.svc.o.Now())
	}
	return err
}

// resyncFromQueue ставит в очередь неотправленные долговременные команды из Postgres по порядку seq.
func (c *conn) resyncFromQueue(ctx context.Context) {
	c.mu.Lock()
	after := c.maxSent
	c.resync = false
	c.mu.Unlock()
	pending, err := c.svc.queue.Pending(ctx, c.id.ClubID, c.id.ComputerID, after, c.svc.o.Now().UTC())
	if err != nil {
		if ctx.Err() == nil {
			c.svc.o.Log.Error("agent pending commands", "computer_id", c.id.ComputerID, "err", err)
		}
		c.mu.Lock()
		c.resync = true
		c.mu.Unlock()
		return
	}
	c.enqueueCommands(ctx, pending, false)
}

// sendLoop — единственная горутина, пишущая в стрим после Welcome.
func (c *conn) sendLoop(ctx context.Context, st agentv1.AgentService_ConnectServer) error {
	for {
		if err := c.flush(st); err != nil {
			return err
		}
		select {
		case <-c.sig:
		case <-c.pk:
			c.onPoke(ctx)
		case <-c.done:
			return c.closeErr
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		}
	}
}

func (c *conn) flush(st agentv1.AgentService_ConnectServer) error {
	for _, m := range c.takeQueue() {
		if err := st.Send(m); err != nil {
			return err
		}
	}
	return nil
}

// onPoke — повтор неподтверждённых команд после AckTimeout и досылка после пропуска seq.
func (c *conn) onPoke(ctx context.Context) {
	now := c.svc.o.Now()
	var resend []*agentv1.Command
	c.mu.Lock()
	resync := c.resync
	for cid, f := range c.inflight {
		if f.accepted || now.Sub(f.sentAt) < c.svc.o.AckTimeout {
			continue
		}
		if e := f.cmd.GetExpiresAt(); (e != nil && !e.AsTime().After(now)) || f.attempts >= maxSendAttempts {
			delete(c.inflight, cid)
			continue
		}
		f.attempts++
		f.sentAt = now
		resend = append(resend, f.cmd)
	}
	sortBySeq(resend)
	for _, cmd := range resend {
		// Повтор того же command_id безопасен: агент не выполняет его второй раз (agent.proto п. 5).
		c.pushLocked(cmdMsg(cmd))
	}
	c.mu.Unlock()
	if resync {
		c.resyncFromQueue(ctx)
	}
}

func sortBySeq(cmds []*agentv1.Command) {
	for i := 1; i < len(cmds); i++ {
		for j := i; j > 0 && cmds[j].GetSeq() < cmds[j-1].GetSeq(); j-- {
			cmds[j], cmds[j-1] = cmds[j-1], cmds[j]
		}
	}
}

// recvLoop — чтение сообщений агента после Hello (отдельная горутина).
func (c *conn) recvLoop(st agentv1.AgentService_ConnectServer) {
	ctx := st.Context()
	for {
		m, err := st.Recv()
		if err != nil {
			c.close(nil) // EOF / отмена: обработчик завершит стрим
			return
		}
		switch p := m.GetPayload().(type) {
		case *agentv1.AgentMessage_Hello:
			c.close(validationErr("hello", "Hello must be sent once"))
			return
		case *agentv1.AgentMessage_Status:
			c.onStatus(ctx, p.Status)
		case *agentv1.AgentMessage_Event:
			c.onEvent(ctx, p.Event)
		case *agentv1.AgentMessage_CommandResult:
			c.onResult(ctx, p.CommandResult)
		}
	}
}

func (c *conn) onStatus(ctx context.Context, s *agentv1.Status) {
	if err := validateStatus(s); err != nil {
		c.svc.o.Log.Debug("agent status ignored", "computer_id", c.id.ComputerID, "err", err)
		return
	}
	c.dropInvalidHealth(s)
	now := c.svc.o.Now()
	c.mu.Lock()
	prev := c.st
	significant := c.applyStatus(s, now)
	// Смена периферии или SMART (агент шлёт её сразу, agent.proto Status.health) не отбрасывается ограничением частоты:
	// она нужна панели сразу, а не со следующим плановым Status (S7-HA-2).
	if !significant && !presence.HealthChanged(prev.Health, c.st.Health) && now.Sub(c.lastStatusAt) < c.svc.o.MinStatusGap {
		// Не чаще 1 Status в 2 с (agent.proto п. 7): лишний отбрасывается, стрим не закрывается.
		c.st = prev
		c.mu.Unlock()
		return
	}
	c.lastStatusAt = now
	fresh, cur := c.st.Health, c.st
	c.mu.Unlock()
	if s.GetHealth() != nil {
		c.observeHealth(ctx, fresh, now)
	}
	c.recordHourly(ctx, s, prev.Health, cur, now)

	if err := c.writePresence(ctx); err != nil && !errors.Is(err, presence.ErrNotOwner) {
		c.svc.o.Log.Warn("agent presence write failed", "computer_id", c.id.ComputerID, "err", err)
	}
	c.tracker.Observe(presence.StateName(s.GetState()))
	if significant && c.svc.o.Presence != nil {
		c.svc.o.Presence.PresenceChanged(ctx, c.state())
		c.mu.Lock()
		c.live = markOf(c.st, now) // computer.state и так перечитает ПК на карте
		c.mu.Unlock()
	} else {
		c.maybeLive(now)
	}
}

func (c *conn) eventKey(eventID string) string {
	return c.svc.store.Prefix() + "evt:" + strconv.FormatInt(c.id.ComputerID, 10) + ":" + eventID
}

func (c *conn) ack(eventID string) {
	c.enqueue(&agentv1.ServerMessage{Payload: &agentv1.ServerMessage_EventAck{EventAck: &agentv1.EventAck{EventId: eventID}}})
}

func (c *conn) onEvent(ctx context.Context, ev *agentv1.AgentEvent) {
	if err := validateEvent(ev); err != nil {
		c.svc.o.Log.Warn("agent event rejected", "computer_id", c.id.ComputerID, "err", err)
		if ev.GetEventId() != "" {
			c.ack(ev.GetEventId()) // иначе испорченное событие навсегда застрянет в журнале агента
		}
		return
	}
	vc := c.svc.store.Client()
	key := c.eventKey(ev.GetEventId())
	if n, err := vc.Do(ctx, vc.B().Exists().Key(key).Build()).AsInt64(); err == nil && n > 0 {
		c.ack(ev.GetEventId())
		return
	}
	if h := c.svc.o.Events; h != nil {
		hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := h.HandleEvent(hctx, c.id, ev)
		cancel()
		if err != nil {
			c.svc.o.Log.Warn("agent event not handled", "computer_id", c.id.ComputerID, "event_id", ev.GetEventId(), "err", err)
			return
		}
	}
	if err := vc.Do(ctx, vc.B().Set().Key(key).Value("1").Ex(eventDedupTTL).Build()).Error(); err != nil {
		c.svc.o.Log.Warn("agent event dedup key not stored", "computer_id", c.id.ComputerID, "err", err)
	}
	c.ack(ev.GetEventId())
}

func (c *conn) onResult(ctx context.Context, res *agentv1.CommandResult) {
	if !validUUID(res.GetCommandId()) {
		return
	}
	if res.GetSeq() == 0 && c.pingResult(res, c.svc.o.Now()) {
		return // ответ на Ping измерения времени ответа (health.go)
	}
	var kind commands.Kind
	final := res.GetStatus() != agentv1.CommandStatus_COMMAND_STATUS_ACCEPTED
	c.mu.Lock()
	if f, ok := c.inflight[res.GetCommandId()]; ok {
		kind = commands.KindOf(f.cmd)
		if final {
			delete(c.inflight, res.GetCommandId())
		} else {
			f.accepted = true
		}
	}
	c.mu.Unlock()
	if res.GetSeq() > 0 {
		if _, err := c.svc.queue.Record(ctx, c.id.ClubID, c.id.ComputerID, res, c.svc.o.Now().UTC()); err != nil {
			c.svc.o.Log.Warn("agent command result not recorded", "computer_id", c.id.ComputerID, "err", err)
		}
	}
	if c.svc.o.Results != nil {
		c.svc.o.Results.CommandResult(ctx, c.id, kind, res)
	}
}

// finish — стрим закрыт: снять регистрацию, дописать состояние, отдать ПК на ожидание переподключения.
func (c *conn) finish() {
	c.close(nil)
	c.svc.detach(c)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.tracker.Close(ctx)
	st := c.state()
	st.LastSeenAt = c.svc.o.Now().UTC()
	released, err := c.svc.store.Release(ctx, c.own, st, c.svc.o.OfflineGrace)
	if err != nil {
		c.svc.o.Log.Warn("agent presence release failed", "computer_id", c.id.ComputerID, "err", err)
	}
	if released && c.svc.o.Presence != nil {
		st.Online = false
		c.svc.o.Presence.PresenceChanged(ctx, st)
	}
}
