//go:build integration

package billing_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// BR-042: траты (club_memberships.total_spent) растут на оплату с основного баланса и уменьшаются на возврат,
// не ниже нуля — сценарии docs/golden-scenarios.md через боевой путь billing.AddSpending / SubtractSpending
// (их вызывают покупка времени, завершение сессии, заказ и отмена заказа магазина).
//
// Laravel: app/Services/LoyaltyProgram.php LoyaltyProgram::addSpending() (~стр. 45–50), subtractSpending() (~стр. 53–63).
func TestSpending(t *testing.T) {
	club := fixDB.Club(t, "spending")
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	spent := func(t *testing.T, user int64) money.Tiyn {
		t.Helper()
		var v int64
		if err := fixDB.Owner.QueryRow(context.Background(),
			"SELECT total_spent FROM club_memberships WHERE club_id = $1 AND user_id = $2", club, user).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return money.Tiyn(v)
	}
	set := func(t *testing.T, user int64, v money.Tiyn) {
		t.Helper()
		if _, err := fixDB.Owner.Exec(context.Background(),
			"UPDATE club_memberships SET total_spent = $3 WHERE club_id = $1 AND user_id = $2", club, user, int64(v)); err != nil {
			t.Fatal(err)
		}
	}
	type step struct {
		add   bool
		cents money.Tiyn
		want  money.Tiyn
	}
	tests := []struct {
		name  string
		start money.Tiyn
		steps []step
	}{
		// BR-042: тариф 900.00 основным (+200 бонусами не считаются) → +900.00; возврат 150.00 → −150.00.
		{"BR-042", 0, []step{{true, 90000, 90000}, {false, 15000, 75000}}},
		// GS-034: 950.00 → +600.00 (поминутно) → +900.00 = 2450.00.
		{"GS-034", 0, []step{{true, 95000, 95000}, {true, 60000, 155000}, {true, 90000, 245000}}},
		// GS-035: смешанная оплата — в траты идёт только основная часть 400.00.
		{"GS-035", 0, []step{{true, 40000, 40000}}},
		// GS-036: заказ 700.00 → отмена → 0.00.
		{"GS-036", 0, []step{{true, 70000, 70000}, {false, 70000, 0}}},
		// GAP-002: покупка 600.00 основным → завершение через 30 мин с возвратом 300.00 → 300.00.
		{"GAP-002", 0, []step{{true, 60000, 60000}, {false, 30000, 30000}}},
		// GAP-011: цена 0 (скидка 100 %) — траты не меняются.
		{"GAP-011", 12345, []step{{true, 0, 12345}}},
		// GAP-013: 200.00 − 350.00 → 0.00 (не −150).
		{"GAP-013", 20000, []step{{false, 35000, 0}}},
		// Неположительные суммы игнорируются (if ($cents <= 0) return).
		{"BR-042/неположительные", 500, []step{{true, -100, 500}, {false, -100, 500}, {false, 0, 500}}},
		{"BR-042/ровно_до_нуля", 500, []step{{false, 500, 0}}},
		{"BR-042/на_тиын_больше_трат", 500, []step{{false, 501, 0}}},
		{"BR-042/тиын", 0, []step{{true, 1, 1}, {false, 1, 0}}},
		// Решение SV-7: предел decimal(12,2) — ровно до предела можно.
		{"SV-7/до_предела", billing.MaxTotalSpent - 1, []step{{true, 1, billing.MaxTotalSpent}}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := fixDB.Customer(t, club, fmt.Sprintf("Клиент %d", i))
			set(t, user, tt.start)
			for n, s := range tt.steps {
				if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
					if s.add {
						return billing.AddSpending(ctx, q, club, user, s.cents, now)
					}
					return billing.SubtractSpending(ctx, q, club, user, s.cents, now)
				}); err != nil {
					t.Fatalf("шаг %d: %v", n+1, err)
				}
				if got := spent(t, user); got != s.want {
					t.Fatalf("шаг %d: total_spent = %d, want %d", n+1, got, s.want)
				}
			}
		})
	}
	t.Run("SV-7/за_пределом_decimal(12,2)", func(t *testing.T) {
		user := fixDB.Customer(t, club, "Предел")
		set(t, user, billing.MaxTotalSpent)
		err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
			return billing.AddSpending(ctx, q, club, user, 1, now)
		})
		if !errors.Is(err, billing.ErrTotalSpentOverflow) {
			t.Fatalf("err = %v, want ErrTotalSpentOverflow", err)
		}
		if got := spent(t, user); got != billing.MaxTotalSpent {
			t.Fatalf("после отката total_spent = %d", got)
		}
	})
	t.Run("BR-042/нет_членства", func(t *testing.T) {
		if err := tx(t, club, func(ctx context.Context, q *store.Queries) error {
			if err := billing.AddSpending(ctx, q, club, 999_999_999, 100, now); err != nil {
				return err
			}
			return billing.SubtractSpending(ctx, q, club, 999_999_999, 100, now)
		}); err != nil {
			t.Fatalf("нет членства — ничего, а не ошибка: %v", err)
		}
	})
}
