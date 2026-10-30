-- Примерные запросы Этапа 0: проверка, что sqlc генерирует код по схеме 0001_init.sql.
-- Правило: каждый тенантный запрос фильтрует по club_id (второй рубеж — RLS, Этап 1).
-- Списки, которые растут, — с keyset-пагинацией по (club_id, id).
-- Секреты клуба (installation_key*, agent_exit_pin_hash) в выборки не попадают.

-- name: GetClub :one
-- Платформенный запрос: клуб по id (id тенанта и есть club_id).
SELECT id, name, slug, settings, locale, timezone, agent_staff_minutes,
       logo_path, signin_image_path, created_at, updated_at
FROM clubs
WHERE id = sqlc.arg(club_id);

-- name: GetClubBySlug :one
-- Платформенный запрос: клуб по slug (публичные страницы, вход).
SELECT id, name, slug, locale, timezone
FROM clubs
WHERE slug = sqlc.arg(slug);

-- name: ListZonesByClub :many
SELECT id, club_id, name, description, capacity, price_per_hour, icon, theme, status,
       is_featured, sort_order, position_x, position_y, width, height, rotation
FROM club_zones
WHERE club_id = sqlc.arg(club_id)
ORDER BY sort_order, id;

-- name: ListComputersByClub :many
-- Keyset-пагинация: after_id = id последней строки предыдущей страницы (0 — первая страница).
-- zone_id NULL — все зоны. Индекс: club_computers_club_id_id_unique.
SELECT id, club_id, club_zone_id, number, status, is_reserved, device_name, display_name,
       agent_state, agent_version, current_session_id, current_session_user_id,
       session_guest, session_ends_at, last_seen_at
FROM club_computers
WHERE club_id = sqlc.arg(club_id)
  AND id > sqlc.arg(after_id)
  AND (sqlc.narg(zone_id)::bigint IS NULL OR club_zone_id = sqlc.narg(zone_id))
ORDER BY id
LIMIT sqlc.arg(page_size);

-- name: CountComputersByClub :one
SELECT count(*) AS total
FROM club_computers
WHERE club_id = sqlc.arg(club_id);

-- name: GetComputer :one
SELECT id, club_id, club_zone_id, number, status, is_reserved, device_name, display_name,
       device_id, agent_state, agent_version, current_session_id, current_session_user_id,
       session_guest, session_ends_at, last_seen_at, staff_user_id, staff_kind, staff_until
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ListActiveTariffsByClub :many
-- Без удалённых (soft delete) — как global scope SoftDeletes в Tariff.
SELECT id, club_id, name, description, duration_minutes, price, payment_method, currency,
       discount_enabled, usage_limit_enabled, usage_limit_minutes, validity_periods, sale_periods
FROM tariffs
WHERE club_id = sqlc.arg(club_id)
  AND is_active
  AND deleted_at IS NULL
ORDER BY name;

-- name: GetMemberBalance :one
-- Только чтение. Изменение баланса — через журнал операций (ledger), Этап 1.
SELECT club_id, user_id, balance, bonus_balance, updated_at
FROM member_balances
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);
