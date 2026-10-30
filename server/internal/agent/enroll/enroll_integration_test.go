//go:build integration

package enroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
)

// stubAgent — защищённый метод-заглушка (AgentService регистрирует агент A3): отдаёт идентичность ПК.
type stubAgent struct {
	agentv1.UnimplementedAgentServiceServer
}

func (stubAgent) GetTranslations(ctx context.Context, _ *agentv1.GetTranslationsRequest) (*agentv1.GetTranslationsResponse, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Internal, "no identity")
	}
	return &agentv1.GetTranslationsResponse{Lines: map[string]string{
		"club": strconv.FormatInt(id.ClubID, 10), "computer": strconv.FormatInt(id.ComputerID, 10), "serial": id.CertSerial,
	}}, nil
}

type harness struct {
	d      *dbtest.DB
	svc    *Service
	cache  *CertCache
	addr   string
	roots  *x509.CertPool
	clubID int64
	staff  principal.Principal
	mu     sync.Mutex
	gone   []RevokedCert
}

var sharedDB *dbtest.DB

func TestMain(m *testing.M) {
	d, cleanup, err := dbtest.Start(context.Background())
	if err != nil {
		println(err.Error())
		os.Exit(1)
	}
	if _, err := db.EnsurePartitions(context.Background(), d.Owner, time.Now(), 2); err != nil {
		println(err.Error())
		cleanup()
		os.Exit(1)
	}
	sharedDB = d
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func newHarness(t *testing.T, planFeatures string) *harness {
	t.Helper()
	ctx := context.Background()
	d := sharedDB
	h := &harness{d: d}
	h.clubID = d.Club(t, "club-"+randomSuffix(t))
	var planID, staffID int64
	mustQueryRow(t, d, &planID, `INSERT INTO saas_plans (slug, name, features, created_at, updated_at)
		VALUES ($1, 'План', $2::jsonb, now(), now()) RETURNING id`, "plan-"+randomSuffix(t), planFeatures)
	mustExec(t, d, `INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, current_period_ends_at)
		VALUES ($1, $2, 'active', now(), now() + interval '30 days')`, h.clubID, planID)
	mustQueryRow(t, d, &staffID, `INSERT INTO users (name, phone, password, created_at, updated_at)
		VALUES ('Админ', '+7' || lpad(floor(random()*1e10)::bigint::text, 10, '0'), 'x', now(), now()) RETURNING id`)
	mustExec(t, d, `INSERT INTO club_memberships (club_id, user_id, role, created_at, updated_at) VALUES ($1, $2, 'admin', now(), now())`,
		h.clubID, staffID)
	h.staff = principal.Principal{UserID: staffID, ClubID: h.clubID, Role: "admin"}

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
	rootPEM, _ := os.ReadFile(filepath.Join(dir, pki.DevRootCertFile))
	h.roots = x509.NewCertPool()
	h.roots.AppendCertsFromPEM(rootPEM)

	h.cache = NewCertCache(d.Platform, CacheOptions{})
	if err := h.cache.Load(ctx); err != nil {
		t.Fatal(err)
	}
	h.cache.OnRevoked(func(r RevokedCert) { h.mu.Lock(); h.gone = append(h.gone, r); h.mu.Unlock() })
	h.svc, err = New(Options{
		Pool: d.App, CA: ca, CodePepper: []byte("0123456789abcdef0123456789abcdef"), AgentEndpoint: "agent.test:443",
		Cache: h.cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := grpcserver.NewServer(grpcserver.Options{
		ServerCertificate: srvCert, ClientCAs: ca.ClientCAs(), Certs: h.cache, Enrollment: NewGRPC(h.svc),
	}, func(s *grpc.Server) { agentv1.RegisterAgentServiceServer(s, stubAgent{}) })
	if err != nil {
		t.Fatal(err)
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

func (h *harness) code(t *testing.T, req CodeRequest) string {
	t.Helper()
	c, err := h.svc.CreateCode(context.Background(), h.staff, req)
	if err != nil {
		t.Fatal(err)
	}
	return c.Code
}

func (h *harness) dial(t *testing.T, cert *tls.Certificate) *grpc.ClientConn {
	t.Helper()
	cfg := &tls.Config{RootCAs: h.roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	conn, err := grpc.NewClient(h.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// pcKey — ключ и CSR, как их делает служба агента (ECDSA P-256).
type pcKey struct {
	key *ecdsa.PrivateKey
	csr string
}

func newPCKey(t *testing.T) pcKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, k)
	if err != nil {
		t.Fatal(err)
	}
	return pcKey{key: k, csr: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))}
}

func (k pcKey) cert(t *testing.T, certPEM string) *tls.Certificate {
	t.Helper()
	c, err := pki.ParseCertificatePEM([]byte(certPEM))
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{c.Raw}, PrivateKey: k.key, Leaf: c}
}

func dev(uuid, mac string) *agentv1.DeviceInfo {
	return &agentv1.DeviceInfo{HardwareUuid: uuid, MacAddress: mac, Hostname: "DESKTOP-" + uuid, AgentVersion: "2.0.0",
		CpuModel: "Ryzen 5", CpuCores: 12, RamTotalMb: 16384, OsVersion: "Windows 11 Pro"}
}

func (h *harness) enroll(t *testing.T, code string, k pcKey, d *agentv1.DeviceInfo) (*agentv1.EnrollResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return agentv1.NewEnrollmentServiceClient(h.dial(t, nil)).Enroll(ctx, &agentv1.EnrollRequest{
		EnrollmentCode: code, CsrPem: k.csr, Device: d})
}

func whoami(t *testing.T, conn *grpc.ClientConn) (map[string]string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := agentv1.NewAgentServiceClient(conn).GetTranslations(ctx, &agentv1.GetTranslationsRequest{})
	return resp.GetLines(), err
}

func wantReason(t *testing.T, err error, c codes.Code, reason string) {
	t.Helper()
	if status.Code(err) != c || grpcserver.ReasonOf(err) != reason {
		t.Fatalf("err = %v (reason %q), want %v %s", err, grpcserver.ReasonOf(err), c, reason)
	}
}

// Полный цикл через настоящий TLS: код → Enroll с CSR → защищённый метод с сертификатом → отзыв → отказ.
func TestEnrollFullCycle(t *testing.T) {
	h := newHarness(t, `["Поддержка"]`)
	k := newPCKey(t)
	code := h.code(t, CodeRequest{})
	resp, err := h.enroll(t, code, k, dev("UUID-1", "aa-bb-cc-00-00-01"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetClubId() != h.clubID || resp.GetComputerId() == 0 || !resp.GetComputerCreated() ||
		resp.GetComputerNumber() != "1" || resp.GetAgentEndpoint() != "agent.test:443" || len(resp.GetCaChainPem()) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
	leaf, _ := pki.ParseCertificatePEM([]byte(resp.GetCertificatePem()))
	if !k.key.PublicKey.Equal(leaf.PublicKey) {
		t.Fatal("certificate is not for the CSR key")
	}
	// Карточка создана в зоне «Не назначена» с MAC и железом.
	var zone, mac, deviceID string
	var cores int32
	mustQueryRow(t, h.d, &zone, `SELECT z.name FROM club_computers c JOIN club_zones z ON z.id = c.club_zone_id WHERE c.id = $1`, resp.GetComputerId())
	mustQueryRow(t, h.d, &mac, `SELECT mac_address FROM club_computers WHERE id = $1`, resp.GetComputerId())
	mustQueryRow(t, h.d, &deviceID, `SELECT device_id FROM club_computers WHERE id = $1`, resp.GetComputerId())
	mustQueryRow(t, h.d, &cores, `SELECT cpu_cores FROM club_computers WHERE id = $1`, resp.GetComputerId())
	if zone != "Не назначена" || mac != "AA:BB:CC:00:00:01" || deviceID != "UUID-1" || cores != 12 {
		t.Fatalf("card: zone=%q mac=%q device=%q cores=%d", zone, mac, deviceID, cores)
	}
	// В базе — только хеш кода; аудит без кода.
	var leaks int
	mustQueryRow(t, h.d, &leaks, `SELECT count(*) FROM audit_log WHERE after::text LIKE '%' || $1 || '%'`, code[:4]+code[5:])
	if leaks != 0 {
		t.Fatal("enrollment code leaked into audit_log")
	}

	conn := h.dial(t, k.cert(t, resp.GetCertificatePem()))
	lines, err := whoami(t, conn)
	if err != nil {
		t.Fatal(err)
	}
	if lines["club"] != strconv.FormatInt(h.clubID, 10) || lines["computer"] != strconv.FormatInt(resp.GetComputerId(), 10) ||
		lines["serial"] != leaf.SerialNumber.Text(16) {
		t.Fatalf("identity = %v", lines)
	}

	t.Run("повтор кода — отказ", func(t *testing.T) {
		_, err := h.enroll(t, code, newPCKey(t), dev("UUID-1", "aa-bb-cc-00-00-01"))
		wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_EXPIRED")
	})
	t.Run("неверный код", func(t *testing.T) {
		_, err := h.enroll(t, "ZZZZ-ZZZZ", newPCKey(t), dev("UUID-x", "aa-bb-cc-00-00-99"))
		wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_INVALID")
		_, err = h.enroll(t, "not a code", newPCKey(t), dev("UUID-x", "aa-bb-cc-00-00-99"))
		wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_INVALID")
	})
	t.Run("отзыв — отказ на открытом соединении и новом", func(t *testing.T) {
		n, err := h.svc.RevokeComputer(context.Background(), h.staff, resp.GetComputerId(), netip.Addr{})
		if err != nil || n != 1 {
			t.Fatalf("revoke: %d %v", n, err)
		}
		_, err = whoami(t, conn)
		wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_CERTIFICATE_REVOKED")
		_, err = whoami(t, h.dial(t, k.cert(t, resp.GetCertificatePem())))
		wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_CERTIFICATE_REVOKED")
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.gone) != 1 || h.gone[0].ComputerID != resp.GetComputerId() {
			t.Fatalf("revoke callbacks = %v", h.gone)
		}
	})
	t.Run("отзыв виден другому инстансу после Refresh", func(t *testing.T) {
		other := NewCertCache(h.d.Platform, CacheOptions{})
		if err := other.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		err := other.CheckCertificate(context.Background(), leaf.SerialNumber.Text(16), h.clubID, resp.GetComputerId(), leaf.NotAfter)
		if !errors.Is(err, grpcserver.ErrCertRevoked) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("кассир не выдаёт коды", func(t *testing.T) {
		_, err := h.svc.CreateCode(context.Background(), principal.Principal{UserID: h.staff.UserID, ClubID: h.clubID, Role: "cashier"}, CodeRequest{})
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("err = %v", err)
		}
	})
}

// Гонка двух Enroll с одним кодом — ровно один успех.
func TestEnrollRaceOneCode(t *testing.T) {
	h := newHarness(t, `{}`)
	code := h.code(t, CodeRequest{})
	const n = 2
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = h.enroll(t, code, newPCKey(t), dev("RACE-"+strconv.Itoa(i), "aa-bb-cc-00-01-0"+strconv.Itoa(i)))
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else {
			wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_EXPIRED")
		}
	}
	if ok != 1 {
		t.Fatalf("successes = %d, errs = %v", ok, errs)
	}
	var cards int
	mustQueryRow(t, h.d, &cards, `SELECT count(*) FROM club_computers WHERE club_id = $1`, h.clubID)
	if cards != 1 {
		t.Fatalf("cards = %d", cards)
	}
}

// Повторный Enroll того же ПК (тот же MAC) — та же карточка, прежний сертификат отозван (reenrolled).
func TestReenrollSameMAC(t *testing.T) {
	h := newHarness(t, `{}`)
	k1 := newPCKey(t)
	r1, err := h.enroll(t, h.code(t, CodeRequest{}), k1, dev("IMG-UUID", "AA:BB:CC:00:02:01"))
	if err != nil {
		t.Fatal(err)
	}
	conn1 := h.dial(t, k1.cert(t, r1.GetCertificatePem()))
	if _, err := whoami(t, conn1); err != nil {
		t.Fatal(err)
	}
	k2 := newPCKey(t)
	r2, err := h.enroll(t, h.code(t, CodeRequest{}), k2, dev("IMG-UUID", "aa:bb:cc:00:02:01"))
	if err != nil {
		t.Fatal(err)
	}
	if r2.GetComputerId() != r1.GetComputerId() || r2.GetComputerCreated() {
		t.Fatalf("second enroll: %+v", r2)
	}
	_, err = whoami(t, conn1)
	wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_CERTIFICATE_REVOKED")
	if _, err := whoami(t, h.dial(t, k2.cert(t, r2.GetCertificatePem()))); err != nil {
		t.Fatal(err)
	}

	// ПК того же образа (тот же UUID, другой MAC) — новая карточка, device_id = MAC-… (BR-124).
	r3, err := h.enroll(t, h.code(t, CodeRequest{}), newPCKey(t), dev("IMG-UUID", "AA:BB:CC:00:02:02"))
	if err != nil {
		t.Fatal(err)
	}
	var deviceID string
	mustQueryRow(t, h.d, &deviceID, `SELECT device_id FROM club_computers WHERE id = $1`, r3.GetComputerId())
	if !r3.GetComputerCreated() || r3.GetComputerId() == r1.GetComputerId() || deviceID != "MAC-AABBCC000202" || r3.GetComputerNumber() != "2" {
		t.Fatalf("image clone: %+v device_id=%q", r3, deviceID)
	}
}

// Перерегистрация: код для конкретной карточки, прежний сертификат отозван сразу.
func TestReregister(t *testing.T) {
	h := newHarness(t, `{}`)
	k := newPCKey(t)
	r, err := h.enroll(t, h.code(t, CodeRequest{}), k, dev("RR-1", "AA:BB:CC:00:03:01"))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := h.svc.Reregister(context.Background(), h.staff, r.GetComputerId(), netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = whoami(t, h.dial(t, k.cert(t, r.GetCertificatePem())))
	wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_CERTIFICATE_REVOKED")
	// Новый ПК (другое железо) занимает карточку по коду перерегистрации.
	r2, err := h.enroll(t, issued.Code, newPCKey(t), dev("RR-2", ""))
	if err != nil {
		t.Fatal(err)
	}
	if r2.GetComputerId() != r.GetComputerId() || r2.GetComputerCreated() {
		t.Fatalf("reregister: %+v", r2)
	}
}

// Подписка неактивна — отказ, код не гасится; лимит ПК тарифа; зона без мест.
func TestEnrollPreconditions(t *testing.T) {
	h := newHarness(t, `{"max_computers": 1}`)
	code := h.code(t, CodeRequest{})
	mustExec(t, h.d, `UPDATE club_subscriptions SET status = 'expired' WHERE club_id = $1`, h.clubID)
	_, err := h.enroll(t, code, newPCKey(t), dev("P-1", "AA:BB:CC:00:04:01"))
	wantReason(t, err, codes.FailedPrecondition, "ERROR_CODE_SUBSCRIPTION_INACTIVE")
	mustExec(t, h.d, `UPDATE club_subscriptions SET status = 'active' WHERE club_id = $1`, h.clubID)
	if _, err := h.enroll(t, code, newPCKey(t), dev("P-1", "AA:BB:CC:00:04:01")); err != nil {
		t.Fatalf("code must survive a failed enroll: %v", err)
	}
	_, err = h.enroll(t, h.code(t, CodeRequest{}), newPCKey(t), dev("P-2", "AA:BB:CC:00:04:02"))
	wantReason(t, err, codes.FailedPrecondition, "ERROR_CODE_COMPUTER_LIMIT_REACHED")

	h2 := newHarness(t, `{}`)
	var zoneID int64
	mustQueryRow(t, h2.d, &zoneID, `INSERT INTO club_zones (club_id, name, capacity, created_at, updated_at)
		VALUES ($1, 'VIP', 0, now(), now()) RETURNING id`, h2.clubID)
	_, err = h2.enroll(t, h2.code(t, CodeRequest{ZoneID: &zoneID}), newPCKey(t), dev("Z-1", "AA:BB:CC:00:04:03"))
	wantReason(t, err, codes.InvalidArgument, "ERROR_CODE_VALIDATION_FAILED")

	_, err = h2.enroll(t, h2.code(t, CodeRequest{}), pcKey{csr: "garbage"}, dev("Z-2", "AA:BB:CC:00:04:04"))
	wantReason(t, err, codes.InvalidArgument, "ERROR_CODE_CSR_INVALID")
}

// Продление: новый ключ → новый сертификат; первое использование нового отзывает прежний.
func TestRenewCertificate(t *testing.T) {
	h := newHarness(t, `{}`)
	k1 := newPCKey(t)
	d := dev("RN-1", "AA:BB:CC:00:05:01")
	r, err := h.enroll(t, h.code(t, CodeRequest{}), k1, d)
	if err != nil {
		t.Fatal(err)
	}
	conn1 := h.dial(t, k1.cert(t, r.GetCertificatePem()))
	cli := agentv1.NewEnrollmentServiceClient(conn1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err = cli.RenewCertificate(ctx, &agentv1.RenewCertificateRequest{CsrPem: k1.csr, Device: d})
	wantReason(t, err, codes.InvalidArgument, "ERROR_CODE_CSR_INVALID") // тот же ключ
	_, err = cli.RenewCertificate(ctx, &agentv1.RenewCertificateRequest{CsrPem: newPCKey(t).csr, Device: dev("OTHER", "11:22:33:44:55:66")})
	wantReason(t, err, codes.PermissionDenied, "ERROR_CODE_DEVICE_MISMATCH")
	_, err = agentv1.NewEnrollmentServiceClient(h.dial(t, nil)).RenewCertificate(ctx, &agentv1.RenewCertificateRequest{CsrPem: newPCKey(t).csr, Device: d})
	wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_CERTIFICATE_REQUIRED") // без сертификата

	k2 := newPCKey(t)
	rn, err := cli.RenewCertificate(ctx, &agentv1.RenewCertificateRequest{CsrPem: k2.csr, Device: d})
	if err != nil {
		t.Fatal(err)
	}
	if rn.GetPreviousValidFor().AsDuration() <= 0 || !rn.GetRenewAfter().AsTime().Before(rn.GetNotAfter().AsTime()) {
		t.Fatalf("renew resp = %+v", rn)
	}
	// Прежний действует, пока новый не использован.
	if _, err := whoami(t, conn1); err != nil {
		t.Fatalf("old cert before first use of new: %v", err)
	}
	conn2 := h.dial(t, k2.cert(t, rn.GetCertificatePem()))
	if _, err := whoami(t, conn2); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err = whoami(t, conn1)
		if err != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_CERTIFICATE_REVOKED")
	var reason string
	mustQueryRow(t, h.d, &reason, `SELECT revoke_reason FROM agent_certificates WHERE club_computer_id = $1 AND issued_via = 'enroll'`, r.GetComputerId())
	if reason != "superseded" {
		t.Fatalf("reason = %q", reason)
	}
}

// Удаление карточки ПК отзывает её сертификаты (триггер) — кэш узнаёт при Refresh.
func TestDeleteComputerRevokes(t *testing.T) {
	h := newHarness(t, `{}`)
	k := newPCKey(t)
	r, err := h.enroll(t, h.code(t, CodeRequest{}), k, dev("DEL-1", "AA:BB:CC:00:06:01"))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.d, `DELETE FROM club_computers WHERE id = $1`, r.GetComputerId())
	if err := h.cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = whoami(t, h.dial(t, k.cert(t, r.GetCertificatePem())))
	wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_CERTIFICATE_REVOKED")
}

// RLS: коды и сертификаты не видны без клуба, коды чужого клуба не видны; таблицы под RLS.
func TestRLS(t *testing.T) {
	h := newHarness(t, `{}`)
	h.code(t, CodeRequest{})
	ctx := context.Background()
	var n int
	if err := h.d.App.QueryRow(ctx, `SELECT count(*) FROM agent_enrollment_codes`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("codes without club: %d %v", n, err)
	}
	other := newHarness(t, `{}`)
	tx, err := h.d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.club_id', $1, true)`, strconv.FormatInt(other.clubID, 10)); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_enrollment_codes WHERE club_id = $1`, h.clubID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("foreign club codes visible: %d %v", n, err)
	}
	rows, err := h.d.Owner.Query(ctx, `SELECT * FROM zafiro_tables_without_rls()`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		t.Errorf("table without RLS: %s", name)
	}
}

func mustExec(t *testing.T, d *dbtest.DB, sql string, args ...any) {
	t.Helper()
	if _, err := d.Owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func mustQueryRow(t *testing.T, d *dbtest.DB, dst any, sql string, args ...any) {
	t.Helper()
	if err := d.Owner.QueryRow(context.Background(), sql, args...).Scan(dst); err != nil {
		t.Fatal(err)
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return strconv.FormatUint(uint64(b[0])<<24|uint64(b[1])<<16|uint64(b[2])<<8|uint64(b[3]), 36)
}
