package shoporders

// Заказы из агента: список по статусу, состояние (новые, последний id), «Выдан», «Отменить».
// Laravel: app/Http/Controllers/ShopOrderController.php, app/Services/AgentShop.php handOver(), cancel().

import (
	"context"
	"errors"

	shopordersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/shoporders"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// perPage — index(): paginate(30).
const perPage = 30

// mapError — ошибки пакета shop: «Заказ уже обработан.» — 422 на поле order, заказа нет — 404.
func mapError(err error) *httpx.APIError {
	var se *shop.Error
	switch {
	case errors.Is(err, shop.ErrOrderNotFound):
		return httpx.NotFound()
	case errors.As(err, &se):
		return httpx.ValidationMsg(se.Field, se.Message, se.Params)
	}
	return nil
}

func linesView(raw []byte) []shopordersapi.ShopOrderLine {
	out := []shopordersapi.ShopOrderLine{}
	lines, err := shop.DecodeItems(raw)
	if err != nil {
		return out // нечитаемые строки — пустой список (в старом — «—» по каждой)
	}
	for _, l := range lines {
		v := shopordersapi.ShopOrderLine{Name: l.Name, Quantity: l.Quantity, TotalTiyn: int64(l.Total)}
		if l.ProductID > 0 {
			v.ProductId = httpx.Ptr(l.ProductID)
		}
		if l.Price > 0 {
			v.PriceTiyn = httpx.Ptr(int64(l.Price))
		}
		out = append(out, v)
	}
	return out
}

// ListShopOrders — index(): заказы по статусу (по умолчанию новые), новые сверху, paginate(30).
func (h *handlers) ListShopOrders(ctx context.Context, req shopordersapi.ListShopOrdersRequestObject) (shopordersapi.ListShopOrdersResponseObject, error) {
	status := shopordersapi.ListShopOrdersParamsStatus(shop.StatusNew)
	if req.Params.Status != nil {
		status = *req.Params.Status
	}
	filter := string(status)
	if status == shopordersapi.ListShopOrdersParamsStatusAll {
		filter = ""
	}
	offset := int32(0)
	if req.Params.Page != nil && *req.Params.Page > 1 {
		offset = int32(*req.Params.Page-1) * perPage // #nosec G115 -- page ≤ 100000 (схема)
	}
	out := shopordersapi.ShopOrderPage{Items: []shopordersapi.ShopOrder{}, PerPage: perPage, Status: shopordersapi.ShopOrderPageStatus(status)}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		total, err := q.ShopOrdersCount(ctx, store.ShopOrdersCountParams{ClubID: clubID, Status: filter})
		if err != nil {
			return err
		}
		out.Total = total
		st, err := q.ShopOrdersState(ctx, clubID)
		if err != nil {
			return err
		}
		out.NewCount, out.Latest = st.NewCount, st.Latest
		rows, err := q.ShopOrdersPage(ctx, store.ShopOrdersPageParams{ClubID: clubID, Status: filter, PageSize: perPage, PageOffset: offset})
		for _, r := range rows {
			out.Items = append(out.Items, orderView(r))
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return shopordersapi.ListShopOrders200JSONResponse(out), nil
}

// GetShopOrdersState — state(): число новых и max(id).
func (h *handlers) GetShopOrdersState(ctx context.Context, _ shopordersapi.GetShopOrdersStateRequestObject) (shopordersapi.GetShopOrdersStateResponseObject, error) {
	var out shopordersapi.ShopOrdersState
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		st, err := q.ShopOrdersState(ctx, httpx.ClubID(ctx))
		out.New, out.Latest = st.NewCount, st.Latest
		return err
	})
	if err != nil {
		return nil, err
	}
	return shopordersapi.GetShopOrdersState200JSONResponse(out), nil
}

func orderView(r store.ShopOrdersPageRow) shopordersapi.ShopOrder {
	return shopordersapi.ShopOrder{
		Id: r.ID, Status: shopordersapi.ShopOrderStatus(r.Status), ComputerName: r.ComputerName, CustomerName: r.CustomerName,
		Items: linesView(r.Items), AmountTiyn: r.Amount, CreatedAt: r.CreatedAt, HandlerName: r.HandlerName, HandledAt: r.HandledAt,
	}
}

// handle — выдача/отмена и заказ после неё.
func (h *handlers) handle(ctx context.Context, q *store.Queries, orderID int64, fn func(context.Context, *store.Queries, shop.HandleParams) (shop.Handled, error)) (shopordersapi.ShopOrder, error) {
	clubID := httpx.ClubID(ctx)
	if _, err := fn(ctx, q, shop.HandleParams{ClubID: clubID, OrderID: orderID, Actor: httpx.Actor(ctx), Now: h.d.NowFunc()}); err != nil {
		return shopordersapi.ShopOrder{}, err
	}
	r, err := q.ShopGetOrder(ctx, store.ShopGetOrderParams{ClubID: clubID, ID: orderID})
	if err != nil {
		return shopordersapi.ShopOrder{}, err
	}
	return orderView(store.ShopOrdersPageRow(r)), nil
}

// MarkShopOrderDone — done(): AgentShop::handOver().
func (h *handlers) MarkShopOrderDone(ctx context.Context, req shopordersapi.MarkShopOrderDoneRequestObject) (shopordersapi.MarkShopOrderDoneResponseObject, error) {
	var out shopordersapi.ShopOrder
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = h.handle(ctx, q, req.OrderId, shop.HandOver)
		return err
	})
	if err != nil {
		return nil, err
	}
	return shopordersapi.MarkShopOrderDone200JSONResponse(out), nil
}

// CancelShopOrder — cancel(): AgentShop::cancel() (деньги — на счёт клиента, товар — на склад).
func (h *handlers) CancelShopOrder(ctx context.Context, req shopordersapi.CancelShopOrderRequestObject) (shopordersapi.CancelShopOrderResponseObject, error) {
	clubID := httpx.ClubID(ctx)
	var out shopordersapi.ShopOrder
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = httpx.Idempotent(ctx, q, clubID, req.Params.IdempotencyKey, "shop_order_cancel", map[string]int64{"order_id": req.OrderId}, h.d.NowFunc(),
			func() (shopordersapi.ShopOrder, error) { return h.handle(ctx, q, req.OrderId, shop.Cancel) })
		return err
	})
	if err != nil {
		return nil, err
	}
	return shopordersapi.CancelShopOrder200JSONResponse(out), nil
}
