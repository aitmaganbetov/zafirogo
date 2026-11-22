package billing

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	billingapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/safego"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/subscription"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram"
)

// Тексты BillingController (ключи перевода).
const (
	msgOpened      = "Счёт выставлен. Оплатите его через Kaspi."
	msgCanceled    = "Счёт отменён."
	logOpened      = "Выставлен счёт :number на :amount ₸"
	logCanceled    = "Отменён счёт :number"
	subjectInvoice = `App\Models\SubscriptionInvoice`
)

// CreateInvoice — BillingController::store(): счёт клуба за тариф на N месяцев за все его ПК.
//
//	'saas_plan_id' => ['required', 'integer', Rule::in($this->billing->payablePlans()->modelKeys())],
//	'months' => ['required', 'integer', Rule::in(SubscriptionInvoice::MONTHS)],
func (h *handlers) CreateInvoice(ctx context.Context, req billingapi.CreateInvoiceRequestObject) (billingapi.CreateInvoiceResponseObject, error) {
	var body billingapi.InvoiceInput
	if req.Body != nil {
		body = *req.Body
	}
	var fe httpx.FormErrors
	if body.SaasPlanId == 0 {
		fe.Add("saas_plan_id", httpx.RuleMessage("saas_plan_id", "required", ""))
	}
	if body.Months == 0 {
		fe.Add("months", httpx.RuleMessage("months", "required", ""))
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	var opened subscription.Opened
	var clubName string
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		clubName = info.Name
		opened, err = subscription.Open(ctx, q, subscription.OpenParams{
			ClubID: p.ClubID, PlanID: body.SaasPlanId, Months: body.Months, By: p.UserID, Now: now, Loc: info.Loc})
		if err != nil {
			return invoiceError(err)
		}
		number := subscription.Number(opened.ID)
		if _, err := activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "billing.invoice_opened", UserID: p.UserID, SubjectType: subjectInvoice, SubjectID: opened.ID, At: now,
		}.Translated(logOpened, map[string]string{"number": number, "amount": money.GroupThousands(opened.Amount/100, " ")})); err != nil {
			return err
		}
		return audit(ctx, q, p.ClubID, "invoice.opened", opened.ID, map[string]any{
			"number": number, "saas_plan_id": body.SaasPlanId, "months": opened.Months, "computers": opened.Computers,
			"price_per_computer": opened.PricePerComputer, "discount_percent": opened.DiscountPercent, "amount": opened.Amount,
		})
	})
	if err != nil {
		return nil, err
	}
	h.reportInvoice(ctx, opened, clubName)
	return billingapi.CreateInvoice201JSONResponse{Message: h.d.T(ctx, msgOpened, nil), Invoice: billingapi.Invoice{
		Id: opened.ID, Number: subscription.Number(opened.ID), PlanName: opened.PlanName, Months: opened.Months,
		Computers: opened.Computers, PricePerComputerTiyn: opened.PricePerComputer, DiscountPercent: opened.DiscountPercent,
		AmountTiyn: opened.Amount, Status: billingapi.Pending, CreatedAt: now,
	}}, nil
}

// CancelInvoice — BillingController::cancel(): свой неоплаченный счёт → canceled; чужой — 404.
func (h *handlers) CancelInvoice(ctx context.Context, req billingapi.CancelInvoiceRequestObject) (billingapi.CancelInvoiceResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		if err := subscription.CancelOwn(ctx, q, p.ClubID, req.InvoiceId, now); err != nil {
			return invoiceError(err)
		}
		number := subscription.Number(req.InvoiceId)
		if _, err := activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "billing.invoice_canceled", UserID: p.UserID, SubjectType: subjectInvoice, SubjectID: req.InvoiceId, At: now,
		}.Translated(logCanceled, map[string]string{"number": number})); err != nil {
			return err
		}
		return audit(ctx, q, p.ClubID, "invoice.canceled", req.InvoiceId, map[string]any{"number": number})
	})
	if err != nil {
		return nil, err
	}
	return billingapi.CancelInvoice200JSONResponse{Message: h.d.T(ctx, msgCanceled, nil)}, nil
}

// invoiceError — ошибки сервиса счетов как ответы формы: поля saas_plan_id и months (Rule::in), invoice (тексты
// SubscriptionBilling и BillingController), 404 для чужого счёта.
func invoiceError(err error) error {
	var fe httpx.FormErrors
	if errors.Is(err, subscription.ErrPlanNotPayable) {
		fe.Add("saas_plan_id", httpx.RuleMessage("saas_plan_id", "in", ""))
	}
	if errors.Is(err, subscription.ErrMonths) {
		fe.Add("months", httpx.RuleMessage("months", "in", ""))
	}
	if e := fe.Err(); e != nil {
		return e
	}
	switch {
	case errors.Is(err, subscription.ErrNotFound):
		return httpx.NotFound()
	case errors.Is(err, subscription.ErrNotConfigured):
		return httpx.Validation("invoice", subscription.MsgNotConfigured)
	case errors.Is(err, subscription.ErrPendingExists):
		return httpx.Validation("invoice", subscription.MsgPendingExists)
	case errors.Is(err, subscription.ErrClosed):
		return httpx.Validation("invoice", subscription.MsgClosed)
	}
	return err
}

// audit — запись audit_log (категория money: счёт — денежное обязательство клуба перед платформой).
func audit(ctx context.Context, q *store.Queries, clubID int64, action string, invoiceID int64, after map[string]any) error {
	a := httpx.Actor(ctx)
	subject := subjectInvoice
	params := store.InsertAuditLogParams{ClubID: &clubID, ActorKind: a.Kind, Category: "money", Action: action,
		SubjectType: &subject, SubjectID: &invoiceID}
	if a.UserID > 0 {
		params.ActorUserID = &a.UserID
	}
	if a.IP.IsValid() {
		ip := a.IP
		params.Ip = &ip
	}
	if a.RequestID != "" {
		params.RequestID = &a.RequestID
	}
	var err error
	if params.After, err = json.Marshal(after); err != nil {
		return err
	}
	return q.InsertAuditLog(ctx, params)
}

// reportInvoice — сообщение о счёте в чат оповещений платформы, в фоне после ответа: медленный Telegram не должен
// задерживать страницу (app()->terminating(fn () => $alerts->invoiceOpened($invoice, $club))).
// Laravel: app/Http/Controllers/BillingController.php store() (Zafiro 77ae201).
func (h *handlers) reportInvoice(ctx context.Context, inv subscription.Opened, clubName string) {
	alerts := h.d.Superadmin.TelegramAlerts
	if alerts == nil {
		return
	}
	// Ссылка — только из конфигурации (superadmin.domain или http.public_url); без неё сообщение уходит без ссылки.
	msg := telegram.OpenedInvoice{
		Number: subscription.Number(inv.ID), Club: clubName, Plan: inv.PlanName, Months: inv.Months, Computers: inv.Computers,
		AmountTenge: inv.Amount / 100, URL: h.d.Superadmin.CabinetURL("/invoices"),
	}
	log := h.d.Log()
	(*safego.Group)(nil).Go("invoice-opened-alert", func() { // паника отправки не роняет процесс (BC-04)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, err := alerts.InvoiceOpened(ctx, msg); err != nil {
			log.WarnContext(ctx, "opened invoice was not reported to the alerts chat", "invoice_id", inv.ID, "err", err)
		}
	})
}
