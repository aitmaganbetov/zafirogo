// Package httpserver собирает HTTP-слой zafiro: chi-роутер, middleware,
// REST по OpenAPI (сгенерированный strict server в ./api) и вшитый фронт (webui).
//
// Каталог называется internal/http, пакет — httpserver, чтобы не конфликтовать с net/http.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/api"
)

// Checker — проверка зависимости для /readyz (например, пинг Postgres).
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// CheckFunc адаптирует функцию к Checker.
type CheckFunc struct {
	CheckName string
	Fn        func(ctx context.Context) error
}

// Name возвращает имя проверки.
func (c CheckFunc) Name() string { return c.CheckName }

// Check выполняет проверку.
func (c CheckFunc) Check(ctx context.Context) error { return c.Fn(ctx) }

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
}

// routeLimits — лимиты эндпоинтов этого роутера: «МЕТОД путь» → правило ratelimit.
// Вход в панель — web-login (в старом лимита не было; включён по умолчанию — решение Q-052, docs/DECISIONS.md).
var routeLimits = map[string]string{
	http.MethodPost + " /api/auth/login": ratelimit.WebLogin,
	// routes/web.php: club-computers.guest-session — throttle:30,1,club-computers.guest-session.
	http.MethodPost + " /api/club-computers/guest-session": "club-computers.guest-session",
}

// limitRoutes применяет routeLimits (до разбора тела strict-обработчиком).
func limitRoutes(l *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		wrapped := make(map[string]http.Handler, len(routeLimits))
		for route, rule := range routeLimits {
			wrapped[route] = l.Middleware(rule)(next)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h, ok := wrapped[r.Method+" "+r.URL.Path]; ok {
				h.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
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
	r.Use(limitRoutes(o.Limiter))

	for path, h := range o.Mounts {
		if h == nil {
			continue
		}
		r.With(permissions.RequireAuth, permissions.RequireClub).Handle(path, h)
	}

	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(o.RequestTimeout))
		routes(r, o)
	})
	return r
}

// routes — REST API, неизвестные /api/* и SPA (с таймаутом запроса).
func routes(r chi.Router, o Options) {

	strict := api.NewStrictHandlerWithOptions(&handlers{opts: o}, []api.StrictMiddlewareFunc{withHTTP, withAdminAuth}, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			o.Logger.WarnContext(r.Context(), "bad request", slog.String("err", err.Error()))
			writeError(w, http.StatusBadRequest, "bad_request")
		},
		ResponseErrorHandlerFunc: writeAPIError(o.Logger),
	})
	api.HandlerWithOptions(strict, api.ChiServerOptions{
		BaseRouter: r,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			// Ошибки параметров (нет Idempotency-Key, неверный id в пути) — 400 до прав и тела.
			code := "bad_request"
			var rh *api.RequiredHeaderError
			if errors.As(err, &rh) && rh.ParamName == "Idempotency-Key" {
				code = "idempotency_key_required"
			}
			o.Logger.WarnContext(r.Context(), "bad request", slog.String("err", err.Error()))
			writeError(w, http.StatusBadRequest, code)
		},
	})

	// Неизвестные /api/* — JSON 404, не SPA.
	r.Handle("/api/*", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found")
	}))

	if o.Frontend != nil {
		r.NotFound(o.Frontend.ServeHTTP)
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// handlers реализует сгенерированный api.StrictServerInterface.
type handlers struct {
	opts Options
}

var _ api.StrictServerInterface = (*handlers)(nil)

// GetHealthz — liveness: процесс жив.
func (h *handlers) GetHealthz(_ context.Context, _ api.GetHealthzRequestObject) (api.GetHealthzResponseObject, error) {
	return api.GetHealthz200JSONResponse{Status: api.HealthStatusOk}, nil
}

// GetReadyz — readiness: все зависимости доступны.
func (h *handlers) GetReadyz(ctx context.Context, _ api.GetReadyzRequestObject) (api.GetReadyzResponseObject, error) {
	res := api.Readiness{Status: api.ReadinessStatusOk, Checks: map[string]api.ReadinessChecks{}}
	for _, c := range h.opts.Checkers {
		cctx, cancel := context.WithTimeout(ctx, h.opts.CheckTimeout)
		err := c.Check(cctx)
		cancel()
		if err != nil {
			// Текст ошибки только в лог: наружу он может раскрыть адреса и имена.
			h.opts.Logger.WarnContext(ctx, "readiness check failed",
				slog.String("check", c.Name()), slog.String("err", err.Error()))
			res.Checks[c.Name()] = api.ReadinessChecksFail
			res.Status = api.ReadinessStatusFail
			continue
		}
		res.Checks[c.Name()] = api.ReadinessChecksOk
	}
	if res.Status != api.ReadinessStatusOk {
		return api.GetReadyz503JSONResponse(res), nil
	}
	return api.GetReadyz200JSONResponse(res), nil
}
