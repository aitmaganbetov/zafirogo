package httpserver

// Касса: смена, продажа POS, пополнение баланса, приход/расход, операции смены, чек, возврат, X/Z-отчёты.
// Laravel: app/Http/Controllers/CashRegisterController.php, app/Services/CashRegisterService.php.

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/api"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты статусов старого кода (flash 'status').
const (
	msgSaleDone        = "Продажа проведена. Распечатайте чек."
	msgTransactionDone = "Операция проведена"
	msgTopUpDone       = "Баланс пользователя пополнен."
	msgTopUpBonusDone  = "Баланс пользователя пополнен. Бонус за пополнение: :amount."
	msgRefundDone      = "Возврат проведён: :amount KZT выданы из кассы."
)

func (h *handlers) cash() *cashsvc.Service {
	if h.opts.Cash != nil {
		return h.opts.Cash
	}
	return &cashsvc.Service{Now: h.opts.Now}
}

// ---------------------------------------------------------------------------
// Представления
// ---------------------------------------------------------------------------

func shiftView(s store.CashShift) api.CashShift {
	return api.CashShift{
		Id: s.ID, Status: api.CashShiftStatus(s.Status), OpenedBy: s.OpenedBy, ClosedBy: s.ClosedBy,
		OpeningBalanceTiyn: s.OpeningBalance, ClosingBalanceTiyn: s.ClosingBalance, OpenedAt: s.OpenedAt,
		ClosedAt: s.ClosedAt, ZReportPrinted: s.ZReportPrintedAt != nil,
	}
}

func totalsView(t cash.Totals) api.CashTotals {
	return api.CashTotals{
		OpeningTiyn: int64(t.Opening), IncomeTiyn: int64(t.Income), ExpenseTiyn: int64(t.Expense), BalanceTiyn: int64(t.Balance()),
	}
}

func shiftState(s *store.CashShift, t cash.Totals) api.CashShiftState {
	if s == nil {
		return api.CashShiftState{}
	}
	sv, tv := shiftView(*s), totalsView(t)
	return api.CashShiftState{Shift: &sv, Totals: &tv}
}

func txView(row store.CashTransaction) (api.CashTransaction, error) {
	op, err := cashsvc.OperationOf(row)
	if err != nil {
		return api.CashTransaction{}, err
	}
	v := api.CashTransaction{
		Id: row.ID, ShiftId: row.CashShiftID, Type: api.CashTransactionType(row.Type), AmountTiyn: row.Amount,
		Description: row.Description, UserId: row.UserID, RefundOfId: row.RefundOfID,
	}
	if row.CreatedAt != nil {
		v.CreatedAt = *row.CreatedAt
	}
	if row.PaymentMethod != nil {
		v.PaymentMethod = ptr(api.CashTransactionPaymentMethod(*row.PaymentMethod))
	}
	if row.IncomeKind != nil {
		v.IncomeKind = ptr(api.CashTransactionIncomeKind(*row.IncomeKind))
	}
	if op.Receipt != nil {
		if op.Receipt.Items != nil {
			items := make([]api.ReceiptLine, 0, len(op.Receipt.Items))
			for _, l := range op.Receipt.Items {
				line := api.ReceiptLine{Name: l.Name, Quantity: l.Quantity, PriceTiyn: int64(l.Price), TotalTiyn: int64(l.Total)}
				if l.ProductID != 0 {
					line.ProductId = ptr(l.ProductID)
				}
				if l.ServiceID != 0 {
					line.ServiceId = ptr(l.ServiceID)
				}
				items = append(items, line)
			}
			v.Items = &items
		}
		if t := op.Receipt.TopUp; t != nil {
			v.TopUp = &api.ReceiptTopUp{UserId: t.UserID, BalanceType: api.ReceiptTopUpBalanceType(t.Field), BonusTiyn: int64(t.Bonus)}
		}
	}
	return v, nil
}

// numberFormat — number_format($x, 2, $dec, ' ') PHP для тиынов.
func numberFormat(t money.Tiyn, dec string) string {
	s := t.Tenge()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	out := b.String() + dec + frac
	if neg {
		out = "-" + out
	}
	return out
}

func (h *handlers) clubID(ctx context.Context) int64 { return h.principal(ctx).ClubID }

// ---------------------------------------------------------------------------
// Смена
// ---------------------------------------------------------------------------

// GetCashShift — CashRegisterController::index(): открытая смена и итоги.
func (h *handlers) GetCashShift(ctx context.Context, _ api.GetCashShiftRequestObject) (api.GetCashShiftResponseObject, error) {
	var out api.CashShiftState
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		shift, totals, err := h.cash().CurrentShift(ctx, q, h.clubID(ctx))
		out = shiftState(shift, totals)
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.GetCashShift200JSONResponse(out), nil
}

// OpenCashShift — CashRegisterController::open().
func (h *handlers) OpenCashShift(ctx context.Context, req api.OpenCashShiftRequestObject) (api.OpenCashShiftResponseObject, error) {
	balance, err := cash.ParseOpeningBalance(money.Tiyn(req.Body.OpeningBalanceTiyn).Tenge())
	if err != nil {
		return nil, err
	}
	clubID := h.clubID(ctx)
	var out api.CashShiftState
	err = h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		out, err = idempotent(ctx, q, clubID, req.Params.IdempotencyKey, "open_shift", req.Body, h.now(), func() (api.CashShiftState, error) {
			res, err := h.cash().OpenShift(ctx, q, cashsvc.OpenShiftParams{ClubID: clubID, OpeningBalance: balance, Actor: h.actor(ctx)})
			if err != nil {
				return api.CashShiftState{}, err
			}
			return shiftState(&res.Shift, cash.Totals{Opening: money.Tiyn(res.Shift.OpeningBalance)}), nil
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.OpenCashShift201JSONResponse(out), nil
}

// CloseCashShift — CashRegisterController::close(): только после отметки печати Z-отчёта.
func (h *handlers) CloseCashShift(ctx context.Context, req api.CloseCashShiftRequestObject) (api.CloseCashShiftResponseObject, error) {
	clubID := h.clubID(ctx)
	var out api.CashShiftState
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = idempotent(ctx, q, clubID, req.Params.IdempotencyKey, "close_shift", map[string]int64{"shift_id": req.ShiftId}, h.now(),
			func() (api.CashShiftState, error) {
				res, err := h.cash().CloseShift(ctx, q, cashsvc.CloseShiftParams{
					ClubID: clubID, ShiftID: req.ShiftId, Actor: h.actor(ctx), RequireZReport: true,
				})
				if err != nil {
					return api.CashShiftState{}, err
				}
				return shiftState(&res.Shift, res.Totals), nil
			})
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.CloseCashShift200JSONResponse(out), nil
}

// ---------------------------------------------------------------------------
// Отчёты
// ---------------------------------------------------------------------------

func (h *handlers) report(ctx context.Context, typ cash.ReportType, shiftID int64) (api.CashReport, error) {
	var out api.CashReport
	clubID := h.clubID(ctx)
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		p := h.principal(ctx)
		operator := ""
		names, err := q.ListClubUserNames(ctx, store.ListClubUserNamesParams{ClubID: clubID, Ids: []int64{p.UserID}})
		if err != nil {
			return err
		}
		if len(names) > 0 {
			operator = names[0].Name
		}
		res, err := h.cash().Report(ctx, q, cashsvc.ReportParams{ClubID: clubID, Type: typ, ShiftID: shiftID, OperatorName: operator})
		if err != nil {
			return err
		}
		r := res.Report
		out = api.CashReport{
			Type: api.CashReportType(typ), Shift: shiftView(res.Shift), Totals: totalsView(r.Totals),
			ItemsTotalTiyn: int64(r.ItemsTotal), DiscountTiyn: int64(r.Discount),
			CashierName: res.People.Cashier, ClosedByName: res.People.ClosedBy, OperatorName: res.People.Operator,
			Kinds: make([]api.CashReportRow, 0, len(r.Kinds)), Items: make([]api.CashReportItem, 0, len(r.Items)),
			Staff: make([]api.CashReportStaff, 0, len(r.Staff)),
		}
		out.Refunds.Count, out.Refunds.AmountTiyn = r.Refunds.Count, int64(r.Refunds.Amount)
		for _, k := range r.Kinds {
			out.Kinds = append(out.Kinds, api.CashReportRow{Label: k.Label, Count: k.Count, AmountTiyn: int64(k.Amount)})
		}
		for _, it := range r.Items {
			out.Items = append(out.Items, api.CashReportItem{Name: it.Name, Quantity: it.Quantity, AmountTiyn: int64(it.Amount), Stock: it.Stock})
		}
		for _, st := range r.Staff {
			out.Staff = append(out.Staff, api.CashReportStaff{
				UserId: st.UserID, Name: st.Name, Count: st.Count, IncomeTiyn: int64(st.Income), ExpenseTiyn: int64(st.Expense),
			})
		}
		return nil
	})
	return out, err
}

// GetCashXReport — CashRegisterController::xReport(): открытая смена (нет — 404).
func (h *handlers) GetCashXReport(ctx context.Context, _ api.GetCashXReportRequestObject) (api.GetCashXReportResponseObject, error) {
	r, err := h.report(ctx, cash.ReportX, 0)
	if err != nil {
		return nil, err
	}
	return api.GetCashXReport200JSONResponse(r), nil
}

// GetCashZReport — CashRegisterController::zReport().
func (h *handlers) GetCashZReport(ctx context.Context, req api.GetCashZReportRequestObject) (api.GetCashZReportResponseObject, error) {
	r, err := h.report(ctx, cash.ReportZ, req.ShiftId)
	if err != nil {
		return nil, err
	}
	return api.GetCashZReport200JSONResponse(r), nil
}

// IssueZReportNonce — одноразовый nonce печати (zReport(): session()->put(z_report_nonce…)).
func (h *handlers) IssueZReportNonce(ctx context.Context, req api.IssueZReportNonceRequestObject) (api.IssueZReportNonceResponseObject, error) {
	var nonce string
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		nonce, err = h.cash().IssueZReportNonce(ctx, q, h.clubID(ctx), req.ShiftId)
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.IssueZReportNonce200JSONResponse{Nonce: nonce}, nil
}

// MarkZReportPrinted — CashRegisterController::markZReportPrinted(): смена не открыта — 422, nonce неверный — 419.
func (h *handlers) MarkZReportPrinted(ctx context.Context, req api.MarkZReportPrintedRequestObject) (api.MarkZReportPrintedResponseObject, error) {
	var ok bool
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		// Неверный nonce гасится и при отказе — транзакция коммитится.
		ok, err = h.cash().MarkZReportPrinted(ctx, q, h.clubID(ctx), req.ShiftId, req.Body.Nonce, h.actor(ctx))
		return err
	})
	switch {
	case errors.Is(err, cashsvc.ErrShiftNotOpen):
		return nil, newAPIError(http.StatusUnprocessableEntity, "shift_not_open", "")
	case err != nil:
		return nil, err
	case !ok:
		return nil, newAPIError(419, "page_expired", "")
	}
	return api.MarkZReportPrinted200JSONResponse{Printed: true}, nil
}

// ---------------------------------------------------------------------------
// Операции
// ---------------------------------------------------------------------------

// CreateCashSale — CashRegisterController::sale().
func (h *handlers) CreateCashSale(ctx context.Context, req api.CreateCashSaleRequestObject) (api.CreateCashSaleResponseObject, error) {
	items := make([]cash.SaleItem, len(req.Body.Items))
	for i, it := range req.Body.Items {
		items[i] = cash.SaleItem{Quantity: it.Quantity}
		if it.ProductId != nil {
			items[i].ProductID = *it.ProductId
		}
		if it.ServiceId != nil {
			items[i].ServiceID = *it.ServiceId
		}
	}
	clubID, key := h.clubID(ctx), req.Params.IdempotencyKey
	var out api.CashOperationResult
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = idempotent(ctx, q, clubID, key, "sale", req.Body, h.now(), func() (api.CashOperationResult, error) {
			res, err := h.cash().Sale(ctx, q, cashsvc.SaleParams{ClubID: clubID, Items: items, IdempotencyKey: key, Actor: h.actor(ctx)})
			if err != nil {
				return api.CashOperationResult{}, err
			}
			tx, err := txView(res.Transaction)
			return api.CashOperationResult{Transaction: tx, Replayed: res.Replayed, Message: msgSaleDone}, err
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.CreateCashSale201JSONResponse(out), nil
}

// CreateBalanceTopUp — CashRegisterController::topUpBalance().
func (h *handlers) CreateBalanceTopUp(ctx context.Context, req api.CreateBalanceTopUpRequestObject) (api.CreateBalanceTopUpResponseObject, error) {
	b := req.Body
	clubID, key := h.clubID(ctx), req.Params.IdempotencyKey
	var out api.TopUpResult
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		// StoreBalanceTopUpRequest: все ошибки полей сразу (user_id, amount, balance_type, payment_method; GS-013).
		var form formErrors
		names, err := q.ListClubUserNames(ctx, store.ListClubUserNamesParams{ClubID: clubID, Ids: []int64{b.UserId}})
		if err != nil {
			return err
		}
		if len(names) == 0 {
			form.add("user_id", ruleMessage("user_id", cash.RuleIn, ""))
		}
		amount, perr := cash.ParseTopUpAmount(money.Tiyn(b.AmountTiyn).Tenge())
		if perr != nil && !form.addErr(perr) {
			return perr
		}
		if !cash.BalanceField(b.BalanceType).Valid() {
			form.add("balance_type", ruleMessage("balance_type", cash.RuleIn, ""))
		}
		if !cash.PaymentMethod(b.PaymentMethod).Valid() {
			form.add("payment_method", ruleMessage("payment_method", cash.RuleIn, ""))
		}
		if err := form.err(); err != nil {
			return err
		}
		out, err = idempotent(ctx, q, clubID, key, "top_up", b, h.now(), func() (api.TopUpResult, error) {
			res, err := h.cash().TopUp(ctx, q, cashsvc.TopUpParams{
				ClubID: clubID, UserID: b.UserId, Field: cash.BalanceField(b.BalanceType), Amount: amount,
				PaymentMethod: cash.PaymentMethod(b.PaymentMethod), IdempotencyKey: key, Actor: h.actor(ctx),
			})
			if err != nil {
				return api.TopUpResult{}, err
			}
			tx, err := txView(res.Transaction)
			if err != nil {
				return api.TopUpResult{}, err
			}
			mb, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: clubID, UserID: b.UserId})
			if err != nil {
				return api.TopUpResult{}, err
			}
			msg := msgTopUpDone
			if res.Bonus > 0 {
				msg = substitute(msgTopUpBonusDone, map[string]string{"amount": numberFormat(res.Bonus, ".")})
			}
			return api.TopUpResult{
				Transaction: tx, BonusTiyn: int64(res.Bonus), NewBalanceTiyn: mb.Balance, NewBonusBalanceTiyn: mb.BonusBalance,
				Replayed: res.Replayed, Message: msg,
			}, nil
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.CreateBalanceTopUp201JSONResponse(out), nil
}

// CreateCashTransaction — CashRegisterController::transaction() (StoreCashTransactionRequest: type, amount, description).
func (h *handlers) CreateCashTransaction(ctx context.Context, req api.CreateCashTransactionRequestObject) (api.CreateCashTransactionResponseObject, error) {
	b := req.Body
	typ := cash.Type(b.Type)
	if typ != cash.Income && typ != cash.Expense {
		return nil, ruleError("type", cash.RuleIn)
	}
	amount, err := cash.ParseTransactionAmount(money.Tiyn(b.AmountTiyn).Tenge())
	if err != nil {
		return nil, err
	}
	switch {
	case strings.TrimSpace(b.Description) == "":
		return nil, ruleError("description", cash.RuleRequired)
	case utf8.RuneCountInString(b.Description) > cash.MaxDescription:
		return nil, ruleError("description", cash.RuleMax)
	}
	clubID, key := h.clubID(ctx), req.Params.IdempotencyKey
	var out api.CashOperationResult
	err = h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = idempotent(ctx, q, clubID, key, "cash_transaction", b, h.now(), func() (api.CashOperationResult, error) {
			res, err := h.cash().Transact(ctx, q, cashsvc.TransactParams{
				ClubID: clubID, Type: typ, Amount: amount, Description: b.Description, IdempotencyKey: key, Actor: h.actor(ctx),
			})
			if err != nil {
				return api.CashOperationResult{}, err
			}
			tx, err := txView(res.Transaction)
			return api.CashOperationResult{Transaction: tx, Replayed: res.Replayed, Message: msgTransactionDone}, err
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.CreateCashTransaction201JSONResponse(out), nil
}

// ListCashOperations — CashRegisterController::operations(): приходы открытой смены с возвратом, новые сверху.
func (h *handlers) ListCashOperations(ctx context.Context, req api.ListCashOperationsRequestObject) (api.ListCashOperationsResponseObject, error) {
	clubID, canRefund := h.clubID(ctx), h.principal(ctx).Can("cash.delete")
	before := int64(math.MaxInt64)
	if req.Params.BeforeId != nil {
		before = *req.Params.BeforeId
	}
	size := pageSize(req.Params.Limit)
	var out api.CashOperationPage
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		shift, err := q.GetOpenCashShift(ctx, clubID)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFoundError()
		}
		if err != nil {
			return err
		}
		rows, err := q.AdminListShiftIncomes(ctx, store.AdminListShiftIncomesParams{
			ClubID: clubID, CashShiftID: shift.ID, BeforeID: before, PageSize: size,
		})
		if err != nil {
			return err
		}
		out = api.CashOperationPage{ShiftId: shift.ID, Items: make([]api.CashReceipt, 0, len(rows))}
		for _, r := range rows {
			row := store.CashTransaction{
				ID: r.ID, CashShiftID: r.CashShiftID, UserID: r.UserID, Type: r.Type, Amount: r.Amount,
				Description: r.Description, ReceiptData: r.ReceiptData, CreatedAt: r.CreatedAt, ClubID: clubID,
				PaymentMethod: r.PaymentMethod, IncomeKind: r.IncomeKind,
			}
			rec, err := receiptView(row, canRefund)
			if err != nil {
				return err
			}
			if r.RefundID != nil {
				ref := api.CashRefundRef{Id: *r.RefundID}
				if r.RefundedAt != nil {
					ref.CreatedAt = *r.RefundedAt
				}
				rec.Refund = &ref
			}
			out.Items = append(out.Items, rec)
		}
		if len(rows) == int(size) {
			out.NextBeforeId = ptr(rows[len(rows)-1].ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return api.ListCashOperations200JSONResponse(out), nil
}

func receiptView(row store.CashTransaction, canRefund bool) (api.CashReceipt, error) {
	tx, err := txView(row)
	if err != nil {
		return api.CashReceipt{}, err
	}
	op, err := cashsvc.OperationOf(row)
	if err != nil {
		return api.CashReceipt{}, err
	}
	return api.CashReceipt{Transaction: tx, Refundable: op.RefundKind() != "", CanRefund: canRefund}, nil
}

// GetCashReceipt — CashRegisterController::receipt(): только приход (иначе 404).
func (h *handlers) GetCashReceipt(ctx context.Context, req api.GetCashReceiptRequestObject) (api.GetCashReceiptResponseObject, error) {
	clubID, canRefund := h.clubID(ctx), h.principal(ctx).Can("cash.delete")
	var out api.CashReceipt
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		row, err := q.GetCashTransaction(ctx, store.GetCashTransactionParams{ClubID: clubID, ID: req.TransactionId})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.Type != string(cash.Income)) {
			return notFoundError()
		}
		if err != nil {
			return err
		}
		if out, err = receiptView(row, canRefund); err != nil {
			return err
		}
		ref, err := q.GetCashRefundOf(ctx, store.GetCashRefundOfParams{ClubID: clubID, RefundOfID: &row.ID})
		switch {
		case err == nil:
			r := api.CashRefundRef{Id: ref.ID}
			if ref.CreatedAt != nil {
				r.CreatedAt = *ref.CreatedAt
			}
			out.Refund = &r
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return api.GetCashReceipt200JSONResponse(out), nil
}

// RefundCashReceipt — CashRegisterController::refund() (право cash.delete проверил withAdminAuth).
func (h *handlers) RefundCashReceipt(ctx context.Context, req api.RefundCashReceiptRequestObject) (api.RefundCashReceiptResponseObject, error) {
	clubID := h.clubID(ctx)
	var out api.RefundResult
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = idempotent(ctx, q, clubID, req.Params.IdempotencyKey, "refund", map[string]int64{"transaction_id": req.TransactionId}, h.now(),
			func() (api.RefundResult, error) {
				res, err := h.cash().Refund(ctx, q, cashsvc.RefundParams{ClubID: clubID, IncomeID: req.TransactionId, Actor: h.actor(ctx)})
				if err != nil {
					return api.RefundResult{}, err
				}
				tx, err := txView(res.Refund)
				if err != nil {
					return api.RefundResult{}, err
				}
				return api.RefundResult{
					Refund: tx, Kind: api.RefundResultKind(res.Kind),
					Message: substitute(msgRefundDone, map[string]string{"amount": numberFormat(money.Tiyn(res.Refund.Amount), ",")}),
				}, nil
			})
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.RefundCashReceipt200JSONResponse(out), nil
}
