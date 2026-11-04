package shop

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Решение Q-003 (docs/DECISIONS.md): shop_orders.items пишется в формате v2 (тиыны, "v": 2).
func TestEncodeItemsV2(t *testing.T) {
	raw, err := EncodeItems([]Line{{ProductID: 3, Name: "Cola", Quantity: 2, Price: 35000, Total: 70000}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":2,"items":[{"product_id":3,"name":"Cola","quantity":2,"price_tiyn":35000,"total_tiyn":70000}]}`
	if string(raw) != want {
		t.Fatalf("items = %s\nwant %s", raw, want)
	}
}

// Решение Q-003: v1 (список старого кода, тенге строками formatCents или числами) и v2 читаются в одни и те же
// строки заказа.
func TestDecodeItemsBothFormats(t *testing.T) {
	for _, a := range []money.Tiyn{0, 1, 5, 99, 100, 25050, 35000, 999_999_999_999} {
		lines := []Line{{ProductID: 3, Name: "Cola", Quantity: 2, Price: a, Total: a * 2}, {ProductID: 4, Name: "Chips", Quantity: 1, Price: a, Total: a}}
		v2, err := EncodeItems(lines)
		if err != nil {
			t.Fatal(err)
		}
		legacy := make([]map[string]any, len(lines))
		for i, l := range lines {
			legacy[i] = map[string]any{"product_id": l.ProductID, "name": l.Name, "quantity": l.Quantity,
				"price": formatCents(l.Price), "total": formatCents(l.Total)}
		}
		v1, _ := json.Marshal(legacy)
		for name, raw := range map[string]json.RawMessage{"v1": v1, "v2": v2} {
			got, err := DecodeItems(raw)
			if err != nil {
				t.Fatalf("%s %s: %v", name, raw, err)
			}
			if !reflect.DeepEqual(got, lines) {
				t.Fatalf("%s %s:\n got %+v\nwant %+v", name, raw, got, lines)
			}
		}
	}
	// v1 с числами (PHP float) и строковым количеством.
	got, err := DecodeItems(json.RawMessage(`[{"product_id":"7","name":"Б","quantity":"3","price":0.1,"total":0.30000000000000004}]`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Line{{ProductID: 7, Name: "Б", Quantity: 3, Price: 10, Total: 30}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("v1 floats = %+v", got)
	}
	for _, s := range []string{"", "null"} {
		if got, err := DecodeItems(json.RawMessage(s)); err != nil || got != nil {
			t.Fatalf("%q: %v %v", s, got, err)
		}
	}
	for _, bad := range []string{
		`{"v":3,"items":[]}`,
		`{"items":[]}`,
		`{"v":2,"items":[{"product_id":1,"name":"x","quantity":1,"price_tiyn":1.5,"total_tiyn":1}]}`,
		`[{"price":"abc"}]`,
		`"x"`,
	} {
		if _, err := DecodeItems(json.RawMessage(bad)); !errors.Is(err, ErrItemsFormat) {
			t.Fatalf("%s: err = %v", bad, err)
		}
	}
}
