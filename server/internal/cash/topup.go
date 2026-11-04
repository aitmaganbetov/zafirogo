package cash

import (
	"errors"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// BonusFunc — расчёт бонуса за пополнение основного баланса (TopUpBonus::for(), BR-041) по настройкам клуба.
// Реализует пакет бонусов (billing); nil — бонуса нет (бонус выключен).
// Аргумент — TopUpAmount.BonusBase: (int) round($amount * 100), как в TopUpBonus::for().
// Та же сигнатура, что billing.TopUpBonusFunc (приводится конверсией типа).
type BonusFunc func(base money.Tiyn) (money.Tiyn, error)

// TopUpPlan — что записать при пополнении на кассе.
type TopUpPlan struct {
	Field  BalanceField
	Amount money.Tiyn // + к балансу Field и cash_transactions.amount (приход кассы)
	Bonus  money.Tiyn // + к bonus_balance (не кассовый приход); 0 — нет
	// Description — cash_transactions.description (русский текст; см. TopUpDescription).
	Description   string
	Receipt       Receipt // receipt_data = {top_up: {user_id, balance_type, bonus}}
	Kind          IncomeKind
	PaymentMethod PaymentMethod
}

// ErrNegativeBonus — BonusFunc вернула отрицательный бонус.
var ErrNegativeBonus = errors.New("cash: negative top-up bonus")

// TopUpDescription — описание операции пополнения.
// Laravel: CashRegisterService::topUpBalance() (~стр. 110–124): __('Пополнение :balance: :user (:payment)'),
// :balance = __('основной баланс') | __('бонусный баланс'), :payment = наличные | карта | QR-код.
// Старый код переводил описание в момент записи (в БД — текст на языке сотрудника); здесь — русский, вопрос CQ-5.
func TopUpDescription(field BalanceField, userName string, method PaymentMethod) string {
	balance := "бонусный баланс"
	if field == FieldMain {
		balance = "основной баланс"
	}
	r := strings.NewReplacer(":balance", balance, ":user", userName, ":payment", method.label())
	return r.Replace("Пополнение :balance: :user (:payment)")
}

// PlanTopUp — пополнение баланса клиента на кассе (BR-053, бонус BR-041).
//
// Laravel: app/Services/CashRegisterService.php topUpBalance() (~стр. 100–143):
//
//	открытая смена (нет → «Сначала откройте смену.», поле amount); member_balances FOR UPDATE (нет → 404);
//	$balance->increment($balanceType, $amount);
//	$bonus = $balanceType === 'balance' ? $this->topUpBonus->for($amount) : ['percent' => 0.0, 'cents' => 0];
//	if ($bonus['cents'] > 0) $balance->increment('bonus_balance', $bonus['cents'] / 100);
//	create(['type' => income, 'amount' => $amount, 'description' => …, 'receipt_data' => ['top_up' => [user_id, balance_type, bonus]]])
//
// Пополнение бонусного баланса — тоже кассовый приход, без бонуса. Траты лояльности не меняются.
// Способ оплаты — только в описании и (Q-013) в необязательном поле, в остатке смены не разделяется.
func PlanTopUp(open *Shift, userID int64, userName string, field BalanceField, amount TopUpAmount, method PaymentMethod, bonus BonusFunc) (TopUpPlan, error) {
	if err := RequireOpen(open, "amount"); err != nil {
		return TopUpPlan{}, err
	}
	if userID <= 0 {
		return TopUpPlan{}, ruleErr("user_id", RuleIn)
	}
	if !field.Valid() {
		return TopUpPlan{}, ruleErr("balance_type", RuleIn)
	}
	if !method.Valid() {
		return TopUpPlan{}, ruleErr("payment_method", RuleIn)
	}
	if amount.Amount <= 0 || amount.Amount > MaxTopUp {
		return TopUpPlan{}, ruleErr("amount", RuleGt)
	}
	var b money.Tiyn
	if field == FieldMain && bonus != nil {
		var err error
		if b, err = bonus(amount.BonusBase); err != nil {
			return TopUpPlan{}, err
		}
		if b < 0 {
			return TopUpPlan{}, ErrNegativeBonus
		}
	}
	return TopUpPlan{
		Field:         field,
		Amount:        amount.Amount,
		Bonus:         b,
		Description:   TopUpDescription(field, userName, method),
		Receipt:       Receipt{TopUp: &TopUpReceipt{UserID: userID, Field: field, Bonus: b}},
		Kind:          KindTopUp,
		PaymentMethod: method,
	}, nil
}
