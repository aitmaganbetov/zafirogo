package grpcserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
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
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
)

// stubAgent — защищённый метод-заглушка: возвращает идентичность из контекста.
type stubAgent struct {
	agentv1.UnimplementedAgentServiceServer
}

func (stubAgent) GetTranslations(ctx context.Context, _ *agentv1.GetTranslationsRequest) (*agentv1.GetTranslationsResponse, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Internal, "no identity")
	}
	return &agentv1.GetTranslationsResponse{Lines: map[string]string{
		"club":     strconv.FormatInt(id.ClubID, 10),
		"computer": strconv.FormatInt(id.ComputerID, 10),
		"serial":   id.CertSerial,
	}}, nil
}

// stubEnroll — Enroll без сертификата.
type stubEnroll struct {
	agentv1.UnimplementedEnrollmentServiceServer
}

func (stubEnroll) Enroll(ctx context.Context, _ *agentv1.EnrollRequest) (*agentv1.EnrollResponse, error) {
	if _, ok := identity.FromContext(ctx); ok {
		return nil, status.Error(codes.Internal, "identity on public method")
	}
	return &agentv1.EnrollResponse{ClubName: "ok"}, nil
}

type fakeCerts struct {
	mu      sync.Mutex
	revoked map[string]bool
}

func (f *fakeCerts) CheckCertificate(_ context.Context, serial string, _, _ int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revoked[serial] {
		return ErrCertRevoked
	}
	return nil
}

type env struct {
	ca    *pki.CA
	addr  string
	roots *x509.CertPool
	certs *fakeCerts
}

func startServer(t *testing.T) *env {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pki")
	opts := pki.Options{DevDir: dir}
	ca, err := pki.LoadCA(opts)
	if err != nil {
		t.Fatal(err)
	}
	srvCert, err := pki.LoadServerCertificate(opts)
	if err != nil {
		t.Fatal(err)
	}
	rootPEM, err := os.ReadFile(filepath.Join(dir, pki.DevRootCertFile))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(rootPEM)
	certs := &fakeCerts{revoked: map[string]bool{}}
	srv, err := NewServer(Options{
		ServerCertificate: srvCert,
		ClientCAs:         ca.ClientCAs(),
		Certs:             certs,
		Enrollment:        stubEnroll{},
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
	return &env{ca: ca, addr: lis.Addr().String(), roots: roots, certs: certs}
}

func (e *env) dial(t *testing.T, cert *tls.Certificate) *grpc.ClientConn {
	t.Helper()
	cfg := &tls.Config{RootCAs: e.roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	conn, err := grpc.NewClient(e.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func (e *env) clientCert(t *testing.T, ca *pki.CA, club, computer int64) (tls.Certificate, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := pki.ParseCSR(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	iss, err := ca.Issue(pki.IssueRequest{CSR: csr, ClubID: club, ComputerID: computer})
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{iss.Cert.Raw}, PrivateKey: key, Leaf: iss.Cert}, iss.Serial
}

func TestMTLS(t *testing.T) {
	e := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	t.Run("Enroll без сертификата", func(t *testing.T) {
		resp, err := agentv1.NewEnrollmentServiceClient(e.dial(t, nil)).Enroll(ctx, &agentv1.EnrollRequest{})
		if err != nil || resp.GetClubName() != "ok" {
			t.Fatalf("Enroll: %v", err)
		}
	})
	t.Run("защищённый метод без сертификата", func(t *testing.T) {
		_, err := agentv1.NewAgentServiceClient(e.dial(t, nil)).GetTranslations(ctx, &agentv1.GetTranslationsRequest{})
		if status.Code(err) != codes.Unauthenticated || ReasonOf(err) != "ERROR_CODE_CERTIFICATE_EXPIRED" {
			t.Fatalf("err = %v (%s)", err, ReasonOf(err))
		}
	})
	cert, serial := e.clientCert(t, e.ca, 5, 77)
	conn := e.dial(t, &cert)
	t.Run("с сертификатом — идентичность в контексте", func(t *testing.T) {
		resp, err := agentv1.NewAgentServiceClient(conn).GetTranslations(ctx, &agentv1.GetTranslationsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		l := resp.GetLines()
		if l["club"] != "5" || l["computer"] != "77" || l["serial"] != serial {
			t.Fatalf("identity = %v", l)
		}
	})
	t.Run("Enroll с сертификатом не получает идентичность", func(t *testing.T) {
		if _, err := agentv1.NewEnrollmentServiceClient(conn).Enroll(ctx, &agentv1.EnrollRequest{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("отозван — отказ на том же соединении", func(t *testing.T) {
		e.certs.mu.Lock()
		e.certs.revoked[serial] = true
		e.certs.mu.Unlock()
		_, err := agentv1.NewAgentServiceClient(conn).GetTranslations(ctx, &agentv1.GetTranslationsRequest{})
		if status.Code(err) != codes.Unauthenticated || ReasonOf(err) != "ERROR_CODE_CERTIFICATE_REVOKED" {
			t.Fatalf("err = %v (%s)", err, ReasonOf(err))
		}
	})
	t.Run("сертификат чужого CA — отказ TLS", func(t *testing.T) {
		other, err := pki.LoadCA(pki.Options{DevDir: filepath.Join(t.TempDir(), "other")})
		if err != nil {
			t.Fatal(err)
		}
		foreign, _ := e.clientCert(t, other, 5, 77)
		_, err = agentv1.NewEnrollmentServiceClient(e.dial(t, &foreign)).Enroll(ctx, &agentv1.EnrollRequest{})
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestErrorDetails(t *testing.T) {
	err := RateLimited(1500*time.Millisecond, "Слишком много попыток.")
	st := status.Convert(err)
	if st.Code() != codes.ResourceExhausted || ReasonOf(err) != "ERROR_CODE_RATE_LIMITED" || len(st.Details()) != 3 {
		t.Fatalf("status = %v, details = %v", st, st.Details())
	}
	err = Validation("csr_pem", "Неверный CSR.")
	if status.Code(err) != codes.InvalidArgument || ReasonOf(err) != "ERROR_CODE_VALIDATION_FAILED" {
		t.Fatal(err)
	}
}
