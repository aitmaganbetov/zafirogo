-- Брони из гостевого приложения (тема Г, docs/upstream-2026-11.md; BR-174…BR-179, BR-190).
-- Laravel: app/Services/GuestBookings.php, app/Services/ComputerReservations.php (startForSignedIn(), startByCode(),
-- startPaidSession(), refund()), app/Http/Controllers/Api/GuestBookingController.php.
-- Общие запросы броней — reservations.sql; блокировка тарифа и счётчик лимита — purchases.sql.

-- name: AppBookingZones :many
-- ClubZone::query()->orderBy('sort_order')->orderBy('id')->get(['id', 'name', 'price_per_hour']).
SELECT id, name, price_per_hour
FROM club_zones
WHERE club_id = sqlc.arg(club_id)
ORDER BY sort_order, id;

-- name: AppBookingComputers :many
-- ПК клуба по номеру (->orderBy('number')) с полями сессии: свободен ли ПК на время брони (options()).
SELECT id, club_zone_id, number, display_name, device_name, is_reserved, current_session_id, current_session_user_id,
       session_guest, session_ends_at
FROM club_computers
WHERE club_id = sqlc.arg(club_id)
ORDER BY number, id;

-- name: AppBookingComputerZones :many
-- ClubComputer::query()->whereKey($computerIds)->distinct()->pluck('club_zone_id'): зоны выбранных ПК (tariffs()).
SELECT DISTINCT club_zone_id
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]);

-- name: AppBookingTariffs :many
-- bookableTariffs(): активные неудалённые тарифы клуба по длительности и имени, с зонами и счётчиком покупок для
-- лимита количества (продажи в агенте + неотменённые брони, BR-190).
SELECT t.id, t.name, t.description, t.duration_minutes, t.price, t.payment_method, t.discount_enabled,
       t.is_active, t.usage_limit_enabled, t.usage_limit_minutes, t.quantity_limit_enabled, t.quantity_limit,
       t.user_roles, t.sale_periods, t.validity_periods,
       COALESCE((SELECT array_agg(zt.club_zone_id ORDER BY zt.club_zone_id) FROM club_zone_tariff zt
                 WHERE zt.club_id = t.club_id AND zt.tariff_id = t.id), '{}')::bigint[] AS zone_ids,
       (COALESCE((SELECT s.sold FROM tariff_sales s WHERE s.club_id = t.club_id AND s.tariff_id = t.id), 0)
        + (SELECT count(*) FROM computer_reservations r
           WHERE r.club_id = t.club_id AND r.tariff_id = t.id AND r.cancelled_at IS NULL))::bigint AS sold
FROM tariffs t
WHERE t.club_id = sqlc.arg(club_id) AND t.is_active AND t.deleted_at IS NULL
ORDER BY t.duration_minutes, t.name, t.id;

-- name: AppBookingLockComputers :many
-- ClubComputer::query()->whereKey($computerIds)->orderBy('id')->lockForUpdate()->get(): ПК группы блокируются по
-- возрастанию id (один порядок у всех броней — без взаимных блокировок).
SELECT id, club_zone_id, number, display_name, device_name, is_reserved
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY id
FOR UPDATE;

-- name: AppBookingBookedComputerIDs :many
-- options(): ПК с ожидающей бронью, пересекающей [from, to) (границы уже с интервалом между бронями).
SELECT DISTINCT club_computer_id
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id)
  AND cancelled_at IS NULL AND arrived_at IS NULL
  AND starts_at < sqlc.arg(to_at) AND ends_at > sqlc.arg(from_at);

-- name: AppBookingAhead :many
-- book(): ожидающие брони гостя, которые ещё не закончились (предел MAX_PENDING; группа по коду — одна бронь).
-- Вызывать под блокировкой строки баланса гостя.
SELECT id, code
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id)
  AND cancelled_at IS NULL AND arrived_at IS NULL
  AND ends_at > sqlc.arg(now);

-- name: AppBookingLockCodes :exec
-- Выдача кодов брони в клубе — по одной транзакции за раз (GAP-058): блокировка до конца транзакции, под ней —
-- AppBookingCodeTaken и вставка броней с новым кодом.
-- Ключ — клуб транзакции (zafiro_current_club_id(), как у запросов строки clubs).
SELECT pg_advisory_xact_lock(hashtextextended('app_booking_code', c.id))
FROM clubs c
WHERE c.id = sqlc.arg(club_id) AND c.id = zafiro_current_club_id();

-- name: AppBookingCodeTaken :one
-- newCode(): код занят бронью клуба, которая закончилась не раньше чем сутки назад.
SELECT EXISTS (
    SELECT 1 FROM computer_reservations
    WHERE club_id = sqlc.arg(club_id) AND code = sqlc.arg(code)::text AND ends_at > sqlc.arg(since)
);

-- name: AppBookingList :many
-- GuestBookingController::index(): брони гостя с началом не раньше чем сутки назад, по началу, не больше 20 строк.
SELECT r.id, r.club_computer_id, r.code, r.starts_at, r.ends_at, r.arrived_at, r.cancelled_at,
       r.paid_main_cents, r.paid_bonus_cents,
       c.number AS computer_number, c.display_name AS computer_display_name, c.device_name AS computer_device_name,
       z.name AS zone_name, t.name AS tariff_name
FROM computer_reservations r
LEFT JOIN club_computers c ON c.club_id = r.club_id AND c.id = r.club_computer_id
LEFT JOIN club_zones z ON z.club_id = c.club_id AND z.id = c.club_zone_id
LEFT JOIN tariffs t ON t.club_id = r.club_id AND t.id = r.tariff_id AND t.deleted_at IS NULL
WHERE r.club_id = sqlc.arg(club_id) AND r.user_id = sqlc.arg(user_id) AND r.starts_at >= sqlc.arg(since)
ORDER BY r.starts_at, r.id
LIMIT 20;

-- name: AppBookingOfUser :one
-- cancel(): ComputerReservation::query()->where('user_id', …)->findOrFail($id) — чужая бронь не находится (404).
SELECT id, code
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id) AND id = sqlc.arg(id);

-- name: AppBookingGroup :many
-- group(): брони, сделанные вместе (один код одного гостя), по id; present() — с ПК, зоной и тарифом.
SELECT r.id, r.club_computer_id, r.code, r.starts_at, r.ends_at, r.arrived_at, r.cancelled_at,
       r.paid_main_cents, r.paid_bonus_cents,
       c.number AS computer_number, c.display_name AS computer_display_name, c.device_name AS computer_device_name,
       z.name AS zone_name, t.name AS tariff_name
FROM computer_reservations r
LEFT JOIN club_computers c ON c.club_id = r.club_id AND c.id = r.club_computer_id
LEFT JOIN club_zones z ON z.club_id = c.club_id AND z.id = c.club_zone_id
LEFT JOIN tariffs t ON t.club_id = r.club_id AND t.id = r.tariff_id AND t.deleted_at IS NULL
WHERE r.club_id = sqlc.arg(club_id) AND r.user_id = sqlc.arg(user_id)
  AND (r.id = sqlc.arg(id) OR (sqlc.narg(code)::text IS NOT NULL AND r.code = sqlc.narg(code)::text))
ORDER BY r.id;

-- name: AppBookingFirstOfGroup :one
-- Первая бронь группы ПК (её id — ссылка операции reservation_payment в ledger).
SELECT min(id)::bigint AS id
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id) AND code = sqlc.arg(code)::text;

-- name: AppBookingPaymentTx :one
-- Операция ledger оплаты брони (у возврата — ReversesTransactionID). У броней, перенесённых из Laravel, её нет.
SELECT id
FROM ledger_transactions
WHERE club_id = sqlc.arg(club_id) AND kind = 'reservation_payment' AND reservation_id = sqlc.arg(reservation_id)::bigint
ORDER BY id
LIMIT 1;

-- name: AppBookingDue :one
-- startForSignedIn(): оплаченная ожидающая бронь гостя на этом ПК, которая начинается не позже now + интервал
-- (until) и ещё не закончилась; самая ранняя, строка блокируется. Вызывать под блокировкой ПК.
SELECT id
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id) AND club_computer_id = sqlc.arg(computer_id) AND user_id = sqlc.arg(user_id)
  AND cancelled_at IS NULL AND arrived_at IS NULL
  AND (paid_main_cents > 0 OR paid_bonus_cents > 0)
  AND starts_at <= sqlc.arg(until) AND ends_at > sqlc.arg(now)
ORDER BY starts_at, id
LIMIT 1
FOR UPDATE;

-- name: AppBookingByCode :one
-- startByCode(): ожидающая незакончившаяся бронь с этим кодом на этом ПК; самая ранняя, строка блокируется.
SELECT id
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id) AND club_computer_id = sqlc.arg(computer_id) AND code = sqlc.arg(code)::text
  AND cancelled_at IS NULL AND arrived_at IS NULL
  AND ends_at > sqlc.arg(now)
ORDER BY starts_at, id
LIMIT 1
FOR UPDATE;

-- name: AppBookingCodeElsewhere :one
-- startByCode(): код есть у ожидающей брони клуба на другом ПК («не для этого ПК») или его нет вовсе.
SELECT EXISTS (
    SELECT 1 FROM computer_reservations
    WHERE club_id = sqlc.arg(club_id) AND code = sqlc.arg(code)::text
      AND cancelled_at IS NULL AND arrived_at IS NULL AND ends_at > sqlc.arg(now)
);

-- name: AppBookingDueCandidates :many
-- Воркер app-bookings:start (UQ-11): оплаченные ожидающие брони всех клубов, которые ещё не закончились и начнутся
-- не позже until (сейчас + наибольший интервал между бронями — 7 дней, как предел брони вперёд). gap_settings —
-- настройка интервала клуба в виде {"club": {"booking_interval": …}} (её разбирает тот же код, что и везде).
-- Пул zafiro_platform; вход гостя на ПК и условия запуска проверяет StartForSignedIn под блокировкой. Частичный
-- индекс computer_reservations_app_pending_starts_at_index (0040).
SELECT r.id, r.club_id, r.club_computer_id, r.user_id, r.starts_at,
       jsonb_build_object('club', jsonb_build_object('booking_interval', c.settings -> 'club' -> 'booking_interval')) AS gap_settings
FROM computer_reservations r
JOIN clubs c ON c.id = r.club_id
WHERE r.from_app AND r.cancelled_at IS NULL AND r.arrived_at IS NULL
  AND r.user_id IS NOT NULL
  AND (r.paid_main_cents > 0 OR r.paid_bonus_cents > 0)
  AND r.starts_at <= sqlc.arg(until) AND r.ends_at > sqlc.arg(now)
ORDER BY r.starts_at, r.id
LIMIT sqlc.arg(max_rows);

-- name: AppBookingTariff :one
-- startPaidSession(): Tariff::query()->find($reservation->tariff_id) — без мягко удалённых.
SELECT id, name, usage_limit_enabled, usage_limit_minutes
FROM tariffs
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: AppBookingUser :one
-- Гость брони: имя и телефон в строку брони (customer_name, customer_phone).
SELECT u.id, u.name, u.phone
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id) AND m.user_id = sqlc.arg(user_id);
