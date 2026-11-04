// Package tariffs — REST-раздел: тарифы (api/tariffs.yaml). Конвенции — api/README.md.
package tariffs

import (
	"github.com/go-chi/chi/v5"

	tariffsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/tariffs"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — права операций раздела: операция → маршрут Laravel (RequireClubArea::ROUTES → право).
var access = httpx.Access{
	Routes: map[string]string{
		"ListTariffs":    "tariffs.index",
		"GetTariffsPage": "tariffs.index",
		"CreateTariff":   "tariffs.store",
		"UpdateTariff":   "tariffs.update",
		"DeleteTariff":   "tariffs.destroy",
	},
}

// handlers реализует tariffsapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
}

var _ tariffsapi.StrictServerInterface = (*handlers)(nil)

// Section — тарифы (api/tariffs.yaml). Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "tariffs", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := tariffsapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[tariffsapi.StrictMiddlewareFunc](access),
		tariffsapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	tariffsapi.HandlerWithOptions(si, tariffsapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
