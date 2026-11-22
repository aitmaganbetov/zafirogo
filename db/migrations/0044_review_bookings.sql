-- 0044_review_bookings: замечания полного код-ревью 22.11.2026 по броням (docs/DECISIONS.md, «ревью 22.11.2026»).
--
--  1. session_purchases.source = 'booking' — часть сессии от оплаченной брони из приложения
--     (reservations.startPaidSession). В Laravel запуск брони пишет в журнал только reservation.arrived, и зоны
--     пересадки (SeatTransfers::allowedZoneIds() — последняя tariff.purchased|time.purchased сессии) его не видят;
--     раньше часть брони писалась как 'minutes' / 'tariff' и сужала зоны пересадки.
--  2. Индекс воркера app-bookings:start (0040) — по (ends_at, id) вместо starts_at. Неявки (ожидающие оплаченные
--     брони, время которых прошло) никто не закрывает, и они копятся в этом частичном индексе навсегда; запрос
--     воркера отбирает ends_at > now и идёт страницами по (ends_at, id), поэтому ключ по ends_at пропускает всю
--     историю одним спуском по дереву (starts_at <= now + интервал клуба проверяется по строке: будущих ожидающих
--     броней немного — не дальше 7 дней вперёд). Замер (Postgres 18, 648 тыс. броней, из них 100 тыс. старых неявок
--     и 20 тыс. будущих): было — 120 тыс. записей индекса, 2 546 буферов, 18,8 мс и рост с историей; стало —
--     20 тыс. записей (только будущие), 419 буферов, 4–5 мс, от истории не зависит.
--  3. computer_reservations (club_id, ends_at) WHERE ожидающая — пересечение ожидающих броней клуба с интервалом
--     (AppBookingBookedComputerIDs, SeatMapBlockedComputers, AppBookingPaidPendingComputer): условие
--     «ends_at > начало» отсекает историю по индексу; существующий (club_id, starts_at) давал только верхнюю
--     границу «starts_at < конец» и читал все прошлые брони клуба. Частичный: закрытые брони (пришёл / отменена)
--     в пересечениях не участвуют. Нижняя граница по starts_at вместо индекса не годится: у брони с кассы нет
--     предела длительности. Замер на тех же данных (клуб с 3 240 бронями): было 3 134 строки кучи, 3 143 буфера,
--     22 мс; стало 125 строк, 129 буферов, 0,3 мс.

-- +goose Up
ALTER TABLE session_purchases DROP CONSTRAINT session_purchases_source_check;
ALTER TABLE session_purchases ADD CONSTRAINT session_purchases_source_check
    CHECK (source IN ('tariff', 'minutes', 'package', 'booking'));
COMMENT ON COLUMN session_purchases.source IS 'tariff — покупка тарифа, minutes — поминутно, package — запуск сохранённого пакета, booking — время оплаченной брони из приложения (package и booking не влияют на зоны пересадки). NULL — данные до 0006';

DROP INDEX computer_reservations_app_pending_starts_at_index;
CREATE INDEX computer_reservations_app_pending_ends_at_index ON computer_reservations (ends_at, id)
    WHERE from_app AND cancelled_at IS NULL AND arrived_at IS NULL;
CREATE INDEX computer_reservations_club_id_pending_ends_at_index ON computer_reservations (club_id, ends_at)
    WHERE cancelled_at IS NULL AND arrived_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS computer_reservations_club_id_pending_ends_at_index;
DROP INDEX IF EXISTS computer_reservations_app_pending_ends_at_index;
CREATE INDEX computer_reservations_app_pending_starts_at_index ON computer_reservations (starts_at)
    WHERE from_app AND cancelled_at IS NULL AND arrived_at IS NULL;
-- Части сессий от броней становятся поминутными (как до 0044), иначе прежний CHECK не встанет.
UPDATE session_purchases SET source = CASE WHEN kind = 'tariff' THEN 'tariff' ELSE 'minutes' END WHERE source = 'booking';
ALTER TABLE session_purchases DROP CONSTRAINT session_purchases_source_check;
ALTER TABLE session_purchases ADD CONSTRAINT session_purchases_source_check
    CHECK (source IN ('tariff', 'minutes', 'package'));
COMMENT ON COLUMN session_purchases.source IS 'tariff — покупка тарифа, minutes — поминутно, package — запуск сохранённого пакета (не влияет на зоны пересадки). NULL — данные до 0006';
