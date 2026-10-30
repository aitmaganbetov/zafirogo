package clubmap

// Карта клуба. Laravel: ClubZoneController::state().

import (
	"context"
	"strings"

	clubmapapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// GetClubMapState — ClubZoneController::state(): ПК из БД + присутствие агента.
func (h *handlers) GetClubMapState(ctx context.Context, _ clubmapapi.GetClubMapStateRequestObject) (clubmapapi.GetClubMapStateResponseObject, error) {
	clubID := httpx.ClubID(ctx)
	out := clubmapapi.ClubMapState{Computers: []clubmapapi.ComputerState{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var after int64
		for {
			rows, err := q.AdminListComputerStates(ctx, store.AdminListComputerStatesParams{ClubID: clubID, AfterID: after, PageSize: 500})
			if err != nil {
				return err
			}
			for _, r := range rows {
				out.Computers = append(out.Computers, computerStateView(r))
			}
			if len(rows) < 500 {
				return nil
			}
			after = rows[len(rows)-1].ID
		}
	})
	if err != nil {
		return nil, err
	}
	if h.d.Presence != nil {
		states, err := h.d.Presence.List(ctx, clubID)
		if err != nil {
			// Карта без присутствия лучше, чем ошибка: все ПК офлайн.
			h.d.Logger.WarnContext(ctx, "club map: presence", "err", err)
		}
		byID := make(map[int64]int, len(out.Computers))
		for i, c := range out.Computers {
			byID[c.Id] = i
		}
		for _, st := range states {
			i, ok := byID[st.ComputerID]
			if !ok || !st.Online {
				continue
			}
			c := &out.Computers[i]
			c.Connection = clubmapapi.Online
			if c.Status == string(clubmapapi.Offline) {
				c.Status = string(clubmapapi.Online)
			}
			if st.AgentState != "" {
				c.AgentState = st.AgentState
			}
			if st.AgentVersion != "" {
				c.AgentVersion = st.AgentVersion
			}
			if !st.LastSeenAt.IsZero() {
				ls := st.LastSeenAt
				c.LastSeen = &ls
			}
		}
	}
	return clubmapapi.GetClubMapState200JSONResponse(out), nil
}

// computerStateView — элемент computerStates старого кода (без классов подсветки — их строит фронт).
func computerStateView(r store.AdminListComputerStatesRow) clubmapapi.ComputerState {
	name := ""
	switch {
	case r.DisplayName != nil && *r.DisplayName != "":
		name = *r.DisplayName
	case r.DeviceName != nil && *r.DeviceName != "":
		name = *r.DeviceName
	default:
		name = "PC-" + pad2(r.Number)
	}
	status := "offline"
	if r.Status == "restarting" {
		status = "maintenance"
	}
	v := clubmapapi.ComputerState{
		Id: r.ID, ZoneId: r.ClubZoneID, Number: r.Number, Name: name, Status: status, Connection: clubmapapi.Offline,
		IsReserved: r.IsReserved, HasSession: r.CurrentSessionID != nil, SessionId: r.CurrentSessionID,
		SessionEndsAt: r.SessionEndsAt, SessionGuest: r.SessionGuest, SessionUserId: r.CurrentSessionUserID,
		LastSeen: r.LastSeenAt, StaffUntil: r.StaffUntil, StaffName: r.StaffName,
	}
	if r.AgentState != nil {
		v.AgentState = *r.AgentState
	}
	if r.AgentVersion != nil {
		v.AgentVersion = *r.AgentVersion
	}
	if v.HasSession {
		user := msgGuestNoAccount
		if !r.SessionGuest {
			parts := []string{}
			if r.SessionUserName != nil && *r.SessionUserName != "" {
				parts = append(parts, *r.SessionUserName)
			}
			if r.SessionUserPhone != nil && *r.SessionUserPhone != "" {
				parts = append(parts, *r.SessionUserPhone)
			}
			user = strings.Join(parts, " · ")
		}
		v.SessionUser = &user
	}
	return v
}

// pad2 — sprintf('%02d', $n).
func pad2(n int64) string {
	s := httpx.Itoa(n)
	if n >= 0 && n < 10 {
		return "0" + s
	}
	return s
}

// msgGuestNoAccount — ClubZoneController::computerStates(): подпись сессии без аккаунта.
const msgGuestNoAccount = "Гость без аккаунта"
