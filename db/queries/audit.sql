-- Аудит-журнал (только добавление): деньги, тарифы, права, финансовые настройки.

-- name: InsertAuditLog :exec
INSERT INTO audit_log (club_id, actor_user_id, actor_kind, category, action, subject_type, subject_id,
                       ledger_transaction_id, before, after, ip, request_id)
VALUES (sqlc.arg(club_id), sqlc.narg(actor_user_id), sqlc.arg(actor_kind), sqlc.arg(category), sqlc.arg(action),
        sqlc.narg(subject_type), sqlc.narg(subject_id), sqlc.narg(ledger_transaction_id), sqlc.narg(before),
        sqlc.narg(after), sqlc.narg(ip), sqlc.narg(request_id));

-- name: ListClubAuditLog :many
-- Новые сверху, keyset по (created_at, id); период — диапазоном [from, before).
SELECT id, actor_user_id, actor_kind, category, action, subject_type, subject_id, ledger_transaction_id,
       before, after, created_at
FROM audit_log
WHERE club_id = sqlc.arg(club_id)::bigint
  AND created_at >= sqlc.arg(from_created_at)
  AND (created_at, id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::bigint)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size);
