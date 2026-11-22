-- 0045_review_grants_guards: замечания полного ревью 22.11.2026 к миграциям 0034–0038 и к правам пула клуба
-- (docs/schema-changes.md §19, docs/DECISIONS.md «ревью 22.11.2026»). Прежние миграции уже применены на dev и
-- staging, поэтому правки — отдельной миграцией. Схема данных не меняется: только права, политика, триггер и индексы.
--
--  1. Пересечение броней с интервалом (SeatMapBlockedComputers) — индекс уже создан миграцией 0044
--     (computer_reservations_club_id_pending_ends_at_index, частичный по ожидающим броням); второй такой же здесь
--     не создаётся. Замер и выбор — docs/schema-changes.md §19.
--  2. subscription_invoices, вставка пулом клуба: снимок счёта нельзя подделать — реселлер и его процент равны
--     действующему реселлеру своего клуба (или пусты, если его нет), цена — цене включённого тарифа, скидка не
--     больше большей из скидки клуба и скидки реселлера. Сумму (amount) и число ПК база не проверяет: формула
--     суммы живёт в одном месте — server/internal/subscription (BR-180), а число ПК меняет сам клуб; оплату по
--     сумме подтверждает суперадмин.
--  3. resellers, subscription_invoices: чтение пулом клуба — по колонкам. Клубу не видны аккаунт реселлера
--     (user_id) и в счёте — снимок процента, начисление реселлеру, кто выставил и кто подтвердил оплату.
--     resellers.commission_bp оставлен: снимок процента в счёт пишет транзакция клуба (SubscriptionClubReseller),
--     и его же сверяет политика вставки (п. 2); в ответы панели клуба процент не попадает (ResellerOfClub).
--  4. club_photos, club_photo_images: пул платформы только читает (каталог, публичная отдача фото) и добавляет;
--     менять и удалять фото — дело клуба (и каскада при удалении клуба).
--  5. platform_money_log: сервер журнал только пишет (читает его сверка переноса — разово, по таблице в тысячи
--     строк), поэтому четыре индекса чтения сняты; остаётся первичный ключ и уникальный ключ идемпотентности.
--  6. clubs.migration_state и clubs.slug меняет только пул платформы (открытие перенесённого клуба — инструмент
--     переноса; адрес клуба панель клуба не меняет): у пула клуба UPDATE на всю строку своего клуба (0028), и до
--     этой миграции он мог сам «открыть» перенесённый клуб (imported → live) в обход go-live и сменить свой slug.

-- +goose Up
-- Счёт выставляет клуб, но его снимок — деньги платформы. Подзапросы выполняются с правами и RLS пула клуба:
-- resellers он видит только своего клуба (club_reseller_read, 0034), club_subscriptions — только свою (0039).
ALTER POLICY app_insert_pending ON subscription_invoices WITH CHECK (
    status = 'pending'
    AND price_per_computer = (SELECT p.price_monthly FROM saas_plans p
                              WHERE p.id = subscription_invoices.saas_plan_id AND p.is_active)
    AND reseller_id IS NOT DISTINCT FROM (SELECT r.id FROM clubs c JOIN resellers r ON r.id = c.reseller_id
                                          WHERE c.id = subscription_invoices.club_id AND r.is_active)
    AND reseller_bp IS NOT DISTINCT FROM (SELECT r.commission_bp FROM clubs c JOIN resellers r ON r.id = c.reseller_id
                                          WHERE c.id = subscription_invoices.club_id AND r.is_active)
    AND discount_percent <= GREATEST(
        COALESCE((SELECT s.discount_percent FROM club_subscriptions s WHERE s.club_id = subscription_invoices.club_id), 0),
        COALESCE((SELECT r.discount_percent FROM clubs c JOIN resellers r ON r.id = c.reseller_id
                  WHERE c.id = subscription_invoices.club_id AND r.is_active), 0))
);

REVOKE SELECT ON resellers, subscription_invoices FROM zafiro_app;
GRANT SELECT (id, code, name, commission_bp, discount_percent, is_active) ON resellers TO zafiro_app;
GRANT SELECT (id, club_id, saas_plan_id, months, computers, price_per_computer, discount_percent, amount, status,
              created_at, paid_at, period_ends_at) ON subscription_invoices TO zafiro_app;

REVOKE UPDATE, DELETE ON club_photos, club_photo_images FROM zafiro_platform;

DROP INDEX IF EXISTS platform_money_log_created_at_index;
DROP INDEX IF EXISTS platform_money_log_club_id_created_at_index;
DROP INDEX IF EXISTS platform_money_log_reseller_id_created_at_index;
DROP INDEX IF EXISTS platform_money_log_invoice_id_index;

-- Как clubs_reseller_guard (0034): менять колонки может роль с членством в zafiro_platform и владелец схемы
-- (миграции, перенос); роль только с zafiro_app — нет. Переход live → imported по-прежнему запрещён всем
-- (clubs_migration_state_guard, 0027).
-- +goose StatementBegin
CREATE FUNCTION zafiro_clubs_platform_columns_guard() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.migration_state IS DISTINCT FROM OLD.migration_state OR NEW.slug IS DISTINCT FROM OLD.slug)
       AND pg_has_role(current_user, 'zafiro_app', 'MEMBER')
       AND NOT pg_has_role(current_user, 'zafiro_platform', 'MEMBER') THEN
        RAISE EXCEPTION 'clubs.migration_state, clubs.slug: only zafiro_platform changes them'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER clubs_platform_columns_guard BEFORE UPDATE OF migration_state, slug ON clubs
    FOR EACH ROW EXECUTE FUNCTION zafiro_clubs_platform_columns_guard();
COMMENT ON COLUMN clubs.slug IS 'Адрес клуба; меняет только пул платформы (clubs_platform_columns_guard)';

-- +goose Down
COMMENT ON COLUMN clubs.slug IS NULL;
DROP TRIGGER IF EXISTS clubs_platform_columns_guard ON clubs;
DROP FUNCTION IF EXISTS zafiro_clubs_platform_columns_guard();
CREATE INDEX IF NOT EXISTS platform_money_log_created_at_index ON platform_money_log (created_at);
CREATE INDEX IF NOT EXISTS platform_money_log_club_id_created_at_index ON platform_money_log (club_id, created_at)
    WHERE club_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS platform_money_log_reseller_id_created_at_index ON platform_money_log (reseller_id, created_at)
    WHERE reseller_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS platform_money_log_invoice_id_index ON platform_money_log (invoice_id) WHERE invoice_id IS NOT NULL;
GRANT UPDATE, DELETE ON club_photos, club_photo_images TO zafiro_platform;
REVOKE SELECT (id, code, name, commission_bp, discount_percent, is_active) ON resellers FROM zafiro_app;
REVOKE SELECT (id, club_id, saas_plan_id, months, computers, price_per_computer, discount_percent, amount, status,
               created_at, paid_at, period_ends_at) ON subscription_invoices FROM zafiro_app;
GRANT SELECT ON resellers, subscription_invoices TO zafiro_app;
ALTER POLICY app_insert_pending ON subscription_invoices WITH CHECK (status = 'pending');
