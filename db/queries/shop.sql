-- Магазин клуба в агенте ПК (server/internal/shop, BR-075): витрина и заказ к ПК с оплатой с основного баланса.

-- name: ShopListProducts :many
-- Api\ProductController: Product::query()->orderBy('name')->paginate(50) — без удалённых (SoftDeletes).
-- Keyset по (name, id) вместо номера страницы: after_name/after_id — последняя строка предыдущей страницы
-- (первая страница — after_id = 0 и пустое имя). Индекс products_club_id_name_index.
SELECT id, sku, name, category, price, quantity
FROM products
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL
  AND (name, id) > (sqlc.arg(after_name)::text, sqlc.arg(after_id)::bigint)
ORDER BY name, id
LIMIT sqlc.arg(page_size);

-- name: ShopLockProducts :many
-- AgentShop::purchase(): Product::query()->whereKey($ids)->orderBy('id')->lockForUpdate() — товары по id,
-- чтобы два заказа не взаимоблокировались; удалённые не продаются.
SELECT id, sku, name, price, quantity
FROM products
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]) AND deleted_at IS NULL
ORDER BY id
FOR UPDATE;

-- name: ShopInsertOrder :one
-- ShopOrder::create([... 'status' => ShopOrder::STATUS_NEW]).
INSERT INTO shop_orders (club_id, user_id, club_computer_id, computer_name, items, amount, status, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(user_id), sqlc.arg(computer_id), sqlc.arg(computer_name), sqlc.arg(items),
        sqlc.arg(amount), 'new', sqlc.arg(now), sqlc.arg(now))
RETURNING id, created_at;

-- name: ShopComputerName :one
-- ПК заказа: ClubComputer::displayName() (display_name ?: device_name ?: PC-NN).
SELECT id, number, display_name, device_name
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ShopCountNewOrders :one
-- Новые заказы клуба (бейдж «Заказы», событие shop_order.created.new_count). Индекс shop_orders_club_id_status_id_index.
SELECT count(*)::bigint
FROM shop_orders
WHERE club_id = sqlc.arg(club_id) AND status = 'new';
