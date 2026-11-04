-- Настройки клуба (Этап 3, группа 5). Laravel: app/Http/Controllers/SettingsController.php, LanguageController.php,
-- ClubLogoController.php, ClubSignInImageController.php. clubs — платформенная таблица без RLS: каждый запрос
-- фильтрует по id = club_id клуба из сессии. Хэш PIN наружу не отдаётся — только признак «задан».

-- name: SettingsClub :one
SELECT id, name, settings, timezone, locale,
       (agent_exit_pin_hash IS NOT NULL)::boolean AS pin_set, agent_exit_pin_changed_at,
       agent_staff_minutes, logo_path, signin_image_path
FROM clubs
WHERE id = sqlc.arg(club_id)::bigint;

-- name: SettingsLockClub :one
-- Сохранение секции, пояса, языка, PIN, картинок: строка клуба блокируется (в старом — перезапись JSON целиком
-- без блокировки: два одновременных сохранения теряли изменения друг друга).
SELECT id, name, settings, timezone, locale, agent_staff_minutes, logo_path, signin_image_path
FROM clubs
WHERE id = sqlc.arg(club_id)::bigint
FOR UPDATE;

-- name: SettingsSave :exec
UPDATE clubs SET settings = sqlc.arg(settings), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(club_id)::bigint;

-- name: SettingsSetTimezone :exec
UPDATE clubs SET timezone = sqlc.arg(timezone), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(club_id)::bigint;

-- name: SettingsSetLocale :exec
UPDATE clubs SET locale = sqlc.arg(locale), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(club_id)::bigint;

-- name: SettingsSetAgentPin :exec
-- agent_exit_pin_changed_at меняется и при удалении PIN (как forceFill в agentPin()).
UPDATE clubs SET agent_exit_pin_hash = sqlc.narg(pin_hash), agent_exit_pin_changed_at = sqlc.arg(now),
                 updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(club_id)::bigint;

-- name: SettingsSetStaffMinutes :exec
UPDATE clubs SET agent_staff_minutes = sqlc.arg(minutes), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(club_id)::bigint;

-- name: SettingsSetLogoPath :exec
UPDATE clubs SET logo_path = sqlc.narg(path), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(club_id)::bigint;

-- name: SettingsSetSigninImagePath :exec
UPDATE clubs SET signin_image_path = sqlc.narg(path), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(club_id)::bigint;

-- name: SettingsPutImage :exec
-- Картинка клуба вида kind (logo | signin); новая заменяет прежнюю.
INSERT INTO club_images (club_id, kind, content_type, data, created_at)
VALUES (sqlc.arg(club_id)::bigint, sqlc.arg(kind), sqlc.arg(content_type), sqlc.arg(data), sqlc.arg(now))
ON CONFLICT (club_id, kind) DO UPDATE SET content_type = EXCLUDED.content_type, data = EXCLUDED.data,
                                          created_at = EXCLUDED.created_at;

-- name: SettingsDeleteImage :exec
DELETE FROM club_images WHERE club_id = sqlc.arg(club_id)::bigint AND kind = sqlc.arg(kind);

-- name: SettingsPublicImage :one
-- ClubLogoController::show() / ClubSignInImageController::show(): публичная картинка клуба (агент грузит её без
-- токена). Пул zafiro_platform: клуб из URL; имя файла сверяется с basename пути в обработчике.
SELECT c.logo_path, c.signin_image_path, i.content_type, i.data
FROM clubs c
JOIN club_images i ON i.club_id = c.id
WHERE c.id = sqlc.arg(club_id)::bigint AND i.kind = sqlc.arg(kind);

-- name: SettingsLanguages :many
-- SettingsController::languages(): все языки платформы (Language::orderBy('sort_order')->orderBy('id')).
SELECT code, name, is_enabled
FROM languages
ORDER BY sort_order, id;
