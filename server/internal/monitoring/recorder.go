// Package monitoring — метрики экрана «Сервер» суперадминки (Этап 3, группа 6): счётчики запросов по каналам и
// клубам, снимок машины, снимки загрузки клубов и фоновые задачи их сбора (решение PD-10: поведение — по коду
// старого проекта). Laravel: app/Services/Monitoring/RequestRecorder.php, ServerProbe.php,
// app/Console/Commands/CollectServerMetrics.php, SnapshotClubLoad.php, PruneMonitoringData.php.
//
// Отличие от старого: RequestRecorder писал строку в файл минуты на каждый запрос, а monitoring:collect
// складывал файлы в request_metrics. Здесь счётчики — в памяти инстанса (без записи на запрос), каждый инстанс
// раз в минуту добавляет законченные минуты в request_metrics/club_request_metrics (сложение в UPSERT — несколько
// инстансов не мешают друг другу). Алерты Telegram — Этап 4 (Q-060).
package monitoring

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// Каналы запросов (RequestRecorder::CHANNELS).
const (
	ChannelPanel = "panel"
	ChannelAgent = "agent"
	ChannelAPI   = "api"
	ChannelGuest = "guest"
)

// Channels — порядок каналов на экране.
var Channels = []string{ChannelPanel, ChannelAgent, ChannelAPI, ChannelGuest}

// Totals — запросы, ошибки 5xx и суммарное время ответа.
type Totals struct {
	Requests, Errors, DurationMs int64
}

// ClubTotals — запросы клуба по каналам за минуту.
type ClubTotals struct {
	Agent, API, Panel, Guest, Errors, DurationMs int64
}

// Drained — законченные минуты: канал и клуб.
type Drained struct {
	Channels map[int64]map[string]Totals
	Clubs    map[int64]map[int64]ClubTotals
}

// Recorder — счётчики запросов в памяти инстанса.
type Recorder struct {
	mu       sync.Mutex
	channels map[int64]map[string]Totals
	clubs    map[int64]map[int64]ClubTotals
	now      func() time.Time
}

// NewRecorder создаёт счётчики (now nil — time.Now).
func NewRecorder(now func() time.Time) *Recorder {
	if now == nil {
		now = time.Now
	}
	return &Recorder{channels: map[int64]map[string]Totals{}, clubs: map[int64]map[int64]ClubTotals{}, now: now}
}

// Record — один запрос (RequestRecorder::record()): канал, клуб (0 — без клуба), длительность, статус.
func (r *Recorder) Record(channel string, clubID int64, d time.Duration, status int) {
	if r == nil {
		return
	}
	minute := r.now().Unix() / 60
	ms := d.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	errs := int64(0)
	if status >= 500 {
		errs = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.channels[minute]
	if m == nil {
		m = map[string]Totals{}
		r.channels[minute] = m
	}
	t := m[channel]
	t.Requests++
	t.Errors += errs
	t.DurationMs += ms
	m[channel] = t
	if clubID <= 0 {
		return
	}
	cm := r.clubs[minute]
	if cm == nil {
		cm = map[int64]ClubTotals{}
		r.clubs[minute] = cm
	}
	c := cm[clubID]
	switch channel {
	case ChannelAgent:
		c.Agent++
	case ChannelAPI:
		c.API++
	case ChannelGuest:
		c.Guest++
	default:
		c.Panel++
	}
	c.Errors += errs
	c.DurationMs += ms
	cm[clubID] = c
}

// Drain забирает минуты меньше beforeMinute (текущая минута остаётся открытой).
func (r *Recorder) Drain(beforeMinute int64) Drained {
	out := Drained{Channels: map[int64]map[string]Totals{}, Clubs: map[int64]map[int64]ClubTotals{}}
	if r == nil {
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for m, v := range r.channels {
		if m < beforeMinute {
			out.Channels[m] = v
			delete(r.channels, m)
		}
	}
	for m, v := range r.clubs {
		if m < beforeMinute {
			out.Clubs[m] = v
			delete(r.clubs, m)
		}
	}
	return out
}

// Restore возвращает несохранённые минуты (ошибка записи в базу): они добавятся к следующей выгрузке.
func (r *Recorder) Restore(d Drained) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for m, v := range d.Channels {
		cur := r.channels[m]
		if cur == nil {
			cur = map[string]Totals{}
			r.channels[m] = cur
		}
		for ch, t := range v {
			c := cur[ch]
			c.Requests += t.Requests
			c.Errors += t.Errors
			c.DurationMs += t.DurationMs
			cur[ch] = c
		}
	}
	for m, v := range d.Clubs {
		cur := r.clubs[m]
		if cur == nil {
			cur = map[int64]ClubTotals{}
			r.clubs[m] = cur
		}
		for id, t := range v {
			c := cur[id]
			c.Agent += t.Agent
			c.API += t.API
			c.Panel += t.Panel
			c.Guest += t.Guest
			c.Errors += t.Errors
			c.DurationMs += t.DurationMs
			cur[id] = c
		}
	}
}

// HTTPChannel — канал HTTP-запроса (RequestRecorder::channel()): /api/v1 — интеграционный API, остальное
// (REST панели, страницы SPA) — панель. Агент и приложение гостя работают по gRPC (GRPCChannel).
func HTTPChannel(path string) string {
	if path == "/api/v1" || strings.HasPrefix(path, "/api/v1/") {
		return ChannelAPI
	}
	return ChannelPanel
}

// GRPCChannel — канал gRPC-метода: сервис агента — agent, сервисы гостя — guest.
func GRPCChannel(fullMethod string) string {
	if strings.Contains(fullMethod, ".AgentService/") || strings.Contains(fullMethod, ".EnrollmentService/") {
		return ChannelAgent
	}
	return ChannelGuest
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Middleware — HTTP-middleware записи запросов (RecordRequestMetrics). Клуб — из Principal (ставится раньше, в
// Auth.Middleware); WebSocket /ws не считается (долгоживущее соединение). nil Recorder — без записи.
func (r *Recorder) Middleware(next http.Handler) http.Handler {
	if r == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/ws" {
			next.ServeHTTP(w, req)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			var club int64
			if p, ok := principal.FromContext(req.Context()); ok {
				club = p.ClubID
			}
			r.Record(HTTPChannel(req.URL.Path), club, time.Since(start), status)
		}()
		next.ServeHTTP(sw, req)
	})
}

// UnaryInterceptor — запись unary-вызовов gRPC (агент и гость; клуб — из сертификата ПК). Ошибка gRPC с кодом
// Internal/Unknown/Unavailable/DataLoss считается как 5xx.
func (r *Recorder) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		var club int64
		if id, ok := identity.FromContext(ctx); ok {
			club = id.ClubID
		}
		r.Record(GRPCChannel(info.FullMethod), club, time.Since(start), grpcStatus(err))
		return resp, err
	}
}

// StreamInterceptor — в стриме агента каждое входящее сообщение (Status вместо heartbeat старого кода) — один
// запрос канала (длительность 0): так счётчик «Агенты ПК» сопоставим с опросами старого агента.
func (r *Recorder) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &countingStream{ServerStream: ss, r: r, channel: GRPCChannel(info.FullMethod)})
	}
}

type countingStream struct {
	grpc.ServerStream
	r       *Recorder
	channel string
}

func (s *countingStream) RecvMsg(m any) error {
	err := s.ServerStream.RecvMsg(m)
	if err == nil {
		var club int64
		if id, ok := identity.FromContext(s.Context()); ok {
			club = id.ClubID
		}
		s.r.Record(s.channel, club, 0, http.StatusOK)
	}
	return err
}

func grpcStatus(err error) int {
	switch status.Code(err) {
	case codes.Internal, codes.Unknown, codes.Unavailable, codes.DataLoss:
		return http.StatusInternalServerError
	}
	return http.StatusOK
}
