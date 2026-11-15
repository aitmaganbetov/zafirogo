-- Город, адрес и профиль клуба, фото, оборудование зон (тема В, docs/upstream-2026-11.md): «Настройка клуба →
-- Город и адрес» и «Настройки → Профиль клуба». Laravel: SettingsController::location(),
-- app/Http/Controllers/ClubProfileController.php. clubs — платформенная таблица без RLS: запросы фильтруют по
-- id = club_id клуба из сессии; club_photos, club_photo_images, club_zones, club_computers — тенантные (RLS).

-- name: ClubProfileGet :one
SELECT id, city, address, profile
FROM clubs
WHERE id = sqlc.arg(club_id)::bigint;

-- name: ClubProfileLock :one
-- Сохранение города, профиля, загрузка фото: строка клуба блокируется (лимит фото считается под блокировкой —
-- две одновременные загрузки не превысят ClubPhoto::LIMIT; в старом такой защиты не было).
SELECT id, city, address, profile
FROM clubs
WHERE id = sqlc.arg(club_id)::bigint
FOR UPDATE;

-- name: ClubProfileSetLocation :exec
-- SettingsController::location(): forceFill(['city', 'address']).
UPDATE clubs SET city = sqlc.narg(city), address = sqlc.narg(address), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(club_id)::bigint;

-- name: ClubProfileSetProfile :exec
-- ClubProfileController::update(): forceFill(['profile' => $profile ?: null]).
UPDATE clubs SET profile = sqlc.narg(profile), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(club_id)::bigint;

-- name: ClubProfilePhotos :many
-- ClubPhoto::orderBy('sort_order')->orderBy('id'): первое — обложка. Не больше 10 строк на клуб.
SELECT id, path, sort_order
FROM club_photos
WHERE club_id = sqlc.arg(club_id)
ORDER BY sort_order, id;

-- name: ClubProfilePhotoStats :one
-- ClubPhoto::count() и (int) ClubPhoto::max('sort_order').
SELECT count(*)::bigint AS total, COALESCE(max(sort_order), 0)::integer AS max_order
FROM club_photos
WHERE club_id = sqlc.arg(club_id);

-- name: ClubProfileInsertPhoto :one
INSERT INTO club_photos (club_id, path, sort_order, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(path), sqlc.arg(sort_order), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: ClubProfileInsertPhotoImage :exec
-- Байты фото (в старом — файл на локальном диске; решение UQ-19).
INSERT INTO club_photo_images (photo_id, club_id, content_type, data, created_at)
VALUES (sqlc.arg(photo_id), sqlc.arg(club_id), sqlc.arg(content_type), sqlc.arg(data), sqlc.arg(now));

-- name: ClubProfileDeletePhoto :execrows
-- ClubProfileController::destroyPhoto(): строка и её байты (каскад club_photo_images).
DELETE FROM club_photos
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ClubProfileCoverPhoto :execrows
-- ClubProfileController::coverPhoto(): sort_order = min(sort_order) − 1 — фото становится первым (обложкой).
UPDATE club_photos p
SET sort_order = (SELECT min(x.sort_order) FROM club_photos x WHERE x.club_id = sqlc.arg(club_id)) - 1,
    updated_at = sqlc.arg(now)
WHERE p.club_id = sqlc.arg(club_id) AND p.id = sqlc.arg(id);

-- name: ClubProfilePublicPhoto :one
-- ClubProfileController::photo(): публичное фото по случайному имени файла (приложение грузит его без токена).
-- Пул zafiro_platform: клуб и имя файла — из адреса, строка ищется по полному пути.
SELECT i.content_type, i.data
FROM club_photos p
JOIN club_photo_images i ON i.club_id = p.club_id AND i.photo_id = p.id
WHERE p.club_id = sqlc.arg(club_id) AND p.path = sqlc.arg(path);

-- name: ClubProfileZones :many
-- Зоны клуба с числом ПК и железом по данным агентов — одним запросом (в старом: withCount('computers') и три
-- запроса ZoneDevices::detected()). Самый частый cpu_model / gpu_model зоны — mode() (при равенстве — первый по
-- алфавиту; в старом порядок при равенстве не определён), память — MAX(ram_total_mb). NULL-значения в расчёт не
-- входят (whereNotNull).
SELECT z.id, z.name, z.description, z.price_per_hour, z.devices,
       COALESCE(pc.pcs, 0)::bigint AS computers_count,
       COALESCE(pc.cpu_model, '')::text AS cpu_model, (pc.cpu_model IS NOT NULL)::boolean AS has_cpu,
       COALESCE(pc.gpu_model, '')::text AS gpu_model, (pc.gpu_model IS NOT NULL)::boolean AS has_gpu,
       COALESCE(pc.ram_total_mb, 0)::bigint AS ram_total_mb, (pc.ram_total_mb IS NOT NULL)::boolean AS has_ram
FROM club_zones z
LEFT JOIN (
    SELECT c.club_zone_id,
           count(*) AS pcs,
           mode() WITHIN GROUP (ORDER BY c.cpu_model) AS cpu_model,
           mode() WITHIN GROUP (ORDER BY c.gpu_model) AS gpu_model,
           max(c.ram_total_mb) AS ram_total_mb
    FROM club_computers c
    WHERE c.club_id = sqlc.arg(club_id)
    GROUP BY c.club_zone_id
) pc ON pc.club_zone_id = z.id
WHERE z.club_id = sqlc.arg(club_id)
ORDER BY z.sort_order, z.id;

-- name: ClubProfileZoneForUpdate :one
-- ClubZone::findOrFail($zone) — зона своего клуба.
SELECT id, name
FROM club_zones
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ClubProfileSetZone :exec
-- ClubProfileController::updateZone(): forceFill(['description', 'devices' => $devices ?: null]).
UPDATE club_zones SET description = sqlc.narg(description), devices = sqlc.narg(devices), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);
