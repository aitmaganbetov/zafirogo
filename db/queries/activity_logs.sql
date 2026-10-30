-- Экран «Логи действий» (Этап 3, группа 4). Laravel: app/Http/Controllers/ActivityLogController.php index(),
-- app/Http/Requests/FilterActivityLogsRequest.php. activity_logs партиционирована по месяцам (created_at): период —
-- диапазоном [from, to), чтобы планировщик отсекал партиции; страницы — keyset (created_at, id), новые сверху.

-- name: ListActivityLogPage :many
-- Пустой event — все события; user_id 0 — все пользователи; пустой search — без поиска (описание или имя/телефон
-- пользователя, ILIKE подстрока — как LIKE '%…%' в MySQL с utf8mb4_unicode_ci).
SELECT l.id, l.event, l.description, l.user_id, l.details, l.ip_address, l.created_at,
       u.name AS user_name
FROM activity_logs l
LEFT JOIN users u ON u.id = l.user_id
WHERE l.club_id = sqlc.arg(club_id)::bigint
  AND l.created_at >= sqlc.arg(from_at) AND l.created_at < sqlc.arg(to_at)
  AND (l.created_at, l.id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::bigint)
  AND (sqlc.arg(event)::text = '' OR l.event = sqlc.arg(event)::text)
  AND (sqlc.arg(user_id)::bigint = 0 OR l.user_id = sqlc.arg(user_id)::bigint)
  AND (sqlc.arg(search)::text = ''
       OR l.description ILIKE sqlc.arg(search)::text
       OR u.name ILIKE sqlc.arg(search)::text OR u.phone ILIKE sqlc.arg(search)::text)
ORDER BY l.created_at DESC, l.id DESC
LIMIT sqlc.arg(page_size);

-- name: CountActivityLogPage :one
-- $logs->total() («Записей: N») с теми же фильтрами.
SELECT count(*)::bigint
FROM activity_logs l
LEFT JOIN users u ON u.id = l.user_id
WHERE l.club_id = sqlc.arg(club_id)::bigint
  AND l.created_at >= sqlc.arg(from_at) AND l.created_at < sqlc.arg(to_at)
  AND (sqlc.arg(event)::text = '' OR l.event = sqlc.arg(event)::text)
  AND (sqlc.arg(user_id)::bigint = 0 OR l.user_id = sqlc.arg(user_id)::bigint)
  AND (sqlc.arg(search)::text = ''
       OR l.description ILIKE sqlc.arg(search)::text
       OR u.name ILIKE sqlc.arg(search)::text OR u.phone ILIKE sqlc.arg(search)::text);

-- name: ListActivityLogUsers :many
-- Пользователи, встречающиеся в журнале клуба (User::whereIn('id', ActivityLog::select('user_id'))->orderBy('name')).
-- Автор записи журнала — член клуба (FK (club_id, user_id) → club_memberships), поэтому выборка идёт по членствам
-- с EXISTS по индексу (club_id, user_id), а не по всему журналу.
SELECT u.id, u.name, u.phone
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)::bigint
  AND EXISTS (SELECT 1 FROM activity_logs l WHERE l.club_id = m.club_id AND l.user_id = m.user_id)
ORDER BY u.name, u.id;

-- name: ClubMemberExists :one
-- Rule::in(ClubMembership::pluck('user_id')): пользователь — член клуба.
SELECT EXISTS (SELECT 1 FROM club_memberships WHERE club_id = sqlc.arg(club_id)::bigint AND user_id = sqlc.arg(user_id))::boolean;
