package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpserver "github.com/aitmaganbetov/zafirogo/server/internal/http"
	"github.com/aitmaganbetov/zafirogo/server/internal/observability"
)

// TestRequestLogsDoNotLeakSecrets — журнал запросов через настоящий роутер: заголовок Authorization, cookie,
// query-строка и тело (пароль) не попадают в журнал ни на одном уровне; запись запроса несёт request_id.
func TestRequestLogsDoNotLeakSecrets(t *testing.T) {
	var buf bytes.Buffer
	o, err := observability.New(context.Background(), observability.Options{Instance: "t"})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(observability.NewLogHandler(
		slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}), o, observability.LogOptions{}))
	router := httpserver.NewRouter(httpserver.Options{Logger: log, Observe: o.HTTPMiddleware})

	secrets := []string{"hdr-secret-token", "cookie-secret-value", "query-secret", "body-secret-password", "zfk_live_abcdef"}
	requests := []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/auth/login?token=query-secret",
			strings.NewReader(`{"phone":"+77001234567","password":"body-secret-password"}`)),
		httptest.NewRequest(http.MethodGet, "/api/v1/customers?api_key=zfk_live_abcdef", nil),
		httptest.NewRequest(http.MethodGet, "/some/spa/page?token=query-secret", nil),
	}
	for _, r := range requests {
		r.Header.Set("Authorization", "Bearer hdr-secret-token")
		r.Header.Set("X-Api-Key", "zfk_live_abcdef")
		r.AddCookie(&http.Cookie{Name: "zafiro_refresh", Value: "cookie-secret-value"})
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(httptest.NewRecorder(), r)
	}
	out := buf.String()
	if !strings.Contains(out, `"msg":"http request"`) {
		t.Fatalf("no request log:\n%s", out)
	}
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("secret %q leaked into logs:\n%s", s, out)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not JSON: %s", line)
		}
		if rec["msg"] == "http request" {
			if id, _ := rec["request_id"].(string); id == "" {
				t.Errorf("request log without request_id: %s", line)
			}
			if strings.Contains(line, "?") {
				t.Errorf("query string logged: %s", line)
			}
		}
	}
}
