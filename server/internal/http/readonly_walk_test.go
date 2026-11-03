package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

var walkParam = regexp.MustCompile(`\{[^}]+\}`)

// writeRoutes — все маршруты роутера с методом, меняющим состояние.
func writeRoutes(t *testing.T, h http.Handler) [][2]string {
	t.Helper()
	cr, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("router %T is not chi.Routes", h)
	}
	var out [][2]string
	if err := chi.Walk(cr, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
			return nil
		}
		route = strings.ReplaceAll(route, "/*/", "/")
		out = append(out, [2]string{method, route})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// SEC-001: режим просмотра суперадмина (решение Q-054) — запись запрещена на уровне HTTP для ЛЮБОГО маршрута
// панели, не только для операций из Access.Routes: обработчики Public-операций проверяют права сами и
// могли забыть ReadOnly. Разрешены кабинет суперадмина (выход из режима просмотра), выход и обновление сессии.
func TestReadOnlyCannotWriteAnywhere(t *testing.T) {
	router := NewRouter(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	viewing := principal.Principal{UserID: 1, ClubID: 3, Role: permissions.RoleOwner, Permissions: permissions.All(),
		Superadmin: true, ReadOnly: true}
	// Laravel: routeIs('superadmin.*', 'auth.logout') — выход и обновление сессии; вход и билет WebSocket.
	allowed := map[string]bool{
		"POST /api/auth/login":      true,
		"POST /api/auth/refresh":    true,
		"POST /api/auth/logout":     true,
		"POST /api/auth/logout-all": true,
		"POST /api/ws-ticket":       true,
		// Вне current-club в Laravel (SQ-5): своя учётная запись, не клуб.
		"POST /api/auth/register":              true,
		"POST /api/forgot-password":            true,
		"POST /api/reset-password":             true,
		"POST /api/pc-login/{code}":            true,
		"POST /api/invitations/{token}/accept": true,
		// Второй фактор входа суперадмина (Этап 7): шаг входа, не клуб.
		"POST /api/auth/second-factor/setup":  true,
		"POST /api/auth/second-factor/verify": true,
	}
	for _, mr := range writeRoutes(t, router) {
		method, route := mr[0], mr[1]
		key := method + " " + route
		path := walkParam.ReplaceAllString(route, "1")
		// Сброс второго фактора суперадмина (Этап 7) — кабинет, но в режиме просмотра запрещён.
		resetSecondFactor := strings.HasPrefix(path, "/api/superadmin/accounts/") && strings.HasSuffix(path, "/second-factor")
		if !resetSecondFactor && (strings.HasPrefix(path, "/api/v1") || strings.HasPrefix(path, "/api/superadmin/") || !strings.HasPrefix(path, "/api/")) {
			// /api/v1 — API-ключ, не сессия; кабинет суперадмина — RequireSuperadmin (не клуб).
			continue
		}
		req := httptest.NewRequestWithContext(principal.WithPrincipal(context.Background(), viewing), method, path,
			strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "k1")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if allowed[key] {
			continue
		}
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissions.CodeReadOnly) {
			t.Errorf("%s: read-only got %d %s", key, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
}
