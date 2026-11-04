-- Каркас панели клуба (Этап 3, F1): бейджи меню. Laravel: partials/sidebar.blade.php.

-- name: LayoutBadges :one
-- ChatController::unreadTotal() (сообщения клиентов без read_at) и ShopOrder::newCount() (заказы status = new).
-- Индексы chat_messages (club_id, from_staff, read_at) и shop_orders (club_id, status, id).
SELECT
    (SELECT count(*) FROM chat_messages m WHERE m.club_id = sqlc.arg(club_id) AND NOT m.from_staff AND m.read_at IS NULL)::bigint AS chat_unread,
    (SELECT count(*) FROM shop_orders o WHERE o.club_id = sqlc.arg(club_id) AND o.status = 'new')::bigint AS orders_new;
