package cash

import (
	"strconv"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// SaleItem — строка запроса продажи POS: товар ИЛИ услуга и количество.
// Laravel: app/Http/Requests/StorePosSaleRequest.php rules() — items.*.product_id required_without service_id,
// items.*.service_id required_without product_id, оба distinct; items.*.quantity required|integer|min:1.
type SaleItem struct {
	ProductID int64 // 0 — не задан
	ServiceID int64 // 0 — не задан
	Quantity  int64
}

// ValidateSaleItems — правила формы продажи, которые не требуют БД (существование товара/услуги в клубе,
// is_active услуги и deleted_at товара проверяет вызывающий: Rule::exists(...)).
// Ошибки — по первому нарушению в порядке полей, как items, items.N.product_id, items.N.quantity.
func ValidateSaleItems(items []SaleItem) error {
	if len(items) == 0 {
		return ruleErr("items", RuleRequired)
	}
	seenProduct := map[int64]bool{}
	seenService := map[int64]bool{}
	for i, it := range items {
		p := "items." + strconv.Itoa(i) + "."
		if it.ProductID == 0 && it.ServiceID == 0 {
			return ruleErr(p+"product_id", RuleRequired)
		}
		if it.ProductID != 0 {
			if seenProduct[it.ProductID] {
				return ruleErr(p+"product_id", RuleDistinct)
			}
			seenProduct[it.ProductID] = true
		}
		if it.ServiceID != 0 {
			if seenService[it.ServiceID] {
				return ruleErr(p+"service_id", RuleDistinct)
			}
			seenService[it.ServiceID] = true
		}
		if it.Quantity < 1 {
			return ruleErr(p+"quantity", RuleMin)
		}
	}
	return nil
}

// Product — товар, как его видит продажа (строка products, прочитанная FOR UPDATE).
type Product struct {
	ID       int64
	SKU      string
	Name     string
	Price    money.Tiyn
	Quantity int64
}

// Service — услуга клуба (club_services, is_active).
type Service struct {
	ID    int64
	Name  string
	Price money.Tiyn
}

// StockWriteOff — списание товара при продаже: products.quantity −= Quantity, stock_movements {write_off,
// quantity, balance_after = BalanceAfter, note = 'Продажа POS'}, журнал stock.written_off, вебхук stock.changed
// {change: −Quantity, quantity: BalanceAfter, reason: sale}.
type StockWriteOff struct {
	ProductID    int64
	SKU          string
	Name         string
	Quantity     int64
	BalanceAfter int64
}

// SaleNote — note движения склада при продаже (не переводится). Laravel: CashRegisterService::sale().
const SaleNote = "Продажа POS"

// Sale — результат продажи POS: что записать.
type Sale struct {
	// Amount — cash_transactions.amount: float-сумма (float) price * qty, записанная в decimal(14,2).
	Amount    money.Tiyn
	Lines     []ReceiptLine
	WriteOffs []StockWriteOff
}

// Description — описание операции продажи: маркер POSSale.
func (Sale) Description() string { return POSSale }

// SellPOS — продажа товаров и услуг на кассе (BR-052).
//
// Laravel: app/Services/CashRegisterService.php sale() (~стр. 19–85):
//
//	$total = 0.0;
//	foreach ($items as $item) {
//	    service: $total += (float) $service->price * $quantity; строка {service_id, name, quantity, price, total}
//	    product (FOR UPDATE): if ($quantity > $product->quantity) → «Недостаточно товара: :product.» (поле items)
//	             $total += (float) $product->price * $quantity; строка {product_id, …}; decrement quantity; stock_movements write_off
//	}
//	create(['type' => income, 'amount' => $total, 'description' => 'Продажа POS', 'receipt_data' => ['items' => …]])
//
// products и services — то, что нашлось по items (по id); строка без найденного товара/услуги — findOrFail (404) у
// вызывающего, здесь — ошибка поля items.N. Сумма — точно Σ price × qty в тиынах (Решение Q-002 (docs/DECISIONS.md):
// целые тиыны везде); старый float-расчёт для цен decimal(12,2) давал ровно то же (TestSaleFloatTotalMatchesExact).
// Покупатель и способ оплаты продажи не записываются (Q-013: payment_method = NULL).
func SellPOS(open *Shift, items []SaleItem, products map[int64]Product, services map[int64]Service) (Sale, error) {
	if err := RequireOpen(open, "items"); err != nil {
		return Sale{}, err
	}
	if err := ValidateSaleItems(items); err != nil {
		return Sale{}, err
	}
	var (
		total money.Tiyn
		sale  Sale
	)
	left := map[int64]int64{} // остатки после списаний этой продажи (товары distinct, но так надёжнее)
	for i, it := range items {
		qty := it.Quantity
		// Laravel: if (! empty($item['service_id'])) — услуга важнее товара, если заданы оба.
		if it.ServiceID != 0 {
			s, ok := services[it.ServiceID]
			if !ok {
				return Sale{}, ruleErr("items."+strconv.Itoa(i)+".service_id", RuleIn)
			}
			line, err := lineTotal(s.Price, qty)
			if err != nil {
				return Sale{}, err
			}
			total = min(total+line, MaxShiftAmount+1) // насыщение: строка ≤ MaxShiftAmount, int64 не переполняется
			sale.Lines = append(sale.Lines, ReceiptLine{ServiceID: s.ID, Name: s.Name, Quantity: qty, Price: s.Price, Total: line})
			continue
		}
		p, ok := products[it.ProductID]
		if !ok {
			return Sale{}, ruleErr("items."+strconv.Itoa(i)+".product_id", RuleIn)
		}
		have, seen := left[p.ID]
		if !seen {
			have = p.Quantity
		}
		if qty > have {
			return Sale{}, &Error{Field: "items", Message: MsgNotEnoughStock, Params: map[string]string{"product": p.Name}}
		}
		line, err := lineTotal(p.Price, qty)
		if err != nil {
			return Sale{}, err
		}
		total = min(total+line, MaxShiftAmount+1)
		left[p.ID] = have - qty
		sale.Lines = append(sale.Lines, ReceiptLine{ProductID: p.ID, Name: p.Name, Quantity: qty, Price: p.Price, Total: line})
		sale.WriteOffs = append(sale.WriteOffs, StockWriteOff{ProductID: p.ID, SKU: p.SKU, Name: p.Name, Quantity: qty, BalanceAfter: have - qty})
	}
	if total > MaxShiftAmount {
		// decimal(14,2) переполнен: в старом коде — ошибка MySQL и откат.
		return Sale{}, ruleErr("items", RuleMax)
	}
	sale.Amount = total
	return sale, nil
}

// lineTotal — сумма строки в тиынах (price × qty) с проверкой переполнения; в старом коде — (float) price * qty
// в чеке и ошибка MySQL при записи суммы больше decimal(14,2).
func lineTotal(price money.Tiyn, qty int64) (money.Tiyn, error) {
	v, err := money.MulChecked(int64(price), qty)
	if err != nil || money.Tiyn(v) > MaxShiftAmount {
		return 0, ruleErr("items", RuleMax)
	}
	return money.Tiyn(v), nil
}
