package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// fractionalNumbers — JSON-числа с дробной частью или экспонентой (float) в документе.
func fractionalNumbers(t *testing.T, doc []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var out []string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if n, ok := tok.(json.Number); ok && strings.ContainsAny(n.String(), ".eE") {
			out = append(out, n.String())
		}
	}
}

// Решение RQ-3 (docs/DECISIONS.md): суммы в вебхуках — строки с двумя знаками плюс *_tiyn, float нет.
func TestNormalizeMoney(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			// sale.completed, как его пишет касса (cash/service/sale.go): price/total строк чека — PHPFloat.
			name: "sale.completed",
			in: `{"amount":"1500.50","amount_tiyn":150050,"cash_shift_id":3,"items":[` +
				`{"name":"Кола","price":750.25,"price_tiyn":75025,"product_id":7,"quantity":2,"total":1500.5,"total_tiyn":150050},` +
				`{"name":"Вода","price":300.0,"price_tiyn":30000,"quantity":1,"service_id":2,"total":300.0,"total_tiyn":30000}],"sale_id":9}`,
			want: `{"amount":"1500.50","amount_tiyn":150050,"cash_shift_id":3,"items":[` +
				`{"name":"Кола","price":"750.25","price_tiyn":75025,"product_id":7,"quantity":2,"total":"1500.50","total_tiyn":150050},` +
				`{"name":"Вода","price":"300.00","price_tiyn":30000,"quantity":1,"service_id":2,"total":"300.00","total_tiyn":30000}],"sale_id":9}`,
		},
		{
			// product.created/updated: price — (string) $product->price, *_tiyn добавляется.
			name: "product.updated",
			in:   `{"changed":["price"],"name":"Чипсы","price":"150.00","product_id":1,"sku":"A1"}`,
			want: `{"changed":["price"],"name":"Чипсы","price":"150.00","price_tiyn":15000,"product_id":1,"sku":"A1"}`,
		},
		{
			// Число-сумма без *_tiyn — переводится точно (без float).
			name: "number without tiyn",
			in:   `{"amount":0.1}`,
			want: `{"amount":"0.10","amount_tiyn":10}`,
		},
		{
			// Источник правды — *_tiyn.
			name: "tiyn wins",
			in:   `{"amount":"1.00","amount_tiyn":250}`,
			want: `{"amount":"2.50","amount_tiyn":250}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeMoney(json.RawMessage(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
			if f := fractionalNumbers(t, got); len(f) > 0 {
				t.Fatalf("float numbers in webhook data: %v", f)
			}
		})
	}

	// Без сумм — байты не меняются (порядок ключей как в событии).
	stock := `{"product_id":1,"sku":"A1","change":-2,"quantity":5,"reason":"sale"}`
	if got, err := normalizeMoney(json.RawMessage(stock)); err != nil || string(got) != stock {
		t.Fatalf("stock.changed: %s, %v", got, err)
	}
	// Уже в формате RQ-3 — тоже без изменений.
	done := `{"price":"150.00","price_tiyn":15000}`
	if got, err := normalizeMoney(json.RawMessage(done)); err != nil || string(got) != done {
		t.Fatalf("normalized: %s, %v", got, err)
	}
	for _, bad := range []string{`{"amount":"1.005"}`, `{"amount":true}`, `{"amount":"1.00","amount_tiyn":1.5}`, `{`} {
		if _, err := normalizeMoney(json.RawMessage(bad)); !errors.Is(err, errBadPayload) {
			t.Errorf("%s: want errBadPayload, got %v", bad, err)
		}
	}
}
