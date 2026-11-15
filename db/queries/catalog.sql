-- Каталог клубов для гостевого приложения (тема В, docs/upstream-2026-11.md): клубы города и страница клуба до
-- входа, вступление в клуб из списка. Laravel: app/Http/Controllers/Api/ClubCatalogController.php,
-- Api/AuthController.php joinClub(). Запросы каталога — публичные, по всем клубам: пул zafiro_platform
-- (исключение правила tenant-filter по префиксу Catalog — sqlc.yaml); в каталоге только клубы с городом.

-- name: CatalogCityCounts :many
-- Club::whereNotNull('city')->groupBy('city')->selectRaw('city, COUNT(*) as clubs').
SELECT city::text AS city, count(*)::bigint AS clubs
FROM clubs
WHERE city IS NOT NULL
GROUP BY city;

-- name: CatalogCityClubs :many
-- Карточки клубов города: число ПК, наименьшая цена часа среди зон, обложка — агрегаты в SQL одним запросом
-- (в старом — withCount / withMin / with('photos')). До 500 клубов по имени (индекс clubs_city_name_index).
SELECT c.id, c.name, c.address, c.logo_path,
       COALESCE(c.profile ->> 'hours', '')::text AS hours, (c.profile ->> 'hours' IS NOT NULL)::boolean AS has_hours,
       COALESCE(pc.pcs, 0)::bigint AS pcs,
       COALESCE(z.price_from, 0)::bigint AS price_from, (z.price_from IS NOT NULL)::boolean AS has_price,
       COALESCE(ph.path, '')::text AS photo_path
FROM clubs c
LEFT JOIN LATERAL (SELECT count(*) AS pcs FROM club_computers x WHERE x.club_id = c.id) pc ON true
LEFT JOIN LATERAL (SELECT min(x.price_per_hour) AS price_from FROM club_zones x WHERE x.club_id = c.id) z ON true
LEFT JOIN LATERAL (
    SELECT x.path FROM club_photos x WHERE x.club_id = c.id ORDER BY x.sort_order, x.id LIMIT 1
) ph ON true
WHERE c.city = sqlc.arg(city)::text
ORDER BY c.name, c.id
LIMIT 500;

-- name: CatalogFreeCandidates :many
-- ПК клубов, свободные по базе: не занят вручную, нет сессии (или она кончилась), нет сотрудника. Брони не
-- учитываются (как в старом). «В сети» проверяется по присутствию агентов (Valkey), а не по last_seen_at (UQ-20).
-- Laravel: ClubCatalogController::free().
SELECT pc.club_id, pc.club_zone_id, pc.id
FROM club_computers pc
WHERE pc.club_id = ANY(sqlc.arg(club_ids)::bigint[])
  AND pc.is_reserved = false
  AND (pc.current_session_id IS NULL OR pc.session_ends_at <= sqlc.arg(now)::timestamptz)
  AND (pc.staff_until IS NULL OR pc.staff_until <= sqlc.arg(now)::timestamptz);

-- name: CatalogClub :one
-- Club::whereNotNull('city')->findOrFail($club): клуб без города — 404.
SELECT id, name, city::text AS city, address, logo_path, profile
FROM clubs
WHERE id = sqlc.arg(club_id)::bigint AND city IS NOT NULL;

-- name: CatalogClubExists :one
-- Маршрутизация входа при поклубном переходе: клуб из запроса есть на этом сервере.
SELECT EXISTS (SELECT 1 FROM clubs WHERE id = sqlc.arg(club_id)::bigint) AS found;

-- name: CatalogJoinClub :execrows
-- AuthController::joinClub(): аккаунт становится клиентом клуба из каталога (только клуб с городом — условие в
-- запросе). Повторный или параллельный вход членство не дублирует (0 строк).
INSERT INTO club_memberships (club_id, user_id, role, discount, created_at, updated_at)
SELECT c.id, sqlc.arg(user_id)::bigint, 'client', 0, sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz
FROM clubs c
WHERE c.id = sqlc.arg(club_id)::bigint AND c.city IS NOT NULL
ON CONFLICT (club_id, user_id) DO NOTHING;
