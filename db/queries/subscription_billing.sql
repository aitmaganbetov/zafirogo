-- Счета за подписку платформы, оплата по Kaspi и скидка клуба (тема Б, docs/upstream-2026-11.md; BR-180…BR-184).
-- Laravel: app/Services/SubscriptionBilling.php, BillingController, Superadmin\InvoiceController,
-- Superadmin\ClubController::discount(). Расчёты — server/internal/subscription.
--
-- Панель клуба (пул zafiro_app, транзакция клуба): Subscription* — выставить счёт и отменить свой неоплаченный;
-- колонок оплаты и подписки этот пул не меняет (миграции 0035, 0039).
-- Кабинет суперадмина (пул zafiro_platform): Superadmin* — отметить оплату, отменить, скидка клуба.

-- ---------------------------------------------------------------------------
-- Панель клуба
-- ---------------------------------------------------------------------------

-- name: SubscriptionPayablePlans :many
-- SubscriptionBilling::payablePlans(): тарифы, за которые клуб может заплатить, — включённые и с ценой.
SELECT id, name, price_monthly::bigint AS price_monthly
FROM saas_plans
WHERE is_active AND price_monthly IS NOT NULL
ORDER BY price_monthly, id;

-- name: SubscriptionKaspiSetting :one
-- SubscriptionBilling::kaspi(): ссылка Kaspi Pay и получатель платформы (PlatformSetting 'billing_kaspi'); не секрет.
SELECT value FROM platform_settings WHERE key = 'billing_kaspi';

-- name: SubscriptionClubComputers :one
-- SubscriptionBilling::computers(): все зарегистрированные ПК клуба (в сети или нет).
SELECT count(*)::bigint FROM club_computers WHERE club_id = sqlc.arg(club_id);

-- name: SubscriptionClubReseller :one
-- Реселлер клуба для снимка скидки и процента в счёте (SubscriptionBilling::discountFor(), open()); неактивный —
-- ни скидки, ни процента. Нет реселлера — нет строки. Пул клуба видит только реселлера своего клуба (0034).
SELECT r.id, r.commission_bp, r.discount_percent, r.is_active
FROM clubs c
JOIN resellers r ON r.id = c.reseller_id
WHERE c.id = sqlc.arg(club_id);

-- name: SubscriptionHasPendingInvoice :one
-- SubscriptionBilling::open(): у клуба один неоплаченный счёт.
SELECT EXISTS (SELECT 1 FROM subscription_invoices WHERE club_id = sqlc.arg(club_id) AND status = 'pending');

-- name: SubscriptionInsertInvoice :one
-- SubscriptionBilling::open(): неоплаченный счёт со снимком числа ПК, цены, скидки и реселлера. Второй неоплаченный
-- счёт клуба — ошибка 23505 индекса subscription_invoices_club_id_pending_unique (BR-184, GAP-046).
INSERT INTO subscription_invoices (club_id, saas_plan_id, months, computers, price_per_computer, discount_percent, amount,
                                   reseller_id, reseller_bp, status, created_by, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(saas_plan_id), sqlc.arg(months), sqlc.arg(computers), sqlc.arg(price_per_computer),
        sqlc.arg(discount_percent), sqlc.arg(amount), sqlc.narg(reseller_id), sqlc.narg(reseller_bp), 'pending',
        sqlc.arg(created_by), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: SubscriptionClubInvoices :many
-- BillingController::show(): 12 последних счетов клуба с названием тарифа.
SELECT i.id, i.saas_plan_id, p.name AS plan_name, i.months, i.computers, i.price_per_computer, i.discount_percent, i.amount,
       i.status, i.created_at, i.paid_at, i.period_ends_at
FROM subscription_invoices i
JOIN saas_plans p ON p.id = i.saas_plan_id
WHERE i.club_id = sqlc.arg(club_id)
ORDER BY i.id DESC
LIMIT 12;

-- name: SubscriptionInvoiceStatus :one
-- Счёт своего клуба (чужой — нет строки: 404, как скоуп клуба в Laravel).
SELECT status FROM subscription_invoices WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: SubscriptionCancelInvoice :execrows
-- SubscriptionBilling::cancel(): только pending → canceled; 0 строк — счёт уже закрыт.
UPDATE subscription_invoices SET status = 'canceled', updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND status = 'pending';

-- ---------------------------------------------------------------------------
-- Кабинет суперадмина: «Оплаты» (Superadmin\InvoiceController)
-- ---------------------------------------------------------------------------

-- name: SuperadminPendingInvoices :many
-- index(): счета, ожидающие оплаты, старые первыми.
SELECT i.id, i.club_id, c.name::text AS club_name, p.name::text AS plan_name, i.months, i.computers, i.price_per_computer,
       i.discount_percent, i.amount, i.created_at
FROM subscription_invoices i
JOIN clubs c ON c.id = i.club_id
JOIN saas_plans p ON p.id = i.saas_plan_id
WHERE i.status = 'pending'
ORDER BY i.id;

-- name: SuperadminClosedInvoices :many
-- index(): оплаченные и отменённые счета, новые первыми, по 30.
SELECT i.id, i.club_id, c.name::text AS club_name, p.name::text AS plan_name, i.months, i.amount, i.status, i.created_at, i.paid_at
FROM subscription_invoices i
JOIN clubs c ON c.id = i.club_id
JOIN saas_plans p ON p.id = i.saas_plan_id
WHERE i.status <> 'pending'
ORDER BY i.id DESC
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: SuperadminCountClosedInvoices :one
SELECT count(*)::bigint FROM subscription_invoices WHERE status <> 'pending';

-- name: SuperadminLockInvoice :one
-- markPaid()/cancel(): счёт под блокировкой строки (двойная оплата и оплата отменённого — проверка статуса под ней).
SELECT i.id, i.club_id, c.name::text AS club_name, i.saas_plan_id, i.months, i.amount, i.reseller_id, i.reseller_bp, i.status
FROM subscription_invoices i
JOIN clubs c ON c.id = i.club_id
WHERE i.id = sqlc.arg(id)
FOR UPDATE OF i;

-- name: SuperadminMarkInvoicePaid :exec
-- markPaid(): счёт оплачен; начисление реселлеру — только если реселлер в счёте остался (BR-186).
UPDATE subscription_invoices
SET status = 'paid', paid_at = sqlc.arg(now), confirmed_by = sqlc.arg(confirmed_by), period_ends_at = sqlc.arg(period_ends_at),
    reseller_amount = sqlc.narg(reseller_amount), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: SuperadminCancelInvoice :exec
UPDATE subscription_invoices SET status = 'canceled', updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: SuperadminInsertPlatformMoney :exec
-- Журнал денег платформы (0038, только добавление): invoice_paid:<id счёта>, reseller_accrual:<id счёта>.
-- Повтор ключа — ошибка уникальности: движение денег не может быть записано дважды.
INSERT INTO platform_money_log (kind, idempotency_key, amount, club_id, invoice_id, reseller_id, actor_user_id, meta, created_at)
VALUES (sqlc.arg(kind), sqlc.arg(idempotency_key), sqlc.arg(amount), sqlc.narg(club_id), sqlc.narg(invoice_id), sqlc.narg(reseller_id),
        sqlc.narg(actor_user_id), sqlc.narg(meta), sqlc.arg(now));

-- ---------------------------------------------------------------------------
-- Кабинет суперадмина: скидка клуба (Superadmin\ClubController::discount)
-- ---------------------------------------------------------------------------

-- name: SuperadminLockSubscriptionDiscount :one
SELECT id, discount_percent, discount_until, discount_note
FROM club_subscriptions
WHERE club_id = sqlc.arg(club_id)::bigint
FOR UPDATE;

-- name: SuperadminSetSubscriptionDiscount :exec
UPDATE club_subscriptions
SET discount_percent = sqlc.arg(discount_percent), discount_until = sqlc.narg(discount_until), discount_note = sqlc.narg(discount_note),
    updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id)::bigint;
