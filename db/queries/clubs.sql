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

-- name: ListZonesByClub :many
SELECT id, club_id, name, description, capacity, price_per_hour, icon, theme, status,
       is_featured, sort_order, position_x, position_y, width, height, rotation
FROM club_zones
WHERE club_id = sqlc.arg(club_id)
ORDER BY sort_order, id;

-- name: CountComputersByClub :one
SELECT count(*) AS total
FROM club_computers
WHERE club_id = sqlc.arg(club_id);

-- name: GetMemberBalance :one
-- Только чтение. Изменение баланса — через журнал операций (ledger), Этап 1.
SELECT club_id, user_id, balance, bonus_balance, updated_at
FROM member_balances
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);
