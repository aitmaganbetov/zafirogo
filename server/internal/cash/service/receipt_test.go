package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
)

func TestReceiptRoundTrip(t *testing.T) {
	cases := map[string]cash.Receipt{
		"sale": {Items: []cash.ReceiptLine{
			{ProductID: 7, Name: "Coca Cola", Quantity: 3, Price: 70000, Total: 210000},
			{ServiceID: 2, Name: "PS5 — 1 час", Quantity: 2, Price: 150000, Total: 300000},
		}},
		"empty-items": {Items: []cash.ReceiptLine{}},
		"top-up":      {TopUp: &cash.TopUpReceipt{UserID: 5, Field: cash.FieldMain, Bonus: 100006}},
		"discount":    {Items: []cash.ReceiptLine{}, DiscountAmount: 150},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := encodeReceipt(r)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeReceipt(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*got, r) {
				t.Fatalf("round trip %s:\n got %+v\nwant %+v", raw, *got, r)
			}
		})
	}
}

// Q-003: суммы внутри receipt_data — тенге числом, как писал старый код ((float) price, cents / 100).
func TestReceiptLegacyFormat(t *testing.T) {
	raw, err := encodeReceipt(cash.Receipt{TopUp: &cash.TopUpReceipt{UserID: 5, Field: cash.FieldMain, Bonus: 12500}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"bonus":125.00`) {
		t.Fatalf("top_up json = %s", raw)
	}
	// Чеки старого кода: price/total — PHP float (700.0, 1000.055 не бывает: decimal(12,2)), старые чеки — без product_id.
	legacy := `{"items":[{"name":"Cola","quantity":"2","price":700.0,"total":1400}],"discount_amount":0}`
	got, err := decodeReceipt(json.RawMessage(legacy))
	if err != nil {
		t.Fatal(err)
	}
	want := cash.Receipt{Items: []cash.ReceiptLine{{Name: "Cola", Quantity: 2, Price: 70000, Total: 140000}}}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("legacy = %+v", *got)
	}
	for _, s := range []string{"", "null"} {
		if r, err := decodeReceipt(json.RawMessage(s)); err != nil || r != nil {
			t.Fatalf("%q: %v %v", s, r, err)
		}
	}
	if r, err := decodeReceipt(json.RawMessage(`"x"`)); err != nil || r == nil || r.Items != nil || r.TopUp != nil {
		t.Fatalf("not an object: %+v %v", r, err)
	}
}
