-- Мониторинг сервера (Этап 3, группа 6): фоновые задачи сбора метрик для экрана «Сервер» суперадминки.
-- Laravel: app/Console/Commands/CollectServerMetrics.php, SnapshotClubLoad.php, PruneMonitoringData.php.
-- Платформенные таблицы и выборки по всем клубам (пул zafiro_platform); исключены из tenant-filter префиксом
-- «Monitoring» в sqlc.yaml. Время — параметром из часов приложения.

-- name: MonitoringInsertServerMetric :exec
-- DB::table('server_metrics')->insertOrIgnore(['minute' => $minute, ...$snapshot]).
INSERT INTO server_metrics (minute, load_1, load_5, load_15, cpu_count, memory_total_mb, memory_used_mb, disk_total_mb,
                            disk_used_mb, uptime_seconds, database_mb, queue_pending, queue_failed, queue_oldest_seconds)
VALUES (sqlc.arg(minute), sqlc.narg(load_1)::float8, sqlc.narg(load_5)::float8, sqlc.narg(load_15)::float8, sqlc.narg(cpu_count),
        sqlc.narg(memory_total_mb), sqlc.narg(memory_used_mb), sqlc.narg(disk_total_mb), sqlc.narg(disk_used_mb),
        sqlc.narg(uptime_seconds), sqlc.narg(database_mb), sqlc.arg(queue_pending), sqlc.arg(queue_failed),
        sqlc.narg(queue_oldest_seconds))
ON CONFLICT (minute) DO NOTHING;

-- name: MonitoringAddRequests :exec
-- CollectServerMetrics::saveRequests(): строка минуты и канала складывается (дошедшие позже — добавляются).
INSERT INTO request_metrics (minute, channel, requests, errors, duration_ms)
VALUES (sqlc.arg(minute), sqlc.arg(channel), sqlc.arg(requests), sqlc.arg(errors), sqlc.arg(duration_ms))
ON CONFLICT (minute, channel) DO UPDATE SET requests = request_metrics.requests + EXCLUDED.requests,
    errors = request_metrics.errors + EXCLUDED.errors, duration_ms = request_metrics.duration_ms + EXCLUDED.duration_ms;

-- name: MonitoringAddClubRequests :exec
-- То же по клубу; клубы, которых нет, пропускаются (как $known в старом коде).
INSERT INTO club_request_metrics (club_id, minute, agent_requests, api_requests, panel_requests, guest_requests, errors, duration_ms)
SELECT c.id, sqlc.arg(minute), sqlc.arg(agent_requests), sqlc.arg(api_requests), sqlc.arg(panel_requests),
       sqlc.arg(guest_requests), sqlc.arg(errors), sqlc.arg(duration_ms)
FROM clubs c WHERE c.id = sqlc.arg(club_id)::bigint
ON CONFLICT (club_id, minute) DO UPDATE SET
    agent_requests = club_request_metrics.agent_requests + EXCLUDED.agent_requests,
    api_requests = club_request_metrics.api_requests + EXCLUDED.api_requests,
    panel_requests = club_request_metrics.panel_requests + EXCLUDED.panel_requests,
    guest_requests = club_request_metrics.guest_requests + EXCLUDED.guest_requests,
    errors = club_request_metrics.errors + EXCLUDED.errors,
    duration_ms = club_request_metrics.duration_ms + EXCLUDED.duration_ms;

-- name: MonitoringClubCounts :many
-- SnapshotClubLoad: ПК каждого клуба (всего и занято) и часовой пояс клуба. «В сети» — из присутствия (Valkey).
SELECT c.id AS club_id, c.timezone::text AS timezone,
       count(pc.id)::bigint AS computers,
       count(pc.id) FILTER (WHERE pc.current_session_id IS NOT NULL
           AND (pc.current_session_user_id IS NOT NULL OR pc.session_guest) AND pc.session_ends_at > sqlc.arg(now))::bigint AS busy
FROM clubs c
JOIN club_computers pc ON pc.club_id = c.id
GROUP BY c.id, c.timezone
ORDER BY c.id;

-- name: MonitoringInsertLoadSnapshot :exec
INSERT INTO club_load_snapshots (club_id, captured_at, weekday, hour, computers, online, busy)
VALUES (sqlc.arg(club_id)::bigint, sqlc.arg(captured_at), sqlc.arg(weekday), sqlc.arg(hour), sqlc.arg(computers),
        sqlc.arg(online), sqlc.arg(busy));

-- name: MonitoringPruneServerMetrics :execrows
DELETE FROM server_metrics WHERE minute < sqlc.arg(before_minute);

-- name: MonitoringPruneRequestMetrics :execrows
DELETE FROM request_metrics WHERE minute < sqlc.arg(before_minute);

-- name: MonitoringPruneLoadSnapshots :execrows
DELETE FROM club_load_snapshots WHERE captured_at < sqlc.arg(before_at);

-- name: MonitoringPruneClubRequests :execrows
DELETE FROM club_request_metrics WHERE minute < sqlc.arg(before_minute);
