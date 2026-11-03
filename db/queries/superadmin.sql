-- Суперадминка (Этап 3, группа 6): кабинет платформы поверх всех клубов. Пул zafiro_platform (политика
-- platform_access), поэтому запросы по всем клубам — это и есть их назначение; в sqlc.yaml они исключены из
-- правила tenant-filter префиксом имени «Superadmin». Laravel: app/Http/Controllers/Superadmin/*.
-- Время — параметром (@now из часов приложения, V-1), не now() в SQL.

-- ---------------------------------------------------------------------------
-- Сервер (DashboardController)
-- ---------------------------------------------------------------------------

-- name: SuperadminServerBuckets :many
-- server_metrics по бакетам: AVG(load_1), AVG(memory_used_mb * 100.0 / memory_total_mb).
SELECT (minute - minute % sqlc.arg(bucket)::bigint)::bigint AS bucket,
       COALESCE(AVG(load_1), 0)::float8 AS load_1,
       COALESCE(AVG(memory_used_mb * 100.0 / NULLIF(memory_total_mb, 0)), 0)::float8 AS memory
FROM server_metrics
WHERE minute >= sqlc.arg(from_minute)::bigint
GROUP BY 1
ORDER BY 1;

-- name: SuperadminServerMaxLoad :one
SELECT COALESCE(MAX(load_1), 0)::float8 FROM server_metrics WHERE minute >= sqlc.arg(from_minute)::bigint;

-- name: SuperadminRequestBuckets :many
-- request_metrics по бакетам и каналам.
SELECT (minute - minute % sqlc.arg(bucket)::bigint)::bigint AS bucket, channel,
       SUM(requests)::bigint AS requests, SUM(errors)::bigint AS errors, SUM(duration_ms)::bigint AS duration_ms
FROM request_metrics
WHERE minute >= sqlc.arg(from_minute)::bigint
GROUP BY 1, 2
ORDER BY 1, 2;

-- name: SuperadminRequestChannels :many
-- Запросы за последние минуты по каналам («Запросы за 15 минут»).
SELECT channel, SUM(requests)::bigint AS requests, SUM(errors)::bigint AS errors, SUM(duration_ms)::bigint AS duration_ms
FROM request_metrics
WHERE minute >= sqlc.arg(from_minute)::bigint
GROUP BY channel;

-- name: SuperadminLatestServerMetric :one
-- Минута последнего снимка сервера: по ней видно, жив ли планировщик.
SELECT minute FROM server_metrics ORDER BY minute DESC LIMIT 1;

-- name: SuperadminClubLoad :many
-- DashboardController::clubLoad(): нагрузка клубов за период, самые нагруженные сверху (до 50).
SELECT club_id,
       SUM(agent_requests)::bigint AS agent, SUM(api_requests)::bigint AS api,
       SUM(panel_requests)::bigint AS panel, SUM(guest_requests)::bigint AS guest,
       SUM(agent_requests + api_requests + panel_requests + guest_requests)::bigint AS requests,
       MAX(agent_requests + api_requests + panel_requests + guest_requests)::bigint AS peak,
       SUM(errors)::bigint AS errors, SUM(duration_ms)::bigint AS duration_ms
FROM club_request_metrics
WHERE minute >= sqlc.arg(from_minute)::bigint
GROUP BY club_id
ORDER BY requests DESC, club_id
LIMIT sqlc.arg(row_limit)::int;

-- name: SuperadminClubLoadPoints :many
SELECT (minute - minute % sqlc.arg(bucket)::bigint)::bigint AS bucket, club_id,
       SUM(agent_requests + api_requests + panel_requests + guest_requests)::bigint AS requests
FROM club_request_metrics
WHERE minute >= sqlc.arg(from_minute)::bigint AND club_id = ANY(sqlc.arg(club_ids)::bigint[])
GROUP BY 1, 2
ORDER BY 1;

-- name: SuperadminClubNames :many
SELECT id, name FROM clubs WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: SuperadminComputerCount :one
SELECT count(*)::bigint FROM club_computers;

-- name: SuperadminAgentVersions :many
-- Версии агента на ПК (10 самых частых).
SELECT agent_version::text AS agent_version, count(*)::bigint AS aggregate
FROM club_computers
WHERE agent_version IS NOT NULL
GROUP BY agent_version
ORDER BY aggregate DESC, agent_version DESC
LIMIT 10;

-- ---------------------------------------------------------------------------
-- Настройки платформы (PlatformSetting)
-- ---------------------------------------------------------------------------

-- name: SuperadminSetting :one
SELECT value FROM platform_settings WHERE key = sqlc.arg(key);

-- name: SuperadminPutSetting :exec
-- PlatformSetting::write(): значение и кто изменил.
INSERT INTO platform_settings (key, value, updated_by, created_at, updated_at)
VALUES (sqlc.arg(key), sqlc.arg(value), sqlc.narg(updated_by), sqlc.arg(now), sqlc.arg(now))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at;

-- name: SuperadminDeleteSetting :exec
-- PlatformSetting::write($key, null): строка удаляется.
DELETE FROM platform_settings WHERE key = sqlc.arg(key);

-- name: SuperadminAgentReleaseTargets :many
-- ПК клубов с активной подпиской и известной версией агента; устаревшие отбираются в Go (AgentUpdate::isOutdated),
-- там же — настройка клуба «Автообновление агента» (settings.club.agent_auto_update, по умолчанию включено).
SELECT c.club_id, c.id, c.agent_version::text AS agent_version,
       COALESCE(cl.settings -> 'club' ->> 'agent_auto_update', '')::text AS auto_update
FROM club_computers c
JOIN clubs cl ON cl.id = c.club_id
JOIN club_subscriptions s ON s.club_id = c.club_id
WHERE c.agent_version IS NOT NULL
  AND ((s.status = 'active' AND (s.current_period_ends_at IS NULL OR s.current_period_ends_at > sqlc.arg(now)))
       OR (s.status = 'trialing' AND s.trial_ends_at > sqlc.arg(now)))
ORDER BY c.club_id, c.id;

-- ---------------------------------------------------------------------------
-- Журнал платформы (PlatformLog) и журналы клубов (LogController)
-- ---------------------------------------------------------------------------

-- name: SuperadminPlatformLogs :many
-- PlatformLog::query()->with(user, club)->latest('id'): фильтры клуб и событие, страница.
SELECT l.id, l.club_id, COALESCE(cl.name, '')::text AS club_name, l.user_id, COALESCE(u.name, '')::text AS user_name, l.event, l.description, l.details,
       l.ip_address, l.created_at
FROM platform_logs l
LEFT JOIN users u ON u.id = l.user_id
LEFT JOIN clubs cl ON cl.id = l.club_id
WHERE (sqlc.narg(club_id)::bigint IS NULL OR l.club_id = sqlc.narg(club_id)::bigint)
  AND (sqlc.arg(event)::text = '' OR l.event = sqlc.arg(event)::text)
ORDER BY l.id DESC
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: SuperadminCountPlatformLogs :one
SELECT count(*)::bigint FROM platform_logs
WHERE (sqlc.narg(club_id)::bigint IS NULL OR club_id = sqlc.narg(club_id)::bigint)
  AND (sqlc.arg(event)::text = '' OR event = sqlc.arg(event)::text);

-- name: SuperadminActivityLogs :many
-- ActivityLog::query()->withoutGlobalScope('club'): журналы всех клубов.
SELECT l.id, l.club_id, COALESCE(cl.name, '')::text AS club_name, l.user_id, COALESCE(u.name, '')::text AS user_name, l.event, l.description, l.details,
       l.ip_address, l.created_at
FROM activity_logs l
LEFT JOIN users u ON u.id = l.user_id
LEFT JOIN clubs cl ON cl.id = l.club_id
WHERE (sqlc.narg(club_id)::bigint IS NULL OR l.club_id = sqlc.narg(club_id)::bigint)
  AND (sqlc.arg(event)::text = '' OR l.event = sqlc.arg(event)::text)
ORDER BY l.id DESC
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: SuperadminCountActivityLogs :one
SELECT count(*)::bigint FROM activity_logs
WHERE (sqlc.narg(club_id)::bigint IS NULL OR club_id = sqlc.narg(club_id)::bigint)
  AND (sqlc.arg(event)::text = '' OR event = sqlc.arg(event)::text);

-- name: SuperadminInsertPlatformLog :one
-- PlatformLogger::record().
INSERT INTO platform_logs (user_id, club_id, event, description, subject_type, subject_id, details, ip_address,
                           user_agent, created_at, updated_at)
VALUES (sqlc.narg(user_id), sqlc.narg(club_id), sqlc.arg(event), sqlc.arg(description), sqlc.narg(subject_type),
        sqlc.narg(subject_id), sqlc.narg(details), sqlc.arg(ip_address), sqlc.narg(user_agent), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: SuperadminCountPlatformEventSince :one
-- TelegramController::show(): кодов восстановления за 30 дней.
SELECT count(*)::bigint FROM platform_logs WHERE event = sqlc.arg(event) AND created_at >= sqlc.arg(since);

-- ---------------------------------------------------------------------------
-- Клубы (ClubController)
-- ---------------------------------------------------------------------------

-- name: SuperadminClubs :many
-- ClubController::index(): клубы с подзапросами, поиск, фильтр подписки, сортировка, страница по 25.
-- «В сети» — из присутствия агентов (Valkey): массивы online_club_ids/online_counts.
WITH online AS (
    SELECT unnest(sqlc.arg(online_club_ids)::bigint[]) AS club_id, unnest(sqlc.arg(online_counts)::bigint[]) AS online
), rows AS (
    SELECT c.id, c.name::text AS name, c.slug::text AS slug, c.created_at,
           COALESCE((SELECT u.name FROM users u JOIN club_memberships m ON m.user_id = u.id
             WHERE m.club_id = c.id AND m.role = 'owner' ORDER BY m.id LIMIT 1), '')::text AS owner_name,
           (SELECT count(*) FROM club_computers pc WHERE pc.club_id = c.id)::bigint AS computers_count,
           COALESCE((SELECT o.online FROM online o WHERE o.club_id = c.id), 0)::bigint AS online_count,
           (SELECT count(*) FROM club_computers pc WHERE pc.club_id = c.id AND pc.current_session_id IS NOT NULL
               AND pc.session_ends_at > sqlc.arg(now) AND (pc.current_session_user_id IS NOT NULL OR pc.session_guest))::bigint AS busy_count,
           (SELECT COALESCE(SUM(r.agent_requests), 0) FROM club_request_metrics r
             WHERE r.club_id = c.id AND r.minute >= sqlc.arg(load_from)::bigint)::bigint AS agent_requests,
           (SELECT COALESCE(SUM(r.api_requests), 0) FROM club_request_metrics r
             WHERE r.club_id = c.id AND r.minute >= sqlc.arg(load_from)::bigint)::bigint AS api_requests,
           -- Нет значения — 0001-01-01 (как NULL MySQL: первым при ASC, последним при DESC); в Go — «нет».
           COALESCE((SELECT MAX(a.created_at) FROM activity_logs a WHERE a.club_id = c.id AND a.event = 'auth.login'), '0001-01-01 00:00:00+00'::timestamptz)::timestamptz AS last_login_at,
           (SELECT count(*) FROM activity_logs a WHERE a.club_id = c.id
               AND a.event IN ('tariff.purchased', 'time.purchased', 'computer.guest_session')
               AND a.created_at >= sqlc.arg(day_ago))::bigint AS sessions_today,
           (SELECT count(*) FROM webhook_deliveries w WHERE w.club_id = c.id AND w.status = 'failed'
               AND w.created_at >= sqlc.arg(day_ago))::bigint AS webhook_failures,
           s.id AS subscription_id, s.status AS subscription_status, s.trial_ends_at, s.current_period_ends_at,
           COALESCE(CASE WHEN s.status = 'trialing' THEN s.trial_ends_at ELSE s.current_period_ends_at END, '0001-01-01 00:00:00+00'::timestamptz)::timestamptz AS subscription_ends_at,
           (s.id IS NOT NULL AND ((s.status = 'active' AND (s.current_period_ends_at IS NULL OR s.current_period_ends_at > sqlc.arg(now)))
               OR (s.status = 'trialing' AND s.trial_ends_at > sqlc.arg(now))))::boolean AS subscription_active
    FROM clubs c
    LEFT JOIN club_subscriptions s ON s.club_id = c.id
    WHERE (sqlc.arg(search)::text = '' OR c.name ILIKE '%' || sqlc.arg(search)::text || '%' OR c.slug ILIKE '%' || sqlc.arg(search)::text || '%')
)
SELECT * FROM rows
WHERE sqlc.arg(subscription)::text = '' OR (sqlc.arg(subscription)::text = 'active') = subscription_active
ORDER BY
    CASE WHEN sqlc.arg(sort)::text = 'name' AND sqlc.arg(direction)::text = 'asc' THEN name END ASC,
    CASE WHEN sqlc.arg(sort)::text = 'name' AND sqlc.arg(direction)::text = 'desc' THEN name END DESC,
    CASE WHEN sqlc.arg(sort)::text = 'online' AND sqlc.arg(direction)::text = 'asc' THEN online_count END ASC,
    CASE WHEN sqlc.arg(sort)::text = 'online' AND sqlc.arg(direction)::text = 'desc' THEN online_count END DESC,
    CASE WHEN sqlc.arg(sort)::text = 'busy' AND sqlc.arg(direction)::text = 'asc' THEN busy_count END ASC,
    CASE WHEN sqlc.arg(sort)::text = 'busy' AND sqlc.arg(direction)::text = 'desc' THEN busy_count END DESC,
    CASE WHEN sqlc.arg(sort)::text = 'agent_requests' AND sqlc.arg(direction)::text = 'asc' THEN agent_requests END ASC,
    CASE WHEN sqlc.arg(sort)::text = 'agent_requests' AND sqlc.arg(direction)::text = 'desc' THEN agent_requests END DESC,
    CASE WHEN sqlc.arg(sort)::text = 'api_requests' AND sqlc.arg(direction)::text = 'asc' THEN api_requests END ASC,
    CASE WHEN sqlc.arg(sort)::text = 'api_requests' AND sqlc.arg(direction)::text = 'desc' THEN api_requests END DESC,
    CASE WHEN sqlc.arg(sort)::text = 'last_login' AND sqlc.arg(direction)::text = 'asc' THEN last_login_at END ASC,
    CASE WHEN sqlc.arg(sort)::text = 'last_login' AND sqlc.arg(direction)::text = 'desc' THEN last_login_at END DESC,
    CASE WHEN sqlc.arg(sort)::text = 'sessions' AND sqlc.arg(direction)::text = 'asc' THEN sessions_today END ASC,
    CASE WHEN sqlc.arg(sort)::text = 'sessions' AND sqlc.arg(direction)::text = 'desc' THEN sessions_today END DESC,
    CASE WHEN sqlc.arg(sort)::text = 'webhooks' AND sqlc.arg(direction)::text = 'asc' THEN webhook_failures END ASC,
    CASE WHEN sqlc.arg(sort)::text = 'webhooks' AND sqlc.arg(direction)::text = 'desc' THEN webhook_failures END DESC,
    CASE WHEN sqlc.arg(sort)::text = 'ends' AND sqlc.arg(direction)::text = 'asc' THEN subscription_ends_at END ASC,
    CASE WHEN sqlc.arg(sort)::text = 'ends' AND sqlc.arg(direction)::text = 'desc' THEN subscription_ends_at END DESC,
    name ASC, id ASC
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: SuperadminCountClubs :one
SELECT count(*)::bigint
FROM clubs c
LEFT JOIN club_subscriptions s ON s.club_id = c.id
WHERE (sqlc.arg(search)::text = '' OR c.name ILIKE '%' || sqlc.arg(search)::text || '%' OR c.slug ILIKE '%' || sqlc.arg(search)::text || '%')
  AND (sqlc.arg(subscription)::text = ''
       OR (sqlc.arg(subscription)::text = 'active') = (s.id IS NOT NULL AND ((s.status = 'active' AND (s.current_period_ends_at IS NULL OR s.current_period_ends_at > sqlc.arg(now)))
            OR (s.status = 'trialing' AND s.trial_ends_at > sqlc.arg(now)))));

-- name: SuperadminClubTotals :one
-- Итоги: всего клубов, с активной подпиской, из них на пробном периоде.
SELECT (SELECT count(*) FROM clubs)::bigint AS clubs,
       (SELECT count(*) FROM club_subscriptions s WHERE (s.status = 'active' AND (s.current_period_ends_at IS NULL OR s.current_period_ends_at > sqlc.arg(now)))
            OR (s.status = 'trialing' AND s.trial_ends_at > sqlc.arg(now)))::bigint AS active,
       (SELECT count(*) FROM club_subscriptions s WHERE s.status = 'trialing' AND s.trial_ends_at > sqlc.arg(now))::bigint AS trial;

-- name: SuperadminClubAgentVersions :many
-- Версии агентов ПК клубов страницы (устаревшие считаются в Go: версии сравниваются как версии).
SELECT club_id, agent_version::text AS agent_version, count(*)::bigint AS aggregate
FROM club_computers
WHERE club_id = ANY(sqlc.arg(club_ids)::bigint[]) AND agent_version IS NOT NULL
GROUP BY club_id, agent_version;

-- name: SuperadminLoadSnapshotsOnline :many
-- ClubAttention: максимум «в сети» по клубу в окне [from, to).
SELECT club_id, MAX(online)::bigint AS online
FROM club_load_snapshots
WHERE captured_at >= sqlc.arg(from_at) AND captured_at < sqlc.arg(to_at)
GROUP BY club_id;

-- name: SuperadminActiveSubscriptionClubs :many
SELECT s.club_id FROM club_subscriptions s
WHERE (s.status = 'active' AND (s.current_period_ends_at IS NULL OR s.current_period_ends_at > sqlc.arg(now)))
   OR (s.status = 'trialing' AND s.trial_ends_at > sqlc.arg(now));

-- name: SuperadminWebhookFailures :many
-- ClubAttention: клубы с ≥ 3 недоставленными вебхуками за сутки.
SELECT club_id, count(*)::bigint AS failures
FROM webhook_deliveries
WHERE status = 'failed' AND created_at >= sqlc.arg(since)
GROUP BY club_id
HAVING count(*) >= sqlc.arg(min_failures)::bigint
ORDER BY club_id;

-- name: SuperadminEndingSubscriptions :many
-- ClubAttention: активные подписки, которые закончатся до until.
SELECT s.club_id, s.status, s.trial_ends_at, s.current_period_ends_at
FROM club_subscriptions s
WHERE ((s.status = 'active' AND (s.current_period_ends_at IS NULL OR s.current_period_ends_at > sqlc.arg(now)))
       OR (s.status = 'trialing' AND s.trial_ends_at > sqlc.arg(now)))
  AND ((s.status = 'active' AND s.current_period_ends_at <= sqlc.arg(until))
       OR (s.status = 'trialing' AND s.trial_ends_at <= sqlc.arg(until)))
ORDER BY s.id;

-- name: SuperadminClub :one
-- ClubController::show(): клуб и подписка с тарифом.
SELECT c.id, c.name::text AS name, c.slug::text AS slug, c.timezone::text AS timezone, c.created_at,
       s.id AS subscription_id, s.status AS subscription_status, s.saas_plan_id, s.trial_ends_at, s.current_period_ends_at,
       COALESCE(p.name, '')::text AS plan_name
FROM clubs c
LEFT JOIN club_subscriptions s ON s.club_id = c.id
LEFT JOIN saas_plans p ON p.id = s.saas_plan_id
WHERE c.id = sqlc.arg(id);

-- name: SuperadminClubMembers :many
-- Участники клуба: owner, admin, manager, operator, прочие; по 30.
SELECT m.id, m.role::text AS role, u.name::text AS name, u.phone::text AS phone, u.blocked_at
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)::bigint
ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'manager' THEN 2 WHEN 'operator' THEN 3 ELSE 4 END, m.id
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: SuperadminCountClubMembers :one
SELECT count(*)::bigint FROM club_memberships WHERE club_id = sqlc.arg(club_id)::bigint;

-- name: SuperadminClubComputerTotal :one
SELECT count(*)::bigint FROM club_computers WHERE club_id = sqlc.arg(club_id)::bigint;

-- name: SuperadminClubHeatmap :many
-- Средняя доля занятых ПК по дню недели и часу (время клуба) за 28 дней снимков.
SELECT weekday::int AS weekday, hour::int AS hour,
       AVG(busy * 100.0 / computers)::float8 AS busy, AVG(online)::float8 AS online
FROM club_load_snapshots
WHERE club_id = sqlc.arg(club_id)::bigint AND captured_at >= sqlc.arg(since) AND computers > 0
GROUP BY weekday, hour;

-- name: SuperadminClubActivity :many
-- 15 последних записей журнала клуба.
SELECT l.id, l.club_id, COALESCE(cl.name, '')::text AS club_name, l.user_id, COALESCE(u.name, '')::text AS user_name, l.event, l.description, l.details,
       l.ip_address, l.created_at
FROM activity_logs l
LEFT JOIN users u ON u.id = l.user_id
LEFT JOIN clubs cl ON cl.id = l.club_id
WHERE l.club_id = sqlc.arg(club_id)::bigint
ORDER BY l.id DESC
LIMIT 15;

-- name: SuperadminClubExists :one
SELECT name::text FROM clubs WHERE id = sqlc.arg(id);

-- name: SuperadminLockSubscription :one
SELECT id, saas_plan_id, status, trial_ends_at, current_period_ends_at
FROM club_subscriptions
WHERE club_id = sqlc.arg(club_id)::bigint
FOR UPDATE;

-- name: SuperadminCheapestActivePlan :one
-- SaasPlan::query()->where('is_active', true)->orderBy('price_monthly')->value('id') (MySQL: NULL первыми).
SELECT id FROM saas_plans WHERE is_active ORDER BY price_monthly ASC NULLS FIRST, id LIMIT 1;

-- name: SuperadminInsertSubscription :one
INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, current_period_ends_at, created_at, updated_at)
VALUES (sqlc.arg(club_id)::bigint, sqlc.arg(saas_plan_id), 'active', sqlc.arg(now), sqlc.arg(ends_at), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: SuperadminExtendSubscription :exec
UPDATE club_subscriptions
SET saas_plan_id = sqlc.arg(saas_plan_id), status = 'active', current_period_ends_at = sqlc.arg(ends_at),
    canceled_at = NULL, updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id)::bigint;

-- name: SuperadminSuspendSubscription :exec
UPDATE club_subscriptions SET status = 'suspended', canceled_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id)::bigint;

-- name: SuperadminChangeSubscriptionPlan :exec
UPDATE club_subscriptions SET saas_plan_id = sqlc.arg(saas_plan_id), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id)::bigint;

-- name: SuperadminExpireDueSubscriptions :many
-- ExpireSubscriptions: оплаченные подписки с прошедшим сроком → expired (только active; триалы остаются trialing — Q-036).
UPDATE club_subscriptions s
SET status = 'expired', updated_at = sqlc.arg(now)
FROM clubs c
WHERE c.id = s.club_id AND s.status = 'active' AND s.current_period_ends_at IS NOT NULL
  AND s.current_period_ends_at <= sqlc.arg(now)
RETURNING s.id, s.club_id, c.name::text AS club_name, s.current_period_ends_at;

-- ---------------------------------------------------------------------------
-- Тарифы платформы (PlanController)
-- ---------------------------------------------------------------------------

-- name: SuperadminPlans :many
-- Планы по цене (MySQL: NULL первыми), с числом подписок.
SELECT p.id, p.slug::text AS slug, p.name::text AS name, p.price_monthly, p.trial_days, p.features, p.is_active,
       (SELECT count(*) FROM club_subscriptions s WHERE s.saas_plan_id = p.id)::bigint AS clubs_count
FROM saas_plans p
ORDER BY p.price_monthly ASC NULLS FIRST, p.id;

-- name: SuperadminPlanSlugTaken :one
SELECT EXISTS (SELECT 1 FROM saas_plans WHERE lower(slug) = lower(sqlc.arg(slug)::text) AND id <> sqlc.arg(except_id)::bigint);

-- name: SuperadminPlanExists :one
SELECT name::text FROM saas_plans WHERE id = sqlc.arg(id);

-- name: SuperadminLockPlan :one
SELECT id, slug::text AS slug, name::text AS name, price_monthly, trial_days, features, is_active
FROM saas_plans WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: SuperadminInsertPlan :one
INSERT INTO saas_plans (slug, name, price_monthly, trial_days, features, is_active, created_at, updated_at)
VALUES (sqlc.arg(slug), sqlc.arg(name), sqlc.narg(price_monthly), sqlc.arg(trial_days), sqlc.narg(features),
        sqlc.arg(is_active), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: SuperadminUpdatePlan :exec
UPDATE saas_plans
SET slug = sqlc.arg(slug), name = sqlc.arg(name), price_monthly = sqlc.narg(price_monthly), trial_days = sqlc.arg(trial_days),
    features = sqlc.narg(features), is_active = sqlc.arg(is_active), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id);

-- ---------------------------------------------------------------------------
-- Пользователи (UserController)
-- ---------------------------------------------------------------------------

-- name: SuperadminUsers :many
-- Аккаунты по имени, поиск по имени или цифрам телефона (≥ 3), фильтр, по 30. second_factor — TOTP настроен (Этап 7).
SELECT id, name::text AS name, phone::text AS phone, is_superadmin, blocked_at,
       EXISTS (SELECT 1 FROM superadmin_totp t WHERE t.user_id = users.id AND t.confirmed_at IS NOT NULL) AS second_factor
FROM users
WHERE (sqlc.arg(search)::text = '' OR name ILIKE '%' || sqlc.arg(search)::text || '%'
       OR (length(sqlc.arg(digits)::text) >= 3 AND phone LIKE '%' || sqlc.arg(digits)::text || '%'))
  AND (sqlc.arg(filter)::text = '' OR (sqlc.arg(filter)::text = 'blocked' AND blocked_at IS NOT NULL)
       OR (sqlc.arg(filter)::text = 'superadmin' AND is_superadmin))
ORDER BY name, id
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: SuperadminCountUsers :one
SELECT count(*)::bigint
FROM users
WHERE (sqlc.arg(search)::text = '' OR name ILIKE '%' || sqlc.arg(search)::text || '%'
       OR (length(sqlc.arg(digits)::text) >= 3 AND phone LIKE '%' || sqlc.arg(digits)::text || '%'))
  AND (sqlc.arg(filter)::text = '' OR (sqlc.arg(filter)::text = 'blocked' AND blocked_at IS NOT NULL)
       OR (sqlc.arg(filter)::text = 'superadmin' AND is_superadmin));

-- name: SuperadminUserMemberships :many
-- Членства аккаунтов страницы во всех клубах.
SELECT m.user_id, m.club_id, m.role::text AS role, c.name::text AS club_name
FROM club_memberships m
JOIN clubs c ON c.id = m.club_id
WHERE m.user_id = ANY(sqlc.arg(user_ids)::bigint[])
ORDER BY m.user_id, m.id;

-- name: SuperadminLockUser :one
SELECT id, name::text AS name, is_superadmin, blocked_at FROM users WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: SuperadminSetUserBlocked :exec
UPDATE users SET blocked_at = sqlc.narg(blocked_at), updated_at = sqlc.arg(now) WHERE id = sqlc.arg(id);

-- name: SuperadminSetUserSuperadmin :exec
UPDATE users SET is_superadmin = sqlc.arg(is_superadmin), updated_at = sqlc.arg(now) WHERE id = sqlc.arg(id);

-- name: SuperadminUserHasMembership :one
-- «Панель своего клуба» в меню кабинета: есть ли у суперадмина членство.
SELECT EXISTS (SELECT 1 FROM club_memberships WHERE user_id = sqlc.arg(user_id));

-- ---------------------------------------------------------------------------
-- Новости платформы (Superadmin\NewsController, NewsManager)
-- ---------------------------------------------------------------------------

-- name: SuperadminPlatformNews :many
SELECT id, title, body, image_path, is_published, created_at
FROM news
WHERE club_id IS NULL
ORDER BY created_at DESC NULLS LAST, id DESC
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: SuperadminCountPlatformNews :one
SELECT count(*)::bigint FROM news WHERE club_id IS NULL;

-- name: SuperadminLockPlatformNews :one
SELECT id, title, image_path FROM news WHERE club_id IS NULL AND id = sqlc.arg(id) FOR UPDATE;

-- name: SuperadminInsertPlatformNews :one
INSERT INTO news (club_id, title, body, image_path, is_published, created_by, created_at, updated_at)
VALUES (NULL, sqlc.arg(title), sqlc.arg(body), sqlc.narg(image_path), sqlc.arg(is_published), sqlc.arg(created_by),
        sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: SuperadminUpdatePlatformNews :exec
UPDATE news
SET title = sqlc.arg(title), body = sqlc.arg(body), is_published = sqlc.arg(is_published),
    image_path = sqlc.narg(image_path), updated_at = sqlc.arg(now)
WHERE club_id IS NULL AND id = sqlc.arg(id);

-- name: SuperadminDeletePlatformNews :exec
DELETE FROM news WHERE club_id IS NULL AND id = sqlc.arg(id);

-- name: SuperadminPutPlatformNewsImage :exec
INSERT INTO news_images (news_id, club_id, content_type, data, created_at)
VALUES (sqlc.arg(news_id), NULL, sqlc.arg(content_type), sqlc.arg(data), sqlc.arg(now))
ON CONFLICT (news_id) DO UPDATE SET content_type = EXCLUDED.content_type, data = EXCLUDED.data,
                                    created_at = EXCLUDED.created_at;

-- name: SuperadminDeletePlatformNewsImage :exec
DELETE FROM news_images WHERE club_id IS NULL AND news_id = sqlc.arg(news_id);

-- ---------------------------------------------------------------------------
-- Языки и переводы (LanguageController, TranslationTransfer)
-- ---------------------------------------------------------------------------

-- name: SuperadminLanguages :many
-- Языки по sort_order, id; переведено — строки с ключом из исходного каталога (строки ru), клубов — по locale.
SELECT l.id, l.code::text AS code, l.name::text AS name, l.is_enabled, l.is_system,
       (SELECT count(*) FROM translations t WHERE t.language_id = l.id
           AND t.key_hash IN (SELECT s.key_hash FROM translations s JOIN languages sl ON sl.id = s.language_id WHERE sl.code = 'ru'))::bigint AS translated_count,
       (SELECT count(*) FROM clubs c WHERE c.locale = l.code)::bigint AS clubs_count
FROM languages l
ORDER BY l.sort_order, l.id;

-- name: SuperadminLanguage :one
SELECT id, code::text AS code, name::text AS name, is_enabled, is_system FROM languages WHERE id = sqlc.arg(id);

-- name: SuperadminLanguageByCode :one
SELECT id, code::text AS code, name::text AS name, is_enabled, is_system FROM languages WHERE code = sqlc.arg(code);

-- name: SuperadminLockLanguage :one
SELECT id, code::text AS code, name::text AS name, is_enabled, is_system FROM languages WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: SuperadminLanguageCodeTaken :one
SELECT EXISTS (SELECT 1 FROM languages WHERE code = sqlc.arg(code));

-- name: SuperadminInsertLanguage :one
INSERT INTO languages (code, name, is_enabled, is_system, sort_order, created_at, updated_at)
VALUES (sqlc.arg(code), sqlc.arg(name), true, false, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM languages),
        sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: SuperadminUpdateLanguage :exec
UPDATE languages SET name = sqlc.arg(name), is_enabled = sqlc.arg(is_enabled), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id);

-- name: SuperadminTouchLanguage :exec
-- Версия переводов (PD-12) — max(updated_at): удаление строки перевода сдвигает время языка.
UPDATE languages SET updated_at = sqlc.arg(now) WHERE id = sqlc.arg(id);

-- name: SuperadminDeleteLanguage :exec
DELETE FROM languages WHERE id = sqlc.arg(id);

-- name: SuperadminLanguageClubsExist :one
SELECT EXISTS (SELECT 1 FROM clubs WHERE locale = sqlc.arg(code));

-- name: SuperadminOtherEnabledLanguageExists :one
SELECT EXISTS (SELECT 1 FROM languages WHERE is_enabled AND id <> sqlc.arg(id));

-- name: SuperadminResetClubLocale :execrows
-- Клубы удалённого языка переходят на язык по умолчанию (locale = NULL).
UPDATE clubs SET locale = NULL WHERE locale = sqlc.arg(code);

-- name: SuperadminSourceStrings :many
-- TranslationCatalog::sourceKeys(): исходный каталог — строки ru (импорт database/translations/ru.php).
SELECT t."group"::text AS grp, t.key, t.key_hash::text AS key_hash, t.value
FROM translations t
JOIN languages l ON l.id = t.language_id
WHERE l.code = 'ru'
ORDER BY t.id;

-- name: SuperadminLanguageStrings :many
SELECT id, key_hash::text AS key_hash, value FROM translations WHERE language_id = sqlc.arg(language_id);

-- name: SuperadminUpsertTranslation :exec
INSERT INTO translations (language_id, "group", key, key_hash, value, created_at, updated_at)
VALUES (sqlc.arg(language_id), sqlc.arg(grp), sqlc.arg(key), sqlc.arg(key_hash), sqlc.arg(value), sqlc.arg(now), sqlc.arg(now))
ON CONFLICT (language_id, "group", key_hash) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at;

-- name: SuperadminDeleteTranslations :exec
DELETE FROM translations WHERE language_id = sqlc.arg(language_id) AND id = ANY(sqlc.arg(ids)::bigint[]);
