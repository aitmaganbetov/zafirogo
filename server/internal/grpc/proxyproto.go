package grpcserver

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PROXY protocol (https://www.haproxy.org/download/2.9/doc/proxy-protocol.txt) для gRPC агентов за L4-балансировщиком.
//
// mTLS заканчивается в самом сервере (балансировщик пробрасывает TCP как есть, docs/deploy.md), поэтому адрес ПК
// балансировщик может передать только заголовком PROXY перед TLS. Без него PeerIP — адрес балансировщика, и не работают
// лимиты регистрации по IP (agent-enroll) и подсети ключа образа (Q-037).
//
// Правила:
//   - заголовок принимается только от доверенных адресов (ProxyProtocol.Trusted) и от них обязателен: соединение без
//     заголовка закрывается (иначе любой клиент в доверенной сети выдавал бы себя за балансировщик и наоборот);
//   - от остальных адресов заголовок не читается — соединение идёт как есть (прямое подключение в обход балансировщика,
//     например из той же сети);
//   - поддерживаются v1 (текст; nginx stream `proxy_protocol on`) и v2 (двоичный; HAProxy `send-proxy-v2`, облачные
//     NLB). LOCAL (v2) и UNKNOWN (v1) — проверки балансировщика: используется настоящий адрес соединения;
//   - заголовок читается не в Accept (медленный клиент не держит цикл приёма), а при первом Read/RemoteAddr — в
//     горутине соединения grpc-go, с собственным таймаутом HeaderTimeout (не дольше дедлайна, поставленного grpc).

// ProxyProtocol — настройки приёма заголовка PROXY.
type ProxyProtocol struct {
	// Trusted — адреса балансировщиков (IP/CIDR). Пусто — PROXY protocol выключен.
	Trusted []netip.Prefix
	// HeaderTimeout — время на получение заголовка (0 — 5 с).
	HeaderTimeout time.Duration
	Logger        *slog.Logger
}

// Ошибки разбора заголовка.
var (
	ErrProxyHeaderMissing = errors.New("grpcserver: PROXY protocol header required from trusted proxy")
	ErrProxyHeaderInvalid = errors.New("grpcserver: invalid PROXY protocol header")
)

const (
	defaultProxyHeaderTimeout = 5 * time.Second
	// proxyV1MaxLen — предел строки v1 по спецификации (включая CRLF).
	proxyV1MaxLen = 107
	// proxyV2MaxLen — предел адресной части v2 с TLV: облачные балансировщики добавляют немного TLV (id VPC и т.п.).
	proxyV2MaxLen = 2048
)

var proxyV2Sig = []byte("\r\n\r\n\x00\r\nQUIT\n")

// NewProxyListener оборачивает listener gRPC: соединения от доверенных адресов начинаются с заголовка PROXY.
// Пустой Trusted — inner без изменений.
func NewProxyListener(inner net.Listener, opts ProxyProtocol) net.Listener {
	if len(opts.Trusted) == 0 {
		return inner
	}
	if opts.HeaderTimeout <= 0 {
		opts.HeaderTimeout = defaultProxyHeaderTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &proxyListener{Listener: inner, opts: opts}
}

type proxyListener struct {
	net.Listener
	opts ProxyProtocol
}

func (l *proxyListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if !l.trusted(c.RemoteAddr()) {
		return c, nil
	}
	return &proxyConn{Conn: c, timeout: l.opts.HeaderTimeout, log: l.opts.Logger}, nil
}

func (l *proxyListener) trusted(a net.Addr) bool {
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return false
	}
	ip := ap.Addr().Unmap()
	for _, p := range l.opts.Trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// proxyConn — соединение от балансировщика; заголовок читается лениво (once).
type proxyConn struct {
	net.Conn
	timeout time.Duration
	log     *slog.Logger

	once   sync.Once
	err    error
	remote net.Addr
	rest   []byte // байты после заголовка, прочитанные в буфер (начало TLS ClientHello)

	mu       sync.Mutex
	deadline time.Time // дедлайн чтения, поставленный владельцем соединения (grpc-go)
}

func (c *proxyConn) init() {
	c.once.Do(func() {
		c.remote, c.rest, c.err = c.readHeader()
		if c.err != nil {
			c.log.Warn("agent grpc: rejected connection from trusted proxy", "proxy", c.Conn.RemoteAddr().String(), "err", c.err)
			_ = c.Close()
		}
	})
}

func (c *proxyConn) readHeader() (net.Addr, []byte, error) {
	c.mu.Lock()
	callerDeadline := c.deadline
	c.mu.Unlock()
	d := time.Now().Add(c.timeout)
	if !callerDeadline.IsZero() && callerDeadline.Before(d) {
		d = callerDeadline
	}
	if err := c.Conn.SetReadDeadline(d); err != nil {
		return nil, nil, err
	}
	br := bufio.NewReaderSize(c.Conn, 256)
	addr, err := parseProxyHeader(br)
	if err != nil {
		return nil, nil, err
	}
	// Вернуть дедлайн владельца (возможно, grpc-go поставил его уже после нашего).
	c.mu.Lock()
	err = c.Conn.SetReadDeadline(c.deadline)
	c.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	var rest []byte
	if n := br.Buffered(); n > 0 {
		b, _ := br.Peek(n)
		rest = bytes.Clone(b)
	}
	return addr, rest, nil
}

func (c *proxyConn) Read(b []byte) (int, error) {
	c.init()
	if c.err != nil {
		return 0, c.err
	}
	if len(c.rest) > 0 {
		n := copy(b, c.rest)
		c.rest = c.rest[n:]
		return n, nil
	}
	return c.Conn.Read(b)
}

// RemoteAddr — адрес клиента из заголовка (для LOCAL/UNKNOWN и при ошибке — адрес соединения).
func (c *proxyConn) RemoteAddr() net.Addr {
	c.init()
	if c.remote != nil {
		return c.remote
	}
	return c.Conn.RemoteAddr()
}

func (c *proxyConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return c.Conn.SetDeadline(t)
}

func (c *proxyConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(t)
}

// parseProxyHeader читает заголовок v1 или v2. nil-адрес — использовать адрес соединения (LOCAL, UNKNOWN,
// неподдерживаемое семейство адресов).
func parseProxyHeader(br *bufio.Reader) (net.Addr, error) {
	sig, err := br.Peek(len(proxyV2Sig))
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrProxyHeaderMissing
		}
		return nil, err
	}
	switch {
	case bytes.Equal(sig, proxyV2Sig):
		return parseProxyV2(br)
	case bytes.HasPrefix(sig, []byte("PROXY ")):
		return parseProxyV1(br)
	default:
		return nil, ErrProxyHeaderMissing
	}
}

func parseProxyV1(br *bufio.Reader) (net.Addr, error) {
	line, err := br.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return nil, fmt.Errorf("%w: v1 line too long", ErrProxyHeaderInvalid)
		}
		return nil, err
	}
	if len(line) > proxyV1MaxLen || !bytes.HasSuffix(line, []byte("\r\n")) {
		return nil, fmt.Errorf("%w: v1 line", ErrProxyHeaderInvalid)
	}
	f := strings.Split(string(line[:len(line)-2]), " ")
	if len(f) < 2 || f[0] != "PROXY" {
		return nil, fmt.Errorf("%w: v1 prefix", ErrProxyHeaderInvalid)
	}
	if f[1] == "UNKNOWN" {
		return nil, nil
	}
	if len(f) != 6 || (f[1] != "TCP4" && f[1] != "TCP6") {
		return nil, fmt.Errorf("%w: v1 fields", ErrProxyHeaderInvalid)
	}
	src, err1 := netip.ParseAddr(f[2])
	dst, err2 := netip.ParseAddr(f[3])
	sport, err3 := strconv.ParseUint(f[4], 10, 16)
	_, err4 := strconv.ParseUint(f[5], 10, 16)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return nil, fmt.Errorf("%w: v1 address", ErrProxyHeaderInvalid)
	}
	if v4 := f[1] == "TCP4"; src.Is4() != v4 || dst.Is4() != v4 || src.Zone() != "" {
		return nil, fmt.Errorf("%w: v1 address family", ErrProxyHeaderInvalid)
	}
	return net.TCPAddrFromAddrPort(netip.AddrPortFrom(src, uint16(sport))), nil
}

func parseProxyV2(br *bufio.Reader) (net.Addr, error) {
	var hdr [16]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return nil, err
	}
	if hdr[12]>>4 != 2 {
		return nil, fmt.Errorf("%w: v2 version", ErrProxyHeaderInvalid)
	}
	cmd := hdr[12] & 0x0f
	if cmd > 1 {
		return nil, fmt.Errorf("%w: v2 command", ErrProxyHeaderInvalid)
	}
	n := int(binary.BigEndian.Uint16(hdr[14:16]))
	if n > proxyV2MaxLen {
		return nil, fmt.Errorf("%w: v2 length %d", ErrProxyHeaderInvalid, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	if cmd == 0 { // LOCAL: проверка самого балансировщика
		return nil, nil
	}
	switch hdr[13] {
	case 0x11: // TCP over IPv4
		if n < 12 {
			return nil, fmt.Errorf("%w: v2 short IPv4 block", ErrProxyHeaderInvalid)
		}
		src := netip.AddrFrom4([4]byte(body[0:4]))
		return net.TCPAddrFromAddrPort(netip.AddrPortFrom(src, binary.BigEndian.Uint16(body[8:10]))), nil
	case 0x21: // TCP over IPv6
		if n < 36 {
			return nil, fmt.Errorf("%w: v2 short IPv6 block", ErrProxyHeaderInvalid)
		}
		src := netip.AddrFrom16([16]byte(body[0:16])).Unmap()
		return net.TCPAddrFromAddrPort(netip.AddrPortFrom(src, binary.BigEndian.Uint16(body[32:34]))), nil
	default: // UNSPEC, UDP, unix: по спецификации — адрес соединения
		return nil, nil
	}
}
