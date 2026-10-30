-- 0014_stage1_decisions: схема по решениям Этапа 1 (docs/DECISIONS.md, docs/open-questions/stage1-*.md).
--
-- 1. CSQ-1 — вид ledger-операции cash_shift_opening: выравнивание кассы при открытии смены (разница начального
--    остатка смены и остатка cash_drawer по журналу) больше не смешивается с прочим приходом/расходом
--    (cash_income/cash_expense с meta.reason = "shift_opening" — так проводилось до этой миграции).
-- 2. CSQ-6 — индекс «ledger-операция по cash_transaction_id»: возврат продажи (pos_refund) и пополнения
--    (top_up_refund) ссылается на исходную операцию (reverses_transaction_id).
-- 3. CSQ-7 — снимок остатка кассы при закрытии смены: последняя учтённая проводка cash_drawer и остаток после неё.
--    Остаток при открытии следующей смены = снимок + проводки после снимка (а не SUM по всем проводкам за годы).
--    NULL — снимка нет (перенесённые смены): остаток считается полной суммой.
-- 4. CSQ-9, PD-6 — cash_transactions.description → text: описание гостевой сессии со всеми ПК (в старом
--    varchar(255) — ошибка MySQL и несостоявшаяся продажа). Форма «Приход/расход» по-прежнему max:255.
-- 5. PQ-4 — цены тарифа и зоны в пределах формы старого кода: 0…9 999 999 999.99 ₸ (StoreTariffRequest max),
--    в тиынах 0…999 999 999 999 (pricing.MaxPrice).

-- +goose Up
ALTER TABLE ledger_transactions DROP CONSTRAINT ledger_transactions_kind_check;
ALTER TABLE ledger_transactions ADD CONSTRAINT ledger_transactions_kind_check CHECK (kind IN (
    'top_up', 'top_up_refund', 'owner_top_up', 'owner_write_off', 'initial_balance', 'friend_transfer',
    'tariff_purchase', 'minutes_purchase', 'package_start', 'session_refund', 'package_kept', 'package_expired',
    'shop_order', 'shop_order_cancel', 'pos_sale', 'pos_refund', 'cash_income', 'cash_expense', 'guest_session',
    'migration_opening_balance',
    'cash_shift_opening'          -- выравнивание кассы при открытии смены (CSQ-1)
));

CREATE INDEX ledger_transactions_club_id_cash_transaction_id_index
    ON ledger_transactions (club_id, cash_transaction_id) WHERE cash_transaction_id IS NOT NULL;

ALTER TABLE cash_shifts ADD COLUMN drawer_snapshot_entry_id bigint;
ALTER TABLE cash_shifts ADD COLUMN drawer_snapshot_balance bigint;
ALTER TABLE cash_shifts ADD COLUMN drawer_snapshot_at timestamptz;
ALTER TABLE cash_shifts ADD CONSTRAINT cash_shifts_drawer_snapshot_check
    CHECK ((drawer_snapshot_entry_id IS NULL) = (drawer_snapshot_balance IS NULL)
           AND (drawer_snapshot_entry_id IS NULL) = (drawer_snapshot_at IS NULL)
           AND (drawer_snapshot_entry_id IS NULL OR status = 'closed'));
CREATE INDEX cash_shifts_club_id_drawer_snapshot_index
    ON cash_shifts (club_id, id) WHERE drawer_snapshot_entry_id IS NOT NULL;
COMMENT ON COLUMN cash_shifts.drawer_snapshot_entry_id IS 'CSQ-7: id последней проводки cash_drawer, учтённой при закрытии (0 — проводок не было); NULL — снимка нет';
COMMENT ON COLUMN cash_shifts.drawer_snapshot_balance IS 'CSQ-7: остаток cash_drawer по журналу (тиыны) после проводки drawer_snapshot_entry_id';
COMMENT ON COLUMN cash_shifts.drawer_snapshot_at IS 'CSQ-7: время БД (now() транзакции закрытия) — нижняя граница партиций для проводок после снимка';

ALTER TABLE cash_transactions ALTER COLUMN description TYPE text;

ALTER TABLE tariffs ADD CONSTRAINT tariffs_price_check CHECK (price BETWEEN 0 AND 999999999999);
ALTER TABLE club_zones ADD CONSTRAINT club_zones_price_per_hour_check CHECK (price_per_hour BETWEEN 0 AND 999999999999);

-- +goose Down
ALTER TABLE club_zones DROP CONSTRAINT IF EXISTS club_zones_price_per_hour_check;
ALTER TABLE tariffs DROP CONSTRAINT IF EXISTS tariffs_price_check;

-- Длинные описания (только гостевые сессии) обрезаются до прежних 255 символов.
ALTER TABLE cash_transactions ALTER COLUMN description TYPE varchar(255) USING left(description, 255);

DROP INDEX IF EXISTS cash_shifts_club_id_drawer_snapshot_index;
ALTER TABLE cash_shifts DROP CONSTRAINT IF EXISTS cash_shifts_drawer_snapshot_check;
ALTER TABLE cash_shifts DROP COLUMN IF EXISTS drawer_snapshot_at;
ALTER TABLE cash_shifts DROP COLUMN IF EXISTS drawer_snapshot_balance;
ALTER TABLE cash_shifts DROP COLUMN IF EXISTS drawer_snapshot_entry_id;

DROP INDEX IF EXISTS ledger_transactions_club_id_cash_transaction_id_index;

-- Журнал только добавляется: если операции cash_shift_opening уже есть, откат прерывается на CHECK
-- (их нельзя переписать в cash_income/cash_expense задним числом).
ALTER TABLE ledger_transactions DROP CONSTRAINT ledger_transactions_kind_check;
ALTER TABLE ledger_transactions ADD CONSTRAINT ledger_transactions_kind_check CHECK (kind IN (
    'top_up', 'top_up_refund', 'owner_top_up', 'owner_write_off', 'initial_balance', 'friend_transfer',
    'tariff_purchase', 'minutes_purchase', 'package_start', 'session_refund', 'package_kept', 'package_expired',
    'shop_order', 'shop_order_cancel', 'pos_sale', 'pos_refund', 'cash_income', 'cash_expense', 'guest_session',
    'migration_opening_balance'
));
