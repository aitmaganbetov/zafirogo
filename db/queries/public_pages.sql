-- Публичные страницы (Этап 3, G7): платформенные таблицы, общие для всех клубов (исключение tenant-filter в sqlc.yaml).

-- name: PublicLanding :one
-- LandingController: SaasPlan::where('is_active', true)->max('trial_days') (0/NULL → 14 в обработчике) и объявленный
-- релиз агента (AgentUpdate::release(), platform_settings 'agent_release') для ссылки на установщик.
SELECT COALESCE((SELECT MAX(p.trial_days) FROM saas_plans p WHERE p.is_active), 0)::int AS trial_days,
       (SELECT s.value FROM platform_settings s WHERE s.key = 'agent_release') AS release;
