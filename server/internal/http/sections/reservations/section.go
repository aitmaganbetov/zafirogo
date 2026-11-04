// Package reservations — REST-раздел: брони ПК (api/reservations.yaml). Конвенции — api/README.md.
package reservations

import (
	"errors"

	"github.com/go-chi/chi/v5"

	reservationsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	booking "github.com/aitmaganbetov/zafirogo/server/internal/reservations"
)

// access — права операций раздела: операция → маршрут Laravel (RequireClubArea::ROUTES → право).
var access = httpx.Access{
	Routes: map[string]string{
		"ListReservations":           "reservations.index",
		"CreateReservation":          "reservations.store",
		"CreateReservationsMany":     "reservations.store-many",
		"SearchReservationCustomers": "reservations.customers",
		"CancelReservation":          "reservations.cancel",
		"ArriveReservation":          "reservations.arrive",
	},
}

type handlers struct {
	d *httpx.Deps
}

var _ reservationsapi.StrictServerInterface = (*handlers)(nil)

// Section — брони ПК. Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "reservations", Mount: mount, Limits: map[string]string{
		"GET /api/reservations/customers": "reservations.customers", // throttle:60,1
	}}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := reservationsapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[reservationsapi.StrictMiddlewareFunc](access),
		reservationsapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError(mapError)})
	reservationsapi.HandlerWithOptions(si, reservationsapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// mapError — ошибки домена броней.
func mapError(err error) *httpx.APIError {
	var be *booking.Error
	switch {
	case errors.As(err, &be):
		return httpx.ValidationMsg(be.Field, be.Message, be.Params)
	case errors.Is(err, booking.ErrNotFound):
		return httpx.NotFound()
	}
	return nil
}
