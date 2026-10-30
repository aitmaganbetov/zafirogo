// Package httpserver собирает HTTP-слой zafiro: chi-роутер, middleware,
// REST по OpenAPI (сгенерированный strict server в ./api) и вшитый фронт (webui).
//
// Каталог называется internal/http, пакет — httpserver, чтобы не конфликтовать с net/http.
package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

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
		RequestLogger(o.Logger),
		Recoverer(o.Logger),
		SecurityHeaders,
		middleware.Timeout(o.RequestTimeout),
	)

	strict := api.NewStrictHandlerWithOptions(&handlers{opts: o}, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			o.Logger.WarnContext(r.Context(), "bad request", slog.String("err", err.Error()))
			writeError(w, http.StatusBadRequest, "bad_request")
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			o.Logger.ErrorContext(r.Context(), "handler error", slog.String("err", err.Error()))
			writeError(w, http.StatusInternalServerError, "internal_error")
		},
	})
	api.HandlerFromMux(strict, r)

	// Неизвестные /api/* — JSON 404, не SPA.
	r.Handle("/api/*", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found")
	}))

	if o.Frontend != nil {
		r.NotFound(o.Frontend.ServeHTTP)
	}
	return r
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
