package warehouse

// Экраны склада (Этап 3, G2): сводка каталога, категории, удаление товара (WarehouseController,
// ProductCategoryController, ProductDeleteController) и остатки для кассира (StockController).

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	warehouseapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/warehouse"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	// perPage — WarehouseController и StockController: paginate(50).
	perPage = 50
	// maxCategoryName — StoreProductCategoryRequest: name max:100.
	maxCategoryName = 100
	// subjectCategory — морф-класс категории в журнале (PD-14).
	subjectCategory = `App\Models\ProductCategory`
	// productPlaceholder — ProductResource::imageUrl() без фото.
	productPlaceholder = "/images/product-placeholder.svg"
	// ruleUnique — Rule::unique (validation.unique) поля name.
	ruleUnique = "unique"
)

func pageOffset(page *int, size int32) int32 {
	if page == nil || *page < 1 {
		return 0
	}
	return int32(*page-1) * size // #nosec G115 -- page ≤ 100000 (схема), size ≤ 100
}

// GetWarehouseOverview — WarehouseController: категории с числом товаров, число товаров, 10 последних движений.
func (h *handlers) GetWarehouseOverview(ctx context.Context, _ warehouseapi.GetWarehouseOverviewRequestObject) (warehouseapi.GetWarehouseOverviewResponseObject, error) {
	out := warehouseapi.WarehouseOverview{Categories: []warehouseapi.WarehouseCategory{}, Movements: []warehouseapi.WarehouseMovement{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		cats, err := q.WarehouseCategoriesWithCounts(ctx, clubID)
		if err != nil {
			return err
		}
		for _, c := range cats {
			out.Categories = append(out.Categories, warehouseapi.WarehouseCategory{Id: c.ID, Name: c.Name, ProductsCount: c.Products})
		}
		if out.ProductsTotal, err = q.WarehouseCountProducts(ctx, store.WarehouseCountProductsParams{ClubID: clubID}); err != nil {
			return err
		}
		moves, err := q.WarehouseRecentMovements(ctx, clubID)
		if err != nil {
			return err
		}
		for _, m := range moves {
			out.Movements = append(out.Movements, warehouseapi.WarehouseMovement{
				Id: m.ID, ProductId: m.ProductID, ProductName: m.ProductName, Type: warehouseapi.WarehouseMovementType(m.Type),
				Quantity: m.Quantity, BalanceAfter: m.BalanceAfter, Note: m.Note, CreatedAt: m.CreatedAt,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return warehouseapi.GetWarehouseOverview200JSONResponse(out), nil
}

// listProductsPage — таблица «Склад» (paginate(50) по имени).
func (h *handlers) listProductsPage(ctx context.Context, page *int, limit *int) (warehouseapi.ProductPage, error) {
	size := int32(perPage)
	if limit != nil {
		size = httpx.PageSize(limit)
	}
	out := warehouseapi.ProductPage{Items: []warehouseapi.Product{}, PerPage: int(size)}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		total, err := q.WarehouseCountProducts(ctx, store.WarehouseCountProductsParams{ClubID: clubID})
		if err != nil {
			return err
		}
		out.Total = total
		rows, err := q.WarehouseListProductsPage(ctx, store.WarehouseListProductsPageParams{
			ClubID: clubID, PageSize: size, PageOffset: pageOffset(page, size),
		})
		for _, r := range rows {
			out.Items = append(out.Items, warehouseapi.Product{
				Id: r.ID, Sku: r.Sku, Name: r.Name, Category: r.Category, ProductCategoryId: r.ProductCategoryID,
				PriceTiyn: r.Price, Quantity: r.Quantity,
			})
		}
		return err
	})
	return out, err
}

// CreateProductCategory — ProductCategoryController (StoreProductCategoryRequest: name required|string|max:100|unique).
func (h *handlers) CreateProductCategory(ctx context.Context, req warehouseapi.CreateProductCategoryRequestObject) (warehouseapi.CreateProductCategoryResponseObject, error) {
	name := req.Body.Name
	switch {
	case strings.TrimSpace(name) == "":
		return nil, httpx.RuleError("name", cash.RuleRequired)
	case utf8.RuneCountInString(name) > maxCategoryName:
		return nil, httpx.RuleErrorWith("name", "max_string", "100")
	}
	var out warehouseapi.ProductCategory
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID, userID := httpx.ClubID(ctx), httpx.Principal(ctx).UserID
		taken, err := q.ProductCategoryNameExists(ctx, store.ProductCategoryNameExistsParams{ClubID: clubID, Name: name})
		if err != nil {
			return err
		}
		if taken {
			return httpx.RuleError("name", ruleUnique)
		}
		now := h.d.NowFunc()
		row, err := q.InsertProductCategory(ctx, store.InsertProductCategoryParams{ClubID: clubID, Name: name, Now: &now})
		if err != nil {
			return err
		}
		out = warehouseapi.ProductCategory{Id: row.ID, Name: row.Name}
		// Laravel: ProductCategoryController — журнал warehouse.category_created.
		return billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, Event: "warehouse.category_created", Description: "Создана категория склада: " + row.Name,
			UserID: userID, SubjectType: subjectCategory, SubjectID: row.ID,
		})
	})
	if err != nil {
		return nil, err
	}
	return warehouseapi.CreateProductCategory201JSONResponse(out), nil
}

// DeleteProduct — ProductDeleteController: SKU освобождается, товар скрывается (SoftDelete), журнал product.deleted.
func (h *handlers) DeleteProduct(ctx context.Context, req warehouseapi.DeleteProductRequestObject) (warehouseapi.DeleteProductResponseObject, error) {
	var out warehouseapi.Product
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID, userID := httpx.ClubID(ctx), httpx.Principal(ctx).UserID
		p, err := q.LockProduct(ctx, store.LockProductParams{ClubID: clubID, ID: req.ProductId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		now := h.d.NowFunc()
		if err := q.SoftDeleteProduct(ctx, store.SoftDeleteProductParams{
			ClubID: clubID, ID: p.ID, Sku: stock.DeletedSKU(p.Sku, p.ID), Now: &now,
		}); err != nil {
			return err
		}
		out = productView(p)
		return billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, Event: "product.deleted", Description: "Удалён товар: " + p.Name, UserID: userID,
			SubjectType: billingsvc.SubjectProduct, SubjectID: p.ID, Details: map[string]any{"sku": p.Sku, "quantity": p.Quantity},
		})
	})
	if err != nil {
		return nil, err
	}
	return warehouseapi.DeleteProduct200JSONResponse(out), nil
}

// ListStock — StockController::index(): поиск по имени или SKU, по категории и имени, paginate(50).
func (h *handlers) ListStock(ctx context.Context, req warehouseapi.ListStockRequestObject) (warehouseapi.ListStockResponseObject, error) {
	search := httpx.LikePattern(req.Params.Q)
	out := warehouseapi.StockPage{Items: []warehouseapi.StockProduct{}, PerPage: perPage}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		total, err := q.WarehouseCountProducts(ctx, store.WarehouseCountProductsParams{ClubID: clubID, Search: search})
		if err != nil {
			return err
		}
		out.Total = total
		rows, err := q.StockListProductsPage(ctx, store.StockListProductsPageParams{
			ClubID: clubID, Search: search, PageSize: perPage, PageOffset: pageOffset(req.Params.Page, perPage),
		})
		for _, r := range rows {
			out.Items = append(out.Items, warehouseapi.StockProduct{
				Id: r.ID, Sku: r.Sku, Name: r.Name, Category: r.Category, PriceTiyn: r.Price, Quantity: r.Quantity, ImageUrl: productPlaceholder,
			})
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return warehouseapi.ListStock200JSONResponse(out), nil
}

// ReceiveStock — StockController::receive(): product_id (товар клуба), quantity integer|min:1|max:100000,
// note nullable|max:255 → AdjustStockAction (receipt). Все ошибки полей сразу (RQ-1).
func (h *handlers) ReceiveStock(ctx context.Context, req warehouseapi.ReceiveStockRequestObject) (warehouseapi.ReceiveStockResponseObject, error) {
	b := req.Body
	var form httpx.FormErrors
	if b.Quantity < 1 {
		form.Add("quantity", httpx.RuleMessage("quantity", cash.RuleMin, ""))
	} else if b.Quantity > stock.MaxReceiveQuantity {
		form.Add("quantity", httpx.RuleMessage("quantity", cash.RuleMax, "100000"))
	}
	note := ""
	if b.Note != nil {
		note = *b.Note
	}
	if utf8.RuneCountInString(note) > maxNote {
		form.Add("note", httpx.RuleMessage("note", cash.RuleMax, ""))
	}
	if err := form.Err(); err != nil {
		return nil, err
	}
	out, err := h.adjust(ctx, b.ProductId, stock.Receipt, b.Quantity, stock.MaxReceiveQuantity, note)
	if err != nil {
		return nil, err
	}
	return warehouseapi.ReceiveStock201JSONResponse(out), nil
}
