package billing

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/subscription"
)

// route('superadmin.invoices.index'): на хосте кабинета — /invoices, иначе /superadmin/invoices на хосте запроса.
func TestCabinetInvoicesURL(t *testing.T) {
	if got := cabinetInvoicesURL("superadmin.zafiro.kz", "zafiro.kz"); got != "https://superadmin.zafiro.kz/invoices" {
		t.Fatal(got)
	}
	if got := cabinetInvoicesURL("", "zafiro.kz"); got != "https://zafiro.kz/superadmin/invoices" {
		t.Fatal(got)
	}
}

// Ошибки сервиса счетов → ответы формы: поля как в $request->validate() и withErrors(['invoice' => …]).
func TestInvoiceError(t *testing.T) {
	fields := func(err error) (int, map[string][]string) {
		var api *httpx.APIError
		if !errors.As(err, &api) {
			return 0, nil
		}
		body, _ := api.Body.(apicommon.ValidationError)
		return api.Status, body.Errors
	}
	for _, tc := range []struct {
		err    error
		status int
		field  string
		text   string
	}{
		{subscription.ErrNotFound, http.StatusNotFound, "", ""},
		{subscription.ErrNotConfigured, http.StatusUnprocessableEntity, "invoice", "Оплата пока не настроена. Напишите в поддержку Zafiro."},
		{subscription.ErrPendingExists, http.StatusUnprocessableEntity, "invoice", "У клуба уже есть неоплаченный счёт: оплатите или отмените его."},
		{subscription.ErrClosed, http.StatusUnprocessableEntity, "invoice", "Счёт уже закрыт."},
		{subscription.ErrMonths, http.StatusUnprocessableEntity, "months", "The selected months is invalid."},
		{subscription.ErrPlanNotPayable, http.StatusUnprocessableEntity, "saas_plan_id", "The selected saas plan id is invalid."},
		{fmt.Errorf("обёртка: %w", subscription.ErrClosed), http.StatusUnprocessableEntity, "invoice", "Счёт уже закрыт."},
	} {
		status, errs := fields(invoiceError(tc.err))
		if status != tc.status || (tc.field != "" && (len(errs[tc.field]) != 1 || errs[tc.field][0] != tc.text)) {
			t.Errorf("%v: %d %v", tc.err, status, errs)
		}
	}
	// Обе ошибки формы сразу.
	if _, errs := fields(invoiceError(errors.Join(subscription.ErrPlanNotPayable, subscription.ErrMonths))); len(errs) != 2 {
		t.Errorf("две ошибки формы: %v", errs)
	}
	// Прочие ошибки не превращаются в ответ формы.
	other := errors.New("db down")
	if got := invoiceError(other); !errors.Is(got, other) {
		t.Errorf("прочая ошибка: %v", got)
	}
}
