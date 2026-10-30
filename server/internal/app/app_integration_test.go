//go:build integration

package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/config"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
)

// Образы — как в docker-compose.dev.yml (при 429 Docker Hub — ZAFIRO_TEST_*_IMAGE с mirror.gcr.io/library/...).
const (
	valkeyImage = "valkey/valkey:9.1.3-alpine"
	natsImage   = "nats:2.15.2-alpine"
)

func imageOr(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

// testEnv — Postgres (миграции + логины), Valkey и NATS JetStream.
type testEnv struct {
	db          *dbtest.DB
	appDSN      string
	platformDSN string
	valkeyAddr  string
	natsURL     string
}

func startEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	e := &testEnv{db: dbtest.New(t)}
	if _, err := db.EnsurePartitions(ctx, e.db.Owner, time.Now(), 2); err != nil {
		t.Fatal(err)
	}

	// Свой платформенный логин: dbtest отдаёт DSN только логина приложения.
	e.appDSN = e.db.AppDSN
	user, pass := "platform_app_"+randHex(t, 4), randHex(t, 12)
	if _, err := e.db.Owner.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE zafiro_platform", user, pass)); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(e.appDSN)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, pass)
	e.platformDSN = u.String()

	vk, err := testcontainers.Run(ctx, imageOr("ZAFIRO_TEST_VALKEY_IMAGE", valkeyImage),
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("6379/tcp").WithStartupTimeout(60*time.Second)))
	if err != nil {
		t.Fatalf("valkey: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(vk) })
	if e.valkeyAddr, err = vk.PortEndpoint(ctx, "6379/tcp", ""); err != nil {
		t.Fatal(err)
	}

	nc, err := tcnats.Run(ctx, imageOr("ZAFIRO_TEST_NATS_IMAGE", natsImage))
	if err != nil {
		t.Fatalf("nats: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(nc) })
	if e.natsURL, err = nc.ConnectionString(ctx); err != nil {
		t.Fatal(err)
	}
	return e
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func (e *testEnv) config(t *testing.T, pkiDir string, extra ...string) *config.Config {
	t.Helper()
	env := append([]string{
		"ZAFIRO_ENV=test",
		"ZAFIRO_HTTP_ADDR=127.0.0.1:0",
		"ZAFIRO_AGENT_GRPC_LISTEN=127.0.0.1:0",
		"ZAFIRO_AGENT_INSTANCE_ID=test-" + randHex(t, 3),
		"ZAFIRO_AGENT_PKI_DEV_DIR=" + pkiDir,
		"ZAFIRO_DATABASE_URL=" + e.appDSN,
		"ZAFIRO_DATABASE_PLATFORM_URL=" + e.platformDSN,
		"ZAFIRO_VALKEY_ADDR=" + e.valkeyAddr,
		"ZAFIRO_NATS_URL=" + e.natsURL,
		"ZAFIRO_AGENT_STREAM_DRAIN_WITHIN=1s",
		"ZAFIRO_SHUTDOWN_TIMEOUT=20s",
		"ZAFIRO_AGENT_SWEEPER_INTERVAL=200ms",
		"ZAFIRO_AGENT_STREAM_OFFLINE_GRACE=300ms",
	}, extra...)
	cfg, err := config.Load("", func() []string { return env })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// running — Build + Run в фоне; stop отменяет контекст и возвращает результат Run.
type running struct {
	app  *App
	stop func() error
}

func start(t *testing.T, cfg *config.Config) *running {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	ctx, cancel := context.WithCancel(context.Background())
	a, err := Build(ctx, cfg, log)
	if err != nil {
		cancel()
		t.Fatalf("Build: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	stopped := false
	stop := func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(40 * time.Second):
			return fmt.Errorf("Run did not return")
		}
	}
	t.Cleanup(func() { _ = stop() })
	return &running{app: a, stop: stop}
}

func getJSON(t *testing.T, u string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(u) //nolint:noctx // тест
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// pc — ключ ПК и клиент gRPC агента.
type pc struct {
	key   *ecdsa.PrivateKey
	csr   string
	roots *x509.CertPool
	addr  string
}

func newPC(t *testing.T, pkiDir, addr string) *pc {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, k)
	if err != nil {
		t.Fatal(err)
	}
	rootPEM, err := os.ReadFile(filepath.Join(pkiDir, pki.DevRootCertFile))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(rootPEM)
	return &pc{key: k, csr: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), roots: roots, addr: addr}
}

func (p *pc) dial(t *testing.T, certPEM string) *grpc.ClientConn {
	t.Helper()
	cfg := &tls.Config{RootCAs: p.roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
	if certPEM != "" {
		c, err := pki.ParseCertificatePEM([]byte(certPEM))
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{{Certificate: [][]byte{c.Raw}, PrivateKey: p.key, Leaf: c}}
	}
	conn, err := grpc.NewClient(p.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// fixtures — клуб с активной подпиской и администратор.
func fixtures(t *testing.T, d *dbtest.DB) (clubID, staffID int64) {
	t.Helper()
	ctx := context.Background()
	clubID = d.Club(t, "app-"+randHex(t, 4))
	var planID int64
	if err := d.Owner.QueryRow(ctx, `INSERT INTO saas_plans (slug, name, features, created_at, updated_at)
		VALUES ($1, 'План', '{}'::jsonb, now(), now()) RETURNING id`, "plan-"+randHex(t, 4)).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Owner.Exec(ctx, `INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, current_period_ends_at)
		VALUES ($1, $2, 'active', now(), now() + interval '30 days')`, clubID, planID); err != nil {
		t.Fatal(err)
	}
	if err := d.Owner.QueryRow(ctx, `INSERT INTO users (name, phone, password, created_at, updated_at)
		VALUES ('Админ', '+7' || lpad(floor(random()*1e10)::bigint::text, 10, '0'), 'x', now(), now()) RETURNING id`).Scan(&staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Owner.Exec(ctx, `INSERT INTO club_memberships (club_id, user_id, role, created_at, updated_at)
		VALUES ($1, $2, 'admin', now(), now())`, clubID, staffID); err != nil {
		t.Fatal(err)
	}
	return clubID, staffID
}

func hello() *agentv1.AgentMessage {
	return &agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Hello{Hello: &agentv1.Hello{
		ProtocolVersion: 1, AgentVersion: "2.0.0", BootId: "boot-1",
		Capabilities: &agentv1.AgentCapabilities{PowerControl: true, ShowMessage: true},
		Device:       &agentv1.DeviceInfo{Hostname: "DESKTOP-1", LocalIp: "192.168.1.10"},
		Status:       &agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_IDLE},
	}}}
}

// recv читает сообщения потока в канал (до ошибки).
func recv(st agentv1.AgentService_ConnectClient) (<-chan *agentv1.ServerMessage, <-chan error) {
	msgs, errc := make(chan *agentv1.ServerMessage, 64), make(chan error, 1)
	go func() {
		defer close(msgs)
		for {
			m, err := st.Recv()
			if err != nil {
				errc <- err
				return
			}
			msgs <- m
		}
	}()
	return msgs, errc
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAppEndToEnd — сборка всех подсистем: healthz/readyz, выдача кода → Enroll по mTLS-каналу → поток
// Connect → статус → ПК online в Presence → отзыв (хук enroll → NATS → кэш → Kick) → остановка без утечек.
func TestAppEndToEnd(t *testing.T) {
	e := startEnv(t)
	// Горутины окружения (testcontainers, пулы dbtest) — не наши.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	pkiDir := filepath.Join(t.TempDir(), "pki")
	r := start(t, e.config(t, pkiDir))
	a := r.app
	base := "http://" + a.HTTPAddr()

	if code, body := getJSON(t, base+"/healthz"); code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz = %d %v", code, body)
	}
	code, body := getJSON(t, base+"/readyz")
	if code != http.StatusOK {
		t.Fatalf("readyz = %d %v", code, body)
	}
	checks, _ := body["checks"].(map[string]any)
	for _, name := range []string{"postgres", "postgres_platform", "valkey", "nats", "shutdown"} {
		if checks[name] != "ok" {
			t.Errorf("readyz check %s = %v", name, checks[name])
		}
	}
	// /ws — за авторизацией.
	if resp, err := http.Get(base + "/ws"); err != nil { //nolint:noctx // тест
		t.Fatal(err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("/ws without token = %d, want 401", resp.StatusCode)
		}
	}

	ctx := context.Background()
	clubID, staffID := fixtures(t, e.db)
	staff := principal.Principal{UserID: staffID, ClubID: clubID, Role: "admin"}
	issued, err := a.Enroll.CreateCode(ctx, staff, enroll.CodeRequest{IP: netip.MustParseAddr("127.0.0.1")})
	if err != nil {
		t.Fatalf("CreateCode: %v", err)
	}

	p := newPC(t, pkiDir, a.GRPCAddr())
	anon := p.dial(t, "")
	ectx, ecancel := context.WithTimeout(ctx, 20*time.Second)
	resp, err := agentv1.NewEnrollmentServiceClient(anon).Enroll(ectx, &agentv1.EnrollRequest{
		EnrollmentCode: issued.Code, CsrPem: p.csr,
		Device: &agentv1.DeviceInfo{HardwareUuid: "U-1", MacAddress: "AA:BB:CC:00:00:01", Hostname: "DESKTOP-1",
			AgentVersion: "2.0.0", OsVersion: "Windows 11 Pro"},
	})
	ecancel()
	_ = anon.Close()
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if resp.GetClubId() != clubID || resp.GetComputerId() == 0 {
		t.Fatalf("Enroll response = %v", resp)
	}
	pcID := resp.GetComputerId()

	// Журнал «Агент зарегистрировал ПК» (хук enroll.Activity → activity.Record).
	var logged int
	if err := e.db.Owner.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'computer.registered'
		AND user_id = $2 AND ip_address IS NOT NULL`, clubID, staffID).Scan(&logged); err != nil || logged != 1 {
		t.Fatalf("computer.registered activity = %d (%v)", logged, err)
	}

	// Без клиентского сертификата поток недоступен.
	{
		conn := p.dial(t, "")
		sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
		st, err := agentv1.NewAgentServiceClient(conn).Connect(sctx)
		if err == nil {
			_, err = st.Recv()
		}
		scancel()
		_ = conn.Close()
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("Connect without certificate: %v", err)
		}
	}

	conn := p.dial(t, resp.GetCertificatePem())
	defer conn.Close()
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	st, err := agentv1.NewAgentServiceClient(conn).Connect(sctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := st.Send(hello()); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	msgs, errc := recv(st)
	select {
	case m := <-msgs:
		if m.GetWelcome() == nil || m.GetWelcome().GetComputerId() != pcID {
			t.Fatalf("expected Welcome for computer %d, got %v", pcID, m)
		}
	case err := <-errc:
		t.Fatalf("stream closed before Welcome: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no Welcome")
	}
	if err := st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{
		State: agentv1.ComputerState_COMPUTER_STATE_IDLE, Load: &agentv1.ComputerLoad{CpuBasisPoints: 1234},
	}}}); err != nil {
		t.Fatalf("send status: %v", err)
	}
	waitFor(t, "computer online in presence", 5*time.Second, func() bool {
		s, ok, err := a.Stream.Presence().Get(ctx, clubID, pcID)
		return err == nil && ok && s.Online && s.Load.CPUBasisPoints == 1234
	})
	var dbStatus string
	if err := e.db.Owner.QueryRow(ctx, `SELECT status FROM club_computers WHERE id = $1`, pcID).Scan(&dbStatus); err != nil || dbStatus != "online" {
		t.Errorf("club_computers.status = %q (%v), want online", dbStatus, err)
	}

	// Отзыв: RevokeComputer → OnRevoked → NATS → CertCache.MarkRevoked → Kick → поток закрыт.
	if n, err := a.Enroll.RevokeComputer(ctx, staff, pcID, netip.MustParseAddr("127.0.0.1")); err != nil || n != 1 {
		t.Fatalf("RevokeComputer = %d, %v", n, err)
	}
	select {
	case err := <-errc:
		if status.Code(err) != codes.Unauthenticated || !strings.Contains(fmt.Sprint(err), "revoked") {
			t.Fatalf("stream after revoke: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream was not closed after revoke")
	}
	scancel()
	_ = conn.Close()

	if err := r.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	http.DefaultClient.CloseIdleConnections()
}

// TestAppDrainOnShutdown — плановая остановка: агент получает Drain, затем поток закрывается, Run
// возвращает nil в пределах бюджета.
func TestAppDrainOnShutdown(t *testing.T) {
	e := startEnv(t)
	pkiDir := filepath.Join(t.TempDir(), "pki")
	r := start(t, e.config(t, pkiDir))
	a := r.app
	ctx := context.Background()
	clubID, staffID := fixtures(t, e.db)
	issued, err := a.Enroll.CreateCode(ctx, principal.Principal{UserID: staffID, ClubID: clubID, Role: "admin"},
		enroll.CodeRequest{IP: netip.MustParseAddr("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	p := newPC(t, pkiDir, a.GRPCAddr())
	anon := p.dial(t, "")
	defer anon.Close()
	resp, err := agentv1.NewEnrollmentServiceClient(anon).Enroll(ctx, &agentv1.EnrollRequest{
		EnrollmentCode: issued.Code, CsrPem: p.csr,
		Device: &agentv1.DeviceInfo{HardwareUuid: "U-2", MacAddress: "AA:BB:CC:00:00:02", Hostname: "DESKTOP-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := p.dial(t, resp.GetCertificatePem())
	defer conn.Close()
	st, err := agentv1.NewAgentServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(hello()); err != nil {
		t.Fatal(err)
	}
	msgs, errc := recv(st)
	if m := <-msgs; m.GetWelcome() == nil {
		t.Fatalf("expected Welcome, got %v", m)
	}

	stopped := make(chan error, 1)
	go func() { stopped <- r.stop() }()
	gotDrain := false
	for !gotDrain {
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatalf("stream closed without Drain: %v", <-errc)
			}
			gotDrain = m.GetCommand().GetDrain() != nil
		case <-time.After(10 * time.Second):
			t.Fatal("no Drain command")
		}
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// TestBuildRejectsSuperuserDSN — логин приложения не должен обходить RLS.
func TestBuildRejectsSuperuserDSN(t *testing.T) {
	e := startEnv(t)
	owner := e.db.Owner.Config().ConnString()
	cfg := e.config(t, filepath.Join(t.TempDir(), "pki"))
	cfg.Database.URL = config.Secret(owner)
	_, err := Build(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "обходит RLS") {
		t.Fatalf("Build with superuser DSN: %v", err)
	}
}
