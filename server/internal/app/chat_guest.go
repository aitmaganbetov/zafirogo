package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/chat"
)

// chatToGuest — ответ сотрудника в чате на ПК клуба, где вошёл гость (команда ChatMessageReceived вместо опроса
// GET /api/guest/chat агентом). Ошибки — только в журнал: сообщение уже сохранено, агент получит его списком.
func chatToGuest(sender *commands.Sender, store *presence.Store, log *slog.Logger) func(ctx context.Context, clubID, userID, messageID int64, author, body string, at time.Time) {
	return func(ctx context.Context, clubID, userID, messageID int64, author, body string, at time.Time) {
		if sender == nil || store == nil {
			return
		}
		chat.Deliver(ctx, log, sender, store, clubID, userID, chat.AgentMessage(messageID, author, body, at))
	}
}
