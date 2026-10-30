-- Вход на ПК по QR с телефона (Этап 3, G7): страница /pc-login/{code} знает только код, клуб — клуб кода.
-- Пул zafiro_platform (политика platform_access), как AgentQrLogin::query()->withoutGlobalScope('club').

-- name: PcLoginFindOpen :one
-- QrLogins::findOpen(): открытый (не подтверждённый, не использованный, не истёкший) вход по хэшу кода в любом клубе,
-- с клубом и ПК (with(['computer', 'club'])). Время — параметром (часы приложения).
SELECT l.club_id, cl.name AS club_name, c.display_name, c.device_name, c.number
FROM agent_qr_logins l
JOIN clubs cl ON cl.id = l.club_id
LEFT JOIN club_computers c ON c.id = l.club_computer_id AND c.club_id = l.club_id
WHERE l.code_hash = sqlc.arg(code_hash)
  AND l.approved_user_id IS NULL AND l.consumed_at IS NULL AND l.expires_at > sqlc.arg(now);
