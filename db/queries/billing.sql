-- Экран «Подписка» (Этап 3, G7): BillingController::show() — подписка клуба с планом (with('plan')).
-- Счета, скидка и оплата — db/queries/subscription_billing.sql.

-- name: BillingSubscription :one
SELECT s.status, s.trial_ends_at, s.current_period_ends_at, s.saas_plan_id, s.discount_percent, s.discount_until,
       p.name AS plan_name, p.price_monthly
FROM club_subscriptions s
JOIN saas_plans p ON p.id = s.saas_plan_id
WHERE s.club_id = sqlc.arg(club_id);
