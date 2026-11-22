package superadmin

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/subscription"
)

// «Оплаты» — счета клубов за подписку: суперадмин отмечает их оплаченными, когда приходит платёж Kaspi.
// Laravel: app/Http/Controllers/Superadmin/InvoiceController.php, superadmin/invoices/index.blade.php.
//
// Отличие от Laravel (решение владельца, ревью 22.11.2026): «Оплата получена» и ссылка на оплату Kaspi требуют
// свежего кода TOTP действующего суперадмина и закрыты в режиме просмотра клуба — как выплата реселлеру (аудит
// A-10). Первое продлевает подписку без денег, второе направляет платежи всех клубов по новой ссылке: украденной
// сессии для этого недостаточно.
const (
	invoicesPage   = 30
	subjectInvoice = `App\Models\SubscriptionInvoice`

	msgInvoicePaid     = "Счёт :number оплачен, подписка продлена до :date."
	msgInvoiceCanceled = "Счёт отменён."
	msgKaspiSaved      = "Настройки оплаты сохранены."
	logInvoicePaid     = "Счёт :number оплачен: подписка клуба :club продлена до :date"
	logInvoiceCanceled = "Отменён счёт :number клуба :club"
	logKaspiChanged    = "Изменена ссылка на оплату Kaspi"
	logKaspiDisabled   = "Оплата через Kaspi отключена"

	maxKaspiURLChars       = 500
	maxKaspiRecipientChars = 200
)

// readKaspi — SubscriptionBilling::kaspi() в транзакции кабинета.
func readKaspi(ctx context.Context, q *store.Queries) (*subscription.Kaspi, error) {
	raw, err := q.SuperadminSetting(ctx, subscription.KaspiSetting)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return subscription.ParseKaspi(raw), nil
}

// SaListInvoices — InvoiceController::index(). Даты — по UTC (кабинет вне клуба: CurrentClub::timezone() = UTC).
func (h *handlers) SaListInvoices(ctx context.Context, req superadminapi.SaListInvoicesRequestObject) (superadminapi.SaListInvoicesResponseObject, error) {
	pg := page(req.Params.Page)
	out := superadminapi.InvoiceBoard{Pending: []superadminapi.PendingInvoice{}, Closed: []superadminapi.ClosedInvoice{}, Page: pg, PerPage: invoicesPage}
	err := h.tx(ctx, func(q *store.Queries) error {
		pending, err := q.SuperadminPendingInvoices(ctx)
		if err != nil {
			return err
		}
		for _, i := range pending {
			row := superadminapi.PendingInvoice{Id: i.ID, Number: subscription.Number(i.ID), ClubId: i.ClubID, ClubName: i.ClubName,
				PlanName: i.PlanName, Months: int(i.Months), Computers: int64(i.Computers), PricePerComputerTiyn: i.PricePerComputer,
				DiscountPercent: int(i.DiscountPercent), AmountTiyn: i.Amount}
			if i.CreatedAt != nil {
				row.Created = i.CreatedAt.UTC().Format("02.01.2006 15:04")
			}
			out.Pending = append(out.Pending, row)
		}
		if out.Total, err = q.SuperadminCountClosedInvoices(ctx); err != nil {
			return err
		}
		closed, err := q.SuperadminClosedInvoices(ctx, store.SuperadminClosedInvoicesParams{PageSize: invoicesPage, PageOffset: int64(pg-1) * invoicesPage})
		if err != nil {
			return err
		}
		for _, i := range closed {
			row := superadminapi.ClosedInvoice{Id: i.ID, Number: subscription.Number(i.ID), ClubId: i.ClubID, ClubName: i.ClubName,
				PlanName: i.PlanName, Months: int(i.Months), AmountTiyn: i.Amount, Status: superadminapi.ClosedInvoiceStatus(i.Status)}
			if i.CreatedAt != nil {
				row.Created = i.CreatedAt.UTC().Format("02.01.2006")
			}
			if i.PaidAt != nil {
				row.Paid = ptr(i.PaidAt.UTC().Format("02.01.2006"))
			}
			out.Closed = append(out.Closed, row)
		}
		kaspi, err := readKaspi(ctx, q)
		if kaspi != nil {
			out.Kaspi = &superadminapi.KaspiSettings{Url: kaspi.URL, Recipient: kaspi.Recipient}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaListInvoices200JSONResponse(out), nil
}

// invoiceError — ошибки сервиса счетов: нет счёта — 404 (findOrFail), закрыт — поле invoice «Счёт уже закрыт.».
func invoiceError(err error) error {
	switch {
	case errors.Is(err, subscription.ErrNotFound):
		return httpx.NotFound()
	case errors.Is(err, subscription.ErrClosed):
		return httpx.Validation("invoice", subscription.MsgClosed)
	}
	return err
}

// SaPayInvoice — InvoiceController::pay(): деньги по счёту пришли, подписка клуба продлевается (BR-183).
func (h *handlers) SaPayInvoice(ctx context.Context, req superadminapi.SaPayInvoiceRequestObject) (superadminapi.SaPayInvoiceResponseObject, error) {
	if ro := readOnly(ctx); ro != nil {
		return superadminapi.SaPayInvoice403JSONResponse{ForbiddenJSONResponse: *ro}, nil
	}
	if limited, err := h.confirmSecondFactor(ctx, req.Body); err != nil {
		return nil, err
	} else if limited != nil {
		return superadminapi.SaPayInvoice429JSONResponse{TooManyRequestsJSONResponse: *limited}, nil
	}
	now := h.d.NowFunc()
	by := httpx.Principal(ctx).UserID
	var paid subscription.Paid
	err := h.tx(ctx, func(q *store.Queries) error {
		var err error
		if paid, err = subscription.MarkPaid(ctx, q, req.InvoiceId, by, now); err != nil {
			return invoiceError(err)
		}
		number, date := subscription.Number(paid.InvoiceID), paid.PeriodEndsAt.UTC().Format("02.01.2006")
		// Детали — как в старом журнале: сумма в тенге (счёт — целые тенге), месяцы.
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.invoice_paid", Key: logInvoicePaid,
			Params: map[string]string{"number": number, "club": paid.ClubName, "date": date},
			ClubID: paid.ClubID, SubjectType: subjectInvoice, SubjectID: paid.InvoiceID,
			Details: map[string]any{"amount": paid.Amount / 100, "months": paid.Months}}); err != nil {
			return err
		}
		var before any
		if paid.Before != nil {
			before = subscriptionAudit(paid.Before.SaasPlanID, paid.Before.Status, paid.Before.CurrentPeriodEndsAt)
		}
		after := map[string]any{"invoice": number, "amount": paid.Amount, "months": paid.Months, "status": "active",
			"current_period_ends_at": paid.PeriodEndsAt.UTC().Format("2006-01-02T15:04:05Z07:00")}
		if paid.ResellerAmount != nil {
			after["reseller_amount"] = *paid.ResellerAmount
		}
		return h.audit(ctx, q, paid.ClubID, "money", "invoice.paid", subjectInvoice, paid.InvoiceID, before, after)
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaPayInvoice200JSONResponse(msg(h.d.T(ctx, msgInvoicePaid, map[string]string{
		"number": subscription.Number(paid.InvoiceID), "date": paid.PeriodEndsAt.UTC().Format("02.01.2006")}))), nil
}

// SaCancelInvoice — InvoiceController::cancel(): неоплаченный счёт клуба → отменён.
func (h *handlers) SaCancelInvoice(ctx context.Context, req superadminapi.SaCancelInvoiceRequestObject) (superadminapi.SaCancelInvoiceResponseObject, error) {
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		c, err := subscription.Cancel(ctx, q, req.InvoiceId, now)
		if err != nil {
			return invoiceError(err)
		}
		number := subscription.Number(c.InvoiceID)
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.invoice_canceled", Key: logInvoiceCanceled,
			Params: map[string]string{"number": number, "club": c.ClubName}, ClubID: c.ClubID,
			SubjectType: subjectInvoice, SubjectID: c.InvoiceID}); err != nil {
			return err
		}
		return h.audit(ctx, q, c.ClubID, "money", "invoice.canceled", subjectInvoice, c.InvoiceID,
			map[string]any{"status": "pending"}, map[string]any{"invoice": number, "status": "canceled"})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaCancelInvoice200JSONResponse(msg(h.d.T(ctx, msgInvoiceCanceled, nil))), nil
}

// SaSetInvoiceKaspi — InvoiceController::kaspi(): ссылка Kaspi Pay и получатель; пустая ссылка выключает оплату.
//
//	'url' => ['nullable', 'url:https', 'max:500'],
//	'recipient' => ['nullable', 'string', 'max:200'],
func (h *handlers) SaSetInvoiceKaspi(ctx context.Context, req superadminapi.SaSetInvoiceKaspiRequestObject) (superadminapi.SaSetInvoiceKaspiResponseObject, error) {
	if ro := readOnly(ctx); ro != nil {
		return superadminapi.SaSetInvoiceKaspi403JSONResponse{ForbiddenJSONResponse: *ro}, nil
	}
	var link, recipient, code string
	if req.Body != nil {
		code = req.Body.Code
		// TrimStrings.
		if req.Body.Url != nil {
			link = strings.TrimSpace(*req.Body.Url)
		}
		if req.Body.Recipient != nil {
			recipient = strings.TrimSpace(*req.Body.Recipient)
		}
	}
	var fe httpx.FormErrors
	switch {
	case link == "":
	case !httpsURL(link):
		fe.Add("url", "The url field must be a valid URL.")
	case utf8.RuneCountInString(link) > maxKaspiURLChars:
		fe.Add("url", httpx.RuleMessage("url", "max_string", "500"))
	}
	if utf8.RuneCountInString(recipient) > maxKaspiRecipientChars {
		fe.Add("recipient", httpx.RuleMessage("recipient", "max_string", "200"))
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	// Код — после проверки полей: ошибка в ссылке не расходует код (один код — одно действие).
	if limited, err := h.confirmSecondFactor(ctx, &superadminapi.SecondFactorConfirm{Code: code}); err != nil {
		return nil, err
	} else if limited != nil {
		return superadminapi.SaSetInvoiceKaspi429JSONResponse{TooManyRequestsJSONResponse: *limited}, nil
	}
	now := h.d.NowFunc()
	uid := httpx.Principal(ctx).UserID
	err := h.tx(ctx, func(q *store.Queries) error {
		before, err := readKaspi(ctx, q)
		if err != nil {
			return err
		}
		// PlatformSetting::write($key, $data['url'] ? ['url' => …, 'recipient' => …] : null)
		key, after := logKaspiDisabled, map[string]any(nil)
		if link == "" {
			err = q.SuperadminDeleteSetting(ctx, subscription.KaspiSetting)
		} else {
			key = logKaspiChanged
			after = map[string]any{"url": link, "recipient": nil}
			if recipient != "" {
				after["recipient"] = recipient
			}
			var raw []byte
			if raw, err = json.Marshal(after); err != nil {
				return err
			}
			err = q.SuperadminPutSetting(ctx, store.SuperadminPutSettingParams{Key: subscription.KaspiSetting, Value: raw, UpdatedBy: &uid, Now: &now})
		}
		if err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.billing_kaspi_changed", Key: key}); err != nil {
			return err
		}
		var was any
		if before != nil {
			was = map[string]any{"url": before.URL, "recipient": before.Recipient}
		}
		var is any
		if after != nil {
			is = after
		}
		return h.audit(ctx, q, 0, "settings", "billing.kaspi_changed", "", 0, was, is)
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaSetInvoiceKaspi200JSONResponse(msg(h.d.T(ctx, msgKaspiSaved, nil))), nil
}

// httpsURL — правило Laravel 'url:https': абсолютный адрес со схемой https и хостом. Ссылку открывает клуб кнопкой
// «Оплатить в Kaspi», поэтому другие схемы (http, javascript:) не принимаются.
func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && !strings.ContainsAny(s, " \t\r\n")
}
