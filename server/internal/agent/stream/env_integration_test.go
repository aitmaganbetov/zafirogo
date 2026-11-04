//go:build integration

package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
)

// Образы — как в docker-compose.dev.yml (Docker Hub при 429 — зеркало mirror.gcr.io).
const (
	valkeyImage = "valkey/valkey:9.1.3-alpine"
	natsImage   = "nats:2.15.2-alpine"
)

// loadChildEnv — признак дочернего процесса-сервера нагрузочного теста.
const loadChildEnv = "ZAFIRO_LOAD_CHILD"

// env — Postgres + Valkey + NATS на весь пакет (TestMain).
type env struct {
	db       *dbtest.DB
	valkey   valkey.Client
	natsURL  string
	vkAddr   string
	cleanups []func()
}

var testEnv *env

func TestMain(m *testing.M) {
	if os.Getenv(loadChildEnv) != "" {
		// Дочерний процесс нагрузочного теста (load_test.go): окружение передаёт родитель.
		os.Exit(m.Run())
	}
	ctx := context.Background()
	e, err := startEnv(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stream integration env:", err)
		os.Exit(1)
	}
	testEnv = e
	code := m.Run()
	for i := len(e.cleanups) - 1; i >= 0; i-- {
		e.cleanups[i]()
	}
	os.Exit(code)
}

func startEnv(ctx context.Context) (*env, error) {
	e := &env{}
	d, cleanup, err := dbtest.Start(ctx)
	if err != nil {
		return nil, err
	}
	e.cleanups = append(e.cleanups, cleanup)
	e.db = d

	vk, err := testcontainers.Run(ctx, valkeyImage,
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("6379/tcp").WithStartupTimeout(60*time.Second)))
	if err != nil {
		return nil, fmt.Errorf("valkey: %w", err)
	}
	e.cleanups = append(e.cleanups, func() { _ = testcontainers.TerminateContainer(vk) })
	if e.vkAddr, err = vk.PortEndpoint(ctx, "6379/tcp", ""); err != nil {
		return nil, err
	}
	if e.valkey, err = valkey.NewClient(valkey.ClientOption{InitAddress: []string{e.vkAddr}, DisableCache: true}); err != nil {
		return nil, err
	}
	e.cleanups = append(e.cleanups, e.valkey.Close)

	nc, err := testcontainers.Run(ctx, natsImage,
		testcontainers.WithExposedPorts("4222/tcp"),
		testcontainers.WithWaitStrategy(wait.ForLog("Server is ready").WithStartupTimeout(60*time.Second)))
	if err != nil {
		return nil, fmt.Errorf("nats: %w", err)
	}
	e.cleanups = append(e.cleanups, func() { _ = testcontainers.TerminateContainer(nc) })
	if e.natsURL, err = nc.PortEndpoint(ctx, "4222/tcp", "nats"); err != nil {
		return nil, err
	}
	return e, nil
}

// fixture — клуб, зона и ПК (через владельца схемы, в обход RLS).
func (e *env) computer(t testing.TB, clubID int64, number int) int64 {
	t.Helper()
	ctx := context.Background()
	var zone int64
	err := e.db.Owner.QueryRow(ctx, "SELECT id FROM club_zones WHERE club_id = $1 ORDER BY id LIMIT 1", clubID).Scan(&zone)
	if err != nil {
		if err := e.db.Owner.QueryRow(ctx,
			"INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Общий зал', now(), now()) RETURNING id",
			clubID).Scan(&zone); err != nil {
			t.Fatalf("zone: %v", err)
		}
	}
	var id int64
	if err := e.db.Owner.QueryRow(ctx,
		"INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, $3, now(), now()) RETURNING id",
		clubID, zone, number).Scan(&id); err != nil {
		t.Fatalf("computer: %v", err)
	}
	return id
}

var clubSeq struct {
	sync.Mutex
	n int
}

func (e *env) club(t testing.TB) int64 {
	clubSeq.Lock()
	clubSeq.n++
	n := clubSeq.n
	clubSeq.Unlock()
	return e.db.Club(t, "stream-"+strconv.Itoa(n)+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
}

func (e *env) nats(t testing.TB) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(e.natsURL)
	if err != nil {
		t.Fatalf("nats: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// testIdentity — вместо mTLS: club/computer из метаданных (только тесты).
func testIdentity(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	md, _ := metadata.FromIncomingContext(ss.Context())
	club, _ := strconv.ParseInt(first(md.Get("x-test-club")), 10, 64)
	pc, _ := strconv.ParseInt(first(md.Get("x-test-computer")), 10, 64)
	if club == 0 || pc == 0 {
		return h(srv, ss)
	}
	id := identity.Identity{ClubID: club, ComputerID: pc, CertSerial: first(md.Get("x-test-serial"))}
	return h(srv, &identityStream{ServerStream: ss, ctx: identity.WithIdentity(ss.Context(), id)})
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

type identityStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *identityStream) Context() context.Context { return s.ctx }

// instance — один «инстанс сервера»: Service + gRPC-сервер на bufconn.
type instance struct {
	svc  *Service
	gs   *grpc.Server
	lis  *bufconn.Listener
	conn *grpc.ClientConn
}

func (e *env) instance(t testing.TB, name string, tune func(*Options)) *instance {
	t.Helper()
	o := Options{
		Instance: name, Pool: e.db.App, Valkey: e.valkey, NATS: e.nats(t),
		KeyPrefix: "t:" + t.Name() + ":", SubjectPrefix: "t." + sanitize(t.Name()),
		StateDebounce: 50 * time.Millisecond, MinStatusGap: time.Millisecond, OfflineGrace: 200 * time.Millisecond,
		TickEvery: 100 * time.Millisecond, AckTimeout: 300 * time.Millisecond,
	}
	if tune != nil {
		tune(&o)
	}
	svc, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	gs := grpc.NewServer(grpc.StreamInterceptor(testIdentity))
	Register(gs, svc)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(lis) }()
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	in := &instance{svc: svc, gs: gs, lis: lis, conn: cc}
	t.Cleanup(func() {
		_ = cc.Close()
		svc.Close()
		gs.Stop()
	})
	return in
}

func sanitize(s string) string {
	b := []byte(s)
	for i, c := range b {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !ok {
			b[i] = '_'
		}
	}
	return string(b)
}

// agent — клиент-агент для тестов.
type agent struct {
	t      testing.TB
	stream agentv1.AgentService_ConnectClient
	cancel context.CancelFunc
	msgs   chan *agentv1.ServerMessage
	errc   chan error
}

func (in *instance) dial(t testing.TB, club, pc int64) (agentv1.AgentService_ConnectClient, context.CancelFunc) {
	t.Helper()
	return in.dialSerial(t, club, pc, "")
}

// dialSerial — dial с серийным номером клиентского сертификата в идентичности.
func (in *instance) dialSerial(t testing.TB, club, pc int64, serial string) (agentv1.AgentService_ConnectClient, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ctx = metadata.AppendToOutgoingContext(ctx, "x-test-club", strconv.FormatInt(club, 10), "x-test-computer", strconv.FormatInt(pc, 10),
		"x-test-serial", serial)
	st, err := agentv1.NewAgentServiceClient(in.conn).Connect(ctx)
	if err != nil {
		cancel()
		t.Fatalf("connect: %v", err)
	}
	return st, cancel
}

func defaultHello() *agentv1.Hello {
	return &agentv1.Hello{
		ProtocolVersion: 1, AgentVersion: "2.0.0", BootId: "boot-1",
		Capabilities: &agentv1.AgentCapabilities{PowerControl: true, ShowMessage: true, ServiceMode: true, WakeOnLan: true, SelfUpdate: true},
		Device:       &agentv1.DeviceInfo{Hostname: "DESKTOP-1", LocalIp: "192.168.1.10", CpuModel: "Ryzen 5", CpuCores: 12, RamTotalMb: 16384},
		Status:       &agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_IDLE},
	}
}

// connect открывает стрим, шлёт Hello и ждёт Welcome.
func (in *instance) connect(t testing.TB, club, pc int64, hello *agentv1.Hello) (*agent, *agentv1.Welcome) {
	t.Helper()
	return in.connectSerial(t, club, pc, "", hello)
}

// connectSerial — connect с серийным номером клиентского сертификата.
func (in *instance) connectSerial(t testing.TB, club, pc int64, serial string, hello *agentv1.Hello) (*agent, *agentv1.Welcome) {
	t.Helper()
	st, cancel := in.dialSerial(t, club, pc, serial)
	a := &agent{t: t, stream: st, cancel: cancel, msgs: make(chan *agentv1.ServerMessage, 256), errc: make(chan error, 1)}
	t.Cleanup(cancel)
	if hello == nil {
		hello = defaultHello()
	}
	if err := st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Hello{Hello: hello}}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	go func() {
		for {
			m, err := st.Recv()
			if err != nil {
				a.errc <- err
				close(a.msgs)
				return
			}
			a.msgs <- m
		}
	}()
	m := a.next(5 * time.Second)
	if m == nil || m.GetWelcome() == nil {
		t.Fatalf("expected Welcome, got %v (err %v)", m, a.err())
	}
	return a, m.GetWelcome()
}

// next — следующее сообщение сервера или nil по таймауту/закрытию.
func (a *agent) next(d time.Duration) *agentv1.ServerMessage {
	select {
	case m, ok := <-a.msgs:
		if !ok {
			return nil
		}
		return m
	case <-time.After(d):
		return nil
	}
}

func (a *agent) command(d time.Duration) *agentv1.Command {
	a.t.Helper()
	m := a.next(d)
	if m == nil {
		return nil
	}
	if m.GetCommand() == nil {
		a.t.Fatalf("expected Command, got %v", m)
	}
	return m.GetCommand()
}

func (a *agent) err() error {
	select {
	case err := <-a.errc:
		return err
	case <-time.After(2 * time.Second):
		return nil
	}
}

// closedWith ждёт закрытия стрима сервером.
func (a *agent) closedWith(d time.Duration) error {
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-a.msgs:
			if !ok {
				err := <-a.errc
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		case <-deadline:
			a.t.Fatalf("stream was not closed in %v", d)
			return nil
		}
	}
}

func (a *agent) send(m *agentv1.AgentMessage) {
	a.t.Helper()
	if err := a.stream.Send(m); err != nil {
		a.t.Fatalf("send: %v", err)
	}
}

func (a *agent) status(state agentv1.ComputerState, cpu uint32) {
	a.send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{
		State: state, Load: &agentv1.ComputerLoad{CpuBasisPoints: cpu}}}})
}

func (a *agent) result(cmd *agentv1.Command, s agentv1.CommandStatus) {
	a.send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_CommandResult{CommandResult: &agentv1.CommandResult{
		CommandId: cmd.GetCommandId(), Seq: cmd.GetSeq(), Status: s}}})
}

// hangUp — агент закрывает стрим (CloseSend + отмена).
func (a *agent) hangUp() {
	_ = a.stream.CloseSend()
	a.cancel()
}

// eventually повторяет проверку до успеха.
func eventually(t testing.TB, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}
