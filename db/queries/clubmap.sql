-- Карта клуба, список ПК и дашборд (Этап 3, группа 1). Laravel: ClubZoneController, ClubComputerController,
-- ClubComputerListController, DashboardController, ClubZoneComputerService.

-- name: ClubMapZones :many
-- ClubZoneController::index(): зоны клуба по sort_order, id (единицы–десятки на клуб).
SELECT id, name, description, capacity, price_per_hour, icon, theme, status, is_featured, sort_order,
       position_x, position_y, width, height, rotation
FROM club_zones
WHERE club_id = sqlc.arg(club_id)
ORDER BY sort_order, id;

-- name: ClubMapZoneTariffs :many
-- Тарифы зон (club_zone_tariff): zone → tariff.
SELECT club_zone_id, tariff_id
FROM club_zone_tariff
WHERE club_id = sqlc.arg(club_id)
ORDER BY club_zone_id, tariff_id;

-- name: ClubMapComputers :many
-- ПК клуба для карты и списка: карточка, железо, сессия, техрежим (with staffUser, sessionUser). Keyset по id.
SELECT c.id, c.club_zone_id, c.number, c.display_name, c.device_name, c.device_id, c.status, c.is_reserved,
       c.position_x, c.position_y, c.agent_state, c.agent_version, c.last_seen_at,
       c.current_session_id, c.current_session_user_id, c.session_guest, c.session_ends_at,
       c.staff_user_id, c.staff_kind, c.staff_until,
       c.mac_address, c.local_ip, c.remote_ip, c.cpu_model, c.cpu_cores, c.ram_total_mb, c.gpu_model, c.os_version,
       c.hardware_reported_at, c.created_at, c.updated_at,
       su.name AS session_user_name, su.phone AS session_user_phone, st.name AS staff_name
FROM club_computers c
LEFT JOIN users su ON su.id = c.current_session_user_id
LEFT JOIN users st ON st.id = c.staff_user_id
WHERE c.club_id = sqlc.arg(club_id) AND c.id > sqlc.arg(after_id)
ORDER BY c.id
LIMIT sqlc.arg(page_size);

-- name: ClubMapSessionPurchases :many
-- ClubZoneController::computerStates(): что куплено в идущих сессиях — тарифы и минуты, по id.
SELECT session_id, kind, name, minutes
FROM session_purchases
WHERE club_id = sqlc.arg(club_id) AND session_id = ANY(sqlc.arg(session_ids)::text[])
ORDER BY id;

-- name: ClubMapHeldComputerIDs :many
-- ComputerReservations::heldComputerIds(): ПК с ожидающей бронью, пересекающей [from, to)
-- (from — сейчас, to — сейчас + booking_interval). Время — параметрами (часы приложения, V-1).
SELECT DISTINCT club_computer_id
FROM computer_reservations
WHERE club_id = sqlc.arg(club_id) AND cancelled_at IS NULL AND arrived_at IS NULL
  AND starts_at < sqlc.arg(to_at) AND ends_at > sqlc.arg(from_at);

-- name: ClubMapTariffs :many
-- Тарифы клуба по имени (Tariff::query()->orderBy('name'); мягко удалённые не показываются).
SELECT id, name, duration_minutes, price, currency, is_active, sale_periods, validity_periods
FROM tariffs
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL
ORDER BY name, id;

-- name: ClubMapSettings :one
-- Настройки клуба (booking_interval, guests, finances.currency) и часовой пояс. clubs — платформенная таблица
-- (без club_id и RLS; исключение tenant-filter в sqlc.yaml), строка — клуб из сессии.
SELECT settings, timezone, agent_staff_minutes FROM clubs WHERE id = sqlc.arg(club_id);

-- name: ClubMapCountZones :one
SELECT count(*)::bigint FROM club_zones WHERE club_id = sqlc.arg(club_id);

-- name: ClubMapInsertZone :one
-- ClubZoneController::store(): зона со смещением позиции 20 + (n*30 % 240), 20 + (n*30 % 180).
INSERT INTO club_zones (club_id, name, description, capacity, price_per_hour, icon, theme, status, is_featured,
                        sort_order, position_x, position_y, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(name), sqlc.narg(description), sqlc.narg(capacity), sqlc.narg(price_per_hour),
        sqlc.arg(icon), sqlc.arg(theme), sqlc.arg(status), sqlc.arg(is_featured), sqlc.arg(sort_order),
        sqlc.arg(position_x), sqlc.arg(position_y), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: ClubMapLockZone :one
SELECT id, name, description, capacity, price_per_hour, icon, theme, status, is_featured, sort_order,
       position_x, position_y, width, height, rotation
FROM club_zones
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ClubMapUpdateZone :exec
UPDATE club_zones
SET name = sqlc.arg(name), description = sqlc.narg(description), capacity = sqlc.narg(capacity),
    price_per_hour = sqlc.narg(price_per_hour), icon = sqlc.arg(icon), theme = sqlc.arg(theme),
    status = sqlc.arg(status), is_featured = sqlc.arg(is_featured), sort_order = sqlc.arg(sort_order),
    updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ClubMapZoneTariffIDs :many
SELECT tariff_id FROM club_zone_tariff
WHERE club_id = sqlc.arg(club_id) AND club_zone_id = sqlc.arg(zone_id)
ORDER BY tariff_id;

-- name: ClubMapDetachZoneTariffs :exec
-- syncWithPivotValues(): снять тарифы, которых нет в новом списке.
DELETE FROM club_zone_tariff
WHERE club_id = sqlc.arg(club_id) AND club_zone_id = sqlc.arg(zone_id)
  AND NOT (tariff_id = ANY(sqlc.arg(keep_ids)::bigint[]));

-- name: ClubMapAttachZoneTariffs :exec
INSERT INTO club_zone_tariff (club_zone_id, tariff_id, club_id)
SELECT sqlc.arg(zone_id), t, sqlc.arg(club_id) FROM unnest(sqlc.arg(tariff_ids)::bigint[]) AS t
ON CONFLICT (club_zone_id, tariff_id) DO NOTHING;

-- name: ClubMapCountTariffs :one
-- tariff_ids.* exists:tariffs,id (club_id): сколько из id — тарифы клуба (мягко удалённые тоже — как exists).
SELECT count(*)::bigint FROM tariffs WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ClubMapZoneRunningSessions :one
-- Решение Q-018: идущие сессии на ПК зоны (ClubComputer::hasPaidSession(): сессия клиента или гостя, конец впереди).
SELECT count(*)::bigint FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND club_zone_id = sqlc.arg(zone_id)
  AND current_session_id IS NOT NULL AND (current_session_user_id IS NOT NULL OR session_guest)
  AND session_ends_at > sqlc.arg(now);

-- name: ClubMapZoneComputerCount :one
SELECT count(*)::bigint FROM club_computers WHERE club_id = sqlc.arg(club_id) AND club_zone_id = sqlc.arg(zone_id);

-- name: ClubMapDisableOrphanTariffs :many
-- Решение Q-018: тарифы удаляемой зоны, у которых не останется других зон, выключаются (тариф без зон
-- действует во всех зонах — без выключения он стал бы продаваться везде). Вызывать до удаления зоны.
UPDATE tariffs t
SET is_active = false, updated_at = sqlc.arg(now)
WHERE t.club_id = sqlc.arg(club_id) AND t.is_active
  AND t.id IN (SELECT zt.tariff_id FROM club_zone_tariff zt WHERE zt.club_id = sqlc.arg(club_id) AND zt.club_zone_id = sqlc.arg(zone_id))
  AND NOT EXISTS (SELECT 1 FROM club_zone_tariff o
                  WHERE o.club_id = sqlc.arg(club_id) AND o.tariff_id = t.id AND o.club_zone_id <> sqlc.arg(zone_id))
RETURNING t.id, t.name;

-- name: ClubMapDeleteZone :exec
DELETE FROM club_zones WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ClubMapOtherZoneRects :many
-- ClubZoneController::updateLayout(): прямоугольники остальных зон (проверка пересечений с зазором 20).
SELECT position_x, position_y, width, height FROM club_zones
WHERE club_id = sqlc.arg(club_id) AND id <> sqlc.arg(id);

-- name: ClubMapUpdateZoneLayout :exec
UPDATE club_zones
SET position_x = sqlc.arg(position_x), position_y = sqlc.arg(position_y), width = sqlc.arg(width),
    height = sqlc.arg(height), rotation = COALESCE(sqlc.narg(rotation)::smallint, rotation), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ClubMapZoneComputersForUpdate :many
-- ClubZoneComputerService::add(): ПК зоны под блокировкой (номер и свободная ячейка).
SELECT id, number, position_x, position_y FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND club_zone_id = sqlc.arg(zone_id)
ORDER BY id
FOR UPDATE;

-- name: ClubMapInsertComputer :one
INSERT INTO club_computers (club_id, club_zone_id, number, device_name, status, position_x, position_y, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(zone_id), sqlc.arg(number), sqlc.arg(device_name), 'available',
        sqlc.arg(position_x), sqlc.arg(position_y), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: ClubMapLockComputer :one
SELECT id, club_zone_id, number, display_name, device_name, mac_address, position_x, position_y,
       current_session_id, current_session_user_id, session_guest, session_ends_at, is_reserved, agent_version
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: ClubMapComputerNumberTaken :one
-- UpdateClubComputerRequest: number unique (club_id, club_zone_id), кроме самого ПК.
SELECT EXISTS (SELECT 1 FROM club_computers WHERE club_id = sqlc.arg(club_id) AND club_zone_id = sqlc.arg(zone_id)
               AND number = sqlc.arg(number) AND id <> sqlc.arg(id))::boolean;

-- name: ClubMapComputerNameTaken :one
SELECT EXISTS (SELECT 1 FROM club_computers WHERE club_id = sqlc.arg(club_id) AND display_name = sqlc.arg(display_name)
               AND id <> sqlc.arg(id))::boolean;

-- name: ClubMapComputerMacTaken :one
SELECT EXISTS (SELECT 1 FROM club_computers WHERE club_id = sqlc.arg(club_id) AND mac_address = sqlc.arg(mac_address)
               AND id <> sqlc.arg(id))::boolean;

-- name: ClubMapUpdateComputer :exec
UPDATE club_computers
SET number = sqlc.arg(number), display_name = sqlc.narg(display_name), mac_address = sqlc.narg(mac_address),
    club_zone_id = sqlc.arg(zone_id), position_x = sqlc.arg(position_x), position_y = sqlc.arg(position_y),
    updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ClubMapDeleteComputer :exec
DELETE FROM club_computers WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ClubMapComputerCellTaken :one
-- ClubComputerController::__invoke(): ячейка занята другим ПК зоны.
SELECT EXISTS (SELECT 1 FROM club_computers WHERE club_id = sqlc.arg(club_id) AND club_zone_id = sqlc.arg(zone_id)
               AND id <> sqlc.arg(id) AND position_x = sqlc.arg(position_x) AND position_y = sqlc.arg(position_y))::boolean;

-- name: ClubMapUpdateComputerLayout :exec
UPDATE club_computers SET position_x = sqlc.arg(position_x), position_y = sqlc.arg(position_y), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ClubMapComputersByIDs :many
-- ПК из списка (выбранные на карте) — только этого клуба, по номеру (журнал: имена через запятую).
SELECT id, club_zone_id, number, display_name, device_name, mac_address, is_reserved, current_session_id,
       agent_version
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[])
ORDER BY number, id;

-- name: ClubMapSetReserved :execrows
UPDATE club_computers SET is_reserved = sqlc.arg(reserved), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ClubMapSetStatus :exec
-- ClubComputerController::control(): restart → restarting, power_off → offline.
UPDATE club_computers SET status = sqlc.arg(status), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ClubMapMarkWake :execrows
-- ComputerPower::queue('power_on'): wake_requested_at у ПК с MAC.
UPDATE club_computers SET wake_requested_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]) AND mac_address IS NOT NULL;

-- name: ClubMapStartStaffVisit :exec
-- ClubComputerSessions::startStaffVisit(..., 'panel'): техрежим из панели на agent_staff_minutes.
UPDATE club_computers
SET staff_user_id = sqlc.narg(staff_user_id), staff_kind = 'panel', staff_until = sqlc.arg(staff_until),
    updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ClubMapComputersWithSession :many
-- endSessions(): выбранные ПК с идущей сессией.
SELECT id, current_session_id FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]) AND current_session_id IS NOT NULL
ORDER BY id;

-- name: ClubComputersPage :many
-- ClubComputerListController: ПК по зоне и номеру, страница 50 (paginate(50)).
SELECT c.id, c.club_zone_id, c.number, c.display_name, c.device_name, c.device_id, c.status, c.is_reserved,
       c.position_x, c.position_y, c.agent_state, c.agent_version, c.last_seen_at, c.current_session_id,
       c.mac_address, c.local_ip, c.remote_ip, c.cpu_model, c.cpu_cores, c.ram_total_mb, c.gpu_model, c.os_version,
       c.hardware_reported_at, c.created_at, c.updated_at, z.name AS zone_name
FROM club_computers c
LEFT JOIN club_zones z ON z.club_id = c.club_id AND z.id = c.club_zone_id
WHERE c.club_id = sqlc.arg(club_id)
ORDER BY c.club_zone_id, c.number, c.id
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: ClubComputersCount :one
SELECT count(*)::bigint FROM club_computers WHERE club_id = sqlc.arg(club_id);

-- name: ClubComputerAgentVersions :many
-- AgentUpdate::status(): версии агентов клуба (сравнение версий — в Go).
SELECT agent_version FROM club_computers WHERE club_id = sqlc.arg(club_id) AND agent_version IS NOT NULL;

-- name: AgentReleaseSetting :one
-- AgentUpdate::release(): объявленный релиз агента (platform_settings 'agent_release'; платформенная, без RLS,
-- общая для всех клубов — исключение tenant-filter в sqlc.yaml).
-- Параметр club_id не фильтрует (строка общая): сигнатура AgentReleaseSetting(ctx, clubID) — для вызывающих разделов.
SELECT value FROM platform_settings WHERE key = 'agent_release' AND sqlc.arg(club_id)::bigint > 0;

-- name: DashboardCash :many
-- DashboardController: суммы и число операций кассы по типу за 7 локальных дней клуба одним запросом
-- (SUM(CASE…) по диапазонам дней в UTC). bounds — 8 границ дней (начало первого … конец последнего).
SELECT t.type, d.i::int AS day,
       COALESCE(sum(t.amount), 0)::bigint AS total, count(t.id)::bigint AS cnt
FROM generate_series(1, 7) AS d(i)
JOIN cash_transactions t
  ON t.club_id = sqlc.arg(club_id)
 AND t.created_at >= (sqlc.arg(bounds)::timestamptz[])[d.i] AND t.created_at < (sqlc.arg(bounds)::timestamptz[])[d.i + 1]
WHERE t.type IN ('income', 'expense')
GROUP BY t.type, d.i
ORDER BY d.i, t.type;

-- name: DashboardCounts :one
-- Товары (всего, остаток < 5), зоны, участники клуба (ClubMembership::query()->count()).
SELECT (SELECT count(*) FROM products p WHERE p.club_id = sqlc.arg(club_id))::bigint AS products,
       (SELECT count(*) FROM products p WHERE p.club_id = sqlc.arg(club_id) AND p.quantity < 5)::bigint AS low_stock,
       (SELECT count(*) FROM club_zones z WHERE z.club_id = sqlc.arg(club_id))::bigint AS zones,
       (SELECT count(*) FROM club_memberships m WHERE m.club_id = sqlc.arg(club_id))::bigint AS members;

-- name: DashboardComputers :many
-- Статусы ПК для «ПК онлайн / забронировано» (ClubComputer::currentStatus() по каждому; присутствие — в Go).
SELECT id, status, is_reserved, last_seen_at FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(page_size);

-- name: DashboardRecentCash :many
-- Последние 6 операций кассы.
SELECT id, type, amount, description, created_at FROM cash_transactions
WHERE club_id = sqlc.arg(club_id)
ORDER BY created_at DESC, id DESC
LIMIT 6;
