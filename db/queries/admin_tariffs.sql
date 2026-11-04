-- Тарифы и зоны для REST админки (Этап 2, B1). Laravel: app/Http/Controllers/TariffController.php index().

-- name: AdminListTariffs :many
-- Тарифы клуба без удалённых (SoftDeletes), с зонами действия (пусто — все зоны).
SELECT t.id, t.name, t.description, t.duration_minutes, t.price, t.currency, t.is_active, t.payment_method,
       t.discount_enabled, t.usage_limit_enabled, t.usage_limit_minutes, t.quantity_limit_enabled, t.quantity_limit,
       COALESCE((SELECT array_agg(z.club_zone_id ORDER BY z.club_zone_id)
                 FROM club_zone_tariff z
                 WHERE z.club_id = t.club_id AND z.tariff_id = t.id), '{}')::bigint[] AS zone_ids
FROM tariffs t
WHERE t.club_id = sqlc.arg(club_id) AND t.deleted_at IS NULL
ORDER BY t.name, t.id;

-- name: GetTariffCurrency :one
-- $tariff->currency для журнала tariff.purchased (с удалёнными: тариф уже заблокирован покупкой).
SELECT currency
FROM tariffs
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- ---------------------------------------------------------------------------
-- Экран «Зоны и тарифы» (Этап 3, группа 4). Laravel: app/Http/Controllers/TariffController.php page(), store(),
-- update(), destroy(); app/Http/Requests/StoreTariffRequest.php.
-- ---------------------------------------------------------------------------

-- name: AdminTariffPageZones :many
-- ClubZone::query()->whereHas('computers')->orderBy('name'): зоны, в которых есть ПК.
SELECT z.id, z.name, z.price_per_hour
FROM club_zones z
WHERE z.club_id = sqlc.arg(club_id)
  AND EXISTS (SELECT 1 FROM club_computers c WHERE c.club_id = z.club_id AND c.club_zone_id = z.id)
ORDER BY z.name, z.id;

-- name: AdminTariffPage :many
-- Tariff::query()->with('zones')->when(zone, …)->orderBy('duration_minutes')->orderBy('name'): тарифы клуба
-- без удалённых; фильтр зоны (0 — нет) — тарифы этой зоны и тарифы всех зон (без зон). sold — продажи в агенте
-- (tariff_sales, Q-007; в старом — COUNT журнала tariff.purchased).
SELECT t.id, t.name, t.description, t.duration_minutes, t.price, t.currency, t.is_active, t.payment_method,
       t.discount_enabled, t.usage_limit_enabled, t.usage_limit_minutes, t.quantity_limit_enabled, t.quantity_limit,
       t.sale_periods, t.validity_periods,
       COALESCE((SELECT array_agg(z.club_zone_id ORDER BY z.club_zone_id)
                 FROM club_zone_tariff z
                 WHERE z.club_id = t.club_id AND z.tariff_id = t.id), '{}')::bigint[] AS zone_ids,
       COALESCE((SELECT s.sold FROM tariff_sales s WHERE s.club_id = t.club_id AND s.tariff_id = t.id), 0)::bigint AS sold
FROM tariffs t
WHERE t.club_id = sqlc.arg(club_id) AND t.deleted_at IS NULL
  AND (sqlc.arg(zone_id)::bigint = 0
       OR EXISTS (SELECT 1 FROM club_zone_tariff z
                  WHERE z.club_id = t.club_id AND z.tariff_id = t.id AND z.club_zone_id = sqlc.arg(zone_id)::bigint)
       OR NOT EXISTS (SELECT 1 FROM club_zone_tariff z WHERE z.club_id = t.club_id AND z.tariff_id = t.id))
ORDER BY t.duration_minutes, t.name, t.id;

-- name: AdminLockTariff :one
-- Тариф для изменения/удаления (route model binding: удалённый — 404), строка блокируется.
SELECT t.id, t.name, t.duration_minutes, t.price, t.is_active, t.currency,
       COALESCE((SELECT array_agg(z.club_zone_id ORDER BY z.club_zone_id)
                 FROM club_zone_tariff z
                 WHERE z.club_id = t.club_id AND z.tariff_id = t.id), '{}')::bigint[] AS zone_ids
FROM tariffs t
WHERE t.club_id = sqlc.arg(club_id) AND t.id = sqlc.arg(id) AND t.deleted_at IS NULL
FOR UPDATE OF t;

-- name: AdminTariffNameTaken :one
-- Rule::unique('tariffs', 'name')->where('club_id')->whereNull('deleted_at')->ignore(id): без учёта регистра
-- (utf8mb4_unicode_ci, Q-025/Q-031).
SELECT EXISTS (SELECT 1 FROM tariffs
               WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL
                 AND lower(name) = lower(sqlc.arg(name)::text) AND id <> sqlc.arg(except_id)::bigint)::boolean;

-- name: AdminZonesWithComputersIn :many
-- Rule::in(ClubZone::whereHas('computers')->pluck('id')): какие из выбранных зон допустимы.
SELECT z.id
FROM club_zones z
WHERE z.club_id = sqlc.arg(club_id) AND z.id = ANY(sqlc.arg(ids)::bigint[])
  AND EXISTS (SELECT 1 FROM club_computers c WHERE c.club_id = z.club_id AND c.club_zone_id = z.id);

-- name: AdminInsertTariff :one
INSERT INTO tariffs (club_id, name, description, duration_minutes, price, is_active, payment_method, currency,
                     discount_enabled, usage_limit_enabled, usage_limit_minutes, quantity_limit_enabled, quantity_limit,
                     validity_periods, sale_periods, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(name), sqlc.narg(description), sqlc.arg(duration_minutes), sqlc.arg(price),
        sqlc.arg(is_active), sqlc.arg(payment_method), sqlc.arg(currency), sqlc.arg(discount_enabled),
        sqlc.arg(usage_limit_enabled), sqlc.narg(usage_limit_minutes), sqlc.arg(quantity_limit_enabled),
        sqlc.narg(quantity_limit), sqlc.narg(validity_periods), sqlc.narg(sale_periods), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: AdminUpdateTariff :exec
-- $tariff->update($data): поля формы (валюта и user_roles не меняются — PD-11).
UPDATE tariffs
SET name = sqlc.arg(name), description = sqlc.narg(description), duration_minutes = sqlc.arg(duration_minutes),
    price = sqlc.arg(price), is_active = sqlc.arg(is_active), payment_method = sqlc.arg(payment_method),
    discount_enabled = sqlc.arg(discount_enabled), usage_limit_enabled = sqlc.arg(usage_limit_enabled),
    usage_limit_minutes = sqlc.narg(usage_limit_minutes), quantity_limit_enabled = sqlc.arg(quantity_limit_enabled),
    quantity_limit = sqlc.narg(quantity_limit), validity_periods = sqlc.narg(validity_periods),
    sale_periods = sqlc.narg(sale_periods), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: AdminSoftDeleteTariff :exec
-- $tariff->delete() (SoftDeletes): связи с зонами и история остаются.
UPDATE tariffs SET deleted_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: AdminDeleteTariffZones :exec
-- syncWithPivotValues(): сначала убрать зоны, которых нет в новом списке.
DELETE FROM club_zone_tariff
WHERE club_id = sqlc.arg(club_id) AND tariff_id = sqlc.arg(tariff_id)
  AND NOT (club_zone_id = ANY(sqlc.arg(keep_zone_ids)::bigint[]));

-- name: AdminAddTariffZones :exec
-- syncWithPivotValues(): добавить недостающие зоны.
INSERT INTO club_zone_tariff (club_id, club_zone_id, tariff_id)
SELECT sqlc.arg(club_id), z, sqlc.arg(tariff_id) FROM unnest(sqlc.arg(zone_ids)::bigint[]) AS z
ON CONFLICT DO NOTHING;
