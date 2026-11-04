-- 0003_audit: аудит-журнал (только добавление, партиции по месяцам). docs/schema-changes.md §5.
--
-- Что пишем (category):
--  * money      — движения денег: пополнение/списание/возврат/перевод/корректировка; ссылка на ledger_transaction_id;
--  * tariff     — создание/изменение/удаление тарифа, цены зон и услуг;
--  * permission — права и роли сотрудников, приглашения, ключи API, PIN агента;
--  * settings   — финансовые настройки клуба (бонусы, лояльность, правила оплаты);
--  * security   — вход сотрудника, сброс пароля, отзыв сертификата ПК.
-- Без секретов (пароли, токены, ключи, коды) и без персональных данных сверх id.
-- activity_logs остаётся журналом интерфейса (история клиента, отчёты) — это другое.
-- Нет FK на clubs/users: аудит переживает любые удаления и не мешает им.

-- +goose Up

CREATE TABLE audit_log (
    id                    bigint GENERATED ALWAYS AS IDENTITY,
    club_id               bigint,
    actor_user_id         bigint,
    actor_kind            varchar(16) NOT NULL,
    category              varchar(16) NOT NULL,
    action                varchar(64) NOT NULL,
    subject_type          varchar(64),
    subject_id            bigint,
    ledger_transaction_id bigint,
    before                jsonb,
    after                 jsonb,
    ip                    inet,
    request_id            varchar(64),
    hash                  bytea,
    created_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT audit_log_pkey PRIMARY KEY (id, created_at),
    CONSTRAINT audit_log_actor_kind_check CHECK (actor_kind IN ('staff', 'guest', 'agent', 'api_key', 'superadmin', 'system')),
    CONSTRAINT audit_log_category_check CHECK (category IN ('money', 'tariff', 'permission', 'settings', 'security')),
    CONSTRAINT audit_log_action_check CHECK (action ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$')
) PARTITION BY RANGE (created_at);
CREATE INDEX audit_log_club_id_created_at_index ON audit_log (club_id, created_at);
CREATE INDEX audit_log_club_id_category_created_at_index ON audit_log (club_id, category, created_at);
CREATE INDEX audit_log_club_subject_index ON audit_log (club_id, subject_type, subject_id, created_at);
COMMENT ON TABLE audit_log IS 'Аудит действий с деньгами, тарифами, правами (только добавление). club_id NULL — событие платформы';
COMMENT ON COLUMN audit_log.hash IS 'Зарезервировано под хэш-цепочку (Q-053), пока не заполняется';

CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION zafiro_forbid_change();

SELECT zafiro_create_monthly_partitions('audit_log', (now() AT TIME ZONE 'UTC')::date, 4);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
