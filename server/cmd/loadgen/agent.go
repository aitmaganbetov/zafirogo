//go:build loadgen

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
)

// agentTLS — как у агента (docs/agent-protocol.md §12.6): доверие только корню PKI, имя agent.<домен>, TLS ≥ 1.2.
func agentTLS(t *target, cert *tls.Certificate) *tls.Config {
	c := &tls.Config{RootCAs: t.pkiRoots, ServerName: "agent." + stgDomain, MinVersion: tls.VersionTLS12}
	if cert != nil {
		c.Certificates = []tls.Certificate{*cert}
	}
	return c
}

func dialFrom(t *target, club int) func(ctx context.Context, _ string) (net.Conn, error) {
	d := t.dialer(club)
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", net.JoinHostPort(t.lbIP, "8443"))
	}
}

// pcSim — один ПК: поток Connect (Hello → Welcome → Status раз в statusEvery, ответы на команды, Drain,
// переподключение с full jitter как у агента) и, если guest, гость с денежными операциями.
type pcSim struct {
	r     *runner
	cred  pcCred
	cert  tls.Certificate
	guest bool

	mu        sync.Mutex
	conn      *grpc.ClientConn
	connected chan struct{} // закрыт, пока поток открыт (пересоздаётся при переподключении)
	stream    agentv1.AgentService_ConnectClient
	sendMu    sync.Mutex
	state     agentv1.ComputerState
	sessionID string
	userID    int64
	lastSeq   uint64
	startCh   chan string // StartSession от сервера (автобиллинг)
	bootID    string
	isUp      atomic.Bool
	welcomes  int
}

func newPC(r *runner, c pcCred, guest bool) (*pcSim, error) {
	leaf, err := pki.ParseCertificatePEM([]byte(c.CertPEM))
	if err != nil {
		return nil, err
	}
	key, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM))
	if err != nil {
		return nil, err
	}
	key.Leaf = leaf
	return &pcSim{r: r, cred: c, cert: key, guest: guest, state: agentv1.ComputerState_COMPUTER_STATE_IDLE,
		connected: make(chan struct{}), startCh: make(chan string, 4), bootID: uuid.NewString()}, nil
}

// runStream — жизнь потока ПК до отмены ctx.
func (p *pcSim) runStream(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		began := time.Now()
		drainWithin, err := p.session(ctx)
		if ctx.Err() != nil {
			return
		}
		var delay time.Duration
		switch {
		case drainWithin > 0:
			attempt = 0 // переподключение уже выполнено в случайный момент окна Drain
			p.r.counters.drained.Add(1)
		default:
			if time.Since(began) >= 60*time.Second {
				attempt = 0
			}
			attempt++
			// full jitter: random(0, min(60 с, 2^attempt с)) — docs/agent-protocol.md §2.
			maxD := time.Duration(min(60, 1<<min(attempt, 6))) * time.Second
			delay = time.Duration(rndInt64N(int64(maxD)))
			p.r.counters.streamErrors.Add(1)
			if err != nil {
				p.r.noteStreamErr(err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// session — одно соединение: возвращает окно Drain (> 0 — переподключились по Drain) или ошибку.
func (p *pcSim) session(ctx context.Context) (time.Duration, error) {
	t0 := time.Now()
	conn, err := grpc.NewClient("passthrough:///"+p.r.t.lbIP+":8443",
		grpc.WithTransportCredentials(credentials.NewTLS(agentTLS(p.r.t, &p.cert))), grpc.WithNoProxy(),
		grpc.WithContextDialer(dialFrom(p.r.t, p.cred.Club)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
		grpc.WithInitialWindowSize(64<<10), grpc.WithInitialConnWindowSize(64<<10),
		grpc.WithReadBufferSize(16<<10), grpc.WithWriteBufferSize(16<<10))
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := agentv1.NewAgentServiceClient(conn).Connect(sctx)
	if err != nil {
		p.r.stats.rec("agent.connect", t0, err)
		return 0, err
	}
	p.mu.Lock()
	hello := &agentv1.Hello{ProtocolVersion: 1, AgentVersion: "2.0.0", BootId: p.bootID, LastCommandSeq: p.lastSeq, Locale: "ru",
		Capabilities: &agentv1.AgentCapabilities{PowerControl: true, ShowMessage: true},
		Status:       p.statusLocked()}
	if p.welcomes == 0 {
		hello.Device = &agentv1.DeviceInfo{Hostname: fmt.Sprintf("LOAD-%d-%d", p.cred.Club, p.cred.PC), LocalIp: "192.168.1.10",
			AgentVersion: "2.0.0", OsVersion: "Windows 11 Pro", HardwareUuid: fmt.Sprintf("LOAD-%d", p.cred.ComputerID)}
	}
	p.mu.Unlock()
	if err := st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Hello{Hello: hello}}); err != nil {
		p.r.stats.rec("agent.connect", t0, err)
		return 0, err
	}
	first, err := st.Recv()
	if err != nil {
		p.r.stats.rec("agent.connect", t0, err)
		return 0, err
	}
	if first.GetWelcome() == nil {
		err := fmt.Errorf("first message is not Welcome: %T", first.GetPayload())
		p.r.stats.rec("agent.connect", t0, err)
		return 0, err
	}
	p.r.stats.rec("agent.connect", t0, nil)
	p.mu.Lock()
	p.conn, p.stream = conn, st
	p.welcomes++
	if ws := first.GetWelcome().GetSession(); ws != nil && ws.GetSessionId() != "" && p.sessionID == "" {
		p.sessionID = ws.GetSessionId()
	}
	ch := p.connected
	p.mu.Unlock()
	close(ch)
	p.isUp.Store(true)
	p.r.counters.up.Add(1)
	defer func() {
		p.isUp.Store(false)
		p.r.counters.up.Add(-1)
		p.mu.Lock()
		p.conn, p.stream = nil, nil
		p.connected = make(chan struct{})
		p.mu.Unlock()
	}()

	// Status раз в statusEvery (без изменений) — как агент с Welcome.status_interval.
	go func() {
		tk := time.NewTicker(p.r.cfg.statusEvery)
		defer tk.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-tk.C:
				p.sendStatus()
			}
		}
	}()

	recvErr := make(chan error, 1)
	drain := make(chan time.Duration, 1)
	go func() {
		for {
			m, err := st.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			if c := m.GetCommand(); c != nil {
				if d := c.GetDrain(); d != nil {
					select {
					case drain <- max(d.GetReconnectWithin().AsDuration(), time.Millisecond):
					default:
					}
					continue
				}
				p.onCommand(c)
			}
		}
	}()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case err := <-recvErr:
		return 0, err
	case within := <-drain:
		// Переподключиться в случайный момент окна; до него поток работает (или закроется сервером раньше).
		select {
		case <-time.After(time.Duration(rndInt64N(int64(within)))):
		case err := <-recvErr:
			_ = err // поток закрылся раньше: переподключение сразу
		case <-ctx.Done():
		}
		return within, nil
	}
}

func (p *pcSim) statusLocked() *agentv1.Status {
	s := &agentv1.Status{State: p.state, SignedInUserId: p.userID, ObservedAt: timestamppb.Now(),
		Load: &agentv1.ComputerLoad{CpuBasisPoints: 1500, RamBasisPoints: 4000, AgentMemoryMb: 60}}
	if p.state == agentv1.ComputerState_COMPUTER_STATE_IN_SESSION {
		s.SessionId = p.sessionID
	}
	return s
}

func (p *pcSim) send(m *agentv1.AgentMessage) {
	p.mu.Lock()
	st := p.stream
	p.mu.Unlock()
	if st == nil {
		return
	}
	p.sendMu.Lock()
	err := st.Send(m)
	p.sendMu.Unlock()
	if err == nil {
		p.r.counters.statusSent.Add(1)
	}
}

func (p *pcSim) sendStatus() {
	p.mu.Lock()
	s := p.statusLocked()
	p.mu.Unlock()
	p.send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: s}})
}

func (p *pcSim) setState(st agentv1.ComputerState, userID int64, session string) {
	p.mu.Lock()
	p.state, p.userID = st, userID
	if session != "" || st != agentv1.ComputerState_COMPUTER_STATE_IN_SESSION {
		p.sessionID = session
	}
	p.mu.Unlock()
	p.sendStatus()
}

// onCommand — итог команды сразу (SUCCEEDED); долговременные — с seq (агент сохраняет last_command_seq).
func (p *pcSim) onCommand(c *agentv1.Command) {
	p.r.counters.commands.Add(1)
	switch k := c.GetKind().(type) {
	case *agentv1.Command_StartSession:
		if sid := k.StartSession.GetSession().GetSessionId(); sid != "" {
			select {
			case p.startCh <- sid:
			default:
			}
		}
	case *agentv1.Command_EndSession:
		p.r.counters.endSessionCmds.Add(1)
	}
	if c.GetSeq() > 0 {
		p.mu.Lock()
		if c.GetSeq() > p.lastSeq {
			p.lastSeq = c.GetSeq()
		}
		p.mu.Unlock()
	}
	if c.GetSeq() == 0 && c.GetPing() == nil {
		return // мгновенные уведомления (BalanceChanged и т.п.) без ответа
	}
	p.send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_CommandResult{CommandResult: &agentv1.CommandResult{
		CommandId: c.GetCommandId(), Seq: c.GetSeq(), Status: agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED,
		CompletedAt: timestamppb.Now(), AgentTime: timestamppb.Now()}}})
}

// waitConn — канал открытого потока (гость работает только при живом потоке, как лаунчер).
func (p *pcSim) waitConn(ctx context.Context) (*grpc.ClientConn, error) {
	for {
		p.mu.Lock()
		conn, ch := p.conn, p.connected
		p.mu.Unlock()
		if conn != nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ch:
		case <-time.After(2 * time.Minute):
			return nil, status.Error(codes.Unavailable, "loadgen: no stream for 2m")
		}
	}
}

// retryable — ответ мог потеряться (обрыв, перезапуск инстанса): повтор с тем же ключом идемпотентности.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		return true
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Aborted: // Aborted — IDEMPOTENCY_IN_PROGRESS
		return true
	}
	return false
}

// money — денежный вызов с повтором тем же ключом (до 6 попыток); записывается одна операция с общей длительностью
// и отдельно — каждый повтор (op + ".retry").
func money[T any](ctx context.Context, p *pcSim, op string, call func(context.Context, *grpc.ClientConn) (T, error)) (T, error) {
	t0 := time.Now()
	var zero T
	var last error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			p.r.stats.rec(op+".retry", time.Now(), last)
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		conn, err := p.waitConn(ctx)
		if err != nil {
			last = err
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		res, err := call(cctx, conn)
		cancel()
		if err == nil || !retryable(err) || ctx.Err() != nil {
			p.r.stats.rec(op, t0, err)
			return res, err
		}
		last = err
	}
	p.r.stats.rec(op, t0, last)
	return zero, last
}

func guestCtx(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func uniform(a, b time.Duration) time.Duration {
	return a + time.Duration(rndInt64N(int64(b-a)+1))
}

// expDur — экспоненциальная пауза со средним mean, не больше 3·mean.
func expDur(mean time.Duration) time.Duration {
	return min(time.Duration(rndExp()*float64(mean)), 3*mean)
}

// runGuest — цикл гостя: вход → покупка (минуты или тариф; в клубах с автобиллингом часть гостей ждёт автопокупку) →
// игра (заказ в магазине) → завершение сессии (или истечение) → выход → пауза.
func (p *pcSim) runGuest(ctx context.Context) {
	cfg := p.r.cfg
	if !sleepCtx(ctx, uniform(0, cfg.guestIdleMean+cfg.playMax)) { // разнести старты по циклу
		return
	}
	uid := guestUserID(p.cred.Club, p.cred.PC)
	for ctx.Err() == nil {
		conn, err := p.waitConn(ctx)
		if err != nil {
			continue
		}
		t0 := time.Now()
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		lr, err := agentv1.NewGuestAuthServiceClient(conn).Login(lctx, &agentv1.LoginRequest{Phone: guestPhone(p.cred.Club, p.cred.PC), Password: loadPassword})
		cancel()
		p.r.stats.rec("guest.login", t0, err)
		if err != nil {
			sleepCtx(ctx, uniform(10*time.Second, 30*time.Second))
			continue
		}
		token := lr.GetSignIn().GetToken().GetAccessToken()
		gctx := guestCtx(ctx, token)
		sid := lr.GetSignIn().GetSession().GetSessionId()
		var ends time.Time
		if s := lr.GetSignIn().GetSession(); s != nil && s.GetEndsAt() != nil {
			ends = s.GetEndsAt().AsTime()
		}
		p.setState(agentv1.ComputerState_COMPUTER_STATE_LOCKED, uid, "")
		for len(p.startCh) > 0 {
			<-p.startCh
		}

		switch {
		case sid != "":
			// сессия осталась с прошлого цикла (прерванный прогон) — продолжаем её
		case autoBillingClub(p.cred.Club) && rndFloat() < cfg.autoBillShare:
			// Ждёт автобиллинг: пауза клуба 1 мин, затем сервер сам покупает минуты на весь баланс (StartSession).
			t1 := time.Now()
			select {
			case sid = <-p.startCh:
				p.r.stats.rec("autobilling.start_session", t1, nil)
			case <-time.After(4 * time.Minute):
				p.r.stats.rec("autobilling.start_session", t1, errors.New("timeout"))
			case <-ctx.Done():
				return
			}
			ends = time.Now().Add(time.Hour)
		default:
			key := uuid.NewString()
			if rndFloat() < 0.5 {
				minutes := int32(5 + rndIntN(16)) // #nosec G115
				req := &agentv1.PurchaseMinutesRequest{IdempotencyKey: key, Minutes: minutes, BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN}
				res, err := money(gctx, p, "guest.purchase_minutes", func(c context.Context, conn *grpc.ClientConn) (*agentv1.PurchaseMinutesResponse, error) {
					return agentv1.NewGuestServiceClient(conn).PurchaseMinutes(c, req)
				})
				if err == nil {
					sid, ends = res.GetPurchase().GetSession().GetSessionId(), res.GetPurchase().GetSession().GetEndsAt().AsTime()
					p.r.ledger.add(key, res.GetPurchase().GetChargedMainTiyn())
					if rndFloat() < cfg.replayShare {
						p.replayMinutes(gctx, req, res.GetPurchase().GetChargedMainTiyn())
					}
				}
			} else {
				req := &agentv1.PurchaseTariffRequest{IdempotencyKey: key, TariffId: zoneID(p.cred.Club, 1), BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN}
				res, err := money(gctx, p, "guest.purchase_tariff", func(c context.Context, conn *grpc.ClientConn) (*agentv1.PurchaseTariffResponse, error) {
					return agentv1.NewGuestServiceClient(conn).PurchaseTariff(c, req)
				})
				if err == nil {
					sid, ends = res.GetPurchase().GetSession().GetSessionId(), res.GetPurchase().GetSession().GetEndsAt().AsTime()
					p.r.ledger.add(key, res.GetPurchase().GetChargedMainTiyn())
				}
			}
		}
		if sid != "" {
			p.setState(agentv1.ComputerState_COMPUTER_STATE_IN_SESSION, uid, sid)
			p.r.inSession.add(p, token)
			play := uniform(cfg.playMin, cfg.playMax)
			orderAt := uniform(0, play)
			if rndFloat() < cfg.orderShare {
				if !sleepCtx(ctx, orderAt) {
					return
				}
				p.placeOrder(gctx, "shop.place_order")
				play -= orderAt
			}
			if !sleepCtx(ctx, play) {
				return
			}
			p.r.inSession.remove(p)
			if ends.IsZero() || time.Now().Before(ends.Add(-20*time.Second)) {
				key := uuid.NewString()
				_, _ = money(gctx, p, "guest.end_session", func(c context.Context, conn *grpc.ClientConn) (*agentv1.EndSessionResponse, error) {
					return agentv1.NewGuestServiceClient(conn).EndSession(c, &agentv1.EndSessionRequest{IdempotencyKey: key, SessionId: sid})
				})
			} else {
				p.r.counters.expiredSessions.Add(1) // время вышло — сессию закрывает воркер expire-sessions
			}
		}
		if conn, err := p.waitConn(ctx); err == nil {
			t2 := time.Now()
			lctx, cancel := context.WithTimeout(gctx, 15*time.Second)
			_, err := agentv1.NewGuestAuthServiceClient(conn).Logout(lctx, &agentv1.LogoutRequest{})
			cancel()
			p.r.stats.rec("guest.logout", t2, err)
		}
		p.setState(agentv1.ComputerState_COMPUTER_STATE_IDLE, 0, "")
		if !sleepCtx(ctx, expDur(cfg.guestIdleMean)) {
			return
		}
	}
}

func (p *pcSim) placeOrder(gctx context.Context, op string) {
	key := uuid.NewString()
	items := []*agentv1.OrderItem{{ProductId: zoneID(p.cred.Club, 1+rndIntN(3)), Quantity: int32(1 + rndIntN(2))}} // #nosec G115
	res, err := money(gctx, p, op, func(c context.Context, conn *grpc.ClientConn) (*agentv1.PlaceOrderResponse, error) {
		return agentv1.NewShopServiceClient(conn).PlaceOrder(c, &agentv1.PlaceOrderRequest{IdempotencyKey: key, Items: items})
	})
	if err == nil {
		p.r.ledger.add(key, res.GetAmountTiyn())
	}
}

// replayMinutes — повтор той же покупки тем же ключом: тот же результат, списания второй раз нет.
func (p *pcSim) replayMinutes(gctx context.Context, req *agentv1.PurchaseMinutesRequest, charged int64) {
	res, err := money(gctx, p, "guest.replay", func(c context.Context, conn *grpc.ClientConn) (*agentv1.PurchaseMinutesResponse, error) {
		return agentv1.NewGuestServiceClient(conn).PurchaseMinutes(c, req)
	})
	if err == nil && res.GetPurchase().GetChargedMainTiyn() != charged {
		p.r.counters.replayMismatch.Add(1)
	}
}
