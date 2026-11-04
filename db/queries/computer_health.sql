-- name: ComputerHealthOpen :many
-- История проблем здоровья ПК (0030, agent/health) — только переходы. Открытые проблемы ПК (новое подключение: трекер не запишет их повторно).
SELECT kind, details
FROM computer_health_events
WHERE club_id = sqlc.arg(club_id) AND computer_id = sqlc.arg(computer_id) AND cleared_at IS NULL
ORDER BY kind;

-- name: ComputerHealthRaise :execrows
-- Проблема появилась: строка, только если открытой того же вида у ПК ещё нет и ПК принадлежит клубу.
INSERT INTO computer_health_events (club_id, computer_id, kind, raised_at, details)
SELECT c.club_id, c.id, sqlc.arg(kind)::varchar, sqlc.arg(raised_at)::timestamptz, sqlc.arg(details)::jsonb
FROM club_computers c
WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(computer_id)
  AND NOT EXISTS (SELECT 1 FROM computer_health_events e
                  WHERE e.club_id = sqlc.arg(club_id) AND e.computer_id = sqlc.arg(computer_id)
                    AND e.kind = sqlc.arg(kind)::varchar AND e.cleared_at IS NULL);

-- name: ComputerHealthClear :execrows
-- Проблема ушла: закрыть открытую строку этого вида.
UPDATE computer_health_events
SET cleared_at = GREATEST(sqlc.arg(cleared_at)::timestamptz, raised_at)
WHERE club_id = sqlc.arg(club_id) AND computer_id = sqlc.arg(computer_id) AND kind = sqlc.arg(kind)
  AND cleared_at IS NULL;

-- name: ComputerHealthHistory :many
-- Карточка ПК: история проблем, новые сверху; keyset по (raised_at, id) — before_* из последней строки страницы.
SELECT id, kind, raised_at, cleared_at, details
FROM computer_health_events
WHERE club_id = sqlc.arg(club_id) AND computer_id = sqlc.arg(computer_id)
  AND (sqlc.narg(before_at)::timestamptz IS NULL
       OR (raised_at, id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::bigint))
ORDER BY raised_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: ComputerHealthComputer :one
-- Карточка ПК клуба (нет строки — 404 для чужого или удалённого ПК) и признак «бездисковый»: последний выпущенный
-- сертификат ПК — по ключу образа (agent_certificates.diskless, 0013, Q-037). Индекс club_computer_index.
SELECT COALESCE((SELECT ac.diskless FROM agent_certificates ac
                 WHERE ac.club_id = c.club_id AND ac.club_computer_id = c.id
                 ORDER BY ac.issued_at DESC LIMIT 1), false)::boolean AS diskless
FROM club_computers c
WHERE c.club_id = sqlc.arg(club_id) AND c.id = sqlc.arg(computer_id);
