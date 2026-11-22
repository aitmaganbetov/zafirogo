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

// secretPathParams — параметры маршрутов chi, значение которых — секрет. Сейчас такие параметры есть только у
// приглашений ({token}) и QR-входа ({code}); новый маршрут с параметром, названным так же по другому поводу
// (публичный код), скроет его в журнале — это безопасная сторона ошибки.
var secretPathParams = map[string]bool{"token": true, "code": true}

// secretPathPrefixes — пути, где следующий сегмент — секрет, даже если маршрут не найден (404, не тот метод).
var secretPathPrefixes = []string{"/api/invitations/", "/api/pc-login/", "/api/guest/qr-login/", "/invitations/", "/pc-login/"}

// logPath — путь запроса для журнала: секретные сегменты заменены на ***. Скрывается сегмент на месте параметра в
// шаблоне маршрута, а не все сегменты с тем же значением.
func logPath(r *http.Request) string {
	p := r.URL.Path
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		p = maskRouteParams(p, rctx.RoutePattern(), rctx)
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

// maskRouteParams — сегменты пути на местах секретных параметров шаблона маршрута. Если шаблон с путём не
// сопоставить по сегментам (кодированный «/» в пути и т. п.), секретные значения скрываются везде, где встречаются.
func maskRouteParams(p, pattern string, rctx *chi.Context) string {
	segs, pat := strings.Split(p, "/"), strings.Split(pattern, "/")
	aligned := pattern != "" && len(pat) <= len(segs)
	for i, ps := range pat {
		if !aligned {
			break
		}
		if ps == "*" { // хвост маршрута: дальше сегменты не сопоставляются
			break
		}
		if !strings.Contains(ps, "{") {
			aligned = ps == segs[i]
			continue
		}
		if secretSegment(ps) && segs[i] != "" {
			segs[i] = logMask
		}
	}
	if aligned {
		return strings.Join(segs, "/")
	}
	for i, k := range rctx.URLParams.Keys {
		if !secretPathParams[k] || i >= len(rctx.URLParams.Values) {
			continue
		}
		if v := rctx.URLParams.Values[i]; v != "" {
			p = strings.ReplaceAll(p, "/"+v, "/"+logMask)
		}
	}
	return p
}

// secretSegment — в сегменте шаблона ({token}, {code:[0-9]+}, {a}-{code}) есть секретный параметр.
func secretSegment(seg string) bool {
	for {
		_, rest, ok := strings.Cut(seg, "{")
		if !ok {
			return false
		}
		name, tail, _ := strings.Cut(rest, "}")
		name, _, _ = strings.Cut(name, ":")
		if secretPathParams[name] {
			return true
		}
		seg = tail
	}
}
