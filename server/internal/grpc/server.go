package grpcserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"slices"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
)

// Методы без клиентского сертификата. Всё, чего нет в списке, требует действующий сертификат ПК.
const (
	MethodEnroll = "/" + "zafiro.agent.v1.EnrollmentService" + "/Enroll"
)

// CertChecker — статус сертификата по серийному номеру (кэш отзывов, enroll.CertCache).
// Возвращает ErrCertRevoked для отозванного/неизвестного и ErrCertMismatch, если ПК/клуб в базе
// не совпадают с сертификатом.
type CertChecker interface {
	CheckCertificate(ctx context.Context, serial string, clubID, computerID int64, notAfter time.Time) error
}

// Ошибки CertChecker.
var (
	ErrCertRevoked  = errors.New("grpcserver: certificate revoked")
	ErrCertMismatch = errors.New("grpcserver: certificate does not match its record")
)

// Limits — ограничения транспорта. Нули — значения по умолчанию (DefaultLimits).
type Limits struct {
	// MaxRecvMsgSize — предел входящего сообщения агента.
	MaxRecvMsgSize int
	// MaxSendMsgSize — предел исходящего (словарь переводов — самый большой ответ).
	MaxSendMsgSize int
	// MaxConcurrentStreams — одновременных RPC на одно соединение (ПК держит одно соединение).
	MaxConcurrentStreams uint32
	// KeepaliveMinTime — чаще клиент пинговать не может (иначе GOAWAY ENHANCE_YOUR_CALM).
	KeepaliveMinTime time.Duration
	// KeepaliveTime / KeepaliveTimeout — сервер пингует молчащее соединение и закрывает без ответа.
	KeepaliveTime    time.Duration
	KeepaliveTimeout time.Duration
	// ConnectionTimeout — время на TLS-рукопожатие и HTTP/2 preface.
	ConnectionTimeout time.Duration
	// WindowSize — начальное окно потока и соединения HTTP/2 (фиксированное: отключает рост BDP).
	WindowSize int32
	// BufferSize — буферы чтения и записи соединения.
	BufferSize int
}

// DefaultLimits — расчёт на 50 000 ПК (несколько инстансов, ~10–25 тыс. соединений на инстанс):
//   - сообщения агента маленькие (Status, CommandResult, CSR ≤ 16 КБ) → приём 1 МБ; ответ до 8 МБ
//     (переводы интерфейса, снимок Welcome);
//   - 16 потоков на соединение: один Connect + параллельные unary-вызовы гостя; больше честному
//     агенту не нужно, а лимит отсекает злоупотребление одним соединением;
//   - keepalive: агент пингует раз в 30 с (docs/agent-protocol.md §2), сервер разрешает не чаще 20 с;
//     сам сервер пингует соединение, молчащее 60 с, и закрывает его через 20 с без ответа — мёртвые
//     ПК (выдернули питание) освобождаются за ~80 с, что совпадает с TTL присутствия 90 с;
//   - MaxConnectionAge не задан: стрим Connect долгоживущий, принудительный разрыв вызвал бы
//     волну переподключений; перебалансировка — командой Drain (с jitter);
//   - память: по умолчанию grpc-go даёт 32+32 КБ буферов на соединение и растит окно HTTP/2 до 4 МБ
//     (BDP). При 25 000 соединений это 1,6 ГБ только буферов, поэтому буферы 16 КБ (буфер записи
//     общий — выделяется на время записи), окно фиксировано 64 КБ;
//   - рукопожатие ≤ 10 с: медленные клиенты не держат горутины и дескрипторы.
func DefaultLimits() Limits {
	return Limits{
		MaxRecvMsgSize:       1 << 20,
		MaxSendMsgSize:       8 << 20,
		MaxConcurrentStreams: 16,
		KeepaliveMinTime:     20 * time.Second,
		KeepaliveTime:        60 * time.Second,
		KeepaliveTimeout:     20 * time.Second,
		ConnectionTimeout:    10 * time.Second,
		WindowSize:           64 << 10,
		BufferSize:           16 << 10,
	}
}

// Options — сборка сервера.
type Options struct {
	// ServerCertificate — серверный TLS-сертификат (pki.LoadServerCertificate).
	ServerCertificate tls.Certificate
	// ClientCAs — выпускающие CA клиентских сертификатов (pki.CA.ClientCAs()).
	ClientCAs *x509.CertPool
	// Certs — проверка отзыва.
	Certs CertChecker
	// Enrollment — реализация EnrollmentService (enroll.GRPC).
	Enrollment agentv1.EnrollmentServiceServer
	// PublicMethods — дополнительные методы без клиентского сертификата (полные имена). По умолчанию только Enroll.
	PublicMethods []string
	// Limits — нули заменяются DefaultLimits.
	Limits Limits
	Logger *slog.Logger
	// Now — часы (тесты).
	Now func() time.Time
	// ExtraUnary / ExtraStream — перехватчики после проверки сертификата (лимиты, метрики).
	ExtraUnary  []grpc.UnaryServerInterceptor
	ExtraStream []grpc.StreamServerInterceptor
	// OuterUnary / OuterStream — внешние перехватчики, до восстановления после паники и проверки сертификата
	// (метрики и спаны Prometheus/OpenTelemetry, Этап 6 O3: отказы UNAUTHENTICATED и паники → INTERNAL тоже видны).
	OuterUnary  []grpc.UnaryServerInterceptor
	OuterStream []grpc.StreamServerInterceptor
}

// Server — gRPC-сервер агентов.
type Server struct {
	*grpc.Server
	tls *tls.Config
}

// TLSConfig — серверный TLS:
//   - ClientAuth VerifyClientCertIfGiven: Enroll идёт без сертификата, остальное проверяет перехватчик;
//     присланный сертификат Go проверяет по ClientCAs уже на рукопожатии (чужой CA, истёкший → отказ TLS);
//   - TLS 1.2 минимум: SChannel Windows 10 (большинство ПК клубов) не умеет TLS 1.3; только ECDHE + AEAD;
//   - session tickets включены: переподключение 50 000 ПК после Drain обходится без полной проверки подписи.
func TLSConfig(cert tls.Certificate, clientCAs *x509.CertPool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		},
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
		NextProtos:       []string{"h2"},
	}
}

// NewServer собирает gRPC-сервер: TLS, лимиты, перехватчики (паника → INTERNAL, проверка сертификата),
// EnrollmentService. register регистрирует остальные сервисы (AgentService — пакет agent/stream).
func NewServer(opts Options, register func(*grpc.Server)) (*Server, error) {
	if opts.ClientCAs == nil || opts.Certs == nil {
		return nil, errors.New("grpcserver: ClientCAs and Certs are required")
	}
	if len(opts.ServerCertificate.Certificate) == 0 {
		return nil, errors.New("grpcserver: server certificate is required")
	}
	lim := withDefaults(opts.Limits)
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	public := map[string]bool{MethodEnroll: true}
	for _, m := range opts.PublicMethods {
		public[m] = true
	}
	auth := &authenticator{certs: opts.Certs, public: public, now: now, logger: logger}
	tlsCfg := TLSConfig(opts.ServerCertificate, opts.ClientCAs)

	unary := append(append(slices.Clone(opts.OuterUnary), recoverUnary(logger), auth.unary), opts.ExtraUnary...)
	stream := append(append(slices.Clone(opts.OuterStream), recoverStream(logger), auth.stream), opts.ExtraStream...)
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
		grpc.MaxRecvMsgSize(lim.MaxRecvMsgSize),
		grpc.MaxSendMsgSize(lim.MaxSendMsgSize),
		grpc.MaxConcurrentStreams(lim.MaxConcurrentStreams),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: lim.KeepaliveMinTime, PermitWithoutStream: true}),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: lim.KeepaliveTime, Timeout: lim.KeepaliveTimeout}),
		grpc.ConnectionTimeout(lim.ConnectionTimeout),
		grpc.InitialWindowSize(lim.WindowSize),
		grpc.InitialConnWindowSize(lim.WindowSize),
		grpc.ReadBufferSize(lim.BufferSize),
		grpc.WriteBufferSize(lim.BufferSize),
		grpc.MaxHeaderListSize(16<<10),
	)
	if opts.Enrollment != nil {
		agentv1.RegisterEnrollmentServiceServer(srv, opts.Enrollment)
	}
	if register != nil {
		register(srv)
	}
	return &Server{Server: srv, tls: tlsCfg}, nil
}

// ServeContext обслуживает lis до отмены ctx, затем GracefulStop (не дольше grace, потом Stop).
func (s *Server) ServeContext(ctx context.Context, lis net.Listener, grace time.Duration) error {
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(lis) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	done := make(chan struct{})
	go func() { s.GracefulStop(); close(done) }()
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		s.Stop()
	}
	return nil
}

func withDefaults(l Limits) Limits {
	d := DefaultLimits()
	if l.MaxRecvMsgSize > 0 {
		d.MaxRecvMsgSize = l.MaxRecvMsgSize
	}
	if l.MaxSendMsgSize > 0 {
		d.MaxSendMsgSize = l.MaxSendMsgSize
	}
	if l.MaxConcurrentStreams > 0 {
		d.MaxConcurrentStreams = l.MaxConcurrentStreams
	}
	if l.KeepaliveMinTime > 0 {
		d.KeepaliveMinTime = l.KeepaliveMinTime
	}
	if l.KeepaliveTime > 0 {
		d.KeepaliveTime = l.KeepaliveTime
	}
	if l.KeepaliveTimeout > 0 {
		d.KeepaliveTimeout = l.KeepaliveTimeout
	}
	if l.ConnectionTimeout > 0 {
		d.ConnectionTimeout = l.ConnectionTimeout
	}
	if l.WindowSize > 0 {
		d.WindowSize = l.WindowSize
	}
	if l.BufferSize > 0 {
		d.BufferSize = l.BufferSize
	}
	return d
}

// ---------------------------------------------------------------------------
// Проверка сертификата
// ---------------------------------------------------------------------------

type authenticator struct {
	certs  CertChecker
	public map[string]bool
	now    func() time.Time
	logger *slog.Logger
}

func (a *authenticator) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ctx, err := a.authenticate(ctx, info.FullMethod)
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (a *authenticator) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx, err := a.authenticate(ss.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
}

// authenticate кладёт identity.Identity в контекст. Методы из public пропускаются без сертификата
// (и без идентичности, даже если сертификат прислан: Enroll не зависит от прежнего сертификата).
func (a *authenticator) authenticate(ctx context.Context, method string) (context.Context, error) {
	if a.public[method] {
		return ctx, nil
	}
	leaf, err := VerifiedPeerCertificate(ctx)
	if err != nil {
		return nil, Error(codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_CERTIFICATE_REQUIRED,
			"ПК не подключён к клубу.", map[string]string{"cause": "no_client_certificate"})
	}
	now := a.now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		// Соединение HTTP/2 живёт дольше сертификата: рукопожатие было, когда он ещё действовал.
		return nil, Error(codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_CERTIFICATE_EXPIRED,
			"Сертификат ПК истёк.", nil)
	}
	clubID, computerID, err := pki.ParseIdentity(leaf)
	if err != nil {
		return nil, Error(codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_CERTIFICATE_REVOKED,
			"Сертификат ПК недействителен.", nil)
	}
	serial := pki.SerialString(leaf.SerialNumber)
	switch err := a.certs.CheckCertificate(ctx, serial, clubID, computerID, leaf.NotAfter); {
	case err == nil:
	case errors.Is(err, ErrCertRevoked), errors.Is(err, ErrCertMismatch):
		return nil, Error(codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_CERTIFICATE_REVOKED,
			"Сертификат ПК отозван.", nil)
	default:
		a.logger.ErrorContext(ctx, "agent certificate check failed", "serial", serial, "error", err)
		return nil, Error(codes.Unavailable, agentv1.ErrorCode_ERROR_CODE_INTERNAL, "Внутренняя ошибка сервера.", nil)
	}
	return identity.WithIdentity(ctx, identity.Identity{ClubID: clubID, ComputerID: computerID, CertSerial: serial}), nil
}

// VerifiedPeerCertificate — клиентский сертификат соединения, проверенный на рукопожатии по ClientCAs.
func VerifiedPeerCertificate(ctx context.Context) (*x509.Certificate, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, errors.New("grpcserver: no peer")
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, errors.New("grpcserver: not a TLS connection")
	}
	if len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return nil, errors.New("grpcserver: no verified client certificate")
	}
	return info.State.VerifiedChains[0][0], nil
}

// PeerIP — IP-адрес клиента (лимиты, журнал). За L4-балансировщиком нужен PROXY protocol.
func PeerIP(ctx context.Context) net.IP {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return nil
	}
	if tcp, ok := p.Addr.(*net.TCPAddr); ok {
		return tcp.IP
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// ---------------------------------------------------------------------------
// Паника → INTERNAL
// ---------------------------------------------------------------------------

func recoverUnary(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ctx, "grpc panic", "method", info.FullMethod, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				err = Internal()
			}
		}()
		return handler(ctx, req)
	}
}

func recoverStream(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ss.Context(), "grpc panic", "method", info.FullMethod, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				err = Internal()
			}
		}()
		return handler(srv, ss)
	}
}

// IsCode — код gRPC ошибки (для тестов).
func IsCode(err error, c codes.Code) bool { return status.Code(err) == c }
