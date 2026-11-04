package httpserver

import (
	"net"
	"net/http"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// IsSuperadminHost — запрос пришёл на хост кабинета (SuperadminHost::is(): сравнение имени без регистра, без порта).
func IsSuperadminHost(r *http.Request, domain string) bool {
	if domain == "" {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.EqualFold(host, domain)
}

// superadminHost — отдельный домен кабинета (решение Q-069). Laravel: routes/web.php (кабинет — Route::domain()),
// app/Http/Middleware/RestrictSuperadminHost.php.
//   - REST кабинета (/api/superadmin/*) отвечает только на хосте кабинета (на другом — 404, маршрутов там нет);
//   - на хосте кабинета — кабинет, вход/сессия (auth.*), переводы и WebSocket-билет; REST панели клуба — только
//     суперадмину в режиме просмотра клуба, иначе 403 (старый код перенаправлял страницу в кабинет — это делает SPA).
//
// Пустой domain — кабинет по /superadmin на любом хосте (как без SUPERADMIN_DOMAIN).
func superadminHost(domain string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if domain == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			cabinetAPI := strings.HasPrefix(p, "/api/superadmin/")
			onCabinet := IsSuperadminHost(r, domain)
			switch {
			case cabinetAPI && !onCabinet:
				writeError(w, http.StatusNotFound, "not_found")
				return
			case onCabinet && strings.HasPrefix(p, "/api/") && !cabinetAPI && !cabinetHostAPI(p):
				if pr, ok := principal.FromContext(r.Context()); !ok || !pr.Superadmin || !pr.ReadOnly {
					permissions.Forbidden(w, "")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// cabinetHostAPI — REST, доступный на хосте кабинета всем (вход, сессия, переводы, билет WebSocket).
func cabinetHostAPI(p string) bool {
	return strings.HasPrefix(p, "/api/auth/") || p == "/api/i18n" || strings.HasPrefix(p, "/api/i18n/") || p == "/api/ws-ticket"
}
