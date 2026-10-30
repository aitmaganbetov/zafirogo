package commands

import (
	"context"
	"errors"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// SessionNotifier — что billing-сервисы вызывают ПОСЛЕ коммита транзакции, изменившей сессию или баланс.
// Ошибка доставки не откатывает уже совершённую операцию: вызывающий её логирует (агент всё равно получит
// актуальную сессию в Welcome при следующем подключении).
//
// Места вызова (server/internal/billing/service; пакет здесь не меняется — связывает оркестратор):
//   - покупка тарифа/минут/пакета, продажа времени на кассе, пересадка (новый ПК) → SessionStarted;
//   - завершение сессии (гостем, сотрудником, пересадка — старый ПК) → SessionEnded;
//   - воркер истечения сессий (worker.ExpireSessions.OnExpired) → SessionEnded с reason EXPIRED;
//   - пополнение/списание/возврат/перевод (любая проводка ledger по member_balances) → BalanceChanged.
type SessionNotifier interface {
	SessionStarted(ctx context.Context, clubID, computerID int64, s *agentv1.ActiveSession, issuedBy int64) error
	SessionEnded(ctx context.Context, clubID, computerID int64, e *agentv1.EndSession, issuedBy int64) error
	BalanceChanged(ctx context.Context, clubID, userID int64, b *agentv1.Balance) error
}

// Notifier — SessionNotifier поверх Sender.
type Notifier struct {
	Sender   *Sender
	Presence *presence.Store
}

var _ SessionNotifier = (*Notifier)(nil)

// SessionStarted — StartSession (долговременная, до конца сессии).
func (n *Notifier) SessionStarted(ctx context.Context, clubID, computerID int64, s *agentv1.ActiveSession, issuedBy int64) error {
	_, err := n.Sender.Send(ctx, clubID, computerID, &agentv1.Command{
		IssuedByUserId: issuedBy,
		Kind:           &agentv1.Command_StartSession{StartSession: &agentv1.StartSession{Session: s}},
	})
	return err
}

// SessionEnded — EndSession (долговременная).
func (n *Notifier) SessionEnded(ctx context.Context, clubID, computerID int64, e *agentv1.EndSession, issuedBy int64) error {
	_, err := n.Sender.Send(ctx, clubID, computerID, &agentv1.Command{
		IssuedByUserId: issuedBy,
		Kind:           &agentv1.Command_EndSession{EndSession: e},
	})
	return err
}

// BalanceChanged — BalanceChanged на все ПК клуба, где сейчас вошёл этот гость (по Status.signed_in_user_id).
func (n *Notifier) BalanceChanged(ctx context.Context, clubID, userID int64, b *agentv1.Balance) error {
	pcs, err := n.Presence.List(ctx, clubID)
	if err != nil {
		return err
	}
	var errs []error
	for _, st := range pcs {
		if !st.Online || st.SignedInUserID != userID {
			continue
		}
		if _, err := n.Sender.Send(ctx, clubID, st.ComputerID, &agentv1.Command{
			Kind: &agentv1.Command_BalanceChanged{BalanceChanged: &agentv1.BalanceChanged{UserId: userID, Balance: b}},
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
