package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// receipt_data кассовой операции. Решение Q-003 (docs/DECISIONS.md): новые записи — формат v2 (суммы в тиынах,
// поле версии "v": 2); чтение поддерживает и формат старого кода (v1, суммы в тенге), который приходит с переносом
// данных, пока инструмент переноса не переведёт всё в v2.
//
//	v2 продажа POS: {"v": 2, "items": [{"product_id"|"service_id": id, "name", "quantity", "price_tiyn", "total_tiyn"}]}
//	v2 пополнение:  {"v": 2, "top_up": {"user_id", "balance_type", "bonus_tiyn"}}
//	v1 продажа POS: {"items": [{"product_id"|"service_id": id, "name", "quantity", "price", "total"}]}
//	v1 пополнение:  {"top_up": {"user_id", "balance_type", "bonus"}}
//
// Поля сумм v2 названы иначе, чем в v1 (price_tiyn, а не price), чтобы читатель v1 не принял тиыны за тенге.
// v1 — как писал старый код: app/Services/CashRegisterService.php sale() ('price' => (float) $product->price,
// 'total' => (float) price * qty), topUpBalance() ('bonus' => $bonus['cents'] / 100); суммы — тенге числом
// (PHP float) или строкой, в тиыны — как MySQL decimal(…,2) / PHP 8.4 (tengeOf).

// ReceiptVersion — версия формата receipt_data, который пишет encodeReceipt (решение Q-003).
const ReceiptVersion = 2

// encodeReceipt — receipt_data для записи (формат v2, решение Q-003).
func encodeReceipt(r cash.Receipt) (json.RawMessage, error) {
	out := map[string]any{"v": ReceiptVersion}
	if r.Items != nil {
		items := make([]map[string]any, 0, len(r.Items))
		for _, l := range r.Items {
			line := map[string]any{
				"name": l.Name, "quantity": l.Quantity, "price_tiyn": int64(l.Price), "total_tiyn": int64(l.Total),
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
			"user_id": r.TopUp.UserID, "balance_type": string(r.TopUp.Field), "bonus_tiyn": int64(r.TopUp.Bonus),
		}
	}
	if r.DiscountAmount != 0 {
		out["discount_amount_tiyn"] = int64(r.DiscountAmount)
	}
	return json.Marshal(out)
}

// ErrReceiptFormat — receipt_data v2 с неверными полями (суммы v2 — только целые тиыны).
var ErrReceiptFormat = errors.New("cash: malformed receipt_data v2")

// decodeReceipt читает receipt_data обоих форматов (v2 и перенесённые старые чеки v1). nil/null — нет данных.
// Не-объект (старые данные) — пустой чек: в старом коде is_array($sale->receipt_data) ? … : [].
func decodeReceipt(raw json.RawMessage) (*cash.Receipt, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var top map[string]json.RawMessage
	if err := unmarshalNumbers(raw, &top); err != nil {
		return &cash.Receipt{}, nil //nolint:nilerr // не объект — как пустой массив PHP
	}
	if isV2(top) {
		return decodeReceiptV2(top)
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

// isV2 — receipt_data в формате v2 ("v": 2).
func isV2(top map[string]json.RawMessage) bool {
	v, ok := top["v"]
	if !ok {
		return false
	}
	var n json.Number
	return unmarshalNumbers(v, &n) == nil && n.String() == strconv.Itoa(ReceiptVersion)
}

// receiptV2 — receipt_data v2 (решение Q-003).
type receiptV2 struct {
	Items *[]struct {
		ProductID int64  `json:"product_id"`
		ServiceID int64  `json:"service_id"`
		Name      string `json:"name"`
		Quantity  int64  `json:"quantity"`
		PriceTiyn int64  `json:"price_tiyn"`
		TotalTiyn int64  `json:"total_tiyn"`
	} `json:"items"`
	TopUp *struct {
		UserID      int64  `json:"user_id"`
		BalanceType string `json:"balance_type"`
		BonusTiyn   int64  `json:"bonus_tiyn"`
	} `json:"top_up"`
	DiscountAmountTiyn int64 `json:"discount_amount_tiyn"`
}

// decodeReceiptV2 — чтение формата v2: строгие типы (суммы — целые тиыны), иначе ErrReceiptFormat.
func decodeReceiptV2(top map[string]json.RawMessage) (*cash.Receipt, error) {
	raw, err := json.Marshal(top)
	if err != nil {
		return nil, err
	}
	var v receiptV2
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReceiptFormat, err)
	}
	r := &cash.Receipt{DiscountAmount: money.Tiyn(v.DiscountAmountTiyn)}
	if v.Items != nil {
		r.Items = make([]cash.ReceiptLine, 0, len(*v.Items))
		for _, it := range *v.Items {
			r.Items = append(r.Items, cash.ReceiptLine{
				ProductID: it.ProductID, ServiceID: it.ServiceID, Name: it.Name, Quantity: it.Quantity,
				Price: money.Tiyn(it.PriceTiyn), Total: money.Tiyn(it.TotalTiyn),
			})
		}
	}
	if v.TopUp != nil {
		r.TopUp = &cash.TopUpReceipt{
			UserID: v.TopUp.UserID, Field: cash.BalanceField(v.TopUp.BalanceType), Bonus: money.Tiyn(v.TopUp.BonusTiyn),
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
