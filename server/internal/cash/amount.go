package cash

import (
	"errors"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Пределы сумм кассы.
const (
	// MaxTopUp — StoreBalanceTopUpRequest: amount max:9999999999.99 (BR-053).
	MaxTopUp money.Tiyn = 999_999_999_999
	// MaxShiftAmount — предел колонок cash_shifts.opening_balance и cash_transactions.amount: decimal(14,2) в MySQL.
	// В старом коде выход за него — ошибка MySQL 1264 и откат (500); здесь — ошибка валидации поля
	// (BR-054 «ограничить сумму по decimal(14,2)»; вопрос CQ-2).
	MaxShiftAmount money.Tiyn = 99_999_999_999_999
)

// parseAmount — строгий разбор суммы из формы (Решение Q-002 (docs/DECISIONS.md)): цифры, необязательная точка и
// не более двух знаков после неё (money.ParseTenge). Пробелы по краям обрезаются, как middleware TrimStrings
// Laravel. Неоднозначный ввод не округляется, а отклоняется: > 2 знаков после точки — правило decimal (422,
// «Поле … должно содержать 0-2 знаков после запятой.», как 'decimal:0,2' в старом StoreClubZoneRequest);
// экспонента, запятая, «.5», «+5» и т. п. — numeric; |сумма| > money.MaxAmount — max.
// Раньше (Laravel 'numeric' + округление MySQL decimal(…,2)) "100.005" записывалось как 100.01.
func parseAmount(field, s string) (money.Tiyn, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ruleErr(field, RuleRequired)
	}
	t, err := money.ParseTenge(s)
	switch {
	case errors.Is(err, money.ErrTooManyDecimals):
		return 0, ruleErr(field, RuleDecimal)
	case errors.Is(err, money.ErrOutOfRange):
		return 0, ruleErr(field, RuleMax)
	case err != nil:
		return 0, ruleErr(field, RuleNumeric)
	}
	return t, nil
}

// ParseOpeningBalance — начальный остаток смены (BR-050).
//
// Laravel: app/Http/Requests/OpenCashShiftRequest.php rules() — 'opening_balance' => required|numeric|min:0;
// app/Http/Controllers/CashRegisterController.php open() — $this->cash->open((float) $request->validated('opening_balance')).
// Решение Q-002 (docs/DECISIONS.md): > 2 знаков после точки — ошибка decimal (в старом — округление MySQL).
func ParseOpeningBalance(s string) (money.Tiyn, error) {
	const field = "opening_balance"
	t, err := parseAmount(field, s)
	if err != nil {
		return 0, err
	}
	if t < 0 {
		return 0, ruleErr(field, RuleMin)
	}
	if t > MaxShiftAmount {
		return 0, ruleErr(field, RuleMax)
	}
	return t, nil
}

// TopUpAmount — сумма пополнения на кассе.
type TopUpAmount struct {
	// Amount — что ложится в member_balances и в cash_transactions.amount.
	// Laravel: CashRegisterService::topUpBalance() — $balance->increment($balanceType, $amount), create(['amount' => $amount]).
	Amount money.Tiyn
	// BonusBase — от чего считается бонус за пополнение: (int) round($amount * 100) (PHP 8.4+).
	// Laravel: app/Services/TopUpBonus.php TopUpBonus::for() — $cents = (int) round($amount * 100) (BR-041).
	// Решение Q-002 (docs/DECISIONS.md): ввод не больше двух знаков после точки, и для него round($amount * 100)
	// равен Amount точно (|сумма| ≤ MaxTopUp, ошибка float много меньше полутиына) — расхождение CQ-4 больше
	// невозможно. Поле оставлено, чтобы источник базы бонуса был виден в типе.
	BonusBase money.Tiyn
}

// ParseTopUpAmount — сумма пополнения баланса клиента на кассе (BR-053).
//
// Laravel: app/Http/Requests/StoreBalanceTopUpRequest.php rules() — 'amount' => required|numeric|gt:0|max:9999999999.99;
// CashRegisterController::topUpBalance() и Api/Staff/CashController::topUp() — (float) $data['amount'] в
// CashRegisterService::topUpBalance(float $amount).
// Решение Q-002 (docs/DECISIONS.md): > 2 знаков после точки — ошибка decimal (в старом — округление MySQL).
func ParseTopUpAmount(s string) (TopUpAmount, error) {
	const field = "amount"
	t, err := parseAmount(field, s)
	if err != nil {
		return TopUpAmount{}, err
	}
	if t <= 0 {
		return TopUpAmount{}, ruleErr(field, RuleGt)
	}
	if t > MaxTopUp {
		return TopUpAmount{}, ruleErr(field, RuleMax)
	}
	return TopUpAmount{Amount: t, BonusBase: t}, nil
}

// TransactionAmount — сумма прихода/расхода кассы (transact()).
type TransactionAmount struct {
	// Amount — что ложится в cash_transactions.amount. Laravel: CashRegisterService::transact() —
	// $shift->transactions()->create($data + …).
	Amount money.Tiyn
}

// ParseTransactionAmount — сумма прихода/расхода (BR-054).
//
// Laravel: app/Http/Requests/StoreCashTransactionRequest.php rules() — 'amount' => required|numeric|gt:0 (без верхней
// границы; выход за decimal(14,2) — ошибка MySQL, здесь — ошибка max, CQ-2).
// Решение Q-002 (docs/DECISIONS.md): > 2 знаков после точки — ошибка decimal (в старом — округление MySQL).
func ParseTransactionAmount(s string) (TransactionAmount, error) {
	const field = "amount"
	t, err := parseAmount(field, s)
	if err != nil {
		return TransactionAmount{}, err
	}
	if t <= 0 {
		return TransactionAmount{}, ruleErr(field, RuleGt)
	}
	if t > MaxShiftAmount {
		return TransactionAmount{}, ruleErr(field, RuleMax)
	}
	return TransactionAmount{Amount: t}, nil
}
