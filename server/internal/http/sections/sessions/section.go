// Package sessions — REST-раздел: сессии ПК со стороны сотрудника (api/sessions.yaml). Конвенции — api/README.md.
package sessions

import (
	"github.com/go-chi/chi/v5"

	sessionsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/sessions"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — права операций раздела: операция → маршрут Laravel (RequireClubArea::ROUTES → право).
var access = httpx.Access{
	Routes: map[string]string{
		"SellGuestSession":   "club-computers.guest-session",
		"EndComputerSession": "club-computers.end-sessions",
	},
}

// handlers реализует sessionsapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
}

var _ sessionsapi.StrictServerInterface = (*handlers)(nil)

// Section — сессии ПК со стороны сотрудника (api/sessions.yaml). Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "sessions", Mount: mount, Limits: limits}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := sessionsapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[sessionsapi.StrictMiddlewareFunc](access),
		sessionsapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	sessionsapi.HandlerWithOptions(si, sessionsapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// limits — routes/web.php: club-computers.guest-session — throttle:30,1,club-computers.guest-session.
var limits = map[string]string{
	"POST /api/club-computers/guest-session": "club-computers.guest-session",
}
