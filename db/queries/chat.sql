-- Чат сотрудников с гостями ПК (Этап 3, группа 4). Laravel: app/Http/Controllers/ChatController.php,
-- app/Models/ChatMessage.php. Сторона гостя (агент) — guest.sql (GuestListChatMessages, GuestInsertChatMessage).

-- name: ListChatConversations :many
-- conversations(): диалоги по последнему сообщению (MAX(id)), непрочитанные гостя, не больше 100; клиент и
-- последнее сообщение — в том же запросе.
WITH g AS (
    SELECT customer_id, max(id)::bigint AS last_id,
           count(*) FILTER (WHERE NOT from_staff AND read_at IS NULL) AS unread
    FROM chat_messages
    WHERE club_id = sqlc.arg(club_id)
    GROUP BY customer_id
    ORDER BY max(id) DESC
    LIMIT 100
)
SELECT g.customer_id, g.last_id, g.unread::bigint AS unread, u.name AS customer_name, u.phone AS customer_phone,
       m.body AS last_body, m.from_staff AS last_from_staff, m.created_at AS last_at
FROM g
LEFT JOIN users u ON u.id = g.customer_id
JOIN chat_messages m ON m.club_id = sqlc.arg(club_id) AND m.id = g.last_id
ORDER BY g.last_id DESC;

-- name: ChatCustomer :one
-- User::find($customerId) для открытого диалога — только клиент, у которого есть сообщения в этом клубе.
SELECT u.id, u.name, u.phone
FROM users u
WHERE u.id = sqlc.arg(customer_id)
  AND EXISTS (SELECT 1 FROM chat_messages m WHERE m.club_id = sqlc.arg(club_id) AND m.customer_id = u.id);

-- name: ListChatThread :many
-- Сообщения диалога после after_id (0 — с начала): новые сверху, не больше 100 (в обработчике — по возрастанию).
SELECT m.id, m.from_staff, m.body, m.created_at, a.name AS author_name,
       c.number AS computer_number, c.display_name AS computer_display_name, c.device_name AS computer_device_name
FROM chat_messages m
LEFT JOIN users a ON a.id = m.author_id
LEFT JOIN club_computers c ON c.club_id = m.club_id AND c.id = m.club_computer_id
WHERE m.club_id = sqlc.arg(club_id) AND m.customer_id = sqlc.arg(customer_id) AND m.id > sqlc.arg(after_id)::bigint
ORDER BY m.id DESC
LIMIT 100;

-- name: MarkChatRead :execrows
-- markRead(): сообщения гостя в диалоге прочитаны.
UPDATE chat_messages SET read_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND customer_id = sqlc.arg(customer_id) AND NOT from_staff AND read_at IS NULL;

-- name: ChatUnreadTotal :one
-- ChatController::unreadTotal().
SELECT count(*)::bigint FROM chat_messages WHERE club_id = sqlc.arg(club_id) AND NOT from_staff AND read_at IS NULL;

-- name: InsertStaffChatMessage :one
-- reply(): ответ сотрудника (from_staff).
INSERT INTO chat_messages (club_id, customer_id, author_id, from_staff, body, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(customer_id), sqlc.arg(author_id), true, sqlc.arg(body), sqlc.arg(now), sqlc.arg(now))
RETURNING id;
