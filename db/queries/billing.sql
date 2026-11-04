-- Экран «Подписка» (Этап 3, G7): BillingController::show() — подписка клуба с планом (with('plan')).

-- name: BillingSubscription :one
SELECT s.status, s.trial_ends_at, s.current_period_ends_at, p.name AS plan_name
FROM club_subscriptions s
JOIN saas_plans p ON p.id = s.saas_plan_id
WHERE s.club_id = sqlc.arg(club_id);
