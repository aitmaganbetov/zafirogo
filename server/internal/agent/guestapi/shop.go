package guestapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

type shopService struct {
	agentv1.UnimplementedShopServiceServer
	s *Server
}

// ListProducts — товары клуба по названию (GET /api/products, Api\ProductController, throttle 60/мин).
// Токен гостя не обязателен (витрина на экране входа); с токеном лимит считается на гостя, без — на ПК.
func (ss *shopService) ListProducts(ctx context.Context, req *agentv1.ListProductsRequest) (*agentv1.ListProductsResponse, error) {
	s := ss.s
	id, err := pc(ctx)
	if err != nil {
		return nil, err
	}
	ctx = withRequestMeta(ctx)
	if bearer(ctx) != "" {
		if ctx, id, _, err = s.guestCall(ctx, "api.products.index"); err != nil {
			return nil, err
		}
	} else if ctx, id, err = s.call(ctx, "api.products.index", nil); err != nil {
		return nil, err
	}
	if req.GetPageSize() < 0 || req.GetPageSize() > shop.MaxPageSize {
		return nil, validation("page_size", "Размер страницы — от 1 до 100.")
	}
	page, err := s.o.Shop.ListProducts(ctx, id.ClubID, int(req.GetPageSize()), req.GetPageToken())
	if errors.Is(err, shop.ErrInvalidInput) {
		return nil, validation("page_token", "Неверный курсор страницы.")
	}
	if err != nil {
		return nil, s.fail(ctx, "ListProducts", err)
	}
	out := &agentv1.ListProductsResponse{NextPageToken: page.Next, Products: make([]*agentv1.Product, 0, len(page.Products))}
	for _, p := range page.Products {
		// ProductResource: currency всегда 'KZT', in_stock = quantity > 0; фото — колонки image_path в схеме нет.
		out.Products = append(out.Products, &agentv1.Product{ProductId: p.ID, Sku: p.SKU, Name: p.Name, Category: p.Category,
			PriceTiyn: int64(p.Price), Currency: "KZT", Quantity: int32(min(p.Quantity, 1<<31-1)), InStock: p.Quantity > 0}) // #nosec G115 -- ограничено
	}
	return out, nil
}

// PlaceOrder — заказ к этому ПК с основного баланса (POST /api/guest/shop/purchase, GuestShopController →
// AgentShop::purchase(), BR-075).
func (ss *shopService) PlaceOrder(ctx context.Context, req *agentv1.PlaceOrderRequest) (*agentv1.PlaceOrderResponse, error) {
	s := ss.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.shop.purchase")
	if err != nil {
		return nil, err
	}
	return once(ctx, s.idem, id.ComputerID, "PlaceOrder", req.GetIdempotencyKey(), req, &agentv1.PlaceOrderResponse{},
		func() (*agentv1.PlaceOrderResponse, error) {
			// GuestFeatures::shopEnabled() → 403 shop_disabled.
			var enabled bool
			var currency string
			err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
				ci, err := loadClub(ctx, q, id)
				enabled, currency = ci.settings.shopEnabled(), ci.settings.currency
				return err
			})
			if err != nil {
				return nil, s.fail(ctx, "PlaceOrder", err)
			}
			if !enabled {
				return nil, rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_FEATURE_DISABLED, shop.MsgShopDisabled, nil, "")
			}
			// items required|array|min:1|max:30; items.*.id integer|min:1; items.*.quantity integer|min:1|max:50.
			items := req.GetItems()
			if len(items) == 0 || len(items) > shop.MaxLines {
				return nil, validation("items", "В заказе — от 1 до 30 строк.")
			}
			quantities := map[int64]int64{}
			for _, it := range items {
				if it.GetProductId() < 1 {
					return nil, validation("items", shop.MsgProductGone)
				}
				if it.GetQuantity() < 1 || it.GetQuantity() > shop.MaxQuantity {
					return nil, validation("items", "Количество — от 1 до 50.")
				}
				quantities[it.GetProductId()] += int64(it.GetQuantity()) // одинаковые id складываются
			}
			order, err := s.o.Shop.PlaceOrder(ctx, shop.OrderRequest{
				ClubID: id.ClubID, UserID: g.UserID, ComputerID: id.ComputerID, Quantities: quantities,
				IdempotencyKey: serviceKey(id.ComputerID, req.GetIdempotencyKey()), Actor: actor(ctx, g),
			})
			if err != nil {
				return nil, s.fail(ctx, "PlaceOrder", err)
			}
			out := &agentv1.PlaceOrderResponse{
				OrderId: order.OrderID, AmountTiyn: int64(order.Amount), Status: agentv1.OrderStatus_ORDER_STATUS_NEW,
				CreatedAt: timestamppb.New(order.CreatedAt),
				Balance:   &agentv1.Balance{MainTiyn: int64(order.MainBalance), BonusTiyn: int64(order.BonusBalance), Currency: currency},
			}
			for _, l := range order.Lines {
				out.Lines = append(out.Lines, &agentv1.OrderLine{ProductId: l.ProductID, Name: l.Name,
					Quantity: int32(l.Quantity), PriceTiyn: int64(l.Price), TotalTiyn: int64(l.Total)}) // #nosec G115 -- ≤ 1500
			}
			if !order.Replayed && order.Amount > 0 {
				s.notifyBalance(ctx, id.ClubID, g.UserID, out.GetBalance())
			}
			return out, nil
		})
}

// notifyBalance — BalanceChanged на ПК, где вошёл гость (после коммита). Ошибка — в лог.
func (s *Server) notifyBalance(ctx context.Context, clubID, userID int64, b *agentv1.Balance) {
	if s.o.Notifier == nil {
		return
	}
	if err := s.o.Notifier.BalanceChanged(context.WithoutCancel(ctx), clubID, userID, b); err != nil {
		s.o.Log.WarnContext(ctx, "balance changed not sent", "club_id", clubID, "user_id", userID, "err", err)
	}
}
