package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Имена файлов dev-PKI в Options.DevDir.
const (
	DevRootCertFile   = "root-ca.pem"
	DevRootKeyFile    = "root-ca.key"
	DevCACertFile     = "agent-ca.pem" // выпускающий CA + корень
	DevCAKeyFile      = "agent-ca.key"
	DevServerCertFile = "server.pem" // серверный сертификат + выпускающий CA
	DevServerKeyFile  = "server.key"
)

// Options — откуда взять CA и серверный сертификат gRPC.
type Options struct {
	// CACertFile, CAKeyFile — выпускающий CA агентов (PEM; ключ — секрет вне репозитория).
	CACertFile string
	CAKeyFile  string
	// ExtraTrustFiles — сертификаты прежних выпускающих CA (PEM; берётся первый сертификат файла — тот же формат,
	// что CACertFile). Клиентские сертификаты, выпущенные ими, принимаются, пока не истекут; новые выпускает
	// только текущий CA (ротация CA, docs/deploy.md «Ротация CA агентов»).
	ExtraTrustFiles []string
	// ServerCertFile, ServerKeyFile — серверный TLS-сертификат gRPC (полная цепочка) и ключ.
	ServerCertFile string
	ServerKeyFile  string
	// DevDir — только для разработки: если файлы выше не заданы, PKI берётся из этой папки и
	// генерируется при первом запуске (каталог 0700, файлы 0600). В production пусто.
	DevDir string
	// DevHosts — имена/IP для dev-серверного сертификата (по умолчанию localhost, 127.0.0.1, ::1).
	DevHosts []string
}

// LoadCA загружает выпускающий CA из файлов или (в dev) из DevDir.
func LoadCA(opts Options) (*CA, error) {
	switch {
	case opts.CACertFile != "" || opts.CAKeyFile != "":
		if opts.CACertFile == "" || opts.CAKeyFile == "" {
			return nil, errors.New("pki: both CA certificate and key files are required")
		}
		return LoadCAFiles(opts.CACertFile, opts.CAKeyFile, opts.ExtraTrustFiles...)
	case opts.DevDir != "":
		if err := EnsureDevPKI(opts.DevDir, opts.DevHosts); err != nil {
			return nil, err
		}
		return LoadCAFiles(filepath.Join(opts.DevDir, DevCACertFile), filepath.Join(opts.DevDir, DevCAKeyFile), opts.ExtraTrustFiles...)
	default:
		return nil, errors.New("pki: CA is not configured (set CA certificate/key files or a dev directory)")
	}
}

// LoadServerCertificate загружает серверный TLS-сертификат из файлов или (в dev) из DevDir.
func LoadServerCertificate(opts Options) (tls.Certificate, error) {
	switch {
	case opts.ServerCertFile != "" || opts.ServerKeyFile != "":
		if opts.ServerCertFile == "" || opts.ServerKeyFile == "" {
			return tls.Certificate{}, errors.New("pki: both server certificate and key files are required")
		}
		return tls.LoadX509KeyPair(opts.ServerCertFile, opts.ServerKeyFile)
	case opts.DevDir != "":
		if err := EnsureDevPKI(opts.DevDir, opts.DevHosts); err != nil {
			return tls.Certificate{}, err
		}
		return tls.LoadX509KeyPair(filepath.Join(opts.DevDir, DevServerCertFile), filepath.Join(opts.DevDir, DevServerKeyFile))
	default:
		return tls.Certificate{}, errors.New("pki: server certificate is not configured")
	}
}

// EnsureDevPKI создаёт в dir (если их ещё нет) dev-корень, выпускающий CA агентов и серверный
// сертификат, подписанный тем же корнем (агент в dev доверяет корню — как pinning в production).
// Только для разработки и тестов.
func EnsureDevPKI(dir string, hosts []string) error {
	if _, err := os.Stat(filepath.Join(dir, DevServerKeyFile)); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("pki: dev dir: %w", err)
	}
	if len(hosts) == 0 {
		hosts = []string{"localhost", "127.0.0.1", "::1"}
	}
	now := time.Now().UTC().Truncate(time.Second)

	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	root, err := selfSign(&x509.Certificate{
		Subject:               pkix.Name{CommonName: "Zafiro Dev Root CA", Organization: []string{"Zafiro (dev)"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}, nil, rootKey, rootKey)
	if err != nil {
		return err
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	ca, err := selfSign(&x509.Certificate{
		Subject:               pkix.Name{CommonName: "Zafiro Dev Agent Issuing CA", Organization: []string{"Zafiro (dev)"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}, root, caKey, rootKey)
	if err != nil {
		return err
	}
	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	srvTmpl := &x509.Certificate{
		Subject:               pkix.Name{CommonName: hosts[0]},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			srvTmpl.IPAddresses = append(srvTmpl.IPAddresses, ip)
		} else {
			srvTmpl.DNSNames = append(srvTmpl.DNSNames, h)
		}
	}
	srv, err := selfSign(srvTmpl, root, srvKey, rootKey)
	if err != nil {
		return err
	}

	files := []struct {
		name string
		data []byte
	}{
		{DevRootCertFile, certPEM(root)},
		{DevRootKeyFile, keyPEM(rootKey)},
		{DevCACertFile, append(certPEM(ca), certPEM(root)...)},
		{DevCAKeyFile, keyPEM(caKey)},
		{DevServerKeyFile, keyPEM(srvKey)},
		{DevServerCertFile, certPEM(srv)},
	}
	for _, f := range files {
		if f.data == nil {
			return errors.New("pki: dev: encode key")
		}
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, 0o600); err != nil {
			return fmt.Errorf("pki: dev: write %s: %w", f.name, err)
		}
	}
	return nil
}

// NewDevIssuingCA выпускает в dir (созданном EnsureDevPKI) новый выпускающий CA агентов под тем же dev-корнем
// и пишет его в name.pem (сертификат + корень) и name.key — как при ротации CA в production (там это делает
// офлайн-церемония с ключом корня). Только для разработки и тестов ротации (S6-O1-1).
func NewDevIssuingCA(dir, name string) (certFile, keyFile string, err error) {
	rootCert, err := os.ReadFile(filepath.Clean(filepath.Join(dir, DevRootCertFile)))
	if err != nil {
		return "", "", fmt.Errorf("pki: dev root: %w", err)
	}
	rootKeyPEM, err := os.ReadFile(filepath.Clean(filepath.Join(dir, DevRootKeyFile)))
	if err != nil {
		return "", "", fmt.Errorf("pki: dev root key: %w", err)
	}
	root, err := ParseCertificatePEM(rootCert)
	if err != nil {
		return "", "", fmt.Errorf("pki: dev root: %w", err)
	}
	rootKey, err := parsePrivateKey(rootKeyPEM)
	if err != nil {
		return "", "", fmt.Errorf("pki: dev root key: %w", err)
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC().Truncate(time.Second)
	ca, err := selfSign(&x509.Certificate{
		Subject:               pkix.Name{CommonName: "Zafiro Dev Agent Issuing CA " + name, Organization: []string{"Zafiro (dev)"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}, root, caKey, rootKey)
	if err != nil {
		return "", "", err
	}
	k := keyPEM(caKey)
	if k == nil {
		return "", "", errors.New("pki: dev: encode key")
	}
	certFile, keyFile = filepath.Clean(filepath.Join(dir, name+".pem")), filepath.Clean(filepath.Join(dir, name+".key"))
	// #nosec G703 -- только dev/тесты: каталог и имя задаёт вызывающий код, не ввод пользователя
	if err := os.WriteFile(certFile, append(certPEM(ca), certPEM(root)...), 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyFile, k, 0o600); err != nil {
		return "", "", err
	}
	return certFile, keyFile, nil
}

func selfSign(tmpl, parent *x509.Certificate, key *ecdsa.PrivateKey, signer crypto.Signer) (*x509.Certificate, error) {
	serial, err := RandomSerial()
	if err != nil {
		return nil, err
	}
	tmpl.SerialNumber = serial
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

func certPEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func keyPEM(k *ecdsa.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}
