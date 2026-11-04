-- Telegram (Этап 4, H2): настройки Telegram Gateway и бота оповещений, коды восстановления пароля и коды
-- подтверждения перевода. Laravel: app/Services/TelegramGateway.php, PasswordRecovery.php, FriendTransferCodes.php,
-- Monitoring/TelegramAlerts.php.
-- Платформенные запросы (пул zafiro_platform): users, platform_settings, password_recovery_codes — без RLS;
-- лимиты кодов перевода считаются на аккаунт во всех клубах (BR-092, решение Q-028/L-1) — политика platform_access.
-- Исключены из tenant-filter префиксом «Telegram» в sqlc.yaml. Время — параметром из часов приложения.

-- name: TelegramSetting :one
-- TelegramGateway::token() / TelegramAlerts::settings(): прямо из таблицы, без кэша.
SELECT value FROM platform_settings WHERE key = sqlc.arg(key);

-- ---------------------------------------------------------------------------
-- Восстановление пароля (PasswordRecovery)
-- ---------------------------------------------------------------------------

-- name: TelegramUserByPhone :one
-- PasswordRecovery::account(): User::where('phone', $phone)->first() (блокировку проверяет вызывающий).
SELECT id, name, phone, blocked_at
FROM users
WHERE phone = sqlc.arg(phone);

-- name: TelegramLockUser :one
-- Отправки кодов одного аккаунта — по очереди (в старом коде проверка лимита и запись кода не были атомарны).
SELECT id, phone FROM users WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: TelegramDeleteOldRecoveryCodes :exec
-- (clone $codes)->where('created_at', '<', now()->subDay())->delete()
DELETE FROM password_recovery_codes
WHERE user_id = sqlc.arg(user_id) AND created_at < sqlc.arg(before);

-- name: TelegramRecoveryCodeStats :one
-- Последний код (latest('id')) и коды за час — лимиты RESEND_SECONDS и CODES_PER_HOUR; нет кодов — 1970-01-01.
SELECT COALESCE((SELECT c.created_at FROM password_recovery_codes c WHERE c.user_id = sqlc.arg(user_id) ORDER BY c.id DESC LIMIT 1), to_timestamp(0))::timestamptz AS last_at,
       (SELECT count(*) FROM password_recovery_codes c WHERE c.user_id = sqlc.arg(user_id) AND c.created_at >= sqlc.arg(since))::bigint AS in_hour,
       COALESCE((SELECT min(c.created_at) FROM password_recovery_codes c WHERE c.user_id = sqlc.arg(user_id) AND c.created_at >= sqlc.arg(since)), to_timestamp(0))::timestamptz AS first_in_hour;

-- name: TelegramInsertRecoveryCode :one
INSERT INTO password_recovery_codes (user_id, code_hash, attempts, expires_at, created_at, updated_at)
VALUES (sqlc.arg(user_id), sqlc.arg(code_hash), 0, sqlc.arg(expires_at), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: TelegramDeleteRecoveryCode :exec
-- Код не ушёл в Telegram: запись снимается (в старом коде её и не было — запись после отправки).
DELETE FROM password_recovery_codes WHERE id = sqlc.arg(id);

-- name: TelegramLatestRecoveryCode :one
-- PasswordRecovery::reset(): последний (по id) неистёкший код FOR UPDATE (параллельные попытки — по очереди).
SELECT id, code_hash, attempts
FROM password_recovery_codes
WHERE user_id = sqlc.arg(user_id) AND expires_at > sqlc.arg(now)
ORDER BY id DESC
LIMIT 1
FOR UPDATE;

-- name: TelegramRecoveryCodeAttempt :exec
-- $recovery->increment('attempts')
UPDATE password_recovery_codes SET attempts = attempts + 1, updated_at = sqlc.arg(now) WHERE id = sqlc.arg(id);

-- name: TelegramDeleteUserRecoveryCodes :exec
DELETE FROM password_recovery_codes WHERE user_id = sqlc.arg(user_id);

-- name: TelegramSetUserPassword :exec
-- $user->forceFill(['password' => $password])->save()
UPDATE users SET password = sqlc.arg(password_hash), updated_at = sqlc.arg(now) WHERE id = sqlc.arg(id);

-- ---------------------------------------------------------------------------
-- Коды подтверждения перевода (FriendTransferCodes::send) — лимиты на аккаунт во всех клубах
-- ---------------------------------------------------------------------------

-- name: TelegramDeleteOldTransferCodes :exec
DELETE FROM transfer_codes
WHERE user_id = sqlc.arg(user_id) AND created_at < sqlc.arg(before);

-- name: TelegramTransferCodeStats :one
SELECT COALESCE((SELECT c.created_at FROM transfer_codes c WHERE c.user_id = sqlc.arg(user_id) ORDER BY c.id DESC LIMIT 1), to_timestamp(0))::timestamptz AS last_at,
       (SELECT count(*) FROM transfer_codes c WHERE c.user_id = sqlc.arg(user_id) AND c.created_at >= sqlc.arg(since))::bigint AS in_hour,
       COALESCE((SELECT min(c.created_at) FROM transfer_codes c WHERE c.user_id = sqlc.arg(user_id) AND c.created_at >= sqlc.arg(since)), to_timestamp(0))::timestamptz AS first_in_hour;

-- name: TelegramInsertTransferCode :one
-- Код привязан к клубу (Q-028/L-1), дружбе и сумме.
INSERT INTO transfer_codes (club_id, user_id, friendship_id, amount_cents, code_hash, attempts, expires_at, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(user_id), sqlc.arg(friendship_id), sqlc.arg(amount_cents), sqlc.arg(code_hash), 0,
        sqlc.arg(expires_at), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: TelegramDeleteTransferCode :exec
DELETE FROM transfer_codes WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);
