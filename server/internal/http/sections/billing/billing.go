// Package billing — экран «Подписка» и счета за подписку (api/billing.yaml). Laravel: BillingController,
// billing/index.blade.php, app/Services/SubscriptionBilling.php. Расчёты и переходы счёта — internal/subscription.
package billing

import (
	"context"
	"errors"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	billingapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/subscription"
)

// access — club-area (RequireClubArea::ROUTES): billing.view — экран, billing.pay — выставить и отменить счёт.
// Middleware subscription у маршрутов нет (RequireActiveSubscription: billing.show, billing.invoices.store,
// billing.invoices.cancel) — клуб с истёкшей подпиской может заплатить (GS-555).
var access = httpx.Access{Routes: map[string]string{
	"GetBilling":    "billing.show",
	"CreateInvoice": "billing.invoices.store",
	"CancelInvoice": "billing.invoices.cancel",
}}

// limits — throttle:10,1 маршрута billing.invoices.store (routes/web.php).
var limits = map[string]string{"POST /api/billing/invoices": "billing.invoices.store"}

// rightPay — право выставить и отменить счёт (ClubPermissions: 'pay' => [] — только владелец или по выбору владельца).
const rightPay = "billing.pay"

type handlers struct{ d *httpx.Deps }

var _ billingapi.StrictServerInterface = (*handlers)(nil)

// Section — подписка клуба.
func Section() httpx.Section { return httpx.Section{Name: "billing", Mount: mount, Limits: limits} }

func mount(r chi.Router, d *httpx.Deps) {
	si := billingapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[billingapi.StrictMiddlewareFunc](access),
		billingapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	billingapi.HandlerWithOptions(si, billingapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// GetBilling — BillingController::show(): подписка с планом, число ПК, скидка, тарифы к оплате, Kaspi, счета.
func (h *handlers) GetBilling(ctx context.Context, _ billingapi.GetBillingRequestObject) (billingapi.GetBillingResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	out := billingapi.Billing{CanPay: p.Can(rightPay), Months: subscription.Months,
		Plans: []billingapi.PayablePlan{}, Invoices: []billingapi.Invoice{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		var sub *subscription.Subscription
		row, err := q.BillingSubscription(ctx, p.ClubID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			sub = &subscription.Subscription{DiscountPercent: int(row.DiscountPercent), DiscountUntil: row.DiscountUntil}
		}
		terms, err := subscription.ClubTerms(ctx, q, p.ClubID, sub, now, info.Loc)
		if err != nil {
			return err
		}
		out.Computers, out.DiscountPercent = terms.Computers, terms.Discount
		if sub != nil {
			out.Subscription = subscriptionView(row, terms, now)
		}
		plans, err := q.SubscriptionPayablePlans(ctx)
		if err != nil {
			return err
		}
		for _, plan := range plans {
			v := billingapi.PayablePlan{Id: plan.ID, Name: plan.Name, PriceMonthlyTiyn: plan.PriceMonthly, Amounts: []billingapi.PlanAmount{}}
			for _, m := range subscription.Months {
				v.Amounts = append(v.Amounts, billingapi.PlanAmount{Months: m,
					AmountTiyn: subscription.Amount(plan.PriceMonthly, terms.Computers, m, terms.Discount)})
			}
			out.Plans = append(out.Plans, v)
		}
		kaspi, err := subscription.ClubKaspi(ctx, q)
		if err != nil {
			return err
		}
		if kaspi != nil {
			out.Kaspi = &billingapi.Kaspi{Url: kaspi.URL}
			if kaspi.Recipient != "" {
				out.Kaspi.Recipient = &kaspi.Recipient
			}
		}
		invoices, err := q.SubscriptionClubInvoices(ctx, p.ClubID)
		if err != nil {
			return err
		}
		for _, inv := range invoices {
			v := invoiceView(inv)
			// $invoices->firstWhere('status', 'pending')
			if out.Pending == nil && inv.Status == "pending" {
				pending := v
				out.Pending = &pending
			}
			out.Invoices = append(out.Invoices, v)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return billingapi.GetBilling200JSONResponse(out), nil
}

// subscriptionView — карточка подписки: статус и цена тарифа за ПК в месяц со своей скидкой клуба
// (ClubSubscription::monthlyPrice(), BR-182) и «за K ПК» — произведение этой цены на число ПК (GAP-038).
func subscriptionView(row store.BillingSubscriptionRow, terms subscription.Terms, now time.Time) *billingapi.BillingSubscription {
	v := &billingapi.BillingSubscription{
		PlanName: row.PlanName, SaasPlanId: row.SaasPlanID, Status: billingapi.BillingSubscriptionStatus(row.Status),
		State:       State(row.Status, row.TrialEndsAt, row.CurrentPeriodEndsAt, now),
		TrialEndsAt: row.TrialEndsAt, CurrentPeriodEndsAt: row.CurrentPeriodEndsAt, DiscountPercent: terms.OwnDiscount,
	}
	if row.PriceMonthly != nil {
		price := subscription.MonthlyPrice(*row.PriceMonthly, terms.OwnDiscount)
		total := price * terms.Computers
		v.MonthlyPriceTiyn, v.MonthlyTotalTiyn = &price, &total
	}
	if terms.OwnDiscount > 0 && row.DiscountUntil != nil {
		until := row.DiscountUntil.Format("2006-01-02")
		v.DiscountUntil = &until
	}
	return v
}

func invoiceView(inv store.SubscriptionClubInvoicesRow) billingapi.Invoice {
	v := billingapi.Invoice{
		Id: inv.ID, Number: subscription.Number(inv.ID), PlanName: inv.PlanName, Months: int(inv.Months),
		Computers: int64(inv.Computers), PricePerComputerTiyn: inv.PricePerComputer, DiscountPercent: int(inv.DiscountPercent),
		AmountTiyn: inv.Amount, Status: billingapi.InvoiceStatus(inv.Status), PeriodEndsAt: inv.PeriodEndsAt,
	}
	if inv.CreatedAt != nil {
		v.CreatedAt = *inv.CreatedAt
	}
	return v
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
