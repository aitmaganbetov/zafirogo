-- 0004_rls: роли и Row-Level Security — второй рубеж тенантности после фильтра club_id в каждом sqlc-запросе.
-- docs/schema-changes.md §6 и раздел «Как реализовано (Этап 1)».
--
-- Роли (NOLOGIN; логины создаются при развёртывании и получают членство в одной из них):
--  * zafiro_app      — сервер в контексте клуба. Без BYPASSRLS, не владелец таблиц. Видит только строки клуба
--                      из app.club_id, который ставит db.InTenantTx: set_config('app.club_id', $1, true).
--  * zafiro_platform — суперадминка и фоновые задачи по всем клубам: политика platform_access (USING true),
--                      осознанный обход изоляции отдельным логином/пулом. BYPASSRLS не нужен (работает и
--                      на управляемом Postgres без суперпользователя).
-- Владелец схемы (запускает миграции) — отдельная роль; FORCE ROW LEVEL SECURITY применяет политики и к нему
-- (суперпользователь обходит RLS всегда).
-- Без app.club_id политика ложна (NULL) — строк нет, как global scope BelongsToClub (`1 = 0`).

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'zafiro_app') THEN
        CREATE ROLE zafiro_app NOLOGIN NOBYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'zafiro_platform') THEN
        CREATE ROLE zafiro_platform NOLOGIN NOBYPASSRLS;
    END IF;
END
$$;
-- +goose StatementEnd

-- Текущий клуб и пользователь транзакции. Пустая строка (значение после конца транзакции, где была
-- set_config(..., true)) и отсутствие настройки — NULL.
-- +goose StatementBegin
CREATE FUNCTION zafiro_current_club_id() RETURNS bigint
    LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT NULLIF(current_setting('app.club_id', true), '')::bigint
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION zafiro_current_user_id() RETURNS bigint
    LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT NULLIF(current_setting('app.user_id', true), '')::bigint
$$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- Политики
-- ---------------------------------------------------------------------------

-- Обычные тенантные таблицы: club_id NOT NULL.
-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        -- 0001
        'club_subscriptions', 'club_memberships', 'member_balances', 'club_invitations', 'product_categories',
        'products', 'stock_movements', 'cash_shifts', 'cash_transactions', 'club_services', 'club_zones',
        'club_computers', 'tariffs', 'club_zone_tariff', 'session_purchases', 'time_packages',
        'computer_reservations', 'agent_qr_logins', 'friendships', 'chat_messages', 'shop_orders', 'todo_tasks',
        'activity_logs', 'api_keys', 'webhook_endpoints', 'webhook_deliveries', 'club_request_metrics',
        'club_load_snapshots',
        -- 0002, 0003
        'ledger_accounts', 'ledger_transactions', 'ledger_idempotency', 'ledger_entries', 'ledger_minute_entries',
        'audit_log'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (club_id = zafiro_current_club_id()) '
                       'WITH CHECK (club_id = zafiro_current_club_id())', t);
        EXECUTE format('CREATE POLICY platform_access ON %I TO zafiro_platform USING (true) WITH CHECK (true)', t);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- club_memberships: пользователь видит свои членства во всех клубах (выбор клуба при входе),
-- если транзакция поставила app.user_id. Только чтение.
CREATE POLICY member_self_read ON club_memberships FOR SELECT USING (user_id = zafiro_current_user_id());

-- news: club_id NULL — новость платформы, её читают все клубы; пишет клуб только свои.
ALTER TABLE news ENABLE ROW LEVEL SECURITY;
ALTER TABLE news FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON news FOR SELECT USING (club_id IS NULL OR club_id = zafiro_current_club_id());
CREATE POLICY tenant_insert ON news FOR INSERT WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY tenant_update ON news FOR UPDATE USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY tenant_delete ON news FOR DELETE USING (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON news TO zafiro_platform USING (true) WITH CHECK (true);

-- api_request_logs: запрос с неверным ключом пишется без клуба (club_id NULL); читать — только свой клуб.
ALTER TABLE api_request_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_request_logs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON api_request_logs USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id IS NULL OR club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON api_request_logs TO zafiro_platform USING (true) WITH CHECK (true);

-- transfer_codes: без club_id (Q-028), клуб — через friendships. Лимиты кодов считаются на аккаунт во всех
-- клубах (BR-092, FriendTransferCodes::send()), поэтому свои коды пользователь (app.user_id) видит и удаляет везде.
ALTER TABLE transfer_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE transfer_codes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON transfer_codes
    USING (user_id = zafiro_current_user_id()
           OR EXISTS (SELECT 1 FROM friendships f
                      WHERE f.id = transfer_codes.friendship_id AND f.club_id = zafiro_current_club_id()))
    WITH CHECK (EXISTS (SELECT 1 FROM friendships f
                        WHERE f.id = transfer_codes.friendship_id AND f.club_id = zafiro_current_club_id()));
CREATE POLICY platform_access ON transfer_codes TO zafiro_platform USING (true) WITH CHECK (true);

-- api_idempotency_keys: без club_id (Q-028), клуб — через api_keys.
ALTER TABLE api_idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON api_idempotency_keys
    USING (EXISTS (SELECT 1 FROM api_keys k
                   WHERE k.id = api_idempotency_keys.api_key_id AND k.club_id = zafiro_current_club_id()))
    WITH CHECK (EXISTS (SELECT 1 FROM api_keys k
                        WHERE k.id = api_idempotency_keys.api_key_id AND k.club_id = zafiro_current_club_id()));
CREATE POLICY platform_access ON api_idempotency_keys TO zafiro_platform USING (true) WITH CHECK (true);

-- Без RLS (платформенные, не данные клуба): users, clubs, saas_plans, password_recovery_codes,
-- platform_settings, languages, translations, server_metrics, request_metrics, platform_logs
-- (журнал суперадмина; club_id — атрибут события, пишется и до входа в клуб, AuthController).

-- Проверка для тестов и CI: таблицы с колонкой club_id без включённого и принудительного RLS.
-- +goose StatementBegin
CREATE FUNCTION zafiro_tables_without_rls() RETURNS SETOF text
    LANGUAGE sql STABLE AS $$
    SELECT c.relname::text
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
    WHERE c.relkind IN ('r', 'p')
      AND NOT c.relispartition
      AND (NOT c.relrowsecurity OR NOT c.relforcerowsecurity)
      AND c.relname NOT IN ('platform_logs')
      AND (EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'club_id' AND NOT a.attisdropped)
           OR c.relname IN ('transfer_codes', 'api_idempotency_keys'))
    ORDER BY 1
$$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- Права
-- ---------------------------------------------------------------------------

GRANT USAGE ON SCHEMA public TO zafiro_app, zafiro_platform;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO zafiro_app, zafiro_platform;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO zafiro_app, zafiro_platform;
-- Таблицы следующих миграций получают те же права; RLS для них включает сама миграция
-- (проверка — zafiro_tables_without_rls() в интеграционных тестах).
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO zafiro_app, zafiro_platform;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO zafiro_app, zafiro_platform;

-- Партиции — только через родителя (на родителе действуют политики).
-- +goose StatementBegin
DO $$
DECLARE
    part regclass;
BEGIN
    FOR part IN
        SELECT i.inhrelid::regclass FROM pg_inherits i
        WHERE i.inhparent IN ('ledger_transactions'::regclass, 'ledger_entries'::regclass,
                              'ledger_minute_entries'::regclass, 'audit_log'::regclass)
    LOOP
        EXECUTE format('REVOKE ALL ON %s FROM zafiro_app, zafiro_platform', part);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- Журналы — только добавление (вдобавок к триггерам 0002/0003).
REVOKE UPDATE, DELETE, TRUNCATE ON ledger_accounts, ledger_transactions, ledger_idempotency, ledger_entries,
    ledger_minute_entries, audit_log FROM zafiro_app, zafiro_platform;

-- Баланс клиента меняет только ledger (триггер ledger_apply_member_entry, SECURITY DEFINER).
REVOKE INSERT, UPDATE ON member_balances FROM zafiro_app, zafiro_platform;
GRANT INSERT (club_id, user_id, created_at, updated_at) ON member_balances TO zafiro_app, zafiro_platform;
GRANT UPDATE (created_at, updated_at) ON member_balances TO zafiro_app, zafiro_platform;

GRANT EXECUTE ON FUNCTION zafiro_create_monthly_partitions(text, date, integer) TO zafiro_platform;

-- +goose Down

REVOKE EXECUTE ON FUNCTION zafiro_create_monthly_partitions(text, date, integer) FROM zafiro_platform;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM zafiro_app, zafiro_platform;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE USAGE, SELECT ON SEQUENCES FROM zafiro_app, zafiro_platform;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM zafiro_app, zafiro_platform;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM zafiro_app, zafiro_platform;
REVOKE USAGE ON SCHEMA public FROM zafiro_app, zafiro_platform;

DROP FUNCTION IF EXISTS zafiro_tables_without_rls();

-- +goose StatementBegin
DO $$
DECLARE
    pol record;
BEGIN
    FOR pol IN
        SELECT p.policyname, p.tablename FROM pg_policies p
        WHERE p.schemaname = 'public'
          AND p.policyname IN ('tenant_isolation', 'platform_access', 'member_self_read',
                               'tenant_read', 'tenant_insert', 'tenant_update', 'tenant_delete')
    LOOP
        EXECUTE format('DROP POLICY %I ON %I', pol.policyname, pol.tablename);
    END LOOP;
    FOR pol IN
        SELECT c.relname AS tablename FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
        WHERE c.relkind IN ('r', 'p') AND (c.relrowsecurity OR c.relforcerowsecurity)
    LOOP
        EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', pol.tablename);
        EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', pol.tablename);
    END LOOP;
END
$$;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS zafiro_current_user_id();
DROP FUNCTION IF EXISTS zafiro_current_club_id();

-- Роли общие для кластера: удаляем, только если они больше нигде не используются.
-- +goose StatementBegin
DO $$
BEGIN
    BEGIN
        DROP ROLE IF EXISTS zafiro_app;
    EXCEPTION WHEN dependent_objects_still_exist THEN
        RAISE NOTICE 'role zafiro_app is still used elsewhere, kept';
    END;
    BEGIN
        DROP ROLE IF EXISTS zafiro_platform;
    EXCEPTION WHEN dependent_objects_still_exist THEN
        RAISE NOTICE 'role zafiro_platform is still used elsewhere, kept';
    END;
END
$$;
-- +goose StatementEnd
