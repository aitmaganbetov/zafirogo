-- Этап 6, ревью безопасности (docs/security-review.md SEC-005): наименьшие права пула клуба zafiro_app на
-- платформенных таблицах без RLS (docs/schema-changes.md §10.8). RLS — второй рубеж тенантности, но users, clubs,
-- platform_settings, saas_plans и platform_logs им не покрыты: ошибка в любом запросе пула клуба могла бы выдать
-- права суперадмина (users.is_superadmin), разблокировать аккаунт, подменить релиз агента (platform_settings) или
-- стереть журнал платформы. Здесь пул клуба теряет то, что ему не нужно; кабинет суперадмина и фоновые задачи
-- работают пулом zafiro_platform (права не меняются).
--
-- Что остаётся у zafiro_app:
--  * users: SELECT; INSERT и UPDATE — только профиль и пароль (регистрация на ПК, клиенты панели, смена пароля);
--    is_superadmin и blocked_at меняет только zafiro_platform (суперадминка); DELETE — нет;
--  * clubs: SELECT; UPDATE — только строку своего клуба (RLS own_club_update по app.club_id, раньше — только
--    WHERE id = клуб из сессии в запросе); INSERT/DELETE — только платформа (регистрация, удаление суперадмином);
--  * platform_settings, saas_plans: только чтение (релиз агента, лендинг, тарифы платформы);
--  * platform_logs: чтение и добавление (журнал попыток входа, PD-13), без изменения и удаления;
--  * goose_db_version: ничего (служебная таблица миграций).

-- +goose Up
-- goose_db_version — таблица goose (её нет, если миграции применяет другой инструмент, как dbtest).
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('goose_db_version') IS NOT NULL THEN
        REVOKE ALL ON goose_db_version FROM zafiro_app, zafiro_platform;
    END IF;
END
$$;
-- +goose StatementEnd

REVOKE INSERT, UPDATE, DELETE ON users FROM zafiro_app;
GRANT INSERT (name, phone, email, email_verified_at, password, remember_token, created_at, updated_at) ON users TO zafiro_app;
GRANT UPDATE (name, phone, email, email_verified_at, password, remember_token, updated_at) ON users TO zafiro_app;

REVOKE INSERT, DELETE ON clubs FROM zafiro_app;
-- clubs: пул клуба меняет только строку своего клуба (app.club_id транзакции, db.InTenantTx); читают все
-- (вход, выбор клуба, лендинг). Без FORCE: владелец схемы (миграции, zafiro_purge_imported_club) не ограничен.
ALTER TABLE clubs ENABLE ROW LEVEL SECURITY;
CREATE POLICY club_read ON clubs FOR SELECT USING (true);
CREATE POLICY own_club_update ON clubs FOR UPDATE TO zafiro_app
    USING (id = zafiro_current_club_id()) WITH CHECK (id = zafiro_current_club_id());
CREATE POLICY platform_access ON clubs TO zafiro_platform USING (true) WITH CHECK (true);

REVOKE INSERT, UPDATE, DELETE ON platform_settings, saas_plans FROM zafiro_app;

REVOKE UPDATE, DELETE ON platform_logs FROM zafiro_app;

-- +goose Down
DROP POLICY platform_access ON clubs;
DROP POLICY own_club_update ON clubs;
DROP POLICY club_read ON clubs;
ALTER TABLE clubs DISABLE ROW LEVEL SECURITY;
GRANT SELECT, INSERT, UPDATE, DELETE ON platform_logs, platform_settings, saas_plans, clubs, users TO zafiro_app;
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('goose_db_version') IS NOT NULL THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON goose_db_version TO zafiro_app, zafiro_platform;
    END IF;
END
$$;
-- +goose StatementEnd
