package clubmap

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/realtime"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// LoadEvent — тип живого обновления загрузки и здоровья ПК (docs/realtime.md §4, право club_map.view).
const LoadEvent = "computer.load"

// LoadData — data события computer.load: то же, что поля load/problems/rtt_ms у ComputerState (api/clubmap.yaml).
type LoadData struct {
	ComputerID int64     `json:"computer_id"`
	Load       *LoadView `json:"load,omitempty"`
	Problems   []string  `json:"problems"`
	RTTMillis  uint32    `json:"rtt_ms,omitempty"`
}

// LoadView — загрузка ПК для карты (сотые доли процента, секунды бездействия).
type LoadView struct {
	CPUBasisPoints uint32 `json:"cpu_bp"`
	RAMBasisPoints uint32 `json:"ram_bp"`
	IdleSeconds    uint32 `json:"idle_seconds"`
}

// LoadDataOf — загрузка и проблемы ПК из снимка присутствия (событие и карта клуба).
func LoadDataOf(st presence.State) LoadData {
	d := LoadData{ComputerID: st.ComputerID, Problems: st.Problems, RTTMillis: st.RTTMillis}
	if d.Problems == nil {
		d.Problems = []string{}
	}
	if st.HasLoad {
		d.Load = &LoadView{CPUBasisPoints: st.Load.CPUBasisPoints, RAMBasisPoints: st.Load.RAMBasisPoints, IdleSeconds: st.Load.IdleSeconds}
	}
	return d
}

// LiveEvents — живые обновления загрузки и здоровья ПК в WebSocket админки (stream.LiveListener): публикация в core
// NATS (realtime.LiveSubject), без outbox и Postgres — частоту уже ограничил стрим (не чаще раза в
// agent.health.live_min_gap на ПК и только при заметном изменении).
type LiveEvents struct {
	NATS *nats.Conn
	Now  func() time.Time
	Log  *slog.Logger
}

// LiveChanged — stream.LiveListener.
func (l *LiveEvents) LiveChanged(st presence.State) {
	if l == nil || l.NATS == nil || st.ClubID <= 0 {
		return
	}
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	data, err := json.Marshal(LoadDataOf(st))
	if err == nil {
		var b []byte
		b, err = json.Marshal(outbox.Envelope{ID: uuid.NewString(), Type: LoadEvent, ClubID: st.ClubID, CreatedAt: now().UTC(), Data: data})
		if err == nil {
			err = l.NATS.Publish(realtime.LiveSubject(st.ClubID, LoadEvent), b)
		}
	}
	if err != nil && l.Log != nil {
		l.Log.Warn("computer.load event", "club_id", st.ClubID, "err", err)
	}
}

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
