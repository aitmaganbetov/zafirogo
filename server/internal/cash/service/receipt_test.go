package service

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
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

// Решение Q-003 (docs/DECISIONS.md): новые чеки пишутся в формате v2 — тиыны и "v": 2.
func TestReceiptWritesV2(t *testing.T) {
	raw, err := encodeReceipt(cash.Receipt{
		Items: []cash.ReceiptLine{{ProductID: 7, Name: "Cola", Quantity: 3, Price: 70010, Total: 210030}},
		TopUp: &cash.TopUpReceipt{UserID: 5, Field: cash.FieldMain, Bonus: 12500},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"v":      float64(2),
		"items":  []any{map[string]any{"product_id": float64(7), "name": "Cola", "quantity": float64(3), "price_tiyn": float64(70010), "total_tiyn": float64(210030)}},
		"top_up": map[string]any{"user_id": float64(5), "balance_type": "balance", "bonus_tiyn": float64(12500)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("receipt v2 = %s", raw)
	}
	// Суммы v2 — только целые тиыны.
	for _, bad := range []string{
		`{"v":2,"items":[{"name":"x","quantity":1,"price_tiyn":1.5,"total_tiyn":1}]}`,
		`{"v":2,"top_up":{"user_id":1,"balance_type":"balance","bonus_tiyn":"100"}}`,
	} {
		if _, err := decodeReceipt(json.RawMessage(bad)); !errors.Is(err, ErrReceiptFormat) {
			t.Fatalf("%s: err = %v", bad, err)
		}
	}
}

// encodeReceiptV1 — receipt_data, как его писал старый код и Этап 1 до решения Q-003: суммы — тенге числом
// ("700.00" — значение PHP-float из decimal(…,2)). Нужен, чтобы сверить чтение обоих форматов.
func encodeReceiptV1(r cash.Receipt) json.RawMessage {
	out := map[string]any{}
	if r.Items != nil {
		items := make([]map[string]any, 0, len(r.Items))
		for _, l := range r.Items {
			line := map[string]any{"name": l.Name, "quantity": l.Quantity,
				"price": json.Number(l.Price.Tenge()), "total": json.Number(l.Total.Tenge())}
			if l.ServiceID != 0 {
				line["service_id"] = l.ServiceID
			} else if l.ProductID != 0 {
				line["product_id"] = l.ProductID
			}
			items = append(items, line)
		}
		out["items"] = items
	}
	if r.TopUp != nil {
		out["top_up"] = map[string]any{"user_id": r.TopUp.UserID, "balance_type": string(r.TopUp.Field), "bonus": json.Number(r.TopUp.Bonus.Tenge())}
	}
	if r.DiscountAmount != 0 {
		out["discount_amount"] = json.Number(r.DiscountAmount.Tenge())
	}
	raw, _ := json.Marshal(out)
	return raw
}

// Решение Q-003: чек v1 (тенге) и v2 (тиыны) с теми же суммами читаются в один и тот же cash.Receipt —
// X/Z-отчёт, чек, возвраты и идемпотентные повторы не зависят от формата записи.
func TestReceiptV1AndV2ReadTheSame(t *testing.T) {
	amounts := []money.Tiyn{0, 1, 5, 10, 99, 100, 101, 1005, 70000, 70010, 123456789, 999_999_999_999, 99_999_999_999_999}
	for _, a := range amounts {
		r := cash.Receipt{
			Items: []cash.ReceiptLine{
				{ProductID: 7, Name: "Cola", Quantity: 3, Price: a, Total: a * 3},
				{ServiceID: 2, Name: "PS5", Quantity: 1, Price: a, Total: a},
				{Name: "старый", Quantity: 1, Price: a, Total: a},
			},
			TopUp:          &cash.TopUpReceipt{UserID: 5, Field: cash.FieldBonus, Bonus: a},
			DiscountAmount: a,
		}
		v2, err := encodeReceipt(r)
		if err != nil {
			t.Fatal(err)
		}
		got2, err := decodeReceipt(v2)
		if err != nil {
			t.Fatal(err)
		}
		got1, err := decodeReceipt(encodeReceiptV1(r))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(*got1, *got2) || !reflect.DeepEqual(*got2, r) {
			t.Fatalf("%d: v1 %+v\nv2 %+v\nwant %+v", a, *got1, *got2, r)
		}
	}
	// v1 из старого PHP: (float) price * qty даёт «некрасивые» float — PHP 8.4 / MySQL decimal(…,2).
	got, err := decodeReceipt(json.RawMessage(`{"items":[{"name":"Б","quantity":3,"price":0.1,"total":0.30000000000000004}],"top_up":{"user_id":1,"balance_type":"balance","bonus":1000.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Items[0].Price != 10 || got.Items[0].Total != 30 || got.TopUp.Bonus != 100050 {
		t.Fatalf("v1 floats = %+v %+v", got.Items[0], *got.TopUp)
	}
}

func TestReceiptLegacyFormat(t *testing.T) {
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
