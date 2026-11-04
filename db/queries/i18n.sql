-- Переводы интерфейса (Этап 3, F1). Laravel: app/Services/TranslationCatalog.php.
-- languages и translations — платформенные таблицы (без club_id и RLS), общие для всех клубов.

-- name: I18nEnabledLanguages :many
-- TranslationCatalog::enabledCodes(): включённые языки по sort_order, id.
SELECT code, name FROM languages WHERE is_enabled ORDER BY sort_order, id;

-- name: I18nVersion :one
-- Версия переводов (ETag): момент последнего изменения строк и языков (мс) + число строк (удаление строки
-- тоже меняет версию — PD-12).
SELECT COALESCE(floor(extract(epoch FROM greatest(
           (SELECT max(GREATEST(t.updated_at, t.created_at)) FROM translations t),
           (SELECT max(GREATEST(l.updated_at, l.created_at)) FROM languages l))) * 1000), 0)::bigint AS changed_ms,
       (SELECT count(*) FROM translations)::bigint AS rows;

-- name: I18nLines :many
-- TranslationCatalog::buildPack(): строки JSON-группы ('*') и групп pagination, validation (__('pagination.next'),
-- тексты валидатора) языка и запасного 'en' (запасной — первым, чтобы язык запроса его перекрыл). Группа agent —
-- только агенту (gRPC). Только включённые языки.
SELECT l.code, t."group", t.key, t.value
FROM translations t
JOIN languages l ON l.id = t.language_id
WHERE t."group" IN ('*', 'pagination', 'validation') AND l.is_enabled AND l.code = ANY(sqlc.arg(codes)::text[])
ORDER BY (l.code = sqlc.arg(locale)::text), t.id;

-- name: I18nEnsureLanguage :exec
-- Языки по умолчанию (миграция 2026_10_13_181908 старого проекта): ru, kk, en — системные.
INSERT INTO languages (code, name, is_enabled, is_system, sort_order, created_at, updated_at)
VALUES (sqlc.arg(code), sqlc.arg(name), true, true, sqlc.arg(sort_order), now(), now())
ON CONFLICT (code) DO NOTHING;

-- name: I18nLanguageID :one
SELECT id FROM languages WHERE code = sqlc.arg(code);

-- name: I18nInsertMissing :execrows
-- TranslationCatalog::sync(): вставить отсутствующую строку; существующую (возможно, отредактированную) не трогать.
INSERT INTO translations (language_id, "group", key, key_hash, value, created_at, updated_at)
VALUES (sqlc.arg(language_id), sqlc.arg(grp), sqlc.arg(key), sqlc.arg(key_hash), sqlc.arg(value), now(), now())
ON CONFLICT (language_id, "group", key_hash) DO NOTHING;
