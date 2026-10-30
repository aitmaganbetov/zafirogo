//go:build integration

package guestapi_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/valkey-io/valkey-go"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/guestapi"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/stream"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Образы — как в docker-compose.dev.yml.
const (
	valkeyImage = "valkey/valkey:9.1.3-alpine"
	natsImage   = "nats:2.15.2-alpine"
)

type testEnv struct {
	db      *dbtest.DB
	valkey  valkey.Client
	natsURL string
	pw      *password.Hasher
}

var env testEnv

func TestMain(m *testing.M) {
	ctx := context.Background()
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "guestapi integration env:", err)
		cleanup()
		os.Exit(1)
	}
	d, c, err := dbtest.Start(ctx)
	if err != nil {
		fail(err)
	}
	cleanups = append(cleanups, c)
	if _, err := db.EnsurePartitions(ctx, d.Owner, time.Now(), 2); err != nil {
		fail(err)
	}
	env.db = d

	vk, err := testcontainers.Run(ctx, valkeyImage, testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("6379/tcp").WithStartupTimeout(60*time.Second)))
	if err != nil {
		fail(err)
	}
	cleanups = append(cleanups, func() { _ = testcontainers.TerminateContainer(vk) })
	vkAddr, err := vk.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		fail(err)
	}
	if env.valkey, err = valkey.NewClient(valkey.ClientOption{InitAddress: []string{vkAddr}, DisableCache: true}); err != nil {
		fail(err)
	}
	cleanups = append(cleanups, env.valkey.Close)

	nc, err := testcontainers.Run(ctx, natsImage, testcontainers.WithExposedPorts("4222/tcp"),
		testcontainers.WithWaitStrategy(wait.ForLog("Server is ready").WithStartupTimeout(60*time.Second)))
	if err != nil {
		fail(err)
	}
	cleanups = append(cleanups, func() { _ = testcontainers.TerminateContainer(nc) })
	if env.natsURL, err = nc.PortEndpoint(ctx, "4222/tcp", "nats"); err != nil {
		fail(err)
	}
	// Дешёвые параметры Argon2id: тесты проверяют поведение, а не стойкость.
	if env.pw, err = password.New(password.Options{Params: password.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}}); err != nil {
		fail(err)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

var seq atomic.Int64

func uniq(prefix string) string {
	return prefix + strconv.FormatInt(seq.Add(1), 10) + "x" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// clock — часы теста (billing, токены, Welcome): стоят на месте, двигаются только advance.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// harness — клуб с подпиской, зоной «Hall» 600 ₸/час, владельцем; PKI, регистрация ПК (A2), стрим (A3) и
// сервисы guestapi на настоящем gRPC с mTLS.
type harness struct {
	t       *testing.T
	club    int64
	owner   int64
	zone    int64
	clock   *clock
	enr     *enroll.Service
	roots   *x509.CertPool
	addr    string
	srv     *guestapi.Server
	stream  *stream.Service
	billing *billingsvc.Service
	prefix  string
}

type harnessOpts struct {
	noLimits bool
	settings string // clubs.settings JSON
}

func newHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	ctx := context.Background()
	d := env.db
	h := &harness{t: t, clock: &clock{t: time.Now().UTC().Truncate(time.Second)}, prefix: uniq("t") + ":"}
	h.club = d.Club(t, uniq("guest-"))
	if o.settings != "" {
		h.exec("UPDATE clubs SET settings = $2::jsonb WHERE id = $1", h.club, o.settings)
	}
	var plan int64
	h.scan(&plan, `INSERT INTO saas_plans (slug, name, features, created_at, updated_at) VALUES ($1, 'План', '{}'::jsonb, now(), now()) RETURNING id`, uniq("plan-"))
	h.exec(`INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, current_period_ends_at)
		VALUES ($1, $2, 'active', now(), now() + interval '30 days')`, h.club, plan)
	h.owner = h.user("Владелец", uniqPhone(), "x", "owner")
	h.scan(&h.zone, "INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, 'Hall', 60000, now(), now()) RETURNING id", h.club)

	dir := filepath.Join(t.TempDir(), "pki")
	popts := pki.Options{DevDir: dir}
	ca, err := pki.LoadCA(popts)
	if err != nil {
		t.Fatal(err)
	}
	srvCert, err := pki.LoadServerCertificate(popts)
	if err != nil {
		t.Fatal(err)
	}
	rootPEM, err := os.ReadFile(filepath.Join(dir, pki.DevRootCertFile)) // #nosec G304 -- временный каталог теста
	if err != nil {
		t.Fatal(err)
	}
	h.roots = x509.NewCertPool()
	h.roots.AppendCertsFromPEM(rootPEM)
	cache := enroll.NewCertCache(d.Platform, enroll.CacheOptions{})
	if err := cache.Load(ctx); err != nil {
		t.Fatal(err)
	}
	h.enr, err = enroll.New(enroll.Options{Pool: d.App, CA: ca, CodePepper: []byte("0123456789abcdef0123456789abcdef"),
		AgentEndpoint: "agent.test:443", Cache: cache})
	if err != nil {
		t.Fatal(err)
	}

	h.billing = billingsvc.New(d.App, billingsvc.ClockFunc(h.clock.Now), nil)
	opts := guestapi.Options{Pool: d.App, Valkey: env.valkey, Passwords: env.pw, Billing: h.billing,
		KeyPrefix: h.prefix + "guest:", QRBaseURL: "https://zafiro.test", QRPollEvery: 100 * time.Millisecond, Now: h.clock.Now}
	if !o.noLimits {
		lim, err := ratelimit.New(ratelimit.Options{Client: env.valkey, Prefix: h.prefix + "rl:"})
		if err != nil {
			t.Fatal(err)
		}
		opts.Limiter = lim
	}
	hooks, err := guestapi.NewHooks(opts)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := nats.Connect(env.natsURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	h.stream, err = stream.New(stream.Options{Instance: "i" + hex.EncodeToString([]byte(h.prefix))[:12] + strconv.FormatInt(seq.Add(1), 10),
		Pool: d.App, Valkey: env.valkey, NATS: nc, KeyPrefix: h.prefix + "agent:", SubjectPrefix: "t" + strconv.FormatInt(seq.Add(1), 10),
		Welcome: hooks, Events: hooks, MinStatusGap: time.Millisecond, StateDebounce: 10 * time.Millisecond,
		TickEvery: 100 * time.Millisecond, Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.stream.Close)
	opts.Stream = h.stream
	var regErr error
	srv, err := grpcserver.NewServer(grpcserver.Options{ServerCertificate: srvCert, ClientCAs: ca.ClientCAs(), Certs: cache,
		Enrollment: enroll.NewGRPC(h.enr)}, func(gs *grpc.Server) { h.srv, regErr = guestapi.Register(gs, opts) })
	if err != nil || regErr != nil {
		t.Fatal(err, regErr)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	h.addr = lis.Addr().String()
	return h
}

func (h *harness) exec(sql string, args ...any) {
	h.t.Helper()
	if _, err := env.db.Owner.Exec(context.Background(), sql, args...); err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
}

func (h *harness) scan(dst any, sql string, args ...any) {
	h.t.Helper()
	if err := env.db.Owner.QueryRow(context.Background(), sql, args...).Scan(dst); err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
}

func (h *harness) int(sql string, args ...any) int64 {
	h.t.Helper()
	var v int64
	h.scan(&v, sql, args...)
	return v
}

var phoneSeq atomic.Int64

func uniqPhone() string {
	return fmt.Sprintf("+77%09d", (time.Now().UnixNano()/1000+phoneSeq.Add(1)*7919)%1_000_000_000)
}

// user — пользователь с паролем (bcrypt, как в перенесённых из Laravel строках) и членством role.
func (h *harness) user(name, phone, pass, role string) int64 {
	h.t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), 4)
	if err != nil {
		h.t.Fatal(err)
	}
	var id int64
	h.scan(&id, "INSERT INTO users (name, phone, password, created_at, updated_at) VALUES ($1, $2, $3, now(), now()) RETURNING id", name, phone, string(hash))
	h.exec("INSERT INTO club_memberships (club_id, user_id, role, created_at, updated_at) VALUES ($1, $2, $3, now(), now())", h.club, id, role)
	h.exec("INSERT INTO member_balances (club_id, user_id, created_at, updated_at) VALUES ($1, $2, now(), now())", h.club, id)
	return id
}

// customer — клиент с паролем и начальными балансами (через ledger: billing.SetInitialBalance).
func (h *harness) customer(name, phone, pass string, main, bonus money.Tiyn) int64 {
	h.t.Helper()
	id := h.user(name, phone, pass, "client")
	if main > 0 || bonus > 0 {
		if err := db.InTenantTx(context.Background(), env.db.App, h.club, func(q *store.Queries) error {
			_, err := billing.SetInitialBalance(context.Background(), q, billing.InitialBalanceParams{ClubID: h.club, UserID: id, Main: main, Bonus: bonus})
			return err
		}); err != nil {
			h.t.Fatal(err)
		}
	}
	return id
}

func (h *harness) balances(user int64) (money.Tiyn, money.Tiyn) {
	h.t.Helper()
	m, b := env.db.Balances(h.t, h.club, user)
	return money.Tiyn(m), money.Tiyn(b)
}

func (h *harness) wantBalances(user int64, main, bonus money.Tiyn) {
	h.t.Helper()
	if m, b := h.balances(user); m != main || b != bonus {
		h.t.Fatalf("balances = %s / %s, want %s / %s", m, b, main, bonus)
	}
}

func (h *harness) noDrift() {
	h.t.Helper()
	if err := db.InTenantTx(context.Background(), env.db.App, h.club, func(q *store.Queries) error {
		rows, err := q.ListMemberBalanceDrift(context.Background(), h.club)
		if err == nil && len(rows) > 0 {
			err = fmt.Errorf("drift: %+v", rows)
		}
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
}

// pc — зарегистрированный ПК (настоящий Enroll по одноразовому коду, сертификат mTLS).
type pc struct {
	h    *harness
	id   int64
	conn *grpc.ClientConn
}

func (h *harness) dial(cert *tls.Certificate) *grpc.ClientConn {
	h.t.Helper()
	cfg := &tls.Config{RootCAs: h.roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	conn, err := grpc.NewClient(h.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

var macSeq atomic.Int64

func (h *harness) enrollPC() *pc {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	code, err := h.enr.CreateCode(ctx, principal.Principal{UserID: h.owner, ClubID: h.club, Role: "owner"}, enroll.CodeRequest{ZoneID: &h.zone})
	if err != nil {
		h.t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		h.t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		h.t.Fatal(err)
	}
	n := macSeq.Add(1)
	mac := fmt.Sprintf("02:00:00:%02X:%02X:%02X", (n>>16)&0xff, (n>>8)&0xff, n&0xff)
	resp, err := agentv1.NewEnrollmentServiceClient(h.dial(nil)).Enroll(ctx, &agentv1.EnrollRequest{
		EnrollmentCode: code.Code, CsrPem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})),
		Device: &agentv1.DeviceInfo{HardwareUuid: uniq("uuid-"), MacAddress: mac, Hostname: "", AgentVersion: "2.0.0"}})
	if err != nil {
		h.t.Fatal(err)
	}
	c, err := pki.ParseCertificatePEM([]byte(resp.GetCertificatePem()))
	if err != nil {
		h.t.Fatal(err)
	}
	conn := h.dial(&tls.Certificate{Certificate: [][]byte{c.Raw}, PrivateKey: key, Leaf: c})
	return &pc{h: h, id: resp.GetComputerId(), conn: conn}
}

func withToken(token string) context.Context {
	ctx := metadata.AppendToOutgoingContext(context.Background(), "zafiro-protocol-version", "1", "zafiro-agent-version", "2.0.0")
	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}
	return ctx
}

func (p *pc) authc() agentv1.GuestAuthServiceClient { return agentv1.NewGuestAuthServiceClient(p.conn) }
func (p *pc) guest() agentv1.GuestServiceClient     { return agentv1.NewGuestServiceClient(p.conn) }
func (p *pc) shop() agentv1.ShopServiceClient       { return agentv1.NewShopServiceClient(p.conn) }
func (p *pc) social() agentv1.SocialServiceClient   { return agentv1.NewSocialServiceClient(p.conn) }
func (p *pc) agent() agentv1.AgentServiceClient     { return agentv1.NewAgentServiceClient(p.conn) }

func (p *pc) login(phone, pass string) (*agentv1.GuestSignIn, error) {
	resp, err := p.authc().Login(context.Background(), &agentv1.LoginRequest{Phone: phone, Password: pass})
	return resp.GetSignIn(), err
}

func (p *pc) mustLogin(phone, pass string) string {
	p.h.t.Helper()
	s, err := p.login(phone, pass)
	if err != nil {
		p.h.t.Fatalf("login %s: %v", phone, err)
	}
	return s.GetToken().GetAccessToken()
}

func wantReason(t *testing.T, err error, c codes.Code, reason agentv1.ErrorCode) {
	t.Helper()
	if status.Code(err) != c || grpcserver.ReasonOf(err) != reason.String() {
		t.Fatalf("err = %v (reason %q), want %v %s", err, grpcserver.ReasonOf(err), c, reason)
	}
}

// agentStream — стрим Connect ПК: Hello, Welcome, приём команд (с подтверждением).
type agentStream struct {
	t       *testing.T
	st      agentv1.AgentService_ConnectClient
	welcome *agentv1.Welcome
	cmds    chan *agentv1.Command
	cancel  context.CancelFunc
}

func (p *pc) connect(bootID string, signedIn int64) *agentStream {
	p.h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := p.agent().Connect(ctx)
	if err != nil {
		cancel()
		p.h.t.Fatal(err)
	}
	state := agentv1.ComputerState_COMPUTER_STATE_IDLE
	if signedIn > 0 {
		state = agentv1.ComputerState_COMPUTER_STATE_LOCKED
	}
	if err := st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Hello{Hello: &agentv1.Hello{
		ProtocolVersion: 1, AgentVersion: "2.0.0", BootId: bootID,
		Status: &agentv1.Status{State: state, SignedInUserId: signedIn},
	}}}); err != nil {
		cancel()
		p.h.t.Fatal(err)
	}
	m, err := st.Recv()
	if err != nil {
		cancel()
		p.h.t.Fatalf("welcome: %v", err)
	}
	a := &agentStream{t: p.h.t, st: st, welcome: m.GetWelcome(), cmds: make(chan *agentv1.Command, 64), cancel: cancel}
	go func() {
		for {
			m, err := st.Recv()
			if err != nil {
				close(a.cmds)
				return
			}
			if c := m.GetCommand(); c != nil {
				_ = st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_CommandResult{CommandResult: &agentv1.CommandResult{
					CommandId: c.GetCommandId(), Seq: c.GetSeq(), Status: agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED}}})
				a.cmds <- c
			}
		}
	}()
	p.h.t.Cleanup(func() { cancel() })
	return a
}

// waitCommand ждёт команду, для которой match = true (остальные пропускает).
func (a *agentStream) waitCommand(match func(*agentv1.Command) bool) *agentv1.Command {
	a.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case c, ok := <-a.cmds:
			if !ok {
				a.t.Fatal("stream closed while waiting for a command")
			}
			if match(c) {
				return c
			}
		case <-timeout:
			a.t.Fatal("command not received")
		}
	}
}
