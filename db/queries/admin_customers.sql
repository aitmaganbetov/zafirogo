-- Клиенты для REST админки (Этап 2, B1). Laravel: app/Http/Controllers/UserController.php index().

-- name: AdminListCustomers :many
-- Клиенты клуба (членства не из ClubRoles::STAFF) с балансами, по user_id (как orderBy('user_id'));
-- keyset: after_user_id — последний user_id предыдущей страницы. Поиск — подстрока имени или телефона
-- (search — экранированный шаблон ILIKE, пусто — все).
SELECT m.user_id, u.name, u.phone, u.email, m.role, m.discount::text AS discount, m.total_spent,
       COALESCE(b.balance, 0)::bigint AS balance, COALESCE(b.bonus_balance, 0)::bigint AS bonus_balance,
       (b.user_id IS NOT NULL)::boolean AS has_balance
FROM club_memberships m
JOIN users u ON u.id = m.user_id
LEFT JOIN member_balances b ON b.club_id = m.club_id AND b.user_id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND m.role NOT IN ('owner', 'admin', 'manager', 'cashier', 'operator')
  AND m.user_id > sqlc.arg(after_user_id)
  AND (sqlc.arg(search)::text = '' OR u.name ILIKE sqlc.arg(search)::text OR u.phone ILIKE sqlc.arg(search)::text)
ORDER BY m.user_id
LIMIT sqlc.arg(page_size);

-- name: AdminGetCustomer :one
-- Клиент клуба (не сотрудник) с балансами; нет — 404.
SELECT m.user_id, u.name, u.phone, u.email, m.role, m.discount::text AS discount, m.total_spent,
       COALESCE(b.balance, 0)::bigint AS balance, COALESCE(b.bonus_balance, 0)::bigint AS bonus_balance,
       (b.user_id IS NOT NULL)::boolean AS has_balance
FROM club_memberships m
JOIN users u ON u.id = m.user_id
LEFT JOIN member_balances b ON b.club_id = m.club_id AND b.user_id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND m.user_id = sqlc.arg(user_id)
  AND m.role NOT IN ('owner', 'admin', 'manager', 'cashier', 'operator');

-- name: AdminListCustomersBefore :many
-- Предыдущая страница списка клиентов (keyset назад): user_id < before_user_id, по убыванию; вызывающий
-- переворачивает строки. Остальное — как AdminListCustomers.
SELECT m.user_id, u.name, u.phone, u.email, m.role, m.discount::text AS discount, m.total_spent,
       COALESCE(b.balance, 0)::bigint AS balance, COALESCE(b.bonus_balance, 0)::bigint AS bonus_balance,
       (b.user_id IS NOT NULL)::boolean AS has_balance
FROM club_memberships m
JOIN users u ON u.id = m.user_id
LEFT JOIN member_balances b ON b.club_id = m.club_id AND b.user_id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND m.role NOT IN ('owner', 'admin', 'manager', 'cashier', 'operator')
  AND m.user_id < sqlc.arg(before_user_id)
  AND (sqlc.arg(search)::text = '' OR u.name ILIKE sqlc.arg(search)::text OR u.phone ILIKE sqlc.arg(search)::text)
ORDER BY m.user_id DESC
LIMIT sqlc.arg(page_size);

-- name: AdminCountCustomers :one
-- «Всего: :count» ($users->total()): клиенты клуба (с тем же поиском).
SELECT count(*)::bigint
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND m.role NOT IN ('owner', 'admin', 'manager', 'cashier', 'operator')
  AND (sqlc.arg(search)::text = '' OR u.name ILIKE sqlc.arg(search)::text OR u.phone ILIKE sqlc.arg(search)::text);

-- name: AdminAccountsLocked :many
-- UserController::index() $staffElsewhere / isAccountLocked(): аккаунты, которые открывают панель какого-либо
-- клуба (сотрудник в любом клубе, withoutGlobalScope('club')) или суперадминку. Пул zafiro_platform (все клубы);
-- users — платформенная таблица.
SELECT u.id
FROM users u
WHERE u.id = ANY(sqlc.arg(user_ids)::bigint[])
  AND (u.is_superadmin
       OR EXISTS (SELECT 1 FROM club_memberships m
                  WHERE m.user_id = u.id AND m.role IN ('owner', 'admin', 'manager', 'cashier', 'operator')));

-- name: AdminUserConflicts :one
-- StoreUserRequest / UserController::update(): Rule::unique('users', 'phone') и Rule::unique('users', 'email')
-- (->ignore(user)). users — платформенная таблица (без club_id и RLS). Пустой phone/email — не проверяется.
SELECT EXISTS (SELECT 1 FROM users p WHERE sqlc.arg(phone)::text <> '' AND p.phone = sqlc.arg(phone)::text)::boolean AS phone_taken,
       EXISTS (SELECT 1 FROM users e WHERE sqlc.arg(email)::text <> '' AND e.email = sqlc.arg(email)::text
                                       AND e.id <> sqlc.arg(except_user_id)::bigint)::boolean AS email_taken;

-- name: AdminCreateCustomer :one
-- UserController::store(): User::create(name, phone, email, password) + ClubMembership::create(role client,
-- discount) в одном запросе (users — без club_id; членство — этого клуба). Пароль — уже хеш (Argon2id).
WITH new_user AS (
    INSERT INTO users (name, phone, email, password, created_at, updated_at)
    VALUES (sqlc.arg(name), sqlc.arg(phone), sqlc.narg(email), sqlc.arg(password_hash), sqlc.arg(now), sqlc.arg(now))
    RETURNING id
)
INSERT INTO club_memberships (club_id, user_id, role, discount, created_at, updated_at)
SELECT sqlc.arg(club_id)::bigint, n.id, 'client', sqlc.arg(discount)::text::numeric, sqlc.arg(now), sqlc.arg(now)
FROM new_user n
RETURNING user_id;

-- name: AdminUpdateCustomerAccount :execrows
-- UserController::update(): $user->fill(['name', 'email'])->save() — только клиенту этого клуба.
UPDATE users
SET name = sqlc.arg(name), email = sqlc.narg(email), updated_at = sqlc.arg(now)
WHERE users.id = sqlc.arg(user_id)
  AND EXISTS (SELECT 1 FROM club_memberships m
              WHERE m.club_id = sqlc.arg(club_id) AND m.user_id = users.id
                AND m.role NOT IN ('owner', 'admin', 'manager', 'cashier', 'operator'));

-- name: AdminUpdateCustomerDiscount :one
-- UserController::update(): $membership->update(['discount' => …]) (numeric(5,2) округляет, как decimal MySQL).
UPDATE club_memberships
SET discount = sqlc.arg(discount)::text::numeric, updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id)
  AND role NOT IN ('owner', 'admin', 'manager', 'cashier', 'operator')
RETURNING discount::text;

-- name: AdminSetCustomerPassword :execrows
-- UserController::resetPassword(): $user->forceFill(['password' => …])->save() — только клиенту этого клуба.
UPDATE users
SET password = sqlc.arg(password_hash), updated_at = sqlc.arg(now)
WHERE users.id = sqlc.arg(user_id)
  AND EXISTS (SELECT 1 FROM club_memberships m
              WHERE m.club_id = sqlc.arg(club_id) AND m.user_id = users.id
                AND m.role NOT IN ('owner', 'admin', 'manager', 'cashier', 'operator'));
