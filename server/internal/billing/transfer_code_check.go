package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// CheckFriendTransfer — проверки самого перевода до того, как платить за код в Telegram.
//
// Laravel: app/Services/FriendTransferCodes.php FriendTransferCodes::checkedCents() (BR-092):
//
//	$cents <= 0 || $cents > MAX_TRANSFER_CENTS → amount «Сумма перевода — от 0,01 до 1 000 000.»
//	не участник дружбы или не принята → friendship «Переводить можно только друзьям.»
//	основной баланс < $cents → amount «Недостаточно средств на основном счёте.» (без блокировки баланса)
//
// Дружбы нет в клубе — ErrFriendshipNotFound (привязка маршрута {friendship} в старом — 404).
func CheckFriendTransfer(ctx context.Context, q *store.Queries, clubID, userID, friendshipID int64, amount money.Tiyn) error {
	if amount <= 0 || amount > MaxTransferAmount {
		return &BalanceError{Field: "amount", Message: msgTransferAmountRange}
	}
	f, err := q.LockFriendshipForTransfer(ctx, store.LockFriendshipForTransferParams{ClubID: clubID, ID: friendshipID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrFriendshipNotFound
	}
	if err != nil {
		return fmt.Errorf("billing: friendship: %w", err)
	}
	if (f.RequesterID != userID && f.AddresseeID != userID) || f.AcceptedAt == nil {
		return &BalanceError{Field: "friendship", Message: msgTransferOnlyFriends}
	}
	var main money.Tiyn
	b, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: clubID, UserID: userID})
	switch {
	case errors.Is(err, pgx.ErrNoRows): // MemberBalance::value('balance') ?? '0'
	case err != nil:
		return fmt.Errorf("billing: balance: %w", err)
	default:
		main = money.Tiyn(b.Balance)
	}
	if main < amount {
		return &BalanceError{Field: "amount", Message: msgNotEnoughMain}
	}
	return nil
}
