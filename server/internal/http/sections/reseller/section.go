// Package reseller — REST-раздел: кабинет реселлера (api/reseller.yaml; тема Д, docs/upstream-2026-11.md) — его код
// и условия, клубы, которые ввели код, начисления с их оплаченных счетов и выплаты.
//
// Laravel: app/Http/Controllers/ResellerController.php, resources/views/reseller/dashboard.blade.php,
// routes/web.php (reseller.dashboard — middleware auth; не реселлер — 403).
//
// Доступ — вошедший аккаунт (RequireAuth) с записью реселлера; клуб сессии не нужен и не используется: всё
// выбирается по id записи реселлера этого аккаунта, поэтому чужих клубов и сумм кабинет показать не может. Хост
// кабинета (reseller.domain) проверяет общий middleware роутера (http/reseller_host.go). Данные — пул
// zafiro_platform (реселлеры и счета всех клубов — данные платформы), только чтение.
package reseller

import (
	"context"
	"errors"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/catalog"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	resellerapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/reseller"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/reseller"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// access — операция «публичная» для проверки прав клуба: доступ решают RequireAuth и запись реселлера аккаунта.
var access = httpx.Access{Public: []string{"GetResellerDashboard"}}

const dateFormat = "02.01.2006" // format('d.m.Y')

type handlers struct {
	d *httpx.Deps
}

var _ resellerapi.StrictServerInterface = (*handlers)(nil)

// Section — кабинет реселлера. Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "reseller", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	h := &handlers{d: d}
	si := resellerapi.NewStrictHandlerWithOptions(h,
		httpx.Middlewares[resellerapi.StrictMiddlewareFunc](access),
		resellerapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	resellerapi.HandlerWithOptions(si, resellerapi.ChiServerOptions{
		BaseRouter: r, ErrorHandlerFunc: d.ParamError,
		Middlewares: []resellerapi.MiddlewareFunc{permissions.RequireAuth},
	})
}

func pageOf(p *int) int {
	if p != nil && *p > 1 {
		return *p
	}
	return 1
}

// GetResellerDashboard — ResellerController::dashboard(). Даты — по UTC (кабинет вне клуба: часовой пояс приложения).
func (h *handlers) GetResellerDashboard(ctx context.Context, req resellerapi.GetResellerDashboardRequestObject) (resellerapi.GetResellerDashboardResponseObject, error) {
	if h.d.PlatformPool == nil {
		return nil, httpx.ErrNotConfigured
	}
	clubsPage, earningsPage := pageOf(req.Params.ClubsPage), pageOf(req.Params.EarningsPage)
	var c reseller.Cabinet
	err := db.InPlatformTx(ctx, h.d.PlatformPool, func(q *store.Queries) error {
		var err error
		c, err = reseller.Dashboard(ctx, q, httpx.Principal(ctx).UserID, clubsPage, earningsPage)
		return err
	})
	if errors.Is(err, reseller.ErrNotReseller) {
		// abort_if($reseller === null, 403, __('Этот аккаунт не реселлер Zafiro.'))
		return nil, httpx.ErrorMsg(403, permissions.CodeForbidden, reseller.MsgNotReseller, nil)
	}
	if err != nil {
		return nil, err
	}
	out := resellerapi.ResellerCabinet{
		Code: c.Code, CommissionBp: c.CommissionBP, DiscountPercent: c.DiscountPercent, IsActive: c.Active,
		EarnedTiyn: c.Earned, PaidOutTiyn: c.PaidOut, BalanceTiyn: c.Balance,
		Clubs:    resellerapi.ResellerClubPage{Items: []resellerapi.ResellerClubRow{}, Page: clubsPage, PerPage: reseller.ClubsPage, Total: c.ClubsTotal},
		Earnings: resellerapi.ResellerEarningPage{Items: []resellerapi.ResellerEarning{}, Page: earningsPage, PerPage: reseller.EarningsPage, Total: c.EarningsTotal},
		Payouts:  []resellerapi.ResellerPayout{},
	}
	for _, club := range c.Clubs {
		out.Clubs.Items = append(out.Clubs.Items, clubRow(club))
	}
	for _, e := range c.Earnings {
		row := resellerapi.ResellerEarning{ClubName: e.ClubName, Months: int(e.Months), AmountTiyn: e.Amount}
		if e.ResellerAmount != nil {
			row.ResellerAmountTiyn = *e.ResellerAmount
		}
		if e.ResellerBp != nil {
			row.ResellerBp = int(*e.ResellerBp)
		}
		if e.PaidAt != nil {
			row.Paid = httpx.Ptr(e.PaidAt.UTC().Format(dateFormat))
		}
		out.Earnings.Items = append(out.Earnings.Items, row)
	}
	for _, p := range c.Payouts {
		row := resellerapi.ResellerPayout{AmountTiyn: p.Amount, Note: p.Note}
		if p.CreatedAt != nil {
			row.Date = p.CreatedAt.UTC().Format(dateFormat)
		}
		out.Payouts = append(out.Payouts, row)
	}
	return resellerapi.GetResellerDashboard200JSONResponse(out), nil
}

// clubRow — строка «N клубов»: подписка — как в reseller/dashboard.blade.php:
//
//	@if($subscription?->status === 'active' && $subscription->current_period_ends_at) :plan до :date
//	@elseif($subscription?->status === 'trialing' && $subscription->trial_ends_at) Пробный период до :date
//	@else Не оплачена
func clubRow(c store.ResellerCabinetClubsRow) resellerapi.ResellerClubRow {
	row := resellerapi.ResellerClubRow{Name: c.Name, Subscription: resellerapi.Unpaid}
	// Cities::name($club->city): города нет в списке — строки нет.
	if c.City != nil {
		if city, ok := catalog.CityByKey(*c.City); ok {
			row.City = &city.Name
		}
	}
	status := ""
	if c.SubscriptionStatus != nil {
		status = *c.SubscriptionStatus
	}
	switch {
	case status == "active" && c.CurrentPeriodEndsAt != nil:
		row.Subscription = resellerapi.Active
		row.SubscriptionUntil = httpx.Ptr(c.CurrentPeriodEndsAt.UTC().Format(dateFormat))
		row.PlanName = c.PlanName
	case status == "trialing" && c.TrialEndsAt != nil:
		row.Subscription = resellerapi.Trial
		row.SubscriptionUntil = httpx.Ptr(c.TrialEndsAt.UTC().Format(dateFormat))
	}
	if c.ResellerAttachedAt != nil {
		row.Attached = httpx.Ptr(c.ResellerAttachedAt.UTC().Format(dateFormat))
	}
	return row
}
