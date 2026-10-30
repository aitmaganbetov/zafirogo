package cash

import (
	"math/big"
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

var (
	ratMaxTopUp      = big.NewRat(int64(MaxTopUp), 100)
	ratMaxShiftValue = big.NewRat(int64(MaxShiftAmount), 100)
)

// numericRat — значение строки, прошедшей правило Laravel `numeric` (PHP is_numeric), как точное десятичное число.
// Так Laravel 10+ сравнивает числа в правилах min/max/gt (Illuminate\Validation\Concerns\ValidatesAttributes:
// BigNumber::of($this->getSize(...)) — brick/math, без float). Экспонента вне ±1000 — ошибка
// (ensureExponentWithinAllowedRange()); ok = false.
func numericRat(s string) (*big.Rat, bool) {
	if !money.PHPIsNumeric(s) {
		return nil, false
	}
	s = strings.Trim(s, " \t\n\r\v\f")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, ok := parseSmallInt(s[i+1:])
		if !ok || e > 1000 || e < -1000 {
			return nil, false
		}
		exp = e
		s = s[:i]
	}
	whole, frac, _ := strings.Cut(s, ".")
	digits := new(big.Int)
	if _, ok := digits.SetString("0"+whole+frac, 10); !ok {
		return nil, false
	}
	if neg {
		digits.Neg(digits)
	}
	r := new(big.Rat).SetInt(digits)
	scale := exp - len(frac)
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(scale))), nil)
	if scale >= 0 {
		r.Mul(r, new(big.Rat).SetInt(p))
	} else {
		r.Quo(r, new(big.Rat).SetInt(p))
	}
	return r, true
}

func parseSmallInt(s string) (int, bool) {
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	v := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		if v < 1_000_000 {
			v = v*10 + int(s[i]-'0')
		}
	}
	if neg {
		v = -v
	}
	return v, true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// ParseOpeningBalance — начальный остаток смены (BR-050).
//
// Laravel: app/Http/Requests/OpenCashShiftRequest.php rules() — 'opening_balance' => required|numeric|min:0;
// app/Http/Controllers/CashRegisterController.php open() — $this->cash->open((float) $request->validated('opening_balance'));
// CashRegisterService::open() пишет float в decimal(14,2) — округление MySQL по строке PHP-float (money.ParseTengeViaFloat).
// Допущение Этапа 1: Q-002.
func ParseOpeningBalance(s string) (money.Tiyn, error) {
	const field = "opening_balance"
	if strings.TrimSpace(s) == "" {
		return 0, ruleErr(field, RuleRequired)
	}
	r, ok := numericRat(s)
	if !ok {
		return 0, ruleErr(field, RuleNumeric)
	}
	if r.Sign() < 0 {
		return 0, ruleErr(field, RuleMin)
	}
	t, err := money.ParseTengeViaFloat(s)
	if err != nil || t > MaxShiftAmount {
		return 0, ruleErr(field, RuleMax)
	}
	return t, nil
}

// TopUpAmount — сумма пополнения на кассе в двух видах, как её видел старый код.
type TopUpAmount struct {
	// Amount — что легло в member_balances и в cash_transactions.amount: (float) → decimal(…,2) MySQL.
	// Laravel: CashRegisterService::topUpBalance() — $balance->increment($balanceType, $amount), create(['amount' => $amount]).
	Amount money.Tiyn
	// BonusBase — от чего считается бонус за пополнение: (int) round($amount * 100) (PHP 8.4+).
	// Laravel: app/Services/TopUpBonus.php TopUpBonus::for() — $cents = (int) round($amount * 100) (BR-041).
	// Почти всегда равно Amount; расходится только на вводе с > 14 значащими цифрами
	// (например "0.0149999999999999": Amount = 2 тиына, BonusBase = 1 тиын) — поэтому передаём оба.
	BonusBase money.Tiyn
}

// ParseTopUpAmount — сумма пополнения баланса клиента на кассе (BR-053).
//
// Laravel: app/Http/Requests/StoreBalanceTopUpRequest.php rules() — 'amount' => required|numeric|gt:0|max:9999999999.99
// (сравнения точные, brick/math); CashRegisterController::topUpBalance() и Api/Staff/CashController::topUp() —
// (float) $data['amount'] в CashRegisterService::topUpBalance(float $amount).
// Отступление: ввод, который проходит gt:0, но округляется MySQL до 0.00 ("0.001"), старый код записывал как
// пополнение на 0.00; ledger не принимает нулевых проводок, поэтому здесь — ошибка gt (вопрос CQ-1).
// Допущение Этапа 1: Q-002.
func ParseTopUpAmount(s string) (TopUpAmount, error) {
	const field = "amount"
	if strings.TrimSpace(s) == "" {
		return TopUpAmount{}, ruleErr(field, RuleRequired)
	}
	r, ok := numericRat(s)
	if !ok {
		return TopUpAmount{}, ruleErr(field, RuleNumeric)
	}
	if r.Sign() <= 0 {
		return TopUpAmount{}, ruleErr(field, RuleGt)
	}
	if r.Cmp(ratMaxTopUp) > 0 {
		return TopUpAmount{}, ruleErr(field, RuleMax)
	}
	amount, err := money.ParseTengeViaFloat(s)
	if err != nil || amount > MaxTopUp {
		return TopUpAmount{}, ruleErr(field, RuleMax)
	}
	if amount <= 0 {
		return TopUpAmount{}, ruleErr(field, RuleGt) // CQ-1
	}
	// Laravel: TopUpBonus::for(float $amount) — $cents = (int) round($amount * 100).
	base := money.FromTengeFloat(money.PHPStringToFloat(s))
	return TopUpAmount{Amount: amount, BonusBase: base}, nil
}

// TransactionAmount — сумма прихода/расхода кассы (transact()).
type TransactionAmount struct {
	// Amount — что легло в cash_transactions.amount: строка запроса как есть → decimal(14,2) MySQL
	// (money.ParseTengeRounded). Laravel: CashRegisterService::transact() — $shift->transactions()->create($data + …).
	Amount money.Tiyn
	// Float — (float) $data['amount']: с ним старый код сравнивал остаток смены («В кассе недостаточно средств.»).
	Float float64
}

// ParseTransactionAmount — сумма прихода/расхода (BR-054).
//
// Laravel: app/Http/Requests/StoreCashTransactionRequest.php rules() — 'amount' => required|numeric|gt:0 (без верхней
// границы; выход за decimal(14,2) — ошибка MySQL, здесь — ошибка max, CQ-2). Нулевая после округления сумма — ошибка gt (CQ-1).
// Допущение Этапа 1: Q-002.
func ParseTransactionAmount(s string) (TransactionAmount, error) {
	const field = "amount"
	if strings.TrimSpace(s) == "" {
		return TransactionAmount{}, ruleErr(field, RuleRequired)
	}
	r, ok := numericRat(s)
	if !ok {
		return TransactionAmount{}, ruleErr(field, RuleNumeric)
	}
	if r.Sign() <= 0 {
		return TransactionAmount{}, ruleErr(field, RuleGt)
	}
	if r.Cmp(ratMaxShiftValue) > 0 {
		return TransactionAmount{}, ruleErr(field, RuleMax)
	}
	amount, err := money.ParseTengeRounded(s)
	if err != nil || amount > MaxShiftAmount {
		return TransactionAmount{}, ruleErr(field, RuleMax)
	}
	if amount <= 0 {
		return TransactionAmount{}, ruleErr(field, RuleGt) // CQ-1
	}
	return TransactionAmount{Amount: amount, Float: money.PHPStringToFloat(s)}, nil
}
