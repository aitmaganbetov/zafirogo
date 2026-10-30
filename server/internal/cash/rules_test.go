package cash

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Решение Q-002 (docs/DECISIONS.md): суммы из форм разбираются строго (money.ParseTenge), > 2 знаков после точки — 422
// (правило decimal), а не округление MySQL, как в старом коде ("100.005" давало 100.01).
func TestParseTopUpAmount(t *testing.T) {
	cases := []struct {
		in           string
		amount, base money.Tiyn
		rule         string
	}{
		{in: "750", amount: 75000, base: 75000},
		{in: "10000.55", amount: 1000055, base: 1000055},
		{in: "0.29", amount: 29, base: 29},                      // 0.29*100 во float = 28.999999999999996 — база бонуса всё равно 29
		{in: " 7.5 ", amount: 750, base: 750},                   // пробелы по краям обрезаются (TrimStrings)
		{in: "9999999999.99", amount: MaxTopUp, base: MaxTopUp}, // max:9999999999.99 включительно
		{in: "Q-002/100.005", rule: RuleDecimal},                // было: округление до 100.01 (BR-041)
		{in: "Q-002/1.005", rule: RuleDecimal},                  // было: 1.01 на баланс и база бонуса 1.00 (CQ-4)
		{in: "Q-002/0.0149999999999999", rule: RuleDecimal},     // было: +0.02 на баланс и база бонуса 1 тиын (CQ-4)
		{in: "Q-002/1.500", rule: RuleDecimal},                  // три знака, даже нулевые
		{in: "Q-002/0.001", rule: RuleDecimal},                  // было: CQ-1 (gt после округления)
		{in: "9999999999.994", rule: RuleDecimal},
		{in: "10000000000", rule: RuleMax},
		{in: "0", rule: RuleGt}, // GS-013
		{in: "-5", rule: RuleGt},
		{in: "abc", rule: RuleNumeric},
		{in: "1,5", rule: RuleNumeric},
		{in: "1e3", rule: RuleNumeric}, // экспонента — неоднозначный ввод
		{in: ".5", rule: RuleNumeric},
		{in: "+5", rule: RuleNumeric},
		{in: "", rule: RuleRequired},
		{in: "   ", rule: RuleRequired},
	}
	for _, c := range cases {
		in := c.in
		if _, v, ok := strings.Cut(in, "Q-002/"); ok {
			in = v
		}
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseTopUpAmount(in)
			if c.rule != "" {
				wantErr(t, err, "amount", c.rule)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			eq(t, "amount", got.Amount, c.amount)
			eq(t, "bonus base", got.BonusBase, c.base)
		})
	}
}

// База бонуса (int) round($amount * 100) старого кода совпадает с точной суммой для любого ввода с ≤ 2 знаками
// (Решение Q-002 (docs/DECISIONS.md) делает расхождение CQ-4 невозможным).
func TestTopUpBonusBaseEqualsAmount(t *testing.T) {
	r := rand.New(rand.NewPCG(20261030, 2))
	for i := 0; i < 200000; i++ {
		v := money.Tiyn(r.Int64N(int64(MaxTopUp))) + 1
		if i%4 == 0 {
			v = money.Tiyn(r.Int64N(100_000_00)) + 1 // до 100 000 тенге — частые суммы
		}
		got, err := ParseTopUpAmount(v.Tenge())
		if err != nil {
			t.Fatal(v, err)
		}
		if php := money.FromTengeFloat(money.PHPStringToFloat(v.Tenge())); php != got.BonusBase || got.Amount != v {
			t.Fatalf("%s: amount %v, base %v, PHP round %v", v.Tenge(), got.Amount, got.BonusBase, php)
		}
	}
}

func TestParseTransactionAmount(t *testing.T) {
	cases := []struct {
		in     string
		amount money.Tiyn
		rule   string
	}{
		{in: "300", amount: 30000},
		{in: "12.9", amount: 1290},
		{in: "999999999999.99", amount: MaxShiftAmount},
		{in: "1.005", rule: RuleDecimal},               // Q-002: было 1.01
		{in: "1.00499999999999999", rule: RuleDecimal}, // Q-002: было 1.00
		{in: "12.999", rule: RuleDecimal},              // Q-002: было 13.00
		{in: "1000000000000", rule: RuleMax},           // decimal(14,2) — в старом коде ошибка MySQL (CQ-2)
		{in: "0.00", rule: RuleGt},
		{in: "0", rule: RuleGt},
		{in: "x", rule: RuleNumeric},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseTransactionAmount(c.in)
			if c.rule != "" {
				wantErr(t, err, "amount", c.rule)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			eq(t, "amount", got.Amount, c.amount)
		})
	}
}

func TestParseOpeningBalance(t *testing.T) {
	for in, want := range map[string]money.Tiyn{"0": 0, "-0": 0, "10000": 1000000, "100.05": 10005} {
		got, err := ParseOpeningBalance(in)
		if err != nil || got != want {
			t.Errorf("ParseOpeningBalance(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for in, rule := range map[string]string{
		"-0.01": RuleMin, "": RuleRequired, "abc": RuleNumeric, "1000000000000": RuleMax,
		"100.005": RuleDecimal, "0.004": RuleDecimal, // Q-002: было 100.01 и 0.00
	} {
		_, err := ParseOpeningBalance(in)
		wantErr(t, err, "opening_balance", rule)
	}
}

// Решение Q-002, CQ-3 (docs/DECISIONS.md): «хватает ли наличных» — точно в тиынах. Старый код сравнивал во float:
// остаток 0.30 + 0.60 = 0.8999999999999999, и расход 0.90 отклонялся, хотя точный остаток 0.90.
func TestExpenseExactComparison(t *testing.T) {
	open := &Shift{ID: 1, Status: StatusOpen, OpeningBalance: 30}
	tot := ComputeTotals(30, []Operation{{Type: Income, Amount: 60}})
	eq(t, "exact balance", tot.Balance(), 90)
	amt, _ := ParseTransactionAmount("0.9")
	if err := CheckExpense(open, tot, amt); err != nil {
		t.Fatalf("CQ-3: расход 0.90 при остатке 0.90 должен пройти: %v", err)
	}
	amt, _ = ParseTransactionAmount("0.91")
	wantErr(t, CheckExpense(open, tot, amt), "amount", MsgNotEnoughCash)
	// Остаток 0.1 + 0.2: расход 0.30 проходит, 0.31 — нет.
	tot = ComputeTotals(10, []Operation{{Type: Income, Amount: 20}})
	amt, _ = ParseTransactionAmount("0.3")
	if err := CheckExpense(open, tot, amt); err != nil {
		t.Fatal(err)
	}
	amt, _ = ParseTransactionAmount("0.31")
	wantErr(t, CheckExpense(open, tot, amt), "amount", MsgNotEnoughCash)
	// Возврат прихода 0.90 при остатке 0.30 + 0.60 — тоже точно.
	tot = ComputeTotals(30, []Operation{{ID: 7, ShiftID: 1, Type: Income, Amount: 60}})
	if _, err := CheckRefund(Operation{ID: 8, ShiftID: 1, Type: Income, Amount: 90, Description: POSSale, Receipt: &Receipt{Items: []ReceiptLine{{Name: "x", Quantity: 1}}}}, open, false, tot); err != nil {
		t.Fatalf("refund 0.90 of 0.90: %v", err)
	}
	eq(t, "closing", tot.ClosingBalance(), 90)
}

// Сумма продажи — точно в тиынах (Решение Q-002 (docs/DECISIONS.md)); старый float-расчёт для цен decimal(12,2)
// и целых количеств давал то же самое: 0.10 × 3 + 0.20 во float = 0.5000000000000001 → "0.5" → 50 тиынов.
func TestSaleFloatTotalMatchesExact(t *testing.T) {
	open := &Shift{ID: 1, Status: StatusOpen}
	// 0.10 × 3 = 0.30000000000000004 во float; + 0.20 = 0.5000000000000001 → "0.5" → 50 тиынов.
	res, err := SellPOS(open, []SaleItem{{ProductID: 1, Quantity: 3}, {ServiceID: 2, Quantity: 1}},
		map[int64]Product{1: {ID: 1, Name: "A", Price: 10, Quantity: 5}}, map[int64]Service{2: {ID: 2, Name: "B", Price: 20}})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "amount", res.Amount, 50)

	r := rand.New(rand.NewPCG(20261030, 1))
	for i := 0; i < 20000; i++ {
		n := 1 + r.IntN(10)
		products := map[int64]Product{}
		var items []SaleItem
		var exact money.Tiyn
		for j := 0; j < n; j++ {
			id := int64(j + 1)
			price := money.Tiyn(r.Int64N(10_000_000)) // до 99 999.99
			if r.IntN(10) == 0 {
				price = money.Tiyn(r.Int64N(100_000_000_000)) // до 999 999 999.99
			}
			qty := 1 + r.Int64N(50)
			products[id] = Product{ID: id, Name: "P", Price: price, Quantity: 1000}
			items = append(items, SaleItem{ProductID: id, Quantity: qty})
			exact += price * money.Tiyn(qty)
		}
		res, err := SellPOS(open, items, products, nil)
		if exact > MaxShiftAmount {
			continue
		}
		if err != nil || res.Amount != exact {
			t.Fatalf("case %d: float total %v (%v), exact %v", i, res.Amount, err, exact)
		}
	}
}

func TestSellPOSRules(t *testing.T) {
	open := &Shift{ID: 1, Status: StatusOpen}
	products := map[int64]Product{1: {ID: 1, Name: "Cola", Price: 100, Quantity: 2}}
	_, err := SellPOS(nil, []SaleItem{{ProductID: 1, Quantity: 1}}, products, nil)
	wantErr(t, err, "items", MsgOpenShiftFirst)
	_, err = SellPOS(open, nil, products, nil)
	wantErr(t, err, "items", RuleRequired)
	_, err = SellPOS(open, []SaleItem{{ProductID: 1, Quantity: 1}, {ProductID: 1, Quantity: 1}}, products, nil)
	wantErr(t, err, "items.1.product_id", RuleDistinct)
	_, err = SellPOS(open, []SaleItem{{ProductID: 1, Quantity: 0}}, products, nil)
	wantErr(t, err, "items.0.quantity", RuleMin)
	_, err = SellPOS(open, []SaleItem{{Quantity: 1}}, products, nil)
	wantErr(t, err, "items.0.product_id", RuleRequired)
	_, err = SellPOS(open, []SaleItem{{ProductID: 9, Quantity: 1}}, products, nil)
	wantErr(t, err, "items.0.product_id", RuleIn)
	// Остаток ровно равен количеству — можно, остаток 0.
	res, err := SellPOS(open, []SaleItem{{ProductID: 1, Quantity: 2}}, products, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "balance_after", res.WriteOffs[0].BalanceAfter, 0)
	// Услуга без верхней границы количества: переполнение суммы — ошибка, а не неверное число.
	_, err = SellPOS(open, []SaleItem{{ServiceID: 5, Quantity: 1 << 62}}, nil, map[int64]Service{5: {ID: 5, Price: 100}})
	wantErr(t, err, "items", RuleMax)
	// Если в строке заданы и товар, и услуга — продаётся услуга (! empty($item['service_id'])).
	res, err = SellPOS(open, []SaleItem{{ProductID: 1, ServiceID: 5, Quantity: 1}}, products, map[int64]Service{5: {ID: 5, Name: "S", Price: 700}})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "service line", res.Lines[0].ServiceID, 5)
	eq(t, "no write-off", len(res.WriteOffs), 0)
}

func TestRefundKind(t *testing.T) {
	cases := map[string]struct {
		op   Operation
		want RefundKind
	}{
		"expense":               {Operation{Type: Expense, Description: POSSale, Receipt: &Receipt{Items: []ReceiptLine{}}}, RefundNone},
		"sale":                  {Operation{Type: Income, Description: POSSale, Receipt: &Receipt{Items: []ReceiptLine{{Name: "A"}}}}, RefundSale},
		"sale with empty items": {Operation{Type: Income, Description: POSSale, Receipt: &Receipt{Items: []ReceiptLine{}}}, RefundSale},
		"POS marker, no items":  {Operation{Type: Income, Description: POSSale}, RefundNone},
		"top-up":                {Operation{Type: Income, Receipt: &Receipt{TopUp: &TopUpReceipt{UserID: 7}}}, RefundTopUp},
		"top-up without user":   {Operation{Type: Income, Receipt: &Receipt{TopUp: &TopUpReceipt{}}}, RefundNone},
		"guest session":         {Operation{Type: Income, Kind: KindGuestSession}, RefundNone},
		"other income":          {Operation{Type: Income, Description: "Разное"}, RefundNone},
	}
	for name, c := range cases {
		eq(t, name, c.op.RefundKind(), c.want)
	}
}

func TestCheckRefundOrder(t *testing.T) {
	open := &Shift{ID: 1, Status: StatusOpen}
	other := Operation{ID: 5, ShiftID: 2, Type: Income, Description: "Разное", Amount: 100}
	// Неподходящий вид проверяется раньше смены.
	_, err := CheckRefund(other, nil, true, Totals{})
	wantErr(t, err, "refund", MsgNotRefundable)
	sale := Operation{ID: 6, ShiftID: 1, Type: Income, Description: POSSale, Amount: 100, Receipt: &Receipt{Items: []ReceiptLine{}}}
	_, err = CheckRefund(sale, nil, false, Totals{})
	wantErr(t, err, "refund", MsgRefundOpenShift)
	// Уже возвращено — раньше нехватки денег.
	_, err = CheckRefund(sale, open, true, Totals{})
	wantErr(t, err, "refund", MsgAlreadyRefunded)
	_, err = CheckRefund(sale, open, false, Totals{Opening: 99})
	wantErr(t, err, "refund", MsgNotEnoughCash)
	kind, err := CheckRefund(sale, open, false, Totals{Opening: 100})
	if err != nil || kind != RefundSale {
		t.Fatalf("CheckRefund = %v, %v", kind, err)
	}
	eq(t, "description", RefundDescription(6), "Возврат по чеку №6")
}

func TestPlanTopUpRefund(t *testing.T) {
	// Нет строки балансов → «Клиент уже потратил часть пополнения…».
	_, err := PlanTopUpRefund(TopUpReceipt{UserID: 1, Field: FieldMain}, 100, nil)
	wantErr(t, err, "refund", MsgTopUpSpent)
	// Возврат бонусного пополнения не трогает основной баланс; бонус снимается из остатка после списания.
	got, err := PlanTopUpRefund(TopUpReceipt{UserID: 1, Field: FieldBonus, Bonus: 300}, 500, &MemberBalance{Main: 1000, Bonus: 700})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "result", got, TopUpRefund{Field: FieldBonus, Amount: 500, BonusTakenBack: 200, NewBalance: 0, NewBonusBalance: 0})
	// Ровно равный баланс — можно (строгое «<»).
	got, err = PlanTopUpRefund(TopUpReceipt{UserID: 1, Field: FieldMain, Bonus: 20000}, 200000, &MemberBalance{Main: 200000, Bonus: 5000})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "GAP-024", got, TopUpRefund{Field: FieldMain, Amount: 200000, BonusTakenBack: 5000, NewBalance: 0, NewBonusBalance: 0})
}

// Решение CQ-7 (docs/DECISIONS.md): сортировка «Продано товаров» — по точной сумме в тиынах, затем по имени.
// В PHP usort по float ставил «Б» (0.10 × 3 = 0.30000000000000004) раньше «А» (0.30 × 1 = 0.3); теперь равные
// суммы идут по имени: «А», «Б», «В».
func TestReportItemsSortedByTiynAmount(t *testing.T) {
	ops := []Operation{
		{ID: 1, Type: Income, Description: POSSale, Amount: 30, Receipt: &Receipt{Items: []ReceiptLine{{ProductID: 1, Name: "Б", Quantity: 3, Price: 10, Total: 30}}}},
		{ID: 2, Type: Income, Description: POSSale, Amount: 30, Receipt: &Receipt{Items: []ReceiptLine{{ProductID: 2, Name: "А", Quantity: 1, Price: 30, Total: 30}}}},
		{ID: 3, Type: Income, Description: POSSale, Amount: 30, Receipt: &Receipt{Items: []ReceiptLine{{ProductID: 3, Name: "В", Quantity: 1, Price: 30, Total: 30}}}},
	}
	r := BuildReport(ReportInput{Operations: ops, Stock: map[int64]int64{1: 4, 3: 0}})
	var names []string
	for _, it := range r.Items {
		names = append(names, it.Name)
	}
	eq(t, "order", strings.Join(names, ","), "А,Б,В")
	eq(t, "stock А (нет товара)", r.Items[0].Stock == nil, true)
	eq(t, "stock Б", *r.Items[1].Stock, 4)
	eq(t, "stock В = 0", *r.Items[2].Stock, 0)
	eq(t, "items_total", r.ItemsTotal, 90)

	// Большая сумма — раньше, независимо от имени.
	ops = append(ops, Operation{ID: 4, Type: Income, Description: POSSale, Amount: 31, Receipt: &Receipt{Items: []ReceiptLine{{ProductID: 4, Name: "Я", Quantity: 1, Price: 31, Total: 31}}}})
	r = BuildReport(ReportInput{Operations: ops})
	eq(t, "first by amount", r.Items[0].Name, "Я")
}

func TestReportItemsGroupingAndOther(t *testing.T) {
	ops := []Operation{
		// Старый чек без product_id, затем такой же товар с product_id: product_id — первый непустой.
		{ID: 1, UserID: 0, Type: Income, Description: POSSale, Amount: 100, Receipt: &Receipt{Items: []ReceiptLine{{Name: "Cola", Quantity: 1, Price: 100, Total: 100}}}},
		{ID: 2, UserID: 9, Type: Income, Description: POSSale, Amount: 200, Receipt: &Receipt{Items: []ReceiptLine{{ProductID: 7, Name: "Cola", Quantity: 2, Price: 100, Total: 200}, {Name: "", Quantity: 1, Price: 5, Total: 5}}}},
		// Приход с маркером «Продажа POS», но без чека: в «Продажа товаров» попадает, в товары — нет.
		{ID: 3, UserID: 9, Type: Income, Description: POSSale, Amount: 50},
		{ID: 4, UserID: 9, Type: Income, Description: "Разное", Amount: 33},
		{ID: 5, UserID: 9, Type: Income, Description: "Разное", Amount: 1},
	}
	r := BuildReport(ReportInput{Operations: ops, Stock: map[int64]int64{7: 11}, Names: map[int64]string{}})
	eq(t, "goods", r.Kinds[0], KindRow{LabelKindGoods, 3, 350})
	eq(t, "other", r.Kinds[3], KindRow{LabelKindOther, 2, 34})
	eq(t, "items", len(r.Items), 2)
	eq(t, "cola", r.Items[0].Name+"/"+itoa(r.Items[0].Quantity)+"/"+itoa(*r.Items[0].Stock), "Cola/3/11")
	eq(t, "no name", r.Items[1].Name, NoName)
	// user_id NULL — «—»; неизвестное имя — тоже «—». Сортировка по приходу ↓.
	eq(t, "staff 0", r.Staff[0], StaffRow{UserID: 9, Name: NoName, Count: 4, Income: 284})
	eq(t, "staff 1", r.Staff[1], StaffRow{UserID: 0, Name: NoName, Count: 1, Income: 100})
}

// «Прочий приход», closing_balance и итоги товаров считаются во float + round, как в PHP; на суммах decimal(14,2)
// это совпадает с точной арифметикой.
func TestReportFloatPathsMatchExact(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 7))
	for i := 0; i < 5000; i++ {
		var ops []Operation
		var other money.Tiyn
		for j := 0; j < 1+r.IntN(30); j++ {
			amt := money.Tiyn(1 + r.Int64N(100_000_000))
			op := Operation{ID: int64(j + 1), Type: Income, Amount: amt}
			switch r.IntN(4) {
			case 0:
				op.Description = POSSale
			case 1:
				op.Kind = KindTopUp
			case 2:
				op.Kind = KindGuestSession
			default:
				other += amt
			}
			ops = append(ops, op)
		}
		opening := money.Tiyn(r.Int64N(1_000_000_000))
		rep := BuildReport(ReportInput{Opening: opening, Operations: ops})
		if other > 0 && rep.Kinds[3].Amount != other {
			t.Fatalf("case %d: other = %v, want %v", i, rep.Kinds[3].Amount, other)
		}
		if rep.Totals.ClosingBalance() != rep.Totals.Balance() {
			t.Fatalf("case %d: closing %v != %v", i, rep.Totals.ClosingBalance(), rep.Totals.Balance())
		}
	}
}

func TestNumberFormat(t *testing.T) {
	for in, want := range map[money.Tiyn]string{0: "0,00", 100: "1,00", 210000: "2 100,00", 123456789: "1 234 567,89", -50: "-0,50", 100000000: "1 000 000,00"} {
		eq(t, in.Tenge(), NumberFormat(in), want)
	}
	eq(t, "KZT", KZT(1200000), "12 000,00 KZT")
}

func TestErrorText(t *testing.T) {
	e := &Error{Field: "items", Message: MsgNotEnoughStock, Params: map[string]string{"product": "Cola"}}
	eq(t, "text", e.Text(), "Недостаточно товара: Cola.")
	eq(t, "error", e.Error(), "items: Недостаточно товара: Cola.")
	eq(t, "rule", ruleErr("amount", RuleGt).Error(), "amount: gt")
}

func TestTopUpDescription(t *testing.T) {
	eq(t, "main cash", TopUpDescription(FieldMain, "Ерлан", PayCash), "Пополнение основной баланс: Ерлан (наличные)")
	eq(t, "bonus qr", TopUpDescription(FieldBonus, "Ерлан", PayQR), "Пополнение бонусный баланс: Ерлан (QR-код)")
}

func TestPlanTopUpBonusBase(t *testing.T) {
	open := &Shift{ID: 1, Status: StatusOpen}
	amt, err := ParseTopUpAmount("0.02")
	if err != nil {
		t.Fatal(err)
	}
	var seen money.Tiyn
	plan, err := PlanTopUp(open, 1, "К", FieldMain, amt, PayCash, func(base money.Tiyn) (money.Tiyn, error) { seen = base; return 0, nil })
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "bonus base", seen, 2)
	eq(t, "amount", plan.Amount, 2)
	// Бонусный баланс: функция бонуса не вызывается.
	called := false
	_, err = PlanTopUp(open, 1, "К", FieldBonus, amt, PayCash, func(money.Tiyn) (money.Tiyn, error) { called = true; return 5, nil })
	if err != nil || called {
		t.Fatalf("bonus func called for bonus_balance: %v", err)
	}
	_, err = PlanTopUp(open, 1, "К", FieldMain, amt, PayCash, func(money.Tiyn) (money.Tiyn, error) { return -1, nil })
	if !errors.Is(err, ErrNegativeBonus) {
		t.Fatalf("negative bonus: %v", err)
	}
}

func TestPlanTransaction(t *testing.T) {
	open := &Shift{ID: 1, Status: StatusOpen}
	amt, _ := ParseTransactionAmount("10")
	_, err := PlanTransaction(open, Totals{}, "other", amt, "x", 1)
	wantErr(t, err, "type", RuleIn)
	_, err = PlanTransaction(open, Totals{}, Income, amt, " ", 1)
	wantErr(t, err, "description", RuleRequired)
	_, err = PlanTransaction(open, Totals{}, Income, amt, strings.Repeat("я", 256), 1)
	wantErr(t, err, "description", RuleMax)
	_, err = PlanTransaction(nil, Totals{}, Income, amt, "x", 1)
	wantErr(t, err, "amount", MsgOpenShiftFirst)
	op, err := PlanTransaction(open, Totals{}, Income, amt, strings.Repeat("я", 255), 1)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "other income kind", op.Kind, KindNone)
}

func TestReportPeople(t *testing.T) {
	names := map[int64]string{1: "Айгерим", 2: "Азат"}
	eq(t, "unknown cashier", ReportPeople(Shift{OpenedBy: 9}, names, "Азат"), People{Cashier: NoName, Operator: "Азат"})
	eq(t, "closed by same", ReportPeople(Shift{OpenedBy: 1, ClosedBy: 1}, names, "Азат"), People{Cashier: "Айгерим", Operator: "Азат"})
	eq(t, "closed by other", ReportPeople(Shift{OpenedBy: 1, ClosedBy: 2}, names, "Азат"), People{Cashier: "Айгерим", ClosedBy: "Азат", Operator: "Азат"})
}

// BR-060: StaffShifts::limit() / hasPlace().
func TestShiftLimit(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  int64 // 0 — без лимита
	}{
		{"missing", nil, 0}, {"zero", 0.0, 0}, {"json number", 2.0, 2}, {"float", 2.9, 2},
		{"string", "3", 3}, {"string float", "2.7", 2}, {"string spaces", " 2 ", 2}, {"exp", "1e1", 10},
		{"negative", "-1", 0}, {"text", "abc", 0}, {"bool", true, 0}, {"json.Number", json.Number("4"), 4},
	}
	for _, c := range cases {
		got := ShiftLimit(c.value)
		var g int64
		if got != nil {
			g = *got
		}
		eq(t, c.name, g, c.want)
	}
	one := int64(1)
	eq(t, "admin unlimited", HasShiftPlace("admin", &one, 5), true)
	eq(t, "no limit", HasShiftPlace(RoleCashier, nil, 5), true)
	eq(t, "GS-273 full", HasShiftPlace(RoleCashier, &one, 1), false)
	eq(t, "GS-273 free", HasShiftPlace(RoleCashier, &one, 0), true)
	eq(t, "setting", ShiftLimitSetting(RoleOperator), "max_operators_on_shift")
}
