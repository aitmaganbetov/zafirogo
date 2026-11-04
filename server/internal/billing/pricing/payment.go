package pricing

import (
	"fmt"
	"strconv"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// BalanceChoice — выбор баланса клиентом в агенте (AgentTariffPurchaseService::BALANCES).
type BalanceChoice string

// Выбор баланса.
const (
	ChoiceNone  BalanceChoice = ""      // не выбран (агенты до 0.0.57): сначала основной, затем бонусы
	ChoiceMain  BalanceChoice = "main"  // всё с основного
	ChoiceBonus BalanceChoice = "bonus" // бонусами до процента клуба, остальное — с основного при смешанной оплате
)

// Balances — основной и бонусный балансы клиента в тиынах (member_balances под FOR UPDATE).
type Balances struct {
	Main  money.Tiyn
	Bonus money.Tiyn
}

func (b Balances) check() error {
	if !b.Main.Valid() || !b.Bonus.Valid() {
		return fmt.Errorf("%w: баланс вне ±%d", ErrInvalidInput, money.MaxAmount)
	}
	return nil
}

// Payment — раскладка цены по балансам и балансы после списания.
type Payment struct {
	Main  money.Tiyn // main_cents части сессии; идёт в накопительные траты (BR-042)
	Bonus money.Tiyn // bonus_cents
	After Balances
}

// Pay раскладывает цену по балансам так же, как sell():
//
//	$bonusLimit = $paymentMethod === 'main_balance_only' ? 0 : bonusLimitCents($price);
//	'bonus': $bonusLimit === 0 && $price > 0 → ошибка (тариф только с основного / «В клубе бонусами нельзя…»)
//	         $bonusPayment = min($bonusBalance, $bonusLimit);
//	         !$mixed && $bonusPayment < $price → ошибка («Бонусами можно оплатить только N%…», если $bonusLimit < $price,
//	                                             иначе «Бонусов не хватает на всю цену…»)
//	'main':  $bonusPayment = 0
//	нет выбора и $mixed:  $bonusPayment = min($bonusLimit, max(0, $price - $mainBalance))
//	нет выбора, без смешанной: $bonusPayment = $mainBalance < $price && $bonusLimit >= $price && $bonusBalance >= $price ? $price : 0
//	$mainPayment = $price - $bonusPayment;
//	$mainBalance < $mainPayment || $bonusBalance < $bonusPayment → «Недостаточно средств для покупки тарифа.»
//
// Laravel: app/Services/AgentTariffPurchaseService.php sell() (стр. 236–269) (BR-018, GS-035, GS-039, GS-049, GS-050,
// GAP-006…GAP-008, GAP-011). Поминутная покупка передаёт MainOrBonus, запуск пакета — MainBalanceOnly, ChoiceMain и цену 0.
// Выбор вне main/bonus отклоняет валидация запроса (Rule::in(BALANCES)) — здесь это ErrInvalidInput.
func (r PaymentRules) Pay(price money.Tiyn, method PaymentMethod, choice BalanceChoice, b Balances) (Payment, error) {
	if err := checkPrice(price); err != nil {
		return Payment{}, err
	}
	if err := b.check(); err != nil {
		return Payment{}, err
	}
	var bonusLimit money.Tiyn
	if method != MainBalanceOnly {
		bonusLimit = r.BonusLimit(price)
	}

	var bonusPayment money.Tiyn
	switch choice {
	case ChoiceBonus:
		if bonusLimit == 0 && price > 0 {
			if method == MainBalanceOnly {
				return Payment{}, ErrMainBalanceOnly
			}
			return Payment{}, ErrBonusNotAllowed
		}
		bonusPayment = min(b.Bonus, bonusLimit)
		if !r.MixedPayment && bonusPayment < price {
			if bonusLimit < price {
				return Payment{}, ErrBonusPercentLimit.with(map[string]string{"percent": strconv.FormatInt(r.BonusMaxPercent, 10)})
			}
			return Payment{}, ErrBonusNotEnough
		}
	case ChoiceMain:
		bonusPayment = 0
	case ChoiceNone:
		if r.MixedPayment {
			bonusPayment = min(bonusLimit, max(0, price-b.Main))
		} else if b.Main < price && bonusLimit >= price && b.Bonus >= price {
			// One balance: the main one, or bonuses for the whole price when the main one is short.
			bonusPayment = price
		}
	default:
		return Payment{}, fmt.Errorf("%w: выбор баланса %q", ErrInvalidInput, choice)
	}
	mainPayment := price - bonusPayment
	if b.Main < mainPayment || b.Bonus < bonusPayment {
		return Payment{}, ErrInsufficientFunds
	}
	return Payment{
		Main:  mainPayment,
		Bonus: bonusPayment,
		After: Balances{Main: b.Main - mainPayment, Bonus: b.Bonus - bonusPayment},
	}, nil
}
