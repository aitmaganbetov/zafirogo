package shop

// Выдача и отмена заказа сотрудником (BR-076, BR-077): панель (shop-orders) и, позже, приложение сотрудника.
// Функции работают в транзакции клуба вызывающего (q — его Queries), как пакет stock.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Статусы заказа. Laravel: app/Models/ShopOrder.php STATUS_*.
const (
	StatusNew       = "new"
	StatusDone      = "done"
	StatusCancelled = "cancelled" //nolint:misspell // значение shop_orders.status старой схемы
)

// Тексты старого кода.
const (
	MsgOrderHandled = "Заказ уже обработан."    // AgentShop::lockNew(), поле order
	CancelNote      = "Отмена заказа из агента" // AgentShop::CANCEL_NOTE (маркер движения склада)
)

// Морф-классы предметов журнала (PD-14).
const (
	subjectShopOrder = `App\Models\ShopOrder`
	subjectUser      = `App\Models\User`
)

// ErrOrderHandled — заказ уже выдан или отменён (422 на поле order).
var ErrOrderHandled = &Error{Code: "order_handled", Field: "order", Message: MsgOrderHandled}

// ErrOrderNotFound — заказа нет в клубе (findOrFail → 404).
var ErrOrderNotFound = errors.New("shop: order not found")

// HandleParams — выдача или отмена заказа сотрудником.
type HandleParams struct {
	ClubID  int64
	OrderID int64
	Actor   billing.BalanceActor // сотрудник (handled_by)
	Now     time.Time
}

// Handled — заказ после выдачи/отмены.
type Handled struct {
	OrderID      int64
	UserID       int64
	Status       string
	ComputerName string
	Amount       money.Tiyn
	Lines        []Line
	HandledAt    time.Time
	NewCount     int64
}

func lockNew(ctx context.Context, q *store.Queries, clubID, orderID int64) (store.ShopLockOrderRow, error) {
	o, err := q.ShopLockOrder(ctx, store.ShopLockOrderParams{ClubID: clubID, ID: orderID})
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrOrderNotFound
	}
	if err != nil {
		return o, fmt.Errorf("shop: lock order: %w", err)
	}
	if o.Status != StatusNew {
		return o, ErrOrderHandled
	}
	return o, nil
}

func handledOf(o store.ShopLockOrderRow, status string, lines []Line, now time.Time) Handled {
	h := Handled{OrderID: o.ID, UserID: o.UserID, Status: status, Amount: money.Tiyn(o.Amount), Lines: lines, HandledAt: now}
	if o.ComputerName != nil {
		h.ComputerName = *o.ComputerName
	}
	return h
}

func finish(ctx context.Context, q *store.Queries, p HandleParams, h *Handled) error {
	staff := p.Actor.UserID
	var by *int64
	if staff > 0 {
		by = &staff
	}
	if err := q.ShopSetOrderHandled(ctx, store.ShopSetOrderHandledParams{
		ClubID: p.ClubID, ID: p.OrderID, Status: h.Status, HandledBy: by, Now: &h.HandledAt,
	}); err != nil {
		return fmt.Errorf("shop: set order status: %w", err)
	}
	n, err := q.ShopCountNewOrders(ctx, p.ClubID)
	if err != nil {
		return err
	}
	h.NewCount = n
	_, err = outbox.Add(ctx, q, outbox.Event{ClubID: p.ClubID, Type: "shop_order.updated", Data: map[string]any{
		"order_id": p.OrderID, "status": h.Status, "new_count": n,
	}})
	return err
}

func recordActivity(ctx context.Context, q *store.Queries, e activity.Entry) error {
	if e.UserID <= 0 {
		return nil
	}
	if _, ok := activity.RequestMetaFromContext(ctx); !ok {
		return nil // без данных запроса журнал не пишется (как billing/service.RecordActivity)
	}
	_, err := activity.Record(ctx, q, e)
	return err
}

// HandOver — заказ отнесли к ПК (BR-076).
//
// Laravel: app/Services/AgentShop.php handOver() (стр. 111–120): lockNew (FOR UPDATE, не new → «Заказ уже обработан.»);
// status=done, handled_by, handled_at=now; журнал shop.order_done «Заказ №:id выдан на ПК :computer» {amount}.
func HandOver(ctx context.Context, q *store.Queries, p HandleParams) (Handled, error) {
	o, err := lockNew(ctx, q, p.ClubID, p.OrderID)
	if err != nil {
		return Handled{}, err
	}
	lines, err := DecodeItems(o.Items)
	if err != nil {
		return Handled{}, err
	}
	h := handledOf(o, StatusDone, lines, p.Now)
	if err := finish(ctx, q, p, &h); err != nil {
		return Handled{}, err
	}
	computer := h.ComputerName
	if o.ComputerName == nil {
		computer = "—" // $order->computer_name ?? '—'
	}
	return h, recordActivity(ctx, q, activity.Entry{
		ClubID: p.ClubID, Event: "shop.order_done", UserID: p.Actor.UserID, At: p.Now,
		Description: "Заказ №" + strconv.FormatInt(o.ID, 10) + " выдан на ПК " + computer,
		SubjectType: subjectShopOrder, SubjectID: o.ID, Details: map[string]any{"amount": formatCents(h.Amount)},
	})
}

// Cancel — заказ не может быть выполнен: деньги — на основной баланс клиента, товар — на склад (BR-077).
//
// Laravel: app/Services/AgentShop.php cancel() (стр. 123–153):
//
//	lockNew; строки с product_id и quantity; существующие (не удалённые) товары FOR UPDATE по id:
//	  quantity += qty; stock_movements {receipt, note 'Отмена заказа из агента'}; вебхук stock.changed {reason: receipt}
//	balance клиента FOR UPDATE: есть → balance += amount
//	LoyaltyProgram::subtractSpending(user, amount); status=отменён, handled_by, handled_at
//	журнал «заказ отменён» (subject = клиент) «Заказ №:id отменён, :amount возвращено на счёт клиента» {amount, order_id}
//
// Деньги — проводка ledger shop_order_cancel (revenue_shop → member_main) с ключом "shop_order_cancel:<id>" (повтор
// не зачислит дважды). Нет строки баланса — она создаётся и сумма зачисляется (решение Q-012: в старом деньги терялись).
func Cancel(ctx context.Context, q *store.Queries, p HandleParams) (Handled, error) {
	o, err := lockNew(ctx, q, p.ClubID, p.OrderID)
	if err != nil {
		return Handled{}, err
	}
	lines, err := DecodeItems(o.Items)
	if err != nil {
		return Handled{}, err
	}
	h := handledOf(o, StatusCancelled, lines, p.Now)

	if err := restock(ctx, q, p, lines); err != nil {
		return Handled{}, err
	}
	if h.Amount > 0 {
		bal, err := credit(ctx, q, p, o)
		if err != nil {
			return Handled{}, err
		}
		if err := subtractSpending(ctx, q, p.ClubID, o.UserID, h.Amount, p.Now); err != nil {
			return Handled{}, err
		}
		if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: p.ClubID, Type: "balance.changed", Data: map[string]any{
			"user_id": o.UserID, "balance": int64(bal.NewBalance), "bonus_balance": int64(bal.NewBonusBalance),
		}}); err != nil {
			return Handled{}, err
		}
	}
	if err := finish(ctx, q, p, &h); err != nil {
		return Handled{}, err
	}
	amount := formatCents(h.Amount)
	return h, recordActivity(ctx, q, activity.Entry{
		ClubID: p.ClubID, Event: "shop.order_cancelled", UserID: p.Actor.UserID, At: p.Now, //nolint:misspell // код события старого журнала
		Description: "Заказ №" + strconv.FormatInt(o.ID, 10) + " отменён, " + amount + " возвращено на счёт клиента",
		SubjectType: subjectUser, SubjectID: o.UserID, Details: map[string]any{"amount": amount, "order_id": o.ID},
	})
}

// restock — товары заказа обратно на склад (существующие, не удалённые; в порядке строк заказа).
func restock(ctx context.Context, q *store.Queries, p HandleParams, lines []Line) error {
	var ids []int64
	for _, l := range lines {
		if l.ProductID > 0 && l.Quantity > 0 && !slices.Contains(ids, l.ProductID) {
			ids = append(ids, l.ProductID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	rows, err := q.ShopLockOrderProducts(ctx, store.ShopLockOrderProductsParams{ClubID: p.ClubID, Ids: ids})
	if err != nil {
		return fmt.Errorf("shop: lock products: %w", err)
	}
	type prod struct {
		sku string
		qty int64
	}
	products := make(map[int64]*prod, len(rows))
	for _, r := range rows {
		products[r.ID] = &prod{sku: r.Sku, qty: r.Quantity}
	}
	for _, l := range lines {
		pr, ok := products[l.ProductID]
		if !ok || l.Quantity <= 0 {
			continue // товар удалён — его остаток не возвращается (как в старом)
		}
		pr.qty += l.Quantity
		m := stock.Movement{ProductID: l.ProductID, Type: stock.Receipt, Quantity: l.Quantity, BalanceAfter: pr.qty, Note: CancelNote, Change: l.Quantity}
		if _, err := stock.ApplyMovement(ctx, q, p.ClubID, p.Actor.UserID, m, p.Now); err != nil {
			return err
		}
		if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: p.ClubID, Type: "stock.changed", Data: map[string]any{
			"product_id": l.ProductID, "sku": pr.sku, "change": l.Quantity, "quantity": pr.qty, "reason": "receipt",
		}}); err != nil {
			return err
		}
	}
	return nil
}

// credit — возврат суммы заказа на основной баланс (ledger shop_order_cancel).
func credit(ctx context.Context, q *store.Queries, p HandleParams, o store.ShopLockOrderRow) (billing.ChargeResult, error) {
	if _, err := ledger.LockBalances(ctx, q, p.ClubID, o.UserID); errors.Is(err, ledger.ErrBalanceNotFound) {
		if err := q.EnsureMemberBalance(ctx, store.EnsureMemberBalanceParams{ClubID: p.ClubID, UserID: o.UserID}); err != nil {
			return billing.ChargeResult{}, fmt.Errorf("shop: ensure balance: %w", err)
		}
	} else if err != nil {
		return billing.ChargeResult{}, err
	}
	meta, err := json.Marshal(map[string]any{"order_id": o.ID})
	if err != nil {
		return billing.ChargeResult{}, err
	}
	return billing.Credit(ctx, q, billing.ChargeParams{
		ClubID: p.ClubID, UserID: o.UserID, Kind: ledger.KindShopOrderCancel, Revenue: ledger.AccountRevenueShop,
		Main: money.Tiyn(o.Amount), IdempotencyKey: "shop_order_cancel:" + strconv.FormatInt(o.ID, 10),
		Refs: ledger.Refs{ShopOrderID: o.ID}, Meta: meta, Actor: p.Actor,
	})
}

// subtractSpending — LoyaltyProgram::subtractSpending(): траты уменьшаются, но не ниже нуля; нет членства — ничего.
func subtractSpending(ctx context.Context, q *store.Queries, clubID, userID int64, cents money.Tiyn, now time.Time) error {
	m, err := q.LockMembershipForPurchase(ctx, store.LockMembershipForPurchaseParams{ClubID: clubID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	next := bonus.SubtractSpending(money.Tiyn(m.TotalSpent), cents)
	return q.SetTotalSpent(ctx, store.SetTotalSpentParams{ClubID: clubID, UserID: userID, TotalSpent: int64(next), Now: &now})
}
