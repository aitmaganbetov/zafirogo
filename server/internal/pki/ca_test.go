package pki

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func devCA(t *testing.T) (*CA, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pki")
	ca, err := LoadCA(Options{DevDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return ca, dir
}

func makeCSR(t *testing.T, key crypto.Signer) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "ignored-by-server"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func p256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestDevPKIFilePermissions(t *testing.T) {
	_, dir := devCA(t)
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", st.Mode().Perm())
	}
	for _, f := range []string{DevRootKeyFile, DevCAKeyFile, DevServerKeyFile, DevCACertFile} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", f, st.Mode().Perm())
		}
	}
	// Повторная загрузка не перегенерирует CA.
	a, err := LoadCA(Options{DevDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadCA(Options{DevDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Certificate().Equal(b.Certificate()) {
		t.Error("dev CA regenerated on second load")
	}
	if len(a.ChainPEM()) != 2 {
		t.Errorf("chain = %d certs, want issuing + root", len(a.ChainPEM()))
	}
	if _, err := LoadServerCertificate(Options{DevDir: dir}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCARejectsMismatchedKey(t *testing.T) {
	_, dir := devCA(t)
	certPEM, _ := os.ReadFile(filepath.Join(dir, DevCACertFile))
	otherKey, _ := os.ReadFile(filepath.Join(dir, DevServerKeyFile))
	if _, err := LoadCAFromPEM(certPEM, otherKey); err == nil {
		t.Fatal("CA with foreign key accepted")
	}
	if _, err := LoadCA(Options{}); err == nil {
		t.Fatal("unconfigured CA accepted")
	}
}

func TestIssueAndVerify(t *testing.T) {
	ca, _ := devCA(t)
	key := p256(t)
	csr, err := ParseCSR(makeCSR(t, key))
	if err != nil {
		t.Fatal(err)
	}
	iss, err := ca.Issue(IssueRequest{CSR: csr, ClubID: 7, ComputerID: 42, Lifetime: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	c := iss.Cert
	if c.Subject.CommonName != "computer:42" || c.Subject.Organization[0] != "club:7" {
		t.Errorf("subject = %v", c.Subject)
	}
	if len(c.URIs) != 1 || c.URIs[0].String() != "zafiro://club/7/computer/42" {
		t.Errorf("SAN URI = %v", c.URIs)
	}
	if len(c.ExtKeyUsage) != 1 || c.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || c.IsCA {
		t.Errorf("EKU = %v, IsCA = %v", c.ExtKeyUsage, c.IsCA)
	}
	if c.SerialNumber.BitLen() > 128 || c.SerialNumber.Sign() <= 0 || iss.Serial != c.SerialNumber.Text(16) {
		t.Errorf("serial = %s", iss.Serial)
	}
	if !key.PublicKey.Equal(c.PublicKey) {
		t.Error("certificate key is not the CSR key")
	}
	if got := iss.NotAfter.Sub(iss.NotBefore); got != 30*24*time.Hour+clockSkew {
		t.Errorf("validity = %v", got)
	}
	if !iss.RenewAfter.After(iss.NotBefore) || !iss.RenewAfter.Before(iss.NotAfter) {
		t.Errorf("renew_after = %v", iss.RenewAfter)
	}
	spki := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	if string(spki[:]) != string(iss.SPKISHA256) {
		t.Error("spki hash")
	}
	club, pc, err := ca.Verify(c)
	if err != nil || club != 7 || pc != 42 {
		t.Fatalf("Verify = %d, %d, %v", club, pc, err)
	}
}

func TestSerialsAreRandom(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		n, err := RandomSerial()
		if err != nil {
			t.Fatal(err)
		}
		s := SerialString(n)
		if seen[s] {
			t.Fatal("duplicate serial")
		}
		seen[s] = true
	}
}

func TestVerifyExpired(t *testing.T) {
	ca, _ := devCA(t)
	csr, _ := ParseCSR(makeCSR(t, p256(t)))
	iss, err := ca.Issue(IssueRequest{CSR: csr, ClubID: 1, ComputerID: 2, Lifetime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ca.SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
	if _, _, err := ca.Verify(iss.Cert); !errors.Is(err, ErrCertificateExpired) {
		t.Fatalf("expired: err = %v", err)
	}
	ca.SetClock(func() time.Time { return time.Now().Add(-time.Hour) })
	if _, _, err := ca.Verify(iss.Cert); !errors.Is(err, ErrCertificateExpired) {
		t.Fatalf("not yet valid: err = %v", err)
	}
}

func TestVerifyForeignCA(t *testing.T) {
	ca, _ := devCA(t)
	other, _ := devCA(t)
	csr, _ := ParseCSR(makeCSR(t, p256(t)))
	iss, err := other.Issue(IssueRequest{CSR: csr, ClubID: 1, ComputerID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ca.Verify(iss.Cert); !errors.Is(err, ErrCertificateInvalid) {
		t.Fatalf("foreign CA: err = %v", err)
	}
}

// csrASN1 — структура PKCS#10 для подмены подписи.
type csrASN1 struct {
	TBS    asn1.RawValue
	SigAlg pkix.AlgorithmIdentifier
	Sig    asn1.BitString
}

func TestParseCSRRejects(t *testing.T) {
	good := makeCSR(t, p256(t))

	// Чужая подпись: CSR с ключом A, подписанный ключом B.
	block, _ := pem.Decode(good)
	var parsed csrASN1
	if _, err := asn1.Unmarshal(block.Bytes, &parsed); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(parsed.TBS.FullBytes)
	sig, err := ecdsa.SignASN1(rand.Reader, p256(t), h[:])
	if err != nil {
		t.Fatal(err)
	}
	parsed.Sig = asn1.BitString{Bytes: sig, BitLength: len(sig) * 8}
	forged, err := asn1.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}

	rsa2048, _ := rsa.GenerateKey(rand.Reader, 2048)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	_, ed, _ := ed25519.GenerateKey(rand.Reader)

	cases := map[string][]byte{
		"чужая подпись":     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: forged}),
		"RSA 2048":          makeCSR(t, rsa2048),
		"ECDSA P-384":       makeCSR(t, p384),
		"Ed25519":           makeCSR(t, ed),
		"пусто":             nil,
		"не PEM":            []byte("hello"),
		"сертификат вместо": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}),
		"два блока":         append(append([]byte{}, good...), good...),
		"слишком большой":   []byte(strings.Repeat("A", MaxCSRBytes+1)),
		"мусор в DER":       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte{0x30, 0x01, 0x00}}),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCSR(data); !errors.Is(err, ErrCSRInvalid) {
				t.Fatalf("err = %v, want ErrCSRInvalid", err)
			}
		})
	}
	if _, err := ParseCSR(good); err != nil {
		t.Fatalf("good CSR: %v", err)
	}
}

func TestParseCSRAcceptsRSA3072(t *testing.T) {
	if testing.Short() {
		t.Skip("RSA 3072 keygen")
	}
	k, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCSR(makeCSR(t, k)); err != nil {
		t.Fatal(err)
	}
}

func TestParseIdentityRejects(t *testing.T) {
	ca, _ := devCA(t)
	csr, _ := ParseCSR(makeCSR(t, p256(t)))
	iss, _ := ca.Issue(IssueRequest{CSR: csr, ClubID: 3, ComputerID: 9})
	c := *iss.Cert
	c.Subject.CommonName = "computer:10"
	if _, _, err := ParseIdentity(&c); !errors.Is(err, ErrCertificateInvalid) {
		t.Fatalf("CN mismatch: %v", err)
	}
	c = *iss.Cert
	c.URIs = nil
	if _, _, err := ParseIdentity(&c); !errors.Is(err, ErrCertificateInvalid) {
		t.Fatalf("no URI: %v", err)
	}
}

func TestSamePublicKey(t *testing.T) {
	ca, _ := devCA(t)
	k := p256(t)
	csr, _ := ParseCSR(makeCSR(t, k))
	iss, _ := ca.Issue(IssueRequest{CSR: csr, ClubID: 1, ComputerID: 1})
	if !SamePublicKey(csr, iss.Cert) {
		t.Error("same key not detected")
	}
	csr2, _ := ParseCSR(makeCSR(t, p256(t)))
	if SamePublicKey(csr2, iss.Cert) {
		t.Error("different keys reported equal")
	}
}

// S6-O1-1: ротация выпускающего CA. Новый CA выпускает; сертификаты прежнего принимаются (и на TLS-рукопожатии
// по ClientCAs, и в Verify), пока он в ExtraTrustFiles; без него — отказ. Корень в доверие не попадает.
func TestRotateIssuingCA(t *testing.T) {
	oldCA, dir := devCA(t)
	oldIss, oldKey := issueFor(t, oldCA, 3)

	certFile, keyFile, err := NewDevIssuingCA(dir, "agent-ca-2")
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{CACertFile: certFile, CAKeyFile: keyFile, ExtraTrustFiles: []string{filepath.Join(dir, DevCACertFile)}}
	newCA, err := LoadCA(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(newCA.ExtraTrust()) != 1 || !newCA.ExtraTrust()[0].Equal(oldCA.Certificate()) {
		t.Fatalf("extra trust = %v", newCA.ExtraTrust())
	}
	newIss, newKey := issueFor(t, newCA, 3)
	if !bytes.Equal(newIss.Cert.RawIssuer, newCA.Certificate().RawSubject) || newCA.Certificate().Equal(oldCA.Certificate()) {
		t.Fatal("new certificates must be issued by the new CA")
	}
	if len(newCA.ChainPEM()) != 2 || !strings.Contains(newCA.ChainPEM()[0], strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: newCA.Certificate().Raw})))) {
		t.Error("ca_chain_pem must be the new CA chain")
	}
	for name, c := range map[string]struct {
		iss Issued
		key *ecdsa.PrivateKey
	}{"old": {oldIss, oldKey}, "new": {newIss, newKey}} {
		if _, pc, err := newCA.Verify(c.iss.Cert); err != nil || pc != 3 {
			t.Errorf("Verify(%s) = %d, %v", name, pc, err)
		}
		if err := handshake(t, newCA, c.iss.Cert, c.key); err != nil {
			t.Errorf("TLS handshake with %s certificate: %v", name, err)
		}
	}

	// Без прежнего CA в доверенных — сертификаты прежнего не принимаются.
	opts.ExtraTrustFiles = nil
	strict, err := LoadCA(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := strict.Verify(oldIss.Cert); !errors.Is(err, ErrCertificateInvalid) {
		t.Errorf("Verify(old) without extra trust = %v", err)
	}
	if err := handshake(t, strict, oldIss.Cert, oldKey); err == nil {
		t.Error("TLS handshake with old certificate must fail without extra trust")
	}
	// Сертификат того же корня, но выпущенный третьим CA, не принимается: доверие — выпускающим CA, не корню.
	f3, k3, err := NewDevIssuingCA(dir, "agent-ca-3")
	if err != nil {
		t.Fatal(err)
	}
	third, err := LoadCA(Options{CACertFile: f3, CAKeyFile: k3})
	if err != nil {
		t.Fatal(err)
	}
	sib, sibKey := issueFor(t, third, 3)
	if _, _, err := newCA.Verify(sib.Cert); !errors.Is(err, ErrCertificateInvalid) {
		t.Errorf("Verify(sibling CA) = %v", err)
	}
	if err := handshake(t, newCA, sib.Cert, sibKey); err == nil {
		t.Error("TLS handshake with sibling CA certificate must fail")
	}
	// Не CA в extra_trust_files — ошибка загрузки.
	if _, err := LoadCA(Options{CACertFile: certFile, CAKeyFile: keyFile, ExtraTrustFiles: []string{filepath.Join(dir, DevServerCertFile)}}); err == nil {
		t.Error("server certificate in extra_trust_files must be rejected")
	}
}

func issueFor(t *testing.T, ca *CA, computerID int64) (Issued, *ecdsa.PrivateKey) {
	t.Helper()
	key := p256(t)
	csr, err := ParseCSR(makeCSR(t, key))
	if err != nil {
		t.Fatal(err)
	}
	iss, err := ca.Issue(IssueRequest{CSR: csr, ClubID: 1, ComputerID: computerID})
	if err != nil {
		t.Fatal(err)
	}
	return iss, key
}

// handshake — mTLS по net.Pipe: сервер требует клиентский сертификат и проверяет его по ca.ClientCAs()
// (как grpcserver.TLSConfig); клиент шлёт только свой сертификат, без цепочки (как агент).
func handshake(t *testing.T, ca *CA, client *x509.Certificate, clientKey *ecdsa.PrivateKey) error {
	t.Helper()
	srvKey := p256(t)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &srvKey.PublicKey, srvKey)
	if err != nil {
		t.Fatal(err)
	}
	srvCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(srvCert)
	sc, cc := net.Pipe()
	defer func() { _ = sc.Close(); _ = cc.Close() }()
	srv := tls.Server(sc, &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca.ClientCAs(),
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: srvKey, Leaf: srvCert}}})
	cli := tls.Client(cc, &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "localhost", RootCAs: roots,
		Certificates: []tls.Certificate{{Certificate: [][]byte{client.Raw}, PrivateKey: clientKey, Leaf: client}}})
	errc := make(chan error, 1)
	go func() { errc <- srv.Handshake() }()
	_ = cli.Handshake() // TLS 1.3: отказ сервера по клиентскому сертификату клиент видит позже — берём ошибку сервера
	_ = cc.Close()
	return <-errc
}
