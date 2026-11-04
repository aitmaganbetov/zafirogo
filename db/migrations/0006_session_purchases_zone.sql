-- 0006_session_purchases_zone: данные сервисного слоя покупок и сессий (server/internal/billing/service).
--
--  1. session_purchases.zone_id / source — откуда взялась часть сессии (предложение S1-SES-02,
--     docs/open-questions/stage1-session.md). Старый SeatTransfers::allowedZoneIds() (app/Services/SeatTransfers.php
--     ~стр. 135–152) брал зону пересадки из журнала действий (tariff.purchased | time.purchased, запуск пакета
--     time.package_started не учитывался). Допущение Этапа 1: Q-007/OQ-BR-04 — источник теперь сама часть сессии.
--     Старые строки — NULL (перенос данных заполняет из activity_logs; см. open-questions/stage1-service.md SV-3).
--  2. tariff_sales — счётчик продаж тарифа в агенте для лимита количества (BR-016). Допущение Этапа 1: Q-007 —
--     старый purchaseCount() считал записи журнала tariff.purchased; кассовые (гостевые) продажи их не писали и
--     лимит не расходуют. Счётчик меняется под блокировкой строки тарифа (FOR UPDATE, как в purchase()).
--  3. billing_requests — идемпотентность запросов покупки/продажи времени: ключ клиента захватывается до
--     блокировок, повтор с тем же телом получает прежний ответ (даже если сессия уже завершена), с другим — ошибку.
--     Отдельно от ledger_idempotency: покупка за 0 тиынов (скидка 100 %, GAP-011) проводок не создаёт.
--  4. Частичный индекс для фоновой задачи истечения сессий (session_ends_at <= now() по всем клубам раз в минуту).

-- +goose Up

ALTER TABLE session_purchases ADD COLUMN zone_id bigint;
ALTER TABLE session_purchases ADD COLUMN source varchar(8);
ALTER TABLE session_purchases ADD CONSTRAINT session_purchases_source_check
    CHECK (source IN ('tariff', 'minutes', 'package'));
COMMENT ON COLUMN session_purchases.zone_id IS 'Зона ПК в момент покупки (для минут — зона цены часа). NULL — данные до 0006. Без FK: как subject_id журнала';
COMMENT ON COLUMN session_purchases.source IS 'tariff — покупка тарифа, minutes — поминутно, package — запуск сохранённого пакета (не влияет на зоны пересадки). NULL — данные до 0006';

CREATE TABLE tariff_sales (
    club_id    bigint NOT NULL,
    tariff_id  bigint NOT NULL,
    sold       bigint NOT NULL DEFAULT 0 CHECK (sold >= 0),
    updated_at timestamptz NOT NULL,
    CONSTRAINT tariff_sales_pkey PRIMARY KEY (club_id, tariff_id),
    CONSTRAINT tariff_sales_tariff_fk FOREIGN KEY (club_id, tariff_id)
        REFERENCES tariffs (club_id, id) ON DELETE CASCADE
);
COMMENT ON TABLE tariff_sales IS 'Продажи тарифа в агенте (лимит количества, BR-016, Q-007). Кассовые гостевые продажи не считаются';

CREATE TABLE billing_requests (
    club_id         bigint NOT NULL,
    idempotency_key varchar(200) NOT NULL,
    operation       varchar(32) NOT NULL,
    fingerprint     bytea NOT NULL,
    response        jsonb,
    created_at      timestamptz NOT NULL,
    CONSTRAINT billing_requests_pkey PRIMARY KEY (club_id, idempotency_key),
    CONSTRAINT billing_requests_club_fk FOREIGN KEY (club_id) REFERENCES clubs (id) ON DELETE CASCADE
);
CREATE INDEX billing_requests_club_id_created_at_index ON billing_requests (club_id, created_at);
COMMENT ON TABLE billing_requests IS 'Ключи идемпотентности покупок/продаж времени: отпечаток тела и ответ первого успешного выполнения';

CREATE INDEX club_computers_session_ends_at_index ON club_computers (session_ends_at)
    WHERE session_ends_at IS NOT NULL;

-- RLS как у остальных тенантных таблиц (0004_rls.sql); права роли получают через ALTER DEFAULT PRIVILEGES.
ALTER TABLE tariff_sales ENABLE ROW LEVEL SECURITY;
ALTER TABLE tariff_sales FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tariff_sales USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON tariff_sales TO zafiro_platform USING (true) WITH CHECK (true);

ALTER TABLE billing_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON billing_requests USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON billing_requests TO zafiro_platform USING (true) WITH CHECK (true);

-- +goose Down

DROP INDEX IF EXISTS club_computers_session_ends_at_index;
DROP TABLE IF EXISTS billing_requests;
DROP TABLE IF EXISTS tariff_sales;
ALTER TABLE session_purchases DROP CONSTRAINT IF EXISTS session_purchases_source_check;
ALTER TABLE session_purchases DROP COLUMN IF EXISTS source;
ALTER TABLE session_purchases DROP COLUMN IF EXISTS zone_id;
