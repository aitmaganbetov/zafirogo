-- Услуги клуба (Этап 3, G2). Laravel: app/Http/Controllers/ClubServiceController.php. Все запросы — с club_id.

-- name: ServicesListPage :many
-- index(): ClubService::orderByDesc('is_active')->orderBy('name')->paginate(50).
SELECT id, name, price, unit, is_active
FROM club_services
WHERE club_id = sqlc.arg(club_id)
ORDER BY is_active DESC, name, id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: ServicesCount :one
SELECT count(*)::bigint FROM club_services WHERE club_id = sqlc.arg(club_id);

-- name: ServiceGet :one
SELECT id, name, price, unit, is_active
FROM club_services
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ServiceInsert :one
-- store(): ClubService::create(['name', 'price', 'unit', 'is_active']).
INSERT INTO club_services (club_id, name, price, unit, is_active, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(name), sqlc.arg(price), sqlc.narg(unit), sqlc.arg(is_active), sqlc.arg(now), sqlc.arg(now))
RETURNING id, name, price, unit, is_active;

-- name: ServiceUpdate :one
-- update(): $service->update([...]).
UPDATE club_services
SET name = sqlc.arg(name), price = sqlc.arg(price), unit = sqlc.narg(unit), is_active = sqlc.arg(is_active),
    updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
RETURNING id, name, price, unit, is_active;

-- name: ServiceDelete :one
-- destroy(): $service->delete() (чеки хранят имя и цену — история не меняется).
DELETE FROM club_services
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
RETURNING id, name;
