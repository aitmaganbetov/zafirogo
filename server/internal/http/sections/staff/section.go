// Package staff — REST-раздел: работники клуба, приглашения и права сотрудников (api/staff.yaml).
// Laravel: app/Http/Controllers/WorkerController.php, ClubInvitationController::store(), PermissionController.php.
// Конвенции — api/README.md.
package staff

import (
	"github.com/go-chi/chi/v5"

	staffapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/staff"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — права операций: операция → маршрут Laravel (RequireClubArea::ROUTES → право). Страница «Права»
// закрыта ролью (club-role:owner,admin), а не правом: проверка — в обработчиках (ownerOrAdmin).
var access = httpx.Access{
	Routes: map[string]string{
		"ListWorkers":      "workers.index",
		"AddWorker":        "workers.store",
		"ChangeWorkerRole": "workers.update",
		"RemoveWorker":     "workers.destroy",
		"CreateInvitation": "club-invitations.store",
	},
	Public: []string{"ListPermissionStaff", "GetStaffPermissions", "UpdateStaffPermissions", "ResetStaffPermissions"},
}

// handlers реализует staffapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
}

var _ staffapi.StrictServerInterface = (*handlers)(nil)

// Section — работники, приглашения, права (api/staff.yaml). Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "staff", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := staffapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[staffapi.StrictMiddlewareFunc](access),
		staffapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	staffapi.HandlerWithOptions(si, staffapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
