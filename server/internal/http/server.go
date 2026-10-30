// Package httpserver собирает HTTP-слой zafiro: chi-роутер, middleware, разделы REST по OpenAPI
// (api/<раздел>.yaml → сгенерированный strict server в ./api/<раздел>, обработчики в ./sections/<раздел>,
// регистрация — sections.go) и вшитый фронт (webui). Конвенции — api/README.md.
//
// Каталог называется internal/http, пакет — httpserver, чтобы не конфликтовать с net/http.
package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/wsticket"
)

// Checker — проверка зависимости для /readyz (например, пинг Postgres).
type Checker = httpx.Checker

// CheckFunc адаптирует функцию к Checker.
type CheckFunc = httpx.CheckFunc

// Presence — присутствие агентов клуба (*presence.Store). nil — все ПК «offline».
type Presence = httpx.Presence

// Options — зависимости роутера.
type Options struct {
	Logger         *slog.Logger
	RequestTimeout time.Duration
	TrustedProxies []netip.Prefix
	// Checkers — проверки для /readyz. Пусто — readyz всегда ok.
	Checkers []Checker
	// CheckTimeout — таймаут одной проверки readyz (по умолчанию 2s).
	CheckTimeout time.Duration
	// Frontend — обработчик SPA (webui.Handler). nil — 404 для всего, кроме API.
	Frontend http.Handler
	// Auth — вход, сессии, Principal (auth.Service). nil — эндпоинты /api/auth/* отвечают 500,
	// Principal в контекст не ставится.
	Auth *auth.Service
	// Limiter — именованные лимиты (ratelimit). nil — без лимитов.
	Limiter *ratelimit.Limiter
	// Mounts — обработчики, монтируемые по пути (например "/ws" — WebSocket админки) за
	// permissions.RequireAuth + permissions.RequireClub. Путь — шаблон chi ("/ws", "/files/*").
	// Для них не действует RequestTimeout (долгоживущие соединения); остальные middleware — общие.
	Mounts map[string]http.Handler

	// REST админки (касса, склад, клиенты, тарифы, сессии ПК, карта клуба). Pool == nil — эндпоинты отвечают 503.
	// Pool — пул логина zafiro_app (тенантные транзакции, RLS).
	Pool db.Beginner
	// PlatformPool — пул логина zafiro_platform (нетенантные таблицы: переводы и т. п.).
	PlatformPool db.Beginner
	// Billing — покупки и сессии ПК (billing/service.New(pool, clock, cash)).
	Billing *billingsvc.Service
	// Cash — касса (cash/service.Service; Notifier — уведомления агента о балансе).
	Cash *cashsvc.Service
	// Presence — присутствие агентов (карта клуба). nil — все ПК офлайн.
	Presence Presence
	// Now — часы (nil — time.Now).
	Now func() time.Time
	// APIKeyPrefix — публичный префикс API-ключа запроса для журнала (details.api_key); nil — нет.
	APIKeyPrefix func(*http.Request) string
	// Valkey — билеты WebSocket (POST /api/ws-ticket → /ws?ticket=…). nil — билетов нет (503).
	Valkey valkey.Client
	// Passwords — хешер паролей (регистрация и приглашения). nil — эти эндпоинты отвечают 503.
	Passwords *password.Hasher
	// Sections — разделы REST; nil — все зарегистрированные (Sections() в sections.go). Для тестов.
	Sections []httpx.Section
}

// limitRoutes применяет лимиты разделов ("МЕТОД /путь" → правило; сегмент {param} — любой непустой сегмент)
// до разбора тела strict-обработчиком.
func limitRoutes(l *ratelimit.Limiter, sections []httpx.Section) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		type route struct {
			method string
			parts  []string
			h      http.Handler
		}
		exact := map[string]http.Handler{}
		var patterns []route
		for _, s := range sections {
			for key, rule := range s.Limits {
				method, path, ok := strings.Cut(key, " ")
				if !ok {
					panic("httpserver: bad limit route " + key)
				}
				h := l.Middleware(rule)(next)
				if !strings.Contains(path, "{") {
					if _, dup := exact[key]; dup {
						panic("httpserver: duplicate limit for " + key)
					}
					exact[key] = h
					continue
				}
				patterns = append(patterns, route{method: method, parts: strings.Split(path, "/"), h: h})
			}
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h, ok := exact[r.Method+" "+r.URL.Path]; ok {
				h.ServeHTTP(w, r)
				return
			}
			if len(patterns) > 0 {
				parts := strings.Split(r.URL.Path, "/")
				for _, p := range patterns {
					if p.method == r.Method && matchParts(p.parts, parts) {
						p.h.ServeHTTP(w, r)
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func matchParts(pattern, path []string) bool {
	if len(pattern) != len(path) {
		return false
	}
	for i, p := range pattern {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			if path[i] == "" {
				return false
			}
			continue
		}
		if p != path[i] {
			return false
		}
	}
	return true
}

// wsTicketAuth — Principal для /ws по одноразовому билету (?ticket=, решение EV-3): браузерный WebSocket не
// ставит Authorization. Билет погашается и превращается в access-токен; дальше — как Auth.Middleware.
func wsTicketAuth(a *auth.Service, tickets *wsticket.Store, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if a == nil || tickets == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ticket := r.URL.Query().Get("ticket")
			if _, ok := principal.FromContext(r.Context()); ok || ticket == "" {
				next.ServeHTTP(w, r)
				return
			}
			access, err := tickets.Redeem(r.Context(), ticket)
			if errors.Is(err, wsticket.ErrInvalid) {
				permissions.Unauthenticated(w)
				return
			}
			if err != nil {
				log.ErrorContext(r.Context(), "ws ticket", slog.String("err", err.Error()))
				permissions.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "")
				return
			}
			p, err := a.Authenticate(r.Context(), access)
			switch {
			case err == nil:
				next.ServeHTTP(w, r.WithContext(principal.WithPrincipal(r.Context(), p)))
			case errors.Is(err, auth.ErrBlocked):
				permissions.Forbidden(w, auth.MsgBlockedAPI)
			case errors.Is(err, auth.ErrUnauthenticated):
				permissions.Unauthenticated(w)
			default:
				log.ErrorContext(r.Context(), "authenticate ws", slog.String("err", err.Error()))
				permissions.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "")
			}
		})
	}
}

// NewRouter собирает корневой обработчик.
func NewRouter(o Options) http.Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 30 * time.Second
	}
	if o.CheckTimeout <= 0 {
		o.CheckTimeout = 2 * time.Second
	}
	sections := o.Sections
	if sections == nil {
		sections = Sections()
	}
	deps := &httpx.Deps{
		Logger: o.Logger, Auth: o.Auth, Pool: o.Pool, PlatformPool: o.PlatformPool, Billing: o.Billing, Cash: o.Cash,
		Presence: o.Presence, Checkers: o.Checkers, CheckTimeout: o.CheckTimeout, Now: o.Now, Passwords: o.Passwords,
	}
	if o.Valkey != nil && o.Auth != nil {
		deps.WSTickets = wsticket.New(o.Valkey, "", 0)
	}

	r := chi.NewRouter()
	r.Use(
		middleware.RequestID,
		RealIP(o.TrustedProxies),
		RequestMeta(o.APIKeyPrefix),
		RequestLogger(o.Logger),
		Recoverer(o.Logger),
		SecurityHeaders,
	)
	if o.Auth != nil {
		// Principal из Authorization: Bearer — до лимитов (ключ «пользователь») и обработчиков.
		r.Use(o.Auth.Middleware)
	}
	r.Use(limitRoutes(o.Limiter, sections))

	for path, h := range o.Mounts {
		if h == nil {
			continue
		}
		r.With(wsTicketAuth(o.Auth, deps.WSTickets, o.Logger), permissions.RequireAuth, permissions.RequireClub).Handle(path, h)
	}

	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(o.RequestTimeout))
		seen := map[string]bool{}
		for _, s := range sections {
			if seen[s.Name] {
				panic("httpserver: duplicate section " + s.Name)
			}
			seen[s.Name] = true
			s.Mount(r, deps)
		}
		// Неизвестные /api/* — JSON 404, не SPA.
		r.Handle("/api/*", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not_found")
		}))
		if o.Frontend != nil {
			r.NotFound(o.Frontend.ServeHTTP)
		}
	})
	return r
}

func writeError(w http.ResponseWriter, status int, code string) { httpx.WriteError(w, status, code) }
