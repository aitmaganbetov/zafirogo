package stock

import (
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

func wantErr(t *testing.T, err error, field, ruleOrMsg string) {
	t.Helper()
	e, ok := AsError(err)
	if !ok || e.Field != field || (e.Rule != ruleOrMsg && e.Message != ruleOrMsg) {
		t.Fatalf("want %s %q, got %v", field, ruleOrMsg, err)
	}
}

// Сценарии склада: docs/golden-scenarios.md (WarehouseTest.php, UsersTest.php, StaffApiTest.php,
// IntegrationApiResourcesTest.php, ProductDeleteTest.php). Имя подтеста = ID.
func TestGoldenStock(t *testing.T) {
	t.Run("GS-125", func(t *testing.T) { // товар с начальным остатком 7
		m, ok, err := Initial(1, 7)
		if err != nil || !ok {
			t.Fatalf("Initial: %v %v", ok, err)
		}
		if m != (Movement{ProductID: 1, Type: Receipt, Quantity: 7, BalanceAfter: 7, Note: NoteInitial, Change: 7}) {
			t.Fatalf("movement = %+v", m)
		}
		if _, ok, _ := Initial(1, 0); ok {
			t.Fatal("quantity 0 must not create a movement")
		}
	})
	t.Run("GS-128", func(t *testing.T) { // приход 7 при остатке 10
		m, err := Adjust(1, 10, Receipt, 7, 0, "Поставка")
		if err != nil || m.BalanceAfter != 17 || m.Type != Receipt || m.Quantity != 7 || m.Note != "Поставка" {
			t.Fatalf("Adjust = %+v, %v", m, err)
		}
	})
	t.Run("GS-129", func(t *testing.T) { // списание 4 из 10
		m, err := Adjust(1, 10, WriteOff, 4, 0, "")
		if err != nil || m.BalanceAfter != 6 || m.Change != -4 || m.LogNote() != NoNote {
			t.Fatalf("Adjust = %+v, %v", m, err)
		}
	})
	t.Run("GS-130", func(t *testing.T) { // списание 4 при остатке 3
		_, err := Adjust(1, 3, WriteOff, 4, 0, "")
		wantErr(t, err, "quantity", MsgWriteOffTooMuch)
	})
	t.Run("GS-143", func(t *testing.T) {
		if got := DeletedSKU("COLA-05", 42); got != "COLA-05~deleted-42" {
			t.Fatal(got)
		}
		long := strings.Repeat("ж", 250)
		if got := DeletedSKU(long, 1); got != strings.Repeat("ж", 200)+"~deleted-1" {
			t.Fatal("mb_substr по символам")
		}
	})
	for _, id := range []string{"GS-268", "GS-269"} { // кассир / оператор: «Приход товара» (max 100000)
		t.Run(id, func(t *testing.T) {
			_, err := Adjust(1, 3, Receipt, 0, MaxReceiveQuantity, "")
			wantErr(t, err, "quantity", "min")
			m, err := Adjust(1, 3, Receipt, 12, MaxReceiveQuantity, "Поставка")
			if err != nil || m.BalanceAfter != 15 {
				t.Fatalf("Adjust = %+v, %v", m, err)
			}
			_, err = Adjust(1, 3, Receipt, 100001, MaxReceiveQuantity, "")
			wantErr(t, err, "quantity", "max")
		})
	}
	t.Run("GS-279", func(t *testing.T) { // приход 5 → 8; списание 2 → 6; списание 99 → 422
		m, err := Adjust(1, 3, Receipt, 5, MaxReceiveQuantity, "")
		if err != nil || m.BalanceAfter != 8 {
			t.Fatalf("receive = %+v, %v", m, err)
		}
		m, err = Adjust(1, 8, WriteOff, 2, 0, "")
		if err != nil || m.BalanceAfter != 6 {
			t.Fatalf("write-off = %+v, %v", m, err)
		}
		_, err = Adjust(1, 6, WriteOff, 99, 0, "")
		wantErr(t, err, "quantity", MsgWriteOffTooMuch)
	})
	t.Run("GS-341", func(t *testing.T) { // API v1: списание 3 из 10 → 7; 100 → ошибка quantity
		m, err := Adjust(1, 10, WriteOff, 3, MaxReceiveQuantity, "")
		if err != nil || m.BalanceAfter != 7 {
			t.Fatalf("Adjust = %+v, %v", m, err)
		}
		_, err = Adjust(1, 7, WriteOff, 100, MaxReceiveQuantity, "")
		wantErr(t, err, "quantity", MsgWriteOffTooMuch)
	})
}

func TestAdjustRules(t *testing.T) {
	m, err := Adjust(1, 5, WriteOff, 5, 0, "")
	if err != nil || m.BalanceAfter != 0 {
		t.Fatalf("write-off to zero: %+v %v", m, err)
	}
	_, err = Adjust(1, 5, "move", 1, 0, "")
	wantErr(t, err, "type", "in")
	_, err = Adjust(1, 5, Receipt, 1, 0, strings.Repeat("я", 256))
	wantErr(t, err, "note", "max")
	if _, _, err := Initial(1, MaxInitialQuantity+1); err == nil {
		t.Fatal("initial > 1000000 must fail")
	}
	if _, _, err := Initial(1, -1); err == nil {
		t.Fatal("initial < 0 must fail")
	}
}

func TestParsePrice(t *testing.T) {
	for in, want := range map[string]money.Tiyn{"1800": 180000, "0": 0, "-0": 0, "100.005": 10001, "12.999": 1300, "9999999999.99": MaxPrice, "1.5e2": 15000} {
		got, err := ParsePrice(in)
		if err != nil || got != want {
			t.Errorf("ParsePrice(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for in, rule := range map[string]string{"": "required", "abc": "numeric", "-0.01": "min", "9999999999.994": "max", "1e20": "max"} {
		_, err := ParsePrice(in)
		wantErr(t, err, "price", rule)
	}
}

func TestProductChanges(t *testing.T) {
	old := ProductFields{SKU: "A", Name: "Cola", CategoryID: 1, Category: "Напитки", Price: 10000}
	if ch := ProductChanges(old, old); len(ch) != 0 {
		t.Fatalf("no changes expected: %v", ch)
	}
	// Цена, округлённая в ту же сумму, — не изменение (originalIsEquivalent по decimal:2).
	p, _ := ParsePrice("100.004")
	upd := old
	upd.Price = p
	if ch := ProductChanges(old, upd); len(ch) != 0 {
		t.Fatalf("same rounded price: %v", ch)
	}
	upd.Name, upd.Price = "Coca Cola", 15050
	ch := ProductChanges(old, upd)
	if len(ch) != 2 || ch[0].Field != "name" || ch[1].String() != "100.00 → 150.50" {
		t.Fatalf("changes = %v", ch)
	}
}
