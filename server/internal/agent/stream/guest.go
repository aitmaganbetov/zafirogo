package stream

import (
	"context"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// GuestVerifier — кто вошёл на ПК по записи самого сервера (живой токен гостя этого ПК, guestapi.Tokens).
// 0 — никто.
//
// claimed — гость, которого заявил ПК в Status; inSession — ПК сообщает идущую сессию. Если заявление совпало с
// записью и сессия идёт, реализация продлевает запись (токен гостя): гость, сидящий дольше срока токена, не
// выпадает из присутствия (docs/agent-protocol.md §4).
type GuestVerifier interface {
	ComputerGuest(ctx context.Context, clubID, computerID, claimed int64, inSession bool) (userID int64, err error)
}

// verifyGuest сверяет Status.signed_in_user_id с записью сервера (аудит безопасности A-01). Поле присылает сам ПК,
// а по нему выбираются ПК для ответа сотрудника в чате (chat.Deliver) и для баланса (commands.Notifier.BalanceChanged,
// автобиллинг): ПК с действующим сертификатом, заявив чужой user_id, получал чужие сообщения и баланс. Источник
// истины — вход гостя, подтверждённый сервером: токен гостя выдаётся этому ПК при входе (SignIn, QR) и отзывается
// при выходе, смене пароля и отзыве сертификата. Заявление, не совпавшее с токеном, обнуляется — в снимок
// присутствия и индекс «гость → ПК» оно не попадает; случай считается (GuestRejected) и пишется в журнал (не чаще
// раза на поток для одного заявленного гостя).
//
// Сбой проверки (Valkey): заявление, равное уже подтверждённому в этом потоке гостю, остаётся — иначе короткий сбой
// Valkey на всех ПК клуба давал 42 → 0 → 42 и всплеск записей outbox; новому заявлению при сбое не верим (следующий
// Status проверится заново).
func (c *conn) verifyGuest(ctx context.Context, s *agentv1.Status) {
	claimed := s.GetSignedInUserId()
	if claimed == 0 {
		return
	}
	var confirmed int64
	var err error
	if v := c.svc.o.Guests; v != nil {
		confirmed, err = v.ComputerGuest(ctx, c.id.ClubID, c.id.ComputerID, claimed,
			s.GetState() == agentv1.ComputerState_COMPUTER_STATE_IN_SESSION)
	}
	if err == nil && confirmed == claimed {
		return
	}
	if err != nil {
		if !c.guestErrWarned {
			c.guestErrWarned = true
			c.svc.o.Log.Warn("agent status: guest not verified", "club_id", c.id.ClubID, "computer_id", c.id.ComputerID, "err", err)
		}
		c.mu.Lock()
		prev := c.st.SignedInUserID
		c.mu.Unlock()
		if prev == claimed {
			return
		}
	}
	s.SignedInUserId = 0
	c.svc.guestRejected.Add(1)
	if err == nil && c.guestWarned != claimed {
		c.guestWarned = claimed
		c.svc.o.Log.Warn("agent status: signed_in_user_id does not match the guest token of this computer",
			"club_id", c.id.ClubID, "computer_id", c.id.ComputerID, "claimed_user_id", claimed, "confirmed_user_id", confirmed)
	}
}
