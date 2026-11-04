-- Касса: смены, кассовые операции, итоги и X/Z-отчёт (server/internal/cash/service).
-- Все запросы — в транзакции клуба (db.InTenantTx): фильтр club_id + RLS.
-- Порядок блокировок как в app/Services/CashRegisterService.php: открытая смена FOR UPDATE первой,
-- затем операция/товары/баланс клиента.

-- name: LockOpenCashShift :one
-- CashShift::query()->where('status', open)->lockForUpdate()->first(): сериализует все операции кассы клуба.
SELECT *
FROM cash_shifts
WHERE club_id = sqlc.arg(club_id) AND status = 'open'
ORDER BY id
LIMIT 1
FOR UPDATE;

-- name: GetOpenCashShift :one
-- Открытая смена без блокировки (страница кассы, X-отчёт).
SELECT *
FROM cash_shifts
WHERE club_id = sqlc.arg(club_id) AND status = 'open'
ORDER BY id
LIMIT 1;

-- name: GetCashShift :one
SELECT *
FROM cash_shifts
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: LockCashShift :one
-- CashShift::query()->lockForUpdate()->findOrFail($id) (close()).
SELECT *
FROM cash_shifts
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: InsertCashShift :one
-- open(): уникальный индекс (club_id, open_marker) не даёт открыть вторую смену параллельно.
INSERT INTO cash_shifts (club_id, opened_by, status, open_marker, opening_balance, opened_at, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.narg(opened_by), 'open', true, sqlc.arg(opening_balance),
        sqlc.arg(now), sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: CloseCashShift :one
-- close(): closed_by, status = closed, open_marker = null, closing_balance, closed_at = now;
-- снимок остатка кассы по журналу (CSQ-7, docs/DECISIONS.md).
UPDATE cash_shifts
SET closed_by = sqlc.narg(closed_by), status = 'closed', open_marker = NULL,
    closing_balance = sqlc.arg(closing_balance), closed_at = sqlc.arg(now), updated_at = sqlc.arg(now),
    drawer_snapshot_entry_id = sqlc.arg(drawer_snapshot_entry_id)::bigint,
    drawer_snapshot_balance = sqlc.arg(drawer_snapshot_balance)::bigint, drawer_snapshot_at = now()
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id) AND status = 'open'
RETURNING *;

-- name: LockCashShiftStatus :one
-- markZReportPrinted(): статус смены под блокировкой (смена не open — 422). Nonce печати — в Valkey (Q-015).
SELECT status
FROM cash_shifts
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: MarkZReportPrinted :exec
-- markZReportPrinted(): отметка печати смены (nonce уже погашен в Valkey).
UPDATE cash_shifts
SET z_report_printed_at = sqlc.arg(now), z_report_printed_by = sqlc.narg(printed_by)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: CashShiftTotals :one
-- CashRegisterService::totals(): SUM по типам (BR-051), агрегат в SQL.
SELECT COALESCE(sum(amount) FILTER (WHERE type = 'income'), 0)::bigint  AS income,
       COALESCE(sum(amount) FILTER (WHERE type = 'expense'), 0)::bigint AS expense
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND cash_shift_id = sqlc.arg(cash_shift_id);

-- name: InsertCashTransaction :one
INSERT INTO cash_transactions (club_id, cash_shift_id, refund_of_id, user_id, type, amount, description,
                               receipt_data, payment_method, income_kind, idempotency_key, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(cash_shift_id), sqlc.narg(refund_of_id), sqlc.narg(user_id), sqlc.arg(type),
        sqlc.arg(amount), sqlc.arg(description), sqlc.narg(receipt_data), sqlc.narg(payment_method),
        sqlc.narg(income_kind), sqlc.narg(idempotency_key), sqlc.arg(now), sqlc.arg(now))
RETURNING *;

-- name: GetCashTransactionByKey :one
-- Повтор запроса кассы с тем же ключом.
SELECT *
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: GetCashTransaction :one
SELECT *
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: LockCashTransaction :one
-- refund(): CashTransaction::query()->lockForUpdate()->findOrFail($income).
SELECT *
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: GetCashRefundOf :one
-- $income->refund(): возврат прихода (refund_of_id уникален).
SELECT *
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND refund_of_id = sqlc.arg(refund_of_id);

-- name: ListCashShiftTransactions :many
-- Операции смены по порядку id (keyset: after_id — последний id предыдущей страницы).
SELECT *
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND cash_shift_id = sqlc.arg(cash_shift_id) AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(page_size);

-- name: CashShiftReportKinds :one
-- ShiftReport::for(): приход по видам (BR-057) и возвраты — одним проходом. «Продажа товаров» —
-- по маркеру description = 'Продажа POS' (как в старом коде), остальные виды — по income_kind (OQ-BR-04).
SELECT count(*) FILTER (WHERE type = 'income' AND description = 'Продажа POS')                    AS goods_count,
       COALESCE(sum(amount) FILTER (WHERE type = 'income' AND description = 'Продажа POS'), 0)::bigint AS goods_amount,
       count(*) FILTER (WHERE type = 'income' AND income_kind = 'top_up')                          AS top_up_count,
       COALESCE(sum(amount) FILTER (WHERE type = 'income' AND income_kind = 'top_up'), 0)::bigint  AS top_up_amount,
       count(*) FILTER (WHERE type = 'income' AND income_kind = 'guest_session')                   AS guest_count,
       COALESCE(sum(amount) FILTER (WHERE type = 'income' AND income_kind = 'guest_session'), 0)::bigint AS guest_amount,
       count(*) FILTER (WHERE type = 'income')                                                     AS income_count,
       COALESCE(sum(amount) FILTER (WHERE type = 'income'), 0)::bigint                             AS income_amount,
       COALESCE(sum(amount) FILTER (WHERE type = 'expense'), 0)::bigint                            AS expense_amount,
       count(*) FILTER (WHERE refund_of_id IS NOT NULL)                                            AS refund_count,
       COALESCE(sum(amount) FILTER (WHERE refund_of_id IS NOT NULL), 0)::bigint                    AS refund_amount
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND cash_shift_id = sqlc.arg(cash_shift_id);

-- name: CashShiftReportStaff :many
-- ShiftReport::for(): сотрудники смены — GROUP BY user_id, sortByDesc('income'); при равном приходе —
-- user_id по возрастанию, NULL первым (CQ-6).
SELECT COALESCE(user_id, 0)::bigint AS user_id,
       count(*) AS count,
       COALESCE(sum(amount) FILTER (WHERE type = 'income'), 0)::bigint  AS income,
       COALESCE(sum(amount) FILTER (WHERE type = 'expense'), 0)::bigint AS expense
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND cash_shift_id = sqlc.arg(cash_shift_id)
GROUP BY user_id
ORDER BY income DESC, user_id ASC NULLS FIRST;

-- name: ListCashShiftSalesForReport :many
-- ShiftReport::for(): чеки продаж POS смены для «Продано товаров», кроме возвращённых (порядок id).
SELECT t.id, t.receipt_data
FROM cash_transactions t
WHERE t.club_id = sqlc.arg(club_id) AND t.cash_shift_id = sqlc.arg(cash_shift_id)
  AND t.type = 'income' AND t.description = 'Продажа POS' AND t.receipt_data IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM cash_transactions r WHERE r.club_id = t.club_id AND r.refund_of_id = t.id)
ORDER BY t.id;

-- name: ListProductQuantities :many
-- Остаток товаров сейчас, включая удалённые (Product::withTrashed()).
SELECT id, quantity
FROM products
WHERE club_id = sqlc.arg(club_id) AND id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ListClubUserNames :many
-- Имена сотрудников/клиентов клуба (шапка и «Сотрудники» отчёта, описание пополнения).
SELECT u.id, u.name
FROM users u
JOIN club_memberships m ON m.user_id = u.id
WHERE m.club_id = sqlc.arg(club_id) AND u.id = ANY(sqlc.arg(ids)::bigint[]);

-- name: GetClubSettingsForCash :one
-- clubs.settings (сырой JSON, как CurrentClub::settings()) — бонус за пополнение (BR-041).
-- У clubs нет RLS: клуб обязан совпадать с тенантом транзакции (zafiro_current_club_id()).
SELECT settings
FROM clubs
WHERE id = sqlc.arg(club_id) AND id = zafiro_current_club_id();

-- name: CashDrawerLedgerBalance :one
-- Остаток счёта кассы по журналу: касса — актив, остаток = −SUM(amount) (docs/schema-changes.md §10.1).
SELECT COALESCE(-sum(e.amount), 0)::bigint AS balance
FROM ledger_accounts a
JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
WHERE a.club_id = sqlc.arg(club_id) AND a.kind = 'cash_drawer' AND a.user_id IS NULL;

-- name: LatestCashDrawerSnapshot :one
-- Последний снимок остатка кассы (CSQ-7): закрытая смена со снимком с наибольшим id (смены клуба открываются
-- по одной, поэтому порядок id = порядок закрытия). Индекс cash_shifts_club_id_drawer_snapshot_index.
SELECT id, drawer_snapshot_entry_id::bigint AS entry_id, drawer_snapshot_balance::bigint AS balance,
       drawer_snapshot_at::timestamptz AS snapshot_at
FROM cash_shifts
WHERE club_id = sqlc.arg(club_id) AND drawer_snapshot_entry_id IS NOT NULL
ORDER BY id DESC
LIMIT 1;

-- name: CashDrawerLedgerSince :one
-- Проводки кассы после снимка (CSQ-7): id > after_entry_id; created_at >= from_created_at — только чтобы
-- не читать старые партиции (граница с запасом, см. cash/service drawerBalance). Остаток = −SUM (актив).
-- last_entry_id — наибольший id среди них (0 — проводок нет). Без снимка: after_entry_id = 0, from — начало времён.
SELECT COALESCE(-sum(e.amount), 0)::bigint AS balance, COALESCE(max(e.id), 0)::bigint AS last_entry_id
FROM ledger_accounts a
JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
WHERE a.club_id = sqlc.arg(club_id) AND a.kind = 'cash_drawer' AND a.user_id IS NULL
  AND e.id > sqlc.arg(after_entry_id) AND e.created_at >= sqlc.arg(from_created_at);

