package warehouse

// Склад: товары (поиск, keyset), создание и правка, приход/списание, категории.
// Laravel: WarehouseController, ProductController + CreateProductAction, UpdateProductAction,
// StockMovementController + AdjustStockAction. Журнал (activity_logs) и вебхуки (outbox) пишутся в транзакции
// действия, как ActivityLogger::record() / WebhookDispatcher::dispatch() внутри DB::transaction() старых Actions.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	warehouseapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/warehouse"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Ограничения формы товара (StoreProductRequest).
const (
	maxSKU             = 100
	maxProductName     = 150
	maxProductQuantity = 1_000_000
	maxNote            = 255
)

func productView(p store.Product) warehouseapi.Product {
	return warehouseapi.Product{
		Id: p.ID, Sku: p.Sku, Name: p.Name, Category: p.Category, ProductCategoryId: p.ProductCategoryID,
		PriceTiyn: p.Price, Quantity: p.Quantity,
	}
}

type productCursor struct {
	Name string `json:"n"`
	ID   int64  `json:"i"`
}

// ListProducts — товары склада по имени (keyset), поиск по имени и SKU.
func (h *handlers) ListProducts(ctx context.Context, req warehouseapi.ListProductsRequestObject) (warehouseapi.ListProductsResponseObject, error) {
	var cur productCursor
	if err := httpx.DecodeCursor(req.Params.Cursor, &cur); err != nil {
		return nil, err
	}
	size := httpx.PageSize(req.Params.Limit)
	out := warehouseapi.ProductPage{Items: []warehouseapi.Product{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := q.AdminListProducts(ctx, store.AdminListProductsParams{
			ClubID: httpx.ClubID(ctx), Search: httpx.LikePattern(req.Params.Q), AfterName: cur.Name, AfterID: cur.ID, PageSize: size,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out.Items = append(out.Items, warehouseapi.Product{
				Id: r.ID, Sku: r.Sku, Name: r.Name, Category: r.Category, ProductCategoryId: r.ProductCategoryID,
				PriceTiyn: r.Price, Quantity: r.Quantity,
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
	return warehouseapi.ListProducts200JSONResponse(out), nil
}

// ListProductCategories — категории склада для формы товара.
func (h *handlers) ListProductCategories(ctx context.Context, _ warehouseapi.ListProductCategoriesRequestObject) (warehouseapi.ListProductCategoriesResponseObject, error) {
	out := []warehouseapi.ProductCategory{}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := q.AdminListProductCategories(ctx, httpx.ClubID(ctx))
		for _, r := range rows {
			out = append(out, warehouseapi.ProductCategory{Id: r.ID, Name: r.Name})
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return warehouseapi.ListProductCategories200JSONResponse(out), nil
}

// validateText — required|string|max:n.
func validateText(field, v string, maxLen int) error {
	if strings.TrimSpace(v) == "" {
		return httpx.RuleError(field, cash.RuleRequired)
	}
	if utf8.RuneCountInString(v) > maxLen {
		return httpx.RuleError(field, cash.RuleMax)
	}
	return nil
}

// stockChanged — вебхук stock.changed (тип-вебхук: данные как в старом коде).
func stockChanged(ctx context.Context, q *store.Queries, clubID int64, p store.Product, change, quantity int64, reason string) error {
	return billingsvc.AddEvent(ctx, q, clubID, "stock.changed", map[string]any{
		"product_id": p.ID, "sku": p.Sku, "change": change, "quantity": quantity, "reason": reason,
	})
}

// CreateProduct — ProductController / CreateProductAction::handle().
func (h *handlers) CreateProduct(ctx context.Context, req warehouseapi.CreateProductRequestObject) (warehouseapi.CreateProductResponseObject, error) {
	b := req.Body
	if err := validateText("sku", b.Sku, maxSKU); err != nil {
		return nil, err
	}
	if err := validateText("name", b.Name, maxProductName); err != nil {
		return nil, err
	}
	switch {
	case b.PriceTiyn < 0:
		return nil, httpx.RuleError("price", cash.RuleMin)
	case money.Tiyn(b.PriceTiyn) > stock.MaxPrice:
		return nil, httpx.RuleError("price", cash.RuleMax)
	case b.Quantity < 0:
		return nil, httpx.RuleErrorWith("quantity", cash.RuleMin, "0")
	case b.Quantity > maxProductQuantity:
		return nil, httpx.RuleError("quantity", cash.RuleMax)
	}
	var out warehouseapi.Product
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID, userID := httpx.ClubID(ctx), httpx.Principal(ctx).UserID
		res, err := stock.CreateProduct(ctx, q, stock.CreateProductParams{
			ClubID: clubID, UserID: userID, SKU: b.Sku, Name: b.Name, CategoryID: b.ProductCategoryId,
			Price: money.Tiyn(b.PriceTiyn), Quantity: b.Quantity, Now: h.d.NowFunc(),
		})
		if err != nil {
			return err
		}
		p := res.Product
		if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, Event: "product.created", Description: "Создан товар: " + p.Name, UserID: userID,
			SubjectType: billingsvc.SubjectProduct, SubjectID: p.ID, Details: map[string]any{"sku": p.Sku, "quantity": b.Quantity},
		}); err != nil {
			return err
		}
		if res.Initial != nil {
			if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
				ClubID: clubID, Event: "stock.received", UserID: userID, SubjectType: billingsvc.SubjectProduct, SubjectID: p.ID,
				Description: "Начальное оприходование: " + p.Name + ", " + strconv.FormatInt(b.Quantity, 10) + " шт.",
				Details:     map[string]any{"quantity": b.Quantity, "balance_after": b.Quantity, "note": stock.NoteInitialLog},
			}); err != nil {
				return err
			}
			if err := stockChanged(ctx, q, clubID, p, b.Quantity, b.Quantity, "initial"); err != nil {
				return err
			}
		}
		// (string) $product->price — decimal:2 строкой.
		if err := billingsvc.AddEvent(ctx, q, clubID, "product.created", map[string]any{
			"product_id": p.ID, "sku": p.Sku, "name": p.Name, "price": money.Tiyn(p.Price).Tenge(), "quantity": b.Quantity,
		}); err != nil {
			return err
		}
		out = productView(p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return warehouseapi.CreateProduct201JSONResponse(out), nil
}

// UpdateProduct — UpdateProductAction::handle(): журнал и вебхук только при изменениях.
func (h *handlers) UpdateProduct(ctx context.Context, req warehouseapi.UpdateProductRequestObject) (warehouseapi.UpdateProductResponseObject, error) {
	b := req.Body
	if b.Sku != nil {
		if err := validateText("sku", *b.Sku, maxSKU); err != nil {
			return nil, err
		}
	}
	if b.Name != nil {
		if err := validateText("name", *b.Name, maxProductName); err != nil {
			return nil, err
		}
	}
	var price *money.Tiyn
	if b.PriceTiyn != nil {
		if *b.PriceTiyn < 0 {
			return nil, httpx.RuleError("price", cash.RuleMin)
		}
		price = httpx.Ptr(money.Tiyn(*b.PriceTiyn))
	}
	var out warehouseapi.Product
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID, userID := httpx.ClubID(ctx), httpx.Principal(ctx).UserID
		res, err := stock.UpdateProduct(ctx, q, stock.UpdateProductParams{
			ClubID: clubID, ProductID: req.ProductId, UserID: userID, SKU: b.Sku, Name: b.Name,
			CategoryID: b.ProductCategoryId, Price: price, Now: h.d.NowFunc(),
		})
		if errors.Is(err, stock.ErrProductNotFound) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		out = productView(res.Product)
		if len(res.Changes) == 0 {
			return nil
		}
		p := res.Product
		details := make(map[string]any, len(res.Changes))
		changed := make([]string, 0, len(res.Changes))
		for _, c := range res.Changes {
			details[c.Field] = c.String()
			changed = append(changed, c.Field)
		}
		if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, Event: "product.updated", Description: "Изменён товар: " + p.Name, UserID: userID,
			SubjectType: billingsvc.SubjectProduct, SubjectID: p.ID, Details: details,
		}); err != nil {
			return err
		}
		return billingsvc.AddEvent(ctx, q, clubID, "product.updated", map[string]any{
			"product_id": p.ID, "sku": p.Sku, "name": p.Name, "price": money.Tiyn(p.Price).Tenge(), "changed": changed,
		})
	})
	if err != nil {
		return nil, err
	}
	return warehouseapi.UpdateProduct200JSONResponse(out), nil
}

// CreateStockMovement — StockMovementController / AdjustStockAction::handle().
func (h *handlers) CreateStockMovement(ctx context.Context, req warehouseapi.CreateStockMovementRequestObject) (warehouseapi.CreateStockMovementResponseObject, error) {
	b := req.Body
	typ := stock.MovementType(b.Type)
	switch {
	case typ != stock.Receipt && typ != stock.WriteOff:
		return nil, httpx.RuleError("type", cash.RuleIn)
	case b.Quantity < 1:
		return nil, httpx.RuleError("quantity", cash.RuleMin)
	case b.Note != nil && utf8.RuneCountInString(*b.Note) > maxNote:
		return nil, httpx.RuleError("note", cash.RuleMax)
	}
	note := ""
	if b.Note != nil {
		note = *b.Note
	}
	var out warehouseapi.StockMovementResult
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID, userID := httpx.ClubID(ctx), httpx.Principal(ctx).UserID
		res, err := stock.AdjustStock(ctx, q, stock.AdjustParams{
			ClubID: clubID, ProductID: b.ProductId, UserID: userID, Type: typ, Quantity: b.Quantity, Note: note, Now: h.d.NowFunc(),
		})
		if errors.Is(err, stock.ErrProductNotFound) {
			return httpx.RuleError("product_id", "exists") // Rule::exists формы
		}
		if err != nil {
			return err
		}
		p := res.Product
		qty := b.Quantity
		e := activity.Entry{
			ClubID: clubID, UserID: userID, SubjectType: billingsvc.SubjectProduct, SubjectID: p.ID,
			Event: "stock.received", Description: "Оприходование: " + p.Name + ", " + strconv.FormatInt(qty, 10) + " шт.",
			Details: map[string]any{"quantity": qty, "balance_after": p.Quantity, "note": stock.NoNote},
		}
		change := qty
		if typ == stock.WriteOff {
			e.Event, e.Description = "stock.written_off", "Списание: "+p.Name+", "+strconv.FormatInt(qty, 10)+" шт."
			change = -qty
		}
		if res.Row.Note != nil && *res.Row.Note != "" {
			e.Details["note"] = *res.Row.Note
		}
		if err := billingsvc.RecordActivity(ctx, q, e); err != nil {
			return err
		}
		if err := stockChanged(ctx, q, clubID, p, change, p.Quantity, string(typ)); err != nil {
			return err
		}
		out = warehouseapi.StockMovementResult{
			Id: res.Row.ID, Product: productView(p), Type: warehouseapi.StockMovementResultType(typ), Quantity: qty, BalanceAfter: p.Quantity,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return warehouseapi.CreateStockMovement201JSONResponse(out), nil
}
