-- 0007_cash_service: поля кассы для сервисного слоя (server/internal/cash/service).
--
-- 1. cash_transactions.income_kind — вид прихода для X/Z-отчёта (BR-057, BR-160, OQ-BR-04; допущение Этапа 1: Q-007).
--    В старом коде вид определялся по журналу действий: приход — «Пополнение балансов», если у него есть запись
--    cash.income с details.balance_type (CashRegisterService::topUpBalance()), и «Гостевые сессии» — с details.computers
--    (GuestSessions::sell()) — app/Services/ShiftReport.php for() ($logged). Здесь — поле операции.
--    NULL — продажа POS (определяется по description = 'Продажа POS', как в старом коде) или прочий приход.
--    При переносе данных: top_up — у приходов с журналом cash.income+balance_type, guest_session — с computers.
-- 2. cash_transactions.idempotency_key — ключ запроса кассы (продажа, пополнение, приход/расход, гостевая сессия):
--    повтор запроса возвращает прежнюю операцию, а не проводит её второй раз (CLAUDE.md: движения денег — с ключом
--    идемпотентности). Ключ ledger-операции тот же. NULL — старые данные и возвраты (их ключ — refund_of_id).
-- 3. cash_shifts.z_report_* — печать Z-отчёта перед закрытием смены в веб-панели (BR-056, Q-015, допущение: в БД).
--    Старый код держал nonce и флаг печати в сессии Laravel (CashRegisterController::zReport(),
--    markZReportPrinted(), close(); ~стр. 137–187). Здесь: sha256 одноразового nonce последней выдачи Z-отчёта
--    и отметка печати (кто и когда). Приложение сотрудника закрывает смену без печати (Api/Staff/CashController::close()).

-- +goose Up
ALTER TABLE cash_transactions ADD COLUMN income_kind varchar(16);
ALTER TABLE cash_transactions ADD CONSTRAINT cash_transactions_income_kind_check
    CHECK (income_kind IN ('top_up', 'guest_session'));
ALTER TABLE cash_transactions ADD CONSTRAINT cash_transactions_income_kind_income_check
    CHECK (income_kind IS NULL OR type = 'income');
COMMENT ON COLUMN cash_transactions.income_kind IS 'Вид прихода для X/Z (OQ-BR-04, Q-007): top_up, guest_session; NULL — продажа POS или прочий приход';

ALTER TABLE cash_transactions ADD COLUMN idempotency_key varchar(200);
ALTER TABLE cash_transactions ADD CONSTRAINT cash_transactions_club_id_idempotency_key_unique
    UNIQUE (club_id, idempotency_key);
COMMENT ON COLUMN cash_transactions.idempotency_key IS 'Ключ запроса кассы; тот же ключ у ledger-операции. NULL — старые данные, возвраты';

-- Итоги и отчёт смены: SUM/COUNT по (club_id, cash_shift_id, type) без чтения строк таблицы.
CREATE INDEX cash_transactions_club_shift_type_index
    ON cash_transactions (club_id, cash_shift_id, type) INCLUDE (amount, user_id, income_kind);

ALTER TABLE cash_shifts ADD COLUMN z_report_nonce_hash bytea;
ALTER TABLE cash_shifts ADD COLUMN z_report_printed_at timestamptz;
ALTER TABLE cash_shifts ADD COLUMN z_report_printed_by bigint;
ALTER TABLE cash_shifts ADD CONSTRAINT cash_shifts_z_report_printed_by_membership_fk
    FOREIGN KEY (club_id, z_report_printed_by) REFERENCES club_memberships (club_id, user_id);
COMMENT ON COLUMN cash_shifts.z_report_nonce_hash IS 'sha256 одноразового nonce последнего показа Z-отчёта (Q-015); сам nonce не хранится';
COMMENT ON COLUMN cash_shifts.z_report_printed_at IS 'Z-отчёт напечатан (afterprint + nonce): без этого веб-панель смену не закрывает (BR-056, Q-015)';

-- +goose Down
ALTER TABLE cash_shifts DROP CONSTRAINT IF EXISTS cash_shifts_z_report_printed_by_membership_fk;
ALTER TABLE cash_shifts DROP COLUMN IF EXISTS z_report_printed_by;
ALTER TABLE cash_shifts DROP COLUMN IF EXISTS z_report_printed_at;
ALTER TABLE cash_shifts DROP COLUMN IF EXISTS z_report_nonce_hash;
DROP INDEX IF EXISTS cash_transactions_club_shift_type_index;
ALTER TABLE cash_transactions DROP CONSTRAINT IF EXISTS cash_transactions_club_id_idempotency_key_unique;
ALTER TABLE cash_transactions DROP COLUMN IF EXISTS idempotency_key;
ALTER TABLE cash_transactions DROP CONSTRAINT IF EXISTS cash_transactions_income_kind_income_check;
ALTER TABLE cash_transactions DROP CONSTRAINT IF EXISTS cash_transactions_income_kind_check;
ALTER TABLE cash_transactions DROP COLUMN IF EXISTS income_kind;
