package httpserver

import (
	"net/http"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
)

// IsResellerHost — запрос пришёл на хост кабинета реселлера (ResellerHost::is(): сравнение имени без регистра, без
// порта).
func IsResellerHost(r *http.Request, domain string) bool { return IsSuperadminHost(r, domain) }

// resellerHost — отдельный домен кабинета реселлера (решение UQ-21: resseler.zafiro.kz, написание — как в
// config/app.php старого проекта). Laravel: routes/web.php (кабинет — Route::domain($resellerDomain)),
// app/Http/Middleware/RestrictResellerHost.php: на хосте реселлера доступны только reseller.*, auth.show-login,
// auth.login, auth.logout, auth.password.*; всё остальное уходит в кабинет.
//   - REST кабинета реселлера (/api/reseller/*) отвечает только на хосте реселлера (на другом — 404, маршрутов там
//     нет);
//   - на хосте реселлера — кабинет, вход/сессия, восстановление пароля и переводы; регистрации клуба, панели клуба и
//     кабинета суперадмина здесь нет: их REST — 403 для любого аккаунта (страницы уводит в кабинет SPA).
//
// Пустой domain — кабинет по /reseller на любом хосте (как без RESELLER_DOMAIN).
func resellerHost(domain string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if domain == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			cabinetAPI := p == "/api/reseller" || strings.HasPrefix(p, "/api/reseller/")
			onHost := IsResellerHost(r, domain)
			switch {
			case cabinetAPI && !onHost:
				writeError(w, http.StatusNotFound, "not_found")
				return
			case onHost && strings.HasPrefix(p, "/api/") && !cabinetAPI && !resellerHostAPI(p):
				permissions.Forbidden(w, "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// resellerHostAPI — REST, доступный на хосте реселлера кроме самого кабинета: вход, сессия и второй фактор
// (auth.login, auth.logout), восстановление пароля (auth.password.*), переводы. Регистрация клуба — нет.
func resellerHostAPI(p string) bool {
	switch p {
	case "/api/auth/register":
		return false
	case "/api/forgot-password", "/api/reset-password", "/api/i18n":
		return true
	}
	return strings.HasPrefix(p, "/api/auth/") || strings.HasPrefix(p, "/api/i18n/")
}
