-- Агент ПК: вход гостя, профиль, тарифы, пакеты, друзья, чат, новости, переводы, PIN сотрудника
-- (server/internal/agent/guestapi). Все запросы — в транзакции клуба ПК (db.InTenantTx, клуб — из сертификата).
-- users, languages, translations — платформенные таблицы без RLS: запросы к ним ограничены клубом через
-- членство (club_memberships) или карточку ПК (club_computers), чтобы гость видел только «свой» клуб.

-- ---------------------------------------------------------------------------
-- Гость и членство
-- ---------------------------------------------------------------------------

-- name: GuestUserInClub :one
-- Пользователь и его членство в клубе (Laravel: $user->currentMembership()).
SELECT u.id, u.name, u.phone, u.password, u.blocked_at, m.role, m.discount::float8 AS discount, m.total_spent
FROM users u
JOIN club_memberships m ON m.user_id = u.id
WHERE m.club_id = sqlc.arg(club_id) AND u.id = sqlc.arg(user_id);

-- name: GuestMembershipRole :one
-- Роль пользователя в клубе (ClubMembership::where('club_id')->where('user_id')->value('role')).
SELECT role
FROM club_memberships
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);

-- name: GuestCreateUser :one
-- Регистрация на ПК (AgentRegisterController): новый аккаунт Zafiro. Строка users вставляется, только если
-- ПК, с которого идёт регистрация, есть в клубе (FROM club_computers).
INSERT INTO users (name, phone, password, created_at, updated_at)
SELECT sqlc.arg(name), sqlc.arg(phone), sqlc.arg(password_hash), sqlc.arg(now), sqlc.arg(now)
FROM club_computers c
WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(computer_id)
RETURNING id;

-- name: GuestCreateMembership :exec
-- ClubMembership::create(['user_id', 'role' => ClubRoles::CLIENT, 'discount' => 0]).
INSERT INTO club_memberships (club_id, user_id, role, discount, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(user_id), 'client', 0, sqlc.arg(now), sqlc.arg(now));

-- name: GuestUpdateUserName :execrows
-- GuestProfileController::name(): forceFill(['name'])->save() — только члену этого клуба.
UPDATE users
SET name = sqlc.arg(name), updated_at = sqlc.arg(now)
WHERE users.id = sqlc.arg(user_id)
  AND EXISTS (SELECT 1 FROM club_memberships m WHERE m.club_id = sqlc.arg(club_id) AND m.user_id = users.id);

-- name: GuestUpdateUserPassword :execrows
-- GuestProfileController::password(): новый хэш пароля (Argon2id) — только члену этого клуба.
UPDATE users
SET password = sqlc.arg(password_hash), updated_at = sqlc.arg(now)
WHERE users.id = sqlc.arg(user_id)
  AND EXISTS (SELECT 1 FROM club_memberships m WHERE m.club_id = sqlc.arg(club_id) AND m.user_id = users.id);

-- name: GuestClubSubscription :one
-- ClubSubscription::isActive() (BR-130): вход гостя только в клуб с активной подпиской.
SELECT status, trial_ends_at, current_period_ends_at
FROM club_subscriptions
WHERE club_id = sqlc.arg(club_id);

-- name: GuestClubOwnerID :one
-- Владелец клуба: от его имени пишутся события без пользователя (resetGuestSession, стр. 170).
SELECT user_id
FROM club_memberships
WHERE club_id = sqlc.arg(club_id) AND role = 'owner'
ORDER BY id
LIMIT 1;

-- name: GuestUserNames :many
-- Имена пользователей клуба (журнал гостя: друзья в переводах).
SELECT u.id, u.name
FROM users u
JOIN club_memberships m ON m.user_id = u.id
WHERE m.club_id = sqlc.arg(club_id) AND u.id = ANY(sqlc.arg(ids)::bigint[]);

-- ---------------------------------------------------------------------------
-- ПК и клуб
-- ---------------------------------------------------------------------------

-- name: GuestComputer :one
-- ПК клуба с зоной (ComputerRef, зона для тарифов и минут).
SELECT c.id, c.club_zone_id, c.number, c.display_name, c.device_name, z.name AS zone_name, z.price_per_hour,
       c.current_session_id, c.current_session_user_id, c.session_guest, c.session_ends_at
FROM club_computers c
JOIN club_zones z ON z.club_id = c.club_id AND z.id = c.club_zone_id
WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(id);

-- Автобиллинг пакетом (S7-AB-2): то же, что GuestClubForAgent + GuestComputer + GuestUserInClub + GetMemberBalance
-- на каждый ПК, но для всех ПК пакета одного клуба одной транзакцией (guestapi abLoadClub).

-- name: GuestClubSettingsForAutoBilling :one
SELECT cl.settings
FROM clubs cl
WHERE cl.id = sqlc.arg(club_id);

-- name: GuestComputersForAutoBilling :many
-- Столбцы — как у GuestComputer (строка приводится к GuestComputerRow).
SELECT c.id, c.club_zone_id, c.number, c.display_name, c.device_name, z.name AS zone_name, z.price_per_hour,
       c.current_session_id, c.current_session_user_id, c.session_guest, c.session_ends_at
FROM club_computers c
JOIN club_zones z ON z.club_id = c.club_id AND z.id = c.club_zone_id
WHERE c.club_id = sqlc.arg(club_id) AND c.id = ANY(sqlc.arg(ids)::bigint[]);

-- name: GuestMembersForAutoBilling :many
-- Членства (скидка, траты) — как GuestUserInClub.
SELECT u.id, m.discount::float8 AS discount, m.total_spent
FROM users u
JOIN club_memberships m ON m.user_id = u.id
WHERE m.club_id = sqlc.arg(club_id) AND m.user_id = ANY(sqlc.arg(user_ids)::bigint[]);

-- name: GuestBalancesForAutoBilling :many
-- Основной баланс — как GetMemberBalance.
SELECT user_id, balance
FROM member_balances
WHERE club_id = sqlc.arg(club_id) AND user_id = ANY(sqlc.arg(user_ids)::bigint[]);

-- name: GuestComputersByIDs :many
SELECT c.id, c.number, c.display_name, c.device_name, z.name AS zone_name
FROM club_computers c
JOIN club_zones z ON z.club_id = c.club_id AND z.id = c.club_zone_id
WHERE c.club_id = sqlc.arg(club_id) AND c.id = ANY(sqlc.arg(ids)::bigint[]);

-- name: GuestClubForAgent :one
-- Настройки клуба для агента (ClubConfig, PIN выхода) — клуб ПК, с которого запрос.
SELECT cl.name, cl.settings, cl.locale, cl.timezone, cl.agent_staff_minutes, cl.logo_path, cl.signin_image_path,
       cl.agent_exit_pin_hash
FROM club_computers c
JOIN clubs cl ON cl.id = c.club_id
WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(computer_id);

-- name: GuestEnabledLanguages :many
-- Языки платформы, включённые суперадмином (LocaleResolver::enabledCodes(), sort_order, id), для ПК клуба.
SELECT l.code, l.name
FROM languages l
WHERE l.is_enabled
  AND EXISTS (SELECT 1 FROM club_computers c WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(computer_id))
ORDER BY l.sort_order, l.id;

-- name: GuestCertificateTimes :one
-- Срок сертификата ПК: Welcome.certificate_renew_after = issued_at + 2/3 срока (pki.Issue).
SELECT issued_at, not_after
FROM agent_certificates
WHERE club_id = sqlc.arg(club_id) AND serial = sqlc.arg(serial) AND club_computer_id = sqlc.arg(computer_id);

-- name: GuestStartStaffVisit :exec
-- ClubComputerSessions::startStaffVisit(): ПК в руках сотрудника на agent_staff_minutes.
UPDATE club_computers
SET staff_user_id = sqlc.narg(staff_user_id), staff_kind = sqlc.arg(kind), staff_until = sqlc.arg(until),
    updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: GuestResetGuestSession :one
-- ClubComputerSessions::resetGuestSession(): гостевая сессия с кассы (session_guest, hasPaidSession()) не
-- переживает перезагрузку ПК — поля сессии в NULL. Нет такой сессии — нет строки.
UPDATE club_computers
SET current_session_id = NULL, current_session_user_id = NULL, session_guest = false, session_ends_at = NULL,
    updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
  AND session_guest
  AND current_session_id IS NOT NULL
  AND session_ends_at > sqlc.arg(now)
RETURNING id, number, display_name, device_name, current_session_id;

-- ---------------------------------------------------------------------------
-- Тарифы и пакеты
-- ---------------------------------------------------------------------------

-- name: GuestListTariffs :many
-- AgentTariffPurchaseService::availableFor(): активные неудалённые тарифы клуба по длительности и имени,
-- с зонами (club_zone_tariff) и счётчиком продаж в агенте (tariff_sales, Q-007).
SELECT t.id, t.name, t.description, t.duration_minutes, t.price, t.payment_method, t.currency, t.discount_enabled,
       t.is_active, t.usage_limit_enabled, t.usage_limit_minutes, t.quantity_limit_enabled, t.quantity_limit,
       t.user_roles, t.sale_periods, t.validity_periods,
       COALESCE((SELECT array_agg(zt.club_zone_id ORDER BY zt.club_zone_id) FROM club_zone_tariff zt
                 WHERE zt.club_id = t.club_id AND zt.tariff_id = t.id), '{}')::bigint[] AS zone_ids,
       COALESCE((SELECT s.sold FROM tariff_sales s WHERE s.club_id = t.club_id AND s.tariff_id = t.id), 0)::bigint AS sold
FROM tariffs t
WHERE t.club_id = sqlc.arg(club_id) AND t.is_active AND t.deleted_at IS NULL
ORDER BY t.duration_minutes, t.name;

-- name: GuestListPackages :many
-- AgentTariffPurchaseService::packagesFor(): неистёкшие пакеты клиента по сроку.
SELECT id, name, minutes_left, expires_at
FROM time_packages
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id) AND expires_at > sqlc.arg(now)
ORDER BY expires_at, id;

-- ---------------------------------------------------------------------------
-- Счётчики гостя (GetProfile, GuestCountersChanged)
-- ---------------------------------------------------------------------------

-- name: GuestPendingFriendRequests :one
-- customerState(): входящие заявки в друзья (ClubComputerController ~стр. 345).
SELECT count(*)::bigint
FROM friendships
WHERE club_id = sqlc.arg(club_id) AND addressee_id = sqlc.arg(user_id) AND accepted_at IS NULL;

-- name: GuestLastStaffChatMessageID :one
-- customerState(): id последнего ответа сотрудника в чате клиента.
SELECT COALESCE(max(id), 0)::bigint
FROM chat_messages
WHERE club_id = sqlc.arg(club_id) AND customer_id = sqlc.arg(user_id) AND from_staff;

-- ---------------------------------------------------------------------------
-- QR-вход (QrLogins)
-- ---------------------------------------------------------------------------

-- name: GuestDeleteComputerQrLogins :exec
-- QrLogins::create(): один открытый код на ПК — новый QR заменяет прежний.
DELETE FROM agent_qr_logins
WHERE club_id = sqlc.arg(club_id) AND club_computer_id = sqlc.arg(computer_id);

-- name: GuestInsertQrLogin :exec
INSERT INTO agent_qr_logins (id, club_id, club_computer_id, code_hash, expires_at, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(club_id), sqlc.arg(computer_id), sqlc.arg(code_hash), sqlc.arg(expires_at),
        sqlc.arg(now), sqlc.arg(now));

-- name: GuestLockQrLogin :one
-- QrLogins::poll(): QR-вход этого ПК FOR UPDATE.
SELECT id, approved_user_id, consumed_at, expires_at
FROM agent_qr_logins
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND club_computer_id = sqlc.arg(computer_id)
FOR UPDATE;

-- name: GuestGetQrLogin :one
SELECT id, approved_user_id, consumed_at, expires_at
FROM agent_qr_logins
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND club_computer_id = sqlc.arg(computer_id);

-- name: GuestConsumeQrLogin :exec
UPDATE agent_qr_logins
SET consumed_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: GuestFindOpenQrLogin :one
-- QrLogins::findOpen(): открытый (не подтверждённый, не использованный, не истёкший) вход по хэшу кода.
SELECT id, club_computer_id, expires_at
FROM agent_qr_logins
WHERE club_id = sqlc.arg(club_id) AND code_hash = sqlc.arg(code_hash)
  AND approved_user_id IS NULL AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: GuestApproveQrLogin :execrows
-- QrLogins::approve(): атомарно, только открытый вход.
UPDATE agent_qr_logins
SET approved_user_id = sqlc.arg(user_id), approved_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
  AND approved_user_id IS NULL AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- ---------------------------------------------------------------------------
-- Друзья (Friends)
-- ---------------------------------------------------------------------------

-- name: GuestListFriendships :many
-- Friends::overview(): дружбы и заявки с участием пользователя, orderByDesc('accepted_at') (в MySQL NULL —
-- в конце при DESC), orderByDesc('id'), limit 300; имя и телефон другой стороны.
SELECT f.id, f.requester_id, f.addressee_id, f.accepted_at, o.name AS other_name, o.phone AS other_phone
FROM friendships f
JOIN users o ON o.id = CASE WHEN f.requester_id = sqlc.arg(user_id) THEN f.addressee_id ELSE f.requester_id END
WHERE f.club_id = sqlc.arg(club_id) AND (f.requester_id = sqlc.arg(user_id) OR f.addressee_id = sqlc.arg(user_id))
ORDER BY f.accepted_at DESC NULLS LAST, f.id DESC
LIMIT 300;

-- name: GuestFindClubUserByPhone :one
-- Friends::request(): клиент клуба с этим телефоном.
SELECT u.id, u.name
FROM users u
JOIN club_memberships m ON m.user_id = u.id
WHERE m.club_id = sqlc.arg(club_id) AND u.phone = sqlc.arg(phone);

-- name: GuestLockFriendshipPair :one
-- Friends::request(): дружба или заявка между двумя пользователями FOR UPDATE.
SELECT id, requester_id, addressee_id, accepted_at
FROM friendships
WHERE club_id = sqlc.arg(club_id)
  AND ((requester_id = sqlc.arg(user_a) AND addressee_id = sqlc.arg(user_b))
    OR (requester_id = sqlc.arg(user_b) AND addressee_id = sqlc.arg(user_a)))
ORDER BY id
LIMIT 1
FOR UPDATE;

-- name: GuestInsertFriendship :one
INSERT INTO friendships (club_id, requester_id, addressee_id, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(requester_id), sqlc.arg(addressee_id), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: GuestLockFriendship :one
SELECT id, requester_id, addressee_id, accepted_at
FROM friendships
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: GuestAcceptFriendship :exec
UPDATE friendships
SET accepted_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: GuestDeleteFriendship :exec
DELETE FROM friendships
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- ---------------------------------------------------------------------------
-- Чат (GuestChatController)
-- ---------------------------------------------------------------------------

-- name: GuestListChatMessages :many
-- Последние 100 сообщений клиента (или после after_id), новые сверху; вызывающий разворачивает.
SELECT cm.id, cm.from_staff, cm.body, cm.created_at, cm.read_at, a.name AS author_name
FROM chat_messages cm
LEFT JOIN users a ON a.id = cm.author_id
WHERE cm.club_id = sqlc.arg(club_id) AND cm.customer_id = sqlc.arg(user_id) AND cm.id > sqlc.arg(after_id)
ORDER BY cm.id DESC
LIMIT 100;

-- name: GuestMarkStaffMessagesRead :exec
-- Ответы сотрудников прочитаны, как только агент клиента их получил.
UPDATE chat_messages
SET read_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND customer_id = sqlc.arg(user_id) AND from_staff AND read_at IS NULL;

-- name: GuestInsertChatMessage :one
INSERT INTO chat_messages (club_id, customer_id, author_id, from_staff, body, club_computer_id, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(user_id), sqlc.arg(user_id), false, sqlc.arg(body), sqlc.narg(computer_id),
        sqlc.arg(now), sqlc.arg(now))
RETURNING id, created_at;

-- ---------------------------------------------------------------------------
-- Новости (GuestNewsController)
-- ---------------------------------------------------------------------------

-- name: GuestListPlatformNews :many
-- News::platform()->published()->latest()->latest('id')->limit(10).
SELECT id, title, body, image_path, created_at
FROM news
WHERE club_id IS NULL AND is_published
ORDER BY created_at DESC NULLS LAST, id DESC
LIMIT sqlc.arg(page_size);

-- name: GuestListClubNews :many
SELECT id, title, body, image_path, created_at
FROM news
WHERE club_id = sqlc.arg(club_id) AND is_published
ORDER BY created_at DESC NULLS LAST, id DESC
LIMIT sqlc.arg(page_size);

-- ---------------------------------------------------------------------------
-- Журнал гостя (GuestHistory) — activity_logs
-- ---------------------------------------------------------------------------

-- name: GuestHistoryOwn :many
-- GuestHistory::page(): действия самого клиента (индекс activity_logs_club_user_fk).
SELECT id, user_id, event, subject_type, subject_id, details, created_at
FROM activity_logs
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id) AND event = ANY(sqlc.arg(events)::text[])
  AND id < sqlc.arg(before_id)
ORDER BY id DESC
LIMIT sqlc.arg(page_size);

-- name: GuestHistoryReceived :many
-- GuestHistory::page(): действия других над аккаунтом клиента (subject = клиент).
SELECT id, user_id, event, subject_type, subject_id, details, created_at
FROM activity_logs
WHERE club_id = sqlc.arg(club_id) AND subject_type = sqlc.arg(subject_type) AND subject_id = sqlc.arg(user_id)
  AND user_id <> sqlc.arg(user_id) AND event = ANY(sqlc.arg(events)::text[])
  AND id < sqlc.arg(before_id)
ORDER BY id DESC
LIMIT sqlc.arg(page_size);

-- name: GuestTariffNames :many
-- Tariff::withTrashed()->whereKey($ids)->pluck('name', 'id').
SELECT id, name
FROM tariffs
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]);

-- ---------------------------------------------------------------------------
-- Переводы агента (TranslationCatalog, AgentTranslationController)
-- ---------------------------------------------------------------------------

-- name: GuestTranslationsVersion :one
-- Версия переводов: в Laravel — счётчик в кэше (TranslationCatalog::flush()). Здесь — момент последнего
-- изменения строк и языков в миллисекундах (правка суперадмина обновляет updated_at), 0 — строк нет.
-- Ограничено ПК клуба: переводы получает только подключённый ПК.
SELECT COALESCE(floor(extract(epoch FROM greatest(
           (SELECT max(GREATEST(t.updated_at, t.created_at)) FROM translations t),
           (SELECT max(GREATEST(l.updated_at, l.created_at)) FROM languages l))) * 1000), 0)::bigint AS version
WHERE EXISTS (SELECT 1 FROM club_computers c WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(computer_id));

-- name: GuestAgentTranslations :many
-- TranslationCatalog::lines($locale, 'agent'): строки группы agent включённого языка.
SELECT t.key, t.value
FROM translations t
JOIN languages l ON l.id = t.language_id
WHERE l.code = sqlc.arg(locale) AND l.is_enabled AND t."group" = 'agent'
  AND EXISTS (SELECT 1 FROM club_computers c WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(computer_id))
ORDER BY t.id;

-- name: GuestLanguageEnabled :one
-- abort_unless(in_array($locale, enabledCodes())).
SELECT EXISTS (SELECT 1 FROM languages l WHERE l.code = sqlc.arg(locale) AND l.is_enabled)
   AND EXISTS (SELECT 1 FROM club_computers c WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(computer_id))
   AS enabled;
