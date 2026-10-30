-- Регистрация владельца с клубом и приглашения в клуб (Этап 3, F1).
-- Laravel: AuthController::register(), ClubInvitationController::show()/accept().
-- Выполняются под пулом zafiro_platform (клуба ещё нет / приглашение ищется по токену до выбора клуба).

-- name: RegisterCreateUser :one
-- User::create(): users — платформенная таблица (без club_id и RLS).
INSERT INTO users (name, phone, email, password, created_at, updated_at)
VALUES (sqlc.arg(name), sqlc.arg(phone), sqlc.narg(email), sqlc.arg(password), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: RegisterPhoneTaken :one
-- 'phone' => 'unique:users'.
SELECT EXISTS (SELECT 1 FROM users WHERE phone = sqlc.arg(phone));

-- name: RegisterCreateClub :one
-- Club::create(['name', 'slug']): clubs — платформенная таблица.
INSERT INTO clubs (name, slug, created_at, updated_at)
VALUES (sqlc.arg(name), sqlc.arg(slug), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: RegisterTrialPlan :one
-- SaasPlan::firstOrCreate(['slug' => 'trial'], ['name' => 'Пробный период', 'price_monthly' => null,
-- 'trial_days' => 14, 'is_active' => true]): существующий тариф не меняется.
INSERT INTO saas_plans (slug, name, price_monthly, trial_days, is_active, created_at, updated_at)
VALUES ('trial', 'Пробный период', NULL, 14, true, sqlc.arg(now), sqlc.arg(now))
ON CONFLICT (slug) DO UPDATE SET slug = saas_plans.slug
RETURNING id, trial_days;

-- name: RegisterCreateSubscription :exec
-- $club->subscription()->create(['status' => 'trialing', 'started_at' => now(), 'trial_ends_at' => …]).
INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, trial_ends_at, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(saas_plan_id), 'trialing', sqlc.arg(now), sqlc.arg(trial_ends_at), sqlc.arg(now), sqlc.arg(now));

-- name: RegisterCreateMembership :exec
-- $club->memberships()->create(['user_id', 'role']).
INSERT INTO club_memberships (club_id, user_id, role, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(user_id), sqlc.arg(role), sqlc.arg(now), sqlc.arg(now));

-- name: InvitationByTokenHash :one
-- ClubInvitationController::findValidInvitation(): не принято и не истекло.
SELECT i.id, i.club_id, i.email, i.role, c.name AS club_name
FROM club_invitations i
JOIN clubs c ON c.id = i.club_id
WHERE i.token_hash = sqlc.arg(token_hash) AND i.accepted_at IS NULL AND i.expires_at > sqlc.arg(now);

-- name: LockInvitation :one
-- lockForUpdate()->findOrFail($invitation->getKey()).
SELECT accepted_at, expires_at
FROM club_invitations
WHERE id = sqlc.arg(id) AND club_id = sqlc.arg(club_id)
FOR UPDATE;

-- name: InvitationMarkAccepted :exec
UPDATE club_invitations SET accepted_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND club_id = sqlc.arg(club_id);

-- name: InvitationMembershipExists :one
-- ClubMembership::where('club_id', …)->where('user_id', …)->exists().
SELECT EXISTS (SELECT 1 FROM club_memberships WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id));

-- name: InvitationUserEmail :one
-- $user->email для проверки «приглашение для другого email». users — платформенная таблица.
SELECT email FROM users WHERE id = sqlc.arg(id);

-- name: ClubLocaleTimezone :one
-- Язык и часовой пояс клуба для /api/auth/me (LocaleResolver::forClub(), часы панели). clubs — без RLS.
SELECT locale, timezone FROM clubs WHERE id = sqlc.arg(id);
