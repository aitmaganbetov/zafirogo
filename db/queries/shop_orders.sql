-- Заказы из агента в панели (Этап 3, G2). Laravel: app/Http/Controllers/ShopOrderController.php,
-- app/Services/AgentShop.php handOver(), cancel() (BR-076, BR-077). Все запросы — с club_id.

-- name: ShopOrdersPage :many
-- index(): ShopOrder::when(status != all, where status)->with(customer:id,name,phone; handler:id,name)
-- ->latest('id')->paginate(30). status = '' — все. Индексы shop_orders_club_id_status_id_index, shop_orders_club_id_id_index.
SELECT o.id, o.user_id, o.club_computer_id, o.computer_name, o.items, o.amount, o.status, o.handled_by, o.handled_at,
       o.created_at, cu.name AS customer_name, h.name AS handler_name
FROM shop_orders o
LEFT JOIN users cu ON cu.id = o.user_id
LEFT JOIN users h ON h.id = o.handled_by
WHERE o.club_id = sqlc.arg(club_id)
  AND (sqlc.arg(status)::text = '' OR o.status = sqlc.arg(status)::text)
ORDER BY o.id DESC
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: ShopOrdersCount :one
SELECT count(*)::bigint
FROM shop_orders
WHERE club_id = sqlc.arg(club_id) AND (sqlc.arg(status)::text = '' OR status = sqlc.arg(status)::text);

-- name: ShopOrdersState :one
-- state(): ShopOrder::newCount() и max(id) — бейдж и «появился новый заказ».
SELECT count(*) FILTER (WHERE status = 'new')::bigint AS new_count, COALESCE(max(id), 0)::bigint AS latest
FROM shop_orders
WHERE club_id = sqlc.arg(club_id);

-- name: ShopLockOrder :one
-- lockNew(): ShopOrder::lockForUpdate()->findOrFail($id).
SELECT id, user_id, club_computer_id, computer_name, items, amount, status
FROM shop_orders
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ShopSetOrderHandled :exec
-- handOver()/cancel(): status, handled_by, handled_at.
UPDATE shop_orders
SET status = sqlc.arg(status), handled_by = sqlc.narg(handled_by), handled_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ShopLockOrderProducts :many
-- cancel(): Product::whereKey($ids)->orderBy('id')->lockForUpdate() — существующие (не удалённые) товары заказа.
SELECT id, sku, quantity
FROM products
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]) AND deleted_at IS NULL
ORDER BY id
FOR UPDATE;

-- name: ShopGetOrder :one
-- Заказ с именами клиента и выдавшего (ответ после выдачи/отмены).
SELECT o.id, o.user_id, o.club_computer_id, o.computer_name, o.items, o.amount, o.status, o.handled_by, o.handled_at,
       o.created_at, cu.name AS customer_name, h.name AS handler_name
FROM shop_orders o
LEFT JOIN users cu ON cu.id = o.user_id
LEFT JOIN users h ON h.id = o.handled_by
WHERE o.club_id = sqlc.arg(club_id) AND o.id = sqlc.arg(id);
