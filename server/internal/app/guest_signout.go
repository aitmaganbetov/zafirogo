package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/valkey-io/valkey-go"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/guestapi"
)

// guestSignOut — отзыв всех токенов гостя (ПК) и команда SignOutGuest на ПК клуба, где они были: сброс пароля
// клиента в панели (UserController::resetPassword(): $user->tokens()->delete()). Ошибки — только в журнал:
// пароль уже сменён, токены истекут сами (TokenTTL).
func guestSignOut(c valkey.Client, ttl time.Duration, sender *commands.Sender, log *slog.Logger) func(ctx context.Context, clubID, userID int64, reason string) {
	tokens := guestapi.NewTokens(c, guestapi.DefaultKeyPrefix, ttl, time.Now)
	return func(ctx context.Context, clubID, userID int64, reason string) {
		pcs, err := tokens.RevokeUser(ctx, userID, guestapi.Guest{})
		if err != nil {
			log.WarnContext(ctx, "guest tokens not revoked after password reset", "user_id", userID, "err", err)
		}
		if sender == nil {
			return
		}
		for _, pc := range pcs {
			if _, err := sender.Send(ctx, clubID, pc, &agentv1.Command{
				Kind: &agentv1.Command_SignOutGuest{SignOutGuest: &agentv1.SignOutGuest{UserId: userID, Reason: reason}},
			}); err != nil {
				log.WarnContext(ctx, "sign out guest command not sent", "computer_id", pc, "err", err)
			}
		}
	}
}
