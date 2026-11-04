package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
)

// Запрещённые метки: идентификаторы тенанта, людей и ПК, контакты — кардинальность растёт с данными (CLAUDE.md,
// «Без club_id/user_id в метках»).
var (
	forbiddenLabelName = regexp.MustCompile(`(?i)(club|user|customer|member|computer|pc_?id|session|phone|email|ip|path|url|subject|amount|tiyn)`)
	// Значение метки с длинным числом — почти наверняка id (коды HTTP — три цифры, sqlstate — 5 символов).
	idLikeValue = regexp.MustCompile(`\d{6,}|/\d+(/|$)`)
)

// exercise прогоняет через Observer все виды событий, включая «опасные» входы с id в пути и значениях.
func exercise(t *testing.T, o *Observer) {
	t.Helper()
	r := chi.NewRouter()
	r.Use(middleware.RequestID, o.HTTPMiddleware)
	r.Get("/api/clubs/{club}/customers/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Post("/api/cash/top-up", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	for _, u := range []string{"/api/clubs/1234567/customers/7654321", "/api/clubs/42/customers/9", "/club-computers/123456",
		"/api/unknown/9999999"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, u, nil))
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/cash/top-up", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PROPFIND", "/x", nil))

	unary := o.UnaryServerInterceptor()
	for _, code := range []codes.Code{codes.OK, codes.Unauthenticated, codes.ResourceExhausted} {
		_, _ = unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/zafiro.agent.v1.GuestService/Login"},
			func(context.Context, any) (any, error) { return nil, status.Error(code, "x") })
	}

	ev := o.WrapAuthEvents(auth.NopEvents{})
	ev.LoginFailed(context.Background(), "+77001234567", auth.ReasonInvalidCredentials)
	ev.LoginFailed(context.Background(), "+77001234567", "Причина с пробелами и 1234567")
	ev.LoginSucceeded(context.Background(), 1234567, 7654321, "Иван")

	tr := o.NewPgxTracer("platform")
	conn := new(pgx.Conn)
	run(tr, conn, "SELECT * FROM users WHERE id = 1234567", nil, "SELECT 1", nil)
	run(tr, conn, "SELECT pg_try_advisory_lock(hashtext($1))", []any{"job:club 1234567"}, "SELECT 1", nil)
	run(tr, conn, "SELECT pg_advisory_unlock(hashtext($1))", []any{"job:club 1234567"}, "SELECT 1", nil)

	l := slog.New(NewLogHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil), o, LogOptions{}))
	l.Warn("tick failed", "job", "monitoring:collect", "club_id", 1234567)
}

func TestMetricLabelsHaveNoTenantCardinality(t *testing.T) {
	o := newTestObserver(t)
	exercise(t, o)
	families, err := o.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) < 20 {
		t.Fatalf("too few metric families: %d", len(families))
	}
	for _, f := range families {
		if !strings.HasPrefix(f.GetName(), "zafiro_") && !strings.HasPrefix(f.GetName(), "go_") &&
			!strings.HasPrefix(f.GetName(), "process_") && !strings.HasPrefix(f.GetName(), "promhttp_") {
			t.Errorf("metric %s: unexpected prefix", f.GetName())
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if forbiddenLabelName.MatchString(lp.GetName()) {
					t.Errorf("metric %s: forbidden label name %q", f.GetName(), lp.GetName())
				}
				if idLikeValue.MatchString(lp.GetValue()) {
					t.Errorf("metric %s: label %s=%q looks like an id", f.GetName(), lp.GetName(), lp.GetValue())
				}
			}
		}
	}
	// Шаблон маршрута вместо пути.
	if got := testutil.ToFloat64(o.m.httpRequests.WithLabelValues("GET", "/api/clubs/{club}/customers/{id}", "200")); got != 2 {
		t.Errorf("route template counter = %v, want 2", got)
	}
	if got := testutil.ToFloat64(o.m.httpRequests.WithLabelValues("GET", routeUnmatched, "404")); got != 1 {
		t.Errorf("unmatched api = %v", got)
	}
	if got := testutil.ToFloat64(o.m.httpRequests.WithLabelValues("GET", routeSPA, "404")); got != 1 {
		t.Errorf("spa = %v", got)
	}
	if got := testutil.ToFloat64(o.m.rateLimited.WithLabelValues("http", "/api/cash/top-up")); got != 1 {
		t.Errorf("rate limited http = %v", got)
	}
	if got := testutil.ToFloat64(o.m.rateLimited.WithLabelValues("grpc", "/zafiro.agent.v1.GuestService/Login")); got != 1 {
		t.Errorf("rate limited grpc = %v", got)
	}
	if got := testutil.ToFloat64(o.m.authLogins.WithLabelValues("failed", "invalid_credentials")); got != 1 {
		t.Errorf("login failures = %v", got)
	}
	if got := testutil.ToFloat64(o.m.authLogins.WithLabelValues("failed", labelOther)); got != 1 {
		t.Errorf("free-form reason must become other: %v", got)
	}
	if got := testutil.ToFloat64(o.m.workerErrors.WithLabelValues("monitoring:collect")); got != 1 {
		t.Errorf("worker errors from log = %v", got)
	}
}

func TestLabelGuardCaps(t *testing.T) {
	g := &labelGuard{max: 2}
	if g.value("a") != "a" || g.value("b") != "b" || g.value("a") != "a" {
		t.Fatal("known values must pass")
	}
	if got := g.value("c"); got != labelOther {
		t.Fatalf("over cap = %q", got)
	}
}

func TestLogHandlerEnrichesAndRedacts(t *testing.T) {
	var buf bytes.Buffer
	o := newTestObserver(t)
	l := slog.New(NewLogHandler(slog.NewJSONHandler(&buf, nil), o, LogOptions{}))
	tid, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	sid, _ := trace.SpanIDFromHex("0123456789abcdef")
	ctx := trace.ContextWithSpanContext(WithRequestID(context.Background(), "req-1"),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled}))
	l.With("password", "with-secret").InfoContext(ctx, "login",
		"password", "p@ss-secret", "authorization", "Bearer tok-secret", "note", "bearer abc-secret",
		"api_key", "zfk_live_secret", "refresh_token", "rt-secret", "phone_masked", "+7700***",
		slog.Group("req", "cookie", "zafiro_session=cookie-secret", "x", "ok"))
	out := buf.String()
	for _, s := range []string{"with-secret", "p@ss-secret", "tok-secret", "abc-secret", "zfk_live_secret", "rt-secret", "cookie-secret"} {
		if strings.Contains(out, s) {
			t.Errorf("secret %q leaked: %s", s, out)
		}
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["request_id"] != "req-1" || rec["trace_id"] != tid.String() || rec["span_id"] != sid.String() {
		t.Errorf("enrichment missing: %v", rec)
	}
	if rec["phone_masked"] != "+7700***" {
		t.Errorf("non-secret attr changed: %v", rec["phone_masked"])
	}

	// Без контекста — без request_id; уже заданный request_id не дублируется.
	buf.Reset()
	l.InfoContext(ctx, "http request", "request_id", "own")
	if n := strings.Count(buf.String(), `"request_id"`); n != 1 {
		t.Errorf("request_id duplicated: %s", buf.String())
	}
}

func TestLogSampling(t *testing.T) {
	var buf bytes.Buffer
	o := newTestObserver(t)
	h := NewLogHandler(slog.NewJSONHandler(&buf, nil), o, LogOptions{SampleInitial: 3, SampleThereafter: 5})
	l := slog.New(h)
	for range 23 {
		l.Info("noisy")
	}
	for range 4 {
		l.Warn("important")
	}
	lines := strings.Count(buf.String(), "\n")
	// 3 первых + каждая 5-я из оставшихся 20 (4) = 7 info в пределах секунды; warn — все 4. Граница секунды может
	// попасть посередине — тогда info больше, но не меньше 7.
	if info := strings.Count(buf.String(), `"noisy"`); info < 7 || info > 14 {
		t.Errorf("sampled info = %d", info)
	}
	if warn := strings.Count(buf.String(), `"important"`); warn != 4 {
		t.Errorf("warn must not be sampled: %d", warn)
	}
	if lines == 27 {
		t.Error("nothing was sampled")
	}
	if got := testutil.ToFloat64(o.m.logDropped.WithLabelValues("info")); got < 1 {
		t.Errorf("dropped counter = %v", got)
	}
}

func TestMetricsHandler(t *testing.T) {
	o := newTestObserver(t)
	exercise(t, o)
	srv := httptest.NewServer(o.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics") //nolint:noctx // тест
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(b.String(), "zafiro_http_requests_total") ||
		!strings.Contains(b.String(), "go_goroutines") {
		t.Fatalf("/metrics = %d\n%s", resp.StatusCode, b.String())
	}
	if resp, err := http.Post(srv.URL+"/metrics", "text/plain", nil); err == nil { //nolint:noctx // тест
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST /metrics = %d", resp.StatusCode)
		}
	}
	if resp, err := http.Get(srv.URL + "/debug/pprof/"); err == nil { //nolint:noctx // тест
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("/debug/pprof = %d, want 404", resp.StatusCode)
		}
	}
}

func TestTracingDisabledByDefault(t *testing.T) {
	o := newTestObserver(t)
	_, span := o.Tracer().Start(context.Background(), "x")
	if span.SpanContext().IsValid() || span.IsRecording() {
		t.Fatal("tracing must be noop without otlp_endpoint")
	}
	if _, err := New(context.Background(), Options{OTLPEndpoint: "ftp://x"}); err == nil {
		t.Fatal("bad endpoint accepted")
	}
	if _, err := New(context.Background(), Options{SampleRatio: 2}); err == nil {
		t.Fatal("bad ratio accepted")
	}
	// Включённая трассировка без доступного коллектора не мешает запуску (экспорт — в фоне).
	on, err := New(context.Background(), Options{OTLPEndpoint: "http://127.0.0.1:1", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, span = on.Tracer().Start(context.Background(), "x")
	if !span.SpanContext().IsValid() {
		t.Fatal("span context must be valid with tracing enabled")
	}
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = on.Shutdown(ctx)
}

func TestNilObserverIsNoop(t *testing.T) {
	var o *Observer
	h := o.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	if o.NewPgxTracer("app") != nil || o.WrapValkey(nil) != nil || o.Registry() != nil {
		t.Fatal("nil observer must be inert")
	}
	_, _ = o.UnaryServerInterceptor()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x/Y"},
		func(context.Context, any) (any, error) { return nil, nil })
	o.SetSources(Sources{})
	_ = o.TrackWorker("x", func(context.Context) error { return nil })(context.Background())
}

// S6-O4-10: отставание автобиллинга — из источника AutoBillingBacklog (число просроченных и возраст старейшего).
func TestAutoBillingBacklogMetrics(t *testing.T) {
	o := newTestObserver(t)
	o.SetSources(Sources{AutoBillingBacklog: func(context.Context) (int64, time.Duration, error) {
		return 1234, 42 * time.Second, nil
	}})
	srv := httptest.NewServer(o.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics") //nolint:noctx // тест
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	for _, want := range []string{"zafiro_agent_auto_billing_due_computers 1234", "zafiro_agent_auto_billing_oldest_due_age_seconds 42"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("/metrics has no %q", want)
		}
	}
}
