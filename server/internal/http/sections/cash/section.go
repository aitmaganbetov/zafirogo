// Package cash — REST-раздел: касса (api/cash.yaml). Конвенции — api/README.md.
package cash

import (
	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"

	cashapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — права операций раздела: операция → маршрут Laravel (RequireClubArea::ROUTES → право).
var access = httpx.Access{
	Routes: map[string]string{
		"GetCashShift":          "cash-register.index",
		"OpenCashShift":         "cash-register.shifts.open",
		"CloseCashShift":        "cash-register.shifts.close",
		"GetCashXReport":        "cash-register.x-report",
		"GetCashZReport":        "cash-register.z-report",
		"IssueZReportNonce":     "cash-register.z-report",
		"MarkZReportPrinted":    "cash-register.z-report.printed",
		"CreateCashSale":        "cash-register.sales.store",
		"CreateBalanceTopUp":    "cash-register.balance-top-ups.store",
		"CreateCashTransaction": "cash-register.transactions.store",
		"ListCashOperations":    "cash-register.operations",
		"GetCashReceipt":        "cash-register.receipt",
		"RefundCashReceipt":     "cash-register.refund",
	},
	// CashRegisterController::refund() — abort_unless(canRefund(), 403, …).
	Messages: map[string]string{"RefundCashReceipt": cash.MsgNoRefundRight},
}

// handlers реализует cashapi.StrictServerInterface.
type handlers struct {
	d *httpx.Deps
}

var _ cashapi.StrictServerInterface = (*handlers)(nil)

// Section — касса (api/cash.yaml). Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "cash", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := cashapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[cashapi.StrictMiddlewareFunc](access),
		cashapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	cashapi.HandlerWithOptions(si, cashapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
