// Package warehouse — REST-раздел: склад (api/warehouse.yaml). Конвенции — api/README.md.
package warehouse

import (
	"github.com/go-chi/chi/v5"

	warehouseapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/warehouse"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — права операций раздела: операция → маршрут Laravel (RequireClubArea::ROUTES → право).
var access = httpx.Access{
	Routes: map[string]string{
		"ListProducts":          "warehouse",
		"ListProductCategories": "warehouse",
		"CreateProduct":         "warehouse.products.store",
		"UpdateProduct":         "warehouse.products.update",
		"CreateStockMovement":   "warehouse.stock-movements.store",
	},
}

// handlers реализует warehouseapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
}

var _ warehouseapi.StrictServerInterface = (*handlers)(nil)

// Section — склад (api/warehouse.yaml). Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "warehouse", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := warehouseapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[warehouseapi.StrictMiddlewareFunc](access),
		warehouseapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	warehouseapi.HandlerWithOptions(si, warehouseapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
