// Package shoporders — REST-раздел: заказы из агента ПК (api/shoporders.yaml). Конвенции — api/README.md.
package shoporders

import (
	"github.com/go-chi/chi/v5"

	shopordersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/shoporders"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — права операций раздела: операция → маршрут Laravel (RequireClubArea::ROUTES → право).
var access = httpx.Access{
	Routes: map[string]string{
		"ListShopOrders":     "shop-orders.index",
		"GetShopOrdersState": "shop-orders.state",
		"MarkShopOrderDone":  "shop-orders.done",
		"CancelShopOrder":    "shop-orders.cancel",
	},
}

// handlers реализует shopordersapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
}

var _ shopordersapi.StrictServerInterface = (*handlers)(nil)

// Section — заказы из агента (api/shoporders.yaml). Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "shoporders", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := shopordersapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[shopordersapi.StrictMiddlewareFunc](access),
		shopordersapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError(mapError)})
	shopordersapi.HandlerWithOptions(si, shopordersapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
