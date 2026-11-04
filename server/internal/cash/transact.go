package cash

import (
	"strings"
	"unicode/utf8"
)

// MaxDescription — StoreCashTransactionRequest: description required|string|max:255 (символы, mb_strlen).
const MaxDescription = 255

// PlanTransaction — приход или расход кассы вручную (BR-054).
//
// Laravel: app/Services/CashRegisterService.php transact() (~стр. 229–253); app/Http/Requests/StoreCashTransactionRequest.php:
// type in:income,expense; amount — ParseTransactionAmount; description required|string|max:255.
// Нет открытой смены → «Сначала откройте смену.» (поле amount); расход больше остатка → «В кассе недостаточно средств.».
// Такой приход в отчёте — «Прочий приход» (вид KindNone), вернуть его нельзя (refundKind() = null).
// Если описание прихода совпадает с маркером «Продажа POS», старый отчёт посчитает его продажей товаров —
// повторяем (описание не фильтровалось).
func PlanTransaction(open *Shift, t Totals, typ Type, amount TransactionAmount, description string, userID int64) (Operation, error) {
	if typ != Income && typ != Expense {
		return Operation{}, ruleErr("type", RuleIn)
	}
	if strings.TrimSpace(description) == "" {
		return Operation{}, ruleErr("description", RuleRequired)
	}
	if utf8.RuneCountInString(description) > MaxDescription {
		return Operation{}, ruleErr("description", RuleMax)
	}
	if err := RequireOpen(open, "amount"); err != nil {
		return Operation{}, err
	}
	if typ == Expense {
		if err := CheckExpense(open, t, amount); err != nil {
			return Operation{}, err
		}
	}
	return Operation{
		ShiftID:     open.ID,
		UserID:      userID,
		Type:        typ,
		Amount:      amount.Amount,
		Description: description,
	}, nil
}
