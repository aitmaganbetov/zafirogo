package v1

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// tx — тенантная транзакция клуба ключа (Principal создателя ключа в c.ctx).
func (h *handlers) tx(c *call, fn func(ctx context.Context, q *store.Queries) error) error {
	return h.d.Tx(c.ctx, fn)
}

// truthy — when($value, …) PHP: null, "", "0", 0 — фильтра нет.
func truthyStr(s *string) bool { return s != nil && *s != "" && *s != "0" }
func truthyInt(n *int64) bool  { return n != nil && *n != 0 }

// pathID — {product}/{computer}/{sale}: whereNumber; иначе маршрута нет (404).
func (c *call) pathID(name string) (int64, bool) {
	s := c.param(name)
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// ---------------------------------------------------------------------------
// GET /me — MeController
// ---------------------------------------------------------------------------

func (h *handlers) me(c *call) response {
	k := c.key
	scopes := k.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return data(http.StatusOK, map[string]any{
		"club": struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		}{k.ClubID, k.ClubName, k.ClubSlug},
		"key": struct {
			Prefix             string   `json:"prefix"`
			Scopes             []string `json:"scopes"`
			ExpiresAt          *string  `json:"expires_at"`
			RateLimitPerMinute int      `json:"rate_limit_per_minute"`
		}{k.Prefix, scopes, iso(k.ExpiresAt), h.d.APILimits.KeyLimit(k.RateLimit)},
	})
}

// ---------------------------------------------------------------------------
// Товары и склад — ProductController, StockMovementController
// ---------------------------------------------------------------------------

func (h *handlers) listProducts(c *call) response {
	f := c.form()
	sku, search := f.str("sku", false, 100), f.str("search", false, 100)
	category := f.integer("category_id", false, nil, nil)
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	p := f.page("products")
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	arg := store.ApiV1ProductsParams{ClubID: c.clubID(), AfterID: p.after, PageSize: p.size()}
	if truthyStr(sku) {
		arg.Sku = sku
	}
	if truthyStr(search) {
		// addcslashes($search, '%_\\').'%' — начало имени.
		arg.SearchPrefix = ptr(strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(*search) + "%")
	}
	if truthyInt(category) {
		arg.CategoryID = category
	}
	var rows []store.ApiV1ProductsRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		rows, err = q.ApiV1Products(ctx, arg)
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	return pageOf(p, rows, func(r store.ApiV1ProductsRow) int64 { return r.ID },
		func(r store.ApiV1ProductsRow) any { return productOf(store.ApiV1ProductRow(r)) })
}

func (h *handlers) product(ctx context.Context, c *call, q *store.Queries, id int64) (store.ApiV1ProductRow, error) {
	return q.ApiV1Product(ctx, store.ApiV1ProductParams{ClubID: c.clubID(), ID: id})
}

func (h *handlers) showProduct(c *call) response {
	id, ok := c.pathID("product")
	if !ok {
		return errorResponse("not_found", http.StatusNotFound)
	}
	var p store.ApiV1ProductRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		p, err = h.product(ctx, c, q, id)
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	return data(http.StatusOK, productOf(p))
}

func (h *handlers) listCategories(c *call) response {
	f := c.form()
	p := f.page("product_categories")
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	var rows []store.ApiV1CategoriesRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		rows, err = q.ApiV1Categories(ctx, store.ApiV1CategoriesParams{ClubID: c.clubID(), AfterID: p.after, PageSize: p.size()})
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	return pageOf(p, rows, func(r store.ApiV1CategoriesRow) int64 { return r.ID },
		func(r store.ApiV1CategoriesRow) any { return categoryView{ID: r.ID, Name: r.Name} })
}

func (h *handlers) listStockMovements(c *call) response {
	f := c.form()
	product := f.integer("product_id", false, nil, nil)
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	p := f.page("stock_movements")
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	arg := store.ApiV1StockMovementsParams{ClubID: c.clubID(), AfterID: p.after, PageSize: p.size()}
	if truthyInt(product) {
		arg.ProductID = product
	}
	var rows []store.ApiV1StockMovementsRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		rows, err = q.ApiV1StockMovements(ctx, arg)
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	return pageOf(p, rows, func(r store.ApiV1StockMovementsRow) int64 { return r.ID },
		func(r store.ApiV1StockMovementsRow) any { return movementOf(store.ApiV1StockMovementRow(r)) })
}

// productForm — правила ProductController::rules() (sku, name, product_category_id, price) и quantity; при правке —
// 'sometimes' без 'required' и запрет quantity. unique/exists — в клубе ключа.
type productForm struct {
	sku, name  *string
	categoryID *int64
	price      *money.Tiyn
	quantity   *int64
}

func (h *handlers) validateProduct(ctx context.Context, c *call, q *store.Queries, exceptID int64, update bool) (productForm, error) {
	f := c.form()
	var out productForm
	required := !update
	field := func(name string) bool { return !update || f.has(name) }
	if field("sku") {
		if out.sku = f.str("sku", required, 100); out.sku != nil {
			taken, err := q.ApiV1SkuTaken(ctx, store.ApiV1SkuTakenParams{ClubID: c.clubID(), Sku: *out.sku, ExceptID: exceptID})
			if err != nil {
				return out, err
			}
			if taken {
				f.fail("sku", "unique", "")
				out.sku = nil
			}
		}
	}
	if field("name") {
		out.name = f.str("name", required, 150)
	}
	if field("product_category_id") {
		if out.categoryID = f.integer("product_category_id", required, nil, nil); out.categoryID != nil {
			ok, err := q.ApiV1CategoryExists(ctx, store.ApiV1CategoryExistsParams{ClubID: c.clubID(), ID: *out.categoryID})
			if err != nil {
				return out, err
			}
			if !ok {
				f.fail("product_category_id", "exists", "")
				out.categoryID = nil
			}
		}
	}
	if field("price") {
		out.price = f.price("price", required)
	}
	if update {
		// 'quantity' => ['prohibited'] с текстом 'Use POST /api/v1/stock-movements to change stock.'
		if _, ok := f.value("quantity"); ok {
			f.fe.Add("quantity", "Use POST /api/v1/stock-movements to change stock.")
		}
	} else {
		out.quantity = f.integer("quantity", true, ptr(int64(0)), ptr(stock.MaxInitialQuantity))
	}
	return out, f.err()
}

// createProduct — ProductController::store() → CreateProductAction (журнал, начальный приход, вебхуки).
func (h *handlers) createProduct(c *call) response {
	var out store.ApiV1ProductRow
	err := h.tx(c, func(ctx context.Context, q *store.Queries) error {
		in, err := h.validateProduct(ctx, c, q, 0, false)
		if err != nil {
			return err
		}
		clubID, userID := c.clubID(), c.key.CreatedBy
		res, err := stock.CreateProduct(ctx, q, stock.CreateProductParams{
			ClubID: clubID, UserID: userID, SKU: *in.sku, Name: *in.name, CategoryID: *in.categoryID, Price: *in.price,
			Quantity: *in.quantity, Now: h.d.NowFunc(),
		})
		if err != nil {
			return err
		}
		p := res.Product
		if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, Event: "product.created", Description: "Создан товар: " + p.Name, UserID: userID,
			SubjectType: billingsvc.SubjectProduct, SubjectID: p.ID, Details: map[string]any{"sku": p.Sku, "quantity": *in.quantity},
		}); err != nil {
			return err
		}
		if res.Initial != nil {
			qty := *in.quantity
			if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
				ClubID: clubID, Event: "stock.received", UserID: userID, SubjectType: billingsvc.SubjectProduct, SubjectID: p.ID,
				Description: "Начальное оприходование: " + p.Name + ", " + strconv.FormatInt(qty, 10) + " шт.",
				Details:     map[string]any{"quantity": qty, "balance_after": qty, "note": stock.NoteInitialLog},
			}); err != nil {
				return err
			}
			if err := billingsvc.AddEvent(ctx, q, clubID, "stock.changed", map[string]any{
				"product_id": p.ID, "sku": p.Sku, "change": qty, "quantity": qty, "reason": "initial",
			}); err != nil {
				return err
			}
		}
		if err := billingsvc.AddEvent(ctx, q, clubID, "product.created", map[string]any{
			"product_id": p.ID, "sku": p.Sku, "name": p.Name, "price": money.Tiyn(p.Price).Tenge(), "quantity": *in.quantity,
		}); err != nil {
			return err
		}
		out, err = h.product(ctx, c, q, p.ID) // $product->refresh()
		return err
	})
	if err != nil {
		return h.fail(c, err)
	}
	return data(http.StatusCreated, productOf(out))
}

// updateProduct — ProductController::update() → UpdateProductAction: журнал и вебхук только при изменениях.
func (h *handlers) updateProduct(c *call) response {
	id, ok := c.pathID("product")
	if !ok {
		return errorResponse("not_found", http.StatusNotFound)
	}
	var out store.ApiV1ProductRow
	err := h.tx(c, func(ctx context.Context, q *store.Queries) error {
		if _, err := h.product(ctx, c, q, id); err != nil { // привязка модели {product}: чужой/удалённый — 404
			return err
		}
		in, err := h.validateProduct(ctx, c, q, id, true)
		if err != nil {
			return err
		}
		clubID, userID := c.clubID(), c.key.CreatedBy
		res, err := stock.UpdateProduct(ctx, q, stock.UpdateProductParams{
			ClubID: clubID, ProductID: id, UserID: userID, SKU: in.sku, Name: in.name, CategoryID: in.categoryID,
			Price: in.price, Now: h.d.NowFunc(),
		})
		if errors.Is(err, stock.ErrProductNotFound) {
			return pgx.ErrNoRows
		}
		if err != nil {
			return err
		}
		if len(res.Changes) > 0 {
			p := res.Product
			details := make(map[string]any, len(res.Changes))
			changed := make([]string, 0, len(res.Changes))
			for _, ch := range res.Changes {
				details[ch.Field] = ch.String()
				changed = append(changed, ch.Field)
			}
			if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
				ClubID: clubID, Event: "product.updated", Description: "Изменён товар: " + p.Name, UserID: userID,
				SubjectType: billingsvc.SubjectProduct, SubjectID: p.ID, Details: details,
			}); err != nil {
				return err
			}
			if err := billingsvc.AddEvent(ctx, q, clubID, "product.updated", map[string]any{
				"product_id": p.ID, "sku": p.Sku, "name": p.Name, "price": money.Tiyn(p.Price).Tenge(), "changed": changed,
			}); err != nil {
				return err
			}
		}
		out, err = h.product(ctx, c, q, id)
		return err
	})
	if err != nil {
		return h.fail(c, err)
	}
	return data(http.StatusOK, productOf(out))
}

// createStockMovement — StockMovementController::store() → AdjustStockAction.
func (h *handlers) createStockMovement(c *call) response {
	var out store.ApiV1StockMovementRow
	err := h.tx(c, func(ctx context.Context, q *store.Queries) error {
		f := c.form()
		productID := f.integer("product_id", true, nil, nil)
		if productID != nil {
			ok, err := q.ApiV1ProductExists(ctx, store.ApiV1ProductExistsParams{ClubID: c.clubID(), ID: *productID})
			if err != nil {
				return err
			}
			if !ok {
				f.fail("product_id", "exists", "")
			}
		}
		typ := f.oneOf("type", string(stock.Receipt), string(stock.WriteOff))
		qty := f.integer("quantity", true, ptr(int64(1)), ptr(stock.MaxReceiveQuantity))
		note := f.str("note", false, stock.MaxNote)
		if err := f.err(); err != nil {
			return err
		}
		clubID, userID := c.clubID(), c.key.CreatedBy
		mt := stock.MovementType(typ)
		n := ""
		if note != nil {
			n = *note
		}
		res, err := stock.AdjustStock(ctx, q, stock.AdjustParams{
			ClubID: clubID, ProductID: *productID, UserID: userID, Type: mt, Quantity: *qty, MaxQuantity: stock.MaxReceiveQuantity,
			Note: n, Now: h.d.NowFunc(),
		})
		if errors.Is(err, stock.ErrProductNotFound) {
			return pgx.ErrNoRows // findOrFail: товар удалён — 404
		}
		if err != nil {
			return err
		}
		p := res.Product
		e := activity.Entry{
			ClubID: clubID, UserID: userID, SubjectType: billingsvc.SubjectProduct, SubjectID: p.ID,
			Event: "stock.received", Description: "Оприходование: " + p.Name + ", " + strconv.FormatInt(*qty, 10) + " шт.",
			Details: map[string]any{"quantity": *qty, "balance_after": p.Quantity, "note": res.Movement.LogNote()},
		}
		if mt == stock.WriteOff {
			e.Event, e.Description = "stock.written_off", "Списание: "+p.Name+", "+strconv.FormatInt(*qty, 10)+" шт."
		}
		if err := billingsvc.RecordActivity(ctx, q, e); err != nil {
			return err
		}
		if err := billingsvc.AddEvent(ctx, q, clubID, "stock.changed", map[string]any{
			"product_id": p.ID, "sku": p.Sku, "change": res.Movement.Change, "quantity": p.Quantity, "reason": string(mt),
		}); err != nil {
			return err
		}
		out, err = q.ApiV1StockMovement(ctx, store.ApiV1StockMovementParams{ClubID: clubID, ID: res.Row.ID})
		return err
	})
	if err != nil {
		return h.fail(c, err)
	}
	return data(http.StatusCreated, movementOf(out))
}

// ---------------------------------------------------------------------------
// Продажи — SaleController
// ---------------------------------------------------------------------------

func (h *handlers) listSales(c *call) response {
	f := c.form()
	from := f.date("from")
	to := f.date("to")
	if to != nil && from != nil && to.Before(*from) {
		f.fail("to", "after_or_equal", "from")
		to = nil
	}
	shift := f.integer("cash_shift_id", false, nil, nil)
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	p := f.page("cash_transactions")
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	arg := store.ApiV1SalesParams{ClubID: c.clubID(), AfterID: p.after, PageSize: p.size(), FromAt: from, ToAt: to}
	if truthyInt(shift) {
		arg.CashShiftID = shift
	}
	var rows []store.ApiV1SalesRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		rows, err = q.ApiV1Sales(ctx, arg)
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	return pageOf(p, rows, func(r store.ApiV1SalesRow) int64 { return r.ID },
		func(r store.ApiV1SalesRow) any { return saleOf(store.ApiV1SaleRow(r)) })
}

func (h *handlers) showSale(c *call) response {
	id, ok := c.pathID("sale")
	if !ok {
		return errorResponse("not_found", http.StatusNotFound)
	}
	var s store.ApiV1SaleRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		s, err = q.ApiV1Sale(ctx, store.ApiV1SaleParams{ClubID: c.clubID(), ID: id})
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	return data(http.StatusOK, saleOf(s))
}

// ---------------------------------------------------------------------------
// ПК и зоны — ComputerController
// ---------------------------------------------------------------------------

func (h *handlers) listZones(c *call) response {
	f := c.form()
	p := f.page("club_zones")
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	var rows []store.ApiV1ZonesRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		rows, err = q.ApiV1Zones(ctx, store.ApiV1ZonesParams{ClubID: c.clubID(), AfterID: p.after, PageSize: p.size()})
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	return pageOf(p, rows, func(r store.ApiV1ZonesRow) int64 { return r.ID }, func(r store.ApiV1ZonesRow) any {
		return zoneView{ID: r.ID, Name: r.Name, Description: r.Description, Capacity: r.Capacity, Status: r.Status, ComputersCount: r.ComputersCount}
	})
}

// online — присутствие агентов клуба (связь и last_seen_at ПК).
func (h *handlers) online(c *call) map[int64]presence.State {
	if h.d.Presence == nil {
		return nil
	}
	states, err := h.d.Presence.List(c.ctx, c.clubID())
	if err != nil {
		h.d.Log().WarnContext(c.ctx, "api v1: presence", "err", err)
	}
	return clubmap.PresenceByID(states)
}

func (h *handlers) listComputers(c *call) response {
	f := c.form()
	zone := f.integer("zone_id", false, nil, nil)
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	p := f.page("club_computers")
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	arg := store.ApiV1ComputersParams{ClubID: c.clubID(), AfterID: p.after, PageSize: p.size()}
	if truthyInt(zone) {
		arg.ZoneID = zone
	}
	var rows []store.ApiV1ComputersRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		rows, err = q.ApiV1Computers(ctx, arg)
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	online := h.online(c)
	return pageOf(p, rows, func(r store.ApiV1ComputersRow) int64 { return r.ID },
		func(r store.ApiV1ComputersRow) any { return computerOf(store.ApiV1ComputerRow(r), online) })
}

func (h *handlers) showComputer(c *call) response {
	id, ok := c.pathID("computer")
	if !ok {
		return errorResponse("not_found", http.StatusNotFound)
	}
	var row store.ApiV1ComputerRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		row, err = q.ApiV1Computer(ctx, store.ApiV1ComputerParams{ClubID: c.clubID(), ID: id})
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	return data(http.StatusOK, computerOf(row, h.online(c)))
}

// reservation — ComputerController::reservation(): бронь одного ПК; то же состояние — без изменений и записей.
func (h *handlers) reservation(c *call) response {
	id, ok := c.pathID("computer")
	if !ok {
		return errorResponse("not_found", http.StatusNotFound)
	}
	var row store.ApiV1ComputerRow
	err := h.tx(c, func(ctx context.Context, q *store.Queries) error {
		clubID := c.clubID()
		if _, err := q.ApiV1Computer(ctx, store.ApiV1ComputerParams{ClubID: clubID, ID: id}); err != nil {
			return err // привязка {computer}: чужой — 404 до проверки тела
		}
		f := c.form()
		reserved := f.boolean("reserved", true)
		if err := f.err(); err != nil {
			return err
		}
		locked, err := q.ApiV1LockComputer(ctx, store.ApiV1LockComputerParams{ClubID: clubID, ID: id})
		if err != nil {
			return err
		}
		row = store.ApiV1ComputerRow(locked)
		if locked.IsReserved == *reserved {
			return nil
		}
		if *reserved && locked.CurrentSessionID != nil {
			return newError("conflict", http.StatusConflict, "The computer has an active session and cannot be reserved.")
		}
		now := h.d.NowFunc()
		if err := q.ApiV1SetReserved(ctx, store.ApiV1SetReservedParams{Reserved: *reserved, Now: &now, ClubID: clubID, ID: id}); err != nil {
			return err
		}
		row.IsReserved = *reserved
		action := "unreserve"
		if *reserved {
			action = "reserve"
		}
		name := clubmap.DisplayName(locked.DisplayName, locked.DeviceName, locked.Number)
		if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, Event: "computer.controlled", UserID: c.key.CreatedBy, SubjectType: billingsvc.SubjectComputer, SubjectID: id,
			Description: h.d.T(ctx, clubmap.MsgControlLog, map[string]string{"action": action, "names": name}),
			Details:     map[string]any{"action": action, "computers": name, "updated": 1},
		}); err != nil {
			return err
		}
		if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: clubID, Type: "computer.reservation_changed",
			Data: map[string]any{"computer_ids": []int64{id}, "reserved": *reserved}}); err != nil {
			return err
		}
		// Карта клуба в админке: «перечитать ПК» (docs/realtime.md).
		_, err = outbox.Add(ctx, q, outbox.Event{ClubID: clubID, Type: "computer.state", Data: map[string]any{"computer_id": id}})
		return err
	})
	if err != nil {
		return h.fail(c, err)
	}
	return data(http.StatusOK, computerOf(row, h.online(c)))
}

// controlActions — action: in:power_on,restart,power_off.
var controlActions = []string{clubmap.ActionPowerOn, clubmap.ActionRestart, clubmap.ActionPowerOff}

// control — ComputerController::control(): до max_bulk_items ПК клуба; ПК с сессией клиента — отказ всей пачке
// (кроме power_on). Команды агентам — после коммита (ComputerPower::queue()).
func (h *handlers) control(c *call) response {
	var (
		plan   clubmap.ControlPlan
		action string
	)
	err := h.tx(c, func(ctx context.Context, q *store.Queries) error {
		clubID := c.clubID()
		f := c.form()
		ids := h.validateComputerIDs(ctx, c, q, f)
		action = f.oneOf("action", controlActions...)
		if err := f.err(); err != nil {
			return err
		}
		locked, err := q.ApiV1LockComputers(ctx, store.ApiV1LockComputersParams{ClubID: clubID, Ids: ids})
		if err != nil {
			return err
		}
		if action != clubmap.ActionPowerOn {
			busy := []int64{}
			for _, r := range locked {
				if r.CurrentSessionID != nil {
					busy = append(busy, r.ID)
				}
			}
			if len(busy) > 0 {
				e := newError("conflict", http.StatusConflict, "Some computers have an active customer session.")
				e.env.ComputerIDs = busy
				return e
			}
		}
		rows := make([]store.ClubMapComputersByIDsRow, len(locked))
		for i, r := range locked {
			rows[i] = store.ClubMapComputersByIDsRow(r)
		}
		env := clubmap.Env{ClubID: clubID, UserID: c.key.CreatedBy, Now: h.d.NowFunc(),
			T: func(key string, params map[string]string) string { return h.d.T(ctx, key, params) }}
		plan, err = clubmap.ControlTx(ctx, q, env, rows, action)
		return err
	})
	if err != nil {
		return h.fail(c, err)
	}
	var sender clubmap.CommandSender
	if h.d.AgentCommands != nil {
		sender = h.d.AgentCommands
	}
	var pres clubmap.Presence
	if h.d.Presence != nil {
		pres = h.d.Presence
	}
	clubmap.SendControl(context.WithoutCancel(c.ctx), sender, pres, c.clubID(), c.key.CreatedBy, plan, h.d.Log())
	var rows []store.ApiV1ComputersByIDsRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		rows, err = q.ApiV1ComputersByIDs(ctx, store.ApiV1ComputersByIDsParams{ClubID: c.clubID(), Ids: plan.IDs})
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	online := h.online(c)
	views := make([]computerView, len(rows))
	for i, r := range rows {
		views[i] = computerOf(store.ApiV1ComputerRow(r), online)
	}
	return data(http.StatusOK, struct {
		Action    string         `json:"action"`
		Updated   int            `json:"updated"`
		Computers []computerView `json:"computers"`
	}{action, len(rows), views})
}

// validateComputerIDs — computer_ids: required|array|min:1|max:max_bulk_items; *: required|integer|distinct|exists.
func (h *handlers) validateComputerIDs(ctx context.Context, c *call, q *store.Queries, f *form) []int64 {
	v, ok := f.value("computer_ids")
	if !ok {
		f.fail("computer_ids", "required", "")
		return nil
	}
	list, isList := v.([]any)
	if !isList {
		f.fail("computer_ids", "array", "")
		return nil
	}
	limit := h.d.APILimits.MaxBulkItems
	switch {
	case len(list) < 1:
		f.fail("computer_ids", "required", "") // пустой массив — required
		return nil
	case len(list) > limit:
		f.fail("computer_ids", "max_array", strconv.Itoa(limit))
		return nil
	}
	ids := make([]int64, 0, len(list))
	idx := make([]int, 0, len(list))
	seen := map[int64]int{}
	for i, e := range list {
		field := "computer_ids." + strconv.Itoa(i)
		if e == nil {
			f.fail(field, "required", "")
			continue
		}
		n, ok := asInt(e)
		if !ok {
			f.fail(field, "integer", "")
			continue
		}
		seen[n]++
		ids = append(ids, n)
		idx = append(idx, i)
	}
	if len(ids) == 0 {
		return nil
	}
	found, err := q.ApiV1ComputerIDsOf(ctx, store.ApiV1ComputerIDsOfParams{ClubID: c.clubID(), Ids: ids})
	if err != nil {
		f.fe.Add("computer_ids", err.Error())
		return nil
	}
	for j, n := range ids {
		field := "computer_ids." + strconv.Itoa(idx[j])
		switch {
		case seen[n] > 1:
			f.fail(field, "distinct", "")
		case !slices.Contains(found, n):
			f.fail(field, "exists", "")
		}
	}
	return ids
}

// ---------------------------------------------------------------------------
// Тарифы и клиенты — TariffController, MemberController
// ---------------------------------------------------------------------------

func (h *handlers) listTariffs(c *call) response {
	f := c.form()
	active := f.boolean("active", false)
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	p := f.page("tariffs")
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	var rows []store.ApiV1TariffsRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		rows, err = q.ApiV1Tariffs(ctx, store.ApiV1TariffsParams{ClubID: c.clubID(), AfterID: p.after, PageSize: p.size(), Active: active})
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	return pageOf(p, rows, func(r store.ApiV1TariffsRow) int64 { return r.ID }, func(r store.ApiV1TariffsRow) any {
		zones := r.ZoneIds
		if zones == nil {
			zones = []int64{}
		}
		return tariffView{ID: r.ID, Name: r.Name, Description: r.Description, DurationMinutes: r.DurationMinutes,
			Price: money.Tiyn(r.Price).Tenge(), Currency: r.Currency, IsActive: r.IsActive, ZoneIDs: zones}
	})
}

func (h *handlers) listMembers(c *call) response {
	f := c.form()
	p := f.page("club_memberships")
	if err := f.err(); err != nil {
		return h.fail(c, err)
	}
	var rows []store.ApiV1MembersRow
	if err := h.tx(c, func(ctx context.Context, q *store.Queries) (err error) {
		rows, err = q.ApiV1Members(ctx, store.ApiV1MembersParams{ClubID: c.clubID(), AfterID: p.after, PageSize: p.size()})
		return err
	}); err != nil {
		return h.fail(c, err)
	}
	return pageOf(p, rows, func(r store.ApiV1MembersRow) int64 { return r.ID }, func(r store.ApiV1MembersRow) any { return memberOf(r) })
}
