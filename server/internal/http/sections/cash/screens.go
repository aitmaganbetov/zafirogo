package cash

// Экран кассы (Этап 3, G2): витрина POS страницами, поиск клиента для пополнения (решение Q-070 — вместо «все
// товары и все клиенты в HTML» CashRegisterController::index()).

import (
	"context"

	cashapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	// catalogCategories — $categories->take(5) кнопок категорий.
	catalogCategories = 5
	// maxServices — предел активных услуг на витрине (в старом — все; клубы держат единицы).
	maxServices = 500
	// ProductPlaceholder — ProductResource::imageUrl() без фото (своих фото у товаров в схеме нет).
	ProductPlaceholder = "/images/product-placeholder.svg"
)

// GetCashCatalog — CashRegisterController::index(): «Все» (число товаров в наличии), 5 категорий, активные услуги.
func (h *handlers) GetCashCatalog(ctx context.Context, _ cashapi.GetCashCatalogRequestObject) (cashapi.GetCashCatalogResponseObject, error) {
	out := cashapi.CashCatalog{Categories: []cashapi.CashCatalogCategory{}, Services: []cashapi.CashService{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		total, err := q.CashCatalogProductCount(ctx, clubID)
		if err != nil {
			return err
		}
		out.ProductsTotal = total
		cats, err := q.CashCatalogCategories(ctx, store.CashCatalogCategoriesParams{ClubID: clubID, MaxCategories: catalogCategories})
		if err != nil {
			return err
		}
		for _, c := range cats {
			out.Categories = append(out.Categories, cashapi.CashCatalogCategory{Name: c.Category, Count: c.Products})
		}
		svc, err := q.CashActiveServices(ctx, store.CashActiveServicesParams{ClubID: clubID, MaxServices: maxServices})
		if err != nil {
			return err
		}
		for _, s := range svc {
			out.Services = append(out.Services, cashapi.CashService{Id: s.ID, Name: s.Name, PriceTiyn: s.Price, Unit: s.Unit})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cashapi.GetCashCatalog200JSONResponse(out), nil
}

type productCursor struct {
	Name string `json:"n"`
	ID   int64  `json:"i"`
}

// ListCashProducts — товары в наличии по имени (keyset), с фильтром категории.
func (h *handlers) ListCashProducts(ctx context.Context, req cashapi.ListCashProductsRequestObject) (cashapi.ListCashProductsResponseObject, error) {
	var cur productCursor
	if err := httpx.DecodeCursor(req.Params.Cursor, &cur); err != nil {
		return nil, err
	}
	category := ""
	if req.Params.Category != nil {
		category = *req.Params.Category
	}
	size := httpx.PageSize(req.Params.Limit)
	out := cashapi.CashProductPage{Items: []cashapi.CashProduct{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := q.CashListProducts(ctx, store.CashListProductsParams{
			ClubID: httpx.ClubID(ctx), Category: category, AfterName: cur.Name, AfterID: cur.ID, PageSize: size,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out.Items = append(out.Items, cashapi.CashProduct{
				Id: r.ID, Name: r.Name, Category: r.Category, PriceTiyn: r.Price, Quantity: r.Quantity, ImageUrl: ProductPlaceholder,
			})
		}
		if len(rows) == int(size) {
			last := rows[len(rows)-1]
			out.NextCursor = httpx.EncodeCursor(productCursor{Name: last.Name, ID: last.ID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cashapi.ListCashProducts200JSONResponse(out), nil
}

// SearchCashCustomers — пользователи клуба с балансами по имени, поиск по имени или телефону.
func (h *handlers) SearchCashCustomers(ctx context.Context, req cashapi.SearchCashCustomersRequestObject) (cashapi.SearchCashCustomersResponseObject, error) {
	out := []cashapi.CashCustomer{}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := q.CashSearchCustomers(ctx, store.CashSearchCustomersParams{
			ClubID: httpx.ClubID(ctx), Search: httpx.LikePattern(req.Params.Q), PageSize: httpx.PageSize(req.Params.Limit),
		})
		for _, r := range rows {
			out = append(out, cashapi.CashCustomer{Id: r.ID, Name: r.Name, Phone: r.Phone, BalanceTiyn: r.Balance, BonusBalanceTiyn: r.BonusBalance})
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return cashapi.SearchCashCustomers200JSONResponse(out), nil
}
