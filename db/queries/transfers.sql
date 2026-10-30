-- Переводы друзьям (BR-091, BR-092): блокировки и коды подтверждения.

-- name: LockFriendshipForTransfer :one
-- Friends::transfer(): Friendship::lockForUpdate()->findOrFail() (app/Services/Friends.php ~стр. 121).
SELECT id, requester_id, addressee_id, accepted_at
FROM friendships
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: LockLatestTransferCode :one
-- FriendTransferCodes::confirm(): последний (по id) неистёкший код пользователя FOR UPDATE
-- (app/Services/FriendTransferCodes.php ~стр. 79). Решение Q-028/L-1 (docs/DECISIONS.md): код ищется по club_id
-- самого кода (0012), коды того же пользователя в других клубах не видны и не перекрывают этот.
SELECT tc.id, tc.friendship_id, tc.amount_cents, tc.code_hash, tc.attempts
FROM transfer_codes tc
WHERE tc.club_id = sqlc.arg(club_id)
  AND tc.user_id = sqlc.arg(user_id)
  AND tc.expires_at > sqlc.arg(now)
ORDER BY tc.id DESC
LIMIT 1
FOR UPDATE;

-- name: IncrementTransferCodeAttempts :exec
-- Неверный код: attempts + 1 отдельной транзакцией (в старом коде — вне транзакции перевода).
UPDATE transfer_codes
SET attempts = attempts + 1, updated_at = now()
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: DeleteUserTransferCodesInClub :exec
-- После перевода по коду удаляются коды пользователя в клубе (в старом коде — все, во всех клубах; решение
-- Q-028/L-1: коды клуба — данные клуба, чужие клубы не трогаем).
DELETE FROM transfer_codes
WHERE club_id = sqlc.arg(club_id) AND user_id = sqlc.arg(user_id);
