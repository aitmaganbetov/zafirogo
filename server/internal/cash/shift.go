package cash

import (
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Status — состояние смены (cash_shifts.status).
type Status string

// Состояния смены. Laravel: app/Models/CashShift.php STATUS_OPEN, STATUS_CLOSED.
const (
	StatusOpen   Status = "open"
	StatusClosed Status = "closed"
)

// Shift — кассовая смена (cash_shifts), то, что нужно расчётам.
type Shift struct {
	ID             int64
	Status         Status
	OpenedBy       int64 // 0 — NULL
	ClosedBy       int64 // 0 — NULL
	OpeningBalance money.Tiyn
	ClosingBalance money.Tiyn // имеет смысл у закрытой смены
}

// IsOpen — смена открыта.
func (s *Shift) IsOpen() bool { return s != nil && s.Status == StatusOpen }

// CheckOpen — открыть смену можно, только если в клубе нет открытой (BR-050).
//
// Laravel: app/Services/CashRegisterService.php open() (~стр. 87–98): открытая смена FOR UPDATE → «Смена уже открыта.»
// (поле opening_balance). Гонку двух открытий ловит уникальный индекс (club_id, open_marker).
func CheckOpen(open *Shift) error {
	if open.IsOpen() {
		return msgErr("opening_balance", MsgShiftAlreadyOpen)
	}
	return nil
}

// RequireOpen — операции продажи, пополнения и прихода/расхода требуют открытой смены.
// field — поле ошибки: items (sale()), amount (topUpBalance(), transact()).
// Laravel: CashRegisterService::sale()/topUpBalance()/transact() — «Сначала откройте смену.»
func RequireOpen(open *Shift, field string) error {
	if !open.IsOpen() {
		return msgErr(field, MsgOpenShiftFirst)
	}
	return nil
}

// Totals — итоги смены (BR-051).
type Totals struct {
	Opening money.Tiyn
	Income  money.Tiyn // SUM(amount WHERE type = income), возвраты сюда не входят
	Expense money.Tiyn // SUM(amount WHERE type = expense), включая возвраты
}

// ComputeTotals — итоги смены по её операциям.
//
// Laravel: app/Services/CashRegisterService.php totals() (~стр. 269–282): SUM по типам в SQL (точно, decimal),
// balance = (float) opening_balance + income − expense. В новом коде суммы считает SQL в тиынах (CLAUDE.md:
// агрегаты в SQL); эта функция — эталон для тестов и для смены, уже прочитанной целиком.
func ComputeTotals(opening money.Tiyn, ops []Operation) Totals {
	t := Totals{Opening: opening}
	for _, op := range ops {
		switch op.Type {
		case Income:
			t.Income += op.Amount
		case Expense:
			t.Expense += op.Amount
		}
	}
	return t
}

// Balance — остаток в кассе: opening + income − expense (точно, в тиынах).
func (t Totals) Balance() money.Tiyn { return t.Opening + t.Income - t.Expense }

// BalanceFloat — остаток так, как его считал PHP: (float) opening_balance + (float) income − (float) expense,
// слева направо во float64. С ним старый код сравнивал расход и возврат («В кассе недостаточно средств.»),
// поэтому сравнение повторяем во float (см. EnoughCash).
// Laravel: CashRegisterService::totals() — (float) $shift->opening_balance + $income - $expense.
func (t Totals) BalanceFloat() float64 {
	return t.Opening.Float() + t.Income.Float() - t.Expense.Float()
}

// ClosingBalance — closing_balance при закрытии: float-остаток, записанный в decimal(14,2)
// (строка PHP-float, округление MySQL — money.FromTengeFloatDecimal). Для всех сумм в пределах decimal(14,2)
// совпадает с Balance() (ошибка float много меньше полутиына); при ошибке преобразования — Balance().
// Laravel: CashRegisterService::close() — 'closing_balance' => $this->balance($shift).
func (t Totals) ClosingBalance() money.Tiyn {
	if v, err := money.FromTengeFloatDecimal(t.BalanceFloat()); err == nil {
		return v
	}
	return t.Balance()
}

// notEnoughCash — PHP: (float) $amount > $this->balance($shift).
// Laravel: CashRegisterService::transact() (~стр. 236), refund() (~стр. 165).
// Сравнение во float повторяет старое поведение и на границе: при остатке 0.3 + 0.6 (= 0.8999999999999999 во float)
// расход 0.90 отклоняется. См. вопрос CQ-3.
func notEnoughCash(amount float64, t Totals) bool { return amount > t.BalanceFloat() }

// CheckExpense — расход из кассы не больше остатка смены (BR-054).
// amount — TransactionAmount из ParseTransactionAmount; сравнивается (float) $data['amount'] запроса.
// Laravel: CashRegisterService::transact() — expense && (float) $data['amount'] > $this->balance($shift) → «В кассе недостаточно средств.» (поле amount).
func CheckExpense(open *Shift, t Totals, amount TransactionAmount) error {
	if err := RequireOpen(open, "amount"); err != nil {
		return err
	}
	if notEnoughCash(amount.Float, t) {
		return msgErr("amount", MsgNotEnoughCash)
	}
	return nil
}

// Close — закрытие смены: проверка и closing_balance (BR-056).
//
// Laravel: CashRegisterService::close() (~стр. 255–267): смена FOR UPDATE; не open → «Смена уже закрыта.» (поле shift);
// closed_by, status = closed, open_marker = null, closing_balance = balance(смены), closed_at = now.
// zReportPrinted — веб-панель требует отметку печати Z-отчёта (CashRegisterController::close(), ~стр. 135–145);
// приложение сотрудника закрывает без неё (Api/Staff/CashController::close()) — там передавать true.
func Close(shift *Shift, t Totals, zReportPrinted bool) (money.Tiyn, error) {
	if !zReportPrinted {
		return 0, msgErr("shift", MsgPrintZFirst)
	}
	if !shift.IsOpen() {
		return 0, msgErr("shift", MsgShiftAlreadyClose)
	}
	return t.ClosingBalance(), nil
}
