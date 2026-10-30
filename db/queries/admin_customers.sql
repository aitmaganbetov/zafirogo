-- Клиенты для REST админки (Этап 2, B1). Laravel: app/Http/Controllers/UserController.php index().

-- name: AdminListCustomers :many
-- Клиенты клуба (членства не из ClubRoles::STAFF) с балансами, по user_id (как orderBy('user_id'));
-- keyset: after_user_id — последний user_id предыдущей страницы. Поиск — подстрока имени или телефона
-- (search — экранированный шаблон ILIKE, пусто — все).
SELECT m.user_id, u.name, u.phone, m.role, m.discount::text AS discount, m.total_spent,
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
SELECT m.user_id, u.name, u.phone, m.role, m.discount::text AS discount, m.total_spent,
       COALESCE(b.balance, 0)::bigint AS balance, COALESCE(b.bonus_balance, 0)::bigint AS bonus_balance,
       (b.user_id IS NOT NULL)::boolean AS has_balance
FROM club_memberships m
JOIN users u ON u.id = m.user_id
LEFT JOIN member_balances b ON b.club_id = m.club_id AND b.user_id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND m.user_id = sqlc.arg(user_id)
  AND m.role NOT IN ('owner', 'admin', 'manager', 'cashier', 'operator');
