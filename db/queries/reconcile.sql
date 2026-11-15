-- Фоновая сверка проекций денег с журналом операций (аудит AC-3, воркер ledger:reconcile —
-- server/internal/worker/ledger_reconcile.go). Сверка НИЧЕГО не исправляет: находит, записывает, оповещает.
--
-- Запросы сверки клуба (ReconcileMemberBalancesTouched, ReconcileUnbalancedTransactions, ReconcileCashDrawerSnapshot
-- и ListMemberBalanceDrift из balances.sql) выполняются пулом клуба zafiro_app в транзакции клуба (RLS — второй
-- рубеж) — каждый одной командой (один снимок данных: параллельная операция не даёт ложного расхождения).
-- ReconcileClubs, ReconcileState и остальные Reconcile…Finding(s) — пул zafiro_platform: список клубов, состояние сверки и её находки (таблицы 0033).

-- name: ReconcileMemberBalancesTouched :many
-- Частая (ежечасная) сверка: только клиенты, чей баланс менялся с прошлой сверки (каждая проводка по счёту клиента
-- ставит member_balances.updated_at — триггер ledger_apply_member_entry), и клиенты с открытым расхождением
-- (user_ids — проверить, не исчезло ли оно). Сумма проводок — по всей истории этих клиентов: индекс
-- ledger_entries_club_account_created_at_index, а не чтение всех проводок клуба. Изменение баланса в обход журнала,
-- не тронувшее updated_at, найдёт полная сверка (ListMemberBalanceDrift, раз в сутки).
WITH scope AS MATERIALIZED (
    SELECT mb.user_id, mb.balance, mb.bonus_balance
    FROM member_balances mb
    WHERE mb.club_id = sqlc.arg(club_id)
      AND (mb.updated_at >= sqlc.arg(since)::timestamptz OR mb.user_id = ANY(sqlc.arg(user_ids)::bigint[]))
), sums AS (
    SELECT a.user_id,
           COALESCE(sum(e.amount) FILTER (WHERE a.kind = 'member_main'), 0)::bigint  AS ledger_main,
           COALESCE(sum(e.amount) FILTER (WHERE a.kind = 'member_bonus'), 0)::bigint AS ledger_bonus
    FROM scope s
    JOIN ledger_accounts a ON a.club_id = sqlc.arg(club_id) AND a.user_id = s.user_id
                          AND a.kind IN ('member_main', 'member_bonus')
    JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
    GROUP BY a.user_id
)
SELECT s.user_id, s.balance, s.bonus_balance,
       COALESCE(x.ledger_main, 0)::bigint AS ledger_main,
       COALESCE(x.ledger_bonus, 0)::bigint AS ledger_bonus
FROM scope s
LEFT JOIN sums x ON x.user_id = s.user_id
WHERE s.balance <> COALESCE(x.ledger_main, 0) OR s.bonus_balance <> COALESCE(x.ledger_bonus, 0)
ORDER BY s.user_id;

-- name: ReconcileUnbalancedTransactions :many
-- Двойная запись: операции клуба с created_at >= since, у которых сумма проводок не равна нулю (в бою это держит
-- отложенный триггер ledger_entries_balanced; сюда попадёт только запись в обход него). Операции — по индексу
-- ledger_transactions_club_id_created_at_index, проводки — по ledger_entries_transaction_fk; старые партиции не
-- читаются (условие по e.created_at повторено, чтобы планировщик отсёк и партиции проводок: замер — 10 мс и
-- 266 буферов против 77 мс и 9 360 без него). Полная сверка передаёт since = начало времён.
SELECT t.id, t.created_at, sum(e.amount)::bigint AS entries_sum
FROM ledger_transactions t
JOIN ledger_entries e ON e.club_id = t.club_id AND e.transaction_id = t.id AND e.created_at = t.created_at
WHERE t.club_id = sqlc.arg(club_id) AND t.created_at >= sqlc.arg(since)::timestamptz
  AND e.created_at >= sqlc.arg(since)::timestamptz
GROUP BY t.id, t.created_at
HAVING sum(e.amount) <> 0
ORDER BY t.id
LIMIT sqlc.arg(lim);

-- name: ReconcileCashDrawerSnapshot :many
-- Снимок остатка кассы последней закрытой смены (CSQ-7: от него касса считает остаток, не читая всю историю)
-- против журнала: −SUM проводок счёта cash_drawer с id не больше снимка. Нет снимка — строк нет (иначе одна).
-- Только полная сверка: читает все проводки кассы клуба.
SELECT s.id AS shift_id, s.drawer_snapshot_balance::bigint AS snapshot_balance,
       (SELECT COALESCE(-sum(e.amount), 0)::bigint
        FROM ledger_accounts a
        JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
        WHERE a.club_id = s.club_id AND a.kind = 'cash_drawer' AND a.user_id IS NULL
          AND e.id <= s.drawer_snapshot_entry_id) AS ledger_balance
FROM cash_shifts s
WHERE s.club_id = sqlc.arg(club_id) AND s.drawer_snapshot_entry_id IS NOT NULL
ORDER BY s.id DESC
LIMIT 1;

-- name: ReconcileClubs :many
-- Клубы пачками (keyset по id): сверка идёт по одному клубу, без общей долгой транзакции.
SELECT id, name::text AS name FROM clubs WHERE id > sqlc.arg(after_id) ORDER BY id LIMIT sqlc.arg(lim);

-- name: ReconcileState :one
SELECT last_started_at, last_success_at, last_full_at, last_duration_ms, last_clubs, last_error
FROM ledger_reconcile_state;

-- name: ReconcileStarted :exec
UPDATE ledger_reconcile_state SET last_started_at = sqlc.arg(now)::timestamptz;

-- name: ReconcileSucceeded :exec
-- full — это был полный проход (last_full_at сдвигается только им).
UPDATE ledger_reconcile_state
SET last_success_at = sqlc.arg(now)::timestamptz,
    last_full_at = CASE WHEN sqlc.arg(full_pass)::boolean THEN sqlc.arg(now)::timestamptz ELSE last_full_at END,
    last_duration_ms = sqlc.arg(duration_ms), last_clubs = sqlc.arg(clubs), last_error = '';

-- name: ReconcileFailed :exec
UPDATE ledger_reconcile_state SET last_duration_ms = sqlc.arg(duration_ms), last_error = sqlc.arg(error);

-- name: ReconcileOpenFindings :many
-- Открытые расхождения клуба: с ними сравнивается то, что сверка нашла сейчас (дедупликация записей журнала).
SELECT id, kind, subject_id, fingerprint
FROM ledger_reconcile_findings
WHERE club_id = sqlc.arg(club_id) AND resolved_at IS NULL
ORDER BY id;

-- name: ReconcileInsertFinding :one
INSERT INTO ledger_reconcile_findings (club_id, kind, subject_id, fingerprint, details, first_seen_at, last_seen_at)
VALUES (sqlc.arg(club_id), sqlc.arg(kind), sqlc.arg(subject_id), sqlc.arg(fingerprint), sqlc.arg(details),
        sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz)
RETURNING id;

-- name: ReconcileSeenFindings :exec
-- Расхождение всё ещё есть и не изменилось: только отметка времени (в журнал платформы не пишется).
UPDATE ledger_reconcile_findings SET last_seen_at = sqlc.arg(now)::timestamptz
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]) AND resolved_at IS NULL;

-- name: ReconcileChangeFinding :exec
-- Расхождение есть, но числа другие: новые значения (об изменении в журнал платформы пишется снова).
UPDATE ledger_reconcile_findings
SET fingerprint = sqlc.arg(fingerprint), details = sqlc.arg(details), last_seen_at = sqlc.arg(now)::timestamptz
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND resolved_at IS NULL;

-- name: ReconcileResolveFindings :exec
UPDATE ledger_reconcile_findings SET resolved_at = sqlc.arg(now)::timestamptz
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]) AND resolved_at IS NULL;

-- name: SuperadminReconcileState :one
-- Экран «Сервер» и сбор метрик: когда сверка прошла и сколько расхождений открыто (все клубы).
SELECT s.last_success_at, s.last_full_at, s.last_error,
       (SELECT count(*) FROM ledger_reconcile_findings f WHERE f.resolved_at IS NULL)::bigint AS open_findings
FROM ledger_reconcile_state s;
