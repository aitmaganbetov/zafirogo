-- Перешифровка секретов текущим ключом данных (Этап 4, H4; воркер reencrypt-secrets, пул zafiro_platform,
-- server/internal/worker/reencrypt_secrets.go). Ключа в SQL нет: выборка → secretbox в Go → запись с проверкой,
-- что значение не изменилось (параллельная ротация секрета пользователем не перезаписывается).

-- name: SecretsWebhooksToReseal :many
-- Секреты вебхуков не текущим ключом (открытый текст до 0025, v1, v2 со старым kid). Keyset по id.
SELECT id, club_id, secret
FROM webhook_endpoints
WHERE id > sqlc.arg(after_id) AND NOT starts_with(secret, sqlc.arg(current_prefix)::text)
ORDER BY id
LIMIT sqlc.arg(batch);

-- name: SecretsResealWebhook :execrows
UPDATE webhook_endpoints SET secret = sqlc.arg(new_secret)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND secret = sqlc.arg(old_secret);

-- name: SecretsPlatformSettingsToReseal :many
-- Токены Telegram (и любые настройки платформы с полем token), зашифрованные не текущим ключом.
SELECT key, value
FROM platform_settings
WHERE value->>'token' ~ '^v[0-9]+:' AND NOT starts_with(value->>'token', sqlc.arg(current_prefix)::text)
ORDER BY key;

-- name: SecretsResealPlatformSetting :execrows
UPDATE platform_settings SET value = jsonb_set(value, '{token}', to_jsonb(sqlc.arg(new_token)::text))
WHERE key = sqlc.arg(key) AND value->>'token' = sqlc.arg(old_token)::text;
