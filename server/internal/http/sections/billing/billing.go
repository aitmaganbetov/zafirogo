// Package billing — экран «Подписка» (api/billing.yaml). Laravel: BillingController::show(), billing/index.blade.php.
package billing

import (
	"context"
	"errors"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	billingapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// access — club-area → billing.view (RequireClubArea::ROUTES); middleware subscription у маршрута нет.
var access = httpx.Access{Routes: map[string]string{"GetBilling": "billing.show"}}

type handlers struct{ d *httpx.Deps }

var _ billingapi.StrictServerInterface = (*handlers)(nil)

// Section — подписка клуба.
func Section() httpx.Section { return httpx.Section{Name: "billing", Mount: mount} }

func mount(r chi.Router, d *httpx.Deps) {
	si := billingapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[billingapi.StrictMiddlewareFunc](access),
		billingapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	billingapi.HandlerWithOptions(si, billingapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// GetBilling — BillingController::show(): ClubSubscription::with('plan')->where('club_id', …)->first().
func (h *handlers) GetBilling(ctx context.Context, _ billingapi.GetBillingRequestObject) (billingapi.GetBillingResponseObject, error) {
	clubID := httpx.ClubID(ctx)
	out := billingapi.Billing{}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		row, err := q.BillingSubscription(ctx, clubID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out.Subscription = &billingapi.BillingSubscription{
			PlanName: row.PlanName, Status: billingapi.BillingSubscriptionStatus(row.Status),
			State:       State(row.Status, row.TrialEndsAt, row.CurrentPeriodEndsAt, h.d.NowFunc()),
			TrialEndsAt: row.TrialEndsAt, CurrentPeriodEndsAt: row.CurrentPeriodEndsAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return billingapi.GetBilling200JSONResponse(out), nil
}

// State — строка «Статус» шаблона: ! isActive() ? (isPastPaidPeriod() ? Истекла : Не активна)
// : (trialing ? Пробный период : Активна). Laravel: app/Models/ClubSubscription.php isActive(), isPastPaidPeriod().
func State(status string, trialEnds, periodEnds *time.Time, now time.Time) billingapi.BillingSubscriptionState {
	active := (status == "active" && (periodEnds == nil || periodEnds.After(now))) ||
		(status == "trialing" && trialEnds != nil && trialEnds.After(now))
	switch {
	case !active && (status == "active" || status == "expired") && periodEnds != nil && periodEnds.Before(now):
		return billingapi.BillingSubscriptionStateExpired
	case !active:
		return billingapi.BillingSubscriptionStateInactive
	case status == "trialing":
		return billingapi.BillingSubscriptionStateTrialing
	}
	return billingapi.BillingSubscriptionStateActive
}
