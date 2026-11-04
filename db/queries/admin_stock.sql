-- Склад для REST админки (Этап 2, B1). Laravel: app/Http/Controllers/WarehouseController.php.

-- name: AdminListProducts :many
-- Товары клуба (без удалённых), по имени; keyset по (name, id): after_name/after_id — последняя строка
-- предыдущей страницы (первая — пустое имя и 0). Поиск — подстрока имени или SKU без учёта регистра
-- (search — уже экранированный шаблон ILIKE, пусто — без поиска).
SELECT id, sku, name, category, product_category_id, price, quantity, updated_at
FROM products
WHERE club_id = sqlc.arg(club_id)
  AND deleted_at IS NULL
  AND (sqlc.arg(search)::text = '' OR name ILIKE sqlc.arg(search)::text OR sku ILIKE sqlc.arg(search)::text)
  AND (name, id) > (sqlc.arg(after_name)::text, sqlc.arg(after_id)::bigint)
ORDER BY name, id
LIMIT sqlc.arg(page_size);

-- name: AdminGetProduct :one
SELECT id, sku, name, category, product_category_id, price, quantity, updated_at
FROM products
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: AdminListProductCategories :many
-- Категории склада (форма товара), по имени.
SELECT id, name
FROM product_categories
WHERE club_id = sqlc.arg(club_id)
ORDER BY name, id;
