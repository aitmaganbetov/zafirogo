package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// A-12: JSON-тело с Content-Type не application/json на REST панели и кабинета — 415; запросы без тела,
// multipart, /api/v1 и старый REST мобильных (слой legacy раньше в цепочке) — как раньше.
func TestRequireJSONBody(t *testing.T) {
	legacyHit := false
	// Заглушка слоя legacy: перехватывает свои пути и дальше по цепочке не пускает (как legacy.API.Middleware).
	legacy := func(_ *httpx.Deps, _ http.Handler) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/staff/") {
					legacyHit = true
					w.WriteHeader(http.StatusNoContent)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
	}
	router := NewRouter(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Legacy: legacy})
	const login = `{"login":"+77010000002","password":"x"}`
	for _, tc := range []struct {
		name, method, path, ct, body string
		chunked                      bool
		rejected                     bool
	}{
		{"панель: text/plain", "POST", "/api/auth/login", "text/plain", login, false, true},
		{"панель: text/plain;charset", "POST", "/api/auth/login", "text/plain; charset=UTF-8", login, false, true},
		{"панель: без заголовка", "POST", "/api/auth/login", "", login, false, true},
		{"панель: form-urlencoded", "POST", "/api/auth/login", "application/x-www-form-urlencoded", login, false, true},
		{"панель: chunked без заголовка", "POST", "/api/auth/login", "", login, true, true},
		{"кабинет: text/plain", "POST", "/api/superadmin/accounts/5/superadmin", "text/plain", `{"code":"123456"}`, false, true},
		{"панель: PUT text/plain", "PUT", "/api/settings/club", "text/plain", `{}`, false, true},
		{"панель: DELETE с телом text/plain", "DELETE", "/api/services/1", "text/plain", `{}`, false, true},
		{"панель: application/json", "POST", "/api/auth/login", "application/json", login, false, false},
		{"панель: application/json; charset", "POST", "/api/auth/login", "application/json; charset=utf-8", login, false, false},
		{"панель: multipart", "POST", "/api/settings/signin-image", "multipart/form-data; boundary=x", "--x--\r\n", false, false},
		{"панель: POST без тела", "POST", "/api/auth/logout", "", "", false, false},
		{"панель: POST без тела, text/plain", "POST", "/api/auth/refresh", "text/plain", "", false, false},
		{"панель: GET", "GET", "/api/auth/me", "", "", false, false},
		{"v1: text/plain", "POST", "/api/v1/top-ups", "text/plain", `{}`, false, false},
		{"v1: без заголовка", "POST", "/api/v1/top-ups", "", `{}`, false, false},
		{"legacy: text/plain", "POST", "/api/staff/cash/top-ups", "text/plain", `{}`, false, false},
		{"legacy: без заголовка", "POST", "/api/staff/cash/top-ups", "", `{}`, false, false},
		{"не API: text/plain", "POST", "/login", "text/plain", "x", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.ct != "" {
				req.Header.Set("Content-Type", tc.ct)
			}
			if tc.chunked {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			got := rec.Code == http.StatusUnsupportedMediaType
			if got != tc.rejected {
				t.Fatalf("status %d (body %s), want rejected=%v", rec.Code, rec.Body.String(), tc.rejected)
			}
			if got && !strings.Contains(rec.Body.String(), `"unsupported_media_type"`) {
				t.Fatalf("body %s", rec.Body.String())
			}
		})
	}
	if !legacyHit {
		t.Fatal("legacy layer was not reached")
	}
}
