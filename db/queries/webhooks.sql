-- Вебхуки интеграций (webhook_endpoints, webhook_deliveries — 0001_init.sql).
-- Laravel: app/Services/Integrations/WebhookDispatcher.php, app/Jobs/DeliverWebhook.php.

-- name: ListWebhookListeners :many
-- Активные endpoint'ы клуба, подписанные на событие (WebhookDispatcher::listeners() + in_array($event, events)).
SELECT id
FROM webhook_endpoints
WHERE club_id = sqlc.arg(club_id)
  AND is_active
  AND events @> jsonb_build_array(sqlc.arg(event)::text)
ORDER BY id;

-- name: InsertWebhookDelivery :execrows
-- Доставка в статусе pending (WebhookDispatcher::dispatch()). event_id детерминирован (событие outbox +
-- endpoint), поэтому повторная обработка того же сообщения JetStream не создаёт вторую доставку.
INSERT INTO webhook_deliveries (club_id, webhook_endpoint_id, event_id, event, payload, status, next_attempt_at,
                                created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(webhook_endpoint_id), sqlc.arg(event_id), sqlc.arg(event), sqlc.arg(payload),
        'pending', sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz)
ON CONFLICT (event_id) DO NOTHING;

-- name: ClaimDueWebhookDeliveries :many
-- Платформенный: «аренда» доставок, которым пора (status = pending, next_attempt_at <= now): next_attempt_at
-- сдвигается на lease_until, чтобы другой инстанс не отправил ту же доставку, пока эта отправляется вне
-- транзакции. Упавший отправитель — доставка вернётся в очередь после аренды.
UPDATE webhook_deliveries
SET next_attempt_at = sqlc.arg(lease_until)::timestamptz
WHERE (club_id, id) IN (SELECT due.club_id, due.id
                        FROM webhook_deliveries AS due
                        WHERE due.status = 'pending'
                          AND due.next_attempt_at <= sqlc.arg(now)::timestamptz
                        ORDER BY due.next_attempt_at
                        LIMIT sqlc.arg(batch_size)
                        FOR UPDATE SKIP LOCKED)
RETURNING club_id, id;

-- name: GetWebhookDelivery :one
SELECT id, club_id, webhook_endpoint_id, event_id, event, payload, status, attempts
FROM webhook_deliveries
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: GetWebhookEndpointForDelivery :one
-- Секрет — для подписи (в Laravel зашифрован APP_KEY; расшифровка при переносе — Q-050).
SELECT id, club_id, url, secret, is_active, failure_count
FROM webhook_endpoints
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: SkipWebhookDelivery :exec
UPDATE webhook_deliveries
SET status = 'skipped', next_attempt_at = NULL, updated_at = sqlc.arg(now)::timestamptz
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: SaveWebhookAttempt :exec
-- Результат попытки: delivered | pending (повтор в next_attempt_at) | failed.
UPDATE webhook_deliveries
SET status           = sqlc.arg(status),
    attempts         = sqlc.arg(attempts),
    last_status_code = sqlc.narg(last_status_code),
    last_error       = sqlc.narg(last_error),
    next_attempt_at  = sqlc.narg(next_attempt_at),
    delivered_at     = sqlc.narg(delivered_at),
    updated_at       = sqlc.arg(now)::timestamptz
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: WebhookEndpointSucceeded :exec
UPDATE webhook_endpoints
SET failure_count = 0, last_success_at = sqlc.arg(now)::timestamptz, updated_at = sqlc.arg(now)::timestamptz
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: WebhookEndpointFailed :one
-- failure_count++; при disable_after подряд неудачных доставках endpoint выключается
-- (DeliverWebhook::handle(), integrations.webhook_disable_after_failures = 20).
UPDATE webhook_endpoints
SET failure_count   = failure_count + 1,
    last_failure_at = sqlc.arg(now)::timestamptz,
    is_active       = CASE WHEN failure_count + 1 >= sqlc.arg(disable_after)::bigint THEN false ELSE is_active END,
    updated_at      = sqlc.arg(now)::timestamptz
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
RETURNING failure_count, is_active;
