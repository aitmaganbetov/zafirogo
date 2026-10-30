// Package shop — магазин клуба в агенте ПК: витрина товаров и заказ к ПК с оплатой с основного баланса (BR-075).
//
// Laravel: app/Services/AgentShop.php purchase() (стр. 37–108), app/Http/Controllers/Api/GuestShopController.php,
// app/Http/Controllers/Api/ProductController.php (витрина, paginate(50)).
//
// Отличия от старого (только слой хранения, суммы те же):
//   - деньги — проводка ledger shop_order (member_main → revenue_shop) с ключом идемпотентности, а не
//     update(['balance']) (CLAUDE.md «Деньги»);
//   - повтор заказа с тем же ключом возвращает первый ответ (billing_requests, операция shop_order);
//   - журнал (shop.purchased), события клуба (stock.changed, shop_order.created, balance.changed) — в той же
//     транзакции (activity / outbox).
//
// Выдача и отмена заказа сотрудником (BR-076, BR-077) — панель/приложение сотрудника, не этот пакет.
package shop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Пределы заказа. Laravel: AgentShop::MAX_LINES, AgentShop::MAX_QUANTITY.
const (
	MaxLines    = 30
	MaxQuantity = 50
	// PageSize — товаров на странице витрины (Api\ProductController: paginate(50)).
	PageSize = 50
	// MaxPageSize — верхняя граница page_size в контракте агента (shop.proto).
	MaxPageSize = 100
)

// Тексты старого кода (ключи перевода) и пометки движений склада (маркеры, не переводятся).
const (
	MsgProductGone    = "Товар больше не продаётся. Обновите магазин." // AgentShop::purchase() стр. 46
	MsgNotEnoughStock = "Недостаточно товара: :product."               // стр. 54
	MsgNotEnoughMain  = "Недостаточно средств на основном счёте."      // стр. 70
	MsgOrdered        = "Заказ оплачен. Сотрудник клуба принесёт его к ПК :computer."
	MsgShopDisabled   = "Магазин в агенте отключён в клубе." // GuestShopController::purchase()
	StockNote         = "Заказ из агента"                    // AgentShop::STOCK_NOTE
)

// operation — billing_requests.operation заказа.
const operation = "shop_order"

// MaxIdempotencyKeyLen — billing_requests.idempotency_key varchar(200).
const MaxIdempotencyKeyLen = 200

// MaxTotalSpent — club_memberships.total_spent в старой схеме decimal(12,2) (как billing/service.MaxTotalSpent).
const MaxTotalSpent money.Tiyn = 999_999_999_999

// subjectComputer — морф-класс предмета журнала shop.purchased (ПК).
const subjectComputer = `App\Models\ClubComputer`

// Error — ошибка проверки с полем запроса и текстом-ключом перевода (ValidationException::withMessages).
type Error struct {
	Code    string
	Field   string
	Message string
	Params  map[string]string
}

func (e *Error) Error() string {
	msg := e.Message
	for k, v := range e.Params {
		msg = strings.ReplaceAll(msg, ":"+k, v)
	}
	return e.Field + ": " + msg
}

// Is сравнивает по Code.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// Коды ошибок заказа.
var (
	ErrProductGone    = &Error{Code: "product_gone", Field: "items", Message: MsgProductGone}
	ErrNotEnoughStock = &Error{Code: "out_of_stock", Field: "items", Message: MsgNotEnoughStock}
	ErrNotEnoughMain  = &Error{Code: "insufficient_funds", Field: "balance", Message: MsgNotEnoughMain}
)

// ErrIdempotencyConflict — ключ уже использован заказом с другим составом.
var ErrIdempotencyConflict = errors.New("shop: idempotency key reused with a different request")

// ErrInvalidInput — запрос, который должна была отклонить валидация вызывающего.
var ErrInvalidInput = errors.New("shop: invalid input")

// ErrComputerNotFound — ПК нет в клубе.
var ErrComputerNotFound = errors.New("shop: computer not found")

// Clock — часы (тесты).
type Clock func() time.Time

// Service — магазин клуба.
type Service struct {
	pool db.Beginner
	now  Clock
}

// New — pool: пул логина zafiro_app.
func New(pool db.Beginner, now Clock) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{pool: pool, now: now}
}

// Product — товар витрины (ProductResource).
type Product struct {
	ID       int64
	SKU      string
	Name     string
	Category string
	Price    money.Tiyn
	Quantity int64
}

// Page — страница витрины и курсор следующей (пусто — конец).
type Page struct {
	Products []Product
	Next     string
}

// ListProducts — товары клуба по названию, страницами (Api\ProductController::__invoke: orderBy('name')->paginate(50)).
// Курсор — непрозрачная строка (keyset по name, id) вместо номера страницы.
func (s *Service) ListProducts(ctx context.Context, clubID int64, pageSize int, cursor string) (Page, error) {
	if pageSize <= 0 {
		pageSize = PageSize
	}
	pageSize = min(pageSize, MaxPageSize)
	afterName, afterID, err := decodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	var out Page
	err = db.InTenantTx(ctx, s.pool, clubID, func(q *store.Queries) error {
		rows, err := q.ShopListProducts(ctx, store.ShopListProductsParams{
			ClubID: clubID, AfterName: afterName, AfterID: afterID, PageSize: int32(pageSize + 1), // #nosec G115 -- ≤ 101
		})
		if err != nil {
			return err
		}
		out = Page{}
		for i, r := range rows {
			if i == pageSize {
				last := rows[i-1]
				out.Next = encodeCursor(last.Name, last.ID)
				break
			}
			out.Products = append(out.Products, Product{ID: r.ID, SKU: r.Sku, Name: r.Name, Category: r.Category,
				Price: money.Tiyn(r.Price), Quantity: r.Quantity})
		}
		return nil
	})
	return out, err
}

type cursor struct {
	Name string `json:"n"`
	ID   int64  `json:"i"`
}

func encodeCursor(name string, id int64) string {
	b, _ := json.Marshal(cursor{Name: name, ID: id}) // структура из строки и числа всегда сериализуется
	return base64URL(b)
}

func decodeCursor(s string) (string, int64, error) {
	if s == "" {
		return "", 0, nil
	}
	b, err := unbase64URL(s)
	if err != nil {
		return "", 0, fmt.Errorf("%w: page token", ErrInvalidInput)
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil || c.ID <= 0 {
		return "", 0, fmt.Errorf("%w: page token", ErrInvalidInput)
	}
	return c.Name, c.ID, nil
}

// OrderRequest — заказ к ПК (POST /api/guest/shop/purchase).
type OrderRequest struct {
	ClubID     int64
	UserID     int64
	ComputerID int64 // ПК из сертификата mTLS (в старом — computer_id + X-Agent-Token)
	// Quantities — product_id → количество; одинаковые строки запроса уже сложены вызывающим
	// (GuestShopController: $quantities[id] += quantity), строк 1…MaxLines, каждая 1…MaxQuantity до сложения.
	Quantities     map[int64]int64
	IdempotencyKey string
	Actor          billing.BalanceActor
}

// Line — строка заказа (элемент shop_orders.items).
type Line struct {
	ProductID int64      `json:"product_id"`
	Name      string     `json:"name"`
	Quantity  int64      `json:"quantity"`
	Price     money.Tiyn `json:"price"`
	Total     money.Tiyn `json:"total"`
}

// Order — оформленный заказ.
type Order struct {
	OrderID      int64      `json:"order_id"`
	Lines        []Line     `json:"lines"`
	Amount       money.Tiyn `json:"amount"`
	MainBalance  money.Tiyn `json:"main_balance"`
	BonusBalance money.Tiyn `json:"bonus_balance"`
	ComputerName string     `json:"computer_name"`
	CreatedAt    time.Time  `json:"created_at"`
	// Stock — остатки после заказа (product_id → quantity).
	Stock map[int64]int64 `json:"stock"`
	// LedgerTransactionID — проводка shop_order (0 — заказ на 0 ₸, денег не двигалось).
	LedgerTransactionID int64 `json:"ledger_transaction_id"`
	// Replayed — повтор с тем же ключом: ничего не записано, ответ первого выполнения.
	Replayed bool `json:"-"`
}

// PlaceOrder — заказ товаров к ПК с основного баланса (BR-075).
//
// Laravel: app/Services/AgentShop.php purchase() (стр. 37–108):
//
//	ksort($quantities); транзакция; товары (не удалённые) FOR UPDATE по id; не все нашлись → items «Товар больше не продаётся…»
//	total = Σ amountToCents(price) * qty; qty > quantity → items «Недостаточно товара: …»
//	balance FOR UPDATE; нет или main < total → balance «Недостаточно средств на основном счёте.»
//	balance -= total; LoyaltyProgram::addSpending(user, total)
//	по строкам: quantity -= qty; stock_movements {write_off, note 'Заказ из агента'}; вебхук stock.changed {reason: sale}
//	журнал shop.purchased (subject = ПК) {amount, items, computer}; shop_orders {status new}
//
// Без скидки и без бонусов; активная сессия не нужна.
func (s *Service) PlaceOrder(ctx context.Context, req OrderRequest) (Order, error) {
	if err := validateOrder(req); err != nil {
		return Order{}, err
	}
	ids := make([]int64, 0, len(req.Quantities))
	for id := range req.Quantities {
		ids = append(ids, id)
	}
	slices.Sort(ids) // ksort($quantities)
	var out Order
	err := db.InTenantTx(ctx, s.pool, req.ClubID, func(q *store.Queries) error {
		out = Order{}
		now := s.now()
		replayed, err := claim(ctx, q, req, ids, now, &out)
		if err != nil || replayed {
			out.Replayed = replayed
			return err
		}
		pc, err := q.ShopComputerName(ctx, store.ShopComputerNameParams{ClubID: req.ClubID, ID: req.ComputerID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrComputerNotFound
		}
		if err != nil {
			return err
		}
		computerName := displayName(pc.DisplayName, pc.DeviceName, pc.Number)

		products, err := q.ShopLockProducts(ctx, store.ShopLockProductsParams{ClubID: req.ClubID, Ids: ids})
		if err != nil {
			return err
		}
		if len(products) != len(ids) {
			return ErrProductGone
		}
		var total money.Tiyn
		lines := make([]Line, 0, len(products))
		for _, p := range products {
			qty := req.Quantities[p.ID]
			if qty > p.Quantity {
				return &Error{Code: ErrNotEnoughStock.Code, Field: "items", Message: MsgNotEnoughStock, Params: map[string]string{"product": p.Name}}
			}
			price := money.Tiyn(p.Price)
			lineTotal := price * money.Tiyn(qty)
			total += lineTotal
			if !total.Valid() {
				return fmt.Errorf("%w: order amount out of range", ErrInvalidInput)
			}
			lines = append(lines, Line{ProductID: p.ID, Name: p.Name, Quantity: qty, Price: price, Total: lineTotal})
		}

		balances, err := ledger.LockBalances(ctx, q, req.ClubID, req.UserID)
		if errors.Is(err, ledger.ErrBalanceNotFound) {
			return ErrNotEnoughMain
		}
		if err != nil {
			return err
		}
		bal := balances[req.UserID]
		if bal.Main < total {
			return ErrNotEnoughMain
		}
		out.MainBalance, out.BonusBalance = bal.Main-total, bal.Bonus

		order, err := s.insertOrder(ctx, q, req, computerName, lines, total, now)
		if err != nil {
			return err
		}
		out.OrderID, out.CreatedAt, out.Lines, out.Amount, out.ComputerName = order.ID, now, lines, total, computerName
		if order.CreatedAt != nil {
			out.CreatedAt = *order.CreatedAt
		}

		if total > 0 {
			meta, err := json.Marshal(map[string]any{"order_id": order.ID, "computer_id": req.ComputerID})
			if err != nil {
				return err
			}
			res, err := billing.Charge(ctx, q, billing.ChargeParams{
				ClubID: req.ClubID, UserID: req.UserID, Kind: ledger.KindShopOrder, Revenue: ledger.AccountRevenueShop,
				Main: total, IdempotencyKey: req.IdempotencyKey, Refs: ledger.Refs{ShopOrderID: order.ID},
				Meta: meta, Actor: req.Actor,
			})
			if err != nil {
				var insufficient *ledger.InsufficientFundsError
				if errors.As(err, &insufficient) {
					return ErrNotEnoughMain
				}
				return err
			}
			out.LedgerTransactionID = res.Ledger.TransactionID
			if b, ok := res.Ledger.BalanceAfter(ledger.Main(req.UserID)); ok {
				out.MainBalance = b
			}
			// Laravel: $this->loyalty->addSpending($user->getKey(), $total) (BR-042).
			if err := addSpending(ctx, q, req.ClubID, req.UserID, total, now); err != nil {
				return err
			}
		}

		out.Stock = make(map[int64]int64, len(lines))
		for i, l := range lines {
			p := products[i]
			m := stock.Movement{ProductID: p.ID, Type: stock.WriteOff, Quantity: l.Quantity,
				BalanceAfter: p.Quantity - l.Quantity, Note: StockNote, Change: -l.Quantity}
			if _, err := stock.ApplyMovement(ctx, q, req.ClubID, req.UserID, m, now); err != nil {
				return err
			}
			out.Stock[p.ID] = m.BalanceAfter
			// WebhookDispatcher::dispatch('stock.changed', {product_id, sku, change, quantity, reason: sale}).
			if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: req.ClubID, Type: "stock.changed", Data: map[string]any{
				"product_id": p.ID, "sku": p.Sku, "change": -l.Quantity, "quantity": m.BalanceAfter, "reason": "sale",
			}}); err != nil {
				return err
			}
		}
		if err := s.record(ctx, q, req, computerName, lines, total); err != nil {
			return err
		}
		newCount, err := q.ShopCountNewOrders(ctx, req.ClubID)
		if err != nil {
			return err
		}
		if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: req.ClubID, Type: "shop_order.created", Data: map[string]any{
			"order_id": order.ID, "status": "new", "new_count": newCount,
		}}); err != nil {
			return err
		}
		if total > 0 {
			if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: req.ClubID, Type: "balance.changed", Data: map[string]any{
				"user_id": req.UserID, "balance": int64(out.MainBalance), "bonus_balance": int64(out.BonusBalance),
			}}); err != nil {
				return err
			}
		}
		return remember(ctx, q, req.ClubID, req.IdempotencyKey, out)
	})
	return out, err
}

func validateOrder(req OrderRequest) error {
	if req.ClubID <= 0 || req.UserID <= 0 || req.ComputerID <= 0 {
		return fmt.Errorf("%w: club, user and computer are required", ErrInvalidInput)
	}
	if len(req.Quantities) == 0 || len(req.Quantities) > MaxLines {
		return fmt.Errorf("%w: 1..%d lines", ErrInvalidInput, MaxLines)
	}
	for id, qty := range req.Quantities {
		// Сложенные одинаковые строки могут дать больше MaxQuantity (BR-075) — верхняя граница: все строки одного товара.
		if id <= 0 || qty < 1 || qty > MaxQuantity*MaxLines {
			return fmt.Errorf("%w: bad line", ErrInvalidInput)
		}
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > MaxIdempotencyKeyLen {
		return fmt.Errorf("%w: idempotency key must be 1..%d bytes", ErrInvalidInput, MaxIdempotencyKeyLen)
	}
	return nil
}

func (s *Service) insertOrder(ctx context.Context, q *store.Queries, req OrderRequest, computerName string, lines []Line, total money.Tiyn, now time.Time) (store.ShopInsertOrderRow, error) {
	body, err := EncodeItems(lines)
	if err != nil {
		return store.ShopInsertOrderRow{}, err
	}
	pcID := req.ComputerID
	return q.ShopInsertOrder(ctx, store.ShopInsertOrderParams{
		ClubID: req.ClubID, UserID: req.UserID, ComputerID: &pcID, ComputerName: &computerName,
		Items: body, Amount: int64(total), Now: &now,
	})
}

// record — журнал shop.purchased (AgentShop::purchase() стр. 82–88).
func (s *Service) record(ctx context.Context, q *store.Queries, req OrderRequest, computerName string, lines []Line, total money.Tiyn) error {
	if _, ok := activity.RequestMetaFromContext(ctx); !ok {
		return nil // без данных запроса (фоновые вызовы, тесты сервиса) журнал не пишется
	}
	names := make([]string, len(lines))
	items := make([]map[string]any, len(lines))
	for i, l := range lines {
		names[i] = l.Name + " × " + strconv.FormatInt(l.Quantity, 10)
		items[i] = map[string]any{"product_id": l.ProductID, "name": l.Name, "quantity": l.Quantity,
			"price": formatCents(l.Price), "total": formatCents(l.Total)}
	}
	_, err := activity.Record(ctx, q, activity.Entry{
		ClubID: req.ClubID, Event: "shop.purchased",
		Description: "Заказ из агента на ПК " + computerName + ": " + strings.Join(names, ", "),
		UserID:      req.UserID, SubjectType: subjectComputer, SubjectID: req.ComputerID,
		Details: map[string]any{"amount": formatCents(total), "items": items, "computer": computerName},
	})
	return err
}

// ItemsVersion — версия формата shop_orders.items, который пишет EncodeItems (решение Q-003).
const ItemsVersion = 2

// ErrItemsFormat — shop_orders.items не читается ни как v2, ни как формат старого кода.
var ErrItemsFormat = errors.New("shop: malformed shop_orders.items")

type itemsV2 struct {
	V     int      `json:"v"`
	Items []itemV2 `json:"items"`
}

type itemV2 struct {
	ProductID int64  `json:"product_id"`
	Name      string `json:"name"`
	Quantity  int64  `json:"quantity"`
	PriceTiyn int64  `json:"price_tiyn"`
	TotalTiyn int64  `json:"total_tiyn"`
}

// EncodeItems — shop_orders.items для записи. Решение Q-003 (docs/DECISIONS.md): формат v2 — суммы в тиынах,
// поле версии: {"v": 2, "items": [{product_id, name, quantity, price_tiyn, total_tiyn}]}.
// Старый код (AgentShop::purchase()) писал список [{product_id, name, quantity, price, total}] с суммами-строками
// formatCents в тенге — его читает DecodeItems (v1).
func EncodeItems(lines []Line) (json.RawMessage, error) {
	out := itemsV2{V: ItemsVersion, Items: make([]itemV2, len(lines))}
	for i, l := range lines {
		out.Items[i] = itemV2{ProductID: l.ProductID, Name: l.Name, Quantity: l.Quantity,
			PriceTiyn: int64(l.Price), TotalTiyn: int64(l.Total)}
	}
	return json.Marshal(out)
}

// DecodeItems читает shop_orders.items обоих форматов (решение Q-003): v2 — объект с "v": 2 и суммами в тиынах
// (только целые); v1 — список старого кода, суммы — тенге строкой ("950.50", formatCents) или числом,
// в тиыны как MySQL decimal(12,2) (money.ParseTengeRounded; экспонента — через float, как (float) в PHP 8.4).
func DecodeItems(raw json.RawMessage) ([]Line, error) {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")):
		return nil, nil
	case trimmed[0] == '{':
		var v itemsV2
		if err := json.Unmarshal(trimmed, &v); err != nil || v.V != ItemsVersion {
			return nil, fmt.Errorf("%w: %s", ErrItemsFormat, trimmed)
		}
		out := make([]Line, len(v.Items))
		for i, it := range v.Items {
			out[i] = Line{ProductID: it.ProductID, Name: it.Name, Quantity: it.Quantity,
				Price: money.Tiyn(it.PriceTiyn), Total: money.Tiyn(it.TotalTiyn)}
		}
		return out, nil
	}
	var legacy []map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &legacy); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrItemsFormat, err)
	}
	out := make([]Line, len(legacy))
	for i, it := range legacy {
		l := Line{}
		var err error
		if l.ProductID, err = legacyInt(it["product_id"]); err != nil {
			return nil, err
		}
		if l.Quantity, err = legacyInt(it["quantity"]); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(it["name"], &l.Name)
		if l.Price, err = legacyTenge(it["price"]); err != nil {
			return nil, err
		}
		if l.Total, err = legacyTenge(it["total"]); err != nil {
			return nil, err
		}
		out[i] = l
	}
	return out, nil
}

// legacyInt — целое из v1 (число или числовая строка).
func legacyInt(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, fmt.Errorf("%w: %s", ErrItemsFormat, raw)
		}
		n = json.Number(strings.TrimSpace(s))
	}
	v, err := n.Int64()
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrItemsFormat, raw)
	}
	return v, nil
}

// legacyTenge — сумма v1 (тенге строкой или числом) в тиынах.
func legacyTenge(raw json.RawMessage) (money.Tiyn, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err != nil {
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			return 0, fmt.Errorf("%w: %s", ErrItemsFormat, raw)
		}
		str = n.String()
	}
	str = strings.TrimSpace(str)
	t, err := money.ParseTengeRounded(str)
	if err == nil {
		return t, nil
	}
	f, ferr := strconv.ParseFloat(str, 64)
	if ferr != nil {
		return 0, fmt.Errorf("%w: amount %q", ErrItemsFormat, str)
	}
	return money.FromTengeFloatDecimal(f)
}

// formatCents — AgentTariffPurchaseService::formatCents(): sprintf('%d.%02d', intdiv(c, 100), c % 100). Суммы заказа
// неотрицательны, для них это то же, что Tiyn.Tenge() ("950.50").
func formatCents(t money.Tiyn) string { return t.Tenge() }

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unbase64URL(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// displayName — ClubComputer::displayName(): display_name ?: device_name ?: sprintf('PC-%02d', number).
func displayName(display, device *string, number int64) string {
	if display != nil && *display != "" {
		return *display
	}
	if device != nil && *device != "" {
		return *device
	}
	return fmt.Sprintf("PC-%02d", number)
}

// addSpending — LoyaltyProgram::addSpending(): club_memberships.total_spent += сумма (BR-042); нет членства — ничего.
func addSpending(ctx context.Context, q *store.Queries, clubID, userID int64, cents money.Tiyn, now time.Time) error {
	total, err := q.IncrementTotalSpent(ctx, store.IncrementTotalSpentParams{ClubID: clubID, UserID: userID, Cents: int64(cents), Now: &now})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if money.Tiyn(total) > MaxTotalSpent {
		return fmt.Errorf("%w: total_spent overflow", ErrInvalidInput)
	}
	return nil
}

// claim — ключ идемпотентности заказа в billing_requests (как billing/service: тот же ключ и состав —
// ответ первого заказа; другой состав — ErrIdempotencyConflict).
func claim(ctx context.Context, q *store.Queries, req OrderRequest, ids []int64, now time.Time, out *Order) (bool, error) {
	lines := make([][2]int64, len(ids))
	for i, id := range ids {
		lines[i] = [2]int64{id, req.Quantities[id]}
	}
	body, err := json.Marshal(struct {
		Operation  string     `json:"operation"`
		UserID     int64      `json:"user_id"`
		ComputerID int64      `json:"computer_id"`
		Lines      [][2]int64 `json:"lines"`
	}{operation, req.UserID, req.ComputerID, lines})
	if err != nil {
		return false, err
	}
	fp := sha256.Sum256(body)
	_, err = q.ClaimBillingRequest(ctx, store.ClaimBillingRequestParams{
		ClubID: req.ClubID, IdempotencyKey: req.IdempotencyKey, Operation: operation, Fingerprint: fp[:], Now: now,
	})
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("shop: claim idempotency key: %w", err)
	}
	prev, err := q.GetBillingRequest(ctx, store.GetBillingRequestParams{ClubID: req.ClubID, IdempotencyKey: req.IdempotencyKey})
	if err != nil {
		return false, fmt.Errorf("shop: read idempotency key: %w", err)
	}
	if prev.Operation != operation || !bytes.Equal(prev.Fingerprint, fp[:]) || len(prev.Response) == 0 {
		return false, ErrIdempotencyConflict
	}
	if err := json.Unmarshal(prev.Response, out); err != nil {
		return false, fmt.Errorf("shop: decode stored response: %w", err)
	}
	return true, nil
}

func remember(ctx context.Context, q *store.Queries, clubID int64, key string, response any) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return q.SetBillingRequestResponse(ctx, store.SetBillingRequestResponseParams{ClubID: clubID, IdempotencyKey: key, Response: body})
}
