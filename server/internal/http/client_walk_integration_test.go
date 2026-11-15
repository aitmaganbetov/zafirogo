//go:build integration

package httpserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Аудит безопасности A-07: «сотрудник» — роль персонала клуба (ClubRoles::isStaff()), а не «роль не пустая».
// Клиент клуба с сессией QR-входа (pc_login) обходит ВСЕ маршруты роутера: успешный ответ — только на задуманных
// публичных и на своих (вход, подтверждение QR, приглашение). Список зон клуба (подключение ПК) и билет WebSocket
// панели он не получает; сотрудник без единого права (оператор) — получает, как в Laravel
// (ClubComputerPolicy::register смотрит роль, колокольчик и сокет — персоналу).
func TestClientIsNotStaffOnAnyRoute(t *testing.T) {
	e := newStaffEnv(t)
	_, phone := e.member("Клиент", "client", "", false)
	r := do(t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": phone, "password": "pw", "pc_login": true}, nil)
	if r.status != 200 {
		t.Fatalf("client login: %d %v", r.status, r.body)
	}
	client := r.body["access_token"].(string)
	// Старые клиенты бывают с другой не-сотруднической ролью (ClubRoles: «Пользователь») — тоже не персонал.
	_, legacyPhone := e.member("Старый клиент", "Пользователь", "", false)
	r = do(t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": legacyPhone, "password": "pw", "pc_login": true}, nil)
	if r.status != 200 {
		t.Fatalf("legacy client login: %d %v", r.status, r.body)
	}
	legacyClient := r.body["access_token"].(string)
	_, operator := e.staff("Оператор без прав", "operator", "{}")

	// Успех клиенту разрешён только здесь (публичное и своё).
	allowed := map[string]bool{
		"GET /api/forgot-password": true, "GET /api/i18n": true, "GET /api/i18n/{locale}": true, "GET /api/landing": true,
		"GET /api/layout/badges": true, // счётчики по правам: клиенту — нули (LayoutBadges)
		"GET /api/auth/me":       true, "POST /api/auth/logout": true, "POST /api/auth/logout-all": true,
		"GET /api/pc-login/{code}": true, "POST /api/pc-login/{code}": true,
		"GET /api/invitations/{token}": true, "POST /api/invitations/{token}/accept": true,
		"GET /docs/api": true, "GET /docs/api/openapi.yaml": true, "GET /healthz": true, "GET /readyz": true,
		"GET /manifest.webmanifest": true, "GET /offline": true,
	}
	type route struct{ method, pattern string }
	var routes []route
	if err := chi.Walk(NewRouter(Options{}).(chi.Routes), func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		pattern = strings.ReplaceAll(pattern, "/*/", "/")
		switch {
		case strings.HasSuffix(pattern, "/*"), strings.HasPrefix(pattern, "/api/v1"): // SPA/404; /api/v1 — по API-ключу
		case method == "GET", method == "POST", method == "PUT", method == "PATCH", method == "DELETE":
			routes = append(routes, route{method, pattern})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(routes) < 150 {
		t.Fatalf("only %d routes walked", len(routes))
	}
	for name, tok := range map[string]string{"client": client, "legacy client": legacyClient} {
		for _, rt := range routes {
			key := rt.method + " " + rt.pattern
			if allowed[key] || key == "POST /api/auth/logout" || key == "POST /api/auth/logout-all" ||
				strings.HasPrefix(rt.pattern, "/api/auth/") {
				continue // вход/выход/сессия — не клуб; выход закрыл бы токен обхода
			}
			var body any
			if rt.method != "GET" {
				body = map[string]any{}
			}
			res := e.send(rt.method, tok, walkParam.ReplaceAllString(rt.pattern, "1"), "walk-"+name, body)
			if res.status >= 200 && res.status < 300 {
				t.Errorf("%s: %s → %d %v", name, key, res.status, res.body)
			}
		}
		// Две находки аудита — явно.
		if res := e.send("GET", tok, "/api/agent-enrollment", "", nil); res.status != 403 {
			t.Errorf("%s: GET /api/agent-enrollment → %d %v (zones of the club leak)", name, res.status, res.body)
		}
		if res := e.send("POST", tok, "/api/ws-ticket", "", nil); res.status != 403 {
			t.Errorf("%s: POST /api/ws-ticket → %d %v", name, res.status, res.body)
		}
	}
	// Персонал без прав разделов эти два маршрута проходит (в этом окружении сервисы не подключены — 503, не 403).
	for _, path := range []string{"GET /api/agent-enrollment", "POST /api/ws-ticket"} {
		method, p, _ := strings.Cut(path, " ")
		if res := e.send(method, operator, p, "", nil); res.status == 403 || res.status == 401 {
			t.Errorf("operator: %s → %d %v", path, res.status, res.body)
		}
	}
}
