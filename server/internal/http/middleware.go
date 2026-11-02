package httpserver

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
)

// RealIP подставляет в r.RemoteAddr IP клиента из X-Forwarded-For / X-Real-IP,
// но ТОЛЬКО если непосредственный собеседник (RemoteAddr) входит в trusted.
// В отличие от chi middleware.RealIP, не доверяет заголовкам от произвольных клиентов
// (иначе rate limit и аудит по IP обходятся подделкой заголовка).
//
// Из X-Forwarded-For берётся самый правый адрес, не принадлежащий доверенным прокси.
func RealIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if len(trusted) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip, ok := clientIP(r, trusted); ok {
				r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request, trusted []netip.Prefix) (netip.Addr, bool) {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	if !isTrusted(peer.Addr().Unmap(), trusted) {
		return netip.Addr{}, false
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		parts := strings.Split(strings.Join(xff, ","), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				return netip.Addr{}, false
			}
			a = a.Unmap()
			if !isTrusted(a, trusted) {
				return a, true
			}
		}
		return netip.Addr{}, false
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		if a, err := netip.ParseAddr(strings.TrimSpace(xrip)); err == nil {
			return a.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

// RequestMeta кладёт в контекст данные запроса для журнала действий (activity.WithRequestMeta): IP клиента
// (r.RemoteAddr после RealIP — с учётом доверенных прокси), User-Agent и публичный префикс API-ключа
// (apiKeyPrefix; nil — без ключей). Laravel: ActivityLogger::record() — request()->ip(), userAgent(),
// attributes['api_key']['prefix']. Ставится на каждый запрос до обработчиков (вход, REST, /ws).
func RequestMeta(apiKeyPrefix func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m := activity.RequestMeta{IP: hostOnly(r.RemoteAddr), UserAgent: r.UserAgent()}
			if apiKeyPrefix != nil {
				m.APIKeyPrefix = apiKeyPrefix(r)
			}
			next.ServeHTTP(w, r.WithContext(activity.WithRequestMeta(r.Context(), m)))
		})
	}
}

// RequestLogger пишет одну запись на запрос: метод, путь (без query — там могут быть токены),
// статус, размер ответа, длительность, request id, IP. Тела и заголовки не логируются.
func RequestLogger(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			switch {
			case status >= 500:
				level = slog.LevelError
			case status >= 400:
				level = slog.LevelWarn
			case r.URL.Path == "/healthz" || r.URL.Path == "/readyz":
				level = slog.LevelDebug
			}
			l.LogAttrs(r.Context(), level, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", middleware.GetReqID(r.Context())),
				slog.String("remote_ip", hostOnly(r.RemoteAddr)),
			)
		})
	}
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// Recoverer перехватывает panic, пишет стек в лог и отвечает 500 без подробностей.
func Recoverer(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler { //nolint:errorlint // сравнение с sentinel по контракту net/http
					panic(rec)
				}
				l.ErrorContext(r.Context(), "panic",
					slog.Any("panic", rec),
					slog.String("request_id", middleware.GetReqID(r.Context())),
					slog.String("stack", string(debug.Stack())),
				)
				if r.Header.Get("Connection") != "Upgrade" {
					writeError(w, http.StatusInternalServerError, "internal_error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// themeScriptHash — sha256 inline-скрипта темы в web/index.html (перенос из
// Zafiro layouts/admin.blade.php). При изменении текста скрипта пересчитать:
//
//	printf %s "<текст скрипта>" | openssl dgst -sha256 -binary | base64
const themeScriptHash = "'sha256-Q0ff/urOFGMeyQTTciYPZpaSp2g7nEe6ZluM9LDruS0='"

// contentSecurityPolicy — CSP для SPA. Скрипты — только с того же origin + inline-скрипт темы по хешу.
// 'unsafe-inline' только для стилей (атрибуты style в React-компонентах).
// Внешних источников нет: Inter и Font Awesome 6.4.0 — из npm в сборке (решение Q-064).
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' " + themeScriptHash + "; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self' " + cspHostPlaceholder + "; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// cspHostPlaceholder — место в contentSecurityPolicy для WebSocket своего хоста (cspFor).
const cspHostPlaceholder = "{ws-self}"

// cspFor — CSP ответа: WebSocket — только на хост запроса (wss://host, ws://host), а не на любой хост (`ws: wss:`
// открывали канал утечки данных при любой XSS, SEC-019). 'self' в connect-src покрывает ws(s) не во всех браузерах,
// поэтому хост указан явно. Host проверен net/http (недопустимые символы — 400 до обработчика).
func cspFor(host string) string {
	ws := "" // без Host (HTTP/1.0) — только 'self'
	if host != "" && !strings.ContainsAny(host, " ;,'\"") {
		ws = "wss://" + host + " ws://" + host
	}
	return strings.Replace(contentSecurityPolicy, cspHostPlaceholder, ws, 1)
}

// SecurityHeaders выставляет защитные заголовки на каждый ответ.
// HSTS — на каждый ответ (SEC-018): за TLS-прокси r.TLS == nil, и раньше заголовка не было вовсе, если его забыли
// в конфигурации прокси. По RFC 6797 §8.1 браузер игнорирует HSTS, полученный по HTTP, поэтому для локальной
// разработки по http:// это ничего не меняет (решение — docs/open-questions/stage6-security.md SQ-3).
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Content-Security-Policy", cspFor(r.Host))
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}
