-- name: ComputerHealthHourlyUpsert :execrows
-- Почасовая история здоровья ПК (0031, agent/health HourlyFlusher): строки часа клуба одним запросом. rows — JSON-массив
-- агрегатов (HourlyRow). Только ПК этого клуба (удалённый ПК пропускается). Повтор того же часа заменяет строку теми же
-- значениями (агрегат часа в Valkey после конца часа не меняется) — сброс идемпотентен.
INSERT INTO computer_health_hourly (
    club_id, computer_id, hour, online_minutes, session_minutes,
    cpu_load_min_bp, cpu_load_avg_bp, cpu_load_max_bp, ram_min_bp, ram_avg_bp, ram_max_bp,
    gpu_load_min_bp, gpu_load_avg_bp, gpu_load_max_bp,
    cpu_temp_min_deci, cpu_temp_avg_deci, cpu_temp_max_deci, gpu_temp_min_deci, gpu_temp_avg_deci, gpu_temp_max_deci,
    system_disk_free_mb, system_disk_total_mb, smart_worst, peripheral_disconnects, unexpected_shutdowns, bugchecks)
SELECT c.club_id, c.id, r.hour, r.online_minutes, r.session_minutes,
       r.cpu_load_min_bp, r.cpu_load_avg_bp, r.cpu_load_max_bp, r.ram_min_bp, r.ram_avg_bp, r.ram_max_bp,
       r.gpu_load_min_bp, r.gpu_load_avg_bp, r.gpu_load_max_bp,
       r.cpu_temp_min_deci, r.cpu_temp_avg_deci, r.cpu_temp_max_deci, r.gpu_temp_min_deci, r.gpu_temp_avg_deci,
       r.gpu_temp_max_deci, r.system_disk_free_mb, r.system_disk_total_mb, r.smart_worst,
       r.peripheral_disconnects, r.unexpected_shutdowns, r.bugchecks
FROM jsonb_to_recordset(sqlc.arg(rows)::jsonb) AS r(
         computer_id bigint, hour timestamptz, online_minutes smallint, session_minutes smallint,
         cpu_load_min_bp integer, cpu_load_avg_bp integer, cpu_load_max_bp integer,
         ram_min_bp integer, ram_avg_bp integer, ram_max_bp integer,
         gpu_load_min_bp integer, gpu_load_avg_bp integer, gpu_load_max_bp integer,
         cpu_temp_min_deci integer, cpu_temp_avg_deci integer, cpu_temp_max_deci integer,
         gpu_temp_min_deci integer, gpu_temp_avg_deci integer, gpu_temp_max_deci integer,
         system_disk_free_mb bigint, system_disk_total_mb bigint, smart_worst varchar,
         peripheral_disconnects integer, unexpected_shutdowns integer, bugchecks integer)
JOIN club_computers c ON c.club_id = sqlc.arg(club_id) AND c.id = r.computer_id
ON CONFLICT (club_id, computer_id, hour) DO UPDATE SET
    online_minutes = EXCLUDED.online_minutes, session_minutes = EXCLUDED.session_minutes,
    cpu_load_min_bp = EXCLUDED.cpu_load_min_bp, cpu_load_avg_bp = EXCLUDED.cpu_load_avg_bp,
    cpu_load_max_bp = EXCLUDED.cpu_load_max_bp, ram_min_bp = EXCLUDED.ram_min_bp, ram_avg_bp = EXCLUDED.ram_avg_bp,
    ram_max_bp = EXCLUDED.ram_max_bp, gpu_load_min_bp = EXCLUDED.gpu_load_min_bp,
    gpu_load_avg_bp = EXCLUDED.gpu_load_avg_bp, gpu_load_max_bp = EXCLUDED.gpu_load_max_bp,
    cpu_temp_min_deci = EXCLUDED.cpu_temp_min_deci, cpu_temp_avg_deci = EXCLUDED.cpu_temp_avg_deci,
    cpu_temp_max_deci = EXCLUDED.cpu_temp_max_deci, gpu_temp_min_deci = EXCLUDED.gpu_temp_min_deci,
    gpu_temp_avg_deci = EXCLUDED.gpu_temp_avg_deci, gpu_temp_max_deci = EXCLUDED.gpu_temp_max_deci,
    system_disk_free_mb = EXCLUDED.system_disk_free_mb, system_disk_total_mb = EXCLUDED.system_disk_total_mb,
    smart_worst = EXCLUDED.smart_worst, peripheral_disconnects = EXCLUDED.peripheral_disconnects,
    unexpected_shutdowns = EXCLUDED.unexpected_shutdowns, bugchecks = EXCLUDED.bugchecks;

-- name: ComputerHealthWeekly :many
-- Панель ПК «По неделям»: агрегаты почасовой истории по неделям (понедельник 00:00 в поясе клуба). Период — диапазоном
-- по hour (индекс — первичный ключ club_id, computer_id, hour). Границы недель (bounds, по возрастанию; from_at — первая,
-- to_at — последняя) считает сервер в поясе клуба (club.Info.StartOfWeek), а неделя строки — номер интервала
-- width_bucket(hour, bounds) (1 — первая неделя): пояс в Postgres не передаётся, поэтому пояс, известный Go, но не
-- Postgres (другая tzdata), не ломает запрос.
-- Средние — взвешенные по минутам онлайн (целочисленно, без float). Часы с проблемами — часы онлайн, в которые была
-- открыта хоть одна проблема журнала computer_health_events; network_slow_hours — часы онлайн с открытой проблемой
-- network_slow (вердикт бездискового ПК). Журнал читается одним проходом: проблемы ПК, пересекающиеся с [from_at, to_at)
-- (индекс history_index), — затем флаги часа по этому небольшому набору (LATERAL bool_or).
-- Бездисковый ПК (diskless, S7-H-18) — одна точка нормализации: проблемы видов hidden_kinds (health.DiskKinds) не
-- считаются, системный диск и SMART — NULL / 0 (строки до перевода ПК на загрузку по сети или от старого агента).
-- Нет измерений — NULL (smart_rank — 0: 1 ok, 2 warning, 3 failing; ранги — agent/health/hourly.go smartRank*).
WITH ev AS MATERIALIZED (
    SELECT e.raised_at, coalesce(e.cleared_at, 'infinity'::timestamptz) AS cleared_at, e.kind = 'network_slow' AS network
    FROM computer_health_events e
    WHERE e.club_id = sqlc.arg(club_id) AND e.computer_id = sqlc.arg(computer_id)
      AND e.raised_at < sqlc.arg(to_at)::timestamptz
      AND (e.cleared_at IS NULL OR e.cleared_at > sqlc.arg(from_at)::timestamptz)
      AND e.kind <> ALL (sqlc.arg(hidden_kinds)::text[])
), wk AS (
    SELECT width_bucket(h.hour, sqlc.arg(bounds)::timestamptz[])::integer AS week_index,
           coalesce(sum(h.online_minutes), 0)::bigint AS online_minutes,
           coalesce(sum(h.session_minutes), 0)::bigint AS session_minutes,
           (sum(h.cpu_load_avg_bp::bigint * h.online_minutes)
               / nullif(sum(h.online_minutes) FILTER (WHERE h.cpu_load_avg_bp IS NOT NULL), 0))::integer AS cpu_load_avg_bp,
           max(h.cpu_load_max_bp)::integer AS cpu_load_max_bp,
           (sum(h.ram_avg_bp::bigint * h.online_minutes)
               / nullif(sum(h.online_minutes) FILTER (WHERE h.ram_avg_bp IS NOT NULL), 0))::integer AS ram_avg_bp,
           max(h.ram_max_bp)::integer AS ram_max_bp,
           (sum(h.gpu_load_avg_bp::bigint * h.online_minutes)
               / nullif(sum(h.online_minutes) FILTER (WHERE h.gpu_load_avg_bp IS NOT NULL), 0))::integer AS gpu_load_avg_bp,
           max(h.gpu_load_max_bp)::integer AS gpu_load_max_bp,
           (sum(h.cpu_temp_avg_deci::bigint * h.online_minutes)
               / nullif(sum(h.online_minutes) FILTER (WHERE h.cpu_temp_avg_deci IS NOT NULL), 0))::integer AS cpu_temp_avg_deci,
           max(h.cpu_temp_max_deci)::integer AS cpu_temp_max_deci,
           (sum(h.gpu_temp_avg_deci::bigint * h.online_minutes)
               / nullif(sum(h.online_minutes) FILTER (WHERE h.gpu_temp_avg_deci IS NOT NULL), 0))::integer AS gpu_temp_avg_deci,
           max(h.gpu_temp_max_deci)::integer AS gpu_temp_max_deci,
           count(*) FILTER (WHERE h.cpu_temp_max_deci >= sqlc.arg(cpu_hot_deci)::integer)::bigint AS cpu_hot_hours,
           count(*) FILTER (WHERE h.gpu_temp_max_deci >= sqlc.arg(gpu_hot_deci)::integer)::bigint AS gpu_hot_hours,
           count(*) FILTER (WHERE f.problem)::bigint AS problem_hours,
           count(*) FILTER (WHERE f.network)::bigint AS network_slow_hours,
           coalesce(sum(h.unexpected_shutdowns), 0)::bigint AS unexpected_shutdowns,
           coalesce(sum(h.bugchecks), 0)::bigint AS bugchecks,
           coalesce(sum(h.peripheral_disconnects), 0)::bigint AS peripheral_disconnects,
           (min(h.system_disk_free_mb) FILTER (WHERE NOT sqlc.arg(diskless)::boolean))::bigint AS system_disk_free_mb,
           (min(h.system_disk_free_mb * 10000 / nullif(h.system_disk_total_mb, 0))
               FILTER (WHERE NOT sqlc.arg(diskless)::boolean))::bigint AS system_disk_free_bp,
           coalesce(max(CASE h.smart_worst WHEN 'failing' THEN 3 WHEN 'warning' THEN 2 WHEN 'ok' THEN 1 END)
               FILTER (WHERE NOT sqlc.arg(diskless)::boolean), 0)::integer AS smart_rank
    FROM computer_health_hourly h
    LEFT JOIN LATERAL (
        SELECT bool_or(true) AS problem, bool_or(ev.network) AS network
        FROM ev
        WHERE ev.raised_at < h.hour + interval '1 hour' AND ev.cleared_at > h.hour
    ) f ON true
    WHERE h.club_id = sqlc.arg(club_id) AND h.computer_id = sqlc.arg(computer_id)
      AND h.hour >= sqlc.arg(from_at)::timestamptz AND h.hour < sqlc.arg(to_at)::timestamptz
    GROUP BY 1
)
-- Измерения — из того же wk через LEFT JOIN: так sqlc выводит для них NULL-типы (*int32/*int64); строка всегда находится.
SELECT w.week_index, w.online_minutes, w.session_minutes,
       m.cpu_load_avg_bp, m.cpu_load_max_bp, m.ram_avg_bp, m.ram_max_bp, m.gpu_load_avg_bp, m.gpu_load_max_bp,
       m.cpu_temp_avg_deci, m.cpu_temp_max_deci, m.gpu_temp_avg_deci, m.gpu_temp_max_deci,
       w.cpu_hot_hours, w.gpu_hot_hours, w.problem_hours, w.network_slow_hours,
       w.unexpected_shutdowns, w.bugchecks, w.peripheral_disconnects,
       m.system_disk_free_mb, m.system_disk_free_bp, w.smart_rank
FROM wk w
LEFT JOIN wk m ON m.week_index = w.week_index
ORDER BY w.week_index DESC;

-- name: ComputerHealthHourlyPoints :many
-- Панель ПК: точки графика по часам за выбранный период (≤ 8 суток — проверяет API), диапазоном по hour.
SELECT hour, online_minutes, session_minutes, cpu_load_avg_bp, cpu_load_max_bp, ram_avg_bp, ram_max_bp,
       gpu_load_avg_bp, gpu_load_max_bp, cpu_temp_avg_deci, cpu_temp_max_deci, gpu_temp_avg_deci, gpu_temp_max_deci
FROM computer_health_hourly
WHERE club_id = sqlc.arg(club_id) AND computer_id = sqlc.arg(computer_id)
  AND hour >= sqlc.arg(from_at)::timestamptz AND hour < sqlc.arg(to_at)::timestamptz
ORDER BY hour;
