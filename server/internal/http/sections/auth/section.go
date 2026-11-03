// Package auth — REST-раздел: вход, сессия, выбор клуба, суперадмин-просмотр, регистрация, приглашения, билет WebSocket (api/auth.yaml). Конвенции — api/README.md.
package auth

import (
	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	authapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — операции входа и сессии проверяют вход сами (у входа и refresh Principal ещё нет).
var access = httpx.Access{
	Public: []string{
		"Login", "RefreshSession", "Logout", "LogoutAll", "GetMe", "SelectClub",
		"SuperadminViewClub", "SuperadminLeaveClub", "Register", "GetInvitation", "AcceptInvitation", "IssueWsTicket",
		"SecondFactorSetup", "SecondFactorVerify",
	},
}

// limits — вход и регистрация: web-login / web-register (в старом лимита не было; включены по умолчанию —
// решение Q-052); принятие приглашения — throttle:10,1,club-invitations.accept (routes/web.php).
var limits = map[string]string{
	"POST /api/auth/login":                 ratelimit.WebLogin,
	"POST /api/auth/register":              ratelimit.WebRegister,
	"POST /api/invitations/{token}/accept": "club-invitations.accept",
}

// handlers реализует authapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
}

var _ authapi.StrictServerInterface = (*handlers)(nil)

// Section — вход, сессия, выбор клуба, режим просмотра суперадмина (api/auth.yaml).
func Section() httpx.Section {
	return httpx.Section{Name: "auth", Mount: mount, Limits: limits}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := authapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[authapi.StrictMiddlewareFunc](access),
		authapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	authapi.HandlerWithOptions(si, authapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
