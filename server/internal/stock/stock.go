package stock

import (
	"errors"
	"math/big"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// MovementType — тип движения склада (stock_movements.type).
type MovementType string

// Типы движений. Laravel: app/Models/StockMovement.php TYPE_RECEIPT, TYPE_WRITE_OFF.
const (
	Receipt  MovementType = "receipt"
	WriteOff MovementType = "write_off"
)

// Пределы количеств.
const (
	// MaxReceiveQuantity — «Приход товара» кассира/оператора, API сотрудника receive и API v1:
	// quantity integer|min:1|max:100000 (StockController::receive(), Api/Staff/ProductController::receive(),
	// Api/V1/StockMovementController::store()). Форма склада (StoreStockMovementRequest) верхней границы не имеет.
	MaxReceiveQuantity int64 = 100_000
	// MaxInitialQuantity — StoreProductRequest: quantity integer|min:0|max:1000000.
	MaxInitialQuantity int64 = 1_000_000
	// MaxPrice — StoreProductRequest: price numeric|min:0|max:9999999999.99.
	MaxPrice money.Tiyn = 999_999_999_999
	// MaxNote — note nullable|string|max:255.
	MaxNote = 255
)

// Тексты (ключи перевода).
const (
	MsgWriteOffTooMuch = "Нельзя списать больше, чем есть на складе." // AdjustStockAction::handle(), поле quantity
	NoteInitial        = "Начальный остаток при создании товара"      // CreateProductAction::handle(): note движения (переводится)
	NoteInitialLog     = "Начальный остаток"                          // там же: details.note журнала
	NoNote             = "—"                                          // details.note журнала, если note пустой
)

// Error — ошибка правила склада с полем формы. Rule — правило валидации (текст даёт слой валидации),
// Message — текст старого кода (ключ перевода).
type Error struct {
	Field   string
	Rule    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Field + ": " + e.Message
	}
	return e.Field + ": " + e.Rule
}

// AsError возвращает *Error из цепочки ошибок.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// Movement — движение склада: что записать в stock_movements и чем стал остаток товара.
type Movement struct {
	ProductID    int64
	Type         MovementType
	Quantity     int64
	BalanceAfter int64 // products.quantity после движения
	Note         string
	// Change — изменение остатка со знаком (вебхук stock.changed.change).
	Change int64
}

// LogNote — details.note журнала stock.received / stock.written_off: $movement->note ?: '—'.
func (m Movement) LogNote() string {
	if m.Note == "" {
		return NoNote
	}
	return m.Note
}

// Adjust — приход или списание товара (BR-070).
//
// Laravel: app/Actions/AdjustStockAction.php handle() (~стр. 16–66):
//
//	товар FOR UPDATE; $quantity = (int) $data['quantity'];
//	write_off && $quantity > $product->quantity → «Нельзя списать больше, чем есть на складе.» (поле quantity)
//	$product->quantity += receipt ? $quantity : −$quantity
//	stock_movements {product_id, user_id, type, quantity, balance_after, note}
//
// current — products.quantity (прочитан FOR UPDATE). maxQuantity — верхняя граница формы (MaxReceiveQuantity для
// «Прихода товара», API сотрудника receive и API v1; 0 — без границы, как StoreStockMovementRequest).
// Списание до нуля разрешено; уход в минус невозможен (в MySQL — unsigned, в Postgres — CHECK quantity >= 0).
func Adjust(productID, current int64, typ MovementType, quantity, maxQuantity int64, note string) (Movement, error) {
	if typ != Receipt && typ != WriteOff {
		return Movement{}, &Error{Field: "type", Rule: "in"}
	}
	if quantity < 1 {
		return Movement{}, &Error{Field: "quantity", Rule: "min"}
	}
	if maxQuantity > 0 && quantity > maxQuantity {
		return Movement{}, &Error{Field: "quantity", Rule: "max"}
	}
	if len([]rune(note)) > MaxNote {
		return Movement{}, &Error{Field: "note", Rule: "max"}
	}
	if typ == WriteOff && quantity > current {
		return Movement{}, &Error{Field: "quantity", Message: MsgWriteOffTooMuch}
	}
	change := quantity
	if typ == WriteOff {
		change = -quantity
	}
	after, err := money.AddChecked(current, change)
	if err != nil {
		return Movement{}, &Error{Field: "quantity", Rule: "max"}
	}
	return Movement{ProductID: productID, Type: typ, Quantity: quantity, BalanceAfter: after, Note: note, Change: change}, nil
}

// Initial — начальный остаток при создании товара (BR-071).
//
// Laravel: app/Actions/CreateProductAction.php handle(): $quantity = (int) $data['quantity'] (StoreProductRequest:
// integer|min:0|max:1000000); товар создаётся с quantity; если $quantity > 0 — движение receipt
// {quantity, balance_after = quantity, note «Начальный остаток при создании товара»}, журнал stock.received
// (details.note «Начальный остаток»), вебхук stock.changed {reason: initial}. При 0 движения нет (ok = false).
func Initial(productID, quantity int64) (m Movement, ok bool, err error) {
	if quantity < 0 {
		return Movement{}, false, &Error{Field: "quantity", Rule: "min"}
	}
	if quantity > MaxInitialQuantity {
		return Movement{}, false, &Error{Field: "quantity", Rule: "max"}
	}
	if quantity == 0 {
		return Movement{}, false, nil
	}
	return Movement{ProductID: productID, Type: Receipt, Quantity: quantity, BalanceAfter: quantity, Note: NoteInitial, Change: quantity}, true, nil
}

// ParsePrice — цена товара из формы (BR-071, BR-072).
//
// Laravel: app/Http/Requests/StoreProductRequest.php — price required|numeric|min:0|max:9999999999.99 (сравнения
// точные, brick/math); CreateProductAction / UpdateProductAction пишут строку запроса в decimal(12,2) — округление
// MySQL по десятичной записи (money.ParseTengeRounded). Допущение Этапа 1: Q-002.
func ParsePrice(s string) (money.Tiyn, error) {
	if strings.TrimSpace(s) == "" {
		return 0, &Error{Field: "price", Rule: "required"}
	}
	r, ok := numericRat(s)
	if !ok {
		return 0, &Error{Field: "price", Rule: "numeric"}
	}
	if r.Sign() < 0 {
		return 0, &Error{Field: "price", Rule: "min"}
	}
	if r.Cmp(ratMaxPrice) > 0 {
		return 0, &Error{Field: "price", Rule: "max"}
	}
	t, err := money.ParseTengeRounded(s)
	if err != nil || t > MaxPrice {
		return 0, &Error{Field: "price", Rule: "max"}
	}
	return t, nil
}

var ratMaxPrice = big.NewRat(int64(MaxPrice), 100)

// numericRat — значение строки, прошедшей правило `numeric` (PHP is_numeric), как точное число: так Laravel 10+
// сравнивает min/max (brick/math BigNumber). Экспонента вне ±1000 — ошибка. Копия cash.numericRat (пакеты независимы).
func numericRat(s string) (*big.Rat, bool) {
	if !money.PHPIsNumeric(s) {
		return nil, false
	}
	s = strings.Trim(s, " \t\n\r\v\f")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	exp := int64(0)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.ParseInt(s[i+1:], 10, 32)
		if err != nil || e > 1000 || e < -1000 {
			return nil, false
		}
		exp = e
		s = s[:i]
	}
	whole, frac, _ := strings.Cut(s, ".")
	digits, ok := new(big.Int).SetString("0"+whole+frac, 10)
	if !ok {
		return nil, false
	}
	if neg {
		digits.Neg(digits)
	}
	scale := exp - int64(len(frac))
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(max(scale, -scale)), nil)
	r := new(big.Rat).SetInt(digits)
	if scale >= 0 {
		return r.Mul(r, new(big.Rat).SetInt(p)), true
	}
	return r.Quo(r, new(big.Rat).SetInt(p)), true
}

// Change — изменение поля товара «было → стало» (журнал product.updated).
type Change struct {
	Field string
	Old   string
	New   string
}

// ProductFields — изменяемые поля товара (sku, name, категория, цена). Остаток меняется только движениями.
type ProductFields struct {
	SKU        string
	Name       string
	CategoryID int64
	Category   string // имя категории (products.category)
	Price      money.Tiyn
}

// ProductChanges — что изменилось при правке товара (BR-072). Пусто — ничего не пишется (ни журнала, ни вебхука).
//
// Laravel: app/Actions/UpdateProductAction.php handle(): $product->fill($data); getDirty() без updated_at;
// изменения «было → стало». Сравнение цены — после приведения к decimal:2 (Eloquent originalIsEquivalent), поэтому
// цена, которая округляется в ту же сумму, изменением не считается. Порядок полей — sku, name, product_category_id,
// category, price (порядок fill() из формы).
// Отличие: старый журнал писал новое значение цены как в запросе ("150.5"), здесь — в формате decimal ("150.50").
func ProductChanges(old, upd ProductFields) []Change {
	var out []Change
	add := func(field, o, n string) {
		if o != n {
			out = append(out, Change{Field: field, Old: o, New: n})
		}
	}
	add("sku", old.SKU, upd.SKU)
	add("name", old.Name, upd.Name)
	add("product_category_id", strconv.FormatInt(old.CategoryID, 10), strconv.FormatInt(upd.CategoryID, 10))
	add("category", old.Category, upd.Category)
	add("price", old.Price.Tenge(), upd.Price.Tenge())
	return out
}

// String — «было → стало» (details журнала product.updated).
func (c Change) String() string { return c.Old + " → " + c.New }

// DeletedSKU — SKU удалённого товара: освобождает исходный SKU (BR-073).
// Laravel: app/Http/Controllers/ProductDeleteController.php __invoke() (~стр. 22): mb_substr($sku, 0, 200) . '~deleted-' . $id
// (по символам; в BR-073 написано substr).
func DeletedSKU(sku string, id int64) string {
	if r := []rune(sku); len(r) > 200 {
		sku = string(r[:200])
	}
	return sku + "~deleted-" + strconv.FormatInt(id, 10)
}
