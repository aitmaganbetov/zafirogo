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
