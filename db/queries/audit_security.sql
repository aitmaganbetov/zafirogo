-- Запросы исправлений аудита безопасности и производительности (фаза 2, исполнитель F2).

-- name: GetPrincipalAuth :one
-- Пользователь запроса и его членство в выбранном клубе одной строкой (PB-10): блокировка, флаг суперадмина, роль и
-- выбор прав читаются на каждом запросе панели (Laravel: SetCurrentClub — «A blocked account loses access at once»,
-- ClubPermissions::allows). Заменяет пару GetUserAuth + GetMembershipAuth в отдельной транзакции. users — платформенная
-- таблица; club_memberships — по club_id и user_id (уникальный индекс club_memberships_club_id_user_id_unique), RLS клуба.
-- member_role IS NULL — членства в клубе нет.
SELECT u.id, u.is_superadmin, u.blocked_at, m.role AS member_role, m.permissions AS member_permissions
FROM users u
LEFT JOIN club_memberships m ON m.user_id = u.id AND m.club_id = sqlc.arg(club_id)
WHERE u.id = sqlc.arg(user_id);

-- name: SeatMapComputers :many
-- Карта пересадки гостя (GuestService.GetSeatMap, решение GG-6). Laravel: app/Services/SeatTransfers.php map() —
-- ClubComputer::query()->orderBy('club_zone_id')->orderBy('number')->limit(500); поля сессии — для условия «свободен»
-- (freeSeatsFor()). «В сети» — status, который ведёт присутствие агента (AS-2), как в TransferSeat. ПК клуба —
-- десятки–сотни строк (club_computers_club_id_id_unique), сортировка в памяти.
SELECT id, club_zone_id, number, display_name, device_name, is_reserved, last_seen_at,
       current_session_id, current_session_user_id, session_guest, session_ends_at, staff_until, status,
       position_x, position_y
FROM club_computers
WHERE club_id = sqlc.arg(club_id)
ORDER BY club_zone_id, number
LIMIT 500;

-- name: SeatMapComputer :one
-- ПК, с которого гость пересаживается (без блокировки строки — карта только читает).
SELECT id, club_zone_id, number, display_name, device_name, is_reserved, last_seen_at,
       current_session_id, current_session_user_id, session_guest, session_ends_at, staff_until, status
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: SeatMapBlockedComputers :many
-- ПК с чужой ожидающей бронью, пересекающей остаток сессии: то же условие, что GetBlockingReservation
-- (ComputerReservations::blocking()), для всех ПК клуба одним запросом (в Laravel — запрос на каждый свободный ПК).
SELECT DISTINCT club_computer_id
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id)
  AND cancelled_at IS NULL AND arrived_at IS NULL
  AND starts_at < sqlc.arg(until) AND ends_at > sqlc.arg(from_at)
  AND (user_id IS NULL OR user_id <> sqlc.arg(user_id));
