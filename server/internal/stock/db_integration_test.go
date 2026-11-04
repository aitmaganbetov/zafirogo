//go:build integration

package stock_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var fixDB *dbtest.DB

var clock = time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	d, cleanup, err := dbtest.Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fixDB = d
	code := m.Run()
	cleanup()
	os.Exit(code)
}

var (
	mu sync.Mutex
	n  int
)

func uniq(p string) string {
	mu.Lock()
	defer mu.Unlock()
	n++
	return fmt.Sprintf("%s-%d", p, n)
}

type env struct {
	t        *testing.T
	club     int64
	user     int64
	category int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, club: fixDB.Club(t, uniq("stock"))}
	e.user = fixDB.Customer(t, e.club, "Сотрудник")
	e.category = e.newCategory("Десерты")
	return e
}

func (e *env) newCategory(name string) int64 {
	var id int64
	if err := fixDB.Owner.QueryRow(context.Background(),
		"INSERT INTO product_categories (club_id, name, created_at, updated_at) VALUES ($1, $2, now(), now()) RETURNING id",
		e.club, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) tx(fn func(ctx context.Context, q *store.Queries) error) error {
	ctx := context.Background()
	return db.InTenantTx(ctx, fixDB.App, e.club, func(q *store.Queries) error { return fn(ctx, q) })
}

func (e *env) create(sku, name string, price money.Tiyn, qty int64) (stock.CreateProductResult, error) {
	var res stock.CreateProductResult
	err := e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = stock.CreateProduct(ctx, q, stock.CreateProductParams{
			ClubID: e.club, UserID: e.user, SKU: sku, Name: name, CategoryID: e.category, Price: price, Quantity: qty, Now: clock,
		})
		return err
	})
	return res, err
}

func (e *env) mustCreate(name string, qty int64) int64 {
	e.t.Helper()
	res, err := e.create(uniq("SKU"), name, 35000, qty)
	if err != nil {
		e.t.Fatal(err)
	}
	return res.Product.ID
}

func (e *env) adjust(product int64, typ stock.MovementType, qty, maxQty int64, note string) (stock.AdjustResult, error) {
	var res stock.AdjustResult
	err := e.tx(func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = stock.AdjustStock(ctx, q, stock.AdjustParams{
			ClubID: e.club, ProductID: product, UserID: e.user, Type: typ, Quantity: qty, MaxQuantity: maxQty, Note: note, Now: clock,
		})
		return err
	})
	return res, err
}

func (e *env) quantity(product int64) int64 {
	e.t.Helper()
	var q int64
	if err := fixDB.Owner.QueryRow(context.Background(), "SELECT quantity FROM products WHERE id = $1", product).Scan(&q); err != nil {
		e.t.Fatal(err)
	}
	return q
}

func (e *env) movements(product int64) int64 {
	e.t.Helper()
	var c int64
	if err := fixDB.Owner.QueryRow(context.Background(), "SELECT count(*) FROM stock_movements WHERE product_id = $1", product).Scan(&c); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func wantErr(t *testing.T, err error, field, rule, msg string) {
	t.Helper()
	e, ok := stock.AsError(err)
	if !ok || e.Field != field || e.Rule != rule || e.Message != msg {
		t.Fatalf("err = %v, want %s %s%s", err, field, rule, msg)
	}
}

func TestStockDB(t *testing.T) {
	t.Run("GS-125", func(t *testing.T) {
		e := newEnv(t)
		res, err := e.create("DST-001", "Чизкейк", 180000, 7)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "sku", res.Product.Sku, "DST-001")
		eq(t, "category", res.Product.Category, "Десерты")
		eq(t, "category id", *res.Product.ProductCategoryID, e.category)
		eq(t, "quantity", res.Product.Quantity, 7)
		eq(t, "movement type", res.Movement.Type, "receipt")
		eq(t, "movement qty", res.Movement.Quantity, 7)
		eq(t, "balance_after", res.Movement.BalanceAfter, 7)
		eq(t, "note", *res.Movement.Note, stock.NoteInitial)
		eq(t, "created_at", res.Product.CreatedAt.Equal(clock), true)
		// Нулевой остаток — без движения.
		zero, err := e.create("DST-002", "Пусто", 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "no movement", zero.Movement == nil, true)
		eq(t, "movements", e.movements(zero.Product.ID), 0)
	})
	t.Run("GS-128", func(t *testing.T) {
		e := newEnv(t)
		p := e.mustCreate("Cola", 10)
		res, err := e.adjust(p, stock.Receipt, 7, 0, "Поставка")
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "quantity", e.quantity(p), 17)
		eq(t, "balance_after", res.Row.BalanceAfter, 17)
		eq(t, "note", *res.Row.Note, "Поставка")
		eq(t, "type", res.Row.Type, "receipt")
		eq(t, "user", *res.Row.UserID, e.user)
	})
	t.Run("GS-129", func(t *testing.T) {
		e := newEnv(t)
		p := e.mustCreate("Cola", 10)
		res, err := e.adjust(p, stock.WriteOff, 4, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "quantity", e.quantity(p), 6)
		eq(t, "balance_after", res.Row.BalanceAfter, 6)
		eq(t, "note NULL", res.Row.Note == nil, true)
		eq(t, "log note", res.Movement.LogNote(), stock.NoNote)
	})
	t.Run("GS-130", func(t *testing.T) {
		e := newEnv(t)
		p := e.mustCreate("Cola", 3)
		before := e.movements(p)
		_, err := e.adjust(p, stock.WriteOff, 4, 0, "")
		wantErr(t, err, "quantity", "", stock.MsgWriteOffTooMuch)
		eq(t, "quantity", e.quantity(p), 3)
		eq(t, "movements", e.movements(p), before)
		// До нуля — можно.
		if _, err := e.adjust(p, stock.WriteOff, 3, 0, ""); err != nil {
			t.Fatal(err)
		}
		eq(t, "zero", e.quantity(p), 0)
	})
	t.Run("GS-268", func(t *testing.T) {
		e := newEnv(t)
		p := e.mustCreate("Coca Cola", 3)
		_, err := e.adjust(p, stock.Receipt, 0, stock.MaxReceiveQuantity, "")
		wantErr(t, err, "quantity", "min", "")
		_, err = e.adjust(p, stock.Receipt, stock.MaxReceiveQuantity+1, stock.MaxReceiveQuantity, "")
		wantErr(t, err, "quantity", "max", "")
		if _, err := e.adjust(p, stock.Receipt, 12, stock.MaxReceiveQuantity, "Поставка"); err != nil {
			t.Fatal(err)
		}
		eq(t, "quantity", e.quantity(p), 15)
	})
	t.Run("GS-279", func(t *testing.T) {
		e := newEnv(t)
		p := e.mustCreate("Cola", 3)
		if _, err := e.adjust(p, stock.Receipt, 5, stock.MaxReceiveQuantity, ""); err != nil {
			t.Fatal(err)
		}
		eq(t, "after receive", e.quantity(p), 8)
		if _, err := e.adjust(p, stock.WriteOff, 2, 0, ""); err != nil {
			t.Fatal(err)
		}
		eq(t, "after write-off", e.quantity(p), 6)
		_, err := e.adjust(p, stock.WriteOff, 99, 0, "")
		wantErr(t, err, "quantity", "", stock.MsgWriteOffTooMuch)
	})
	t.Run("GS-339", func(t *testing.T) {
		e := newEnv(t)
		res, err := e.create("API-1", "Api tea", 25050, 4)
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.create("API-1", "Другой", 100, 0)
		wantErr(t, err, "sku", "unique", "")
		// Тот же SKU в другом клубе — можно (GS-340).
		if _, err := newEnv(t).create("API-1", "Чужой", 100, 0); err != nil {
			t.Fatal(err)
		}
		other := e.newCategory("Mine drinks")
		price := money.Tiyn(30000)
		name := "Api tea 2"
		var upd stock.UpdateProductResult
		if err := e.tx(func(ctx context.Context, q *store.Queries) error {
			var err error
			upd, err = stock.UpdateProduct(ctx, q, stock.UpdateProductParams{
				ClubID: e.club, ProductID: res.Product.ID, Name: &name, Price: &price, CategoryID: &other, Now: clock,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		eq(t, "price", money.Tiyn(upd.Product.Price).Tenge(), "300.00")
		eq(t, "category", upd.Product.Category, "Mine drinks")
		eq(t, "quantity unchanged", e.quantity(res.Product.ID), 4)
		var fields []string
		for _, c := range upd.Changes {
			fields = append(fields, c.Field+": "+c.String())
		}
		eq(t, "changes", fmt.Sprint(fields), fmt.Sprint([]string{
			"name: Api tea → Api tea 2",
			fmt.Sprintf("product_category_id: %d → %d", e.category, other),
			"category: Десерты → Mine drinks",
			"price: 250.50 → 300.00",
		}))
		// Без изменений — ничего не пишется.
		if err := e.tx(func(ctx context.Context, q *store.Queries) error {
			var err error
			upd, err = stock.UpdateProduct(ctx, q, stock.UpdateProductParams{ClubID: e.club, ProductID: res.Product.ID, Price: &price})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		eq(t, "no changes", len(upd.Changes), 0)
	})
	t.Run("not-found", func(t *testing.T) {
		e := newEnv(t)
		p := e.mustCreate("Cola", 3)
		if _, err := fixDB.Owner.Exec(context.Background(), "UPDATE products SET deleted_at = now() WHERE id = $1", p); err != nil {
			t.Fatal(err)
		}
		_, err := e.adjust(p, stock.Receipt, 1, 0, "")
		if !errors.Is(err, stock.ErrProductNotFound) {
			t.Fatalf("deleted product: %v", err)
		}
		other := newEnv(t)
		q2 := other.mustCreate("Чужой", 3)
		_, err = e.adjust(q2, stock.Receipt, 1, 0, "")
		if !errors.Is(err, stock.ErrProductNotFound) {
			t.Fatalf("other club product: %v", err)
		}
		_, err = e.create(uniq("SKU"), "X", 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		e.category = other.category // категория чужого клуба
		_, err = e.create(uniq("SKU"), "X", 100, 0)
		wantErr(t, err, "product_category_id", "exists", "")
	})
	t.Run("concurrent-write-offs", func(t *testing.T) {
		// Остаток не уходит в минус: пять списаний по 1 из 3 — три успешны.
		e := newEnv(t)
		p := e.mustCreate("Cola", 3)
		errs := make([]error, 5)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = e.adjust(p, stock.WriteOff, 1, 0, "")
			}()
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else {
				wantErr(t, err, "quantity", "", stock.MsgWriteOffTooMuch)
			}
		}
		eq(t, "succeeded", ok, 3)
		eq(t, "quantity", e.quantity(p), 0)
	})
}
