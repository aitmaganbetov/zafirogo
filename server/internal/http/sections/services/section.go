// Package services — REST-раздел: услуги клуба (api/services.yaml). Конвенции — api/README.md.
package services

import (
	"github.com/go-chi/chi/v5"

	servicesapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/services"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — права операций раздела: операция → маршрут Laravel (RequireClubArea::ROUTES → право).
var access = httpx.Access{
	Routes: map[string]string{
		"ListServices":  "services.index",
		"CreateService": "services.store",
		"UpdateService": "services.update",
		"DeleteService": "services.destroy",
	},
}

// handlers реализует servicesapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
}

var _ servicesapi.StrictServerInterface = (*handlers)(nil)

// Section — услуги клуба (api/services.yaml). Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "services", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := servicesapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[servicesapi.StrictMiddlewareFunc](access),
		servicesapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	servicesapi.HandlerWithOptions(si, servicesapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
