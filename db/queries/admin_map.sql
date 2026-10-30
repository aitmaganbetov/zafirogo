-- Карта клуба для REST админки (Этап 2, B1). Laravel: app/Http/Controllers/ClubZoneController.php state().

-- name: AdminListComputerStates :many
-- ПК клуба с сессией и клиентом сессии (ClubComputer with sessionUser, staffUser). Состояние связи — из
-- присутствия агента (Valkey), не из БД. Keyset по id (after_id), клубы — до сотен ПК.
SELECT c.id, c.club_zone_id, c.number, c.display_name, c.device_name, c.status, c.is_reserved,
       c.agent_state, c.agent_version, c.last_seen_at,
       c.current_session_id, c.current_session_user_id, c.session_guest, c.session_ends_at,
       c.staff_user_id, c.staff_kind, c.staff_until,
       su.name AS session_user_name, su.phone AS session_user_phone, st.name AS staff_name
FROM club_computers c
LEFT JOIN users su ON su.id = c.current_session_user_id
LEFT JOIN users st ON st.id = c.staff_user_id
WHERE c.club_id = sqlc.arg(club_id) AND c.id > sqlc.arg(after_id)
ORDER BY c.id
LIMIT sqlc.arg(page_size);

-- name: AdminGetComputerSession :one
-- Сессия ПК (для пересадки сотрудником: чья сессия).
SELECT id, current_session_id, current_session_user_id, session_guest, session_ends_at
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);
