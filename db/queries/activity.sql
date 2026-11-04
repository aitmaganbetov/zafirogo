-- Журнал действий клуба (activity_logs, партиции по месяцам — 0011_events.sql).
-- Laravel: app/Services/ActivityLogger.php ActivityLogger::record().

-- name: InsertActivityLog :one
INSERT INTO activity_logs (club_id, event, description, user_id, subject_type, subject_id, details, ip_address,
                           user_agent, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(event), sqlc.arg(description), sqlc.arg(user_id), sqlc.narg(subject_type),
        sqlc.narg(subject_id), sqlc.narg(details), sqlc.arg(ip_address), sqlc.narg(user_agent),
        sqlc.arg(created_at), sqlc.arg(created_at))
RETURNING id;

-- name: ListClubActivityLogs :many
-- Новые сверху, keyset по (created_at, id); период — диапазоном [from, before). Пустой event — все события.
SELECT id, event, description, user_id, subject_type, subject_id, details, ip_address, user_agent, created_at
FROM activity_logs
WHERE club_id = sqlc.arg(club_id)::bigint
  AND created_at >= sqlc.arg(from_created_at)
  AND (created_at, id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::bigint)
  AND (sqlc.arg(event)::text = '' OR event = sqlc.arg(event)::text)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size);
