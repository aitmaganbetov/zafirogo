// Package customers — REST-раздел: клиенты клуба и их балансы (api/customers.yaml). Конвенции — api/README.md.
package customers

import (
	"github.com/go-chi/chi/v5"

	customersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/customers"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — права операций раздела: операция → маршрут Laravel (RequireClubArea::ROUTES → право).
var access = httpx.Access{
	Routes: map[string]string{
		"ListCustomers":             "users.index",
		"GetCustomer":               "users.index",
		"ListCustomerLedger":        "users.index",
		"AdjustCustomerBalance":     "users.update",
		"SetCustomerInitialBalance": "users.store",
		"CreateCustomer":            "users.store",
		"UpdateCustomer":            "users.update",
		"ResetCustomerPassword":     "users.password",
	},
}

// limits — routes/web.php: users.password — throttle:20,1,users.password.
var limits = map[string]string{
	"POST /api/customers/{userId}/password": "users.password",
}

// handlers реализует customersapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
}

var _ customersapi.StrictServerInterface = (*handlers)(nil)

// Section — клиенты клуба (api/customers.yaml). Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "customers", Mount: mount, Limits: limits}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := customersapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[customersapi.StrictMiddlewareFunc](access),
		customersapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	customersapi.HandlerWithOptions(si, customersapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
