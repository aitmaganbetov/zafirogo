package billing

// Операции с балансом клиента верхнего уровня. Деньги двигаются только через ledger (Begin/Post);
// формулы и проверки — буквально из Laravel (ссылки у каждой функции), тексты ошибок — ключи t().
// Вызывать внутри db.InTenantTx клуба. Права (кто может) проверяет вызывающий (HTTP/gRPC-слой).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// BalanceField — какой баланс клиента: значения как в формах старого кода (balance_type / top_up_balance).
type BalanceField string

// Балансы клиента.
const (
	FieldMain  BalanceField = "balance"       // основной
	FieldBonus BalanceField = "bonus_balance" // бонусный
)

// Valid сообщает, что поле — одно из двух балансов.
func (f BalanceField) Valid() bool { return f == FieldMain || f == FieldBonus }

func (f BalanceField) account(userID int64) ledger.Account {
	if f == FieldBonus {
		return ledger.Bonus(userID)
	}
	return ledger.Main(userID)
}

// BalanceActor — кто выполняет операцию (для ledger и аудита).
type BalanceActor struct {
	UserID    int64  // 0 — система
	Kind      string // staff, guest, agent, api_key, superadmin, system (audit_log.actor_kind)
	IP        netip.Addr
	RequestID string
}

// BalanceError — ошибка проверки с полем формы и текстом-ключом перевода (как ValidationException::withMessages).
type BalanceError struct {
	Field   string
	Message string            // русский текст = ключ t()
	Params  map[string]string // подстановки :name
}

func (e *BalanceError) Error() string {
	msg := e.Message
	for k, v := range e.Params {
		msg = strings.ReplaceAll(msg, ":"+k, v)
	}
	return e.Field + ": " + msg
}

// ErrBalanceAmountOutOfRange — сумма вне допустимого правилами формы диапазона (валидация запроса).
var ErrBalanceAmountOutOfRange = errors.New("billing: amount out of range")

// ErrBalanceAmountDecimals — больше двух знаков после точки (правило decimal, 422). Решение Q-002 (docs/DECISIONS.md).
var ErrBalanceAmountDecimals = errors.New("billing: more than two decimal places")

// Тексты ошибок старого кода (ключи перевода).
const (
	msgTransferAmountRange   = "Сумма перевода — от 0,01 до 1 000 000."
	msgTransferOnlyFriends   = "Переводить можно только друзьям."
	msgNotEnoughMain         = "Недостаточно средств на основном счёте."
	msgWriteOffMoreThanFunds = "На балансе клиента меньше суммы списания: :amount."
	msgTopUpAlreadySpent     = "Клиент уже потратил часть пополнения: на балансе меньше суммы возврата."
	msgTransferCodeWrong     = "Код неверный или устарел. Запросите новый код."
)

// MaxCashTopUp — StoreBalanceTopUpRequest: amount ≤ 9 999 999 999.99 (BR-053).
const MaxCashTopUp money.Tiyn = 999_999_999_999

// ---------------------------------------------------------------------------
// Пополнение на кассе (BR-053) и бонус за пополнение (BR-041)
// ---------------------------------------------------------------------------

// TopUpBonusFunc — расчёт бонуса за пополнение основного баланса (TopUpBonus::for(), BR-041): реализует пакет
// бонусов по настройкам клуба; nil — бонуса нет.
type TopUpBonusFunc func(amount money.Tiyn) (money.Tiyn, error)

// TopUpParams — пополнение баланса клиента через кассу.
type TopUpParams struct {
	ClubID int64
	UserID int64
	Field  BalanceField
	Amount money.Tiyn
	// BonusBase — от чего считать бонус за пополнение: (int) round($amount * 100) (cash.TopUpAmount.BonusBase, CQ-4).
	// Laravel: app/Services/TopUpBonus.php TopUpBonus::for() — $cents = (int) round($amount * 100), а баланс получает
	// (float) → decimal(12,2); суммы расходятся только на вводе с > 14 значащими цифрами. 0 — считать от Amount.
	BonusBase         money.Tiyn
	PaymentMethod     string // cash, card, qr (Q-013: хранится, в остатке не разделяется)
	CashShiftID       int64
	CashTransactionID int64
	IdempotencyKey    string
	Bonus             TopUpBonusFunc
	Actor             BalanceActor
}

// TopUpResult — итог пополнения.
type TopUpResult struct {
	Ledger          ledger.Result
	NewBalance      money.Tiyn // баланс Field после операции
	Bonus           money.Tiyn // бонус за пополнение (0 — нет)
	NewBonusBalance money.Tiyn // бонусный баланс после операции (если менялся)
}

// TopUp — пополнение основного или бонусного баланса на кассе.
//
// Laravel: app/Services/CashRegisterService.php CashRegisterService::topUpBalance() (~стр. 100–143):
// баланс FOR UPDATE (нет строки — 404); <balance_type> += amount; бонус только при balance_type = balance
// (TopUpBonus::for(), стр. ~114); бонус — в bonus_balance, не кассовый приход.
// Проводки: счёт клиента +amount / касса −amount (приход в кассу); бонус: member_bonus +bonus / bonus_fund −bonus.
// Кассовый документ (cash_transactions) и журнал действий пишет вызывающий (пакет cash).
func TopUp(ctx context.Context, q *store.Queries, p TopUpParams) (TopUpResult, error) {
	if !p.Field.Valid() || p.UserID <= 0 {
		return TopUpResult{}, fmt.Errorf("%w: user and balance field are required", ledger.ErrInvalidOperation)
	}
	if p.Amount <= 0 || p.Amount > MaxCashTopUp || p.BonusBase < 0 {
		return TopUpResult{}, ErrBalanceAmountOutOfRange
	}
	request := map[string]any{
		"user_id": p.UserID, "field": p.Field, "amount": int64(p.Amount),
		"payment_method": p.PaymentMethod, "cash_transaction_id": p.CashTransactionID,
	}
	if p.BonusBase > 0 && p.BonusBase != p.Amount {
		request["bonus_base"] = int64(p.BonusBase) // в отпечатке, только если отличается (прежние ключи не меняются)
	}
	pending, replay, err := ledger.Begin(ctx, q, p.ClubID, ledger.KindTopUp, p.IdempotencyKey, request)
	if err != nil {
		return TopUpResult{}, err
	}
	if replay != nil {
		return topUpResult(*replay, p.UserID, p.Field), nil
	}

	if _, err := ledger.LockBalances(ctx, q, p.ClubID, p.UserID); err != nil {
		return TopUpResult{}, err
	}
	var bonus money.Tiyn
	if p.Field == FieldMain && p.Bonus != nil {
		// Laravel: CashRegisterService::topUpBalance(): $bonus = $balanceType === 'balance' ? $this->topUpBonus->for($amount) : 0
		base := p.Amount
		if p.BonusBase > 0 {
			base = p.BonusBase // CQ-4
		}
		if bonus, err = p.Bonus(base); err != nil {
			return TopUpResult{}, err
		}
		if bonus < 0 {
			return TopUpResult{}, fmt.Errorf("%w: negative top-up bonus", ledger.ErrInvalidOperation)
		}
	}
	entries := []ledger.Entry{
		{Account: p.Field.account(p.UserID), Amount: p.Amount},
		{Account: ledger.ClubAccount(ledger.AccountCashDrawer), Amount: -p.Amount},
	}
	if bonus > 0 {
		entries = append(entries,
			ledger.Entry{Account: ledger.Bonus(p.UserID), Amount: bonus},
			ledger.Entry{Account: ledger.ClubAccount(ledger.AccountBonusFund), Amount: -bonus})
	}
	meta, err := json.Marshal(map[string]any{"payment_method": p.PaymentMethod, "bonus": int64(bonus)})
	if err != nil {
		return TopUpResult{}, err
	}
	res, err := pending.Post(ctx, q, ledger.Operation{
		Entries: entries, Meta: meta, ActorUserID: p.Actor.UserID,
		Refs: ledger.Refs{CashShiftID: p.CashShiftID, CashTransactionID: p.CashTransactionID},
	})
	if err != nil {
		return TopUpResult{}, err
	}
	out := topUpResult(res, p.UserID, p.Field)
	return out, auditMoney(ctx, q, p.ClubID, p.Actor, "money.top_up", p.UserID, res, map[string]any{
		"field": p.Field, "amount": int64(p.Amount), "bonus": int64(out.Bonus), "payment_method": p.PaymentMethod,
		"new_balance": int64(out.NewBalance),
	})
}

func topUpResult(res ledger.Result, userID int64, field BalanceField) TopUpResult {
	out := TopUpResult{Ledger: res}
	out.NewBalance, _ = res.BalanceAfter(field.account(userID))
	if field == FieldMain {
		out.Bonus = res.Amount(ledger.Bonus(userID))
	}
	out.NewBonusBalance, _ = res.BalanceAfter(ledger.Bonus(userID))
	return out
}

// ---------------------------------------------------------------------------
// Возврат пополнения по чеку (BR-055, ветка top_up)
// ---------------------------------------------------------------------------

// RefundTopUpParams — возврат пополнения. Bonus — receipt_data.top_up.bonus прихода в тиынах.
type RefundTopUpParams struct {
	ClubID                  int64
	UserID                  int64 // receipt_data.top_up.user_id
	Field                   BalanceField
	Amount                  money.Tiyn // сумма прихода
	Bonus                   money.Tiyn
	IncomeCashTransactionID int64 // возвращаемый приход
	CashShiftID             int64
	RefundCashTransactionID int64  // расход-возврат, если уже создан
	ReversesTransactionID   int64  // ledger-операция пополнения, если известна
	IdempotencyKey          string // пусто — "top_up_refund:<IncomeCashTransactionID>"
	Actor                   BalanceActor
}

// RefundTopUpResult — итог возврата пополнения.
type RefundTopUpResult struct {
	Ledger          ledger.Result
	NewBalance      money.Tiyn // баланс Field после возврата
	BonusTakenBack  money.Tiyn
	NewBonusBalance money.Tiyn
}

// RefundTopUp забирает с баланса клиента сумму пополнения и, сколько осталось, бонус за него.
//
// Laravel: app/Services/CashRegisterService.php CashRegisterService::refund() (~стр. 174–186):
//
//	$balance = MemberBalance::lockForUpdate(); null или $balance->{$field} < amount → «Клиент уже потратил часть пополнения…»
//	$balance->decrement($field, amount); $bonus = min(top_up.bonus, bonus_balance после списания); $bonus > 0 → decrement
//
// Проверки смены и остатка кассы («Вернуть можно только операцию открытой смены.», «В кассе недостаточно
// средств.») и кассовый расход — у вызывающего (пакет cash).
// Проводки: счёт клиента −amount / касса +amount (выдача из кассы); бонус: member_bonus −bonus / bonus_fund +bonus.
func RefundTopUp(ctx context.Context, q *store.Queries, p RefundTopUpParams) (RefundTopUpResult, error) {
	if !p.Field.Valid() || p.UserID <= 0 || p.Amount <= 0 || p.Bonus < 0 {
		return RefundTopUpResult{}, fmt.Errorf("%w: bad top-up refund", ledger.ErrInvalidOperation)
	}
	key := p.IdempotencyKey
	if key == "" {
		key = "top_up_refund:" + strconv.FormatInt(p.IncomeCashTransactionID, 10)
	}
	request := map[string]any{
		"user_id": p.UserID, "field": p.Field, "amount": int64(p.Amount), "bonus": int64(p.Bonus),
		"income_cash_transaction_id": p.IncomeCashTransactionID,
	}
	pending, replay, err := ledger.Begin(ctx, q, p.ClubID, ledger.KindTopUpRefund, key, request)
	if err != nil {
		return RefundTopUpResult{}, err
	}
	if replay != nil {
		return refundTopUpResult(*replay, p.UserID, p.Field), nil
	}

	balances, err := ledger.LockBalances(ctx, q, p.ClubID, p.UserID)
	if errors.Is(err, ledger.ErrBalanceNotFound) {
		return RefundTopUpResult{}, &BalanceError{Field: "refund", Message: msgTopUpAlreadySpent}
	}
	if err != nil {
		return RefundTopUpResult{}, err
	}
	b := balances[p.UserID]
	fieldBalance := b.Main
	if p.Field == FieldBonus {
		fieldBalance = b.Bonus
	}
	if fieldBalance < p.Amount {
		return RefundTopUpResult{}, &BalanceError{Field: "refund", Message: msgTopUpAlreadySpent}
	}
	bonusAfter := b.Bonus
	if p.Field == FieldBonus {
		bonusAfter -= p.Amount
	}
	// Laravel: $bonus = min((float) ($topUp['bonus'] ?? 0), (float) $balance->refresh()->bonus_balance); if ($bonus > 0)
	bonusBack := min(p.Bonus, bonusAfter)
	if bonusBack < 0 {
		bonusBack = 0
	}

	deltas := map[ledger.Account]money.Tiyn{
		p.Field.account(p.UserID):                    -p.Amount,
		ledger.ClubAccount(ledger.AccountCashDrawer): p.Amount,
	}
	if bonusBack > 0 {
		deltas[ledger.Bonus(p.UserID)] -= bonusBack
		deltas[ledger.ClubAccount(ledger.AccountBonusFund)] += bonusBack
	}
	meta, err := json.Marshal(map[string]any{"bonus_taken_back": int64(bonusBack)})
	if err != nil {
		return RefundTopUpResult{}, err
	}
	res, err := pending.Post(ctx, q, ledger.Operation{
		Entries: entriesOf(deltas), Meta: meta, ActorUserID: p.Actor.UserID,
		Refs: ledger.Refs{
			CashShiftID: p.CashShiftID, CashTransactionID: p.RefundCashTransactionID,
			ReversesTransactionID: p.ReversesTransactionID,
		},
	})
	if err != nil {
		return RefundTopUpResult{}, err
	}
	out := refundTopUpResult(res, p.UserID, p.Field)
	return out, auditMoney(ctx, q, p.ClubID, p.Actor, "money.top_up_refund", p.UserID, res, map[string]any{
		"field": p.Field, "amount": int64(p.Amount), "bonus_taken_back": int64(out.BonusTakenBack),
		"income_cash_transaction_id": p.IncomeCashTransactionID,
	})
}

func refundTopUpResult(res ledger.Result, userID int64, field BalanceField) RefundTopUpResult {
	out := RefundTopUpResult{Ledger: res}
	out.NewBalance, _ = res.BalanceAfter(field.account(userID))
	out.NewBonusBalance, _ = res.BalanceAfter(ledger.Bonus(userID))
	// Списание бонуса за пополнение = кредит бонусного фонда.
	out.BonusTakenBack = res.Amount(ledger.ClubAccount(ledger.AccountBonusFund))
	return out
}

// ---------------------------------------------------------------------------
// Пополнение и списание владельцем без кассы (BR-058), начальный баланс (BR-059)
// ---------------------------------------------------------------------------

// OwnerAdjustmentMax — UserController::update(): top_up_amount numeric min:0.01 max:10000000.
const OwnerAdjustmentMax money.Tiyn = 1_000_000_000

// ParseOwnerAdjustmentAmount — сумма пополнения/списания владельцем из формы.
//
// Laravel: app/Http/Controllers/UserController.php UserController::update() (~стр. 95, 113):
// правило 'numeric', 'min:0.01', 'max:10000000'; затем $topUp = round((float) $data['top_up_amount'], 2).
// Решение Q-002 (docs/DECISIONS.md): разбор строгий (money.ParseTenge): > 2 знаков после точки — ErrBalanceAmountDecimals
// (422), а не round(…, 2) — было "1234.565" → 1234.57 (BR-058); экспонента, запятая — ErrBalanceAmountOutOfRange.
// Для ввода с ≤ 2 знаками сумма та же, что в старом коде.
func ParseOwnerAdjustmentAmount(s string) (money.Tiyn, error) {
	t, err := money.ParseTenge(strings.TrimSpace(s))
	switch {
	case errors.Is(err, money.ErrTooManyDecimals):
		return 0, ErrBalanceAmountDecimals
	case err != nil:
		return 0, ErrBalanceAmountOutOfRange
	case t < 1 || t > OwnerAdjustmentMax:
		return 0, ErrBalanceAmountOutOfRange
	}
	return t, nil
}

// OwnerAdjustParams — пополнение (WriteOff = false) или списание баланса владельцем без кассы.
type OwnerAdjustParams struct {
	ClubID         int64
	UserID         int64
	Field          BalanceField
	WriteOff       bool
	Amount         money.Tiyn // ParseOwnerAdjustmentAmount
	IdempotencyKey string
	Actor          BalanceActor
}

// OwnerAdjustResult — итог корректировки.
type OwnerAdjustResult struct {
	Ledger     ledger.Result
	NewBalance money.Tiyn
}

// OwnerAdjust — пополнение/списание баланса клиента владельцем без кассы: без кассовой операции,
// без бонуса за пополнение (GAP-020), траты лояльности не меняются.
//
// Laravel: app/Http/Controllers/UserController.php UserController::update() (~стр. 127–163):
// write_off — баланс FOR UPDATE, (float) $balance->{$field} < $topUp → ошибка top_up_amount
// «На балансе клиента меньше суммы списания: :amount.» (number_format(…, 2, '.', ' ')); иначе decrement.
// top_up — баланс FOR UPDATE (firstOrFail), increment. Право (только роль owner, canTopUpWithoutCash()) — у вызывающего.
// Проводки: счёт клиента ±amount / owner_adjustment ∓amount.
func OwnerAdjust(ctx context.Context, q *store.Queries, p OwnerAdjustParams) (OwnerAdjustResult, error) {
	if !p.Field.Valid() || p.UserID <= 0 {
		return OwnerAdjustResult{}, fmt.Errorf("%w: user and balance field are required", ledger.ErrInvalidOperation)
	}
	if p.Amount <= 0 || p.Amount > OwnerAdjustmentMax {
		return OwnerAdjustResult{}, ErrBalanceAmountOutOfRange
	}
	kind := ledger.KindOwnerTopUp
	if p.WriteOff {
		kind = ledger.KindOwnerWriteOff
	}
	request := map[string]any{"user_id": p.UserID, "field": p.Field, "amount": int64(p.Amount)}
	pending, replay, err := ledger.Begin(ctx, q, p.ClubID, kind, p.IdempotencyKey, request)
	if err != nil {
		return OwnerAdjustResult{}, err
	}
	if replay != nil {
		out := OwnerAdjustResult{Ledger: *replay}
		out.NewBalance, _ = replay.BalanceAfter(p.Field.account(p.UserID))
		return out, nil
	}

	balances, err := ledger.LockBalances(ctx, q, p.ClubID, p.UserID)
	if err != nil {
		return OwnerAdjustResult{}, err
	}
	acc := p.Field.account(p.UserID)
	delta := p.Amount
	if p.WriteOff {
		if current := balances.Of(acc); current < p.Amount {
			return OwnerAdjustResult{}, &BalanceError{
				Field: "top_up_amount", Message: msgWriteOffMoreThanFunds,
				Params: map[string]string{"amount": numberFormatSpaced(current)},
			}
		}
		delta = -p.Amount
	}
	res, err := pending.Post(ctx, q, ledger.Operation{
		Entries: []ledger.Entry{
			{Account: acc, Amount: delta},
			{Account: ledger.ClubAccount(ledger.AccountOwnerAdjustment), Amount: -delta},
		},
		ActorUserID: p.Actor.UserID,
	})
	if err != nil {
		return OwnerAdjustResult{}, err
	}
	out := OwnerAdjustResult{Ledger: res}
	out.NewBalance, _ = res.BalanceAfter(acc)
	return out, auditMoney(ctx, q, p.ClubID, p.Actor, "money."+string(kind), p.UserID, res, map[string]any{
		"field": p.Field, "amount": int64(p.Amount), "new_balance": int64(out.NewBalance), "without_cash": true,
	})
}

// InitialBalanceParams — начальные балансы нового клиента.
type InitialBalanceParams struct {
	ClubID int64
	UserID int64
	Main   money.Tiyn
	Bonus  money.Tiyn
	Actor  BalanceActor
}

// SetInitialBalance — начальный основной и бонусный баланс нового клиента (без кассы и бонуса).
//
// Laravel: app/Http/Controllers/UserController.php UserController::store() (~стр. 56–83): member_balances
// создаётся сразу с balance/bonus_balance из формы (право customer_money.update — у вызывающего).
// Здесь строка member_balances уже создана с нулями, суммы — операцией initial_balance с ключом
// "initial_balance:<user_id>". Нулевые суммы — без операции.
func SetInitialBalance(ctx context.Context, q *store.Queries, p InitialBalanceParams) (ledger.Result, error) {
	if p.UserID <= 0 || p.Main < 0 || p.Bonus < 0 {
		return ledger.Result{}, fmt.Errorf("%w: bad initial balance", ledger.ErrInvalidOperation)
	}
	if p.Main == 0 && p.Bonus == 0 {
		return ledger.Result{}, nil
	}
	deltas := map[ledger.Account]money.Tiyn{
		ledger.Main(p.UserID):                             p.Main,
		ledger.Bonus(p.UserID):                            p.Bonus,
		ledger.ClubAccount(ledger.AccountOwnerAdjustment): -(p.Main + p.Bonus),
	}
	res, err := ledger.Post(ctx, q, ledger.Operation{
		ClubID: p.ClubID, Kind: ledger.KindInitialBalance,
		IdempotencyKey: "initial_balance:" + strconv.FormatInt(p.UserID, 10),
		Entries:        entriesOf(deltas), ActorUserID: p.Actor.UserID,
	})
	if err != nil || res.Replayed {
		return res, err
	}
	return res, auditMoney(ctx, q, p.ClubID, p.Actor, "money.initial_balance", p.UserID, res, map[string]any{
		"balance": int64(p.Main), "bonus_balance": int64(p.Bonus),
	})
}

// MigrateOpeningBalance — входящий остаток клиента при переносе клуба из MySQL (tools/migrate-from-mysql):
// операция migration_opening_balance с ключом "opening_balance:<user_id>", счёт opening_balance.
// Допускает отрицательные остатки старых данных (Q-004).
func MigrateOpeningBalance(ctx context.Context, q *store.Queries, clubID, userID int64, main, bonus money.Tiyn) (ledger.Result, error) {
	if main == 0 && bonus == 0 {
		return ledger.Result{}, nil
	}
	deltas := map[ledger.Account]money.Tiyn{
		ledger.Main(userID):                              main,
		ledger.Bonus(userID):                             bonus,
		ledger.ClubAccount(ledger.AccountOpeningBalance): -(main + bonus),
	}
	return ledger.Post(ctx, q, ledger.Operation{
		ClubID: clubID, Kind: ledger.KindMigrationOpeningBalance,
		IdempotencyKey: "opening_balance:" + strconv.FormatInt(userID, 10),
		Entries:        entriesOf(deltas),
	})
}

// ---------------------------------------------------------------------------
// Списание за покупку и возврат (общие для тарифов, минут, магазина, сессий)
// ---------------------------------------------------------------------------

// ChargeParams — списание с основного и/или бонусного баланса в выручку (покупка). Суммы уже посчитаны
// правилами покупки (BR-018 и др.), здесь только движение денег.
type ChargeParams struct {
	ClubID         int64
	UserID         int64
	Kind           ledger.Kind        // tariff_purchase, minutes_purchase, shop_order, …
	Revenue        ledger.AccountKind // revenue_time, revenue_shop, revenue_pos
	Main           money.Tiyn         // ≥ 0
	Bonus          money.Tiyn         // ≥ 0
	IdempotencyKey string
	Refs           ledger.Refs
	Meta           json.RawMessage
	Actor          BalanceActor
}

// ChargeResult — балансы после операции.
type ChargeResult struct {
	Ledger          ledger.Result
	NewBalance      money.Tiyn
	NewBonusBalance money.Tiyn
}

// Charge — списание за покупку: основной → выручка, бонусы → бонусный фонд (бонусы не выручка, BR-042).
// Недостаточно средств — *ledger.InsufficientFundsError (текст ошибки — у вызывающего: у каждой покупки свой,
// например «Недостаточно средств для покупки тарифа.» BR-018, «Недостаточно средств на основном счёте.» BR-075).
func Charge(ctx context.Context, q *store.Queries, p ChargeParams) (ChargeResult, error) {
	return moveForPurchase(ctx, q, p, -1)
}

// Credit — возврат покупки на балансы (обратные проводки Charge): BR-032 (session_refund), BR-077 (shop_order_cancel).
// Нет строки member_balances — *ledger.BalanceNotFoundError (старый SessionSettlement::settle() тогда деньги
// не начислял, OQ-BR-12 — решает вызывающий).
func Credit(ctx context.Context, q *store.Queries, p ChargeParams) (ChargeResult, error) {
	return moveForPurchase(ctx, q, p, 1)
}

func moveForPurchase(ctx context.Context, q *store.Queries, p ChargeParams, sign money.Tiyn) (ChargeResult, error) {
	if p.UserID <= 0 || p.Main < 0 || p.Bonus < 0 || (p.Main == 0 && p.Bonus == 0) {
		return ChargeResult{}, fmt.Errorf("%w: bad charge amounts", ledger.ErrInvalidOperation)
	}
	if p.Revenue == "" || p.Revenue.Member() {
		return ChargeResult{}, fmt.Errorf("%w: revenue account is required", ledger.ErrInvalidOperation)
	}
	deltas := map[ledger.Account]money.Tiyn{}
	if p.Main > 0 {
		deltas[ledger.Main(p.UserID)] += sign * p.Main
		deltas[ledger.ClubAccount(p.Revenue)] -= sign * p.Main
	}
	if p.Bonus > 0 {
		deltas[ledger.Bonus(p.UserID)] += sign * p.Bonus
		deltas[ledger.ClubAccount(ledger.AccountBonusFund)] -= sign * p.Bonus
	}
	res, err := ledger.Post(ctx, q, ledger.Operation{
		ClubID: p.ClubID, Kind: p.Kind, IdempotencyKey: p.IdempotencyKey,
		Entries: entriesOf(deltas), Meta: p.Meta, ActorUserID: p.Actor.UserID, Refs: p.Refs,
	})
	if err != nil {
		return ChargeResult{}, err
	}
	out := ChargeResult{Ledger: res}
	out.NewBalance, _ = res.BalanceAfter(ledger.Main(p.UserID))
	out.NewBonusBalance, _ = res.BalanceAfter(ledger.Bonus(p.UserID))
	if res.Replayed {
		return out, nil
	}
	return out, auditMoney(ctx, q, p.ClubID, p.Actor, "money."+string(p.Kind), p.UserID, res, map[string]any{
		"main": int64(p.Main), "bonus": int64(p.Bonus),
	})
}

// ---------------------------------------------------------------------------
// Общее
// ---------------------------------------------------------------------------

// entriesOf — проводки из сумм по счетам (нулевые пропускаются), в стабильном порядке.
func entriesOf(deltas map[ledger.Account]money.Tiyn) []ledger.Entry {
	out := make([]ledger.Entry, 0, len(deltas))
	for acc, amount := range deltas {
		if amount != 0 {
			out = append(out, ledger.Entry{Account: acc, Amount: amount})
		}
	}
	sortEntries(out)
	return out
}

func sortEntries(e []ledger.Entry) {
	for i := 1; i < len(e); i++ {
		for j := i; j > 0 && e[j].Account.String() < e[j-1].Account.String(); j-- {
			e[j], e[j-1] = e[j-1], e[j]
		}
	}
}

// numberFormatSpaced — PHP number_format($x, 2, '.', ' ') для суммы с двумя знаками: "1 234.50", "-12.00".
func numberFormatSpaced(t money.Tiyn) string {
	s := t.Tenge()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	out := b.String() + "." + frac
	if neg {
		out = "-" + out
	}
	return out
}

// auditMoney пишет запись аудита о денежной операции (категория money).
func auditMoney(ctx context.Context, q *store.Queries, clubID int64, actor BalanceActor, action string, userID int64, res ledger.Result, after map[string]any) error {
	body, err := json.Marshal(after)
	if err != nil {
		return err
	}
	kind := actor.Kind
	if kind == "" {
		kind = "system"
	}
	subjectType := "user"
	params := store.InsertAuditLogParams{
		ClubID:              &clubID,
		ActorKind:           kind,
		Category:            "money",
		Action:              action,
		SubjectType:         &subjectType,
		SubjectID:           &userID,
		LedgerTransactionID: &res.TransactionID,
		After:               body,
	}
	if actor.UserID > 0 {
		params.ActorUserID = &actor.UserID
	}
	if actor.IP.IsValid() {
		ip := actor.IP
		params.Ip = &ip
	}
	if actor.RequestID != "" {
		params.RequestID = &actor.RequestID
	}
	if err := q.InsertAuditLog(ctx, params); err != nil {
		return fmt.Errorf("billing: audit: %w", err)
	}
	return nil
}
