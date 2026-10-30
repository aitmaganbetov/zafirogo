// Package layout — каркас панели клуба (api/layout.yaml): бейджи меню.
package layout

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	layoutapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/layout"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Бейджи нужны любому сотруднику с выбранным клубом (меню есть у всех); счётчик раздела — по его праву.
var access = httpx.Access{Public: []string{"GetSidebarBadges"}}

type handlers struct {
	d *httpx.Deps
}

var _ layoutapi.StrictServerInterface = (*handlers)(nil)

// Section — каркас панели клуба.
func Section() httpx.Section {
	return httpx.Section{Name: "layout", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := layoutapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[layoutapi.StrictMiddlewareFunc](access),
		layoutapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	layoutapi.HandlerWithOptions(si, layoutapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// GetSidebarBadges — ChatController::unreadTotal(), ShopOrder::newCount().
func (h *handlers) GetSidebarBadges(ctx context.Context, _ layoutapi.GetSidebarBadgesRequestObject) (layoutapi.GetSidebarBadgesResponseObject, error) {
	p := httpx.Principal(ctx)
	if p.UserID == 0 {
		return nil, httpx.NewAPIError(http.StatusUnauthorized, permissions.CodeUnauthenticated, permissions.MsgUnauthenticated)
	}
	if status, code, msg, ok := permissions.CheckClub(p, http.MethodGet); !ok {
		return nil, httpx.NewAPIError(status, code, msg)
	}
	var out layoutapi.SidebarBadges
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		row, err := q.LayoutBadges(ctx, p.ClubID)
		if err != nil {
			return err
		}
		if p.Can("chat.view") {
			out.Chat = row.ChatUnread
		}
		if p.Can("orders.view") {
			out.Orders = row.OrdersNew
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return layoutapi.GetSidebarBadges200JSONResponse(out), nil
}
