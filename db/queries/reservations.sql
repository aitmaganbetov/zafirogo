-- Брони ПК (Этап 3, группа 4). Laravel: app/Http/Controllers/ComputerReservationController.php,
-- app/Services/ComputerReservations.php, app/Models/ComputerReservation.php.
-- Блокирующая бронь при покупке времени — purchases.sql (GetBlockingReservation, MarkReservationsArrived).

-- name: ListDayReservations :many
-- index(): брони локального дня клуба [from, to) по starts_at, по времени начала; paginate(50). Список ограничен
-- одним днём (индекс (club_id, starts_at)), поэтому страницы — номером, как ->links() старого шаблона.
SELECT r.id, r.club_computer_id, r.user_id, r.customer_name, r.customer_phone, r.note, r.starts_at, r.ends_at,
       r.arrived_at, r.cancelled_at,
       c.number AS computer_number, c.display_name AS computer_display_name, c.device_name AS computer_device_name,
       z.name AS zone_name, u.name AS user_name, u.phone AS user_phone
FROM computer_reservations r
LEFT JOIN club_computers c ON c.club_id = r.club_id AND c.id = r.club_computer_id
LEFT JOIN club_zones z ON z.club_id = c.club_id AND z.id = c.club_zone_id
LEFT JOIN users u ON u.id = r.user_id
WHERE r.club_id = sqlc.arg(club_id)
  AND r.starts_at >= sqlc.arg(from_at) AND r.starts_at < sqlc.arg(to_at)
ORDER BY r.starts_at, r.id
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: CountDayReservations :one
-- Всего броней дня ($reservations->total()) — для пагинации.
SELECT count(*)::bigint
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id) AND starts_at >= sqlc.arg(from_at) AND starts_at < sqlc.arg(to_at);

-- name: ListReservationComputers :many
-- ПК клуба для выбора (with('zone')->orderBy('club_zone_id')->orderBy('number')).
SELECT c.id, c.number, c.display_name, c.device_name, c.club_zone_id, z.name AS zone_name
FROM club_computers c
LEFT JOIN club_zones z ON z.club_id = c.club_id AND z.id = c.club_zone_id
WHERE c.club_id = sqlc.arg(club_id)
ORDER BY c.club_zone_id NULLS FIRST, c.number, c.id;

-- name: LockReservationComputer :one
-- ClubComputer::lockForUpdate()->findOrFail(): строка ПК сериализует брони и покупки времени на нём.
-- hasPaidSession(): current_session_id, (current_session_user_id или session_guest), session_ends_at в будущем.
SELECT c.id, c.number, c.display_name, c.device_name, c.current_session_id, c.current_session_user_id,
       c.session_guest, c.session_ends_at
FROM club_computers c
WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(id)
FOR UPDATE OF c;

-- name: FirstOverlappingReservation :one
-- ComputerReservation::overlapping(from, to)->orderBy('starts_at')->first(): ожидающая (не отменена, клиент не
-- пришёл) бронь ПК, пересекающая [from, to).
SELECT id, starts_at, ends_at
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id) AND club_computer_id = sqlc.arg(computer_id)
  AND cancelled_at IS NULL AND arrived_at IS NULL
  AND starts_at < sqlc.arg(to_at) AND ends_at > sqlc.arg(from_at)
ORDER BY starts_at, id
LIMIT 1;

-- name: InsertReservation :one
INSERT INTO computer_reservations (club_id, club_computer_id, user_id, customer_name, customer_phone, note, starts_at,
                                   ends_at, created_by, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(computer_id), sqlc.narg(user_id), sqlc.narg(customer_name),
        sqlc.narg(customer_phone), sqlc.narg(note), sqlc.arg(starts_at), sqlc.arg(ends_at), sqlc.arg(created_by),
        sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: LockReservation :one
-- close(): бронь с ПК, строка блокируется.
SELECT r.id, r.club_computer_id, r.starts_at, r.ends_at, r.arrived_at, r.cancelled_at,
       c.number AS computer_number, c.display_name AS computer_display_name, c.device_name AS computer_device_name
FROM computer_reservations r
LEFT JOIN club_computers c ON c.club_id = r.club_id AND c.id = r.club_computer_id
WHERE r.club_id = sqlc.arg(club_id) AND r.id = sqlc.arg(id)
FOR UPDATE OF r;

-- name: CancelReservation :exec
UPDATE computer_reservations SET cancelled_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ArriveReservation :exec
UPDATE computer_reservations SET arrived_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ReservationClubCustomer :one
-- Rule::exists('club_memberships', 'user_id')->where('club_id') + User::findOrFail(): клиент брони — любой
-- пользователь клуба (членство с любой ролью).
SELECT u.id, u.name, u.phone
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id) AND m.user_id = sqlc.arg(user_id);

-- name: SearchReservationCustomers :many
-- customers(): пользователи клуба по имени или телефону (цифры ≥ 3), по имени, не больше 10.
SELECT u.id, u.name, u.phone
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND (u.name ILIKE sqlc.arg(name_like)::text
       OR (sqlc.arg(phone_like)::text <> '' AND u.phone LIKE sqlc.arg(phone_like)::text))
ORDER BY u.name, u.id
LIMIT 10;

-- name: ReservationComputersExist :one
-- Rule::exists('club_computers', 'id')->where('club_id'): сколько из выбранных ПК есть в клубе.
SELECT count(*)::bigint FROM club_computers WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]);
