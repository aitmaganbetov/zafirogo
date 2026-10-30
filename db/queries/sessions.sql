-- Сессии ПК: досрочное завершение с расчётом (SessionSettlement), пересадка, естественное истечение.
-- Laravel: app/Services/ClubComputerSessions.php end(), expireAll(); app/Services/SessionSettlement.php settle();
-- app/Services/SeatTransfers.php transfer(), allowedZoneIds().

-- name: ListSessionPurchases :many
-- SessionSettlement::settle(): SessionPurchase::where('session_id', …)->orderByDesc('id')->get().
SELECT id, user_id, kind, tariff_id, name, minutes, main_cents, bonus_cents, expires_at
FROM session_purchases
WHERE club_id = sqlc.arg(club_id) AND session_id = sqlc.arg(session_id)
ORDER BY id DESC;

-- name: DeleteSessionPurchases :exec
DELETE FROM session_purchases
WHERE club_id = sqlc.arg(club_id) AND session_id = sqlc.arg(session_id);

-- name: InsertTimePackage :one
-- SessionSettlement::settle(): TimePackage::create() — сохранённые минуты тарифа с лимитом использования.
INSERT INTO time_packages (club_id, user_id, tariff_id, name, minutes_left, expires_at, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(user_id), sqlc.narg(tariff_id), sqlc.arg(name), sqlc.arg(minutes_left),
        sqlc.arg(expires_at), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: ClearComputerSession :exec
-- ClubComputerSessions::SESSION_FIELDS: сессия снята (end()).
UPDATE club_computers
SET current_session_id = NULL, current_session_user_id = NULL, session_guest = false, session_ends_at = NULL,
    updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: MoveComputerSession :exec
-- SeatTransfers::transfer(): на исходном ПК обнуляются id, клиент и конец; session_guest не трогается.
-- На целевом ПК ставятся те же три поля (session_guest тоже не трогается).
UPDATE club_computers
SET current_session_id = sqlc.narg(session_id),
    current_session_user_id = sqlc.narg(session_user_id),
    session_ends_at = sqlc.narg(session_ends_at),
    updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ListSessionZonePurchases :many
-- Части сессии для зон пересадки (Допущение Этапа 1: Q-007/OQ-BR-04 — вместо журнала tariff.purchased /
-- time.purchased). tariff_found — тариф есть и НЕ удалён мягко (Tariff::find() без withTrashed).
SELECT sp.id, sp.kind, sp.source, sp.zone_id, sp.tariff_id,
       (t.id IS NOT NULL)::boolean AS tariff_found,
       COALESCE((SELECT array_agg(zt.club_zone_id ORDER BY zt.club_zone_id)
                 FROM club_zone_tariff zt
                 WHERE zt.club_id = sp.club_id AND zt.tariff_id = sp.tariff_id), '{}')::bigint[] AS tariff_zone_ids
FROM session_purchases sp
LEFT JOIN tariffs t ON t.club_id = sp.club_id AND t.id = sp.tariff_id AND t.deleted_at IS NULL
WHERE sp.club_id = sqlc.arg(club_id) AND sp.session_id = sqlc.arg(session_id)
ORDER BY sp.id;

-- name: ExpireDueSessions :many
-- Платформенный запрос (пул zafiro_platform, все клубы): ClubComputerSessions::expireAll() —
-- UPDATE … WHERE session_ends_at <= now() по всем клубам, без расчёта возврата (GAP-005, Q-010).
-- now передаётся из Go (часы сервиса), усечённым до секунды, как строка Y-m-d H:i:s в старом коде.
UPDATE club_computers
SET current_session_id = NULL, current_session_user_id = NULL, session_guest = false, session_ends_at = NULL,
    updated_at = sqlc.arg(now)
WHERE session_ends_at IS NOT NULL AND session_ends_at <= sqlc.arg(now)::timestamptz
RETURNING club_id, id;
