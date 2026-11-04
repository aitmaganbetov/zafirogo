package cash

import "github.com/aitmaganbetov/zafirogo/server/internal/money"

// Type — тип кассовой операции (cash_transactions.type).
type Type string

// Типы операций. Laravel: app/Models/CashTransaction.php TYPE_INCOME, TYPE_EXPENSE.
const (
	Income  Type = "income"
	Expense Type = "expense"
)

// POSSale — маркер продажи POS в description (не переводится).
// Laravel: app/Models/CashTransaction.php POS_SALE.
const POSSale = "Продажа POS"

// PaymentMethod — способ оплаты (cash/card/qr). Решение Q-013 (docs/DECISIONS.md) — необязательное поле
// (cash_transactions.payment_method nullable), в остатке смены и X/Z-отчётах не используется.
type PaymentMethod string

// Способы оплаты. Laravel: StoreBalanceTopUpRequest rules() — payment_method in:cash,card,qr.
const (
	PayCash PaymentMethod = "cash"
	PayCard PaymentMethod = "card"
	PayQR   PaymentMethod = "qr"
)

// Valid сообщает, что способ оплаты из списка cash/card/qr.
func (m PaymentMethod) Valid() bool { return m == PayCash || m == PayCard || m == PayQR }

// label — подпись способа оплаты в описании операции.
// Laravel: CashRegisterService::topUpBalance() — $paymentLabels = ['cash' => __('наличные'), 'card' => __('карта'), 'qr' => __('QR-код')].
func (m PaymentMethod) label() string {
	switch m {
	case PayCard:
		return "карта"
	case PayQR:
		return "QR-код"
	default:
		return "наличные"
	}
}

// BalanceField — какой баланс клиента пополняется (balance_type).
type BalanceField string

// Балансы клиента. Laravel: StoreBalanceTopUpRequest — balance_type in:balance,bonus_balance.
const (
	FieldMain  BalanceField = "balance"
	FieldBonus BalanceField = "bonus_balance"
)

// Valid сообщает, что поле — balance или bonus_balance.
func (f BalanceField) Valid() bool { return f == FieldMain || f == FieldBonus }

// IncomeKind — вид прихода для X/Z-отчёта.
//
// В старом коде вид «Пополнение балансов» / «Гостевые сессии» определялся по журналу действий:
// приход входит в вид, если у него есть запись cash.income с details.balance_type / details.computers
// (Laravel: app/Services/ShiftReport.php for(), $logged(); BR-057, BR-160). В новом коде вид — поле
// кассовой операции, которое заполняет вызывающий (OQ-BR-04, Решение Q-007/BR-160 (docs/DECISIONS.md)).
// «Продажа товаров» определяется не видом, а маркером description = POSSale, как в старом коде.
type IncomeKind string

// Виды прихода.
const (
	KindNone         IncomeKind = ""              // прочий приход (transact()) или продажа POS
	KindTopUp        IncomeKind = "top_up"        // журнал cash.income с details.balance_type (topUpBalance())
	KindGuestSession IncomeKind = "guest_session" // журнал cash.income с details.computers (GuestSessions::sell())
)

// ReceiptLine — строка чека продажи POS (receipt_data.items[]).
// Laravel: CashRegisterService::sale() — ['product_id'|'service_id', 'name', 'quantity', 'price' => (float), 'total' => (float) price * qty].
type ReceiptLine struct {
	ProductID int64 // 0 — нет (услуга или старый чек только с именем)
	ServiceID int64 // 0 — не услуга
	Name      string
	Quantity  int64
	Price     money.Tiyn
	Total     money.Tiyn
}

// IsService — строка-услуга (isset($line['service_id'])).
func (l ReceiptLine) IsService() bool { return l.ServiceID != 0 }

// TopUpReceipt — receipt_data.top_up пополнения: клиент, баланс и бонус, который дало пополнение.
// Laravel: CashRegisterService::topUpBalance() — ['top_up' => ['user_id', 'balance_type', 'bonus' => cents / 100]].
type TopUpReceipt struct {
	UserID int64
	Field  BalanceField
	Bonus  money.Tiyn
}

// Receipt — receipt_data кассовой операции.
type Receipt struct {
	// Items — строки продажи. nil — ключа items нет; пустой срез — массив без строк
	// (refundKind() проверяет is_array, поэтому это разные случаи).
	Items []ReceiptLine
	// TopUp — данные пополнения; nil — нет (или старое пополнение без клиента).
	TopUp *TopUpReceipt
	// DiscountAmount — receipt_data.discount_amount: в старом коде нигде не пишется (наследие, всегда 0; GAP-029, ОВ-6),
	// но ShiftReport::for() его суммирует.
	DiscountAmount money.Tiyn
}

// Operation — кассовая операция смены (cash_transactions).
type Operation struct {
	ID          int64
	ShiftID     int64
	UserID      int64 // 0 — нет (user_id NULL)
	Type        Type
	Amount      money.Tiyn
	Description string
	Receipt     *Receipt // nil — receipt_data NULL
	RefundOfID  int64    // 0 — не возврат
	Kind        IncomeKind
	// PaymentMethod — Q-013: хранится, в расчётах не участвует.
	PaymentMethod PaymentMethod
}

// IsPOSSale — приход с маркером продажи POS.
func (o Operation) IsPOSSale() bool { return o.Type == Income && o.Description == POSSale }

// RefundKind — что возвращает возврат этого прихода: "sale" (товары на склад), "top_up" (сумма с баланса клиента)
// или "" — вернуть нельзя (расход, гостевая сессия, прочий приход, пополнение без клиента в чеке).
//
// Laravel: app/Models/CashTransaction.php refundKind() (~стр. 45–55); BR-055.
func (o Operation) RefundKind() RefundKind {
	if o.Type != Income {
		return RefundNone
	}
	if o.Description == POSSale && o.Receipt != nil && o.Receipt.Items != nil {
		return RefundSale
	}
	if o.Receipt != nil && o.Receipt.TopUp != nil && o.Receipt.TopUp.UserID != 0 {
		return RefundTopUp
	}
	return RefundNone
}

// RefundKind — вид возврата.
type RefundKind string

// Виды возврата.
const (
	RefundNone  RefundKind = ""
	RefundSale  RefundKind = "sale"
	RefundTopUp RefundKind = "top_up"
)
