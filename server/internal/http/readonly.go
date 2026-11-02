package httpserver

import (
	"net/http"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// readOnlyGuard — режим просмотра клуба суперадмином (решение Q-054): любой запрос, меняющий состояние, к REST
// панели — 403 read_only на уровне роутера, до обработчика. Laravel: SetCurrentClub::viewAsSuperadmin() —
// `! $request->isMethodSafe() && ! $request->routeIs('superadmin.*', 'auth.logout')`.
//
// Раньше запрет проверял каждый обработчик (Access.Routes или своя проверка Public-операций); одна забытая
// проверка открывала запись в чужой клуб из режима просмотра (SEC-001, docs/security-review.md).
func readOnlyGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := principal.FromContext(r.Context()); ok && p.ReadOnly && !readOnlyAllowed(r.Method, r.URL.Path) {
			permissions.WriteError(w, http.StatusForbidden, permissions.CodeReadOnly, permissions.MsgReadOnly)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// readOnlyAllowed — запросы, разрешённые в режиме просмотра: безопасные методы, кабинет суперадмина
// (superadmin.*), выход и обновление сессии (auth.logout), билет WebSocket (только чтение событий) и маршруты
// Laravel вне current-club (docs/open-questions/stage6-security.md SQ-5).
func readOnlyAllowed(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	// Действия своей учётной записи вне клуба (в Laravel — не под current-club): подтверждение входа на ПК по QR,
	// принятие приглашения, восстановление пароля, регистрация.
	if strings.HasPrefix(path, "/api/superadmin/") || strings.HasPrefix(path, "/api/pc-login/") ||
		(strings.HasPrefix(path, "/api/invitations/") && strings.HasSuffix(path, "/accept")) {
		return true
	}
	switch path {
	case "/api/auth/logout", "/api/auth/logout-all", "/api/auth/refresh", "/api/auth/login", "/api/ws-ticket",
		"/api/auth/register", "/api/forgot-password", "/api/reset-password":
		return true
	}
	return false
}
