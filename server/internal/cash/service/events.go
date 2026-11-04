package service

// Журнал действий (activity_logs), события клуба и вебхуки (outbox) кассы и склада — в транзакции операции,
// как ActivityLogger::record() и WebhookDispatcher::dispatch() внутри DB::transaction() старого кода
// (app/Services/CashRegisterService.php, app/Services/GuestSessions.php). Уведомление агента о балансе —
// после коммита (billingsvc.AfterCommit: транзакцией владеет вызывающий).

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Подписи балансов и способов оплаты (ключи перевода, как в старом коде).
// Laravel: CashRegisterService::topUpBalance() — $balanceLabel, $paymentLabels.
var (
	balanceLabels = map[cash.BalanceField]string{cash.FieldMain: "основной баланс", cash.FieldBonus: "бонусный баланс"}
	paymentLabels = map[cash.PaymentMethod]string{cash.PayCash: "наличные", cash.PayCard: "карта", cash.PayQR: "QR-код"}
)

// Виды кассовой операции в событии cash.transaction (docs/realtime.md).
const (
	txSale         = "sale"
	txTopUp        = "top_up"
	txRefund       = "refund"
	txIncome       = "income"
	txExpense      = "expense"
	txGuestSession = "guest_session"
)

func record(ctx context.Context, q *store.Queries, e activity.Entry) error {
	return billingsvc.RecordActivity(ctx, q, e)
}

func event(ctx context.Context, q *store.Queries, clubID int64, typ string, data any) error {
	return billingsvc.AddEvent(ctx, q, clubID, typ, data)
}

// cashTransactionEvent — cash.transaction {transaction_id, kind, amount (тиыны)}.
func cashTransactionEvent(ctx context.Context, q *store.Queries, clubID int64, row store.CashTransaction, kind string) error {
	return event(ctx, q, clubID, "cash.transaction", map[string]any{"transaction_id": row.ID, "kind": kind, "amount": row.Amount})
}

// shiftEvent — cash.shift_changed {shift_id, action}.
func shiftEvent(ctx context.Context, q *store.Queries, clubID, shiftID int64, action string) error {
	return event(ctx, q, clubID, "cash.shift_changed", map[string]any{"shift_id": shiftID, "action": action})
}

// stockMoved — журнал stock.written_off/stock.received и вебхук stock.changed по одному движению товара.
// Laravel: CashRegisterService::sale() (стр. 61–62), returnToStock() (стр. 225–226).
func stockMoved(ctx context.Context, q *store.Queries, clubID, userID int64, product store.Product, mv store.StockMovement, sale bool) error {
	qty := mv.Quantity
	e := activity.Entry{
		ClubID: clubID, UserID: userID, SubjectType: billingsvc.SubjectProduct, SubjectID: product.ID,
		Details: map[string]any{"quantity": qty, "balance_after": mv.BalanceAfter, "note": cash.RefundNote},
	}
	change, reason := qty, "refund"
	if sale {
		e.Event, e.Description = "stock.written_off", "Списание при продаже: "+product.Name+", "+strconv.FormatInt(qty, 10)+" шт."
		e.Details["note"] = cash.SaleNote
		change, reason = -qty, "sale"
	} else {
		e.Event, e.Description = "stock.received", "Возврат товара на склад: "+product.Name+", "+strconv.FormatInt(qty, 10)+" шт."
	}
	if err := record(ctx, q, e); err != nil {
		return err
	}
	return event(ctx, q, clubID, "stock.changed", map[string]any{
		"product_id": product.ID, "sku": product.Sku, "change": change, "quantity": mv.BalanceAfter, "reason": reason,
	})
}

// receiptItems — receipt_data.items в виде вебхука sale.completed старого кода: price/total — float PHP
// (json_encode), плюс суммы в тиынах (*_tiyn — новое, open-questions/stage2-rest.md RQ-3).
func receiptItems(lines []cash.ReceiptLine) []map[string]any {
	out := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		item := map[string]any{
			"name": l.Name, "quantity": l.Quantity,
			"price": billingsvc.PHPFloat(l.Price), "total": billingsvc.PHPFloat(l.Total),
			"price_tiyn": int64(l.Price), "total_tiyn": int64(l.Total),
		}
		if l.ServiceID != 0 {
			item["service_id"] = l.ServiceID
		} else {
			item["product_id"] = l.ProductID
		}
		out = append(out, item)
	}
	return out
}

// itemsSummary — «name × qty, …» (product.purchased).
func itemsSummary(lines []cash.ReceiptLine) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = l.Name + " × " + strconv.FormatInt(l.Quantity, 10)
	}
	return strings.Join(parts, ", ")
}

// phpFloatOf — (float) процент в details (bonus_percent): json_encode float PHP.
func phpFloatOf(f float64) json.Number {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return json.Number(s)
}

// balanceChanged — событие balance.changed с текущими балансами клиента и уведомление агента после коммита.
func (s *Service) balanceChanged(ctx context.Context, q *store.Queries, clubID, userID int64) error {
	mb, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: clubID, UserID: userID})
	if err != nil {
		return err
	}
	main, bonus := money.Tiyn(mb.Balance), money.Tiyn(mb.BonusBalance)
	if err := event(ctx, q, clubID, "balance.changed", billingsvc.BalanceEvent(userID, main, bonus)); err != nil {
		return err
	}
	if s != nil && s.Notifier != nil {
		n := s.Notifier
		currency := ""
		if raw, err := q.GetClubSettingsForCash(ctx, clubID); err == nil {
			currency = currencyOf(raw)
		}
		billingsvc.AfterCommit(ctx, func(ctx context.Context) {
			billingsvc.NotifyBalance(ctx, n, clubID, userID, main, bonus, currency)
		})
	}
	return nil
}

// currencyOf — data_get(settings, 'finances.currency', 'KZT').
func currencyOf(settings []byte) string {
	var root struct {
		Finances struct {
			Currency any `json:"currency"`
		} `json:"finances"`
	}
	if json.Unmarshal(settings, &root) == nil {
		if c, ok := root.Finances.Currency.(string); ok && c != "" {
			return c
		}
	}
	return "KZT"
}
