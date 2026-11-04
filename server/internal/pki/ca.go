package pki

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Ошибки выпуска и проверки сертификатов.
var (
	// ErrCSRInvalid — CSR не разобран, подпись не сходится, неверный тип или размер ключа.
	// Соответствует ERROR_CODE_CSR_INVALID.
	ErrCSRInvalid = errors.New("pki: invalid CSR")
	// ErrCertificateExpired — сертификат истёк или ещё не действует.
	ErrCertificateExpired = errors.New("pki: certificate expired or not yet valid")
	// ErrCertificateInvalid — сертификат не нашего CA, без clientAuth или без идентичности ПК.
	ErrCertificateInvalid = errors.New("pki: certificate invalid")
)

const (
	// URIScheme — схема SAN URI клиентского сертификата: zafiro://club/<club_id>/computer/<computer_id>.
	URIScheme = "zafiro"
	// CommonNamePrefix — CN клиентского сертификата: computer:<computer_id>.
	CommonNamePrefix = "computer:"
	// OrganizationPrefix — O клиентского сертификата: club:<club_id>.
	OrganizationPrefix = "club:"
	// clockSkew — на сколько назад сдвигается NotBefore (часы ПК клуба могут отставать).
	clockSkew = 5 * time.Minute
	// MaxCSRBytes — предел размера CSR в PEM.
	MaxCSRBytes = 16 << 10
)

// CA — выпускающий CA агентов (обычно промежуточный «agent-issuing», подписанный офлайн-корнем).
type CA struct {
	cert  *x509.Certificate
	key   crypto.Signer
	chain []*x509.Certificate // выпускающий → … → корень (корень может отсутствовать)
	pool  *x509.CertPool      // якоря проверки клиентских сертификатов: текущий выпускающий CA + extra
	extra []*x509.Certificate // прежние выпускающие CA (ротация): только проверка, не выпуск
	now   func() time.Time
}

// LoadCAFromPEM разбирает выпускающий CA: certPEM — его сертификат, затем (необязательно) цепочка до
// корня; keyPEM — ключ (PKCS#8 или SEC1 EC). Ключ должен соответствовать сертификату.
// extraTrust — сертификаты прежних выпускающих CA, чьи клиентские сертификаты ещё действуют (ротация CA).
func LoadCAFromPEM(certPEM, keyPEM []byte, extraTrust ...*x509.Certificate) (*CA, error) {
	certs, err := parseCertificates(certPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: CA certificate: %w", err)
	}
	if len(certs) == 0 {
		return nil, errors.New("pki: CA certificate: no CERTIFICATE block")
	}
	key, err := parsePrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: CA key: %w", err)
	}
	issuing := certs[0]
	if !issuing.IsCA || !issuing.BasicConstraintsValid {
		return nil, errors.New("pki: CA certificate is not a CA")
	}
	if !publicKeysEqual(issuing.PublicKey, key.Public()) {
		return nil, errors.New("pki: CA key does not match CA certificate")
	}
	pool := x509.NewCertPool()
	pool.AddCert(issuing)
	for _, c := range extraTrust {
		if c == nil || !c.IsCA || !c.BasicConstraintsValid {
			return nil, errors.New("pki: extra trusted certificate is not a CA")
		}
		pool.AddCert(c)
	}
	return &CA{cert: issuing, key: key, chain: certs, pool: pool, extra: extraTrust, now: time.Now}, nil
}

// LoadCAFiles читает выпускающий CA из файлов (секреты смонтированы вне репозитория). extraTrustFiles —
// сертификаты прежних выпускающих CA (из каждого файла берётся первый сертификат, как из certFile; корень из
// хвоста цепочки в доверие не добавляется — иначе принимались бы сертификаты любого CA под ним).
func LoadCAFiles(certFile, keyFile string, extraTrustFiles ...string) (*CA, error) {
	extra := make([]*x509.Certificate, 0, len(extraTrustFiles))
	for _, f := range extraTrustFiles {
		data, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			return nil, fmt.Errorf("pki: read extra trusted CA: %w", err)
		}
		certs, err := parseCertificates(data)
		if err != nil {
			return nil, fmt.Errorf("pki: extra trusted CA %s: %w", filepath.Base(f), err)
		}
		if len(certs) == 0 {
			return nil, fmt.Errorf("pki: extra trusted CA %s: no CERTIFICATE block", filepath.Base(f))
		}
		extra = append(extra, certs[0])
	}
	certPEM, err := os.ReadFile(filepath.Clean(certFile))
	if err != nil {
		return nil, fmt.Errorf("pki: read CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(filepath.Clean(keyFile))
	if err != nil {
		return nil, fmt.Errorf("pki: read CA key: %w", err)
	}
	return LoadCAFromPEM(certPEM, keyPEM, extra...)
}

// ExtraTrust — прежние выпускающие CA, которым ещё доверяем (ротация CA).
func (ca *CA) ExtraTrust() []*x509.Certificate { return ca.extra }

// Certificate — сертификат выпускающего CA.
func (ca *CA) Certificate() *x509.Certificate { return ca.cert }

// ChainPEM — цепочка CA (выпускающий → корень) в PEM для EnrollResponse.ca_chain_pem.
func (ca *CA) ChainPEM() []string {
	out := make([]string, 0, len(ca.chain))
	for _, c := range ca.chain {
		out = append(out, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})))
	}
	return out
}

// ClientCAs — пул для tls.Config.ClientCAs: выпускающие CA, которым доверяем клиентские сертификаты.
func (ca *CA) ClientCAs() *x509.CertPool { return ca.pool }

// SetClock подменяет часы (тесты).
func (ca *CA) SetClock(now func() time.Time) { ca.now = now }

// ParseCSR разбирает PKCS#10 CSR в PEM, проверяет подпись и тип ключа (ECDSA P-256 или RSA ≥ 3072).
// Subject и расширения CSR игнорируются: сервер пишет свои.
func ParseCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	if len(csrPEM) == 0 || len(csrPEM) > MaxCSRBytes {
		return nil, fmt.Errorf("%w: size", ErrCSRInvalid)
	}
	block, rest := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%w: expected one CERTIFICATE REQUEST block", ErrCSRInvalid)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCSRInvalid, err)
	}
	switch csr.SignatureAlgorithm {
	case x509.ECDSAWithSHA256, x509.ECDSAWithSHA384, x509.ECDSAWithSHA512,
		x509.SHA256WithRSA, x509.SHA384WithRSA, x509.SHA512WithRSA,
		x509.SHA256WithRSAPSS, x509.SHA384WithRSAPSS, x509.SHA512WithRSAPSS:
	default:
		return nil, fmt.Errorf("%w: signature algorithm %s", ErrCSRInvalid, csr.SignatureAlgorithm)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: signature: %w", ErrCSRInvalid, err)
	}
	if err := checkClientKey(csr.PublicKey); err != nil {
		return nil, err
	}
	return csr, nil
}

func checkClientKey(pub any) error {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return fmt.Errorf("%w: ECDSA curve must be P-256", ErrCSRInvalid)
		}
		return nil
	case *rsa.PublicKey:
		if n := k.N.BitLen(); n < 3072 || n > 8192 {
			return fmt.Errorf("%w: RSA key must be 3072..8192 bits", ErrCSRInvalid)
		}
		return nil
	default:
		return fmt.Errorf("%w: key type %T is not supported", ErrCSRInvalid, pub)
	}
}

// IssueRequest — что выпустить.
type IssueRequest struct {
	CSR        *x509.CertificateRequest
	ClubID     int64
	ComputerID int64
	// Lifetime — срок сертификата (по умолчанию 30 дней).
	Lifetime time.Duration
}

// Issued — выпущенный сертификат.
type Issued struct {
	Cert    *x509.Certificate
	CertPEM string
	// Serial — серийный номер hex (нижний регистр), как в agent_certificates.serial.
	Serial     string
	NotBefore  time.Time
	NotAfter   time.Time
	RenewAfter time.Time
	// SPKISHA256 — SHA-256 открытого ключа (SubjectPublicKeyInfo).
	SPKISHA256 []byte
}

// DefaultLifetime — срок клиентского сертификата по умолчанию (docs/agent-protocol.md §3).
const DefaultLifetime = 30 * 24 * time.Hour

// Issue выпускает клиентский сертификат: CN=computer:<id>, O=club:<club_id>,
// SAN URI zafiro://club/<club_id>/computer/<id>, EKU clientAuth, серийный номер — случайные 128 бит.
// Ключ клиента в сервер не попадает: подписывается открытый ключ из CSR.
func (ca *CA) Issue(req IssueRequest) (Issued, error) {
	if req.CSR == nil || req.ClubID <= 0 || req.ComputerID <= 0 {
		return Issued{}, errors.New("pki: issue: CSR, club and computer are required")
	}
	if err := checkClientKey(req.CSR.PublicKey); err != nil {
		return Issued{}, err
	}
	lifetime := req.Lifetime
	if lifetime <= 0 {
		lifetime = DefaultLifetime
	}
	serial, err := RandomSerial()
	if err != nil {
		return Issued{}, err
	}
	now := ca.now().UTC().Truncate(time.Second)
	notBefore := now.Add(-clockSkew)
	notAfter := now.Add(lifetime)
	if notAfter.After(ca.cert.NotAfter) {
		notAfter = ca.cert.NotAfter
	}
	if !notAfter.After(now) {
		return Issued{}, errors.New("pki: issuing CA expired")
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   CommonNamePrefix + strconv.FormatInt(req.ComputerID, 10),
			Organization: []string{OrganizationPrefix + strconv.FormatInt(req.ClubID, 10)},
		},
		URIs:                  []*url.URL{IdentityURI(req.ClubID, req.ComputerID)},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	if _, ok := req.CSR.PublicKey.(*rsa.PublicKey); ok {
		tmpl.KeyUsage |= x509.KeyUsageKeyEncipherment
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, req.CSR.PublicKey, ca.key)
	if err != nil {
		return Issued{}, fmt.Errorf("pki: sign: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return Issued{}, fmt.Errorf("pki: parse issued: %w", err)
	}
	spki := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	// Продление на 2/3 срока (docs/agent-protocol.md §3).
	renewAfter := now.Add(notAfter.Sub(now) * 2 / 3)
	return Issued{
		Cert:       cert,
		CertPEM:    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		Serial:     SerialString(serial),
		NotBefore:  notBefore,
		NotAfter:   notAfter,
		RenewAfter: renewAfter,
		SPKISHA256: spki[:],
	}, nil
}

// Verify проверяет клиентский сертификат: цепочка до нашего выпускающего CA, EKU clientAuth, срок,
// идентичность ПК в SAN. Отзыв проверяет вызывающий (RevocationCache).
func (ca *CA) Verify(cert *x509.Certificate) (clubID, computerID int64, err error) {
	now := ca.now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return 0, 0, ErrCertificateExpired
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:       ca.pool,
		CurrentTime: now,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		var inv x509.CertificateInvalidError
		if errors.As(err, &inv) && inv.Reason == x509.Expired {
			return 0, 0, ErrCertificateExpired
		}
		return 0, 0, fmt.Errorf("%w: %w", ErrCertificateInvalid, err)
	}
	return ParseIdentity(cert)
}

// ParseIdentity достаёт club_id и computer_id из SAN URI и сверяет с CN.
func ParseIdentity(cert *x509.Certificate) (clubID, computerID int64, err error) {
	if len(cert.URIs) != 1 {
		return 0, 0, fmt.Errorf("%w: expected one SAN URI", ErrCertificateInvalid)
	}
	u := cert.URIs[0]
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Scheme != URIScheme || u.Host != "club" || len(parts) != 3 || parts[1] != "computer" {
		return 0, 0, fmt.Errorf("%w: SAN URI", ErrCertificateInvalid)
	}
	clubID, err1 := strconv.ParseInt(parts[0], 10, 64)
	computerID, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || clubID <= 0 || computerID <= 0 {
		return 0, 0, fmt.Errorf("%w: SAN URI ids", ErrCertificateInvalid)
	}
	if cert.Subject.CommonName != CommonNamePrefix+parts[2] {
		return 0, 0, fmt.Errorf("%w: CN does not match SAN", ErrCertificateInvalid)
	}
	return clubID, computerID, nil
}

// IdentityURI — SAN URI клиентского сертификата.
func IdentityURI(clubID, computerID int64) *url.URL {
	return &url.URL{Scheme: URIScheme, Host: "club",
		Path: "/" + strconv.FormatInt(clubID, 10) + "/computer/" + strconv.FormatInt(computerID, 10)}
}

// RandomSerial — случайный положительный серийный номер 128 бит (RFC 5280: ≤ 20 октетов).
func RandomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	for {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return nil, fmt.Errorf("pki: serial: %w", err)
		}
		if n.Sign() > 0 {
			return n, nil
		}
	}
}

// SerialString — серийный номер в hex нижним регистром (ключ в agent_certificates и Identity.CertSerial).
func SerialString(n *big.Int) string { return n.Text(16) }

// ParseCertificatePEM разбирает один сертификат в PEM.
func ParseCertificatePEM(certPEM []byte) (*x509.Certificate, error) {
	certs, err := parseCertificates(certPEM)
	if err != nil {
		return nil, err
	}
	if len(certs) != 1 {
		return nil, errors.New("pki: expected one certificate")
	}
	return certs[0], nil
}

func parseCertificates(data []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func parsePrivateKey(data []byte) (crypto.Signer, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, errors.New("no private key block")
		}
		switch block.Type {
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			s, ok := k.(crypto.Signer)
			if !ok {
				return nil, fmt.Errorf("unsupported key %T", k)
			}
			return s, nil
		case "EC PRIVATE KEY":
			return x509.ParseECPrivateKey(block.Bytes)
		}
	}
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	ea, ok := a.(equaler)
	return ok && ea.Equal(b)
}

// SamePublicKey — совпадает ли ключ CSR с ключом сертификата (продление требует новую пару ключей).
func SamePublicKey(csr *x509.CertificateRequest, cert *x509.Certificate) bool {
	return publicKeysEqual(csr.PublicKey, cert.PublicKey)
}
