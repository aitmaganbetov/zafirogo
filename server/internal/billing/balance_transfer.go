package billing

// Переводы основного баланса другу (BR-091) и подтверждение кодом из Telegram (BR-092).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// MaxTransferAmount — Friends::MAX_TRANSFER_CENTS (1 000 000 тенге), app/Services/Friends.php.
const MaxTransferAmount money.Tiyn = 100_000_000

// Лимиты кодов подтверждения перевода: app/Services/FriendTransferCodes.php
// CODE_MINUTES, RESEND_SECONDS, CODES_PER_HOUR, MAX_ATTEMPTS.
const (
	TransferCodeMinutes       = 5
	TransferCodeResendSeconds = 60
	TransferCodesPerHour      = 5
	TransferCodeMaxAttempts   = 5
)

// ErrFriendshipNotFound — дружбы с таким id нет в клубе (findOrFail → 404 в старом коде).
var ErrFriendshipNotFound = errors.New("billing: friendship not found")

// FriendTransferParams — перевод с основного баланса UserID другу по дружбе FriendshipID.
// Amount — money.ParseTransferAmount(ввод) (Friends::amountToCents(), BR-002: неверный ввод → 0).
// Настройку guests.allow_balance_transfer_to_friends и пароль проверяет вызывающий (GuestFriendController).
type FriendTransferParams struct {
	ClubID         int64
	UserID         int64
	FriendshipID   int64
	Amount         money.Tiyn
	IdempotencyKey string
	Actor          BalanceActor
}

// FriendTransferResult — ответ перевода: новый основной баланс отправителя и сумма.
type FriendTransferResult struct {
	Ledger   ledger.Result
	Balance  money.Tiyn // основной баланс отправителя после перевода
	Amount   money.Tiyn
	FriendID int64
}

// FriendTransfer переводит деньги с основного баланса пользователя на основной баланс друга (бонусы не переводятся,
// траты лояльности не меняются, кассы нет).
//
// Laravel: app/Services/Friends.php Friends::transfer() (~стр. 113–156):
//
//	$cents <= 0 || $cents > MAX_TRANSFER_CENTS → amount «Сумма перевода — от 0,01 до 1 000 000.»
//	дружба FOR UPDATE; не участник или не принята → friendship «Переводить можно только друзьям.»
//	оба member_balances FOR UPDATE ORDER BY user_id; нет любого → та же ошибка
//	storedCents(from) < $cents → amount «Недостаточно средств на основном счёте.»
//
// Проводки: member_main отправителя −amount / member_main друга +amount.
func FriendTransfer(ctx context.Context, q *store.Queries, p FriendTransferParams) (FriendTransferResult, error) {
	if p.Amount <= 0 || p.Amount > MaxTransferAmount {
		return FriendTransferResult{}, &BalanceError{Field: "amount", Message: msgTransferAmountRange}
	}
	key := p.IdempotencyKey
	request := map[string]any{"user_id": p.UserID, "friendship_id": p.FriendshipID, "amount": int64(p.Amount)}
	pending, replay, err := ledger.Begin(ctx, q, p.ClubID, ledger.KindFriendTransfer, key, request)
	if err != nil {
		return FriendTransferResult{}, err
	}
	if replay != nil {
		return transferResult(*replay, p.UserID, p.Amount), nil
	}

	f, err := q.LockFriendshipForTransfer(ctx, store.LockFriendshipForTransferParams{ClubID: p.ClubID, ID: p.FriendshipID})
	if errors.Is(err, pgx.ErrNoRows) {
		return FriendTransferResult{}, ErrFriendshipNotFound
	}
	if err != nil {
		return FriendTransferResult{}, fmt.Errorf("billing: lock friendship: %w", err)
	}
	// Laravel: Friendship::involves() / otherThan()
	if (f.RequesterID != p.UserID && f.AddresseeID != p.UserID) || f.AcceptedAt == nil {
		return FriendTransferResult{}, &BalanceError{Field: "friendship", Message: msgTransferOnlyFriends}
	}
	friendID := f.RequesterID
	if friendID == p.UserID {
		friendID = f.AddresseeID
	}
	balances, err := ledger.LockBalances(ctx, q, p.ClubID, p.UserID, friendID)
	if errors.Is(err, ledger.ErrBalanceNotFound) {
		return FriendTransferResult{}, &BalanceError{Field: "friendship", Message: msgTransferOnlyFriends}
	}
	if err != nil {
		return FriendTransferResult{}, err
	}
	if balances[p.UserID].Main < p.Amount {
		return FriendTransferResult{}, &BalanceError{Field: "amount", Message: msgNotEnoughMain}
	}
	meta, err := json.Marshal(map[string]any{"friendship_id": p.FriendshipID, "to_user_id": friendID})
	if err != nil {
		return FriendTransferResult{}, err
	}
	res, err := pending.Post(ctx, q, ledger.Operation{
		Entries: []ledger.Entry{
			{Account: ledger.Main(p.UserID), Amount: -p.Amount},
			{Account: ledger.Main(friendID), Amount: p.Amount},
		},
		Meta: meta, ActorUserID: p.UserID,
	})
	if err != nil {
		return FriendTransferResult{}, err
	}
	out := transferResult(res, p.UserID, p.Amount)
	out.FriendID = friendID
	return out, auditMoney(ctx, q, p.ClubID, p.Actor, "money.friend_transfer", p.UserID, res, map[string]any{
		"amount": int64(p.Amount), "to_user_id": friendID, "friendship_id": p.FriendshipID,
	})
}

func transferResult(res ledger.Result, userID int64, amount money.Tiyn) FriendTransferResult {
	out := FriendTransferResult{Ledger: res, Amount: amount}
	out.Balance, _ = res.BalanceAfter(ledger.Main(userID))
	for _, e := range res.Entries {
		if e.Account.Kind == ledger.AccountMemberMain && e.Account.UserID != userID {
			out.FriendID = e.Account.UserID
		}
	}
	return out
}

// TransferCodeMismatchError — код не подошёл (дружба, сумма или сам код): транзакцию перевода нужно
// откатить и отдельной транзакцией засчитать попытку (RecordTransferCodeAttempt) — как в старом коде
// ($wrong->increment('attempts') вне DB::transaction). Текст для клиента — тот же, что у устаревшего кода.
type TransferCodeMismatchError struct{ CodeID int64 }

func (e *TransferCodeMismatchError) Error() string { return "code: " + msgTransferCodeWrong }

// AsBalanceError — ошибка поля для ответа клиенту.
func (e *TransferCodeMismatchError) AsBalanceError() *BalanceError {
	return &BalanceError{Field: "code", Message: msgTransferCodeWrong}
}

// ConfirmTransferCodeParams — подтверждение перевода кодом.
type ConfirmTransferCodeParams struct {
	ClubID       int64
	UserID       int64
	FriendshipID int64
	AmountInput  string // как пришло от клиента (Friends::amountToCents)
	Code         string
	Now          time.Time
	// VerifyCode сравнивает код с transfer_codes.code_hash (bcrypt, Hash::check в старом коде).
	VerifyCode func(codeHash, code string) bool
	Actor      BalanceActor
}

// ConfirmFriendTransferCode — перевод по коду из Telegram.
//
// Laravel: app/Services/FriendTransferCodes.php FriendTransferCodes::confirm() (~стр. 72–103):
// последний (по id) неистёкший код пользователя FOR UPDATE; нет или attempts ≥ MAX_ATTEMPTS → code
// «Код неверный или устарел. Запросите новый код.»; дружба/сумма/код не совпали → попытка засчитывается
// вне транзакции (здесь — *TransferCodeMismatchError, см. RecordTransferCodeAttempt); иначе Friends::transfer()
// и удаление кодов пользователя. Ключ идемпотентности — "transfer_code:<id кода>": один код — один перевод.
func ConfirmFriendTransferCode(ctx context.Context, q *store.Queries, p ConfirmTransferCodeParams) (FriendTransferResult, error) {
	if p.VerifyCode == nil {
		return FriendTransferResult{}, fmt.Errorf("%w: VerifyCode is required", ledger.ErrInvalidOperation)
	}
	cents := money.ParseTransferAmount(p.AmountInput)
	code, err := q.LockLatestTransferCode(ctx, store.LockLatestTransferCodeParams{ClubID: p.ClubID, UserID: p.UserID, Now: p.Now})
	if errors.Is(err, pgx.ErrNoRows) {
		return FriendTransferResult{}, &BalanceError{Field: "code", Message: msgTransferCodeWrong}
	}
	if err != nil {
		return FriendTransferResult{}, fmt.Errorf("billing: lock transfer code: %w", err)
	}
	if code.Attempts >= TransferCodeMaxAttempts {
		return FriendTransferResult{}, &BalanceError{Field: "code", Message: msgTransferCodeWrong}
	}
	if code.FriendshipID != p.FriendshipID || code.AmountCents != int64(cents) || !p.VerifyCode(code.CodeHash, p.Code) {
		return FriendTransferResult{}, &TransferCodeMismatchError{CodeID: code.ID}
	}
	res, err := FriendTransfer(ctx, q, FriendTransferParams{
		ClubID: p.ClubID, UserID: p.UserID, FriendshipID: p.FriendshipID, Amount: cents,
		IdempotencyKey: "transfer_code:" + strconv.FormatInt(code.ID, 10), Actor: p.Actor,
	})
	if err != nil {
		return FriendTransferResult{}, err
	}
	if err := q.DeleteUserTransferCodesInClub(ctx, store.DeleteUserTransferCodesInClubParams{ClubID: p.ClubID, UserID: p.UserID}); err != nil {
		return FriendTransferResult{}, fmt.Errorf("billing: delete transfer codes: %w", err)
	}
	return res, nil
}

// RecordTransferCodeAttempt засчитывает неверную попытку кода (attempts + 1) — в своей транзакции,
// после отката транзакции ConfirmFriendTransferCode.
func RecordTransferCodeAttempt(ctx context.Context, q *store.Queries, clubID, codeID int64) error {
	return q.IncrementTransferCodeAttempts(ctx, store.IncrementTransferCodeAttemptsParams{ClubID: clubID, ID: codeID})
}
