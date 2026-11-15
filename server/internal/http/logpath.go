package httpserver

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Секреты в пути запроса не пишутся в журнал запросов (аудит безопасности A-05): токен приглашения сотрудника
// (/api/invitations/{token} — единственный секрет, дающий роль в клубе на 7 дней) и код QR-входа на ПК
// (/api/pc-login/{code}), а также страницы SPA с теми же секретами. Читатель журналов (или внешней системы сбора
// логов) иначе мог бы принять приглашение раньше адресата. Query не пишется вовсе (билет /ws и т.п.). Метрики и
// трассировка пишут шаблон маршрута chi (observability.routeOf), не путь.
const logMask = "***"

// secretPathParams — параметры маршрутов chi, значение которых — секрет.
var secretPathParams = map[string]bool{"token": true, "code": true}

// secretPathPrefixes — пути, где следующий сегмент — секрет, даже если маршрут не найден (404, не тот метод).
var secretPathPrefixes = []string{"/api/invitations/", "/api/pc-login/", "/api/guest/qr-login/", "/invitations/", "/pc-login/"}

// logPath — путь запроса для журнала: секретные сегменты заменены на ***.
func logPath(r *http.Request) string {
	p := r.URL.Path
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		for i, k := range rctx.URLParams.Keys {
			if !secretPathParams[k] || i >= len(rctx.URLParams.Values) {
				continue
			}
			if v := rctx.URLParams.Values[i]; v != "" {
				p = strings.ReplaceAll(p, "/"+v, "/"+logMask)
			}
		}
	}
	for _, pre := range secretPathPrefixes {
		if !strings.HasPrefix(p, pre) {
			continue
		}
		rest := p[len(pre):]
		seg, tail, _ := strings.Cut(rest, "/")
		if seg == "" || seg == logMask {
			break
		}
		p = pre + logMask
		if tail != "" || strings.HasSuffix(rest, "/") {
			p += "/" + tail
		}
		break
	}
	return p
}
