-- Списки кассы для REST админки (Этап 2, B1). Laravel: app/Http/Controllers/CashRegisterController.php.

-- name: AdminListShiftIncomes :many
-- operations(): приходы смены, новые сверху, с возвратом (refund:id,refund_of_id,created_at).
-- Keyset по id: before_id — последний id предыдущей страницы (первая страница — MaxInt64).
-- Индекс: cash_transactions (club_id, cash_shift_id, …) — фильтр по смене, сортировка по id.
SELECT t.id, t.cash_shift_id, t.user_id, t.type, t.amount, t.description, t.receipt_data, t.payment_method,
       t.income_kind, t.created_at,
       r.id AS refund_id, r.created_at AS refunded_at
FROM cash_transactions t
LEFT JOIN cash_transactions r ON r.club_id = t.club_id AND r.refund_of_id = t.id
WHERE t.club_id = sqlc.arg(club_id)
  AND t.cash_shift_id = sqlc.arg(cash_shift_id)
  AND t.type = 'income'
  AND t.id < sqlc.arg(before_id)
ORDER BY t.id DESC
LIMIT sqlc.arg(page_size);
