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
-- по hour (индекс — первичный ключ club_id, computer_id, hour); границы недель считает сервер в том же поясе.
-- Средние — взвешенные по минутам онлайн (целочисленно, без float). Часы с проблемами — часы онлайн, в которые была
-- открыта хоть одна проблема журнала computer_health_events (индекс history_index). Нет измерений — -1 (smart_rank — 0:
-- 1 ok, 2 warning, 3 failing).
SELECT date_trunc('week', h.hour AT TIME ZONE sqlc.arg(tz)::text)::date AS week_start,
       count(*)::bigint AS hours,
       coalesce(sum(h.online_minutes), 0)::bigint AS online_minutes,
       coalesce(sum(h.session_minutes), 0)::bigint AS session_minutes,
       coalesce(sum(h.cpu_load_avg_bp::bigint * h.online_minutes)
           / nullif(sum(h.online_minutes) FILTER (WHERE h.cpu_load_avg_bp IS NOT NULL), 0), -1)::integer AS cpu_load_avg_bp,
       coalesce(max(h.cpu_load_max_bp), -1)::integer AS cpu_load_max_bp,
       coalesce(sum(h.ram_avg_bp::bigint * h.online_minutes)
           / nullif(sum(h.online_minutes) FILTER (WHERE h.ram_avg_bp IS NOT NULL), 0), -1)::integer AS ram_avg_bp,
       coalesce(max(h.ram_max_bp), -1)::integer AS ram_max_bp,
       coalesce(sum(h.gpu_load_avg_bp::bigint * h.online_minutes)
           / nullif(sum(h.online_minutes) FILTER (WHERE h.gpu_load_avg_bp IS NOT NULL), 0), -1)::integer AS gpu_load_avg_bp,
       coalesce(max(h.gpu_load_max_bp), -1)::integer AS gpu_load_max_bp,
       coalesce(sum(h.cpu_temp_avg_deci::bigint * h.online_minutes)
           / nullif(sum(h.online_minutes) FILTER (WHERE h.cpu_temp_avg_deci IS NOT NULL), 0), -1)::integer AS cpu_temp_avg_deci,
       coalesce(max(h.cpu_temp_max_deci), -1)::integer AS cpu_temp_max_deci,
       coalesce(sum(h.gpu_temp_avg_deci::bigint * h.online_minutes)
           / nullif(sum(h.online_minutes) FILTER (WHERE h.gpu_temp_avg_deci IS NOT NULL), 0), -1)::integer AS gpu_temp_avg_deci,
       coalesce(max(h.gpu_temp_max_deci), -1)::integer AS gpu_temp_max_deci,
       count(*) FILTER (WHERE h.cpu_temp_max_deci >= sqlc.arg(cpu_hot_deci)::integer)::bigint AS cpu_hot_hours,
       count(*) FILTER (WHERE h.gpu_temp_max_deci >= sqlc.arg(gpu_hot_deci)::integer)::bigint AS gpu_hot_hours,
       count(*) FILTER (WHERE EXISTS (
           SELECT 1 FROM computer_health_events e
           WHERE e.club_id = h.club_id AND e.computer_id = h.computer_id
             AND e.raised_at < h.hour + interval '1 hour' AND (e.cleared_at IS NULL OR e.cleared_at > h.hour)))::bigint
           AS problem_hours,
       coalesce(sum(h.unexpected_shutdowns), 0)::bigint AS unexpected_shutdowns,
       coalesce(sum(h.bugchecks), 0)::bigint AS bugchecks,
       coalesce(sum(h.peripheral_disconnects), 0)::bigint AS peripheral_disconnects,
       coalesce(min(h.system_disk_free_mb), -1)::bigint AS system_disk_free_mb,
       coalesce(min(h.system_disk_free_mb * 10000 / nullif(h.system_disk_total_mb, 0)), -1)::bigint AS system_disk_free_bp,
       coalesce(max(CASE h.smart_worst WHEN 'failing' THEN 3 WHEN 'warning' THEN 2 WHEN 'ok' THEN 1 END), 0)::integer AS smart_rank
FROM computer_health_hourly h
WHERE h.club_id = sqlc.arg(club_id) AND h.computer_id = sqlc.arg(computer_id)
  AND h.hour >= sqlc.arg(from_at)::timestamptz AND h.hour < sqlc.arg(to_at)::timestamptz
GROUP BY 1
ORDER BY 1 DESC;

-- name: ComputerHealthHourlyPoints :many
-- Панель ПК: точки графика по часам за выбранный период (≤ 8 суток — проверяет API), диапазоном по hour.
SELECT hour, online_minutes, session_minutes, cpu_load_avg_bp, cpu_load_max_bp, ram_avg_bp, ram_max_bp,
       gpu_load_avg_bp, gpu_load_max_bp, cpu_temp_avg_deci, cpu_temp_max_deci, gpu_temp_avg_deci, gpu_temp_max_deci
FROM computer_health_hourly
WHERE club_id = sqlc.arg(club_id) AND computer_id = sqlc.arg(computer_id)
  AND hour >= sqlc.arg(from_at)::timestamptz AND hour < sqlc.arg(to_at)::timestamptz
ORDER BY hour;
