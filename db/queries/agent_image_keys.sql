-- Ключи образа бездисковых ПК и ожидающие подтверждения ПК (решение Q-037, docs/DECISIONS.md).
-- Каждый тенантный запрос фильтрует по club_id. LookupImageKey — поиск до того, как известен клуб
-- (политика image_key_lookup, 0013_agent_image_keys.sql).

-- ---------------------------------------------------------------------------
-- Ключи образа
-- ---------------------------------------------------------------------------

-- name: CreateImageKey :one
INSERT INTO agent_image_keys (club_id, key_hash, key_prefix, name, subnets, created_by_user_id, expires_at)
VALUES (sqlc.arg(club_id), sqlc.arg(key_hash), sqlc.arg(key_prefix), sqlc.arg(name), sqlc.arg(subnets)::cidr[],
        sqlc.narg(created_by_user_id), sqlc.narg(expires_at))
RETURNING id, created_at;

-- name: ListImageKeys :many
-- Ключи клуба для панели (без хеша). Отозванные и истёкшие — тоже (история), новые сверху.
SELECT id, key_prefix, name, subnets, created_by_user_id, created_at, expires_at, revoked_at, revoked_by_user_id,
       last_used_at
FROM agent_image_keys
WHERE club_id = sqlc.arg(club_id)
ORDER BY created_at DESC, id DESC
LIMIT 200;

-- name: RevokeImageKey :one
UPDATE agent_image_keys
SET revoked_at = now(), revoked_by_user_id = sqlc.narg(revoked_by_user_id)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND revoked_at IS NULL
RETURNING id, key_prefix, name;

-- name: LookupImageKey :one
-- Видна только строка с хешем из app.enroll_code_hash (политика image_key_lookup). Возвращает club_id для app.club_id.
SELECT id, club_id
FROM agent_image_keys
WHERE key_hash = sqlc.arg(key_hash);

-- name: GetImageKeyForEnroll :one
-- Ключ под блокировкой: отзыв в панели ждёт окончания текущей регистрации и наоборот.
SELECT id, subnets, expires_at, revoked_at
FROM agent_image_keys
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR SHARE;

-- name: GetClubForImageEnroll :one
-- Название клуба для ответа Enroll. Без блокировки (в отличие от LockClubForEnroll): известные ПК карточку не
-- создают, а бездисковый клуб перезагружает сотни ПК разом. clubs — платформенная таблица (без RLS).
SELECT id AS club_id, name
FROM clubs
WHERE id = sqlc.arg(id);

-- name: TouchImageKey :exec
UPDATE agent_image_keys
SET last_used_at = now()
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- ---------------------------------------------------------------------------
-- Ожидающие подтверждения ПК
-- ---------------------------------------------------------------------------

-- name: GetOpenPendingComputer :one
-- Нерешённая или отклонённая заявка MAC клуба (одна — индекс agent_pending_computers_open_mac_unique).
SELECT id, rejected_at
FROM agent_pending_computers
WHERE club_id = sqlc.arg(club_id) AND mac_address = sqlc.arg(mac_address) AND approved_at IS NULL
FOR UPDATE;

-- name: CountWaitingPendingComputers :one
SELECT count(*) AS total
FROM agent_pending_computers
WHERE club_id = sqlc.arg(club_id) AND approved_at IS NULL AND rejected_at IS NULL;

-- name: InsertPendingComputer :one
-- Новый MAC. ON CONFLICT — параллельный Enroll того же ПК успел вставить заявку.
INSERT INTO agent_pending_computers (club_id, image_key_id, mac_address, hardware_uuid, hostname, device, ip)
VALUES (sqlc.arg(club_id), sqlc.arg(image_key_id), sqlc.arg(mac_address), sqlc.narg(hardware_uuid), sqlc.narg(hostname),
        sqlc.arg(device), sqlc.narg(ip))
ON CONFLICT (club_id, mac_address) WHERE approved_at IS NULL
DO UPDATE SET last_seen_at = now(), attempts = agent_pending_computers.attempts + 1, ip = EXCLUDED.ip,
              image_key_id = EXCLUDED.image_key_id, hardware_uuid = EXCLUDED.hardware_uuid,
              hostname = EXCLUDED.hostname, device = EXCLUDED.device
RETURNING id, (xmax = 0)::boolean AS inserted;

-- name: TouchPendingComputer :exec
-- Повторный Enroll ожидающего ПК: железо и адрес — последние.
UPDATE agent_pending_computers
SET last_seen_at = now(), attempts = attempts + 1, ip = sqlc.narg(ip), image_key_id = sqlc.arg(image_key_id),
    hardware_uuid = sqlc.narg(hardware_uuid), hostname = sqlc.narg(hostname), device = sqlc.arg(device)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: ListWaitingPendingComputers :many
-- Ожидающие подтверждения ПК клуба (панель), последние обращения сверху.
SELECT id, image_key_id, mac_address, hardware_uuid, hostname, device, ip, created_at, last_seen_at, attempts
FROM agent_pending_computers
WHERE club_id = sqlc.arg(club_id) AND approved_at IS NULL AND rejected_at IS NULL
ORDER BY last_seen_at DESC, id DESC
LIMIT 200;

-- name: GetPendingComputerForDecision :one
SELECT id, mac_address, hardware_uuid, hostname, device, ip, rejected_at
FROM agent_pending_computers
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND approved_at IS NULL
FOR UPDATE;

-- name: ApprovePendingComputer :exec
UPDATE agent_pending_computers
SET approved_at = now(), approved_by_user_id = sqlc.narg(approved_by_user_id), rejected_at = NULL,
    rejected_by_user_id = NULL, club_computer_id = sqlc.arg(club_computer_id)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND approved_at IS NULL;

-- name: RejectPendingComputer :execrows
UPDATE agent_pending_computers
SET rejected_at = now(), rejected_by_user_id = sqlc.narg(rejected_by_user_id)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND approved_at IS NULL AND rejected_at IS NULL;

-- name: RevokeImageKeyCertificates :many
-- Отзыв ключа образа отзывает и действующие сертификаты, выпущенные по нему (включая продлённые).
UPDATE agent_certificates
SET revoked_at = now(), revoke_reason = 'staff', revoked_by_user_id = sqlc.narg(revoked_by_user_id), changed_at = now()
WHERE club_id = sqlc.arg(club_id) AND image_key_id = sqlc.arg(image_key_id) AND revoked_at IS NULL
RETURNING serial, club_computer_id;
