//go:build integration

package app

import (
	"context"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
)

// TestAgentCARotation — S6-O1-1: смена выпускающего CA без обрыва ПК. После перезапуска с новым CA и прежним в
// agent.pki.extra_trust_files ПК с сертификатом прежнего CA подключается и продлевается (новый сертификат — от
// нового CA); без extra_trust_files прежний сертификат не принимается.
func TestAgentCARotation(t *testing.T) {
	e := startEnv(t)
	pkiDir := filepath.Join(t.TempDir(), "pki")
	ctx := context.Background()
	clubID, staffID := fixtures(t, e.db)
	staff := principal.Principal{UserID: staffID, ClubID: clubID, Role: "admin"}
	device := &agentv1.DeviceInfo{HardwareUuid: "U-R", MacAddress: "AA:BB:CC:00:00:0F", Hostname: "DESKTOP-R",
		AgentVersion: "2.0.0", OsVersion: "Windows 11 Pro"}

	// 1. Регистрация на прежнем CA (dev-PKI).
	r1 := start(t, e.config(t, pkiDir))
	code, err := r1.app.Enroll.CreateCode(ctx, staff, enroll.CodeRequest{IP: netip.MustParseAddr("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	oldKey := newPC(t, pkiDir, r1.app.GRPCAddr())
	anon := oldKey.dial(t, "")
	enrolled, err := agentv1.NewEnrollmentServiceClient(anon).Enroll(ctx, &agentv1.EnrollRequest{
		EnrollmentCode: code.Code, CsrPem: oldKey.csr, Device: device})
	_ = anon.Close()
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	pcID := enrolled.GetComputerId()
	if err := r1.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 2. Новый выпускающий CA под тем же корнем; прежний — в extra_trust_files.
	certFile, keyFile, err := pki.NewDevIssuingCA(pkiDir, "agent-ca-2")
	if err != nil {
		t.Fatal(err)
	}
	files := []string{
		"ZAFIRO_AGENT_PKI_CA_CERT_FILE=" + certFile, "ZAFIRO_AGENT_PKI_CA_KEY_FILE=" + keyFile,
		"ZAFIRO_AGENT_PKI_SERVER_CERT_FILE=" + filepath.Join(pkiDir, pki.DevServerCertFile),
		"ZAFIRO_AGENT_PKI_SERVER_KEY_FILE=" + filepath.Join(pkiDir, pki.DevServerKeyFile),
	}
	r2 := start(t, e.config(t, pkiDir, append(files, "ZAFIRO_AGENT_PKI_EXTRA_TRUST_FILES="+filepath.Join(pkiDir, pki.DevCACertFile))...))
	oldKey.addr = r2.app.GRPCAddr()
	stop, _ := connectPC(ctx, t, oldKey, enrolled.GetCertificatePem(), pcID) // прежний сертификат принят
	stop()

	newKey := newPC(t, pkiDir, r2.app.GRPCAddr())
	conn := oldKey.dial(t, enrolled.GetCertificatePem())
	renewed, err := agentv1.NewEnrollmentServiceClient(conn).RenewCertificate(ctx, &agentv1.RenewCertificateRequest{
		CsrPem: newKey.csr, Device: device})
	_ = conn.Close()
	if err != nil {
		t.Fatalf("RenewCertificate with the previous CA certificate: %v", err)
	}
	cert, err := pki.ParseCertificatePEM([]byte(renewed.GetCertificatePem()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cert.Issuer.CommonName, "agent-ca-2") {
		t.Fatalf("renewed certificate issuer = %q, want the new CA", cert.Issuer.CommonName)
	}
	if len(renewed.GetCaChainPem()) == 0 || !strings.Contains(renewed.GetCaChainPem()[0], "BEGIN CERTIFICATE") {
		t.Fatal("ca_chain_pem is empty")
	}
	stop, _ = connectPC(ctx, t, newKey, renewed.GetCertificatePem(), pcID)
	stop()
	if err := r2.stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 3. Прежний CA убран из доверенных: его сертификат не проходит рукопожатие, новый — проходит.
	r3 := start(t, e.config(t, pkiDir, files...))
	oldKey.addr, newKey.addr = r3.app.GRPCAddr(), r3.app.GRPCAddr()
	conn = oldKey.dial(t, enrolled.GetCertificatePem())
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st, err := agentv1.NewAgentServiceClient(conn).Connect(sctx)
	if err == nil {
		if err = st.Send(hello()); err == nil {
			_, err = st.Recv()
		}
	}
	_ = conn.Close()
	if err == nil {
		t.Fatal("certificate of the removed CA must be rejected")
	}
	stop, _ = connectPC(ctx, t, newKey, renewed.GetCertificatePem(), pcID)
	stop()
}
