-- Экраны склада (Этап 3, G2): каталог (WarehouseController, ProductCategoryController, ProductDeleteController) и
-- остатки для кассира (StockController). Все запросы — в транзакции клуба, с club_id.

-- name: WarehouseCategoriesWithCounts :many
-- WarehouseController: ProductCategory::withCount('products')->orderBy('name') — удалённые товары не считаются
-- (SoftDeletes у Product).
SELECT c.id, c.name, count(p.id)::bigint AS products
FROM product_categories c
LEFT JOIN products p ON p.club_id = c.club_id AND p.product_category_id = c.id AND p.deleted_at IS NULL
WHERE c.club_id = sqlc.arg(club_id)
GROUP BY c.id, c.name
ORDER BY c.name, c.id;

-- name: WarehouseCountProducts :one
-- Товаров клуба (не удалённых): «Товаров: :products» и $stock->total(); с поиском — StockController.
SELECT count(*)::bigint
FROM products
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL
  AND (sqlc.arg(search)::text = '' OR name ILIKE sqlc.arg(search)::text OR sku ILIKE sqlc.arg(search)::text);

-- name: WarehouseListProductsPage :many
-- WarehouseController: Product::select(id, sku, name, category, price, quantity)->orderBy('name')->paginate(50).
SELECT id, sku, name, category, product_category_id, price, quantity
FROM products
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL
ORDER BY name, id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: WarehouseRecentMovements :many
-- WarehouseController: StockMovement::with('product:id,name')->latest()->limit(10). Имя удалённого товара тоже
-- показывается (в старом $movement->product — null и ошибка страницы). Индекс stock_movements_club_id_created_at_index.
SELECT m.id, m.product_id, p.name AS product_name, m.type, m.quantity, m.balance_after, m.note, m.created_at
FROM stock_movements m
JOIN products p ON p.club_id = m.club_id AND p.id = m.product_id
WHERE m.club_id = sqlc.arg(club_id)
ORDER BY m.created_at DESC NULLS LAST, m.id DESC
LIMIT 10;

-- name: InsertProductCategory :one
-- ProductCategoryController: ProductCategory::create(['name']). Уникальность имени в клубе — индекс
-- product_categories_club_id_name_unique (Rule::unique).
INSERT INTO product_categories (club_id, name, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(name), sqlc.arg(now), sqlc.arg(now))
RETURNING id, name;

-- name: ProductCategoryNameExists :one
-- StoreProductCategoryRequest: Rule::unique('product_categories', 'name')->where('club_id', …).
SELECT EXISTS (
    SELECT 1 FROM product_categories WHERE club_id = sqlc.arg(club_id) AND name = sqlc.arg(name)
)::boolean;

-- name: SoftDeleteProduct :exec
-- ProductDeleteController: sku → '<sku>~deleted-<id>' (освобождает SKU), затем SoftDelete.
UPDATE products
SET sku = sqlc.arg(sku), deleted_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: StockListProductsPage :many
-- StockController::index(): поиск по имени или SKU (LIKE, экранированный шаблон), orderBy('category')->orderBy('name')
-- ->paginate(50). Индекс products_club_id_category_name_index (0016).
SELECT id, sku, name, category, price, quantity
FROM products
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL
  AND (sqlc.arg(search)::text = '' OR name ILIKE sqlc.arg(search)::text OR sku ILIKE sqlc.arg(search)::text)
ORDER BY category, name, id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);
