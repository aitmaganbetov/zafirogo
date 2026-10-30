-- Переводы друзьям (BR-091, BR-092): блокировки и коды подтверждения.

-- name: LockFriendshipForTransfer :one
-- Friends::transfer(): Friendship::lockForUpdate()->findOrFail() (app/Services/Friends.php ~стр. 121).
SELECT id, requester_id, addressee_id, accepted_at
FROM friendships
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: LockLatestTransferCode :one
-- FriendTransferCodes::confirm(): последний (по id) неистёкший код пользователя FOR UPDATE
-- (app/Services/FriendTransferCodes.php ~стр. 79). Ограничен дружбами клуба (см. open-questions/stage1-ledger.md).
SELECT tc.id, tc.friendship_id, tc.amount_cents, tc.code_hash, tc.attempts
FROM transfer_codes tc
JOIN friendships f ON f.id = tc.friendship_id
WHERE f.club_id = sqlc.arg(club_id)
  AND tc.user_id = sqlc.arg(user_id)
  AND tc.expires_at > sqlc.arg(now)
ORDER BY tc.id DESC
LIMIT 1
FOR UPDATE OF tc;

-- name: IncrementTransferCodeAttempts :exec
-- Неверный код: attempts + 1 отдельной транзакцией (в старом коде — вне транзакции перевода).
UPDATE transfer_codes tc
SET attempts = tc.attempts + 1, updated_at = now()
FROM friendships f
WHERE f.id = tc.friendship_id AND f.club_id = sqlc.arg(club_id) AND tc.id = sqlc.arg(id);

-- name: DeleteUserTransferCodesInClub :exec
-- После перевода по коду удаляются коды пользователя (в старом коде — все, во всех клубах).
DELETE FROM transfer_codes tc
USING friendships f
WHERE f.id = tc.friendship_id AND f.club_id = sqlc.arg(club_id) AND tc.user_id = sqlc.arg(user_id);
