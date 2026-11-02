-- Интеграционный API /api/v1 (Этап 4, H1). Laravel: routes/api_v1.php, app/Http/Controllers/Api/V1/*,
-- app/Http/Middleware/Api/*, app/Services/Integrations/ApiKeyManager.php. Клуб — только из API-ключа.
-- Списки — курсор по id (cursorPaginate старого кода): after_id — последний id прошлой страницы, page_size — limit+1.

-- name: ApiV1FindKey :one
-- ApiKeyManager::load(): ключ по уникальному префиксу (политика api_key_lookup, 0022 — до выбора клуба) и клуб.
SELECT k.id, k.club_id, k.created_by, k.prefix, k.key_hash, k.scopes, k.allowed_ips, k.rate_limit_per_minute,
       k.expires_at, k.revoked_at, c.name AS club_name, c.slug AS club_slug
FROM api_keys k
JOIN clubs c ON c.id = k.club_id
WHERE k.prefix = sqlc.arg(prefix);

-- name: ApiV1OwnerAccess :one
-- $owner?->canManageIntegrations(): роль owner или can_manage_integrations.
SELECT (role = 'owner' OR can_manage_integrations)::boolean AS allowed
FROM club_memberships
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);

-- name: ApiV1TouchKey :exec
-- AuthenticateApiKey::touch(): last_used_at и last_used_ip (не чаще раза в минуту на ключ).
UPDATE api_keys SET last_used_at = sqlc.arg(now), last_used_ip = sqlc.arg(ip)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ApiV1InsertRequestLog :exec
-- LogApiRequest: club_id NULL — ключ не прошёл проверку (политика 0004/0022 разрешает NULL).
INSERT INTO api_request_logs (club_id, api_key_id, key_prefix, method, endpoint, status, duration_ms, ip, created_at)
VALUES (sqlc.narg(club_id), sqlc.narg(api_key_id), sqlc.narg(key_prefix), sqlc.arg(method), sqlc.arg(endpoint),
        sqlc.arg(status), sqlc.arg(duration_ms), sqlc.arg(ip), sqlc.arg(created_at));

-- name: ApiV1IdempotencyGet :one
SELECT id, request_hash, status, response, created_at
FROM api_idempotency_keys
WHERE club_id = sqlc.arg(club_id) AND api_key_id = sqlc.arg(api_key_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: ApiV1IdempotencyInsert :one
-- ApiIdempotency::handle(): первая заявка ключа (status NULL — «выполняется»). Нет строки — ключ уже занят.
INSERT INTO api_idempotency_keys (club_id, api_key_id, idempotency_key, request_hash, created_at)
VALUES (sqlc.arg(club_id), sqlc.arg(api_key_id), sqlc.arg(idempotency_key), sqlc.arg(request_hash), sqlc.arg(now))
ON CONFLICT (api_key_id, idempotency_key) DO NOTHING
RETURNING id;

-- name: ApiV1IdempotencyFinish :exec
UPDATE api_idempotency_keys SET status = sqlc.arg(status), response = sqlc.arg(response)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ApiV1IdempotencyDelete :exec
DELETE FROM api_idempotency_keys WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ApiV1Products :many
-- ProductController::index(): sku — точное совпадение, search — начало имени (LIKE 'x%' в MySQL без учёта регистра),
-- category_id. Удалённые (SoftDeletes) не видны.
SELECT id, sku, name, product_category_id, category, price, quantity, updated_at
FROM products
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL AND id > sqlc.arg(after_id)
  AND (sqlc.narg(sku)::text IS NULL OR sku = sqlc.narg(sku)::text)
  AND (sqlc.narg(search_prefix)::text IS NULL OR name ILIKE sqlc.narg(search_prefix)::text)
  AND (sqlc.narg(category_id)::bigint IS NULL OR product_category_id = sqlc.narg(category_id)::bigint)
ORDER BY id
LIMIT sqlc.arg(page_size);

-- name: ApiV1Product :one
SELECT id, sku, name, product_category_id, category, price, quantity, updated_at
FROM products
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: ApiV1SkuTaken :one
-- Rule::unique('products', 'sku')->where('club_id', …)->ignore($product?->id) (удалённые тоже занимают SKU).
SELECT EXISTS (
    SELECT 1 FROM products
    WHERE club_id = sqlc.arg(club_id) AND sku = sqlc.arg(sku) AND id <> sqlc.arg(except_id)
)::boolean;

-- name: ApiV1CategoryExists :one
-- Rule::exists('product_categories', 'id')->where('club_id', …).
SELECT EXISTS (SELECT 1 FROM product_categories WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id))::boolean;

-- name: ApiV1ProductExists :one
-- Rule::exists('products', 'id')->where('club_id', …) — без условия deleted_at, как в старом.
SELECT EXISTS (SELECT 1 FROM products WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id))::boolean;

-- name: ApiV1Categories :many
SELECT id, name
FROM product_categories
WHERE club_id = sqlc.arg(club_id) AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(page_size);

-- name: ApiV1StockMovements :many
SELECT id, product_id, type, quantity, balance_after, note, created_at
FROM stock_movements
WHERE club_id = sqlc.arg(club_id) AND id > sqlc.arg(after_id)
  AND (sqlc.narg(product_id)::bigint IS NULL OR product_id = sqlc.narg(product_id)::bigint)
ORDER BY id
LIMIT sqlc.arg(page_size);

-- name: ApiV1StockMovement :one
SELECT id, product_id, type, quantity, balance_after, note, created_at
FROM stock_movements
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ApiV1Sales :many
-- SaleController::sales(): приходы кассы с receipt_data (продажи POS и всё, что пишет чек). from/to — включительно.
SELECT id, cash_shift_id, amount, receipt_data, created_at
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND type = 'income' AND receipt_data IS NOT NULL AND id > sqlc.arg(after_id)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR created_at >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR created_at <= sqlc.narg(to_at)::timestamptz)
  AND (sqlc.narg(cash_shift_id)::bigint IS NULL OR cash_shift_id = sqlc.narg(cash_shift_id)::bigint)
ORDER BY id
LIMIT sqlc.arg(page_size);

-- name: ApiV1Sale :one
SELECT id, cash_shift_id, amount, receipt_data, created_at
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND type = 'income' AND receipt_data IS NOT NULL;

-- name: ApiV1Zones :many
-- ClubZone::query()->withCount('computers') — счётчик подзапросом (без запроса на зону).
SELECT z.id, z.name, z.description, z.capacity, z.status,
       (SELECT count(*) FROM club_computers c WHERE c.club_id = z.club_id AND c.club_zone_id = z.id)::bigint AS computers_count
FROM club_zones z
WHERE z.club_id = sqlc.arg(club_id) AND z.id > sqlc.arg(after_id)
ORDER BY z.id
LIMIT sqlc.arg(page_size);

-- name: ApiV1Computers :many
SELECT id, club_zone_id, number, display_name, device_name, status, is_reserved, current_session_id, last_seen_at
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id > sqlc.arg(after_id)
  AND (sqlc.narg(zone_id)::bigint IS NULL OR club_zone_id = sqlc.narg(zone_id)::bigint)
ORDER BY id
LIMIT sqlc.arg(page_size);

-- name: ApiV1ComputersByIDs :many
-- ПК после команды: whereKey(ids)->orderBy('number').
SELECT id, club_zone_id, number, display_name, device_name, status, is_reserved, current_session_id, last_seen_at
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY number, id;

-- name: ApiV1Computer :one
SELECT id, club_zone_id, number, display_name, device_name, status, is_reserved, current_session_id, last_seen_at
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ApiV1LockComputer :one
-- ComputerController::reservation(): ClubComputer::query()->lockForUpdate()->findOrFail().
SELECT id, club_zone_id, number, display_name, device_name, status, is_reserved, current_session_id, last_seen_at
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ApiV1LockComputers :many
-- ComputerController::control(): whereKey(ids)->orderBy('number')->lockForUpdate() (поля — как ClubMapComputersByIDs).
SELECT id, club_zone_id, number, display_name, device_name, mac_address, is_reserved, current_session_id,
       agent_version
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY number, id
FOR UPDATE;

-- name: ApiV1CountComputers :one
-- Rule::exists('club_computers', 'id')->where('club_id', …) для computer_ids.*: сколько id из списка — ПК клуба.
SELECT count(*)::bigint FROM club_computers WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ApiV1ComputerIDsOf :many
SELECT id FROM club_computers WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ApiV1SetReserved :exec
UPDATE club_computers SET is_reserved = sqlc.arg(reserved), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ApiV1Tariffs :many
-- Tariff::query()->with('zones'): зоны — массивом в той же строке (без запроса на тариф). Удалённые не видны.
SELECT t.id, t.name, t.description, t.duration_minutes, t.price, t.currency, t.is_active,
       COALESCE((SELECT array_agg(zt.club_zone_id ORDER BY zt.club_zone_id)
                 FROM club_zone_tariff zt
                 WHERE zt.club_id = t.club_id AND zt.tariff_id = t.id), '{}')::bigint[] AS zone_ids
FROM tariffs t
WHERE t.club_id = sqlc.arg(club_id) AND t.deleted_at IS NULL AND t.id > sqlc.arg(after_id)
  AND (sqlc.narg(active)::boolean IS NULL OR t.is_active = sqlc.narg(active)::boolean)
ORDER BY t.id
LIMIT sqlc.arg(page_size);

-- name: ApiV1Members :many
-- ClubMembership::query()->with(['user:id,name,phone', 'balance']) — одним запросом.
SELECT m.id, m.user_id, m.role, u.name AS user_name, u.phone AS user_phone,
       b.balance, b.bonus_balance
FROM club_memberships m
LEFT JOIN users u ON u.id = m.user_id
LEFT JOIN member_balances b ON b.club_id = m.club_id AND b.user_id = m.user_id
WHERE m.club_id = sqlc.arg(club_id) AND m.id > sqlc.arg(after_id)
ORDER BY m.id
LIMIT sqlc.arg(page_size);
