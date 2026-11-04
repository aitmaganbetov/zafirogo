-- Настройки → «API и интеграции» (Этап 3, группа 5). Laravel: app/Http/Controllers/IntegrationController.php,
-- app/Services/Integrations/ApiKeyManager.php. Таблицы api_keys, webhook_endpoints, webhook_deliveries,
-- api_request_logs — 0001_init.sql. Ключ хранится SHA-256 и префиксом; секрет вебхука — для подписи (Q-050).

-- name: IntegrationsAccess :one
-- @can('manage-integrations'): ClubMembership::canManageIntegrations() — владелец или флаг членства.
SELECT id, role, can_manage_integrations
FROM club_memberships
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);

-- name: IntegrationsListKeys :many
-- ApiKey::query()->with('creator:id,name')->latest('id')->get().
SELECT k.id, k.name, u.name AS creator_name, k.created_at, k.prefix, k.scopes, k.allowed_ips,
       k.rate_limit_per_minute, k.expires_at, k.last_used_at, k.last_used_ip, k.revoked_at
FROM api_keys k
LEFT JOIN users u ON u.id = k.created_by
WHERE k.club_id = sqlc.arg(club_id)
ORDER BY k.id DESC;

-- name: IntegrationsCountActiveKeys :one
SELECT count(*)::bigint FROM api_keys WHERE club_id = sqlc.arg(club_id) AND revoked_at IS NULL;

-- name: IntegrationsInsertKey :one
INSERT INTO api_keys (club_id, created_by, name, prefix, key_hash, scopes, allowed_ips, rate_limit_per_minute,
                      expires_at, rotated_from_id, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(created_by), sqlc.arg(name), sqlc.arg(prefix), sqlc.arg(key_hash),
        sqlc.arg(scopes), sqlc.narg(allowed_ips), sqlc.narg(rate_limit_per_minute), sqlc.narg(expires_at),
        sqlc.narg(rotated_from_id), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: IntegrationsLockKey :one
SELECT id, name, prefix, scopes, allowed_ips, rate_limit_per_minute, expires_at, revoked_at
FROM api_keys
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: IntegrationsRevokeKey :exec
UPDATE api_keys SET revoked_at = sqlc.arg(now), revoked_by = sqlc.arg(revoked_by), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: IntegrationsListWebhooks :many
-- WebhookEndpoint::query()->latest('id')->get() (не больше 20 на клуб).
SELECT id, url, events, is_active, failure_count, last_success_at, last_failure_at
FROM webhook_endpoints
WHERE club_id = sqlc.arg(club_id)
ORDER BY id DESC;

-- name: IntegrationsCountWebhooks :one
SELECT count(*)::bigint FROM webhook_endpoints WHERE club_id = sqlc.arg(club_id);

-- name: IntegrationsInsertWebhook :one
INSERT INTO webhook_endpoints (club_id, created_by, url, secret, events, is_active, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(created_by), sqlc.arg(url), sqlc.arg(secret), sqlc.arg(events), true,
        sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: IntegrationsLockWebhook :one
SELECT id, url, events, is_active
FROM webhook_endpoints
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: IntegrationsUpdateWebhook :exec
-- updateWebhook(): при включении — failure_count = 0.
UPDATE webhook_endpoints
SET url           = sqlc.arg(url),
    events        = sqlc.arg(events),
    is_active     = sqlc.arg(is_active),
    failure_count = CASE WHEN sqlc.arg(reset_failures)::boolean THEN 0 ELSE failure_count END,
    updated_at    = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: IntegrationsSetWebhookSecret :exec
UPDATE webhook_endpoints SET secret = sqlc.arg(secret), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: IntegrationsDeleteWebhook :exec
DELETE FROM webhook_endpoints WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: IntegrationsListDeliveries :many
-- Журнал доставок клуба, новые сверху; keyset по id (before_id = id последней строки; 0 — первая страница).
-- Индекс webhook_deliveries_club_id_id_index (0019).
SELECT d.id, d.created_at, d.event, e.url AS endpoint_url, d.status, d.last_error, d.attempts
FROM webhook_deliveries d
LEFT JOIN webhook_endpoints e ON e.club_id = d.club_id AND e.id = d.webhook_endpoint_id
WHERE d.club_id = sqlc.arg(club_id)
  AND (sqlc.arg(before_id)::bigint = 0 OR d.id < sqlc.arg(before_id)::bigint)
ORDER BY d.id DESC
LIMIT sqlc.arg(page_size);

-- name: IntegrationsLockDelivery :one
SELECT id, event, status
FROM webhook_deliveries
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: IntegrationsRetryDelivery :exec
-- retryDelivery(): status = pending, next_attempt_at = now() — доставку берёт webhook.Deliverer.
UPDATE webhook_deliveries SET status = 'pending', next_attempt_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: IntegrationsListRequests :many
-- ApiRequestLog::where('club_id', $clubId)->latest('created_at')->limit(30).
SELECT created_at, key_prefix, method, endpoint, status, duration_ms, ip
FROM api_request_logs
WHERE club_id = sqlc.arg(club_id)
ORDER BY created_at DESC, id DESC
LIMIT 30;

-- name: IntegrationsListStaff :many
-- ClubMembership::with('user')->whereIn('role', ['admin', 'manager', 'cashier', 'operator'])->orderBy('role').
SELECT m.id, u.name, m.role, m.can_manage_integrations
FROM club_memberships m
LEFT JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id) AND m.role IN ('admin', 'manager', 'cashier', 'operator')
ORDER BY m.role, m.id;

-- name: IntegrationsLockMember :one
SELECT m.id, m.user_id, m.role, m.can_manage_integrations, u.name
FROM club_memberships m
LEFT JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id) AND m.id = sqlc.arg(id)
FOR UPDATE OF m;

-- name: IntegrationsSetMemberAccess :exec
UPDATE club_memberships SET can_manage_integrations = sqlc.arg(allowed), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);
