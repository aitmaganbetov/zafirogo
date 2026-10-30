//go:build integration

package shop_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var fixDB *dbtest.DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	d, cleanup, err := dbtest.Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := db.EnsurePartitions(ctx, d.Owner, time.Now(), 2); err != nil {
		fmt.Fprintln(os.Stderr, err)
		cleanup()
		os.Exit(1)
	}
	fixDB = d
	code := m.Run()
	cleanup()
	os.Exit(code)
}

var seq atomic.Int64

// shopEnv — подготовка AgentShopTest.php (shopSetup()): клиент «Арман» 1000.00 / 300.00, зона «Hall», ПК №5,
// «Cola» (COLA) 350 ₸ × 10, «Чипсы» (CHIPS) 250.50 ₸ × 1.
type shopEnv struct {
	t              *testing.T
	club, user, pc int64
	cola, chips    int64
	svc            *shop.Service
	keyN           int
}

func newShopEnv(t *testing.T) *shopEnv { return newShopEnvWith(t, 100000) }

func newShopEnvWith(t *testing.T, main money.Tiyn) *shopEnv {
	t.Helper()
	ctx := context.Background()
	e := &shopEnv{t: t, svc: shop.New(fixDB.App, nil)}
	e.club = fixDB.Club(t, "shop-"+strconv.FormatInt(seq.Add(1), 10)+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	var zone int64
	e.scan(&zone, "INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Hall', now(), now()) RETURNING id", e.club)
	e.scan(&e.pc, "INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 5, now(), now()) RETURNING id", e.club, zone)
	e.scan(&e.cola, "INSERT INTO products (club_id, sku, name, category, price, quantity, created_at, updated_at) VALUES ($1, 'COLA', 'Cola', 'Напитки', 35000, 10, now(), now()) RETURNING id", e.club)
	e.scan(&e.chips, "INSERT INTO products (club_id, sku, name, category, price, quantity, created_at, updated_at) VALUES ($1, 'CHIPS', 'Чипсы', 'Снеки', 25050, 1, now(), now()) RETURNING id", e.club)
	e.user = fixDB.Customer(t, e.club, "Арман")
	if err := db.InTenantTx(ctx, fixDB.App, e.club, func(q *store.Queries) error {
		_, err := billing.SetInitialBalance(ctx, q, billing.InitialBalanceParams{ClubID: e.club, UserID: e.user, Main: main, Bonus: 30000})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *shopEnv) scan(dst any, sql string, args ...any) {
	e.t.Helper()
	if err := fixDB.Owner.QueryRow(context.Background(), sql, args...).Scan(dst); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *shopEnv) int(sql string, args ...any) int64 {
	e.t.Helper()
	var v int64
	e.scan(&v, sql, args...)
	return v
}

func (e *shopEnv) order(q map[int64]int64) (shop.Order, error) {
	e.keyN++
	return e.svc.PlaceOrder(context.Background(), shop.OrderRequest{ClubID: e.club, UserID: e.user, ComputerID: e.pc, Quantities: q,
		IdempotencyKey: "test:" + strconv.FormatInt(e.club, 10) + ":" + strconv.Itoa(e.keyN), Actor: billing.BalanceActor{UserID: e.user, Kind: "guest"}})
}

func (e *shopEnv) wantBalances(main, bonus money.Tiyn) {
	e.t.Helper()
	m, b := fixDB.Balances(e.t, e.club, e.user)
	if money.Tiyn(m) != main || money.Tiyn(b) != bonus {
		e.t.Fatalf("balances = %s / %s, want %s / %s", money.Tiyn(m), money.Tiyn(b), main, bonus)
	}
}

func (e *shopEnv) noDrift() {
	e.t.Helper()
	if err := db.InTenantTx(context.Background(), fixDB.App, e.club, func(q *store.Queries) error {
		rows, err := q.ListMemberBalanceDrift(context.Background(), e.club)
		if err == nil && len(rows) > 0 {
			err = fmt.Errorf("drift: %+v", rows)
		}
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
}

func TestGoldenAgentShop(t *testing.T) {
	// GS-072: 2 × 350 + 1 × 250.50 = 950.50 с основного; бонусы не трогаются; остатки 8 и 0; заказ new.
	t.Run("GS-072", func(t *testing.T) {
		e := newShopEnv(t)
		o, err := e.order(map[int64]int64{e.cola: 2, e.chips: 1})
		if err != nil {
			t.Fatal(err)
		}
		if o.Amount != 95050 || o.MainBalance != 4950 || o.BonusBalance != 30000 {
			t.Fatalf("order = %+v", o)
		}
		e.wantBalances(4950, 30000)
		if c, ch := e.int("SELECT quantity FROM products WHERE id = $1", e.cola), e.int("SELECT quantity FROM products WHERE id = $1", e.chips); c != 8 || ch != 0 {
			t.Fatalf("stock = %d, %d", c, ch)
		}
		if n := e.int("SELECT count(*) FROM stock_movements WHERE club_id = $1 AND type = 'write_off' AND note = 'Заказ из агента'", e.club); n != 2 {
			t.Fatalf("movements = %d", n)
		}
		var status, name string
		var amount, pc int64
		e.scan(&status, "SELECT status FROM shop_orders WHERE id = $1", o.OrderID)
		e.scan(&name, "SELECT computer_name FROM shop_orders WHERE id = $1", o.OrderID)
		e.scan(&amount, "SELECT amount FROM shop_orders WHERE id = $1", o.OrderID)
		e.scan(&pc, "SELECT club_computer_id FROM shop_orders WHERE id = $1", o.OrderID)
		if status != "new" || name != "PC-05" || amount != 95050 || pc != e.pc {
			t.Fatalf("shop_orders = %s %s %d %d", status, name, amount, pc)
		}
		// Проводка shop_order: member_main −950.50 / revenue_shop +950.50.
		if n := e.int(`SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'shop_order' AND shop_order_id = $2`, e.club, o.OrderID); n != 1 {
			t.Fatalf("ledger shop_order = %d", n)
		}
		e.noDrift()
	})

	// GS-073: сверх остатка, сверх основного (бонусы не считаются), неизвестный товар — ничего не списано.
	t.Run("GS-073", func(t *testing.T) {
		e := newShopEnv(t)
		cases := []struct {
			q    map[int64]int64
			want error
		}{
			{map[int64]int64{e.chips: 2}, shop.ErrNotEnoughStock},
			{map[int64]int64{e.cola: 3}, shop.ErrNotEnoughMain}, // 1050 > 1000 основного, бонусы 300 не в счёт
			{map[int64]int64{999999: 1}, shop.ErrProductGone},
		}
		for _, c := range cases {
			if _, err := e.order(c.q); !errors.Is(err, c.want) {
				t.Fatalf("order %v: err = %v, want %v", c.q, err, c.want)
			}
		}
		e.wantBalances(100000, 30000)
		if c := e.int("SELECT quantity FROM products WHERE id = $1", e.cola); c != 10 {
			t.Fatalf("cola = %d", c)
		}
		if n := e.int("SELECT count(*) FROM shop_orders WHERE club_id = $1", e.club); n != 0 {
			t.Fatalf("orders = %d", n)
		}
	})

	// GS-036 (первая половина): заказ из агента — траты лояльности (total_spent 700.00), основной 5000 → 4300.
	t.Run("GS-036", func(t *testing.T) {
		e := newShopEnvWith(t, 500000)
		e.wantBalances(500000, 30000)
		if _, err := e.order(map[int64]int64{e.cola: 2}); err != nil {
			t.Fatal(err)
		}
		e.wantBalances(430000, 30000)
		if s := e.int("SELECT total_spent FROM club_memberships WHERE club_id = $1 AND user_id = $2", e.club, e.user); s != 70000 {
			t.Fatalf("total_spent = %d", s)
		}
	})
}

// Повтор с тем же ключом не списывает второй раз; тот же ключ с другим составом — конфликт.
func TestPlaceOrderIdempotent(t *testing.T) {
	e := newShopEnv(t)
	req := shop.OrderRequest{ClubID: e.club, UserID: e.user, ComputerID: e.pc, Quantities: map[int64]int64{e.cola: 1},
		IdempotencyKey: "same-key", Actor: billing.BalanceActor{UserID: e.user, Kind: "guest"}}
	first, err := e.svc.PlaceOrder(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.svc.PlaceOrder(context.Background(), req)
	if err != nil || !again.Replayed || again.OrderID != first.OrderID || again.Amount != 35000 {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	e.wantBalances(65000, 30000)
	req.Quantities = map[int64]int64{e.cola: 2}
	if _, err := e.svc.PlaceOrder(context.Background(), req); !errors.Is(err, shop.ErrIdempotencyConflict) {
		t.Fatalf("conflict err = %v", err)
	}
	if n := e.int("SELECT count(*) FROM shop_orders WHERE club_id = $1", e.club); n != 1 {
		t.Fatalf("orders = %d", n)
	}
}

func TestListProductsKeyset(t *testing.T) {
	e := newShopEnv(t)
	for i := 0; i < 5; i++ {
		var id int64
		e.scan(&id, "INSERT INTO products (club_id, sku, name, category, price, quantity, created_at, updated_at) VALUES ($1, $2, 'Вода', 'Напитки', 10000, 0, now(), now()) RETURNING id",
			e.club, "W"+strconv.Itoa(i))
	}
	var deleted int64
	e.scan(&deleted, "INSERT INTO products (club_id, sku, name, category, price, quantity, deleted_at, created_at, updated_at) VALUES ($1, 'OLD', 'Аааа', 'x', 1, 1, now(), now(), now()) RETURNING id", e.club)
	var names []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		p, err := e.svc.ListProducts(context.Background(), e.club, 3, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, pr := range p.Products {
			names = append(names, pr.Name)
		}
		if p.Next == "" {
			break
		}
		cursor = p.Next
	}
	want := []string{"Cola", "Вода", "Вода", "Вода", "Вода", "Вода", "Чипсы"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if _, err := e.svc.ListProducts(context.Background(), e.club, 3, "garbage!"); !errors.Is(err, shop.ErrInvalidInput) {
		t.Fatalf("bad cursor err = %v", err)
	}
}
