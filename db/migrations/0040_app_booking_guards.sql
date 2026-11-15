-- 0040_app_booking_guards: защита броней на уровне базы (тема Г, docs/upstream-2026-11.md §3.6; решения —
-- docs/open-questions/upstream-bookings.md UB-2, UB-3).
--
--  1. Две ожидающие брони одного ПК не пересекаются по времени. В Laravel это держит только блокировка строки ПК
--     (ComputerReservations::create(): lockForUpdate + проверка пересечения с интервалом между бронями); здесь та же
--     блокировка остаётся (reservations.Create), а ограничение — второй рубеж: путь, забывший блокировку, получит
--     ошибку 23P01, а не две оплаченные брони на одно место. Интервал между бронями (настройка клуба) в
--     ограничение не входит — его проверяет код под блокировкой. Диапазон по id ПК — int8range (у диапазонов
--     встроенный класс GiST, расширение btree_gist не нужно); id ПК уникален на всю таблицу, поэтому club_id в
--     ограничении не участвует. GREATEST — строка с ends_at < starts_at (в старой схеме CHECK нет) не ломает
--     построение индекса.
--  2. Индекс для фоновой задачи запуска оплаченных броней вошедших гостей (worker app-bookings:start, UQ-11):
--     ожидающие брони из приложения по времени начала — по всем клубам; индекс частичный и маленький.
--
-- Уникальность кода брони среди броней клуба (GAP-058) индексом не выражается: код один на группу ПК (несколько
-- строк), а «занят» он только пока ends_at > now − 1 день. Её держит транзакционная advisory-блокировка клуба
-- (db/queries/app_bookings.sql AppBookingLockCodes) + проверка занятости под ней.

-- +goose Up
ALTER TABLE computer_reservations ADD CONSTRAINT computer_reservations_pending_no_overlap
    EXCLUDE USING gist (
        int8range(club_computer_id, club_computer_id, '[]') WITH &&,
        tstzrange(starts_at, GREATEST(starts_at, ends_at)) WITH &&
    ) WHERE (cancelled_at IS NULL AND arrived_at IS NULL);
COMMENT ON CONSTRAINT computer_reservations_pending_no_overlap ON computer_reservations IS
    'Ожидающие (не отменённые, клиент не пришёл) брони одного ПК не пересекаются по времени; интервал между бронями проверяет код под блокировкой ПК';
CREATE INDEX computer_reservations_app_pending_starts_at_index ON computer_reservations (starts_at)
    WHERE from_app AND cancelled_at IS NULL AND arrived_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS computer_reservations_app_pending_starts_at_index;
ALTER TABLE computer_reservations DROP CONSTRAINT IF EXISTS computer_reservations_pending_no_overlap;
