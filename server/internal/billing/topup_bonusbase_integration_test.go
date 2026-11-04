//go:build integration

package billing_test

import (
	"context"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// CQ-4: бонус за пополнение считается от (int) round($amount * 100) (TopUpBonus::for()), а не от записанной суммы.
// Ввод "0.0149999999999999": баланс +0.02 (Amount = 2), база бонуса 1 тиын (BonusBase = 1).
func TestTopUpBonusBase(t *testing.T) {
	cases := []struct {
		name               string
		amount, base, want money.Tiyn
	}{
		{"CQ-4/base-differs", 2, 1, 1},
		{"CQ-4/base-zero-means-amount", 250000, 0, 250000},
		{"CQ-4/base-equal", 250000, 250000, 250000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			club := fixDB.Club(t, "cq4-"+key("c"))
			u := customer(t, club, "Клиент", 0, 0)
			var got money.Tiyn = -1
			hook := func(base money.Tiyn) (money.Tiyn, error) { got = base; return 0, nil }
			if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
				_, err := billing.TopUp(ctx, q, billing.TopUpParams{
					ClubID: club, UserID: u, Field: billing.FieldMain, Amount: c.amount, BonusBase: c.base,
					PaymentMethod: "cash", IdempotencyKey: key("cq4"), Bonus: hook,
				})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("bonus base = %d, want %d", got, c.want)
			}
			wantBalances(t, club, u, c.amount, 0)
		})
	}
}
