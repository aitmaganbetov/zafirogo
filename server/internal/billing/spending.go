package billing

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// MaxTotalSpent — club_memberships.total_spent в старой схеме decimal(12,2): больше — ошибка MySQL 1264 и откат
// (решение SV-7, docs/DECISIONS.md).
const MaxTotalSpent money.Tiyn = 999_999_999_999

// ErrTotalSpentOverflow — накопительные траты вышли бы за decimal(12,2).
var ErrTotalSpentOverflow = errors.New("billing: total_spent overflow")

// AddSpending — LoyaltyProgram::addSpending() (BR-042): траты клиента растут только на положительную сумму
// (оплата с основного баланса; бонусы не считаются) и копятся всегда, даже при выключенной программе.
// Нет членства — ничего (UPDATE 0 строк). Единственная реализация: покупка времени (billing/service) и заказ
// магазина (shop).
//
// Laravel: app/Services/LoyaltyProgram.php LoyaltyProgram::addSpending() (~стр. 45–50):
//
//	if ($cents > 0) { ...->increment('total_spent', formatCents($cents)); }
func AddSpending(ctx context.Context, q *store.Queries, clubID, userID int64, cents money.Tiyn, now time.Time) error {
	if cents <= 0 {
		return nil
	}
	total, err := q.IncrementTotalSpent(ctx, store.IncrementTotalSpentParams{ClubID: clubID, UserID: userID, Cents: int64(cents), Now: &now})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if money.Tiyn(total) > MaxTotalSpent {
		return ErrTotalSpentOverflow
	}
	return nil
}

// SubtractSpending — LoyaltyProgram::subtractSpending() (BR-042): возврат (отмена заказа, возврат основного
// баланса при завершении сессии) уменьшает траты, но не ниже нуля — bonus.SubtractSpending под блокировкой
// строки членства. Нет членства — ничего.
//
// Laravel: app/Services/LoyaltyProgram.php LoyaltyProgram::subtractSpending() (~стр. 53–63).
func SubtractSpending(ctx context.Context, q *store.Queries, clubID, userID int64, cents money.Tiyn, now time.Time) error {
	if cents <= 0 {
		return nil
	}
	m, err := q.LockMembershipForPurchase(ctx, store.LockMembershipForPurchaseParams{ClubID: clubID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	next := bonus.SubtractSpending(money.Tiyn(m.TotalSpent), cents)
	return q.SetTotalSpent(ctx, store.SetTotalSpentParams{ClubID: clubID, UserID: userID, TotalSpent: int64(next), Now: &now})
}
