-- Вход сотрудника, текущий пользователь, выбор клуба (server/internal/auth).
-- users — платформенная таблица без club_id и без RLS (0004_rls.sql): запросы к ней — исключения
-- tenant-filter в sqlc.yaml (GetUserByPhone, GetUserAuth, UpdateUserPasswordHash).
-- club_memberships читаются в транзакции с app.user_id (политика member_self_read) или app.club_id.

-- name: GetUserByPhone :one
-- Вход по телефону (Laravel: AuthController::login → Auth::attempt(['phone' => …])). Индекс users_phone_unique.
SELECT id, name, phone, password, is_superadmin, blocked_at
FROM users
WHERE phone = sqlc.arg(phone);

-- name: GetUserAuth :one
-- Пользователь запроса (без пароля): блокировка и флаг суперадмина проверяются на каждом запросе
-- (Laravel: SetCurrentClub — «A blocked account loses access at once»).
SELECT id, name, phone, is_superadmin, blocked_at
FROM users
WHERE id = sqlc.arg(id);

-- name: UpdateUserPasswordHash :execrows
-- Перехеш при входе (bcrypt → Argon2id). Условие на старый хэш: параллельная смена пароля не затирается.
-- Laravel: SessionGuard::rehashPasswordIfRequired() — forceFill()->save() обновляет и updated_at.
UPDATE users
SET password = sqlc.arg(new_hash), updated_at = now()
WHERE id = sqlc.arg(id) AND password = sqlc.arg(old_hash);

-- name: ListUserMemberships :many
-- Клубы пользователя: сначала членства сотрудника, затем по id (Laravel: AuthController::login,
-- orderByRaw("CASE WHEN role IN (STAFF) THEN 0 ELSE 1 END")->orderBy('id')). Нужен app.user_id.
SELECT m.id, m.club_id, m.role, c.name AS club_name
FROM club_memberships m
JOIN clubs c ON c.id = m.club_id
WHERE m.user_id = sqlc.arg(user_id)
ORDER BY CASE WHEN m.role IN ('owner', 'admin', 'manager', 'cashier', 'operator') THEN 0 ELSE 1 END, m.id;

-- name: GetMembershipAuth :one
-- Роль и выбор прав сотрудника в клубе (ClubPermissions::allows).
SELECT role, permissions, can_manage_integrations
FROM club_memberships
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);

-- name: ListClubUserIDsByRole :many
-- Члены клуба с ролью, кроме пользователя (StaffShifts::onShift: whereIn user_id members with role, except one).
SELECT user_id
FROM club_memberships
WHERE club_id = sqlc.arg(club_id) AND role = sqlc.arg(role) AND user_id <> sqlc.arg(except_user_id)
ORDER BY user_id;
