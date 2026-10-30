package clubmap

import (
	"context"
	"log/slog"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// PresenceEvents — события computer.state для WebSocket админки из присутствия агента (вместо опроса
// GET /club-map/state раз в 15 с, docs/realtime.md): подключение, смена состояния агента/сессии/гостя,
// закрытие стрима (stream.PresenceListener) и офлайн после ожидания переподключения (presence.Sweeper.OnOffline).
// Пишется только при изменении состояния (стрим вызывает слушателя лишь для значимых изменений), не на Status.
type PresenceEvents struct {
	Pool db.Beginner
	Log  *slog.Logger
}

// PresenceChanged — stream.PresenceListener.
func (p *PresenceEvents) PresenceChanged(ctx context.Context, st presence.State) {
	conn := "offline"
	if st.Online {
		conn = "online"
	}
	data := map[string]any{"computer_id": st.ComputerID, "connection": conn, "agent_state": st.AgentState}
	if st.AgentVersion != "" {
		data["agent_version"] = st.AgentVersion
	}
	if !st.LastSeenAt.IsZero() {
		data["last_seen"] = st.LastSeenAt.UTC().Format(time.RFC3339)
	}
	p.add(ctx, st.ClubID, data)
}

// Offline — presence.Sweeper.OnOffline: ПК записан офлайн.
func (p *PresenceEvents) Offline(ctx context.Context, clubID, computerID int64) {
	p.add(ctx, clubID, map[string]any{"computer_id": computerID, "connection": "offline", "status": "offline"})
}

func (p *PresenceEvents) add(ctx context.Context, clubID int64, data map[string]any) {
	if p == nil || p.Pool == nil || clubID <= 0 {
		return
	}
	err := db.InTenantTx(ctx, p.Pool, clubID, func(q *store.Queries) error {
		_, err := outbox.Add(ctx, q, outbox.Event{ClubID: clubID, Type: "computer.state", Data: data})
		return err
	})
	if err != nil && p.Log != nil {
		p.Log.WarnContext(ctx, "computer.state event", "club_id", clubID, "err", err)
	}
}
