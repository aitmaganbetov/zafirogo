-- Работники, приглашения и права сотрудников для REST админки (Этап 3, G3).
-- Laravel: app/Http/Controllers/WorkerController.php, ClubInvitationController::store(), PermissionController.php.
-- Порядок персонала — как orderByRaw("CASE role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'manager' THEN 2
-- WHEN 'cashier' THEN 3 ELSE 4 END")->orderBy('id'); keyset — по (ранг, id).

-- name: AdminListWorkersAfter :many
-- WorkerController::index(): персонал клуба (ClubRoles::STAFF) после (after_rank, after_id).
SELECT m.id, m.user_id, u.name, u.phone, m.role,
       (CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'manager' THEN 2 WHEN 'cashier' THEN 3 ELSE 4 END)::int AS role_rank
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND m.role IN ('owner', 'admin', 'manager', 'cashier', 'operator')
  AND ((CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'manager' THEN 2 WHEN 'cashier' THEN 3 ELSE 4 END), m.id)
      > (sqlc.arg(after_rank)::int, sqlc.arg(after_id)::bigint)
ORDER BY role_rank, m.id
LIMIT sqlc.arg(page_size);

-- name: AdminListWorkersBefore :many
-- Предыдущая страница персонала (keyset назад, по убыванию; вызывающий переворачивает строки).
SELECT m.id, m.user_id, u.name, u.phone, m.role,
       (CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'manager' THEN 2 WHEN 'cashier' THEN 3 ELSE 4 END)::int AS role_rank
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND m.role IN ('owner', 'admin', 'manager', 'cashier', 'operator')
  AND ((CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'manager' THEN 2 WHEN 'cashier' THEN 3 ELSE 4 END), m.id)
      < (sqlc.arg(before_rank)::int, sqlc.arg(before_id)::bigint)
ORDER BY role_rank DESC, m.id DESC
LIMIT sqlc.arg(page_size);

-- name: AdminCountWorkers :one
SELECT count(*)::bigint
FROM club_memberships
WHERE club_id = sqlc.arg(club_id) AND role IN ('owner', 'admin', 'manager', 'cashier', 'operator');

-- name: AdminListPermissionStaff :many
-- PermissionController::index(): персонал кроме владельца (весь список, ->get()), признак «права изменены»
-- (empty($member->permissions): NULL, {} и [] — пусто).
SELECT m.id, m.user_id, u.name, u.phone, m.role,
       (m.permissions IS NOT NULL AND m.permissions NOT IN ('null'::jsonb, '{}'::jsonb, '[]'::jsonb))::boolean AS customized
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND m.role IN ('admin', 'manager', 'cashier', 'operator')
ORDER BY (CASE m.role WHEN 'admin' THEN 1 WHEN 'manager' THEN 2 WHEN 'cashier' THEN 3 ELSE 4 END), m.id;

-- name: AdminGetMembershipForUpdate :one
-- Членство клуба по id (route model binding {membership}) с блокировкой строки.
SELECT m.id, m.user_id, u.name, u.phone, m.role, m.permissions
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id) AND m.id = sqlc.arg(id)
FOR UPDATE OF m;

-- name: AdminGetMembership :one
SELECT m.id, m.user_id, u.name, u.phone, m.role, m.permissions
FROM club_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.club_id = sqlc.arg(club_id) AND m.id = sqlc.arg(id);

-- name: AdminMembershipByUserForUpdate :one
-- WorkerController::store(): ClubMembership::where('user_id', …)->lockForUpdate()->first() (клуб — глобальный скоуп).
SELECT id, role
FROM club_memberships
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id)
FOR UPDATE;

-- name: AdminInsertStaffMembership :exec
-- WorkerController::store(): ClubMembership::create(['user_id', 'role', 'discount' => 0]).
INSERT INTO club_memberships (club_id, user_id, role, discount, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(user_id), sqlc.arg(role), 0, sqlc.arg(now), sqlc.arg(now));

-- name: AdminSetMembershipRole :exec
-- WorkerController::store()/update()/destroy(): $membership->update(['role' => …]).
UPDATE club_memberships
SET role = sqlc.arg(role), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: AdminSetMembershipPermissions :exec
-- PermissionController::update()/reset(): forceFill(['permissions' => $given ?: null])->save().
UPDATE club_memberships
SET permissions = sqlc.narg(permissions), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: AdminInsertInvitation :one
-- ClubInvitationController::store(): ClubInvitation::create([...]) (токен хранится только хешем sha256).
INSERT INTO club_invitations (club_id, email, role, token_hash, invited_by, expires_at, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.narg(email), sqlc.arg(role), sqlc.arg(token_hash), sqlc.arg(invited_by),
        sqlc.arg(expires_at), sqlc.arg(now), sqlc.arg(now))
RETURNING id;
