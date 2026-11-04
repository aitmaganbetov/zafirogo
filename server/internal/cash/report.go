package cash

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Подписи строк X/Z-отчёта — ключи перевода (русский текст, как __('…') в Laravel).
// Laravel: app/Services/ShiftReport.php for(), resources/views/cash-register/report.blade.php.
const (
	LabelKindGoods   = "Продажа товаров"
	LabelKindTopUps  = "Пополнение балансов"
	LabelKindGuests  = "Гостевые сессии"
	LabelKindOther   = "Прочий приход"
	LabelRefunds     = "Возвраты (расход)"
	LabelNoGoods     = "За смену товары не продавались."
	LabelItemsTotal  = "Всего"
	LabelDiscount    = "Скидки"
	LabelShiftNumber = "Номер смены"
	LabelCashier     = "Кассир"
	LabelClosedBy    = "Закрыл смену"
	LabelOperator    = "Оператор"
	LabelOpening     = "Начальный остаток"
	LabelIncome      = "Приход"
	LabelExpense     = "Расход"
	LabelInCash      = "Итог в кассе"
	NoName           = "—"
)

// KindRow — строка «Приход по видам»: подпись, число операций, сумма.
type KindRow struct {
	Label  string
	Count  int64
	Amount money.Tiyn
}

// RefundsRow — возвраты смены (расходы с refund_of_id).
type RefundsRow struct {
	Count  int64
	Amount money.Tiyn
}

// ItemRow — строка «Продано товаров»: имя, количество, сумма и остаток на складе сейчас (nil — услуга
// или товар не найден: «—»).
type ItemRow struct {
	Name     string
	Quantity int64
	Amount   money.Tiyn
	Stock    *int64
}

// StaffRow — строка «Сотрудники».
type StaffRow struct {
	UserID  int64 // 0 — user_id NULL
	Name    string
	Count   int64
	Income  money.Tiyn
	Expense money.Tiyn
}

// Report — X/Z-отчёт смены: то, что ShiftReport::for() и CashRegisterService::totals() давали Blade-шаблону
// cash-register/report.blade.php и API приложения сотрудника (Api/Staff/CashController::report()).
type Report struct {
	Totals     Totals
	Kinds      []KindRow
	Refunds    RefundsRow
	Items      []ItemRow
	ItemsTotal money.Tiyn // items_total (показывает только API приложения сотрудника)
	Discount   money.Tiyn // discount — всегда 0 в данных старого кода (GAP-029)
	Staff      []StaffRow
}

// ReportInput — данные смены для отчёта.
type ReportInput struct {
	Opening money.Tiyn
	// Operations — все операции смены в порядке id (как их отдаёт SQL без ORDER BY; порядок важен только для
	// «первого» product_id одноимённых строк и для порядка float-сложения).
	Operations []Operation
	// Stock — текущий остаток товаров по id, включая удалённые (Product::withTrashed()).
	Stock map[int64]int64
	// Names — имена сотрудников по user_id.
	Names map[int64]string
}

// BuildReport — состав X/Z-отчёта (BR-057).
//
// Laravel: app/Services/ShiftReport.php for() (~стр. 22–101):
//   - виды прихода: «Продажа товаров» (description = 'Продажа POS'), «Пополнение балансов» и «Гостевые сессии»
//     (журнал cash.income с details.balance_type / details.computers — здесь Operation.Kind, допущение Q-007/BR-160);
//     строки всегда есть, даже с нулём; «Прочий приход» — только если операций прихода больше, чем попало в виды:
//     count = all − Σcount, amount = round(all_sum − Σsum, 2) во float;
//   - возвраты: операции смены с refund_of_id (count, SUM);
//   - товары: строки чеков продаж POS с receipt_data, кроме возвращённых; группировка по name: quantity +=, amount += total
//     (float); product_id — первый непустой; сортировка amount ↓ (в тиынах, CQ-7), затем name ↑ (strcmp); stock — текущий остаток
//     (последний коммит старого репо fab81f2: «остаток склада вместо суммы продаж»), у услуг — null;
//   - items_total = round(Σ amount, 2); discount = round(Σ receipt.discount_amount, 2);
//   - сотрудники: по user_id — count (все операции), SUM прихода, SUM расхода; sortByDesc('income') (устойчиво).
//
// Суммы по SQL (виды, возвраты, сотрудники, итоги) в старом коде точные (SUM по decimal) — здесь сложение тиынов.
// Float-места (прочий приход, суммы товаров) повторены во float64 + PHPRound; сортировка товаров — по тиынам (CQ-7).
// Способ оплаты (Q-013) не участвует.
func BuildReport(in ReportInput) Report {
	r := Report{Totals: ComputeTotals(in.Opening, in.Operations)}

	// Виды прихода.
	var goods, topUps, guests, all KindRow
	goods.Label, topUps.Label, guests.Label = LabelKindGoods, LabelKindTopUps, LabelKindGuests
	refunded := map[int64]bool{}
	for _, op := range in.Operations {
		if op.RefundOfID != 0 {
			refunded[op.RefundOfID] = true
			r.Refunds.Count++
			r.Refunds.Amount += op.Amount
		}
		if op.Type != Income {
			continue
		}
		all.Count++
		all.Amount += op.Amount
		if op.Description == POSSale {
			goods.Count++
			goods.Amount += op.Amount
		}
		if op.Kind == KindTopUp {
			topUps.Count++
			topUps.Amount += op.Amount
		}
		if op.Kind == KindGuestSession {
			guests.Count++
			guests.Amount += op.Amount
		}
	}
	r.Kinds = []KindRow{goods, topUps, guests}
	counted := goods.Count + topUps.Count + guests.Count
	if all.Count > counted {
		// Laravel: $countedAmount += (float) $sum->amount (по видам, по порядку);
		// 'amount' => round((float) $all->amount - $countedAmount, 2)
		countedAmount := 0.0
		for _, k := range r.Kinds {
			countedAmount += k.Amount.Float()
		}
		r.Kinds = append(r.Kinds, KindRow{
			Label:  LabelKindOther,
			Count:  all.Count - counted,
			Amount: roundedTiyn(all.Amount.Float() - countedAmount),
		})
	}

	r.Items, r.ItemsTotal, r.Discount = soldItems(in, refunded)
	r.Staff = staffRows(in)
	return r
}

type itemAcc struct {
	row       ItemRow
	amountF   float64 // (float) сумма, как в PHP: слагаемое items_total
	productID int64
}

func soldItems(in ReportInput, refunded map[int64]bool) ([]ItemRow, money.Tiyn, money.Tiyn) {
	var (
		order     []string
		byName    = map[string]*itemAcc{}
		discountF float64
	)
	for _, op := range in.Operations {
		// Laravel: (clone $income)->where('description', POS_SALE)->whereNotNull('receipt_data')->whereNotIn('id', refund_of_id …)
		if !op.IsPOSSale() || op.Receipt == nil || refunded[op.ID] {
			continue
		}
		discountF += op.Receipt.DiscountAmount.Float()
		for _, l := range op.Receipt.Items {
			name := l.Name
			if name == "" {
				name = NoName // (string) ($line['name'] ?? '—')
			}
			acc, ok := byName[name]
			if !ok {
				acc = &itemAcc{row: ItemRow{Name: name}}
				byName[name] = acc
				order = append(order, name)
			}
			if acc.productID == 0 { // $items[$name]['product_id'] ??= …
				acc.productID = l.ProductID
			}
			acc.row.Quantity += l.Quantity
			acc.row.Amount += l.Total
			acc.amountF += lineFloat(l)
		}
	}
	accs := make([]*itemAcc, 0, len(order))
	for _, n := range order {
		accs = append(accs, byName[n])
	}
	// Laravel: usort($items, fn ($a, $b) => $b['amount'] <=> $a['amount'] ?: strcmp($a['name'], $b['name'])).
	// Решение CQ-7 (docs/DECISIONS.md): сумма сравнивается точно в тиынах, а не во float (в PHP 0.10 × 3 =
	// 0.30000000000000004 вставало раньше 0.30 × 1); затем — по имени, как strcmp.
	sort.SliceStable(accs, func(i, j int) bool {
		if accs[i].row.Amount != accs[j].row.Amount {
			return accs[i].row.Amount > accs[j].row.Amount
		}
		return accs[i].row.Name < accs[j].row.Name // strcmp — побайтно, как сравнение строк Go
	})
	items := make([]ItemRow, 0, len(accs))
	sumF := 0.0
	for _, a := range accs {
		row := a.row
		if a.productID != 0 {
			if q, ok := in.Stock[a.productID]; ok {
				row.Stock = &q
			}
		}
		items = append(items, row)
		sumF += a.amountF // array_sum(array_column($items, 'amount')) — по отсортированному списку
	}
	return items, roundedTiyn(sumF), roundedTiyn(discountF)
}

// lineFloat — (float) $line['total'] строки чека: в чеке POS это (float) price * quantity (CashRegisterService::sale()).
// Если чек хранит другую сумму (старые данные), берём её.
func lineFloat(l ReceiptLine) float64 {
	if l.Price*money.Tiyn(l.Quantity) == l.Total {
		return l.Price.Float() * float64(l.Quantity)
	}
	return l.Total.Float()
}

// roundedTiyn — PHP round($x, 2) для float-суммы в тенге, в тиынах.
func roundedTiyn(f float64) money.Tiyn {
	return money.Tiyn(money.PHPIntCast(math.Round(money.PHPRound(f, 2) * 100)))
}

func staffRows(in ReportInput) []StaffRow {
	byUser := map[int64]*StaffRow{}
	var ids []int64
	for _, op := range in.Operations {
		row, ok := byUser[op.UserID]
		if !ok {
			row = &StaffRow{UserID: op.UserID}
			byUser[op.UserID] = row
			ids = append(ids, op.UserID)
		}
		row.Count++
		switch op.Type {
		case Income:
			row.Income += op.Amount
		case Expense:
			row.Expense += op.Amount
		}
	}
	// GROUP BY user_id без ORDER BY: порядок групп в старом коде не задан; берём user_id по возрастанию
	// (NULL первым, как MySQL), затем устойчивая сортировка по приходу ↓ — sortByDesc('income').
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	rows := make([]StaffRow, 0, len(ids))
	for _, id := range ids {
		row := *byUser[id]
		row.Name = NoName
		if id != 0 {
			if n, ok := in.Names[id]; ok {
				row.Name = n
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Income > rows[j].Income })
	return rows
}

// ReportType — X (промежуточный, только открытая смена) или Z (итоговый, любая смена).
type ReportType string

// Типы отчёта. Laravel: CashRegisterController::xReport() / zReport().
const (
	ReportX ReportType = "X"
	ReportZ ReportType = "Z"
)

// CheckXReport — X-отчёт строится только по открытой смене (CashRegisterController::xReport(): firstOrFail → 404).
func CheckXReport(open *Shift) bool { return open.IsOpen() }

// People — имена в шапке отчёта.
type People struct {
	Cashier  string // открывший смену; «—», если неизвестен
	ClosedBy string // закрывший смену, если это другой сотрудник; "" — строки нет
	Operator string // кто формирует отчёт сейчас
}

// ReportPeople — шапка отчёта.
// Laravel: CashRegisterController::people() (~стр. 167–176): cashierName = имя opened_by; closedByName — только если
// closed_by не null и не равен opened_by; operatorName — текущий пользователь. Blade: {{ $cashierName ?? '—' }},
// @if($closedByName).
func ReportPeople(s Shift, names map[int64]string, operator string) People {
	p := People{Cashier: NoName, Operator: operator}
	if n, ok := names[s.OpenedBy]; ok && s.OpenedBy != 0 {
		p.Cashier = n
	}
	if s.ClosedBy != 0 && s.ClosedBy != s.OpenedBy {
		p.ClosedBy = names[s.ClosedBy]
	}
	return p
}

// Line — строка отчёта, как её показывает Blade: ячейки по порядку.
type Line struct {
	Section string // info, metrics, kinds, items, staff
	Cells   []string
}

// String — ячейки через « · » (так сценарии GS записывают строки отчёта).
func (l Line) String() string { return strings.Join(l.Cells, " · ") }

// Lines — строки отчёта в порядке resources/views/cash-register/report.blade.php (без дат открытия/формирования —
// их формирует слой представления в часовом поясе клуба). Подписи — ключи перевода.
func (r Report) Lines(shiftID int64, p People) []Line {
	var out []Line
	add := func(section string, cells ...string) { out = append(out, Line{Section: section, Cells: cells}) }

	add("info", LabelShiftNumber, "#"+strconv.FormatInt(shiftID, 10))
	add("info", LabelCashier, p.Cashier)
	if p.ClosedBy != "" {
		add("info", LabelClosedBy, p.ClosedBy)
	}
	add("info", LabelOperator, p.Operator)

	add("metrics", LabelOpening, KZT(r.Totals.Opening))
	add("metrics", LabelIncome, "+ "+KZT(r.Totals.Income))
	add("metrics", LabelExpense, "− "+KZT(r.Totals.Expense))
	add("metrics", LabelInCash, KZT(r.Totals.Balance()))

	for _, k := range r.Kinds {
		add("kinds", k.Label, strconv.FormatInt(k.Count, 10), KZT(k.Amount))
	}
	if r.Refunds.Count > 0 {
		add("kinds", LabelRefunds, strconv.FormatInt(r.Refunds.Count, 10), "− "+KZT(r.Refunds.Amount))
	}

	if len(r.Items) == 0 {
		add("items", LabelNoGoods)
	} else {
		var qty int64
		for _, it := range r.Items {
			stock := NoName
			if it.Stock != nil {
				stock = strconv.FormatInt(*it.Stock, 10)
			}
			add("items", it.Name, strconv.FormatInt(it.Quantity, 10), stock)
			qty += it.Quantity
		}
		add("items", LabelItemsTotal, strconv.FormatInt(qty, 10))
		if r.Discount > 0 {
			add("items", LabelDiscount, "− "+KZT(r.Discount))
		}
	}

	for _, s := range r.Staff {
		add("staff", s.Name, strconv.FormatInt(s.Count, 10), KZT(s.Income), KZT(s.Expense))
	}
	return out
}

// KZT — сумма в отчёте: number_format($x, 2, ',', ' ') . ' KZT' («2 100,00 KZT»). Валюта в шаблоне отчёта
// зашита (не из настроек клуба).
func KZT(t money.Tiyn) string { return NumberFormat(t) + " KZT" }

// NumberFormat — PHP number_format($tenge, 2, ',', ' '): разделитель тысяч — пробел, дробной части — запятая.
func NumberFormat(t money.Tiyn) string {
	s := t.Tenge() // "-1234.50"
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i := 0; i < len(whole); i++ {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteByte(whole[i])
	}
	b.WriteByte(',')
	b.WriteString(frac)
	return b.String()
}
