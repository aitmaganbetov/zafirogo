package stock

// Склад поверх БД: те же транзакции, что AdjustStockAction / CreateProductAction / UpdateProductAction старого кода
// (товар FOR UPDATE → проверка → products.quantity → stock_movements). Вызывать внутри db.InTenantTx клуба.
// Журнал интерфейса (activity_logs: stock.received, stock.written_off, product.created/updated) и вебхуки
// (stock.changed, product.created/updated) — у вызывающего: результаты содержат всё, что для них нужно.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ErrProductNotFound — товара нет в клубе или он удалён (findOrFail → 404 в старом коде).
var ErrProductNotFound = errors.New("stock: product not found")

// LockProduct — Product::query()->lockForUpdate()->findOrFail($id): товар клуба (не удалённый) под блокировкой строки.
func LockProduct(ctx context.Context, q *store.Queries, clubID, productID int64) (store.Product, error) {
	p, err := q.LockProduct(ctx, store.LockProductParams{ClubID: clubID, ID: productID})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Product{}, ErrProductNotFound
	}
	if err != nil {
		return store.Product{}, fmt.Errorf("stock: lock product: %w", err)
	}
	return p, nil
}

// ApplyMovement записывает движение, уже рассчитанное под блокировкой товара (Adjust, Initial или списание продажи):
// products.quantity = BalanceAfter и строка stock_movements {type, quantity, balance_after, note}.
// userID 0 — NULL. Пустой note — NULL (как 'note' => $data['note'] ?? null).
func ApplyMovement(ctx context.Context, q *store.Queries, clubID, userID int64, m Movement, now time.Time) (store.StockMovement, error) {
	if m.BalanceAfter < 0 || m.Quantity < 0 {
		// В MySQL — unsigned, в Postgres — CHECK; сюда не доходит, если движение рассчитано Adjust/Initial.
		return store.StockMovement{}, &Error{Field: "quantity", Message: MsgWriteOffTooMuch}
	}
	if err := q.SetProductQuantity(ctx, store.SetProductQuantityParams{
		ClubID: clubID, ID: m.ProductID, Quantity: m.BalanceAfter, Now: &now,
	}); err != nil {
		return store.StockMovement{}, fmt.Errorf("stock: set quantity: %w", err)
	}
	row, err := q.InsertStockMovement(ctx, store.InsertStockMovementParams{
		ClubID: clubID, ProductID: m.ProductID, UserID: nonZero(userID), Type: string(m.Type),
		Quantity: m.Quantity, BalanceAfter: m.BalanceAfter, Note: nonEmpty(m.Note), Now: &now,
	})
	if err != nil {
		return store.StockMovement{}, fmt.Errorf("stock: insert movement: %w", err)
	}
	return row, nil
}

// AdjustParams — приход или списание товара (форма склада, «Приход товара» на кассе, API сотрудника и API v1).
type AdjustParams struct {
	ClubID    int64
	ProductID int64
	UserID    int64
	Type      MovementType
	Quantity  int64
	// MaxQuantity — верхняя граница формы: MaxReceiveQuantity для «Прихода товара», API сотрудника и API v1;
	// 0 — без границы (StoreStockMovementRequest).
	MaxQuantity int64
	Note        string
	Now         time.Time
}

// AdjustResult — движение и товар после него.
type AdjustResult struct {
	Movement Movement
	Row      store.StockMovement
	Product  store.Product // с новым остатком
}

// AdjustStock — приход или списание товара (BR-070).
//
// Laravel: app/Actions/AdjustStockAction.php handle() (~стр. 16–66): товар FOR UPDATE (findOrFail);
// списание больше остатка → «Нельзя списать больше, чем есть на складе.» (поле quantity); quantity ±; stock_movements.
// Остаток не уходит в минус: проверка здесь + CHECK (quantity >= 0) в БД.
func AdjustStock(ctx context.Context, q *store.Queries, p AdjustParams) (AdjustResult, error) {
	product, err := LockProduct(ctx, q, p.ClubID, p.ProductID)
	if err != nil {
		return AdjustResult{}, err
	}
	m, err := Adjust(product.ID, product.Quantity, p.Type, p.Quantity, p.MaxQuantity, p.Note)
	if err != nil {
		return AdjustResult{}, err
	}
	row, err := ApplyMovement(ctx, q, p.ClubID, p.UserID, m, nowOr(p.Now))
	if err != nil {
		return AdjustResult{}, err
	}
	product.Quantity = m.BalanceAfter
	return AdjustResult{Movement: m, Row: row, Product: product}, nil
}

// CreateProductParams — новый товар (форма склада, API v1).
type CreateProductParams struct {
	ClubID     int64
	UserID     int64
	SKU        string
	Name       string
	CategoryID int64
	Price      money.Tiyn // ParsePrice
	Quantity   int64
	Now        time.Time
}

// CreateProductResult — созданный товар и движение начального остатка (nil — остаток 0, движения нет).
type CreateProductResult struct {
	Product  store.Product
	Initial  *Movement
	Movement *store.StockMovement
}

// CreateProduct — товар с начальным остатком (BR-071).
//
// Laravel: app/Actions/CreateProductAction.php handle(): категория findOrFail (в форме — exists, поле product_category_id);
// товар с category = имя категории и quantity; при quantity > 0 — движение receipt «Начальный остаток при создании товара».
// SKU уникален в клубе (StoreProductRequest: unique) — здесь ошибка поля sku по уникальному индексу.
func CreateProduct(ctx context.Context, q *store.Queries, p CreateProductParams) (CreateProductResult, error) {
	if p.Price < 0 || p.Price > MaxPrice {
		return CreateProductResult{}, &Error{Field: "price", Rule: "max"}
	}
	if _, _, err := Initial(0, p.Quantity); err != nil {
		return CreateProductResult{}, err
	}
	category, err := q.GetProductCategory(ctx, store.GetProductCategoryParams{ClubID: p.ClubID, ID: p.CategoryID})
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateProductResult{}, &Error{Field: "product_category_id", Rule: "exists"}
	}
	if err != nil {
		return CreateProductResult{}, fmt.Errorf("stock: category: %w", err)
	}
	now := nowOr(p.Now)
	product, err := q.InsertProduct(ctx, store.InsertProductParams{
		ClubID: p.ClubID, Sku: p.SKU, Name: p.Name, Category: category.Name, ProductCategoryID: &category.ID,
		Price: int64(p.Price), Quantity: p.Quantity, Now: &now,
	})
	if err != nil {
		return CreateProductResult{}, mapProductError(err)
	}
	out := CreateProductResult{Product: product}
	m, ok, err := Initial(product.ID, p.Quantity)
	if err != nil || !ok {
		return out, err
	}
	row, err := q.InsertStockMovement(ctx, store.InsertStockMovementParams{
		ClubID: p.ClubID, ProductID: product.ID, UserID: nonZero(p.UserID), Type: string(m.Type),
		Quantity: m.Quantity, BalanceAfter: m.BalanceAfter, Note: nonEmpty(m.Note), Now: &now,
	})
	if err != nil {
		return CreateProductResult{}, fmt.Errorf("stock: insert movement: %w", err)
	}
	out.Initial, out.Movement = &m, &row
	return out, nil
}

// UpdateProductParams — правка товара; nil — поле не передано (API v1 PATCH), иначе — новое значение.
type UpdateProductParams struct {
	ClubID     int64
	ProductID  int64
	UserID     int64
	SKU        *string
	Name       *string
	CategoryID *int64
	Price      *money.Tiyn
	Now        time.Time
}

// UpdateProductResult — товар после правки и изменения «было → стало» (пусто — ничего не записано).
type UpdateProductResult struct {
	Product store.Product
	Changes []Change
}

// UpdateProduct — правка sku, имени, категории и цены товара (BR-072). Остаток — только движениями.
//
// Laravel: app/Actions/UpdateProductAction.php handle(): товар FOR UPDATE (findOrFail); категория findOrFail → category;
// fill; getDirty() без updated_at; нет изменений — ничего не пишется; иначе save + журнал product.updated.
func UpdateProduct(ctx context.Context, q *store.Queries, p UpdateProductParams) (UpdateProductResult, error) {
	product, err := LockProduct(ctx, q, p.ClubID, p.ProductID)
	if err != nil {
		return UpdateProductResult{}, err
	}
	old := fieldsOf(product)
	upd := old
	if p.SKU != nil {
		upd.SKU = *p.SKU
	}
	if p.Name != nil {
		upd.Name = *p.Name
	}
	if p.Price != nil {
		if *p.Price < 0 || *p.Price > MaxPrice {
			return UpdateProductResult{}, &Error{Field: "price", Rule: "max"}
		}
		upd.Price = *p.Price
	}
	if p.CategoryID != nil {
		category, err := q.GetProductCategory(ctx, store.GetProductCategoryParams{ClubID: p.ClubID, ID: *p.CategoryID})
		if errors.Is(err, pgx.ErrNoRows) {
			return UpdateProductResult{}, &Error{Field: "product_category_id", Rule: "exists"}
		}
		if err != nil {
			return UpdateProductResult{}, fmt.Errorf("stock: category: %w", err)
		}
		upd.CategoryID, upd.Category = category.ID, category.Name
	}
	changes := ProductChanges(old, upd)
	if len(changes) == 0 {
		return UpdateProductResult{Product: product}, nil
	}
	now := nowOr(p.Now)
	var categoryID *int64
	if upd.CategoryID != 0 {
		categoryID = &upd.CategoryID
	}
	if err := q.UpdateProductFields(ctx, store.UpdateProductFieldsParams{
		ClubID: p.ClubID, ID: product.ID, Sku: upd.SKU, Name: upd.Name, Category: upd.Category,
		ProductCategoryID: categoryID, Price: int64(upd.Price), Now: &now,
	}); err != nil {
		return UpdateProductResult{}, mapProductError(err)
	}
	product.Sku, product.Name, product.Category, product.ProductCategoryID, product.Price =
		upd.SKU, upd.Name, upd.Category, categoryID, int64(upd.Price)
	product.UpdatedAt = &now
	return UpdateProductResult{Product: product, Changes: changes}, nil
}

func fieldsOf(p store.Product) ProductFields {
	f := ProductFields{SKU: p.Sku, Name: p.Name, Category: p.Category, Price: money.Tiyn(p.Price)}
	if p.ProductCategoryID != nil {
		f.CategoryID = *p.ProductCategoryID
	}
	return f
}

// mapProductError — уникальный SKU в клубе → ошибка поля sku (правило unique формы).
func mapProductError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "products_club_id_sku_unique" {
		return &Error{Field: "sku", Rule: "unique"}
	}
	return fmt.Errorf("stock: product: %w", err)
}

func nowOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

func nonZero(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
