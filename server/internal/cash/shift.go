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

// ClosingBalance — closing_balance при закрытии: остаток смены точно в тиынах.
// Laravel: CashRegisterService::close() — 'closing_balance' => $this->balance($shift) (float, записанный в decimal(14,2);
// для сумм в пределах decimal(14,2) совпадает с точным остатком).
// Решение Q-002 (docs/DECISIONS.md): остаток кассы считается точно, без float.
func (t Totals) ClosingBalance() money.Tiyn { return t.Balance() }

// notEnoughCash — PHP: (float) $amount > $this->balance($shift).
// Laravel: CashRegisterService::transact() (~стр. 236), refund() (~стр. 165).
// Решение Q-002, CQ-3 (docs/DECISIONS.md): сравнение точное, в тиынах. Старый код сравнивал во float, и при остатке
// 0.30 + 0.60 (= 0.8999999999999999 во float) расход 0.90 отклонялся; теперь проходит.
func notEnoughCash(amount money.Tiyn, t Totals) bool { return amount > t.Balance() }

// CheckExpense — расход из кассы не больше остатка смены (BR-054).
// amount — TransactionAmount из ParseTransactionAmount; сравнение точное (Решение Q-002 (docs/DECISIONS.md)).
// Laravel: CashRegisterService::transact() — expense && (float) $data['amount'] > $this->balance($shift) → «В кассе недостаточно средств.» (поле amount).
func CheckExpense(open *Shift, t Totals, amount TransactionAmount) error {
	if err := RequireOpen(open, "amount"); err != nil {
		return err
	}
	if notEnoughCash(amount.Amount, t) {
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
