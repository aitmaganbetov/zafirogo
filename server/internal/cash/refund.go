package cash

import (
	"strconv"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// CheckRefund — можно ли вернуть приход по чеку (BR-055) и что он возвращает.
//
// Laravel: app/Services/CashRegisterService.php refund() (~стр. 150–167), проверки по порядку:
//
//	$kind = $income->refundKind(); null → «Эту операцию вернуть нельзя.»
//	$shift === null || $income->cash_shift_id !== $shift->getKey() → «Вернуть можно только операцию открытой смены.»
//	$income->refund()->exists() → «Эта операция уже возвращена.»
//	(float) $income->amount > $this->balance($shift) → «В кассе недостаточно средств.»
//
// Все ошибки — поле refund. open — открытая смена клуба (nil — нет), t — её итоги, alreadyRefunded — у прихода
// есть возврат (cash_transactions.refund_of_id = income.id). Право cash.delete проверяет вызывающий (403, MsgNoRefundRight).
func CheckRefund(income Operation, open *Shift, alreadyRefunded bool, t Totals) (RefundKind, error) {
	kind := income.RefundKind()
	if kind == RefundNone {
		return kind, msgErr("refund", MsgNotRefundable)
	}
	if !open.IsOpen() || income.ShiftID != open.ID {
		return kind, msgErr("refund", MsgRefundOpenShift)
	}
	if alreadyRefunded {
		return kind, msgErr("refund", MsgAlreadyRefunded)
	}
	if notEnoughCash(income.Amount.Float(), t) {
		return kind, msgErr("refund", MsgNotEnoughCash)
	}
	return kind, nil
}

// RefundOperation — расход-возврат: type = expense, amount = сумма прихода, refund_of_id = приход,
// description «Возврат по чеку №ID». Laravel: CashRegisterService::refund() (~стр. 193–199).
func RefundOperation(income Operation, open *Shift, userID int64) Operation {
	return Operation{
		ShiftID:     open.ID,
		UserID:      userID,
		Type:        Expense,
		Amount:      income.Amount,
		Description: RefundDescription(income.ID),
		RefundOfID:  income.ID,
	}
}

// RefundDescription — __('Возврат по чеку №:number'). Русский текст, как TopUpDescription (CQ-5).
func RefundDescription(incomeID int64) string {
	return "Возврат по чеку №" + strconv.FormatInt(incomeID, 10)
}

// RefundNote — note движения склада при возврате продажи (не переводится). Laravel: returnToStock().
const RefundNote = "Возврат POS"

// StockReturn — товар, который возврат продажи кладёт обратно на склад: products.quantity += Quantity,
// stock_movements {receipt, note 'Возврат POS'}, журнал stock.received, вебхук stock.changed {reason: refund}.
// ProductID = 0 — старый чек без product_id: товар ищется по имени (Product::where('name', …)->first()).
// Удалённый (soft) или не найденный товар вызывающий пропускает — остаток не возвращается (BR-055, BR-073).
type StockReturn struct {
	ProductID int64
	Name      string
	Quantity  int64
}

// SaleStockReturns — какие строки чека продажи возвращаются на склад.
//
// Laravel: CashRegisterService::returnToStock() (~стр. 207–227): услуга (isset service_id) — пропуск;
// $quantity = (int) ($line['quantity'] ?? 0); товар по product_id или, для старых чеков, по name;
// товара нет или $quantity <= 0 — пропуск. Порядок — как строки чека (так же блокируются товары, BR-005).
func SaleStockReturns(lines []ReceiptLine) []StockReturn {
	var out []StockReturn
	for _, l := range lines {
		if l.IsService() || l.Quantity <= 0 {
			continue
		}
		out = append(out, StockReturn{ProductID: l.ProductID, Name: l.Name, Quantity: l.Quantity})
	}
	return out
}

// MemberBalance — балансы клиента (member_balances), прочитанные FOR UPDATE.
type MemberBalance struct {
	Main  money.Tiyn
	Bonus money.Tiyn
}

// TopUpRefund — что возврат пополнения снимает с баланса клиента.
type TopUpRefund struct {
	Field           BalanceField
	Amount          money.Tiyn // − с баланса Field
	BonusTakenBack  money.Tiyn // − с bonus_balance: сколько осталось от бонуса за это пополнение
	NewBalance      money.Tiyn // баланс Field после возврата
	NewBonusBalance money.Tiyn
}

// PlanTopUpRefund — возврат пополнения (ветка top_up в refund(), BR-055).
//
// Laravel: CashRegisterService::refund() (~стр. 175–187):
//
//	$field = $topUp['balance_type'] === 'bonus_balance' ? 'bonus_balance' : 'balance';
//	$balance === null || (float) $balance->{$field} < (float) $income->amount → «Клиент уже потратил часть пополнения…»
//	$balance->decrement($field, (float) $income->amount);
//	$bonus = min((float) ($topUp['bonus'] ?? 0), (float) $balance->refresh()->bonus_balance); if ($bonus > 0) decrement
//
// Сравнения float здесь равносильны целочисленным: каждая сторона — одно decimal-значение, приведённое к float.
// balance nil — у клиента нет строки member_balances. Траты лояльности не меняются.
// Тот же расчёт делает billing.RefundTopUp при проводке в ledger; эта функция — чистая модель правила для тестов и UI.
func PlanTopUpRefund(top TopUpReceipt, amount money.Tiyn, balance *MemberBalance) (TopUpRefund, error) {
	field := FieldMain
	if top.Field == FieldBonus {
		field = FieldBonus
	}
	if balance == nil {
		return TopUpRefund{}, msgErr("refund", MsgTopUpSpent)
	}
	main, bonus := balance.Main, balance.Bonus
	have := main
	if field == FieldBonus {
		have = bonus
	}
	if have < amount {
		return TopUpRefund{}, msgErr("refund", MsgTopUpSpent)
	}
	if field == FieldBonus {
		bonus -= amount
	} else {
		main -= amount
	}
	back := min(top.Bonus, bonus)
	if back < 0 {
		back = 0
	}
	bonus -= back
	out := TopUpRefund{Field: field, Amount: amount, BonusTakenBack: back, NewBonusBalance: bonus, NewBalance: main}
	if field == FieldBonus {
		out.NewBalance = bonus
	}
	return out, nil
}

// CanShowRefund — показывать ли кнопку «Возврат» у прихода в списке операций и в чеке.
// Laravel: resources/views/cash-register/refund-button.blade.php: возврата ещё нет, есть право (canRefund),
// refundKind() !== null и смена операции открыта. Уже возвращённый приход показывает «Возвращено».
func CanShowRefund(op Operation, refunded, canRefund bool, opShift *Shift) bool {
	return !refunded && canRefund && op.RefundKind() != RefundNone && opShift.IsOpen()
}
