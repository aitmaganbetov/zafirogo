package superadmin

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// logsPage — paginate(50) LogController.
const logsPage = 50

// logRow — строка superadmin/logs/rows.blade.php: время UTC, клуб, кто (журнал платформы без пользователя —
// «Система»), подпись события, описание (на языке запроса по ключу details._t, W-3), IP.
type logRow struct {
	id          int64
	clubID      *int64
	clubName    string
	userID      *int64
	userName    string
	event       string
	description string
	details     json.RawMessage
	ip          string
	at          *time.Time
}

func (h *handlers) rows(ctx context.Context, rs []logRow, platform bool) []superadminapi.LogRow {
	out := make([]superadminapi.LogRow, 0, len(rs))
	locale := h.d.Locale(ctx)
	for _, r := range rs {
		row := superadminapi.LogRow{Id: r.id, ClubId: r.clubID, ClubName: r.clubName, UserName: r.userName,
			System: platform && r.userID == nil, Event: r.event, Description: r.description, IpAddress: r.ip}
		if r.clubID == nil {
			row.ClubName = ""
		}
		if r.at != nil {
			row.AtUtc = r.at.UTC().Format("02.01.2006 15:04")
		}
		label, ok := activity.Label(r.event)
		if platform {
			label, ok = activity.PlatformLabel(r.event)
		}
		if ok {
			row.EventLabel = &label
		}
		if t := activity.TextOf(r.details); t != nil && h.d.I18n != nil {
			row.Description = h.d.I18n.Translate(ctx, locale, t.Key, t.Params)
		}
		out = append(out, row)
	}
	return out
}

func platformRows(rs []store.SuperadminPlatformLogsRow) []logRow {
	out := make([]logRow, 0, len(rs))
	for _, r := range rs {
		out = append(out, logRow{id: r.ID, clubID: r.ClubID, clubName: r.ClubName, userID: r.UserID, userName: r.UserName,
			event: r.Event, description: r.Description, details: r.Details, ip: r.IpAddress, at: r.CreatedAt})
	}
	return out
}

func activityRows(rs []store.SuperadminActivityLogsRow) []logRow {
	out := make([]logRow, 0, len(rs))
	for _, r := range rs {
		id, uid, at := r.ClubID, r.UserID, r.CreatedAt
		out = append(out, logRow{id: r.ID, clubID: &id, clubName: r.ClubName, userID: &uid, userName: r.UserName,
			event: r.Event, description: r.Description, details: r.Details, ip: r.IpAddress, at: &at})
	}
	return out
}

func clubActivityRows(rs []store.SuperadminClubActivityRow) []logRow {
	out := make([]logRow, 0, len(rs))
	for _, r := range rs {
		id, uid, at := r.ClubID, r.UserID, r.CreatedAt
		out = append(out, logRow{id: r.ID, clubID: &id, clubName: r.ClubName, userID: &uid, userName: r.UserName,
			event: r.Event, description: r.Description, details: r.Details, ip: r.IpAddress, at: &at})
	}
	return out
}

// SaListLogs — LogController: вкладки «Действия суперадминов» (platform_logs) и «Журналы клубов» (activity_logs).
func (h *handlers) SaListLogs(ctx context.Context, req superadminapi.SaListLogsRequestObject) (superadminapi.SaListLogsResponseObject, error) {
	tab := "platform"
	if req.Params.Tab != nil {
		switch *req.Params.Tab {
		case "platform", "clubs":
			tab = string(*req.Params.Tab)
		default:
			return nil, httpx.RuleError("tab", "in")
		}
	}
	event := ""
	if req.Params.Event != nil {
		event = *req.Params.Event
		if len([]rune(event)) > 64 {
			return nil, httpx.RuleErrorWith("event", "max_string", "64")
		}
	}
	pg := page(req.Params.Page)
	out := superadminapi.LogList{Tab: tab, Page: pg, PerPage: logsPage, Events: []superadminapi.EventOption{}}
	err := h.tx(ctx, func(q *store.Queries) error {
		var club *int64
		if req.Params.Club != nil {
			name, err := q.SuperadminClubExists(ctx, *req.Params.Club)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.RuleError("club", "exists")
			}
			if err != nil {
				return err
			}
			club = req.Params.Club
			out.Club = &struct {
				Id   int64  `json:"id"`
				Name string `json:"name"`
			}{Id: *club, Name: name}
		}
		offset := int64(pg-1) * logsPage
		if tab == "platform" {
			for _, l := range activity.PlatformEventLabels {
				out.Events = append(out.Events, superadminapi.EventOption{Event: l.Event, Label: l.Label})
			}
			total, err := q.SuperadminCountPlatformLogs(ctx, store.SuperadminCountPlatformLogsParams{ClubID: club, Event: event})
			if err != nil {
				return err
			}
			rs, err := q.SuperadminPlatformLogs(ctx, store.SuperadminPlatformLogsParams{ClubID: club, Event: event, PageSize: logsPage, PageOffset: offset})
			out.Total, out.Items = total, h.rows(ctx, platformRows(rs), true)
			return err
		}
		for _, l := range activity.EventLabels {
			out.Events = append(out.Events, superadminapi.EventOption{Event: l.Event, Label: l.Label})
		}
		total, err := q.SuperadminCountActivityLogs(ctx, store.SuperadminCountActivityLogsParams{ClubID: club, Event: event})
		if err != nil {
			return err
		}
		rs, err := q.SuperadminActivityLogs(ctx, store.SuperadminActivityLogsParams{ClubID: club, Event: event, PageSize: logsPage, PageOffset: offset})
		out.Total, out.Items = total, h.rows(ctx, activityRows(rs), false)
		return err
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaListLogs200JSONResponse(out), nil
}
