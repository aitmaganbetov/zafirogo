//go:build integration

package billing_test

import (
	"fmt"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// BR-055 (ветка top_up), GS-005, GAP-024: возврат пополнения, проведённый billing.RefundTopUp через ledger,
// до тиына совпадает с правилом cash.PlanTopUpRefund — golden-сценарии кассы (cash/rules_test.go, sim_test.go)
// и границы (ровно равный баланс, на тиын меньше, бонус больше остатка, нет строки балансов).
//
// Laravel: app/Services/CashRegisterService.php CashRegisterService::refund() (~стр. 174–186).
func TestRefundTopUpMatchesPlan(t *testing.T) {
	club := fixDB.Club(t, "refund-plan")
	type bal struct{ main, bonus money.Tiyn }
	cases := []struct {
		name          string
		field         billing.BalanceField
		amount, bonus money.Tiyn // сумма прихода и receipt_data.top_up.bonus
		have          *bal       // nil — у клиента нет строки member_balances
	}{
		{"GS-005/потрачена_часть", billing.FieldMain, 200000, 20000, &bal{150000, 20000}},
		{"GS-005/после_доплаты", billing.FieldMain, 200000, 20000, &bal{250000, 20000}},
		{"GAP-024/ровно_равный_баланс", billing.FieldMain, 200000, 20000, &bal{200000, 5000}},
		{"BR-055/на_тиын_меньше", billing.FieldMain, 200000, 20000, &bal{199999, 20000}},
		{"BR-055/на_тиын_больше", billing.FieldMain, 200000, 20000, &bal{200001, 20001}},
		{"BR-055/бонус_потрачен_весь", billing.FieldMain, 200000, 20000, &bal{200000, 0}},
		{"BR-055/бонус_без_бонуса_в_чеке", billing.FieldMain, 100, 0, &bal{100, 700}},
		{"BR-055/бонус_больше_остатка", billing.FieldMain, 100, 5000, &bal{100, 4999}},
		{"BR-055/бонусное_пополнение", billing.FieldBonus, 500, 300, &bal{1000, 700}},
		{"BR-055/бонусное_ровно", billing.FieldBonus, 700, 0, &bal{0, 700}},
		{"BR-055/бонусное_не_хватает", billing.FieldBonus, 701, 0, &bal{100000, 700}},
		{"BR-055/бонусное_с_бонусом_в_чеке", billing.FieldBonus, 500, 300, &bal{0, 800}},
		{"BR-055/основной_пуст_бонус_есть", billing.FieldMain, 1, 0, &bal{0, 100}},
		{"BR-055/минимум", billing.FieldMain, 1, 1, &bal{1, 1}},
		{"BR-055/максимум_кассы", billing.FieldMain, 999_999_999_999, 99_999_999_999, &bal{999_999_999_999, 99_999_999_999}},
		{"BR-055/нет_строки_балансов", billing.FieldMain, 100, 0, nil},
		{"BR-055/нет_строки_балансов_бонус", billing.FieldBonus, 100, 0, nil},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var user int64
			var planBalance *cash.MemberBalance
			if c.have != nil {
				user = customer(t, club, fmt.Sprintf("Клиент %d", i), c.have.main, c.have.bonus)
				planBalance = &cash.MemberBalance{Main: c.have.main, Bonus: c.have.bonus}
			} else {
				user = fixDB.Customer(t, club, fmt.Sprintf("Без балансов %d", i))
			}
			want, wantErr := cash.PlanTopUpRefund(cash.TopUpReceipt{UserID: user, Field: cash.BalanceField(c.field), Bonus: c.bonus}, c.amount, planBalance)
			got, err := refundTopUp(t, club, user, c.field, c.amount, c.bonus, int64(1000+i))
			if wantErr != nil {
				ce, _ := cash.AsError(wantErr)
				wantBalanceError(t, err, ce.Field, ce.Message)
				if c.have != nil {
					wantBalances(t, club, user, c.have.main, c.have.bonus)
				}
				return
			}
			if err != nil {
				t.Fatalf("RefundTopUp: %v; план: %+v", err, want)
			}
			// RefundTopUpResult.NewBonusBalance берётся из проводок ledger: если бонусный счёт не тронут (возврат с
			// основного и бонус за пополнение уже потрачен или его не было), поле равно 0, а не остатку бонусов —
			// как TopUpResult.NewBonusBalance. Сам остаток в БД сверяется ниже.
			wantBonusField := want.NewBonusBalance
			if c.field == billing.FieldMain && want.BonusTakenBack == 0 {
				wantBonusField = 0
			}
			if got.NewBalance != want.NewBalance || got.BonusTakenBack != want.BonusTakenBack || got.NewBonusBalance != wantBonusField {
				t.Fatalf("RefundTopUp = {%d, back %d, bonus %d}, план = %+v", got.NewBalance, got.BonusTakenBack, got.NewBonusBalance, want)
			}
			main, bonus := c.have.main, want.NewBonusBalance
			if c.field == billing.FieldMain {
				main = want.NewBalance
			}
			wantBalances(t, club, user, main, bonus)
		})
	}
}
