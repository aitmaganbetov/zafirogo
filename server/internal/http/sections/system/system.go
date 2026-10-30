// Package system — служебные эндпоинты /healthz и /readyz (api/system.yaml).
package system

import (
	"context"
	"log/slog"

	"github.com/go-chi/chi/v5"

	systemapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/system"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

var access = httpx.Access{Public: []string{"GetHealthz", "GetReadyz"}}

type handlers struct {
	d *httpx.Deps
}

var _ systemapi.StrictServerInterface = (*handlers)(nil)

// Section — служебные эндпоинты.
func Section() httpx.Section {
	return httpx.Section{Name: "system", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := systemapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[systemapi.StrictMiddlewareFunc](access),
		systemapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	systemapi.HandlerWithOptions(si, systemapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// GetHealthz — liveness: процесс жив.
func (h *handlers) GetHealthz(_ context.Context, _ systemapi.GetHealthzRequestObject) (systemapi.GetHealthzResponseObject, error) {
	return systemapi.GetHealthz200JSONResponse{Status: systemapi.HealthStatusOk}, nil
}

// GetReadyz — readiness: все зависимости доступны.
func (h *handlers) GetReadyz(ctx context.Context, _ systemapi.GetReadyzRequestObject) (systemapi.GetReadyzResponseObject, error) {
	res := systemapi.Readiness{Status: systemapi.ReadinessStatusOk, Checks: map[string]systemapi.ReadinessChecks{}}
	timeout := h.d.CheckTimeout
	for _, c := range h.d.Checkers {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		err := c.Check(cctx)
		cancel()
		if err != nil {
			// Текст ошибки только в лог: наружу он может раскрыть адреса и имена.
			h.d.Log().WarnContext(ctx, "readiness check failed",
				slog.String("check", c.Name()), slog.String("err", err.Error()))
			res.Checks[c.Name()] = systemapi.ReadinessChecksFail
			res.Status = systemapi.ReadinessStatusFail
			continue
		}
		res.Checks[c.Name()] = systemapi.ReadinessChecksOk
	}
	if res.Status != systemapi.ReadinessStatusOk {
		return systemapi.GetReadyz503JSONResponse(res), nil
	}
	return systemapi.GetReadyz200JSONResponse(res), nil
}
