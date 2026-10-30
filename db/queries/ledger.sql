-- Журнал денежных операций (ledger). Пишет только пакет internal/ledger (Post); остальные — через него.
-- Все запросы — в транзакции клуба (db.InTenantTx): фильтр club_id + RLS.

-- name: CurrentTenantClubID :one
-- Клуб, поставленный транзакции (app.club_id); NULL — не тенантная транзакция.
SELECT zafiro_current_club_id()::bigint AS club_id;

-- name: ClaimLedgerIdempotencyKey :one
-- Захват ключа: строка не вставилась (ErrNoRows) — операция с этим ключом уже есть (или идёт в другой
-- транзакции: INSERT ждёт её завершения). id операции выдаётся здесь же из ledger_transaction_id_seq.
INSERT INTO ledger_idempotency (club_id, idempotency_key, fingerprint, transaction_id, transaction_created_at)
VALUES (sqlc.arg(club_id), sqlc.arg(idempotency_key), sqlc.arg(fingerprint),
        nextval('ledger_transaction_id_seq'), now())
ON CONFLICT (club_id, idempotency_key) DO NOTHING
RETURNING transaction_id, transaction_created_at;

-- name: GetLedgerIdempotencyKey :one
SELECT fingerprint, transaction_id, transaction_created_at
FROM ledger_idempotency
WHERE club_id = sqlc.arg(club_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: UpsertLedgerAccounts :many
-- Счета операции: создаёт недостающие и возвращает id всех. user_id = 0 — счёт клуба (NULL).
-- Счёт, вставленный параллельной транзакцией, этот снимок может не увидеть — Go повторяет запрос.
WITH wanted AS (
    -- unnest в списке выборки: массивы одной длины разворачиваются попарно.
    SELECT w.kind, NULLIF(w.user_id, 0) AS user_id
    FROM (SELECT unnest(sqlc.arg(kinds)::text[]) AS kind, unnest(sqlc.arg(user_ids)::bigint[]) AS user_id) AS w
), inserted AS (
    INSERT INTO ledger_accounts (club_id, kind, user_id)
    SELECT sqlc.arg(club_id), wanted.kind, wanted.user_id FROM wanted
    ON CONFLICT ON CONSTRAINT ledger_accounts_club_kind_user_unique DO NOTHING
    RETURNING id, kind, user_id
)
SELECT inserted.id, inserted.kind::text AS kind, COALESCE(inserted.user_id, 0)::bigint AS user_id FROM inserted
UNION ALL
SELECT a.id, a.kind::text AS kind, COALESCE(a.user_id, 0)::bigint AS user_id
FROM ledger_accounts a
JOIN wanted ON a.kind = wanted.kind AND a.user_id IS NOT DISTINCT FROM wanted.user_id
WHERE a.club_id = sqlc.arg(club_id);

-- name: InsertLedgerTransaction :exec
INSERT INTO ledger_transactions (id, club_id, kind, idempotency_key, actor_user_id, cash_shift_id,
                                 cash_transaction_id, session_id, shop_order_id, reverses_transaction_id,
                                 meta, created_at)
VALUES (sqlc.arg(id), sqlc.arg(club_id), sqlc.arg(kind), sqlc.arg(idempotency_key), sqlc.narg(actor_user_id),
        sqlc.narg(cash_shift_id), sqlc.narg(cash_transaction_id), sqlc.narg(session_id), sqlc.narg(shop_order_id),
        sqlc.narg(reverses_transaction_id), sqlc.narg(meta), sqlc.arg(created_at));

-- name: InsertLedgerEntries :many
-- Проводки одной операции. Триггер ledger_apply_member_entry сдвигает member_balances и ставит balance_after.
INSERT INTO ledger_entries (club_id, transaction_id, account_id, amount, created_at)
SELECT sqlc.arg(club_id), sqlc.arg(transaction_id), e.account_id, e.amount, sqlc.arg(created_at)
FROM (SELECT unnest(sqlc.arg(account_ids)::bigint[]) AS account_id, unnest(sqlc.arg(amounts)::bigint[]) AS amount) AS e
RETURNING id, account_id, amount, balance_after;

-- name: InsertLedgerMinuteEntries :exec
-- Движения минут (пакеты времени). time_package_id/tariff_id = 0 — NULL.
INSERT INTO ledger_minute_entries (club_id, transaction_id, user_id, time_package_id, tariff_id, minutes, created_at)
SELECT sqlc.arg(club_id), sqlc.arg(transaction_id), m.user_id, NULLIF(m.time_package_id, 0), NULLIF(m.tariff_id, 0),
       m.minutes, sqlc.arg(created_at)
FROM (SELECT unnest(sqlc.arg(user_ids)::bigint[]) AS user_id,
             unnest(sqlc.arg(time_package_ids)::bigint[]) AS time_package_id,
             unnest(sqlc.arg(tariff_ids)::bigint[]) AS tariff_id,
             unnest(sqlc.arg(minutes)::bigint[]) AS minutes) AS m;

-- name: GetLedgerTransaction :one
SELECT id, club_id, kind, idempotency_key, actor_user_id, cash_shift_id, cash_transaction_id, session_id,
       shop_order_id, reverses_transaction_id, meta, created_at
FROM ledger_transactions
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND created_at = sqlc.arg(created_at);

-- name: ListLedgerEntriesByTransaction :many
SELECT e.id, e.account_id, a.kind::text AS account_kind, COALESCE(a.user_id, 0)::bigint AS user_id,
       e.amount, e.balance_after
FROM ledger_entries e
JOIN ledger_accounts a ON a.club_id = e.club_id AND a.id = e.account_id
WHERE e.club_id = sqlc.arg(club_id) AND e.transaction_id = sqlc.arg(transaction_id)
  AND e.created_at = sqlc.arg(created_at)
ORDER BY e.id;

-- name: ListLedgerMinuteEntriesByTransaction :many
SELECT id, user_id, COALESCE(time_package_id, 0)::bigint AS time_package_id,
       COALESCE(tariff_id, 0)::bigint AS tariff_id, minutes
FROM ledger_minute_entries
WHERE club_id = sqlc.arg(club_id) AND transaction_id = sqlc.arg(transaction_id)
  AND created_at = sqlc.arg(created_at)
ORDER BY id;

-- name: ListMemberLedgerEntries :many
-- История баланса клиента (основной и бонусный), новые сверху, keyset по (created_at, id):
-- before_created_at/before_id — последняя строка предыдущей страницы (первая страница — now() + 1 день, 0 → MaxInt64).
-- Период — диапазоном [from, before). Индекс: ledger_entries_club_account_created_at_index.
SELECT e.id, e.transaction_id, t.kind AS transaction_kind, a.kind::text AS account_kind, e.amount,
       e.balance_after, e.created_at
FROM ledger_accounts a
JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
JOIN ledger_transactions t ON t.club_id = e.club_id AND t.id = e.transaction_id AND t.created_at = e.created_at
WHERE a.club_id = sqlc.arg(club_id)
  AND a.user_id = sqlc.arg(user_id)
  AND a.kind IN ('member_main', 'member_bonus')
  AND e.created_at >= sqlc.arg(from_created_at)
  AND (e.created_at, e.id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::bigint)
ORDER BY e.created_at DESC, e.id DESC
LIMIT sqlc.arg(page_size);

-- name: SumClubAccountByPeriod :one
-- Оборот счёта клуба за период [from, to) (SUM в SQL). Знак — как в проводках (> 0 кредит).
SELECT COALESCE(sum(e.amount), 0)::bigint AS total, count(*) AS entries
FROM ledger_accounts a
JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
WHERE a.club_id = sqlc.arg(club_id)
  AND a.kind = sqlc.arg(kind)
  AND a.user_id IS NULL
  AND e.created_at >= sqlc.arg(from_created_at)
  AND e.created_at < sqlc.arg(to_created_at);
