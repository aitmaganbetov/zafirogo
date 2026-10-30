// Package clubmap — REST-раздел: карта клуба (api/clubmap.yaml). Конвенции — api/README.md.
package clubmap

import (
	"github.com/go-chi/chi/v5"

	clubmapapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — права операций раздела: операция → маршрут Laravel (RequireClubArea::ROUTES → право).
var access = httpx.Access{
	Routes: map[string]string{
		"GetClubMapState": "club-map.state",
	},
}

// handlers реализует clubmapapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
}

var _ clubmapapi.StrictServerInterface = (*handlers)(nil)

// Section — карта клуба (api/clubmap.yaml). Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "clubmap", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := clubmapapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[clubmapapi.StrictMiddlewareFunc](access),
		clubmapapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	clubmapapi.HandlerWithOptions(si, clubmapapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
