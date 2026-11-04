-- 0005_cash_payment_method: способ оплаты в кассовой операции.
-- Допущение Этапа 1: Q-013 — колонка nullable, в остатке смены и X/Z-отчётах не используется
-- (как в старом коде: CashRegisterService::topUpBalance() пишет способ только в текст description и журнал).
-- Старые строки при переносе — NULL. Тот же способ кладётся в ledger_transactions.meta операции top_up.

-- +goose Up
ALTER TABLE cash_transactions ADD COLUMN payment_method varchar(8);
ALTER TABLE cash_transactions ADD CONSTRAINT cash_transactions_payment_method_check
    CHECK (payment_method IN ('cash', 'card', 'qr'));
COMMENT ON COLUMN cash_transactions.payment_method IS 'cash/card/qr (Q-013); NULL — не указан (POS-продажа, старые данные). В остатке смены не разделяется';

-- +goose Down
ALTER TABLE cash_transactions DROP CONSTRAINT IF EXISTS cash_transactions_payment_method_check;
ALTER TABLE cash_transactions DROP COLUMN IF EXISTS payment_method;
