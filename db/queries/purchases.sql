-- Покупка времени клиентом в агенте (тариф, минуты, запуск пакета) и гостевая продажа на кассе:
-- чтение и блокировки в порядке старого кода (server/internal/billing/service, BR-005).
-- Laravel: app/Services/AgentTariffPurchaseService.php purchase(), purchaseMinutes(), startPackage(), sell();
-- app/Services/GuestSessions.php sell(); app/Services/ComputerReservations.php blocking(), markArrivedBy().

-- name: LockTariffForPurchase :one
-- Tariff::query()->lockForUpdate()->findOrFail(): без мягко удалённых (SoftDeletes).
SELECT id, name, duration_minutes, price, payment_method, discount_enabled, is_active,
       usage_limit_enabled, usage_limit_minutes, quantity_limit_enabled, quantity_limit,
       user_roles, sale_periods, validity_periods
FROM tariffs
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND deleted_at IS NULL
FOR UPDATE;

-- name: GetTariffWithTrashed :one
-- $package->tariff (withTrashed) — тариф пакета, в том числе мягко удалённый.
SELECT id, name, duration_minutes, price, payment_method, discount_enabled, is_active,
       usage_limit_enabled, usage_limit_minutes, quantity_limit_enabled, quantity_limit,
       user_roles, sale_periods, validity_periods, deleted_at
FROM tariffs
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: GetActiveTariff :one
-- GuestSessions::sell(): Tariff::query()->where('is_active', true)->find() — без блокировки.
SELECT id, name, duration_minutes, price, payment_method, discount_enabled, is_active,
       usage_limit_enabled, usage_limit_minutes, quantity_limit_enabled, quantity_limit,
       user_roles, sale_periods, validity_periods
FROM tariffs
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND is_active AND deleted_at IS NULL;

-- name: ListTariffZoneIDs :many
SELECT club_zone_id
FROM club_zone_tariff
WHERE club_id = sqlc.arg(club_id) AND tariff_id = sqlc.arg(tariff_id)
ORDER BY club_zone_id;

-- name: LockMembershipForPurchase :one
-- $user->currentMembership()->lockForUpdate()->firstOrFail(). discount — decimal(5,2), в PHP (float).
SELECT role, discount::float8 AS discount, total_spent
FROM club_memberships
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id)
FOR UPDATE;

-- name: GetMembershipForPricing :one
-- $user->currentMembership()->first() без блокировки (hourlyPrice()).
SELECT role, discount::float8 AS discount, total_spent
FROM club_memberships
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);

-- name: IncrementTotalSpent :one
-- LoyaltyProgram::addSpending(): ClubMembership::where('user_id')->increment('total_spent', …) (BR-042).
UPDATE club_memberships
SET total_spent = total_spent + sqlc.arg(cents), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id)
RETURNING total_spent;

-- name: SetTotalSpent :exec
-- LoyaltyProgram::subtractSpending(): новое значение считает bonus.SubtractSpending под блокировкой строки.
UPDATE club_memberships
SET total_spent = sqlc.arg(total_spent), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);

-- name: GetTariffSales :one
-- Продаж тарифа в агенте (Решение Q-007 (docs/DECISIONS.md); вызывать под блокировкой тарифа).
SELECT COALESCE((SELECT sold FROM tariff_sales WHERE club_id = sqlc.arg(club_id) AND tariff_id = sqlc.arg(tariff_id)), 0)::bigint AS sold;

-- name: IncrementTariffSales :exec
INSERT INTO tariff_sales (club_id, tariff_id, sold, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(tariff_id), 1, sqlc.arg(now))
ON CONFLICT (club_id, tariff_id) DO UPDATE SET sold = tariff_sales.sold + 1, updated_at = EXCLUDED.updated_at;

-- name: LockComputer :one
-- ClubComputer::query()->lockForUpdate()->findOrFail().
SELECT id, club_zone_id, number, display_name, device_name, is_reserved, last_seen_at,
       current_session_id, current_session_user_id, session_guest, session_ends_at, staff_until, status
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: LockComputers :many
-- Несколько ПК в одном порядке (по id), чтобы две операции не взаимоблокировались
-- (SeatTransfers::transfer(), GuestSessions::sell()).
SELECT id, club_zone_id, number, display_name, device_name, is_reserved, last_seen_at,
       current_session_id, current_session_user_id, session_guest, session_ends_at, staff_until, status
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY id
FOR UPDATE;

-- name: GetZone :one
SELECT id, name, price_per_hour
FROM club_zones
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ListZonesByIDs :many
SELECT id, name, price_per_hour
FROM club_zones
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY id;

-- name: GetBlockingReservation :one
-- ComputerReservations::blocking(): чужая ожидающая бронь, пересекающая [from, until) (until уже с интервалом
-- между бронями); своя бронь клиента (user_id) не мешает; user_id = 0 — гость (мешает любая).
SELECT id, starts_at
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id)
  AND club_computer_id = sqlc.arg(computer_id)
  AND cancelled_at IS NULL AND arrived_at IS NULL
  AND starts_at < sqlc.arg(until) AND ends_at > sqlc.arg(from_at)
  AND (user_id IS NULL OR user_id <> sqlc.arg(user_id))
ORDER BY starts_at
LIMIT 1;

-- name: MarkReservationsArrived :exec
-- ComputerReservations::markArrivedBy(): своя бронь клиента на это время выполнена.
UPDATE computer_reservations
SET arrived_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id)
  AND club_computer_id = sqlc.arg(computer_id)
  AND user_id = sqlc.arg(user_id)
  AND cancelled_at IS NULL AND arrived_at IS NULL
  AND starts_at < sqlc.arg(until) AND ends_at > sqlc.arg(from_at);

-- name: InsertSessionPurchase :one
INSERT INTO session_purchases (club_id, session_id, user_id, kind, tariff_id, name, minutes, main_cents, bonus_cents,
                               expires_at, zone_id, source, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(session_id), sqlc.arg(user_id), sqlc.arg(kind), sqlc.narg(tariff_id), sqlc.arg(name),
        sqlc.arg(minutes), sqlc.arg(main_cents), sqlc.arg(bonus_cents), sqlc.narg(expires_at), sqlc.narg(zone_id),
        sqlc.arg(source), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: SetComputerSession :exec
-- Начало или продление сессии: sell() / GuestSessions::sell() ($computer->update([...])).
UPDATE club_computers
SET current_session_id = sqlc.arg(session_id),
    current_session_user_id = sqlc.narg(session_user_id),
    session_guest = sqlc.arg(session_guest),
    session_ends_at = sqlc.arg(session_ends_at),
    updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: LockTimePackage :one
-- TimePackage::query()->where('user_id', …)->lockForUpdate()->findOrFail().
SELECT id, user_id, tariff_id, name, minutes_left, expires_at
FROM time_packages
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: DeleteTimePackage :exec
DELETE FROM time_packages
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ClaimBillingRequest :one
-- Захват ключа идемпотентности до блокировок: параллельный запрос с тем же ключом ждёт COMMIT первого.
INSERT INTO billing_requests (club_id, idempotency_key, operation, fingerprint, created_at)
VALUES (sqlc.arg(club_id), sqlc.arg(idempotency_key), sqlc.arg(operation), sqlc.arg(fingerprint), sqlc.arg(now))
ON CONFLICT (club_id, idempotency_key) DO NOTHING
RETURNING club_id;

-- name: GetBillingRequest :one
SELECT operation, fingerprint, response
FROM billing_requests
WHERE club_id = sqlc.arg(club_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: SetBillingRequestResponse :exec
UPDATE billing_requests
SET response = sqlc.arg(response)
WHERE club_id = sqlc.arg(club_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: ListExpiredTimePackages :many
-- Платформенный запрос (пул zafiro_platform, все клубы): просроченные пакеты для суточного сгорания —
-- TimePackage::withoutGlobalScope('club')->where('expires_at', '<', now())->delete() (routes/console.php,
-- session-purchases:prune). Сжигает каждый пакет транзакция его клуба (Решение Q-011, docs/DECISIONS.md).
SELECT club_id, id, user_id
FROM time_packages
WHERE expires_at < sqlc.arg(now)
ORDER BY club_id, id;
