-- Outbox событий (0011_events.sql): запись — в транзакции действия (роль zafiro_app, тенантная транзакция);
-- выборка и пометка — публикатор (роль zafiro_platform, один исполнитель на кластер — worker/leader).

-- name: InsertOutboxEvent :one
INSERT INTO outbox_events (club_id, type, payload)
VALUES (sqlc.arg(club_id), sqlc.arg(type), sqlc.arg(payload))
RETURNING id, event_id, created_at;

-- name: ClaimOutboxBatch :many
-- Платформенный: неопубликованные события всех клубов по порядку записи. Строки заблокированы до конца
-- транзакции публикатора; SKIP LOCKED — второй публикатор (если лидерство на миг раздвоилось) их не возьмёт.
SELECT id, club_id, event_id, type, payload, created_at, attempts
FROM outbox_events
WHERE published_at IS NULL
ORDER BY id
LIMIT sqlc.arg(batch_size)
FOR UPDATE SKIP LOCKED;

-- name: MarkOutboxPublished :execrows
-- Платформенный: опубликованные события (id — первичный ключ; club_id — их клубы).
UPDATE outbox_events
SET published_at = sqlc.arg(published_at)::timestamptz
WHERE id = ANY(sqlc.arg(ids)::bigint[])
  AND club_id = ANY(sqlc.arg(club_ids)::bigint[])
  AND published_at IS NULL;

-- name: MarkOutboxAttemptFailed :exec
UPDATE outbox_events
SET attempts = attempts + 1, last_error = sqlc.arg(last_error)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: DeletePublishedOutboxEvents :execrows
-- Платформенный: очистка опубликованных старше срока, батчами.
DELETE FROM outbox_events
WHERE (club_id, id) IN (SELECT old.club_id, old.id FROM outbox_events AS old
                        WHERE old.published_at < sqlc.arg(published_before)::timestamptz
                        ORDER BY old.published_at
                        LIMIT sqlc.arg(batch_size));

-- name: CountUnpublishedOutboxEvents :one
-- Платформенный: размер очереди и возраст самого старого события (метрика/алерт).
SELECT count(*)::bigint AS pending, COALESCE(EXTRACT(EPOCH FROM now() - min(created_at)), 0)::bigint AS oldest_age_seconds, count(DISTINCT club_id)::bigint AS clubs
FROM outbox_events
WHERE published_at IS NULL;
