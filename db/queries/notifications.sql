-- Колокольчик уведомлений панели клуба (Этап 3, группа 4). Laravel: app/Services/ClubNotifications.php for(),
-- app/Http/Controllers/NotificationController.php. Уведомления выводятся из существующих данных; «непрочитано» —
-- после club_memberships.notifications_seen_at.

-- name: NotificationsSeenAt :one
SELECT notifications_seen_at FROM club_memberships WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);

-- name: MarkNotificationsSeen :exec
-- markSeen().
UPDATE club_memberships SET notifications_seen_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);

-- name: NotificationActivity :many
-- activity(): события из списка за ACTIVITY_DAYS дней, не свои, новые сверху (latest('id')), не больше 15.
SELECT l.id, l.event, l.description, l.details, l.created_at, u.name AS user_name
FROM activity_logs l
LEFT JOIN users u ON u.id = l.user_id
WHERE l.club_id = sqlc.arg(club_id)
  AND l.created_at >= sqlc.arg(since)
  AND l.event = ANY(sqlc.arg(events)::text[])
  AND l.user_id <> sqlc.arg(user_id)
ORDER BY l.id DESC
LIMIT 15;

-- name: NotificationChat :one
-- chat(): число непрочитанных сообщений гостей и последнее из них.
SELECT count(*)::bigint AS unread,
       COALESCE((array_agg(id ORDER BY id DESC))[1], 0)::bigint AS last_id,
       COALESCE((array_agg(body ORDER BY id DESC))[1], '')::text AS last_body,
       COALESCE((array_agg(created_at ORDER BY id DESC))[1], 'epoch')::timestamptz AS last_at
FROM chat_messages
WHERE club_id = sqlc.arg(club_id) AND NOT from_staff AND read_at IS NULL;

-- name: NotificationOrders :one
-- orders(): число новых заказов и последний из них.
SELECT count(*)::bigint AS new_count,
       COALESCE((array_agg(id ORDER BY id DESC))[1], 0)::bigint AS last_id,
       COALESCE((array_agg(computer_name ORDER BY id DESC))[1], '—')::text AS last_computer,
       COALESCE((array_agg(created_at ORDER BY id DESC))[1], 'epoch')::timestamptz AS last_at
FROM shop_orders
WHERE club_id = sqlc.arg(club_id) AND status = 'new';

-- name: NotificationLowStock :many
-- lowStock(): товары с остатком меньше порога, по остатку и имени.
SELECT id, name, quantity, updated_at
FROM products
WHERE club_id = sqlc.arg(club_id) AND quantity < sqlc.arg(below)
ORDER BY quantity, name, id;

-- name: NotificationOfflineComputers :many
-- offlineComputers(): ПК, которые должны быть на связи (status <> 'offline'), но давно не отвечали.
SELECT c.id, c.number, c.display_name, c.device_name, c.last_seen_at, z.name AS zone_name
FROM club_computers c
LEFT JOIN club_zones z ON z.club_id = c.club_id AND z.id = c.club_zone_id
WHERE c.club_id = sqlc.arg(club_id)
  AND c.last_seen_at IS NOT NULL AND c.last_seen_at < sqlc.arg(seen_before)
  AND c.status <> 'offline'
ORDER BY c.club_zone_id, c.number, c.id;

-- name: NotificationSubscription :one
-- subscription(): подписка клуба.
SELECT status, trial_ends_at, current_period_ends_at
FROM club_subscriptions
WHERE club_id = sqlc.arg(club_id)
LIMIT 1;

-- name: NotificationInvitations :many
-- invitations(): непринятые приглашения, истекающие в ближайшие сутки.
SELECT id, email, role, expires_at
FROM club_invitations
WHERE club_id = sqlc.arg(club_id) AND accepted_at IS NULL
  AND expires_at >= sqlc.arg(now) AND expires_at <= sqlc.arg(until)
ORDER BY expires_at, id;

-- name: NotificationComputerHealth :many
-- Новое (Этап 7, S7-H-11): открытые проблемы здоровья ПК (computer_health_events, cleared_at IS NULL) — строка на ПК,
-- свежие сверху, не больше page_size ПК (частичный индекс computer_health_events_open_index). Виды — по алфавиту,
-- подробности — {вид: details на момент появления}.
SELECT e.computer_id, c.number, c.display_name, c.device_name,
       max(e.raised_at)::timestamptz AS last_raised_at,
       array_agg(e.kind::text ORDER BY e.kind)::text[] AS kinds,
       jsonb_object_agg(e.kind, e.details)::jsonb AS details
FROM computer_health_events e
JOIN club_computers c ON c.club_id = e.club_id AND c.id = e.computer_id
WHERE e.club_id = sqlc.arg(club_id) AND e.cleared_at IS NULL
GROUP BY e.computer_id, c.number, c.display_name, c.device_name
ORDER BY last_raised_at DESC, e.computer_id
LIMIT sqlc.arg(page_size);
