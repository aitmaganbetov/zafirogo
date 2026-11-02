package grpcserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
)

func v2Header(cmd, fam byte, body []byte) []byte {
	var b bytes.Buffer
	b.Write(proxyV2Sig)
	b.WriteByte(0x20 | cmd)
	b.WriteByte(fam)
	_ = binary.Write(&b, binary.BigEndian, uint16(len(body)))
	b.Write(body)
	return b.Bytes()
}

func v2TCP4(src, dst string, sport, dport uint16) []byte {
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	body := append(append(s[:], d[:]...), 0, 0, 0, 0)
	binary.BigEndian.PutUint16(body[8:], sport)
	binary.BigEndian.PutUint16(body[10:], dport)
	return v2Header(1, 0x11, body)
}

func TestParseProxyHeader(t *testing.T) {
	v6body := make([]byte, 36)
	src6 := netip.MustParseAddr("2001:db8::7").As16()
	copy(v6body, src6[:])
	binary.BigEndian.PutUint16(v6body[32:], 4242)
	tlv := append(v2TCP4("203.0.113.9", "10.0.0.1", 50000, 8443)[16:], 0x04, 0x00, 0x02, 'a', 'b') // + TLV NOOP

	cases := []struct {
		name    string
		in      []byte
		want    string // "" — адрес соединения (nil)
		wantErr error
	}{
		{"v1 TCP4", []byte("PROXY TCP4 203.0.113.9 10.0.0.1 50000 8443\r\n"), "203.0.113.9:50000", nil},
		{"v1 TCP6", []byte("PROXY TCP6 2001:db8::7 2001:db8::1 4242 8443\r\n"), "[2001:db8::7]:4242", nil},
		{"v1 UNKNOWN", []byte("PROXY UNKNOWN\r\n"), "", nil},
		{"v1 без CR", []byte("PROXY TCP4 203.0.113.9 10.0.0.1 50000 8443\n"), "", ErrProxyHeaderInvalid},
		{"v1 семейство не совпадает", []byte("PROXY TCP4 2001:db8::7 10.0.0.1 1 2\r\n"), "", ErrProxyHeaderInvalid},
		{"v1 порт вне диапазона", []byte("PROXY TCP4 203.0.113.9 10.0.0.1 70000 8443\r\n"), "", ErrProxyHeaderInvalid},
		{"v1 слишком длинная строка", []byte("PROXY TCP4 " + strings.Repeat("1", 300) + "\r\n"), "", ErrProxyHeaderInvalid},
		{"v2 TCP4", v2TCP4("203.0.113.9", "10.0.0.1", 50000, 8443), "203.0.113.9:50000", nil},
		{"v2 TCP4 с TLV", v2Header(1, 0x11, tlv), "203.0.113.9:50000", nil},
		{"v2 TCP6", v2Header(1, 0x21, v6body), "[2001:db8::7]:4242", nil},
		{"v2 LOCAL", v2Header(0, 0x00, nil), "", nil},
		{"v2 UNSPEC", v2Header(1, 0x00, nil), "", nil},
		{"v2 короткий блок IPv4", v2Header(1, 0x11, make([]byte, 8)), "", ErrProxyHeaderInvalid},
		{"v2 неизвестная команда", v2Header(2, 0x11, make([]byte, 12)), "", ErrProxyHeaderInvalid},
		{"v2 слишком длинный", v2Header(1, 0x11, make([]byte, proxyV2MaxLen+1)), "", ErrProxyHeaderInvalid},
		{"нет заголовка (TLS ClientHello)", []byte("\x16\x03\x01\x02\x00\x01\x00\x01\xfc\x03\x03\x00\x00\x00"), "", ErrProxyHeaderMissing},
		{"соединение закрыто до заголовка", []byte("PROX"), "", ErrProxyHeaderMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, err := parseProxyHeader(bufio.NewReaderSize(bytes.NewReader(tc.in), 256))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if addr != nil {
				got = addr.String()
			}
			if got != tc.want {
				t.Fatalf("addr = %q, want %q", got, tc.want)
			}
		})
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// acceptOne — listener с PROXY protocol; клиент пишет payload, сервер возвращает принятое соединение.
func acceptOne(t *testing.T, trusted string, payload []byte) net.Conn {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	lis := NewProxyListener(inner, ProxyProtocol{
		Trusted: []netip.Prefix{netip.MustParsePrefix(trusted)}, HeaderTimeout: 300 * time.Millisecond, Logger: quietLogger(),
	})
	client, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if len(payload) > 0 {
		if _, err := client.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	c, err := lis.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestProxyListener(t *testing.T) {
	t.Run("доверенный: адрес из заголовка, данные после него не теряются", func(t *testing.T) {
		c := acceptOne(t, "127.0.0.1/32", append(v2TCP4("203.0.113.9", "10.0.0.1", 50000, 8443), "hello"...))
		buf := make([]byte, 5)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
			t.Fatalf("read = %q, %v", buf, err)
		}
		if got := c.RemoteAddr().String(); got != "203.0.113.9:50000" {
			t.Fatalf("RemoteAddr = %s", got)
		}
	})
	t.Run("RemoteAddr до Read читает заголовок", func(t *testing.T) {
		c := acceptOne(t, "127.0.0.0/8", []byte("PROXY TCP4 198.51.100.4 10.0.0.1 1234 8443\r\nX"))
		if got := c.RemoteAddr().String(); got != "198.51.100.4:1234" {
			t.Fatalf("RemoteAddr = %s", got)
		}
		buf := make([]byte, 1)
		if _, err := io.ReadFull(c, buf); err != nil || buf[0] != 'X' {
			t.Fatalf("read = %q, %v", buf, err)
		}
	})
	t.Run("доверенный без заголовка — соединение закрыто", func(t *testing.T) {
		c := acceptOne(t, "127.0.0.1/32", []byte("\x16\x03\x01\x00\x10abcdefghijklmnop"))
		if _, err := c.Read(make([]byte, 8)); !errors.Is(err, ErrProxyHeaderMissing) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("доверенный молчит — таймаут заголовка", func(t *testing.T) {
		c := acceptOne(t, "127.0.0.1/32", nil)
		start := time.Now()
		_, err := c.Read(make([]byte, 8))
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("err = %v", err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("header timeout took %s", d)
		}
	})
	t.Run("дедлайн владельца сохраняется после заголовка", func(t *testing.T) {
		c := acceptOne(t, "127.0.0.1/32", []byte("PROXY UNKNOWN\r\n"))
		if err := c.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		_, err := c.Read(make([]byte, 8)) // заголовок прочитан, данных нет → дедлайн владельца
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("err = %v", err)
		}
		if got := c.RemoteAddr().(*net.TCPAddr).IP.String(); got != "127.0.0.1" {
			t.Fatalf("UNKNOWN RemoteAddr = %s", got)
		}
	})
	t.Run("недоверенный — заголовок не читается", func(t *testing.T) {
		raw := []byte("PROXY TCP4 203.0.113.9 10.0.0.1 50000 8443\r\n")
		c := acceptOne(t, "10.0.0.0/8", raw)
		if _, ok := c.(*proxyConn); ok {
			t.Fatal("untrusted connection wrapped")
		}
		buf := make([]byte, len(raw))
		if _, err := io.ReadFull(c, buf); err != nil || !bytes.Equal(buf, raw) {
			t.Fatalf("read = %q, %v", buf, err)
		}
		if got := c.RemoteAddr().(*net.TCPAddr).IP.String(); got != "127.0.0.1" {
			t.Fatalf("RemoteAddr = %s", got)
		}
	})
	t.Run("пустой список — listener без изменений", func(t *testing.T) {
		inner, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer inner.Close()
		if NewProxyListener(inner, ProxyProtocol{}) != inner {
			t.Fatal("listener wrapped without trusted proxies")
		}
	})
}

// peerEnroll — Enroll отвечает адресом клиента, который видит сервер (PeerIP).
type peerEnroll struct {
	agentv1.UnimplementedEnrollmentServiceServer
}

func (peerEnroll) Enroll(ctx context.Context, _ *agentv1.EnrollRequest) (*agentv1.EnrollResponse, error) {
	return &agentv1.EnrollResponse{ClubName: PeerIP(ctx).String()}, nil
}

// TestProxyProtocolGRPC — gRPC + TLS за «балансировщиком»: PeerIP — адрес из заголовка v1/v2, TLS не ломается.
func TestProxyProtocolGRPC(t *testing.T) {
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
	srv, err := NewServer(Options{ServerCertificate: srvCert, ClientCAs: ca.ClientCAs(), Certs: &fakeCerts{}, Enrollment: peerEnroll{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lis := NewProxyListener(inner, ProxyProtocol{Trusted: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, Logger: quietLogger()})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	call := func(t *testing.T, header []byte) (string, error) {
		t.Helper()
		dialer := func(ctx context.Context, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
			if _, err := c.Write(header); err != nil {
				_ = c.Close()
				return nil, err
			}
			return c, nil
		}
		cfg := &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
		conn, err := grpc.NewClient("passthrough:///"+inner.Addr().String(),
			grpc.WithTransportCredentials(credentials.NewTLS(cfg)), grpc.WithContextDialer(dialer))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := agentv1.NewEnrollmentServiceClient(conn).Enroll(ctx, &agentv1.EnrollRequest{})
		return resp.GetClubName(), err
	}

	if got, err := call(t, []byte("PROXY TCP4 203.0.113.9 127.0.0.1 50000 8443\r\n")); err != nil || got != "203.0.113.9" {
		t.Fatalf("v1: peer = %q, %v", got, err)
	}
	if got, err := call(t, v2TCP4("198.51.100.4", "127.0.0.1", 1234, 8443)); err != nil || got != "198.51.100.4" {
		t.Fatalf("v2: peer = %q, %v", got, err)
	}
	if got, err := call(t, v2Header(0, 0, nil)); err != nil || got != "127.0.0.1" {
		t.Fatalf("v2 LOCAL: peer = %q, %v", got, err)
	}
	if _, err := call(t, nil); err == nil {
		t.Fatal("connection without PROXY header from trusted proxy accepted")
	}
}
