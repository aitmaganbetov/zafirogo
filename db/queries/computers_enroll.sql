-- Подключение ПК (Этап 3, G7): ПК клуба для «Перерегистрировать» / «Отключить» на экране /club-computers/enrollment.

-- name: EnrollmentComputers :many
-- Все ПК клуба по зоне и номеру (как список ПК); число ПК клуба ограничено картой зала.
SELECT id, club_zone_id, number, display_name, device_name
FROM club_computers
WHERE club_id = sqlc.arg(club_id)
ORDER BY club_zone_id, number, id;
