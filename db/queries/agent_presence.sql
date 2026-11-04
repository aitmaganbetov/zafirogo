-- Стрим агента ПК: присутствие (club_computers) и очередь долговременных команд (agent_commands).
-- server/internal/agent/{presence,commands,stream}; миграция 0010_agent_stream.sql.
-- Правило записи: Postgres получает только ИЗМЕНЕНИЯ состояния ПК (онлайн/офлайн, agent_state, версия,
-- железо, IP, загрузка Windows), а не каждый Status — сравнение делает Go до вызова запроса.

-- name: GetAgentComputer :one
-- Карточка ПК для Hello/Welcome: номер, зона, клуб, сохранённое состояние агента, активная сессия и последний
-- выданный seq команды (одним запросом: подключение 50 000 ПК после перезапуска — это 50 000 таких чтений).
SELECT c.id, c.club_id, c.number, c.display_name, c.device_name, z.name AS zone_name,
       cl.name AS club_name, cl.locale AS club_locale, cl.agent_staff_minutes,
       c.status, c.agent_state, c.agent_version, c.agent_boot_id, c.remote_ip, c.local_ip,
       c.mac_address, c.cpu_model, c.cpu_cores, c.ram_total_mb, c.gpu_model, c.os_version,
       c.current_session_id, c.current_session_user_id, c.session_guest, c.session_ends_at,
       COALESCE((SELECT k.last_seq FROM agent_command_counters k
                 WHERE k.club_id = c.club_id AND k.computer_id = c.id), 0)::bigint AS last_command_seq,
       -- Бездисковый ПК (0013, Q-037) — то же правило, что у панели (computer_health.sql ComputerHealthComputer):
       -- действующий сертификат ПК (не отозван, не истёк; у ПК в сети это сертификат подключения — все действующие
       -- сертификаты ПК одного вида: выпуск по коду или ключу образа отзывает прежние, продление сохраняет признак),
       -- иначе последний выпущенный. Индекс club_computer_index: диапазон not_after > now() — одна-две строки.
       COALESCE((SELECT ac.diskless FROM agent_certificates ac
                 WHERE ac.club_id = c.club_id AND ac.club_computer_id = c.id AND ac.not_after > now()
                   AND ac.revoked_at IS NULL
                 ORDER BY ac.issued_at DESC LIMIT 1),
                (SELECT ac.diskless FROM agent_certificates ac
                 WHERE ac.club_id = c.club_id AND ac.club_computer_id = c.id
                 ORDER BY ac.issued_at DESC LIMIT 1), false)::boolean AS diskless
FROM club_computers c
JOIN club_zones z ON z.club_id = c.club_id AND z.id = c.club_zone_id
JOIN clubs cl ON cl.id = c.club_id
WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(computer_id);

-- name: RecordAgentHello :exec
-- ПК подключился (или сменил версию/железо/IP/загрузку). Пишется, только если что-то из этого изменилось
-- (presence.Recorder сравнивает с GetAgentComputer). Поле NULL — не менять.
-- Laravel: app/Services/ClubComputerSessions.php changes() — status = 'online', remote_ip, agent_version,
-- железо и hardware_reported_at, device_name (только непустое).
UPDATE club_computers
SET status               = 'online',
    last_seen_at         = sqlc.arg(now),
    agent_version        = COALESCE(sqlc.narg(agent_version), agent_version),
    agent_boot_id        = COALESCE(sqlc.narg(agent_boot_id), agent_boot_id),
    remote_ip            = COALESCE(sqlc.narg(remote_ip), remote_ip),
    local_ip             = COALESCE(sqlc.narg(local_ip), local_ip),
    cpu_model            = COALESCE(sqlc.narg(cpu_model), cpu_model),
    cpu_cores            = COALESCE(sqlc.narg(cpu_cores), cpu_cores),
    ram_total_mb         = COALESCE(sqlc.narg(ram_total_mb), ram_total_mb),
    gpu_model            = COALESCE(sqlc.narg(gpu_model), gpu_model),
    os_version           = COALESCE(sqlc.narg(os_version), os_version),
    device_name          = COALESCE(sqlc.narg(device_name), device_name),
    hardware_reported_at = CASE WHEN sqlc.arg(hardware_changed)::boolean THEN sqlc.arg(now) ELSE hardware_reported_at END,
    updated_at           = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(computer_id);

-- name: RecordAgentState :execrows
-- Агент сменил состояние (после дебаунса). Состояние «гость у ПК» (idle, locked, in_session) снимает
-- отметку сотрудника за ПК — Laravel: app/Services/ClubComputerSessions.php changes() (CUSTOMER_STATES → STAFF_FIELDS).
-- Сессию агент не меняет: она — данные сервера (sessionUpdates() для оплаченной сессии ничего не пишет).
UPDATE club_computers
SET agent_state   = sqlc.arg(agent_state),
    last_seen_at  = sqlc.arg(now),
    staff_user_id = CASE WHEN sqlc.arg(agent_state) IN ('idle', 'locked', 'in_session') THEN NULL ELSE staff_user_id END,
    staff_kind    = CASE WHEN sqlc.arg(agent_state) IN ('idle', 'locked', 'in_session') THEN NULL ELSE staff_kind END,
    staff_until   = CASE WHEN sqlc.arg(agent_state) IN ('idle', 'locked', 'in_session') THEN NULL ELSE staff_until END,
    updated_at    = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(computer_id)
  AND agent_state IS DISTINCT FROM sqlc.arg(agent_state);

-- name: MarkAgentOffline :execrows
-- Стрим закрыт и не открылся снова за время ожидания (или истёк TTL после падения инстанса).
-- last_seen_at — когда ПК последний раз был на связи.
UPDATE club_computers
SET status = 'offline', last_seen_at = sqlc.arg(last_seen_at), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(computer_id) AND status = 'online';

-- name: MarkAgentOnline :execrows
-- Исправление гонки «фоновая проверка записала офлайн, а ПК уже переподключился» (presence.Sweeper).
UPDATE club_computers
SET status = 'online', last_seen_at = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(computer_id) AND status <> 'online';

-- name: ListOnlineComputers :many
-- Платформенный запрос (пул zafiro_platform): ПК со status = 'online' для сверки с Valkey.
-- Keyset по (club_id, id); индекс club_computers_online_index.
SELECT club_id, id
FROM club_computers
WHERE status = 'online'
  AND (club_id, id) > (sqlc.arg(after_club_id)::bigint, sqlc.arg(after_id)::bigint)
ORDER BY club_id, id
LIMIT sqlc.arg(row_limit);

-- name: NextAgentCommandSeq :one
-- Следующий seq долговременной команды ПК (пропуски допустимы: откат транзакции «съедает» номер не всегда,
-- но агенту важен только порядок).
INSERT INTO agent_command_counters (club_id, computer_id, last_seq)
VALUES (sqlc.arg(club_id), sqlc.arg(computer_id), 1)
ON CONFLICT (club_id, computer_id) DO UPDATE SET last_seq = agent_command_counters.last_seq + 1
RETURNING last_seq;

-- name: GetAgentCommandSeq :one
-- Последний выданный seq (Welcome.latest_command_seq); 0 — команд не было.
SELECT COALESCE((SELECT last_seq FROM agent_command_counters
                 WHERE club_id = sqlc.arg(club_id) AND computer_id = sqlc.arg(computer_id)), 0)::bigint AS last_seq;

-- name: RaiseAgentCommandSeq :exec
-- Агент сообщил last_command_seq больше выданного сервером (восстановление базы из копии): новые команды
-- должны получить больший seq, иначе агент примет их за дубли.
INSERT INTO agent_command_counters (club_id, computer_id, last_seq)
VALUES (sqlc.arg(club_id), sqlc.arg(computer_id), sqlc.arg(last_seq))
ON CONFLICT (club_id, computer_id) DO UPDATE SET last_seq = EXCLUDED.last_seq
WHERE agent_command_counters.last_seq < EXCLUDED.last_seq;

-- name: InsertAgentCommand :exec
INSERT INTO agent_commands (club_id, computer_id, seq, command_id, kind, payload, issued_by_user_id,
                            created_at, expires_at)
VALUES (sqlc.arg(club_id), sqlc.arg(computer_id), sqlc.arg(seq), sqlc.arg(command_id), sqlc.arg(kind),
        sqlc.arg(payload), sqlc.narg(issued_by_user_id), sqlc.arg(created_at), sqlc.narg(expires_at));

-- name: ListPendingAgentCommands :many
-- Невыполненные неистёкшие команды с seq > last_command_seq из Hello — по возрастанию seq. Индекс — PK.
SELECT seq, command_id, kind, payload, expires_at
FROM agent_commands
WHERE club_id = sqlc.arg(club_id) AND computer_id = sqlc.arg(computer_id)
  AND seq > sqlc.arg(after_seq) AND completed_at IS NULL
  AND (expires_at IS NULL OR expires_at > sqlc.arg(now))
ORDER BY seq
LIMIT sqlc.arg(row_limit);

-- name: AcceptAgentCommand :execrows
-- CommandResult(ACCEPTED): агент принял долгую команду, итог придёт позже.
UPDATE agent_commands
SET status = 'accepted', accepted_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND computer_id = sqlc.arg(computer_id) AND command_id = sqlc.arg(command_id)
  AND status = 'pending';

-- name: CompleteAgentCommand :execrows
-- Итог команды (SUCCEEDED / FAILED / DUPLICATE). Повторный итог не перезаписывает первый.
UPDATE agent_commands
SET status = sqlc.arg(status), error_code = sqlc.narg(error_code), result_message = sqlc.narg(result_message),
    completed_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND computer_id = sqlc.arg(computer_id) AND command_id = sqlc.arg(command_id)
  AND completed_at IS NULL;

-- name: ExpireAgentCommands :many
-- Платформенный запрос (пул zafiro_platform): невыполненные команды с прошедшим expires_at → 'expired'.
-- Пачками по row_limit; индекс agent_commands_open_expires_at_index.
UPDATE agent_commands a
SET status = 'expired', completed_at = sqlc.arg(now)
FROM (SELECT o.club_id, o.computer_id, o.seq FROM agent_commands o
      WHERE o.completed_at IS NULL AND o.expires_at <= sqlc.arg(now)
      ORDER BY o.expires_at
      LIMIT sqlc.arg(row_limit)
      FOR UPDATE SKIP LOCKED) d
WHERE a.club_id = d.club_id AND a.computer_id = d.computer_id AND a.seq = d.seq
RETURNING a.club_id, a.computer_id, a.command_id;

-- name: DeleteOldAgentCommands :execrows
-- Платформенный запрос (пул zafiro_platform): удаление строк старше срока хранения, пачками.
DELETE FROM agent_commands a
USING (SELECT o.club_id, o.computer_id, o.seq FROM agent_commands o
       WHERE o.created_at < sqlc.arg(before)
       LIMIT sqlc.arg(row_limit)) d
WHERE a.club_id = d.club_id AND a.computer_id = d.computer_id AND a.seq = d.seq;
