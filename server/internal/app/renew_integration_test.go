//go:build integration

package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// connectPC открывает поток Connect, шлёт Hello и ждёт Welcome.
func connectPC(ctx context.Context, t *testing.T, p *pc, certPEM string, pcID int64) (cancel func(), errc <-chan error) {
	t.Helper()
	conn := p.dial(t, certPEM)
	sctx, scancel := context.WithCancel(ctx)
	st, err := agentv1.NewAgentServiceClient(conn).Connect(sctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := st.Send(hello()); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	msgs, ec := recv(st)
	select {
	case m := <-msgs:
		if m.GetWelcome() == nil || m.GetWelcome().GetComputerId() != pcID {
			t.Fatalf("expected Welcome for computer %d, got %v", pcID, m)
		}
	case err := <-ec:
		t.Fatalf("stream closed before Welcome: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no Welcome")
	}
	go func() {
		for range msgs { //nolint:revive // слив сообщений, чтобы recv не блокировался
		}
	}()
	return func() { scancel(); _ = conn.Close() }, ec
}

// TestRenewedStreamSurvivesSupersede — продление: новый поток (новый сертификат) не закрывается отзывом прежнего
// сертификата (superseded при первом использовании нового). Раньше Kick по отзыву закрывал поток ПК по computer_id —
// и, если новый поток уже открыт, именно его (агент получал CERTIFICATE_REVOKED и удалял новый ключ).
// «Отключить ПК» по-прежнему закрывает поток.
func TestRenewedStreamSurvivesSupersede(t *testing.T) {
	e := startEnv(t)
	pkiDir := filepath.Join(t.TempDir(), "pki")
	r := start(t, e.config(t, pkiDir))
	a := r.app
	ctx := context.Background()
	clubID, staffID := fixtures(t, e.db)
	staff := principal.Principal{UserID: staffID, ClubID: clubID, Role: "admin"}
	issued, err := a.Enroll.CreateCode(ctx, staff, enroll.CodeRequest{IP: netip.MustParseAddr("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	device := &agentv1.DeviceInfo{HardwareUuid: "U-3", MacAddress: "AA:BB:CC:00:00:03", Hostname: "DESKTOP-3",
		AgentVersion: "2.0.0", OsVersion: "Windows 11 Pro"}
	oldKey := newPC(t, pkiDir, a.GRPCAddr())
	anon := oldKey.dial(t, "")
	resp, err := agentv1.NewEnrollmentServiceClient(anon).Enroll(ctx, &agentv1.EnrollRequest{
		EnrollmentCode: issued.Code, CsrPem: oldKey.csr, Device: device})
	_ = anon.Close()
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	pcID := resp.GetComputerId()
	var oldSerial string
	if err := e.db.Owner.QueryRow(ctx, `SELECT serial FROM agent_certificates WHERE club_computer_id = $1`, pcID).Scan(&oldSerial); err != nil {
		t.Fatal(err)
	}

	// Поток на прежнем сертификате; по тому же каналу — RenewCertificate новым ключом.
	stop1, errc1 := connectPC(ctx, t, oldKey, resp.GetCertificatePem(), pcID)
	defer stop1()
	newKey := newPC(t, pkiDir, a.GRPCAddr())
	conn1 := oldKey.dial(t, resp.GetCertificatePem())
	renewed, err := agentv1.NewEnrollmentServiceClient(conn1).RenewCertificate(ctx, &agentv1.RenewCertificateRequest{
		CsrPem: newKey.csr, Device: device})
	_ = conn1.Close()
	if err != nil {
		t.Fatalf("RenewCertificate: %v", err)
	}

	// Новый поток с новым сертификатом → прежний сертификат отзывается (superseded), прежний поток закрыт.
	stop2, errc2 := connectPC(ctx, t, newKey, renewed.GetCertificatePem(), pcID)
	defer stop2()
	waitFor(t, "old certificate superseded", 10*time.Second, func() bool {
		var reason *string
		err := e.db.Owner.QueryRow(ctx, `SELECT revoke_reason FROM agent_certificates WHERE serial = $1`, oldSerial).Scan(&reason)
		return err == nil && reason != nil && *reason == "superseded"
	})
	select {
	case <-errc1:
	case <-time.After(10 * time.Second):
		t.Fatal("old stream was not closed")
	}

	// Событие отзыва прежнего сертификата приходит (ещё раз) уже при открытом новом потоке — как из NATS
	// другого инстанса или CertCache.Refresh: новый поток жив.
	kickRevoked(ctx, a.Stream, slog.New(slog.NewTextHandler(io.Discard, nil)))(
		enroll.RevokedCert{Serial: oldSerial, ClubID: clubID, ComputerID: pcID})
	select {
	case err := <-errc2:
		t.Fatalf("renewed stream closed by revocation of the previous certificate: %v", err)
	case <-time.After(2 * time.Second):
	}

	// «Отключить ПК» отзывает новый сертификат — поток закрывается с CERTIFICATE_REVOKED.
	if n, err := a.Enroll.RevokeComputer(ctx, staff, pcID, netip.MustParseAddr("127.0.0.1")); err != nil || n != 1 {
		t.Fatalf("RevokeComputer = %d, %v", n, err)
	}
	select {
	case err := <-errc2:
		if status.Code(err) != codes.Unauthenticated || !strings.Contains(fmt.Sprint(err), "revoked") {
			t.Fatalf("stream after revoke: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream was not closed after revoke")
	}
	stop1()
	stop2()
	if err := r.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
