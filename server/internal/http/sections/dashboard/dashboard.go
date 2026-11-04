// Package dashboard — REST-раздел: админпанель (api/dashboard.yaml). Laravel: DashboardController.
package dashboard

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	dashboardapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/dashboard"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// access — маршрут dashboard закрыт ролью (club-role:owner,admin,manager,cashier,operator), а не правом
// RequireClubArea, поэтому проверка — в обработчике.
var access = httpx.Access{Public: []string{"GetDashboard"}}

// roles — кому сводка: персонал без кассира и оператора (DashboardController уводил их на кассу и список ПК).
var roles = []string{"owner", "admin", "manager"}

type handlers struct{ d *httpx.Deps }

var _ dashboardapi.StrictServerInterface = (*handlers)(nil)

// Section — админпанель. Регистрация — server/internal/http/sections.go.
func Section() httpx.Section { return httpx.Section{Name: "dashboard", Mount: mount} }

func mount(r chi.Router, d *httpx.Deps) {
	si := dashboardapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[dashboardapi.StrictMiddlewareFunc](access),
		dashboardapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	dashboardapi.HandlerWithOptions(si, dashboardapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// DayBounds — границы 7 локальных дней клуба (последний — сегодня) в UTC: 8 моментов (DashboardController:
// now(tz)->startOfDay(), subDays(6…0), каждый день — [начало, начало следующего)).
func DayBounds(now time.Time, loc *time.Location) []time.Time {
	l := now.In(loc)
	today := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	out := make([]time.Time, 8)
	for i := range 8 {
		out[i] = today.AddDate(0, 0, i-6).UTC()
	}
	return out
}

// GetDashboard — DashboardController::__invoke().
func (h *handlers) GetDashboard(ctx context.Context, _ dashboardapi.GetDashboardRequestObject) (dashboardapi.GetDashboardResponseObject, error) {
	p := httpx.Principal(ctx)
	switch {
	case p.UserID == 0:
		return nil, httpx.NewAPIError(401, "unauthenticated", "Unauthenticated.")
	case p.ClubID == 0 || p.Role == "":
		return nil, httpx.NewAPIError(403, permissions.CodeForbidden, permissions.MsgNoClubs)
	case !slices.Contains(roles, p.Role):
		return nil, httpx.NewAPIError(403, permissions.CodeForbidden, "")
	}
	clubID, now := p.ClubID, h.d.NowFunc()
	var (
		out      dashboardapi.Dashboard
		settings store.ClubMapSettingsRow
		cash     []store.DashboardCashRow
		counts   store.DashboardCountsRow
		pcs      []store.DashboardComputersRow
		recent   []store.DashboardRecentCashRow
		loc      *time.Location
	)
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if settings, err = q.ClubMapSettings(ctx, clubID); err != nil {
			return err
		}
		if loc, err = time.LoadLocation(settings.Timezone); err != nil {
			loc = time.UTC
		}
		bounds := DayBounds(now, loc)
		out.Days = make([]dashboardapi.DashboardDay, 7)
		for i := range 7 {
			out.Days[i].Label = bounds[i].In(loc).Format("02.01")
		}
		if cash, err = q.DashboardCash(ctx, store.DashboardCashParams{ClubID: clubID, Bounds: bounds}); err != nil {
			return err
		}
		if counts, err = q.DashboardCounts(ctx, clubID); err != nil {
			return err
		}
		var after int64
		for {
			page, err := q.DashboardComputers(ctx, store.DashboardComputersParams{ClubID: clubID, AfterID: after, PageSize: 1000})
			if err != nil {
				return err
			}
			pcs = append(pcs, page...)
			if len(page) < 1000 {
				break
			}
			after = page[len(page)-1].ID
		}
		var s struct {
			Finances struct {
				Currency any `json:"currency"`
			} `json:"finances"`
		}
		_ = json.Unmarshal(settings.Settings, &s)
		out.Currency = "KZT"
		if c, ok := s.Finances.Currency.(string); ok && c != "" {
			out.Currency = c
		}
		recent, err = q.DashboardRecentCash(ctx, clubID)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, r := range cash {
		i := int(r.Day) - 1
		if i < 0 || i >= 7 {
			continue
		}
		if r.Type == "income" {
			out.Days[i].IncomeTiyn = r.Total
		} else {
			out.Days[i].ExpensesTiyn = r.Total
		}
		if i == 6 {
			out.TodayTransactionCount += r.Cnt
			if r.Type == "income" {
				out.TodayIncomeTiyn = r.Total
			} else {
				out.TodayExpensesTiyn, out.TodayExpenseCount = r.Total, r.Cnt
			}
		}
	}
	out.ProductCount, out.LowStockCount, out.ZoneCount, out.UserCount = counts.Products, counts.LowStock, counts.Zones, counts.Members
	var online map[int64]bool
	if h.d.Presence != nil {
		states, err := h.d.Presence.List(ctx, clubID)
		if err != nil {
			h.d.Log().WarnContext(ctx, "dashboard: presence", "err", err)
		}
		online = map[int64]bool{}
		for id := range clubmap.PresenceByID(states) {
			online[id] = true
		}
	}
	for _, pc := range pcs {
		out.ComputerCount++
		// DashboardController не отмечает bookedNow (его ставит только карта): «забронировано» — ручной флаг брони.
		switch (clubmap.Computer{ID: pc.ID, Status: pc.Status, IsReserved: pc.IsReserved, LastSeenAt: pc.LastSeenAt, Online: online[pc.ID]}).CurrentStatus() {
		case "online":
			out.OnlineComputerCount++
		case "reserved":
			out.ReservedComputerCount++
		}
	}
	out.RecentTransactions = make([]dashboardapi.DashboardTransaction, 0, len(recent))
	for _, r := range recent {
		out.RecentTransactions = append(out.RecentTransactions, dashboardapi.DashboardTransaction{
			Id: r.ID, Type: dashboardapi.DashboardTransactionType(r.Type), AmountTiyn: r.Amount, Description: r.Description, CreatedAt: r.CreatedAt,
		})
	}
	return dashboardapi.GetDashboard200JSONResponse(out), nil
}
