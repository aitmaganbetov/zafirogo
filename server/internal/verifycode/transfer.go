package verifycode

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// TransferCodes — код подтверждения перевода другу (вместо пароля): привязан к дружбе и сумме, действует один раз.
// Отправляется только для перевода, который может пройти сейчас. Подтверждение — billing.ConfirmFriendTransferCode.
// Laravel: app/Services/FriendTransferCodes.php FriendTransferCodes::send() (BR-092).
//
// Лимиты (60 с, 5 в час) — на аккаунт во всех клубах, как в старом (transfer_codes без club_id); сам код — данные
// клуба (club_id, решение Q-028/L-1).
type TransferCodes struct {
	// App — пул zafiro_app (проверки перевода в транзакции клуба, RLS).
	App db.Beginner
	// Platform — пул zafiro_platform (лимиты по всем клубам, запись кода).
	Platform db.Beginner
	Gateway  Sender
	Hasher   Hasher
	Now      func() time.Time
	Log      *slog.Logger
}

func (t *TransferCodes) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Send — FriendTransferCodes::send(). Клуб и пользователь — из аутентификации. Ошибки: *Error (поле code),
// *billing.BalanceError (amount, friendship), billing.ErrFriendshipNotFound.
func (t *TransferCodes) Send(ctx context.Context, clubID, userID, friendshipID int64, amount money.Tiyn) error {
	ok := false
	if t.Gateway != nil {
		var err error
		if ok, err = t.Gateway.Configured(ctx); err != nil {
			return err
		}
	}
	if !ok {
		return &Error{Kind: KindUnavailable, Field: "code", Key: MsgTransferUnavailable}
	}
	// checkedCents(): сумма, дружба, баланс — до оплаты кода.
	if err := db.InTenantTxWith(ctx, t.App, clubID, db.Options{UserID: userID}, func(q *store.Queries) error {
		return billing.CheckFriendTransfer(ctx, q, clubID, userID, friendshipID, amount)
	}); err != nil {
		return err
	}
	code, err := newCode()
	if err != nil {
		return err
	}
	now := t.now()
	var phone string
	var codeID int64
	var limitErr *Error
	err = db.InPlatformTx(ctx, t.Platform, func(q *store.Queries) error {
		limitErr, codeID = nil, 0
		u, err := q.TelegramLockUser(ctx, userID)
		if err != nil {
			return err
		}
		phone = u.Phone
		if err := q.TelegramDeleteOldTransferCodes(ctx, store.TelegramDeleteOldTransferCodesParams{UserID: userID, Before: ptr(now.Add(-24 * time.Hour))}); err != nil {
			return err
		}
		st, err := q.TelegramTransferCodeStats(ctx, store.TelegramTransferCodeStatsParams{UserID: userID, Since: ptr(now.Add(-time.Hour))})
		if err != nil {
			return err
		}
		if limitErr = limits("code", now, st.LastAt, st.InHour, st.FirstInHour); limitErr != nil {
			return nil
		}
		hash, err := t.Hasher.Hash(ctx, code)
		if err != nil {
			return err
		}
		codeID, err = q.TelegramInsertTransferCode(ctx, store.TelegramInsertTransferCodeParams{
			ClubID: clubID, UserID: userID, FriendshipID: friendshipID, AmountCents: int64(amount),
			CodeHash: hash, ExpiresAt: now.Add(CodeTTL), Now: &now,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("verifycode: transfer code: %w", err)
	}
	if limitErr != nil {
		return limitErr
	}
	sent, err := t.Gateway.SendCode(ctx, phone, code, CodeTTL)
	if err != nil || !sent {
		if e := db.InPlatformTx(context.WithoutCancel(ctx), t.Platform, func(q *store.Queries) error {
			return q.TelegramDeleteTransferCode(ctx, store.TelegramDeleteTransferCodeParams{ClubID: clubID, ID: codeID})
		}); e != nil && t.Log != nil {
			t.Log.WarnContext(ctx, "transfer code: drop unsent code", "err", e)
		}
		if err != nil {
			return err
		}
		return &Error{Kind: KindNotSent, Field: "code", Key: MsgNotSent}
	}
	return nil
}
