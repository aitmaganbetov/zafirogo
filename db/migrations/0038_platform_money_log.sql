-- 0038_platform_money_log: журнал денег платформы — только добавление (решение UQ-6, docs/upstream-2026-11.md §3.5,
-- §7 — рекомендация принята). Новая таблица, в старом Laravel её нет.
--
-- Оплата счёта за подписку, начисление реселлеру и выплата реселлеру — деньги платформы, а не клуба: в клубный
-- ledger (счета клиентов, касса и выручка клуба) они не ложатся. Правило «все движения денег — через журнал с
-- ключом идемпотентности» для них выполняет эта таблица: каждое движение — одна строка, которую нельзя ни
-- исправить, ни удалить (триггеры zafiro_forbid_change, как у ledger_*, и права только SELECT/INSERT).
--
-- Документы (subscription_invoices, reseller_payouts) остаются источником для экранов: «заработано», «выплачено»
-- и «к выплате» считаются по ним (BR-187), как в старом. Журнал — неизменяемая история: он хранит суммы и после
-- удаления реселлера (его выплаты удаляются каскадом, BR-189, UQ-10) или клуба — поэтому ссылки без внешних ключей.
--
-- Виды и ключи идемпотентности (одна строка на событие; повтор ключа — уникальный индекс):
--   invoice_paid     — клуб оплатил счёт: amount = сумма счёта;            ключ invoice_paid:<id счёта>
--   reseller_accrual — реселлеру начислен процент с оплаты: amount = reseller_amount счёта;
--                                                                         ключ reseller_accrual:<id счёта>
--   reseller_payout  — реселлеру выплачено: amount = сумма выплаты;        ключ reseller_payout:<id выплаты>
-- Суммы — тиыны, всегда ≥ 0; направление задаёт вид. Запись — в той же транзакции, что и изменение документа.
--
-- Партиций нет: журнал растёт на 1–2 строки с оплаты счёта (тысячи строк в год на всю платформу), хранится вечно.
-- Тенантность: есть club_id → RLS включён и принудителен (как у всех таблиц с club_id, 0004), но пулу клуба
-- zafiro_app таблица не видна совсем (права отозваны); пишет и читает только zafiro_platform (суперадминка,
-- кабинет реселлера, перенос). Политика tenant_isolation — для владельца схемы: zafiro_purge_imported_club (0027)
-- удаляет строки не открытого перенесённого клуба (их записал перенос) вместе с остальными его данными.

-- +goose Up
CREATE TABLE platform_money_log (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind            varchar(32)  NOT NULL,
    idempotency_key varchar(200) NOT NULL,
    amount          bigint       NOT NULL,
    club_id         bigint,
    invoice_id      bigint,
    reseller_id     bigint,
    payout_id       bigint,
    actor_user_id   bigint,
    meta            jsonb,
    created_at      timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT platform_money_log_idempotency_key_unique UNIQUE (idempotency_key),
    CONSTRAINT platform_money_log_kind_check CHECK (kind IN ('invoice_paid', 'reseller_accrual', 'reseller_payout')),
    CONSTRAINT platform_money_log_key_check CHECK (length(idempotency_key) > 0),
    CONSTRAINT platform_money_log_amount_check CHECK (amount >= 0),
    CONSTRAINT platform_money_log_refs_check CHECK (CASE kind
        WHEN 'invoice_paid' THEN club_id IS NOT NULL AND invoice_id IS NOT NULL AND payout_id IS NULL
        WHEN 'reseller_accrual' THEN club_id IS NOT NULL AND invoice_id IS NOT NULL AND reseller_id IS NOT NULL
                                     AND payout_id IS NULL
        WHEN 'reseller_payout' THEN reseller_id IS NOT NULL AND payout_id IS NOT NULL AND club_id IS NULL
                                    AND invoice_id IS NULL AND amount > 0
    END)
);
CREATE INDEX platform_money_log_created_at_index ON platform_money_log (created_at);
CREATE INDEX platform_money_log_club_id_created_at_index ON platform_money_log (club_id, created_at) WHERE club_id IS NOT NULL;
CREATE INDEX platform_money_log_reseller_id_created_at_index ON platform_money_log (reseller_id, created_at)
    WHERE reseller_id IS NOT NULL;
CREATE INDEX platform_money_log_invoice_id_index ON platform_money_log (invoice_id) WHERE invoice_id IS NOT NULL;

COMMENT ON TABLE platform_money_log IS
    'Журнал денег платформы (только добавление, UQ-6): оплаты счетов за подписку, начисления и выплаты реселлерам. Суммы — тиыны. Ссылки без внешних ключей: строки остаются после удаления клуба и реселлера';
COMMENT ON COLUMN platform_money_log.kind IS
    'invoice_paid — клуб оплатил счёт; reseller_accrual — начислено реселлеру с оплаты; reseller_payout — выплачено реселлеру';
COMMENT ON COLUMN platform_money_log.idempotency_key IS
    'invoice_paid:<id счёта>, reseller_accrual:<id счёта>, reseller_payout:<id выплаты>';
COMMENT ON COLUMN platform_money_log.amount IS 'Сумма в тиынах, ≥ 0 (направление — по виду)';
COMMENT ON COLUMN platform_money_log.club_id IS 'Клуб счёта (clubs.id); у выплаты — NULL';
COMMENT ON COLUMN platform_money_log.invoice_id IS 'subscription_invoices.id';
COMMENT ON COLUMN platform_money_log.reseller_id IS 'resellers.id (у invoice_paid — реселлер счёта, если был)';
COMMENT ON COLUMN platform_money_log.payout_id IS 'reseller_payouts.id';
COMMENT ON COLUMN platform_money_log.actor_user_id IS 'Суперадмин, отметивший оплату или записавший выплату (users.id); NULL — перенос из MySQL';
COMMENT ON COLUMN platform_money_log.meta IS 'Снимок для чтения человеком: номер счёта, месяцы, процент в базисных пунктах, {"source": "mysql"} у перенесённых';

CREATE TRIGGER platform_money_log_append_only BEFORE UPDATE OR DELETE ON platform_money_log
    FOR EACH ROW EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER platform_money_log_no_truncate BEFORE TRUNCATE ON platform_money_log
    FOR EACH STATEMENT EXECUTE FUNCTION zafiro_forbid_change();

ALTER TABLE platform_money_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform_money_log FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON platform_money_log USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON platform_money_log TO zafiro_platform USING (true) WITH CHECK (true);

REVOKE ALL ON platform_money_log FROM zafiro_app, zafiro_platform;
GRANT SELECT, INSERT ON platform_money_log TO zafiro_platform;
REVOKE ALL ON SEQUENCE platform_money_log_id_seq FROM zafiro_app;

-- +goose Down
DROP TABLE IF EXISTS platform_money_log;
