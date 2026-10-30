package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// receipt_data кассовой операции — JSON того же вида, что в старом коде (допущение Этапа 1: Q-003 — формат JSON
// в БД не меняем, суммы внутри — тенге числом; в тиыны — на границе чтения):
//
//	продажа POS: {"items": [{"product_id"|"service_id": id, "name", "quantity", "price", "total"}]}
//	пополнение:  {"top_up": {"user_id", "balance_type", "bonus"}}
//
// Laravel: app/Services/CashRegisterService.php sale() ('price' => (float) $product->price, 'total' => (float) price * qty),
// topUpBalance() ('bonus' => $bonus['cents'] / 100). Числа пишем десятичной записью с двумя знаками ("700.00"):
// значение то же, что у PHP-float из decimal(…,2).

// encodeReceipt — receipt_data для записи.
func encodeReceipt(r cash.Receipt) (json.RawMessage, error) {
	out := map[string]any{}
	if r.Items != nil {
		items := make([]map[string]any, 0, len(r.Items))
		for _, l := range r.Items {
			line := map[string]any{
				"name": l.Name, "quantity": l.Quantity,
				"price": json.Number(l.Price.Tenge()), "total": json.Number(l.Total.Tenge()),
			}
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
		out["top_up"] = map[string]any{
			"user_id": r.TopUp.UserID, "balance_type": string(r.TopUp.Field), "bonus": json.Number(r.TopUp.Bonus.Tenge()),
		}
	}
	if r.DiscountAmount != 0 {
		out["discount_amount"] = json.Number(r.DiscountAmount.Tenge())
	}
	return json.Marshal(out)
}

// decodeReceipt читает receipt_data (в том числе перенесённые старые чеки). nil/null — нет данных.
// Не-объект (старые данные) — пустой чек: в старом коде is_array($sale->receipt_data) ? … : [].
func decodeReceipt(raw json.RawMessage) (*cash.Receipt, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var top map[string]json.RawMessage
	if err := unmarshalNumbers(raw, &top); err != nil {
		return &cash.Receipt{}, nil //nolint:nilerr // не объект — как пустой массив PHP
	}
	r := &cash.Receipt{}
	if itemsRaw, ok := top["items"]; ok {
		var items []map[string]any
		if err := unmarshalNumbers(itemsRaw, &items); err == nil {
			r.Items = make([]cash.ReceiptLine, 0, len(items))
			for _, it := range items {
				l := cash.ReceiptLine{
					ProductID: intOf(it["product_id"]),
					ServiceID: intOf(it["service_id"]),
					Name:      stringOf(it["name"]),
					Quantity:  intOf(it["quantity"]),
				}
				var err error
				if l.Price, err = tengeOf(it["price"]); err != nil {
					return nil, err
				}
				if l.Total, err = tengeOf(it["total"]); err != nil {
					return nil, err
				}
				r.Items = append(r.Items, l)
			}
		}
	}
	if topRaw, ok := top["top_up"]; ok {
		var t map[string]any
		if err := unmarshalNumbers(topRaw, &t); err == nil && t != nil {
			bonus, err := tengeOf(t["bonus"])
			if err != nil {
				return nil, err
			}
			r.TopUp = &cash.TopUpReceipt{
				UserID: intOf(t["user_id"]),
				Field:  cash.BalanceField(stringOf(t["balance_type"])),
				Bonus:  bonus,
			}
		}
	}
	if d, ok := top["discount_amount"]; ok {
		var v any
		if err := unmarshalNumbers(d, &v); err == nil {
			if r.DiscountAmount, err = tengeOf(v); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

func unmarshalNumbers(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

// intOf — (int) значения JSON, как PHP: число (целая часть), числовая строка; остальное — 0.
func intOf(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return money.PHPIntCast(f)
		}
	case string:
		return money.PHPStringToInt(x)
	case bool:
		if x {
			return 1
		}
	}
	return 0
}

func stringOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}

// tengeOf — сумма чека (тенге числом или строкой) в тиынах: decimal-запись, округление как MySQL decimal(…,2).
func tengeOf(v any) (money.Tiyn, error) {
	var s string
	switch x := v.(type) {
	case nil:
		return 0, nil
	case json.Number:
		s = x.String()
	case string:
		s = strings.TrimSpace(x)
		if s == "" {
			return 0, nil
		}
	default:
		return 0, nil
	}
	t, err := money.ParseTengeRounded(s)
	if err != nil {
		// Экспонента и т.п. — через float, как (float) в PHP.
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return 0, fmt.Errorf("cash: receipt amount %q: %w", s, err)
		}
		return money.FromTengeFloatDecimal(f)
	}
	return t, nil
}
