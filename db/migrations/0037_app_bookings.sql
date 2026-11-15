-- 0037_app_bookings: брони из гостевого приложения (тема Г, docs/upstream-2026-11.md §2 «Г», §3.2 BR-174…BR-179,
-- BR-190, §3.5): оплата брони с балансов гостя, бронь тарифом, код брони на группу ПК; виды операций ledger.
-- Laravel: 2026_11_15_100000_add_app_payment_to_computer_reservations.php,
-- 2026_11_16_100000_add_tariff_to_computer_reservations.php, 2026_11_17_100000_add_code_to_computer_reservations.php.
--
-- Отличия от Laravel:
--  * paid_main_cents / paid_bonus_cents: unsignedInteger → bigint ≥ 0. Это УЖЕ тиыны («центы» старого кода) — при
--    переносе не умножаются на 100. Имена оставлены как в Laravel;
--  * tariff_id — составной внешний ключ (club_id, tariff_id): тариф только своего клуба (как tariff_sales, 0006);
--    при удалении тарифа обнуляется одна колонка tariff_id;
--  * code — CHECK «ровно 6 цифр» (GuestBookings генерирует 6 цифр; колонка — string(8)). Хранится открытым текстом:
--    его повторно видят гость и сотрудник (UQ-14);
--  * оплата, тариф и код бывают только у брони из приложения (CHECK computer_reservations_app_only_check);
--  * индексы (club_id, code) и (club_id, tariff_id) — частичные (у броней с кассы этих значений нет);
--  * ledger (в старом журнала операций нет): виды reservation_payment (оплата брони, одна операция на группу ПК)
--    и reservation_refund (возврат отменённой брони, на каждый ПК) в CHECK ledger_transactions.kind и ссылка
--    ledger_transactions.reservation_id (ledger.Refs.ReservationID; без внешнего ключа, как остальные ссылки
--    журнала на документы).

-- +goose Up
ALTER TABLE computer_reservations ADD COLUMN from_app boolean NOT NULL DEFAULT false;
ALTER TABLE computer_reservations ADD COLUMN paid_main_cents bigint NOT NULL DEFAULT 0;
ALTER TABLE computer_reservations ADD COLUMN paid_bonus_cents bigint NOT NULL DEFAULT 0;
ALTER TABLE computer_reservations ADD COLUMN tariff_id bigint;
ALTER TABLE computer_reservations ADD COLUMN code varchar(8);
ALTER TABLE computer_reservations ADD CONSTRAINT computer_reservations_paid_main_cents_check CHECK (paid_main_cents >= 0);
ALTER TABLE computer_reservations ADD CONSTRAINT computer_reservations_paid_bonus_cents_check CHECK (paid_bonus_cents >= 0);
ALTER TABLE computer_reservations ADD CONSTRAINT computer_reservations_code_check CHECK (code ~ '^[0-9]{6}$');
ALTER TABLE computer_reservations ADD CONSTRAINT computer_reservations_app_only_check
    CHECK (from_app OR (paid_main_cents = 0 AND paid_bonus_cents = 0 AND tariff_id IS NULL AND code IS NULL));
ALTER TABLE computer_reservations ADD CONSTRAINT computer_reservations_tariff_fk FOREIGN KEY (club_id, tariff_id)
    REFERENCES tariffs (club_id, id) ON DELETE SET NULL (tariff_id);
-- Брони гостя в приложении.
CREATE INDEX computer_reservations_club_id_user_id_starts_at_index ON computer_reservations (club_id, user_id, starts_at);
-- Вход по коду брони на ПК и группа ПК одной оплаты.
CREATE INDEX computer_reservations_club_id_code_index ON computer_reservations (club_id, code) WHERE code IS NOT NULL;
-- Лимит количества тарифа с учётом броней (BR-190) и внешний ключ на тариф.
CREATE INDEX computer_reservations_club_id_tariff_id_index ON computer_reservations (club_id, tariff_id)
    WHERE tariff_id IS NOT NULL;
COMMENT ON COLUMN computer_reservations.from_app IS
    'Бронь создана гостем в приложении и оплачена с его балансов (Laravel: 2026_11_15_100000_add_app_payment_to_computer_reservations)';
COMMENT ON COLUMN computer_reservations.paid_main_cents IS
    'Оплачено с основного баланса за этот ПК, тиыны (в Laravel — тоже тиыны: paid_main_cents, без × 100)';
COMMENT ON COLUMN computer_reservations.paid_bonus_cents IS
    'Оплачено с бонусного баланса за этот ПК, тиыны (доля бонусов группы, BR-176; в Laravel — тоже тиыны)';
COMMENT ON COLUMN computer_reservations.tariff_id IS
    'Бронь тарифом, а не по часам: время начнётся как этот тариф и входит в лимит его покупок (Laravel: 2026_11_16_100000_add_tariff_to_computer_reservations)';
COMMENT ON COLUMN computer_reservations.code IS
    'Код брони, 6 цифр, один на ПК, забронированные вместе: ввод на экране входа ПК начинает оплаченное время без аккаунта (Laravel: 2026_11_17_100000_add_code_to_computer_reservations)';

ALTER TABLE ledger_transactions DROP CONSTRAINT ledger_transactions_kind_check;
ALTER TABLE ledger_transactions ADD CONSTRAINT ledger_transactions_kind_check CHECK (kind IN (
    'top_up', 'top_up_refund', 'owner_top_up', 'owner_write_off', 'initial_balance', 'friend_transfer',
    'tariff_purchase', 'minutes_purchase', 'package_start', 'session_refund', 'package_kept', 'package_expired',
    'shop_order', 'shop_order_cancel', 'pos_sale', 'pos_refund', 'cash_income', 'cash_expense', 'guest_session',
    'migration_opening_balance', 'cash_shift_opening',
    'reservation_payment',        -- оплата брони из приложения, одна операция на группу ПК (BR-176)
    'reservation_refund'          -- возврат отменённой брони, на каждый ПК (BR-179)
));
ALTER TABLE ledger_transactions ADD COLUMN reservation_id bigint;
CREATE INDEX ledger_transactions_club_id_reservation_id_index ON ledger_transactions (club_id, reservation_id)
    WHERE reservation_id IS NOT NULL;
COMMENT ON COLUMN ledger_transactions.reservation_id IS
    'computer_reservations.id: оплата брони — первая бронь группы ПК, возврат — отменённая бронь (без внешнего ключа)';

-- +goose Down
DROP INDEX IF EXISTS ledger_transactions_club_id_reservation_id_index;
ALTER TABLE ledger_transactions DROP COLUMN IF EXISTS reservation_id;
-- Журнал только добавляется: если операции reservation_payment / reservation_refund уже есть, откат прерывается
-- на CHECK (как 0014).
ALTER TABLE ledger_transactions DROP CONSTRAINT ledger_transactions_kind_check;
ALTER TABLE ledger_transactions ADD CONSTRAINT ledger_transactions_kind_check CHECK (kind IN (
    'top_up', 'top_up_refund', 'owner_top_up', 'owner_write_off', 'initial_balance', 'friend_transfer',
    'tariff_purchase', 'minutes_purchase', 'package_start', 'session_refund', 'package_kept', 'package_expired',
    'shop_order', 'shop_order_cancel', 'pos_sale', 'pos_refund', 'cash_income', 'cash_expense', 'guest_session',
    'migration_opening_balance', 'cash_shift_opening'
));

DROP INDEX IF EXISTS computer_reservations_club_id_tariff_id_index;
DROP INDEX IF EXISTS computer_reservations_club_id_code_index;
DROP INDEX IF EXISTS computer_reservations_club_id_user_id_starts_at_index;
ALTER TABLE computer_reservations DROP CONSTRAINT IF EXISTS computer_reservations_tariff_fk;
ALTER TABLE computer_reservations DROP CONSTRAINT IF EXISTS computer_reservations_app_only_check;
ALTER TABLE computer_reservations DROP CONSTRAINT IF EXISTS computer_reservations_code_check;
ALTER TABLE computer_reservations DROP CONSTRAINT IF EXISTS computer_reservations_paid_bonus_cents_check;
ALTER TABLE computer_reservations DROP CONSTRAINT IF EXISTS computer_reservations_paid_main_cents_check;
ALTER TABLE computer_reservations DROP COLUMN IF EXISTS code;
ALTER TABLE computer_reservations DROP COLUMN IF EXISTS tariff_id;
ALTER TABLE computer_reservations DROP COLUMN IF EXISTS paid_bonus_cents;
ALTER TABLE computer_reservations DROP COLUMN IF EXISTS paid_main_cents;
ALTER TABLE computer_reservations DROP COLUMN IF EXISTS from_app;
