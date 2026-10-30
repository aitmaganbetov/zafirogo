-- Балансы клиентов (member_balances — проекция ledger). Изменение — только ledger.Post.

-- name: LockMemberBalances :many
-- Блокировка балансов перед списанием — как lockForUpdate() в старом коде (BR-005):
-- несколько клиентов — в порядке user_id (Friends::transfer(), app/Services/Friends.php ~стр. 128).
SELECT user_id, balance, bonus_balance
FROM member_balances
WHERE club_id = sqlc.arg(club_id) AND user_id = ANY(sqlc.arg(user_ids)::bigint[])
ORDER BY user_id
FOR UPDATE;

-- name: EnsureMemberBalance :exec
-- Строка баланса клиента (нулевая). Членство в клубе должно существовать (FK).
INSERT INTO member_balances (club_id, user_id, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(user_id), now(), now())
ON CONFLICT (club_id, user_id) DO NOTHING;

-- name: ListMemberBalanceDrift :many
-- Сверка проекции с журналом (ночная проверка): клиенты клуба, у которых balance/bonus_balance
-- не равны сумме проводок по их счетам. Агрегаты — в SQL.
WITH sums AS (
    SELECT a.user_id,
           COALESCE(sum(e.amount) FILTER (WHERE a.kind = 'member_main'), 0)::bigint  AS ledger_main,
           COALESCE(sum(e.amount) FILTER (WHERE a.kind = 'member_bonus'), 0)::bigint AS ledger_bonus
    FROM ledger_accounts a
    JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
    WHERE a.club_id = sqlc.arg(club_id) AND a.kind IN ('member_main', 'member_bonus')
    GROUP BY a.user_id
)
SELECT mb.user_id, mb.balance, mb.bonus_balance,
       COALESCE(s.ledger_main, 0)::bigint AS ledger_main,
       COALESCE(s.ledger_bonus, 0)::bigint AS ledger_bonus
FROM member_balances mb
LEFT JOIN sums s ON s.user_id = mb.user_id
WHERE mb.club_id = sqlc.arg(club_id)
  AND (mb.balance <> COALESCE(s.ledger_main, 0) OR mb.bonus_balance <> COALESCE(s.ledger_bonus, 0))
ORDER BY mb.user_id;
