package verifycode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// PasswordRecovery — «Забыли пароль» на сайте и в агенте ПК: 6-значный код уходит в Telegram на телефон аккаунта,
// код задаёт новый пароль. Неизвестный и заблокированный номер получают тот же ответ, что и известный (код не шлётся).
// Laravel: app/Services/PasswordRecovery.php.
type PasswordRecovery struct {
	// Platform — пул zafiro_platform (users, password_recovery_codes, platform_logs).
	Platform db.Beginner
	Gateway  Sender
	Hasher   Hasher
	// SignOut — выход везде после смены пароля (сеансы панели, токены гостя на ПК, токены мобильных приложений):
	// в старом коде $user->tokens()->delete() и удаление строк sessions. Вызывается после коммита.
	SignOut func(ctx context.Context, userID int64)
	Now     func() time.Time
	Log     *slog.Logger
}

func (p *PasswordRecovery) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *PasswordRecovery) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// Available — PasswordRecovery::available(): Gateway настроен.
func (p *PasswordRecovery) Available(ctx context.Context) (bool, error) {
	if p == nil || p.Gateway == nil {
		return false, nil
	}
	return p.Gateway.Configured(ctx)
}

// account — PasswordRecovery::account(): пользователь по телефону; нет или заблокирован — nil.
func account(ctx context.Context, q *store.Queries, phone string) (*store.TelegramUserByPhoneRow, error) {
	u, err := q.TelegramUserByPhone(ctx, phone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if u.BlockedAt != nil {
		return nil, nil
	}
	return &u, nil
}

// SendCode — PasswordRecovery::sendCode(): phone — уже нормализованный +7XXXXXXXXXX. Ошибки для пользователя —
// *Error (поле phone).
func (p *PasswordRecovery) SendCode(ctx context.Context, phone string) error {
	ok, err := p.Available(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return &Error{Kind: KindUnavailable, Field: "phone", Key: MsgRecoveryUnavailable}
	}
	code, err := newCode()
	if err != nil {
		return err
	}
	now := p.now()
	var user *store.TelegramUserByPhoneRow
	var codeID int64
	var limitErr *Error
	err = db.InPlatformTx(ctx, p.Platform, func(q *store.Queries) error {
		limitErr, codeID = nil, 0
		var err error
		if user, err = account(ctx, q, phone); err != nil || user == nil {
			return err
		}
		if _, err := q.TelegramLockUser(ctx, user.ID); err != nil {
			return err
		}
		if err := q.TelegramDeleteOldRecoveryCodes(ctx, store.TelegramDeleteOldRecoveryCodesParams{UserID: user.ID, Before: ptr(now.Add(-24 * time.Hour))}); err != nil {
			return err
		}
		st, err := q.TelegramRecoveryCodeStats(ctx, store.TelegramRecoveryCodeStatsParams{UserID: user.ID, Since: ptr(now.Add(-time.Hour))})
		if err != nil {
			return err
		}
		if limitErr = limits("phone", now, st.LastAt, st.InHour, st.FirstInHour); limitErr != nil {
			return nil
		}
		hash, err := p.Hasher.Hash(ctx, code)
		if err != nil {
			return err
		}
		codeID, err = q.TelegramInsertRecoveryCode(ctx, store.TelegramInsertRecoveryCodeParams{
			UserID: user.ID, CodeHash: hash, ExpiresAt: now.Add(CodeTTL), Now: &now,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("verifycode: recovery code: %w", err)
	}
	if user == nil {
		return nil // неизвестный или заблокированный номер — тот же ответ, ничего не отправляется
	}
	if limitErr != nil {
		return limitErr
	}
	sent, err := p.Gateway.SendCode(ctx, phone, code, CodeTTL)
	if err != nil || !sent {
		p.drop(ctx, codeID)
		if err != nil {
			return err
		}
		return &Error{Kind: KindNotSent, Field: "phone", Key: MsgNotSent}
	}
	err = db.InPlatformTx(ctx, p.Platform, func(q *store.Queries) error {
		_, err := activity.RecordPlatform(ctx, q, activity.PlatformEntry{Event: "user.password_code_sent",
			Key: "Код восстановления пароля отправлен в Telegram: :user", Params: map[string]string{"user": user.Name}, UserID: user.ID})
		return err
	})
	if err != nil {
		p.log().WarnContext(ctx, "password recovery: platform log", "err", err)
	}
	return nil
}

// drop снимает резерв кода, который не ушёл в Telegram (в старом коде запись создавалась после отправки).
func (p *PasswordRecovery) drop(ctx context.Context, id int64) {
	if id == 0 {
		return
	}
	if err := db.InPlatformTx(context.WithoutCancel(ctx), p.Platform, func(q *store.Queries) error {
		return q.TelegramDeleteRecoveryCode(ctx, id)
	}); err != nil {
		p.log().WarnContext(ctx, "password recovery: drop unsent code", "err", err)
	}
}

// Reset — PasswordRecovery::reset(): верный код задаёт новый пароль (newPassword уже проверен вызывающим: ≥ 6,
// подтверждение) и завершает все сеансы аккаунта; неверный засчитывает попытку. Ошибка для пользователя —
// *Error (поле code). Возвращает id пользователя.
func (p *PasswordRecovery) Reset(ctx context.Context, phone, code, newPassword string) (int64, error) {
	now := p.now()
	var userID int64
	var fail *Error
	err := db.InPlatformTx(ctx, p.Platform, func(q *store.Queries) error {
		userID, fail = 0, nil
		user, err := account(ctx, q, phone)
		if err != nil {
			return err
		}
		if user == nil {
			fail = &Error{Kind: KindCodeExpired, Field: "code", Key: MsgCodeWrong}
			return nil
		}
		rec, err := q.TelegramLatestRecoveryCode(ctx, store.TelegramLatestRecoveryCodeParams{UserID: user.ID, Now: now})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && rec.Attempts >= MaxAttempts) {
			fail = &Error{Kind: KindCodeExpired, Field: "code", Key: MsgCodeWrong}
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := p.Hasher.Verify(ctx, code, rec.CodeHash); err != nil {
			// $recovery->increment('attempts') — фиксируется (транзакция завершается успешно).
			fail = &Error{Kind: KindCodeWrong, Field: "code", Key: MsgCodeWrong}
			return q.TelegramRecoveryCodeAttempt(ctx, store.TelegramRecoveryCodeAttemptParams{ID: rec.ID, Now: &now})
		}
		hash, err := p.Hasher.Hash(ctx, newPassword)
		if err != nil {
			return err
		}
		if err := q.TelegramSetUserPassword(ctx, store.TelegramSetUserPasswordParams{PasswordHash: hash, Now: &now, ID: user.ID}); err != nil {
			return err
		}
		if err := q.TelegramDeleteUserRecoveryCodes(ctx, user.ID); err != nil {
			return err
		}
		if _, err := activity.RecordPlatform(ctx, q, activity.PlatformEntry{Event: "user.password_recovered",
			Key: "Пароль восстановлен через Telegram: :user", Params: map[string]string{"user": user.Name}, UserID: user.ID}); err != nil {
			return err
		}
		userID = user.ID
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("verifycode: reset password: %w", err)
	}
	if fail != nil {
		return 0, fail
	}
	if p.SignOut != nil {
		p.SignOut(context.WithoutCancel(ctx), userID)
	}
	return userID, nil
}
