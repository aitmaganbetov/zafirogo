//go:build integration

package httpserver

// Сквозные тесты экранов кассы, склада, заказов с ПК и услуг (Этап 3, G2): витрина и поиск клиента, операции
// по страницам, nonce печати Z на сотрудника, каталог склада, остатки и приход кассира, выдача и отмена заказа
// (деньги — ledger, товар — склад), CRUD услуг.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
)

func TestCashScreens(t *testing.T) {
	e := newAdminEnv(t)
	_, owner := e.staff("Владелец", "owner")
	_, cashier := e.staff("Кассир", "cashier")
	cola := e.product("Cola", 45000, 10)
	e.product("Пусто", 10000, 0)
	e.exec("UPDATE product_categories SET name = 'Напитки' WHERE club_id = $1", e.club)
	snack := e.scalar(`INSERT INTO products (club_id, sku, name, category, product_category_id, price, quantity, created_at, updated_at)
		VALUES ($1, 'SN-1', 'Arahis', 'Снеки', $2, 30000, 5, now(), now()) RETURNING id`, e.club, e.category)
	e.service("Печать", 5000, true)
	e.service("Выключенная", 5000, false)
	cust := e.customer(owner, "Айгерим Клиент", 150000, 0)

	t.Run("catalog", func(t *testing.T) {
		c := e.get(cashier, "/api/cash/catalog")
		expect(t, c, 200, "catalog")
		if num(c.body["products_total"]) != 2 {
			t.Fatalf("products_total %v", c.body)
		}
		cats, _ := c.body["categories"].([]any)
		// Порядок первого появления в списке по имени: Arahis (Снеки) раньше Cola (Напитки).
		if len(cats) != 2 || obj(cats[0])["name"] != "Снеки" || obj(cats[1])["name"] != "Напитки" {
			t.Fatalf("categories %v", cats)
		}
		if svc, _ := c.body["services"].([]any); len(svc) != 1 || num(obj(svc[0])["price_tiyn"]) != 5000 {
			t.Fatalf("services %v", svc)
		}
		p := e.get(cashier, "/api/cash/products?limit=1")
		items, _ := p.body["items"].([]any)
		if len(items) != 1 || num(obj(items[0])["id"]) != snack || p.body["next_cursor"] == nil {
			t.Fatalf("page 1 %v", p.body)
		}
		p2 := e.get(cashier, fmt.Sprintf("/api/cash/products?limit=1&cursor=%s", p.body["next_cursor"]))
		if items, _ := p2.body["items"].([]any); len(items) != 1 || num(obj(items[0])["id"]) != cola {
			t.Fatalf("page 2 %v", p2.body)
		}
		f := e.get(cashier, "/api/cash/products?category="+"%D0%9D%D0%B0%D0%BF%D0%B8%D1%82%D0%BA%D0%B8")
		if items, _ := f.body["items"].([]any); len(items) != 1 || obj(items[0])["image_url"] != "/images/product-placeholder.svg" {
			t.Fatalf("category filter %v", f.body)
		}
	})

	t.Run("customer search", func(t *testing.T) {
		st, body := e.raw("GET", cashier, "/api/cash/customers?q=%D0%B0%D0%B9%D0%B3%D0%B5%D1%80", "", nil)
		var list []map[string]any
		if st != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list) != 1 || num(list[0]["id"]) != cust ||
			num(list[0]["balance_tiyn"]) != 150000 {
			t.Fatalf("search: %d %s", st, body)
		}
		st, body = e.raw("GET", cashier, "/api/cash/customers?limit=2", "", nil)
		if err := json.Unmarshal([]byte(body), &list); st != 200 || err != nil || len(list) != 2 {
			t.Fatalf("limit: %d %s", st, body)
		}
	})

	t.Run("operations pages and per-user Z nonce", func(t *testing.T) {
		shift := e.openShift(owner, 100000)
		for i := 0; i < 3; i++ {
			expect(t, e.post(owner, "/api/cash/transactions", map[string]any{"type": "income", "amount_tiyn": 1000 + i, "description": "Приход"}), 201, "income")
		}
		ops := e.get(owner, "/api/cash/operations?page=2&limit=2")
		expect(t, ops, 200, "operations")
		if items, _ := ops.body["items"].([]any); len(items) != 1 || num(ops.body["total"]) != 3 || num(ops.body["per_page"]) != 2 {
			t.Fatalf("page 2: %v", ops.body)
		}
		if ops.body["next_before_id"] != nil {
			t.Fatalf("page mode has no keyset cursor: %v", ops.body)
		}
		// Nonce выдан владельцу — кассир с ним не отметит печать (как сессия сотрудника в старом).
		n := e.post(owner, fmt.Sprintf("/api/cash/shifts/%d/z-report/nonce", shift), nil)
		expect(t, n, 200, "nonce")
		printed := fmt.Sprintf("/api/cash/shifts/%d/z-report/printed", shift)
		if r := e.post(cashier, printed, map[string]any{"nonce": n.body["nonce"]}); r.status != 419 {
			t.Fatalf("other user's nonce: %d %v", r.status, r.body)
		}
		expect(t, e.post(owner, printed, map[string]any{"nonce": n.body["nonce"]}), 200, "printed")
		if e.scalar("SELECT count(*) FROM information_schema.columns WHERE table_name = 'cash_shifts' AND column_name = 'z_report_nonce_hash'") != 0 {
			t.Fatal("z_report_nonce_hash must be dropped (0016)")
		}
	})
}

func TestWarehouseScreens(t *testing.T) {
	e := newAdminEnv(t)
	_, owner := e.staff("Владелец", "owner")
	_, manager := e.staff("Менеджер", "manager")
	_, cashier := e.staff("Кассир", "cashier")
	p := e.product("Cola", 45000, 10)
	e.product("Fanta", 40000, 3)

	cat := e.post(manager, "/api/warehouse/categories", map[string]any{"name": "Снеки"})
	expect(t, cat, 201, "category")
	expect(t, e.post(manager, "/api/warehouse/categories", map[string]any{"name": "Снеки"}), 422, "duplicate category")
	expect(t, e.post(cashier, "/api/warehouse/categories", map[string]any{"name": "X"}), 403, "cashier category")
	if e.activityCount("warehouse.category_created", num(cat.body["id"])) != 1 {
		t.Fatal("category journal")
	}

	// Приход кассира (stock.create), верхняя граница формы — 100000.
	r := e.post(cashier, "/api/stock/receive", map[string]any{"product_id": p, "quantity": 5, "note": "Поставка"})
	expect(t, r, 201, "receive")
	if num(r.body["balance_after"]) != 15 {
		t.Fatalf("receive %v", r.body)
	}
	expectField(t, e.post(cashier, "/api/stock/receive", map[string]any{"product_id": p, "quantity": 100001}), "quantity",
		"The quantity field must not be greater than 100000.")

	ov := e.get(manager, "/api/warehouse/overview")
	expect(t, ov, 200, "overview")
	cats, _ := ov.body["categories"].([]any)
	if num(ov.body["products_total"]) != 2 || len(cats) != 2 || num(obj(cats[0])["products_count"]) != 2 {
		t.Fatalf("overview %v", ov.body)
	}
	if mv, _ := ov.body["movements"].([]any); len(mv) != 1 || obj(mv[0])["note"] != "Поставка" || obj(mv[0])["product_name"] != "Cola" {
		t.Fatalf("movements %v", ov.body["movements"])
	}

	page := e.get(manager, "/api/warehouse/products?page=1&limit=1")
	if items, _ := page.body["items"].([]any); len(items) != 1 || num(page.body["total"]) != 2 {
		t.Fatalf("products page %v", page.body)
	}

	stock := e.get(cashier, "/api/stock?q=fan")
	if items, _ := stock.body["items"].([]any); len(items) != 1 || num(stock.body["total"]) != 1 || num(stock.body["per_page"]) != 50 {
		t.Fatalf("stock search %v", stock.body)
	}

	// Удаление — только владелец (warehouse.delete); SKU освобождается, история остаётся.
	expect(t, e.g2Do("DELETE", manager, fmt.Sprintf("/api/warehouse/products/%d", p)), 403, "manager delete")
	expect(t, e.g2Do("DELETE", owner, fmt.Sprintf("/api/warehouse/products/%d", p)), 200, "owner delete")
	expect(t, e.g2Do("DELETE", owner, fmt.Sprintf("/api/warehouse/products/%d", p)), 404, "deleted twice")
	if e.scalar("SELECT count(*) FROM products WHERE id = $1 AND deleted_at IS NOT NULL AND sku LIKE '%~deleted-%'", p) != 1 {
		t.Fatal("soft delete")
	}
	if num(e.get(manager, "/api/warehouse/overview").body["products_total"]) != 1 || e.activityCount("product.deleted", p) != 1 {
		t.Fatal("deleted product counted")
	}
}

func TestServicesScreen(t *testing.T) {
	e := newAdminEnv(t)
	_, manager := e.staff("Менеджер", "manager")
	_, cashier := e.staff("Кассир", "cashier")

	r := e.post(manager, "/api/services", map[string]any{"name": "  PS5 — 1 час ", "price_tiyn": 150000, "unit": " час "})
	expect(t, r, 201, "create")
	id := num(r.body["id"])
	if r.body["name"] != "PS5 — 1 час" || r.body["unit"] != "час" || r.body["is_active"] != true {
		t.Fatalf("create %v", r.body)
	}
	bad := e.post(manager, "/api/services", map[string]any{"name": "", "price_tiyn": 1_000_000_000})
	expect(t, bad, 422, "validation")
	if fieldError(bad, "name") == "" || fieldError(bad, "price") == "" {
		t.Fatalf("all field errors at once: %v", bad.body)
	}
	up := e.g2JSON("PATCH", manager, fmt.Sprintf("/api/services/%d", id), map[string]any{"name": "PS5", "price_tiyn": 100000, "unit": "", "is_active": false})
	expect(t, up, 200, "update")
	if up.body["unit"] != nil || up.body["is_active"] != false {
		t.Fatalf("update %v", up.body)
	}
	e.service("Печать", 5000, true)
	expect(t, e.get(cashier, "/api/services"), 403, "cashier list")
	list := e.get(manager, "/api/services")
	expect(t, list, 200, "list")
	items, _ := list.body["items"].([]any)
	if len(items) != 2 || obj(items[0])["name"] != "Печать" || list.body["can_create"] != true || num(list.body["total"]) != 2 {
		t.Fatalf("list (active first) %v", list.body)
	}
	expect(t, e.post(cashier, "/api/services", map[string]any{"name": "X", "price_tiyn": 1}), 403, "cashier create")
	expect(t, e.g2Do("DELETE", manager, fmt.Sprintf("/api/services/%d", id)), 200, "delete")
	if e.activityCount("service.deleted", id) != 1 || e.activityCount("service.updated", id) != 1 {
		t.Fatal("journal")
	}
}

func TestShopOrdersScreen(t *testing.T) {
	e := newAdminEnv(t)
	_, owner := e.staff("Владелец", "owner")
	_, operator := e.staff("Оператор", "operator")
	cust := e.customer(owner, "Клиент", 500000, 0)
	cola := e.product("Cola", 45000, 10)
	zone := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Зал', now(), now()) RETURNING id", e.club)
	pc := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 5, now(), now()) RETURNING id", e.club, zone)
	shopSvc := shop.New(e.db.App, nil)
	order := func() int64 {
		o, err := shopSvc.PlaceOrder(context.Background(), shop.OrderRequest{
			ClubID: e.club, UserID: cust, ComputerID: pc, Quantities: map[int64]int64{cola: 2}, IdempotencyKey: newKey(),
			Actor: billing.BalanceActor{UserID: cust, Kind: "guest"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return o.OrderID
	}
	o1, o2 := order(), order()
	if main, _ := e.balances(cust); main != 500000-180000 {
		t.Fatalf("after orders %d", main)
	}

	list := e.get(operator, "/api/shop-orders")
	expect(t, list, 200, "list")
	items, _ := list.body["items"].([]any)
	if len(items) != 2 || num(list.body["new_count"]) != 2 || num(list.body["latest"]) != o2 || list.body["status"] != "new" {
		t.Fatalf("list %v", list.body)
	}
	first := obj(items[0])
	if lines, _ := first["items"].([]any); len(lines) != 1 || num(obj(lines[0])["total_tiyn"]) != 90000 || first["computer_name"] != "PC-05" {
		t.Fatalf("order view %v", first)
	}

	done := e.post(operator, fmt.Sprintf("/api/shop-orders/%d/done", o1), nil)
	expect(t, done, 200, "done")
	if done.body["status"] != "done" || done.body["handler_name"] != "Оператор" {
		t.Fatalf("done %v", done.body)
	}
	expectField(t, e.post(operator, fmt.Sprintf("/api/shop-orders/%d/done", o1), nil), "order", shop.MsgOrderHandled)

	// Отмена — orders.delete (оператор по умолчанию имеет? — проверяем владельцем); деньги и товар возвращаются.
	key := newKey()
	c := e.postKey(owner, fmt.Sprintf("/api/shop-orders/%d/cancel", o2), key, nil)
	expect(t, c, 200, "cancel")
	again := e.postKey(owner, fmt.Sprintf("/api/shop-orders/%d/cancel", o2), key, nil)
	expect(t, again, 200, "cancel replay")
	expectField(t, e.post(owner, fmt.Sprintf("/api/shop-orders/%d/cancel", o2), nil), "order", shop.MsgOrderHandled)
	if main, _ := e.balances(cust); main != 500000-90000 {
		t.Fatalf("after cancel %d", main)
	}
	if q := e.scalar("SELECT quantity FROM products WHERE id = $1", cola); q != 8 {
		t.Fatalf("stock after cancel %d", q)
	}
	if e.scalar("SELECT count(*) FROM stock_movements WHERE product_id = $1 AND note = $2", cola, shop.CancelNote) != 1 {
		t.Fatal("cancel movement")
	}
	if e.scalar("SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'shop_order_cancel'", e.club) != 1 {
		t.Fatal("ledger cancel")
	}
	st := e.get(operator, "/api/shop-orders/state")
	if num(st.body["new"]) != 0 || num(st.body["latest"]) != o2 {
		t.Fatalf("state %v", st.body)
	}
	all := e.get(operator, "/api/shop-orders?status=all")
	if items, _ := all.body["items"].([]any); len(items) != 2 || num(all.body["total"]) != 2 {
		t.Fatalf("all %v", all.body)
	}
	if e.outboxCount("shop_order.updated") != 2 {
		t.Fatal("shop_order.updated events")
	}
}

// g2Do — запрос без тела (DELETE) с токеном.
func (e *adminEnv) g2Do(method, tok, path string) resp {
	e.t.Helper()
	return do(e.t, method, e.srv.URL+path, nil, bearer(tok))
}

// g2JSON — запрос с телом (PATCH) с токеном.
func (e *adminEnv) g2JSON(method, tok, path string, body any) resp {
	e.t.Helper()
	return do(e.t, method, e.srv.URL+path, body, bearer(tok))
}
