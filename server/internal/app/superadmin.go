package app

import (
	"context"
	"log/slog"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// broadcastOnline — команда всем ПК на связи по всей платформе (суперадминка: TranslationsChanged после правки
// переводов, PD-12). В фоне, после коммита: ответ суперадмину не ждёт рассылку.
func broadcastOnline(sender *commands.Sender, store *presence.Store, log *slog.Logger) func(ctx context.Context, cmd *agentv1.Command) {
	return func(ctx context.Context, cmd *agentv1.Command) {
		if sender == nil || store == nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			pcs, err := store.OnlineComputers(ctx, time.Now())
			if err != nil {
				log.WarnContext(ctx, "broadcast: presence", "err", err)
				return
			}
			for _, pc := range pcs {
				if _, err := sender.Send(ctx, pc[0], pc[1], cmd); err != nil {
					log.WarnContext(ctx, "broadcast: send", "club_id", pc[0], "computer_id", pc[1], "err", err)
				}
			}
		}()
	}
}
