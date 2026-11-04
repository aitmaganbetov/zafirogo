-- Второй фактор суперадмина (Этап 7, docs/open-questions/stage7-totp.md): ключ TOTP и коды восстановления.
-- Только пул zafiro_platform (server/internal/auth/mfa, кабинет суперадмина, воркер reencrypt-secrets, команда
-- zafiro superadmin-totp-reset). Таблицы платформенные (без club_id): префикс Superadmin — исключение tenant-filter.

-- name: SuperadminTotpConfirmed :one
-- Настроен ли TOTP (первый код введён).
SELECT EXISTS (SELECT 1 FROM superadmin_totp WHERE user_id = sqlc.arg(user_id) AND confirmed_at IS NOT NULL);

-- name: SuperadminTotpLock :one
-- Ключ под блокировкой строки: проверка кода и запись принятого шага — в одной транзакции.
SELECT user_id, secret, confirmed_at, last_step
FROM superadmin_totp
WHERE user_id = sqlc.arg(user_id)
FOR UPDATE;

-- name: SuperadminTotpBegin :execrows
-- Новый ключ на этапе настройки. Настроенный (confirmed_at) не заменяется: 0 строк.
INSERT INTO superadmin_totp (user_id, secret, confirmed_at, last_step, created_at, updated_at)
VALUES (sqlc.arg(user_id), sqlc.arg(secret), NULL, 0, sqlc.arg(now), sqlc.arg(now))
ON CONFLICT (user_id) DO UPDATE
    SET secret = excluded.secret, last_step = 0, updated_at = excluded.updated_at
    WHERE superadmin_totp.confirmed_at IS NULL;

-- name: SuperadminTotpConfirm :exec
UPDATE superadmin_totp
SET confirmed_at = sqlc.arg(now), last_step = sqlc.arg(step), updated_at = sqlc.arg(now)
WHERE user_id = sqlc.arg(user_id) AND confirmed_at IS NULL;

-- name: SuperadminTotpAccept :execrows
-- Принятый шаг: только больше последнего (защита от повтора кода).
UPDATE superadmin_totp
SET last_step = sqlc.arg(step), updated_at = sqlc.arg(now)
WHERE user_id = sqlc.arg(user_id) AND last_step < sqlc.arg(step);

-- name: SuperadminTotpDelete :execrows
DELETE FROM superadmin_totp WHERE user_id = sqlc.arg(user_id);

-- name: SuperadminRecoveryDeleteAll :exec
DELETE FROM superadmin_recovery_codes WHERE user_id = sqlc.arg(user_id);

-- name: SuperadminRecoveryInsert :exec
INSERT INTO superadmin_recovery_codes (user_id, code_hash, created_at)
SELECT sqlc.arg(user_id), h, sqlc.arg(now) FROM unnest(sqlc.arg(code_hashes)::text[]) AS h;

-- name: SuperadminRecoveryUse :execrows
-- Код восстановления одноразовый: отмечается использованным, повтор — 0 строк.
UPDATE superadmin_recovery_codes
SET used_at = sqlc.arg(now)
WHERE user_id = sqlc.arg(user_id) AND code_hash = sqlc.arg(code_hash) AND used_at IS NULL;

-- name: SuperadminRecoveryLeft :one
SELECT count(*)::bigint FROM superadmin_recovery_codes WHERE user_id = sqlc.arg(user_id) AND used_at IS NULL;

-- name: SuperadminUserByPhone :one
-- Аварийный сброс из командной строки: пользователь по нормализованному телефону.
SELECT id, name::text AS name, is_superadmin FROM users WHERE phone = sqlc.arg(phone);

-- name: SuperadminTotpToReseal :many
-- Ключи TOTP, зашифрованные не текущим ключом данных (воркер reencrypt-secrets). Keyset по user_id.
SELECT user_id, secret
FROM superadmin_totp
WHERE user_id > sqlc.arg(after_id) AND NOT starts_with(secret, sqlc.arg(current_prefix)::text)
ORDER BY user_id
LIMIT sqlc.arg(batch);

-- name: SuperadminTotpReseal :execrows
UPDATE superadmin_totp SET secret = sqlc.arg(new_secret)
WHERE user_id = sqlc.arg(user_id) AND secret = sqlc.arg(old_secret);
