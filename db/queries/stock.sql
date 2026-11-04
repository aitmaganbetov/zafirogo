-- Склад и товары (server/internal/stock, BR-070…BR-073). Все запросы — в транзакции клуба (db.InTenantTx).
-- Остаток товара меняется только вместе со строкой stock_movements (AdjustStockAction, продажа и возврат POS).

-- name: LockProduct :one
-- Product::query()->lockForUpdate()->findOrFail($id): товар клуба, не удалённый (SoftDeletes).
SELECT *
FROM products
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
FOR UPDATE;

-- name: LockProductByName :one
-- Возврат старого чека без product_id: Product::query()->lockForUpdate()->where('name', …)->first()
-- (порядок в старом коде не задан — берём наименьший id, CQ-9).
SELECT *
FROM products
WHERE club_id = sqlc.arg(club_id) AND name = sqlc.arg(name) AND deleted_at IS NULL
ORDER BY id
LIMIT 1
FOR UPDATE;

-- name: GetProduct :one
SELECT *
FROM products
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: SetProductQuantity :exec
-- Новый остаток после движения (вычислен под блокировкой строки). CHECK (quantity >= 0) — последний рубеж.
UPDATE products
SET quantity = sqlc.arg(quantity), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: InsertStockMovement :one
INSERT INTO stock_movements (club_id, product_id, user_id, type, quantity, balance_after, note, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(product_id), sqlc.narg(user_id), sqlc.arg(type), sqlc.arg(quantity),
        sqlc.arg(balance_after), sqlc.narg(note), sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: ListProductStockMovements :many
-- Движения товара, новые сверху (keyset по id: before_id — последний id предыдущей страницы).
SELECT *
FROM stock_movements
WHERE club_id = sqlc.arg(club_id) AND product_id = sqlc.arg(product_id) AND id < sqlc.arg(before_id)
ORDER BY id DESC
LIMIT sqlc.arg(page_size);

-- name: GetProductCategory :one
-- ProductCategory::query()->findOrFail($id) (клуб — global scope).
SELECT id, name
FROM product_categories
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: InsertProduct :one
-- CreateProductAction::handle(): товар с начальным остатком; SKU уникален в клубе (products_club_id_sku_unique).
INSERT INTO products (club_id, sku, name, category, product_category_id, price, quantity, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(sku), sqlc.arg(name), sqlc.arg(category), sqlc.arg(product_category_id),
        sqlc.arg(price), sqlc.arg(quantity), sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: UpdateProductFields :exec
-- UpdateProductAction::handle(): sku, name, категория, цена (остаток — только движениями).
UPDATE products
SET sku = sqlc.arg(sku), name = sqlc.arg(name), category = sqlc.arg(category),
    product_category_id = sqlc.arg(product_category_id), price = sqlc.arg(price), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ListActiveClubServicesByIDs :many
-- Услуги продажи POS: ClubService::query()->where('is_active', true)->findOrFail($id).
SELECT id, name, price
FROM club_services
WHERE club_id = sqlc.arg(club_id) AND is_active AND id = ANY(sqlc.arg(ids)::bigint[]);
