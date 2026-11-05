// Package clubmap — REST-раздел: карта клуба (api/clubmap.yaml). Конвенции — api/README.md.
package clubmap

import (
	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	clubmapapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — права операций раздела: операция → маршрут Laravel (RequireClubArea::ROUTES → право).
var access = httpx.Access{
	Routes: map[string]string{
		"GetClubMap":           "club-map.index",
		"GetClubMapState":      "club-map.state",
		"CreateZone":           "club-map.store",
		"UpdateZone":           "club-map.update",
		"DeleteZone":           "club-map.destroy",
		"UpdateZoneLayout":     "club-map.layout",
		"AddZoneComputer":      "club-computers.store",
		"UpdateComputer":       "club-computers.update",
		"DeleteComputer":       "club-computers.destroy",
		"UpdateComputerLayout": "club-computers.layout",
		"ControlComputers":     "club-computers.control",
		"EndComputerSessions":  "club-computers.end-sessions",
		"NotifyComputers":      "club-computers.notify",
		// Этап 7 (новое): состояние ПК и история проблем — то же право, что у состояния карты (club_map.view).
		"GetComputerHealth":        "club-map.state",
		"ListComputerHealthEvents": "club-map.state",
		"GetComputerHealthWeekly":  "club-map.state",
		"GetComputerHealthHourly":  "club-map.state",
	},
}

// handlers реализует clubmapapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
	// verdict — пороги вердикта недели с умолчаниями (Deps.HealthVerdict, один раз при монтировании).
	verdict health.VerdictRules
}

var _ clubmapapi.StrictServerInterface = (*handlers)(nil)

// Section — карта клуба (api/clubmap.yaml). Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "clubmap", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := clubmapapi.NewStrictHandlerWithOptions(&handlers{d: d, verdict: d.HealthVerdict.WithDefaults()},
		httpx.Middlewares[clubmapapi.StrictMiddlewareFunc](access),
		clubmapapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError(mapError)})
	clubmapapi.HandlerWithOptions(si, clubmapapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
