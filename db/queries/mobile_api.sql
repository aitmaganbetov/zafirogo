-- Мобильные приложения (Flutter mobileclient / mobileemployee): слой совместимости со старым REST
-- (server/internal/http/legacy, решение Q-058, docs/mobile-compat.md). Токены — mobile_api_tokens (0024).

-- ---------------------------------------------------------------------------
-- Токены Sanctum
-- ---------------------------------------------------------------------------

-- name: MobileTokenLookup :one
-- PersonalAccessToken::findToken("<id>|<токен>"): строка по id и sha256 токена. Транзакция ставит
-- app.mobile_token_hash (политика mobile_token_lookup) — клуб ещё не известен.
SELECT t.id, t.club_id, t.user_id, t.name, t.expires_at, t.revoked_at, u.blocked_at
FROM mobile_api_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.id = sqlc.arg(id) AND t.token_hash = sqlc.arg(token_hash);

-- name: MobileTokenInsert :one
-- $user->createToken($deviceName, ['club:'.$clubId], now()->addDays(30)).
INSERT INTO mobile_api_tokens (club_id, user_id, name, token_hash, created_at, expires_at)
VALUES (sqlc.arg(club_id), sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(now), sqlc.arg(expires_at))
RETURNING id;

-- name: MobileTokenTouch :exec
-- Sanctum: last_used_at при каждом запросе (здесь — не чаще раза в минуту, см. legacy.touchEvery).
UPDATE mobile_api_tokens SET last_used_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: MobileTokenRevoke :exec
-- $request->user()->currentAccessToken()->delete(): строка остаётся с revoked_at (маршрутизация по клубу).
UPDATE mobile_api_tokens SET revoked_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: MobileTokensRevokeUser :execrows
-- $user->tokens()->delete() (PasswordRecovery::reset()): токены аккаунта во всех клубах. Транзакция с app.user_id
-- (политики mobile_token_owner_*); условие на club_id — только чтобы явно не трогать чужие строки.
UPDATE mobile_api_tokens SET revoked_at = sqlc.arg(now)
WHERE user_id = sqlc.arg(user_id) AND club_id IS NOT NULL AND revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Вход, клубы аккаунта, профиль
-- ---------------------------------------------------------------------------

-- name: MobileUserClubs :many
-- Клубы аккаунта (AuthController::clubList(): orderBy clubs.name) и id членства (login: ClubMembership::first() —
-- первое по id). Транзакция с app.user_id (политика member_self_read).
SELECT m.id AS membership_id, m.club_id, c.name AS club_name, m.role
FROM club_memberships m
JOIN clubs c ON c.id = m.club_id
WHERE m.user_id = sqlc.arg(user_id)
ORDER BY c.name, c.id;

-- name: MobileMemberProfile :one
-- AuthController::userData() и Staff\MeController::show(): членство, балансы, клуб (название, валюта, пояс).
SELECT m.role, m.discount::float8 AS discount, m.total_spent, m.permissions, m.notifications_seen_at,
       COALESCE(b.balance, 0)::bigint AS balance, COALESCE(b.bonus_balance, 0)::bigint AS bonus_balance,
       c.name AS club_name, c.timezone, c.settings,
       u.name AS user_name, u.phone AS user_phone
FROM club_memberships m
JOIN clubs c ON c.id = m.club_id
JOIN users u ON u.id = m.user_id
LEFT JOIN member_balances b ON b.club_id = m.club_id AND b.user_id = m.user_id
WHERE m.club_id = sqlc.arg(club_id) AND m.user_id = sqlc.arg(user_id);

-- ---------------------------------------------------------------------------
-- Приложение сотрудника (routes/api_staff.php)
-- ---------------------------------------------------------------------------

-- name: MobileStaffZones :many
-- ComputerController::index(): ClubZone::orderBy('sort_order')->orderBy('id').
SELECT id, name, price_per_hour
FROM club_zones
WHERE club_id = sqlc.arg(club_id)
ORDER BY sort_order, id;

-- name: MobileStaffTariffs :many
-- ComputerController::index(): Tariff::where('is_active', true)->with('zones:id')->orderBy('duration_minutes')->orderBy('name').
SELECT t.id, t.name, t.duration_minutes, t.price, t.usage_limit_enabled, t.usage_limit_minutes,
       COALESCE(array_agg(zt.club_zone_id ORDER BY zt.club_zone_id) FILTER (WHERE zt.club_zone_id IS NOT NULL),
                '{}')::bigint[] AS zone_ids
FROM tariffs t
LEFT JOIN club_zone_tariff zt ON zt.club_id = t.club_id AND zt.tariff_id = t.id
WHERE t.club_id = sqlc.arg(club_id) AND t.is_active AND t.deleted_at IS NULL
GROUP BY t.id
ORDER BY t.duration_minutes, t.name;

-- name: MobileStaffShiftTransactions :many
-- CashController::show(): $shift->transactions()->latest('id')->limit(N).
SELECT id, type, amount, description, created_at
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND cash_shift_id = sqlc.arg(shift_id)
ORDER BY id DESC
LIMIT sqlc.arg(page_size);

-- name: MobileStaffRecentTransactions :many
-- MeController::today(): CashTransaction::latest('id')->limit(10).
SELECT id, type, amount, description, created_at
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id)
ORDER BY id DESC
LIMIT sqlc.arg(page_size);

-- name: MobileStaffCustomers :many
-- CustomerController::index(): клиенты клуба (не сотрудники), поиск по имени (LIKE) или цифрам телефона,
-- orderBy('user_id')->paginate(25). total — для meta.last_page.
SELECT m.user_id, u.name, u.phone, m.discount::float8 AS discount, m.total_spent,
       COALESCE(b.balance, 0)::bigint AS balance, COALESCE(b.bonus_balance, 0)::bigint AS bonus_balance,
       count(*) OVER ()::bigint AS total
FROM club_memberships m
JOIN users u ON u.id = m.user_id
LEFT JOIN member_balances b ON b.club_id = m.club_id AND b.user_id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND m.role NOT IN ('owner', 'admin', 'manager', 'cashier', 'operator')
  AND (sqlc.arg(name_like)::text = ''
       OR u.name ILIKE sqlc.arg(name_like)::text
       OR (sqlc.arg(phone_like)::text <> '' AND u.phone LIKE sqlc.arg(phone_like)::text))
ORDER BY m.user_id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: MobileStaffProducts :many
-- Staff\ProductController::index(): поиск по имени или SKU, orderBy('category')->orderBy('name')->paginate(50).
SELECT id, sku, name, category, price, quantity, count(*) OVER ()::bigint AS total
FROM products
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL
  AND (sqlc.arg(search_like)::text = '' OR name ILIKE sqlc.arg(search_like)::text OR sku ILIKE sqlc.arg(search_like)::text)
ORDER BY category, name, id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: MobileStaffOrders :many
-- Staff\OrderController::index(): ShopOrder::latest('id')->paginate(30) с клиентом (id, имя, телефон) и сотрудником.
SELECT o.id, o.user_id, o.computer_name, o.items, o.amount, o.status, o.handled_at, o.created_at,
       cu.name AS customer_name, cu.phone AS customer_phone, h.name AS handler_name,
       count(*) OVER ()::bigint AS total
FROM shop_orders o
LEFT JOIN users cu ON cu.id = o.user_id
LEFT JOIN users h ON h.id = o.handled_by
WHERE o.club_id = sqlc.arg(club_id)
  AND (sqlc.arg(status)::text = '' OR o.status = sqlc.arg(status)::text)
ORDER BY o.id DESC
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: MobileStaffOrder :one
-- Заказ после выдачи/отмены (->load(['customer', 'handler'])).
SELECT o.id, o.user_id, o.computer_name, o.items, o.amount, o.status, o.handled_at, o.created_at,
       cu.name AS customer_name, cu.phone AS customer_phone, h.name AS handler_name
FROM shop_orders o
LEFT JOIN users cu ON cu.id = o.user_id
LEFT JOIN users h ON h.id = o.handled_by
WHERE o.club_id = sqlc.arg(club_id) AND o.id = sqlc.arg(id);

-- name: MobileStaffCashDays :many
-- MeController::today(): приход/расход и число операций по местным дням клуба (одним запросом; границы дней —
-- диапазоны в UTC, индекс cash_transactions (club_id, created_at)). day — номер дня 0..6 (6 — сегодня).
SELECT d.day::int AS day, ct.type,
       COALESCE(sum(ct.amount), 0)::bigint AS total, count(ct.id)::bigint AS count
FROM unnest(sqlc.arg(starts)::timestamptz[]) WITH ORDINALITY AS d(start_at, day)
JOIN cash_transactions ct ON ct.club_id = sqlc.arg(club_id)
     AND ct.created_at >= d.start_at AND ct.created_at < d.start_at + interval '1 day'
GROUP BY d.day, ct.type;

-- name: MobileStaffLowStock :many
-- MeController::today(): Product::where('quantity', '<', 5)->orderBy('quantity')->orderBy('name')->limit(20).
SELECT id, name, quantity
FROM products
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL AND quantity < 5
ORDER BY quantity, name
LIMIT 20;

-- name: MobileStaffNewOrders :one
-- ShopOrder::newCount().
SELECT count(*)::bigint FROM shop_orders WHERE club_id = sqlc.arg(club_id) AND status = 'new';
