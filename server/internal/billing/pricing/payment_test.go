package pricing

import (
	"errors"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Раскладка цены по балансам (sell()): BR-018 примеры, GS-035, GS-039, GS-046, GS-049, GS-050, GS-053, GAP-006…GAP-008, GAP-011.
func TestPay(t *testing.T) {
	mixed30 := RawFinances{"allow_mixed_payment": true, "bonus_max_percent": 30}
	cases := []struct {
		name      string
		finances  RawFinances
		price     money.Tiyn
		method    PaymentMethod
		choice    BalanceChoice
		balances  Balances
		wantErr   error
		wantMain  money.Tiyn
		wantBonus money.Tiyn
		wantAfter Balances
	}{
		{"BR-018/пример-1", mixed30, 100000, MainOrBonus, ChoiceBonus, Balances{80000, 50000}, nil, 70000, 30000, Balances{10000, 20000}},
		{"BR-018/пример-2", RawFinances{"bonus_max_percent": 30}, 100000, MainOrBonus, ChoiceBonus, Balances{80000, 50000}, ErrBonusPercentLimit, 0, 0, Balances{}},
		{"BR-018/пример-3", nil, 100000, MainOrBonus, ChoiceNone, Balances{50000, 200000}, nil, 0, 100000, Balances{50000, 100000}},
		{"BR-018/пример-4", RawFinances{"allow_mixed_payment": true, "bonus_max_percent": 50}, 60000, MainOrBonus, ChoiceBonus, Balances{100000, 100000}, nil, 30000, 30000, Balances{70000, 70000}},
		{"GS-046/покупка", RawFinances{"allow_mixed_payment": true, "bonus_max_percent": 50}, 60000, MainOrBonus, ChoiceBonus, Balances{100000, 100000}, nil, 30000, 30000, Balances{70000, 70000}},
		{"GS-035", RawFinances{"allow_mixed_payment": true}, 100000, MainOrBonus, ChoiceNone, Balances{40000, 100000}, nil, 40000, 60000, Balances{0, 40000}},
		{"GS-039/17_мин", RawFinances{"allow_mixed_payment": true}, 14167, MainOrBonus, ChoiceNone, Balances{10000, 5000}, nil, 10000, 4167, Balances{0, 833}},
		{"GS-039/60_мин", RawFinances{"allow_mixed_payment": true}, 50000, MainOrBonus, ChoiceNone, Balances{0, 833}, ErrInsufficientFunds, 0, 0, Balances{}},
		{"GS-049/Пакет_bonus", mixed30, 100000, MainOrBonus, ChoiceBonus, Balances{200000, 50000}, nil, 70000, 30000, Balances{130000, 20000}},
		{"GS-049/Пакет_main", mixed30, 100000, MainOrBonus, ChoiceMain, Balances{130000, 20000}, nil, 100000, 0, Balances{30000, 20000}},
		{"GS-049/Только_деньги_bonus", mixed30, 100000, MainBalanceOnly, ChoiceBonus, Balances{30000, 20000}, ErrMainBalanceOnly, 0, 0, Balances{}},
		{"GS-049/0%_bonus", RawFinances{"allow_mixed_payment": true, "bonus_max_percent": 0}, 10000, MainOrBonus, ChoiceBonus, Balances{30000, 20000}, ErrBonusNotAllowed, 0, 0, Balances{}},
		{"GS-050/30%_bonus", RawFinances{"bonus_max_percent": 30}, 100000, MainOrBonus, ChoiceBonus, Balances{200000, 50000}, ErrBonusPercentLimit, 0, 0, Balances{}},
		{"GS-050/100%_бонусов_мало", RawFinances{"bonus_max_percent": 100}, 100000, MainOrBonus, ChoiceBonus, Balances{200000, 50000}, ErrBonusNotEnough, 0, 0, Balances{}},
		{"GS-050/бонусный_1500", RawFinances{"bonus_max_percent": 100}, 100000, MainOrBonus, ChoiceBonus, Balances{200000, 150000}, nil, 0, 100000, Balances{200000, 50000}},
		{"GS-050/без_выбора", RawFinances{"bonus_max_percent": 100}, 100000, MainOrBonus, ChoiceNone, Balances{200000, 50000}, nil, 100000, 0, Balances{100000, 50000}},
		{"GS-053/весь_баланс", nil, 200000, MainOrBonus, ChoiceMain, Balances{200000, 0}, nil, 200000, 0, Balances{0, 0}},
		{"GAP-006", mixed30, 100000, MainOrBonus, ChoiceNone, Balances{50000, 100000}, ErrInsufficientFunds, 0, 0, Balances{}},
		{"GAP-007", RawFinances{"bonus_max_percent": 100}, 100000, MainOrBonus, ChoiceNone, Balances{50000, 150000}, nil, 0, 100000, Balances{50000, 50000}},
		{"GAP-008", mixed30, 99999, MainOrBonus, ChoiceBonus, Balances{200000, 50000}, nil, 70000, 29999, Balances{130000, 20001}},
		{"GAP-011", nil, 0, MainOrBonus, ChoiceNone, Balances{0, 0}, nil, 0, 0, Balances{0, 0}},
		{"GAP-011/bonus_цена_0", RawFinances{"bonus_max_percent": 0}, 0, MainOrBonus, ChoiceBonus, Balances{0, 0}, nil, 0, 0, Balances{0, 0}},
		// Ветки без смешанной оплаты и без выбора: бонусы целиком, только если основного мало и бонусы покрывают цену.
		{"BR-018/без_выбора/основной_мало_бонусов_мало", nil, 100000, MainOrBonus, ChoiceNone, Balances{50000, 90000}, ErrInsufficientFunds, 0, 0, Balances{}},
		{"BR-018/без_выбора/лимит_меньше_цены", RawFinances{"bonus_max_percent": 99}, 100000, MainOrBonus, ChoiceNone, Balances{50000, 200000}, ErrInsufficientFunds, 0, 0, Balances{}},
		{"BR-018/без_выбора/main_balance_only", nil, 100000, MainBalanceOnly, ChoiceNone, Balances{50000, 200000}, ErrInsufficientFunds, 0, 0, Balances{}},
		{"BR-018/смешанная/без_выбора/основного_хватает", mixed30, 100000, MainOrBonus, ChoiceNone, Balances{150000, 100000}, nil, 100000, 0, Balances{50000, 100000}},
		{"BR-018/смешанная/bonus/бонусов_меньше_лимита", mixed30, 100000, MainOrBonus, ChoiceBonus, Balances{100000, 10000}, nil, 90000, 10000, Balances{10000, 0}},
		// Цена 1 тиын, 50 %: лимит intdiv(50, 100) = 0 → «В клубе бонусами нельзя оплачивать время.» (как в старом коде).
		{"BR-018/лимит_0_из-за_округления", RawFinances{"allow_mixed_payment": true, "bonus_max_percent": 50}, 1, MainOrBonus, ChoiceBonus, Balances{100, 100}, ErrBonusNotAllowed, 0, 0, Balances{}},
		// Отрицательный основной баланс: даже цена 0 не проходит (mainBalance < mainPayment).
		{"BR-018/отрицательный_баланс_цена_0", nil, 0, MainBalanceOnly, ChoiceMain, Balances{-1, 0}, ErrInsufficientFunds, 0, 0, Balances{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pay, err := RulesFromFinances(c.finances).Pay(c.price, c.method, c.choice, c.balances)
			assertErr(t, err, c.wantErr)
			if c.wantErr != nil {
				return
			}
			if pay.Main != c.wantMain || pay.Bonus != c.wantBonus || pay.After != c.wantAfter {
				t.Fatalf("got %+v, want main %d bonus %d after %+v", pay, c.wantMain, c.wantBonus, c.wantAfter)
			}
		})
	}
}

// BR-018/пример-2: текст ошибки с процентом клуба; ключ ошибки — balance.
func TestPay_ErrorText(t *testing.T) {
	_, err := RulesFromFinances(RawFinances{"bonus_max_percent": 30}).Pay(100000, MainOrBonus, ChoiceBonus, Balances{80000, 50000})
	var e *Error
	if !errors.As(err, &e) || e.Field != "balance" || e.Params["percent"] != "30" {
		t.Fatalf("%#v", err)
	}
	if got := e.Error(); got != "balance: Бонусами можно оплатить только 30% цены, а смешанная оплата в клубе выключена. Оплатите с основного баланса." {
		t.Fatalf("%q", got)
	}
	if !errors.Is(err, ErrBonusPercentLimit) || errors.Is(err, ErrBonusNotEnough) {
		t.Fatal("errors.Is по коду")
	}
}

func TestPay_InvalidInput(t *testing.T) {
	r := DefaultPaymentRules()
	for name, f := range map[string]func() error{
		"цена<0": func() error { _, err := r.Pay(-1, MainOrBonus, ChoiceMain, Balances{}); return err },
		"баланс": func() error {
			_, err := r.Pay(1, MainOrBonus, ChoiceMain, Balances{Main: money.MaxAmount + 1})
			return err
		},
		"выбор_cash":    func() error { _, err := r.Pay(1, MainOrBonus, "cash", Balances{Main: 100}); return err },
		"цена>MaxPrice": func() error { _, err := r.Pay(MaxPrice+1, MainOrBonus, ChoiceMain, Balances{}); return err },
	} {
		if err := f(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
