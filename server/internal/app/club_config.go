package app

import (
	"context"
	"log/slog"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/guestapi"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// clubConfigChanged — команда ConfigChanged с актуальным ClubConfig всем ПК клуба в сети (настройки клуба,
// логотип, картинка экрана входа, язык, пояс изменены в панели; в старом агент узнавал их со следующим heartbeat).
// ПК не в сети получат конфигурацию в Welcome при подключении. Рассылка — в фоне (ответ панели не ждёт её);
// конфигурация одна для всего клуба — собирается один раз. Ошибки — только в журнал.
func clubConfigChanged(hooks *guestapi.Hooks, sender *commands.Sender, store *presence.Store, log *slog.Logger) func(ctx context.Context, clubID int64) {
	return func(ctx context.Context, clubID int64) {
		if hooks == nil || sender == nil || store == nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			states, err := store.List(ctx, clubID)
			if err != nil {
				log.WarnContext(ctx, "club config: presence", "club_id", clubID, "err", err)
				return
			}
			var cmd *agentv1.Command
			for _, st := range states {
				if !st.Online {
					continue
				}
				if cmd == nil {
					if cmd, err = hooks.ConfigChanged(ctx, identity.Identity{ClubID: clubID, ComputerID: st.ComputerID}); err != nil {
						log.WarnContext(ctx, "club config: build", "club_id", clubID, "err", err)
						return
					}
				}
				if _, err := sender.Send(ctx, clubID, st.ComputerID, cmd); err != nil {
					log.WarnContext(ctx, "club config: send", "club_id", clubID, "computer_id", st.ComputerID, "err", err)
				}
			}
		}()
	}
}
