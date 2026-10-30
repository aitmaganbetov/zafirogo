package httpserver

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
	"github.com/aitmaganbetov/zafirogo/server/internal/http/api"
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

func customerView(r store.AdminGetCustomerRow) api.Customer {
	return api.Customer{
		UserId: r.UserID, Name: r.Name, Phone: r.Phone, Role: r.Role, Discount: r.Discount, TotalSpentTiyn: r.TotalSpent,
		BalanceTiyn: r.Balance, BonusBalanceTiyn: r.BonusBalance,
	}
}

// customer — клиент клуба (не сотрудник); нет — 404 (findOrFail членства в старом).
func customer(ctx context.Context, q *store.Queries, clubID, userID int64) (store.AdminGetCustomerRow, error) {
	row, err := q.AdminGetCustomer(ctx, store.AdminGetCustomerParams{ClubID: clubID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, notFoundError()
	}
	return row, err
}

// ListCustomers — UserController::index(): клиенты (не сотрудники) по user_id, с поиском.
func (h *handlers) ListCustomers(ctx context.Context, req api.ListCustomersRequestObject) (api.ListCustomersResponseObject, error) {
	var after int64
	if req.Params.AfterId != nil {
		after = *req.Params.AfterId
	}
	size := pageSize(req.Params.Limit)
	out := api.CustomerPage{Items: []api.Customer{}}
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := q.AdminListCustomers(ctx, store.AdminListCustomersParams{
			ClubID: h.clubID(ctx), AfterUserID: after, Search: likePattern(req.Params.Q), PageSize: size,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out.Items = append(out.Items, customerView(store.AdminGetCustomerRow(r)))
		}
		if len(rows) == int(size) {
			out.NextAfterId = ptr(rows[len(rows)-1].UserID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return api.ListCustomers200JSONResponse(out), nil
}

// GetCustomer — карточка клиента.
func (h *handlers) GetCustomer(ctx context.Context, req api.GetCustomerRequestObject) (api.GetCustomerResponseObject, error) {
	var out api.Customer
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		row, err := customer(ctx, q, h.clubID(ctx), req.UserId)
		out = customerView(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.GetCustomer200JSONResponse(out), nil
}

type ledgerCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

// ledgerFrom — нижняя граница истории (диапазоном, без функций над created_at).
var ledgerFrom = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ListCustomerLedger — история баланса клиента: проводки ledger по member_main/member_bonus, новые сверху.
func (h *handlers) ListCustomerLedger(ctx context.Context, req api.ListCustomerLedgerRequestObject) (api.ListCustomerLedgerResponseObject, error) {
	cur := ledgerCursor{At: h.now().Add(24 * time.Hour), ID: math.MaxInt64}
	if err := decodeCursor(req.Params.Cursor, &cur); err != nil {
		return nil, err
	}
	size := pageSize(req.Params.Limit)
	out := api.LedgerPage{Items: []api.LedgerEntry{}}
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := h.clubID(ctx)
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
			e := api.LedgerEntry{
				Id: r.ID, TransactionId: r.TransactionID, Kind: r.TransactionKind, Account: api.LedgerEntryAccount(r.AccountKind),
				AmountTiyn: r.Amount, CreatedAt: r.CreatedAt,
			}
			if r.BalanceAfter != nil {
				e.BalanceAfterTiyn = *r.BalanceAfter
			}
			out.Items = append(out.Items, e)
		}
		if len(rows) == int(size) {
			last := rows[len(rows)-1]
			out.NextCursor = encodeCursor(ledgerCursor{At: last.CreatedAt, ID: last.ID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return api.ListCustomerLedger200JSONResponse(out), nil
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
	if h.opts.Cash != nil && h.opts.Cash.Notifier != nil {
		n := h.opts.Cash.Notifier
		billingsvc.AfterCommit(ctx, func(ctx context.Context) {
			billingsvc.NotifyBalance(ctx, n, clubID, userID, main, bonus, "")
		})
	}
	return row, nil
}

// AdjustCustomerBalance — UserController::update(): top_up_amount / top_up_balance / balance_action (только владелец).
func (h *handlers) AdjustCustomerBalance(ctx context.Context, req api.AdjustCustomerBalanceRequestObject) (api.AdjustCustomerBalanceResponseObject, error) {
	b := req.Body
	if h.principal(ctx).Role != permissions.RoleOwner {
		// 'top_up_amount' => prohibited (canTopUpWithoutCash() — только роль owner).
		return nil, validation("top_up_amount", msgOwnerOnlyBalance)
	}
	field := billing.BalanceField(b.BalanceType)
	if !field.Valid() {
		return nil, ruleError("top_up_balance", cash.RuleIn)
	}
	if b.Action != "top_up" && b.Action != "write_off" {
		return nil, ruleError("balance_action", cash.RuleIn)
	}
	amount, err := billing.ParseOwnerAdjustmentAmount(money.Tiyn(b.AmountTiyn).Tenge())
	if err != nil {
		if b.AmountTiyn < 1 {
			return nil, ruleError("top_up_amount", cash.RuleMin)
		}
		return nil, ruleError("top_up_amount", cash.RuleMax)
	}
	writeOff := b.Action == "write_off"
	clubID, key := h.clubID(ctx), req.Params.IdempotencyKey
	var out api.BalanceAdjustmentResult
	err = h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = idempotent(ctx, q, clubID, key, "owner_adjust", map[string]any{"user_id": req.UserId, "body": b}, h.now(),
			func() (api.BalanceAdjustmentResult, error) {
				c, err := customer(ctx, q, clubID, req.UserId)
				if err != nil {
					return api.BalanceAdjustmentResult{}, err
				}
				actor := h.actor(ctx)
				res, err := billing.OwnerAdjust(ctx, q, billing.OwnerAdjustParams{
					ClubID: clubID, UserID: req.UserId, Field: field, WriteOff: writeOff, Amount: amount,
					IdempotencyKey: key, Actor: actor,
				})
				if err != nil {
					return api.BalanceAdjustmentResult{}, err
				}
				// Laravel: UserController::update() (стр. 135–157) — журнал user.balance_written_off / user.balance_topped_up.
				label := balanceLabel[field]
				e := activity.Entry{
					ClubID: clubID, UserID: actor.UserID, SubjectType: billingsvc.SubjectUser, SubjectID: req.UserId,
					Event: "user.balance_topped_up", Description: "Пополнение " + label + " без кассы: " + c.Name,
					Details: map[string]any{
						"amount": billingsvc.PHPFloat(amount), "balance_type": label, "payment_method": msgOwnerPaymentLabel,
						"new_balance": billingsvc.PHPFloat(res.NewBalance), "without_cash": true,
					},
				}
				msg := msgSavedToppedUp
				if writeOff {
					e.Event, e.Description = "user.balance_written_off", "Списание "+label+" без кассы: "+c.Name
					e.Details = map[string]any{
						"amount": billingsvc.PHPFloat(amount), "balance_field": string(field), "balance_type": label,
						"new_balance": billingsvc.PHPFloat(res.NewBalance), "without_cash": true,
					}
					msg = msgSavedWrittenOff
				}
				if err := billingsvc.RecordActivity(ctx, q, e); err != nil {
					return api.BalanceAdjustmentResult{}, err
				}
				row, err := h.notifyBalance(ctx, q, clubID, req.UserId)
				if err != nil {
					return api.BalanceAdjustmentResult{}, err
				}
				return api.BalanceAdjustmentResult{
					Customer: customerView(row), Message: substitute(msg, map[string]string{"amount": numberFormat(amount, ".")}),
				}, nil
			})
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.AdjustCustomerBalance200JSONResponse(out), nil
}

// SetCustomerInitialBalance — UserController::store(): начальные balance / bonus_balance (право customer_money.update).
func (h *handlers) SetCustomerInitialBalance(ctx context.Context, req api.SetCustomerInitialBalanceRequestObject) (api.SetCustomerInitialBalanceResponseObject, error) {
	b := req.Body
	if !h.principal(ctx).Can("customer_money.update") && (b.BalanceTiyn != 0 || b.BonusBalanceTiyn != 0) {
		return nil, newAPIError(http.StatusForbidden, permissions.CodeForbidden, msgMoneyByManager)
	}
	for _, f := range []struct {
		name string
		v    int64
	}{{"balance", b.BalanceTiyn}, {"bonus_balance", b.BonusBalanceTiyn}} {
		if f.v < 0 {
			return nil, ruleError(f.name, cash.RuleMin)
		}
		if money.Tiyn(f.v) > maxInitialBalance {
			return nil, ruleError(f.name, cash.RuleMax)
		}
	}
	clubID, key := h.clubID(ctx), req.Params.IdempotencyKey
	var out api.Customer
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		out, err = idempotent(ctx, q, clubID, key, "initial_balance", map[string]any{"user_id": req.UserId, "body": b}, h.now(),
			func() (api.Customer, error) {
				if _, err := customer(ctx, q, clubID, req.UserId); err != nil {
					return api.Customer{}, err
				}
				if _, err := billing.SetInitialBalance(ctx, q, billing.InitialBalanceParams{
					ClubID: clubID, UserID: req.UserId, Main: money.Tiyn(b.BalanceTiyn), Bonus: money.Tiyn(b.BonusBalanceTiyn),
					Actor: h.actor(ctx),
				}); err != nil {
					return api.Customer{}, err
				}
				row, err := h.notifyBalance(ctx, q, clubID, req.UserId)
				return customerView(row), err
			})
		return err
	})
	if err != nil {
		return nil, err
	}
	return api.SetCustomerInitialBalance200JSONResponse(out), nil
}
