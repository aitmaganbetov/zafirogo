-- Этап 4, H4: секреты в базе только зашифрованными (решение Q-050, docs/schema-changes.md «Шифрование секретов в базе»).
--
-- В Laravel webhook_endpoints.secret — каст 'encrypted' (app/Models/WebhookEndpoint.php), токены Telegram в
-- platform_settings — Crypt::encryptString (app/Services/TelegramGateway.php, Monitoring/TelegramAlerts.php).
-- Здесь шифрует сервер (server/internal/secretbox, AES-256-GCM, ключи security.data_keys): ключа в SQL нет,
-- поэтому существующие открытые секреты перешифровывает воркер reencrypt-secrets при старте сервера
-- (server/internal/worker/reencrypt_secrets.go), а не эта миграция.
--
-- Ограничение: в webhook_endpoints.secret — только шифротекст secretbox ("v1:…" Этапа 3 или "v2:<kid>:…").
-- NOT VALID — новые и изменённые строки проверяются сразу, существующие открытые секреты дожидаются воркера;
-- если открытых секретов нет (новая база или воркер уже отработал), ограничение сразу проверяется целиком.
-- Иначе VALIDATE выполнит следующая миграция после перевода всех серверов (docs/schema-changes.md).

-- +goose Up
-- +goose StatementBegin
ALTER TABLE webhook_endpoints
    ADD CONSTRAINT webhook_endpoints_secret_sealed CHECK (secret ~ '^v[0-9]+:') NOT VALID;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM webhook_endpoints WHERE secret !~ '^v[0-9]+:') THEN
        ALTER TABLE webhook_endpoints VALIDATE CONSTRAINT webhook_endpoints_secret_sealed;
    END IF;
END $$;

COMMENT ON COLUMN webhook_endpoints.secret IS
    'Секрет подписи, зашифрован secretbox (v2:<kid>:…, контекст webhook_endpoints.secret:club=<club_id>); в Laravel — Crypt (APP_KEY), перешифровка при переносе — Q-050';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE webhook_endpoints DROP CONSTRAINT webhook_endpoints_secret_sealed;
COMMENT ON COLUMN webhook_endpoints.secret IS 'В Laravel зашифрован Crypt (APP_KEY), см. docs/open-questions/database.md';
-- +goose StatementEnd
