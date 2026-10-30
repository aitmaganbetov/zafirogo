-- Регистрация ПК (одноразовые коды), клиентские сертификаты агентов, карточка ПК по MAC.
-- Laravel: app/Services/ClubComputerRegistrar.php, app/Http/Controllers/Api/AgentEnrollController.php (BR-124…127).
-- Каждый тенантный запрос фильтрует по club_id; платформенные (кэш сертификатов по всем клубам) помечены.

-- ---------------------------------------------------------------------------
-- Коды регистрации
-- ---------------------------------------------------------------------------

-- name: CreateEnrollmentCode :one
INSERT INTO agent_enrollment_codes (club_id, code_hash, club_zone_id, club_computer_id, created_by_user_id, expires_at)
VALUES (sqlc.arg(club_id), sqlc.arg(code_hash), sqlc.narg(club_zone_id), sqlc.narg(club_computer_id),
        sqlc.narg(created_by_user_id), sqlc.arg(expires_at))
RETURNING id, created_at, expires_at;

-- name: ListActiveEnrollmentCodes :many
-- Действующие коды клуба (панель «Подключить ПК»). Код не показывается — только когда и кем выдан.
SELECT id, club_zone_id, club_computer_id, created_by_user_id, created_at, expires_at
FROM agent_enrollment_codes
WHERE club_id = sqlc.arg(club_id)
  AND created_at >= sqlc.arg(since)
  AND used_at IS NULL AND canceled_at IS NULL AND expires_at > sqlc.arg(now)
ORDER BY created_at DESC, id DESC
LIMIT 100;

-- name: CancelEnrollmentCode :execrows
UPDATE agent_enrollment_codes
SET canceled_at = now()
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND used_at IS NULL AND canceled_at IS NULL;

-- name: LookupEnrollmentCode :one
-- Поиск кода до того, как известен клуб: строка видна только через политику enroll_code_lookup
-- (транзакция ставит app.enroll_code_hash = hex(code_hash)). Возвращает club_id для app.club_id.
SELECT id, club_id
FROM agent_enrollment_codes
WHERE code_hash = sqlc.arg(code_hash);

-- name: ConsumeEnrollmentCode :one
-- Атомарно гасит код: второй Enroll с тем же кодом ждёт блокировку строки, перечитывает условие и
-- получает 0 строк.
UPDATE agent_enrollment_codes
SET used_at = now(), used_ip = sqlc.narg(used_ip)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
  AND used_at IS NULL AND canceled_at IS NULL AND expires_at > now()
RETURNING club_zone_id, club_computer_id, created_by_user_id;

-- name: SetEnrollmentCodeComputer :exec
UPDATE agent_enrollment_codes
SET used_computer_id = sqlc.arg(computer_id)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- ---------------------------------------------------------------------------
-- Клуб, подписка, зоны
-- ---------------------------------------------------------------------------

-- name: LockClubForEnroll :one
-- Club::query()->lockForUpdate()->findOrFail() в ClubComputerRegistrar::register(): регистрации клуба
-- по очереди (номер ПК = max + 1, лимит ПК тарифа). clubs — платформенная таблица (без RLS).
SELECT id AS club_id, name
FROM clubs
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: GetClubSubscriptionForAgent :one
-- ClubSubscription::isActive() (BR-130) и features тарифа SaaS (max_computers, BR-134).
SELECT s.status, s.trial_ends_at, s.current_period_ends_at, p.features
FROM club_subscriptions s
JOIN saas_plans p ON p.id = s.saas_plan_id
WHERE s.club_id = sqlc.arg(club_id);

-- name: GetDefaultZone :one
-- ClubComputerRegistrar::defaultZone(): зона «Не назначена» с иконкой desktop.
SELECT id, name, capacity
FROM club_zones
WHERE club_id = sqlc.arg(club_id) AND name = 'Не назначена' AND icon = 'desktop'
ORDER BY id
LIMIT 1;

-- name: CreateDefaultZone :one
INSERT INTO club_zones (club_id, name, icon, theme, status, is_featured, sort_order, position_x, position_y,
                        width, height, created_at, updated_at)
VALUES (sqlc.arg(club_id), 'Не назначена', 'desktop', 'neutral', 'available', false, 0, 20, 20, 390, 240, now(), now())
RETURNING id, name, capacity;

-- name: GetZoneForEnroll :one
SELECT id, name, capacity
FROM club_zones
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: CountZoneComputers :one
SELECT count(*) AS total
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND club_zone_id = sqlc.arg(zone_id);

-- name: MaxZoneComputerNumber :one
SELECT COALESCE(max(number), 0)::bigint AS max_number
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND club_zone_id = sqlc.arg(zone_id);

-- ---------------------------------------------------------------------------
-- Карточка ПК (ClubComputerRegistrar::find / register / adoptMac)
-- ---------------------------------------------------------------------------

-- name: FindComputerByMac :one
SELECT id, club_zone_id, number, display_name, device_name, device_id, mac_address
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND mac_address = sqlc.arg(mac_address);

-- name: FindComputerByDeviceIDWithoutMac :one
-- Карточка старого агента: UUID без MAC — её занимает первый ПК, сообщивший этот UUID со своим MAC.
SELECT id, club_zone_id, number, display_name, device_name, device_id, mac_address
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND lower(device_id) = lower(sqlc.arg(device_id)::text) AND mac_address IS NULL
ORDER BY id
LIMIT 1;

-- name: FindComputerByDeviceID :one
SELECT id, club_zone_id, number, display_name, device_name, device_id, mac_address
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND lower(device_id) = lower(sqlc.arg(device_id)::text)
ORDER BY id
LIMIT 1;

-- name: FindComputerByDeviceName :one
SELECT id, club_zone_id, number, display_name, device_name, device_id, mac_address
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND lower(device_name) = lower(sqlc.arg(device_name)::text)
ORDER BY id
LIMIT 1;

-- name: GetComputerForEnroll :one
SELECT id, club_zone_id, number, display_name, device_name, device_id, mac_address
FROM club_computers
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: DeviceIDTaken :one
-- ClubComputer::query()->where('device_id', …)->exists(): в MySQL сравнение без учёта регистра (utf8mb4_unicode_ci).
SELECT EXISTS (
    SELECT 1 FROM club_computers WHERE club_id = sqlc.arg(club_id) AND lower(device_id) = lower(sqlc.arg(device_id)::text)
) AS taken;

-- name: AdoptComputerMac :exec
-- ClubComputerRegistrar::adoptMac(): MAC пишется только в пустое поле.
UPDATE club_computers
SET mac_address = sqlc.arg(mac_address), updated_at = now()
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND mac_address IS NULL;

-- name: InsertEnrolledComputer :one
INSERT INTO club_computers (club_id, club_zone_id, number, device_name, device_id, mac_address, status,
                            created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(club_zone_id), sqlc.arg(number), sqlc.arg(device_name), sqlc.narg(device_id),
        sqlc.narg(mac_address), 'available', now(), now())
RETURNING id, club_zone_id, number, display_name, device_name, device_id, mac_address;

-- name: UpdateComputerHardware :exec
-- Железо из DeviceInfo (как heartbeat в ClubComputerSessions::heartbeat()); имя и device_id карточки не меняются.
UPDATE club_computers
SET agent_version = sqlc.narg(agent_version), os_version = sqlc.narg(os_version), cpu_model = sqlc.narg(cpu_model),
    cpu_cores = sqlc.narg(cpu_cores), ram_total_mb = sqlc.narg(ram_total_mb), gpu_model = sqlc.narg(gpu_model),
    local_ip = sqlc.narg(local_ip), remote_ip = sqlc.narg(remote_ip), hardware_reported_at = now(), updated_at = now()
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- ---------------------------------------------------------------------------
-- Сертификаты
-- ---------------------------------------------------------------------------

-- name: InsertAgentCertificate :exec
-- diskless/image_key_id — сертификат по ключу образа (0013, решение Q-037): короткий, продлевается тоже коротким.
INSERT INTO agent_certificates (serial, club_id, club_computer_id, spki_sha256, not_before, not_after, issued_via,
                                enrollment_code_id, replaces_serial, mac_address, hardware_uuid, diskless, image_key_id)
VALUES (sqlc.arg(serial), sqlc.arg(club_id), sqlc.arg(club_computer_id), sqlc.arg(spki_sha256), sqlc.arg(not_before),
        sqlc.arg(not_after), sqlc.arg(issued_via), sqlc.narg(enrollment_code_id), sqlc.narg(replaces_serial),
        sqlc.narg(mac_address), sqlc.narg(hardware_uuid), sqlc.arg(diskless), sqlc.narg(image_key_id));

-- name: GetAgentCertificate :one
SELECT serial, club_id, club_computer_id, spki_sha256, not_before, not_after, replaces_serial, first_used_at,
       revoked_at, revoke_reason, diskless, image_key_id
FROM agent_certificates
WHERE club_id = sqlc.arg(club_id) AND serial = sqlc.arg(serial);

-- name: RevokeComputerCertificates :many
-- Отзыв всех действующих сертификатов карточки (кроме except_serial, если задан).
UPDATE agent_certificates
SET revoked_at = now(), revoke_reason = sqlc.arg(reason)::varchar, revoked_by_user_id = sqlc.narg(revoked_by_user_id),
    changed_at = now()
WHERE club_id = sqlc.arg(club_id) AND club_computer_id = sqlc.arg(club_computer_id) AND revoked_at IS NULL
  AND serial <> COALESCE(sqlc.narg(except_serial)::varchar, '')
RETURNING serial;

-- name: RevokeAgentCertificate :execrows
UPDATE agent_certificates
SET revoked_at = now(), revoke_reason = sqlc.arg(reason)::varchar, revoked_by_user_id = sqlc.narg(revoked_by_user_id),
    changed_at = now()
WHERE club_id = sqlc.arg(club_id) AND serial = sqlc.arg(serial) AND revoked_at IS NULL;

-- name: MarkAgentCertificateUsed :one
-- Первое подключение с сертификатом: возвращает serial прежнего (продлённого) сертификата для отзыва.
UPDATE agent_certificates
SET first_used_at = now(), changed_at = now()
WHERE club_id = sqlc.arg(club_id) AND serial = sqlc.arg(serial) AND first_used_at IS NULL
RETURNING replaces_serial, club_computer_id;

-- name: CountRecentComputerCertificates :one
-- Сколько сертификатов выпущено карточке за период (лимит продлений).
SELECT count(*) AS total
FROM agent_certificates
WHERE club_id = sqlc.arg(club_id) AND club_computer_id = sqlc.arg(club_computer_id)
  AND not_after > sqlc.arg(since) AND issued_at >= sqlc.arg(since);

-- name: ListValidAgentCertificates :many
-- Платформенный запрос (роль zafiro_platform): полная загрузка кэша действующих сертификатов всех клубов.
-- Keyset по serial. Индекс: agent_certificates_valid_index.
SELECT serial, club_id, club_computer_id, not_after, replaces_serial, first_used_at
FROM agent_certificates
WHERE revoked_at IS NULL AND not_after > sqlc.arg(now) AND serial > sqlc.arg(after_serial)
ORDER BY serial
LIMIT sqlc.arg(page_size);

-- name: ListAgentCertificateChanges :many
-- Платформенный запрос: изменения после (since, after_serial) (выпуск, первое использование, отзыв) для кэша инстанса.
SELECT serial, club_id, club_computer_id, not_after, replaces_serial, first_used_at, revoked_at, changed_at
FROM agent_certificates
WHERE (changed_at, serial) > (sqlc.arg(since)::timestamptz, sqlc.arg(after_serial)::varchar)
ORDER BY changed_at, serial
LIMIT sqlc.arg(page_size);

-- name: GetAgentCertificateAnyClub :one
-- Платформенный запрос: серийный номер, которого ещё нет в кэше инстанса (выпущен другим инстансом).
SELECT serial, club_id, club_computer_id, not_after, replaces_serial, first_used_at, revoked_at
FROM agent_certificates
WHERE serial = sqlc.arg(serial);
