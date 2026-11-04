package customers

// Клиенты клуба: список с поиском, карточка с балансами, история баланса из ledger, пополнение/списание
// владельцем без кассы, начальный баланс. Laravel: app/Http/Controllers/UserController.php.

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	customersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/customers"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты UserController (ключи перевода).
const (
	msgOwnerOnlyBalance  = "Пополнять и списывать баланс без кассы может только владелец клуба."
	msgMoneyByManager    = "Скидку и баланс клиента задаёт менеджер; пополнение — через кассу."
	msgSavedWrittenOff   = "Данные клиента сохранены. С баланса списано :amount без кассы."
	msgSavedToppedUp     = "Данные клиента сохранены. Баланс пополнен на :amount без кассы."
	msgOwnerPaymentLabel = "без кассы (владелец)"
	// maxInitialBalance — StoreUserRequest: balance / bonus_balance max:9999999999.99.
	maxInitialBalance = money.Tiyn(999_999_999_999)
)

var balanceLabel = map[billing.BalanceField]string{billing.FieldMain: "основной баланс", billing.FieldBonus: "бонусный баланс"}

// customer — клиент клуба (не сотрудник); нет — 404 (findOrFail членства в старом).
func customer(ctx context.Context, q *store.Queries, clubID, userID int64) (store.AdminGetCustomerRow, error) {
	row, err := q.AdminGetCustomer(ctx, store.AdminGetCustomerParams{ClubID: clubID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, httpx.NotFound()
	}
	return row, err
}

// ListCustomers — UserController::index(): клиенты (не сотрудники) по user_id, с поиском; keyset вперёд (after_id)
// и назад (before_id), всего — «Всего: :count».
func (h *handlers) ListCustomers(ctx context.Context, req customersapi.ListCustomersRequestObject) (customersapi.ListCustomersResponseObject, error) {
	var after, before int64
	if req.Params.AfterId != nil {
		after = *req.Params.AfterId
	}
	if req.Params.BeforeId != nil {
		before = *req.Params.BeforeId
	}
	if after > 0 && before > 0 {
		return nil, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
	}
	size := httpx.PageSize(req.Params.Limit)
	search := httpx.LikePattern(req.Params.Q)
	out := customersapi.CustomerPage{Items: []customersapi.Customer{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		var rows []customerRow
		if before > 0 {
			list, err := q.AdminListCustomersBefore(ctx, store.AdminListCustomersBeforeParams{
				ClubID: clubID, BeforeUserID: before, Search: search, PageSize: size + 1,
			})
			if err != nil {
				return err
			}
			more := len(list) > int(size)
			if more {
				list = list[:size]
			}
			for i := len(list) - 1; i >= 0; i-- {
				rows = append(rows, fromListBefore(list[i]))
			}
			if more {
				out.PrevBeforeId = httpx.Ptr(rows[0].UserID)
			}
			if len(rows) > 0 {
				out.NextAfterId = httpx.Ptr(rows[len(rows)-1].UserID)
			}
		} else {
			list, err := q.AdminListCustomers(ctx, store.AdminListCustomersParams{
				ClubID: clubID, AfterUserID: after, Search: search, PageSize: size + 1,
			})
			if err != nil {
				return err
			}
			more := len(list) > int(size)
			if more {
				list = list[:size]
			}
			for _, r := range list {
				rows = append(rows, fromList(r))
			}
			if more {
				out.NextAfterId = httpx.Ptr(rows[len(rows)-1].UserID)
			}
			if after > 0 && len(rows) > 0 {
				out.PrevBeforeId = httpx.Ptr(rows[0].UserID)
			}
		}
		total, err := q.AdminCountCustomers(ctx, store.AdminCountCustomersParams{ClubID: clubID, Search: search})
		if err != nil {
			return err
		}
		out.Total = total
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.UserID)
		}
		v, err := h.loadView(ctx, q, clubID, ids)
		if err != nil {
			return err
		}
		for _, r := range rows {
			out.Items = append(out.Items, v.customer(r))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return customersapi.ListCustomers200JSONResponse(out), nil
}

// view — клиент с лояльностью и признаком «сотрудник другого клуба».
func (h *handlers) view(ctx context.Context, q *store.Queries, clubID int64, row store.AdminGetCustomerRow) (customersapi.Customer, error) {
	v, err := h.loadView(ctx, q, clubID, []int64{row.UserID})
	if err != nil {
		return customersapi.Customer{}, err
	}
	return v.customer(fromGet(row)), nil
}

// GetCustomer — карточка клиента.
func (h *handlers) GetCustomer(ctx context.Context, req customersapi.GetCustomerRequestObject) (customersapi.GetCustomerResponseObject, error) {
	var out customersapi.Customer
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		row, err := customer(ctx, q, clubID, req.UserId)
		if err != nil {
			return err
		}
		out, err = h.view(ctx, q, clubID, row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return customersapi.GetCustomer200JSONResponse(out), nil
}

type ledgerCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

// ledgerFrom — нижняя граница истории (диапазоном, без функций над created_at).
var ledgerFrom = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ListCustomerLedger — история баланса клиента: проводки ledger по member_main/member_bonus, новые сверху.
func (h *handlers) ListCustomerLedger(ctx context.Context, req customersapi.ListCustomerLedgerRequestObject) (customersapi.ListCustomerLedgerResponseObject, error) {
	cur := ledgerCursor{At: h.d.NowFunc().Add(24 * time.Hour), ID: math.MaxInt64}
	if err := httpx.DecodeCursor(req.Params.Cursor, &cur); err != nil {
		return nil, err
	}
	size := httpx.PageSize(req.Params.Limit)
	out := customersapi.LedgerPage{Items: []customersapi.LedgerEntry{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		if _, err := customer(ctx, q, clubID, req.UserId); err != nil {
			return err
		}
		uid := req.UserId
		rows, err := q.ListMemberLedgerEntries(ctx, store.ListMemberLedgerEntriesParams{
			ClubID: clubID, UserID: &uid, FromCreatedAt: ledgerFrom, BeforeCreatedAt: cur.At, BeforeID: cur.ID, PageSize: size,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			e := customersapi.LedgerEntry{
				Id: r.ID, TransactionId: r.TransactionID, Kind: r.TransactionKind, Account: customersapi.LedgerEntryAccount(r.AccountKind),
				AmountTiyn: r.Amount, CreatedAt: r.CreatedAt,
			}
			if r.BalanceAfter != nil {
				e.BalanceAfterTiyn = *r.BalanceAfter
			}
			out.Items = append(out.Items, e)
		}
		if len(rows) == int(size) {
			last := rows[len(rows)-1]
			out.NextCursor = httpx.EncodeCursor(ledgerCursor{At: last.CreatedAt, ID: last.ID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return customersapi.ListCustomerLedger200JSONResponse(out), nil
}

// notifyBalance — balance.changed и уведомление агента после коммита.
func (h *handlers) notifyBalance(ctx context.Context, q *store.Queries, clubID, userID int64) (store.AdminGetCustomerRow, error) {
	row, err := customer(ctx, q, clubID, userID)
	if err != nil {
		return row, err
	}
	main, bonus := money.Tiyn(row.Balance), money.Tiyn(row.BonusBalance)
	if err := billingsvc.AddEvent(ctx, q, clubID, "balance.changed", billingsvc.BalanceEvent(userID, main, bonus)); err != nil {
		return row, err
	}
	if h.d.Cash != nil && h.d.Cash.Notifier != nil {
		n := h.d.Cash.Notifier
		billingsvc.AfterCommit(ctx, func(ctx context.Context) {
			billingsvc.NotifyBalance(ctx, n, clubID, userID, main, bonus, "")
		})
	}
	return row, nil
}

// ownerAdjustment — проверенные поля пополнения/списания без кассы (top_up_amount, top_up_balance, balance_action).
type ownerAdjustment struct {
	field    billing.BalanceField
	writeOff bool
	amount   money.Tiyn
}

// parseOwnerAdjustment — правила UserController::update() для top_up_amount (numeric|min:0.01|max:10000000),
// top_up_balance (in:balance,bonus_balance), balance_action (in:top_up,write_off). Право (owner) — у вызывающего.
func parseOwnerAdjustment(f *httpx.FormErrors, amountTiyn int64, balanceType, action string) ownerAdjustment {
	out := ownerAdjustment{field: billing.BalanceField(balanceType), writeOff: action == "write_off"}
	amount, err := billing.ParseOwnerAdjustmentAmount(money.Tiyn(amountTiyn).Tenge())
	switch {
	case errors.Is(err, billing.ErrBalanceAmountDecimals): // Решение Q-002 (docs/DECISIONS.md)
		f.AddErr(httpx.RuleError("top_up_amount", cash.RuleDecimal))
	case err != nil && amountTiyn < 1:
		f.AddErr(httpx.RuleError("top_up_amount", cash.RuleMin))
	case err != nil:
		f.AddErr(httpx.RuleError("top_up_amount", cash.RuleMax))
	}
	out.amount = amount
	if !out.field.Valid() {
		f.AddErr(httpx.RuleError("top_up_balance", cash.RuleIn))
	}
	if action != "top_up" && action != "write_off" {
		f.AddErr(httpx.RuleError("balance_action", cash.RuleIn))
	}
	return out
}

// ownerAdjust — пополнение/списание без кассы в транзакции q (ledger + журнал user.balance_topped_up /
// user.balance_written_off). Возвращает ключ сообщения. Laravel: UserController::update() (стр. 135–157).
func (h *handlers) ownerAdjust(ctx context.Context, q *store.Queries, clubID int64, c store.AdminGetCustomerRow, a ownerAdjustment, key string) (string, error) {
	actor := httpx.Actor(ctx)
	res, err := billing.OwnerAdjust(ctx, q, billing.OwnerAdjustParams{
		ClubID: clubID, UserID: c.UserID, Field: a.field, WriteOff: a.writeOff, Amount: a.amount,
		IdempotencyKey: key, Actor: actor,
	})
	if err != nil {
		return "", err
	}
	label := h.d.T(ctx, balanceLabel[a.field], nil)
	params := map[string]string{"balance": label, "user": c.Name}
	e := activity.Entry{
		ClubID: clubID, UserID: actor.UserID, SubjectType: billingsvc.SubjectUser, SubjectID: c.UserID,
		Event: "user.balance_topped_up", Description: h.d.T(ctx, "Пополнение :balance без кассы: :user", params),
		Details: map[string]any{
			"amount": billingsvc.PHPFloat(a.amount), "balance_type": label, "payment_method": h.d.T(ctx, msgOwnerPaymentLabel, nil),
			"new_balance": billingsvc.PHPFloat(res.NewBalance), "without_cash": true,
		},
	}
	msg := msgSavedToppedUp
	if a.writeOff {
		e.Event, e.Description = "user.balance_written_off", h.d.T(ctx, "Списание :balance без кассы: :user", params)
		e.Details = map[string]any{
			"amount": billingsvc.PHPFloat(a.amount), "balance_field": string(a.field), "balance_type": label,
			"new_balance": billingsvc.PHPFloat(res.NewBalance), "without_cash": true,
		}
		msg = msgSavedWrittenOff
	}
	if err := billingsvc.RecordActivity(ctx, q, e); err != nil {
		return "", err
	}
	return msg, nil
}

// AdjustCustomerBalance — UserController::update(): top_up_amount / top_up_balance / balance_action (только владелец).
func (h *handlers) AdjustCustomerBalance(ctx context.Context, req customersapi.AdjustCustomerBalanceRequestObject) (customersapi.AdjustCustomerBalanceResponseObject, error) {
	b := req.Body
	if httpx.Principal(ctx).Role != permissions.RoleOwner {
		// 'top_up_amount' => prohibited (canTopUpWithoutCash() — только роль owner).
		return nil, httpx.Validation("top_up_amount", msgOwnerOnlyBalance)
	}
	var form httpx.FormErrors
	adj := parseOwnerAdjustment(&form, b.AmountTiyn, b.BalanceType, b.Action)
	if err := form.Err(); err != nil {
		return nil, err
	}
	clubID, key := httpx.ClubID(ctx), req.Params.IdempotencyKey
	var out customersapi.BalanceAdjustmentResult
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = httpx.Idempotent(ctx, q, clubID, key, "owner_adjust", map[string]any{"user_id": req.UserId, "body": b}, h.d.NowFunc(),
			func() (customersapi.BalanceAdjustmentResult, error) {
				c, err := customer(ctx, q, clubID, req.UserId)
				if err != nil {
					return customersapi.BalanceAdjustmentResult{}, err
				}
				msg, err := h.ownerAdjust(ctx, q, clubID, c, adj, key)
				if err != nil {
					return customersapi.BalanceAdjustmentResult{}, err
				}
				row, err := h.notifyBalance(ctx, q, clubID, req.UserId)
				if err != nil {
					return customersapi.BalanceAdjustmentResult{}, err
				}
				view, err := h.view(ctx, q, clubID, row)
				if err != nil {
					return customersapi.BalanceAdjustmentResult{}, err
				}
				return customersapi.BalanceAdjustmentResult{
					Customer: view, Message: h.d.T(ctx, msg, map[string]string{"amount": httpx.NumberFormat(adj.amount, ".")}),
				}, nil
			})
		return err
	})
	if err != nil {
		return nil, err
	}
	return customersapi.AdjustCustomerBalance200JSONResponse(out), nil
}

// checkInitialBalance — StoreUserRequest: balance / bonus_balance nullable|numeric|min:0|max:9999999999.99.
func checkInitialBalance(f *httpx.FormErrors, field string, v int64) {
	if v < 0 {
		f.AddErr(httpx.RuleError(field, cash.RuleMin))
	} else if money.Tiyn(v) > maxInitialBalance {
		f.AddErr(httpx.RuleError(field, cash.RuleMax))
	}
}

// SetCustomerInitialBalance — UserController::store(): начальные balance / bonus_balance (право customer_money.update).
func (h *handlers) SetCustomerInitialBalance(ctx context.Context, req customersapi.SetCustomerInitialBalanceRequestObject) (customersapi.SetCustomerInitialBalanceResponseObject, error) {
	b := req.Body
	if !httpx.Principal(ctx).Can("customer_money.update") && (b.BalanceTiyn != 0 || b.BonusBalanceTiyn != 0) {
		return nil, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, msgMoneyByManager)
	}
	var form httpx.FormErrors
	checkInitialBalance(&form, "balance", b.BalanceTiyn)
	checkInitialBalance(&form, "bonus_balance", b.BonusBalanceTiyn)
	if err := form.Err(); err != nil {
		return nil, err
	}
	clubID, key := httpx.ClubID(ctx), req.Params.IdempotencyKey
	var out customersapi.Customer
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = httpx.Idempotent(ctx, q, clubID, key, "initial_balance", map[string]any{"user_id": req.UserId, "body": b}, h.d.NowFunc(),
			func() (customersapi.Customer, error) {
				if _, err := customer(ctx, q, clubID, req.UserId); err != nil {
					return customersapi.Customer{}, err
				}
				if _, err := billing.SetInitialBalance(ctx, q, billing.InitialBalanceParams{
					ClubID: clubID, UserID: req.UserId, Main: money.Tiyn(b.BalanceTiyn), Bonus: money.Tiyn(b.BonusBalanceTiyn),
					Actor: httpx.Actor(ctx),
				}); err != nil {
					return customersapi.Customer{}, err
				}
				row, err := h.notifyBalance(ctx, q, clubID, req.UserId)
				if err != nil {
					return customersapi.Customer{}, err
				}
				return h.view(ctx, q, clubID, row)
			})
		return err
	})
	if err != nil {
		return nil, err
	}
	return customersapi.SetCustomerInitialBalance200JSONResponse(out), nil
}
