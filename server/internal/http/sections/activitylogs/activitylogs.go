// Package activitylogs — REST-раздел: журнал действий клуба (api/activitylogs.yaml). Конвенции — api/README.md.
//
// Laravel: app/Http/Controllers/ActivityLogController.php index(), app/Http/Requests/FilterActivityLogsRequest.php,
// app/Models/ActivityLog.php (EVENT_LABELS, detailText()). Описание — на языке клуба: новые записи хранят ключ и
// параметры (details._t, решение W-3), старые — готовый текст.
package activitylogs

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	activitylogsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/activitylogs"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var access = httpx.Access{Routes: map[string]string{"ListActivityLogs": "activity-logs.index"}}

type handlers struct {
	d *httpx.Deps
}

var _ activitylogsapi.StrictServerInterface = (*handlers)(nil)

// Section — логи действий. Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "activitylogs", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := activitylogsapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[activitylogsapi.StrictMiddlewareFunc](access),
		activitylogsapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	activitylogsapi.HandlerWithOptions(si, activitylogsapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// perPage — paginate(50).
const perPage = 50

type cursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

// Границы периода без фильтра (диапазон, а не функции над created_at).
var (
	minTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	maxTime = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
)

// parseDay — правило 'date' + Carbon::parse($v, $tz)->startOfDay() для дат формы (input type=date).
func parseDay(info club.Info, s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04", "2006-01-02 15:04:05", "02.01.2006"} {
		if t, err := time.ParseInLocation(layout, strings.TrimSpace(s), info.Loc); err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, info.Loc), true
		}
	}
	return time.Time{}, false
}

func strp(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// ListActivityLogs — index(): фильтры, 50 записей, новые сверху, пользователи журнала, ярлыки событий.
func (h *handlers) ListActivityLogs(ctx context.Context, req activitylogsapi.ListActivityLogsRequestObject) (activitylogsapi.ListActivityLogsResponseObject, error) {
	p := httpx.Principal(ctx)
	cur := cursor{At: maxTime, ID: math.MaxInt64}
	if err := httpx.DecodeCursor(req.Params.Cursor, &cur); err != nil {
		return nil, err
	}
	out := activitylogsapi.ActivityLogPage{Items: []activitylogsapi.ActivityLogEntry{}, Users: []activitylogsapi.ActivityLogUser{},
		Events: make([]activitylogsapi.ActivityLogEvent, 0, len(activity.EventLabels))}
	for _, e := range activity.EventLabels {
		out.Events = append(out.Events, activitylogsapi.ActivityLogEvent{Event: e.Event, Label: h.d.T(ctx, e.Label, nil)})
	}
	from, to, event, search := strp(req.Params.From), strp(req.Params.To), strp(req.Params.Event), strp(req.Params.Search)
	userRaw := strp(req.Params.UserId)
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		// FilterActivityLogsRequest — все ошибки сразу, в порядке правил.
		var fe httpx.FormErrors
		fromAt, toAt := minTime, maxTime
		if from != "" {
			if d, ok := parseDay(info, from); ok {
				fromAt = d
			} else {
				fe.Add("from", httpx.RuleMessage("from", "date", ""))
			}
		}
		if to != "" {
			if d, ok := parseDay(info, to); ok {
				toAt = d.AddDate(0, 0, 1)
			} else {
				fe.Add("to", httpx.RuleMessage("to", "date", ""))
			}
		}
		if event != "" {
			if _, ok := activity.Label(event); !ok {
				fe.Add("event", httpx.RuleMessage("event", "in", ""))
			}
		}
		var userID int64
		if userRaw != "" {
			id, err := strconv.ParseInt(userRaw, 10, 64)
			if err != nil {
				fe.Add("user_id", httpx.RuleMessage("user_id", "integer", ""))
			} else if ok, err := q.ClubMemberExists(ctx, store.ClubMemberExistsParams{ClubID: p.ClubID, UserID: id}); err != nil {
				return err
			} else if !ok {
				fe.Add("user_id", httpx.RuleMessage("user_id", "in", ""))
			} else {
				userID = id
			}
		}
		if utf8.RuneCountInString(search) > 120 {
			fe.Add("search", httpx.RuleMessage("search", "max_string", "120"))
		}
		if err := fe.Err(); err != nil {
			return err
		}
		like := httpx.LikePattern(&search)
		if out.Total, err = q.CountActivityLogPage(ctx, store.CountActivityLogPageParams{
			ClubID: p.ClubID, FromAt: fromAt, ToAt: toAt, Event: event, UserID: userID, Search: like,
		}); err != nil {
			return err
		}
		rows, err := q.ListActivityLogPage(ctx, store.ListActivityLogPageParams{
			ClubID: p.ClubID, FromAt: fromAt, ToAt: toAt, BeforeCreatedAt: cur.At, BeforeID: cur.ID,
			Event: event, UserID: userID, Search: like, PageSize: perPage,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			e := activitylogsapi.ActivityLogEntry{
				Id: r.ID, Event: r.Event, Description: r.Description, UserName: r.UserName, CreatedAt: r.CreatedAt,
				Details: h.details(ctx, r.Details),
			}
			if r.IpAddress != "" {
				ip := r.IpAddress
				e.IpAddress = &ip
			}
			if t := activity.TextOf(r.Details); t != nil {
				e.Description = h.d.T(ctx, t.Key, t.Params)
			}
			out.Items = append(out.Items, e)
		}
		if len(rows) == perPage {
			last := rows[len(rows)-1]
			out.NextCursor = httpx.EncodeCursor(cursor{At: last.CreatedAt, ID: last.ID})
		}
		users, err := q.ListActivityLogUsers(ctx, p.ClubID)
		for _, u := range users {
			phone := u.Phone
			out.Users = append(out.Users, activitylogsapi.ActivityLogUser{Id: u.ID, Name: u.Name, Phone: &phone})
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return activitylogsapi.ListActivityLogs200JSONResponse(out), nil
}

// details — пары «ключ: значение» details в порядке ключей JSON (служебный _t не показывается).
func (h *handlers) details(ctx context.Context, raw json.RawMessage) []activitylogsapi.ActivityLogDetail {
	out := []activitylogsapi.ActivityLogDetail{}
	if len(raw) == 0 {
		return out
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return out
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		key, _ := tok.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return out
		}
		if key == activity.TextKey {
			continue
		}
		out = append(out, activitylogsapi.ActivityLogDetail{Key: key, Value: h.detailText(ctx, v)})
	}
	return out
}

// detailText — ActivityLog::detailText(): списки товаров «Cola × 2, Чипсы × 1», прочие списки — через запятую,
// логические — «да»/«нет», null — «—».
func (h *handlers) detailText(ctx context.Context, v any) string {
	switch x := v.(type) {
	case nil:
		return "—"
	case bool:
		if x {
			return h.d.T(ctx, "да", nil)
		}
		return h.d.T(ctx, "нет", nil)
	case string:
		return x
	case json.Number:
		return phpNumber(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			switch it := item.(type) {
			case map[string]any:
				name, okN := it["name"]
				qty, okQ := it["quantity"]
				if okN && okQ && name != nil && qty != nil {
					parts = append(parts, scalar(name)+" × "+scalar(qty))
				} else {
					b, _ := json.Marshal(it)
					parts = append(parts, string(b))
				}
			case []any:
				b, _ := json.Marshal(it)
				parts = append(parts, string(b))
			default:
				parts = append(parts, h.detailText(ctx, it))
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			parts = append(parts, h.detailText(ctx, item))
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

// scalar — значение как строка PHP ($item['name'].' × '.$item['quantity']).
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return phpNumber(x)
	case bool:
		if x {
			return "1"
		}
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// phpNumber — (string) числа из json_decode: целое — как есть, дробное — кратчайшая запись float (600.0 → "600").
func phpNumber(n json.Number) string {
	if i, err := n.Int64(); err == nil {
		return strconv.FormatInt(i, 10)
	}
	f, err := n.Float64()
	if err != nil {
		return n.String()
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
