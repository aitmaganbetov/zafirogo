-- Реселлеры (тема Д, docs/upstream-2026-11.md; BR-185…BR-189). Laravel: app/Http/Controllers/Superadmin/ResellerController.php,
-- app/Http/Controllers/ResellerController.php, SettingsController::reseller(), app/Models/Reseller.php.
--
-- Все запросы Reseller* — данные платформы (resellers, reseller_payouts, platform_money_log, привязка клуба), пул
-- zafiro_platform: кабинет суперадмина, кабинет реселлера, привязка клуба по коду (clubs.reseller_id пул клуба
-- изменить не может — триггер clubs_reseller_guard, 0034). Исключение — ResellerOfClub: карточка «Реселлер» настроек
-- клуба, пул клуба (RLS показывает ему только реселлера своего клуба).
-- Суммы — тиыны (bigint), процент реселлера — базисные пункты; «заработано», «выплачено» считает SQL (BR-187).

-- ---------------------------------------------------------------------------
-- Кабинет суперадмина: «Реселлеры» (Superadmin\ResellerController)
-- ---------------------------------------------------------------------------

-- name: ResellerList :many
-- index(): реселлеры, новые первыми, по 30; поиск — по коду или имени (pattern — уже экранированный шаблон LIKE).
-- earned — withSum(invoices where status = 'paid', 'reseller_amount'); paid_out — withSum(payouts, 'amount').
SELECT r.id, r.user_id, r.code::text AS code, r.name::text AS name, r.commission_bp, r.discount_percent, r.is_active,
       u.name::text AS user_name, u.phone::text AS user_phone,
       (SELECT coalesce(sum(i.reseller_amount), 0)::bigint FROM subscription_invoices i
         WHERE i.reseller_id = r.id AND i.status = 'paid') AS earned,
       (SELECT coalesce(sum(p.amount), 0)::bigint FROM reseller_payouts p WHERE p.reseller_id = r.id) AS paid_out
FROM resellers r
JOIN users u ON u.id = r.user_id
WHERE (sqlc.arg(pattern)::text = '' OR r.code ILIKE sqlc.arg(pattern)::text OR r.name ILIKE sqlc.arg(pattern)::text)
ORDER BY r.id DESC
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: ResellerCount :one
SELECT count(*)::bigint
FROM resellers r
WHERE (sqlc.arg(pattern)::text = '' OR r.code ILIKE sqlc.arg(pattern)::text OR r.name ILIKE sqlc.arg(pattern)::text);

-- name: ResellerClubsOf :many
-- with('clubs:id,name,reseller_id,reseller_attached_at'): клубы реселлеров страницы.
SELECT c.id, c.name::text AS name, c.reseller_id::bigint AS reseller_id, c.reseller_attached_at
FROM clubs c
WHERE c.reseller_id = ANY (sqlc.arg(reseller_ids)::bigint[])
ORDER BY c.reseller_id, c.id;

-- name: ResellerUserByPhone :one
-- store(): аккаунт с этим телефоном (существующий сохраняет пароль). Блокировка — два одновременных создания
-- реселлера одному аккаунту идут по очереди.
SELECT id, name::text AS name FROM users WHERE phone = sqlc.arg(phone) FOR UPDATE;

-- name: ResellerCodeTaken :one
-- Rule::unique('resellers', 'code').
SELECT EXISTS (SELECT 1 FROM resellers WHERE code = sqlc.arg(code));

-- name: ResellerUserIsReseller :one
-- store(): «Аккаунт с этим телефоном уже реселлер.»; вход на хосте реселлера и кабинет — тот же признак.
SELECT EXISTS (SELECT 1 FROM resellers WHERE user_id = sqlc.arg(user_id));

-- name: ResellerCreateUser :one
-- store(): User::create(['name', 'phone', 'password']) — новый аккаунт реселлера (хэш Argon2id).
INSERT INTO users (name, phone, password, created_at, updated_at)
VALUES (sqlc.arg(name), sqlc.arg(phone), sqlc.arg(password), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: ResellerInsert :one
INSERT INTO resellers (user_id, code, name, commission_bp, discount_percent, is_active, created_at, updated_at)
VALUES (sqlc.arg(user_id), sqlc.arg(code), sqlc.arg(name), sqlc.arg(commission_bp), sqlc.arg(discount_percent), true,
        sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: ResellerLock :one
-- update(), payout(), detach(), destroy(): реселлер под блокировкой строки (нет строки — 404).
SELECT id, user_id, code::text AS code, name::text AS name, commission_bp, discount_percent, is_active
FROM resellers
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: ResellerUpdate :exec
-- update(): процент, скидка клубам и «код действует».
UPDATE resellers
SET commission_bp = sqlc.arg(commission_bp), discount_percent = sqlc.arg(discount_percent), is_active = sqlc.arg(is_active),
    updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id);

-- name: ResellerPayoutByKey :one
-- Повтор формы «Записать выплату» с тем же ключом идемпотентности: выплата уже записана (§3.6).
SELECT id, reseller_id, amount, note FROM reseller_payouts WHERE idempotency_key = sqlc.arg(idempotency_key)::text;

-- name: ResellerInsertPayout :one
-- payout(): $reseller->payouts()->create([...$data, 'created_by' => …]). Повтор ключа — 23505 индекса
-- reseller_payouts_idempotency_key_unique.
INSERT INTO reseller_payouts (reseller_id, amount, note, created_by, idempotency_key, created_at, updated_at)
VALUES (sqlc.arg(reseller_id), sqlc.arg(amount), sqlc.narg(note), sqlc.arg(created_by), sqlc.arg(idempotency_key)::text,
        sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: ResellerInsertPayoutMoney :exec
-- Журнал денег платформы (0038, только добавление): reseller_payout:<id выплаты>. Остаётся и после удаления
-- реселлера, когда сами выплаты удалены каскадом (BR-189, UQ-10).
INSERT INTO platform_money_log (kind, idempotency_key, amount, reseller_id, payout_id, actor_user_id, meta, created_at)
VALUES ('reseller_payout', sqlc.arg(idempotency_key), sqlc.arg(amount), sqlc.arg(reseller_id), sqlc.arg(payout_id),
        sqlc.arg(actor_user_id), sqlc.narg(meta), sqlc.arg(now));

-- name: ResellerLockClub :one
-- detach() и привязка по коду: клуб под блокировкой строки.
SELECT id, name::text AS name, reseller_id FROM clubs WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: ResellerDetachClub :exec
-- detach(): $club->forceFill(['reseller_id' => null, 'reseller_attached_at' => null])->save().
UPDATE clubs SET reseller_id = NULL, reseller_attached_at = NULL, updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND reseller_id = sqlc.arg(reseller_id)::bigint;

-- name: ResellerDetachAll :execrows
-- destroy(): Club::query()->where('reseller_id', …)->update([...]) — массовое обновление, updated_at не меняется.
UPDATE clubs SET reseller_id = NULL, reseller_attached_at = NULL WHERE reseller_id = sqlc.arg(reseller_id)::bigint;

-- name: ResellerDelete :exec
-- destroy(): выплаты удаляются каскадом, в счетах reseller_id → NULL (reseller_amount остаётся), аккаунт остаётся.
DELETE FROM resellers WHERE id = sqlc.arg(id);

-- ---------------------------------------------------------------------------
-- Настройки клуба: карточка «Реселлер» (SettingsController::reseller)
-- ---------------------------------------------------------------------------

-- name: ResellerOfClub :one
-- settings/club.blade.php, карточка #reseller: реселлер клуба из сессии (пул клуба; нет реселлера — нет строки).
-- Процент реселлера клубу не отдаётся.
SELECT r.name::text AS name, r.code::text AS code, r.discount_percent, r.is_active
FROM clubs c
JOIN resellers r ON r.id = c.reseller_id
WHERE c.id = sqlc.arg(club_id);

-- name: ResellerActiveByCode :one
-- reseller(): Reseller::query()->where('code', $code)->where('is_active', true)->first().
SELECT id, code::text AS code, name::text AS name FROM resellers WHERE code = sqlc.arg(code) AND is_active;

-- name: ResellerAttachClub :execrows
-- reseller(): клуб привязывается один раз (0 строк — уже привязан: гонка двух вводов, §3.6).
UPDATE clubs SET reseller_id = sqlc.arg(reseller_id)::bigint, reseller_attached_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND reseller_id IS NULL;

-- ---------------------------------------------------------------------------
-- Кабинет реселлера (ResellerController::dashboard). Каждый запрос — только по reseller_id вошедшего.
-- ---------------------------------------------------------------------------

-- name: ResellerByUser :one
-- Reseller::query()->where('user_id', …)->first(): нет строки — 403 «Этот аккаунт не реселлер Zafiro.».
SELECT id, code::text AS code, name::text AS name, commission_bp, discount_percent, is_active
FROM resellers
WHERE user_id = sqlc.arg(user_id);

-- name: ResellerTotals :one
-- BR-187: заработано — сумма reseller_amount оплаченных счетов реселлера; выплачено — сумма выплат.
SELECT (SELECT coalesce(sum(i.reseller_amount), 0)::bigint FROM subscription_invoices i
         WHERE i.reseller_id = sqlc.arg(reseller_id)::bigint AND i.status = 'paid') AS earned,
       (SELECT coalesce(sum(p.amount), 0)::bigint FROM reseller_payouts p
         WHERE p.reseller_id = sqlc.arg(reseller_id)::bigint) AS paid_out;

-- name: ResellerCabinetClubs :many
-- Клубы реселлера: недавно привязанные первыми (в MySQL NULL при DESC — последние), с подпиской и тарифом.
SELECT c.id, c.name::text AS name, c.city, c.reseller_attached_at,
       s.status AS subscription_status, s.current_period_ends_at, s.trial_ends_at, p.name AS plan_name
FROM clubs c
LEFT JOIN club_subscriptions s ON s.club_id = c.id
LEFT JOIN saas_plans p ON p.id = s.saas_plan_id
WHERE c.reseller_id = sqlc.arg(reseller_id)::bigint
ORDER BY c.reseller_attached_at DESC NULLS LAST, c.id
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: ResellerCabinetClubsCount :one
SELECT count(*)::bigint FROM clubs c WHERE c.reseller_id = sqlc.arg(reseller_id)::bigint;

-- name: ResellerCabinetEarnings :many
-- «Начисления»: оплаченные счета реселлера, новые первыми (latest('paid_at')->latest('id')), по 20.
SELECT i.id, c.name::text AS club_name, i.months, i.amount, i.reseller_bp, i.reseller_amount, i.paid_at
FROM subscription_invoices i
JOIN clubs c ON c.id = i.club_id
WHERE i.reseller_id = sqlc.arg(reseller_id)::bigint AND i.status = 'paid'
ORDER BY i.paid_at DESC, i.id DESC
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: ResellerCabinetEarningsCount :one
SELECT count(*)::bigint FROM subscription_invoices i
WHERE i.reseller_id = sqlc.arg(reseller_id)::bigint AND i.status = 'paid';

-- name: ResellerCabinetPayouts :many
-- «Выплаты»: 20 последних (latest('id')->limit(20)).
SELECT p.amount, p.note, p.created_at
FROM reseller_payouts p
WHERE p.reseller_id = sqlc.arg(reseller_id)::bigint
ORDER BY p.id DESC
LIMIT 20;
