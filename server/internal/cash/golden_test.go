package cash

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Эталонные сценарии кассы: docs/golden-scenarios.md (GS — из Pest-тестов старого проекта, GAP — по коду,
// числа сверены с tools/php-oracle/tests/GapScenariosTest.php). Имя подтеста = ID сценария.
func TestGoldenCash(t *testing.T) {
	cases := map[string]func(t *testing.T){
		// TopUpBonusTest.php — «a top-up of the main balance adds the bonus of the level it reaches; cash takes only the money»
		"GS-002": func(t *testing.T) {
			s := newSim(t)
			s.bonus = topUpBonusFor([2]float64{2000, 5}, [2]float64{10000, 10})
			cashier := s.user("Кассир")
			c := s.customer("Клиент бонус", 10000, 2500)
			s.mustOpen("0", cashier)

			_, p := s.mustTopUp(cashier, c, FieldMain, "1000", PayCash)
			eq(t, "bonus 1000", p.Bonus, 0)
			eq(t, "bonus balance", s.balances[c].Bonus, 2500)

			_, p = s.mustTopUp(cashier, c, FieldMain, "2500", PayCash)
			eq(t, "bonus 2500", p.Bonus, 12500)
			eq(t, "main", s.balances[c].Main, 360000)
			eq(t, "bonus balance", s.balances[c].Bonus, 15000)

			_, p = s.mustTopUp(cashier, c, FieldMain, "10000.55", PayCash)
			eq(t, "bonus 10000.55", p.Bonus, 100006) // intdiv(1000055×1000+5000, 10000)
			eq(t, "bonus balance", s.balances[c].Bonus, 115006)
			eq(t, "main", s.balances[c].Main, 1360055)
			eq(t, "cash income", s.totals(s.openShiftPtr()).Income, 1350055)
		},
		// «no bonus when it is switched off or the bonus balance is topped up»
		"GS-003": func(t *testing.T) {
			s := newSim(t)
			cashier := s.user("Кассир")
			c := s.customer("Клиент бонус", 10000, 2500)
			s.mustOpen("0", cashier)
			s.bonus = nil // topup_bonus_enabled = false → levels() = []
			s.mustTopUp(cashier, c, FieldMain, "1000", PayCash)
			eq(t, "bonus balance", s.balances[c].Bonus, 2500)
			s.bonus = topUpBonusFor([2]float64{100, 50})
			_, p := s.mustTopUp(cashier, c, FieldBonus, "1000", PayCash)
			eq(t, "bonus", p.Bonus, 0)
			eq(t, "bonus balance", s.balances[c].Bonus, 102500)
		},
		// CashRefundTest.php — «a refund of a sale pays the money out of the shift and puts the goods back to the stock»
		"GS-004": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			sh := s.mustOpen("1000", owner)
			cola := s.product("Coca Cola", 70000, 10)
			sale := s.mustSale(owner, SaleItem{ProductID: cola, Quantity: 3})
			eq(t, "sale", sale.Amount, 210000)
			eq(t, "stock", s.products[cola].Quantity, 7)
			eq(t, "refund button", CanShowRefund(sale, false, true, sh), true)

			ref, _, err := s.refund(owner, sale)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, "refund type", ref.Type, Expense)
			eq(t, "refund amount", ref.Amount, 210000)
			eq(t, "refund_of_id", ref.RefundOfID, sale.ID)
			eq(t, "stock", s.products[cola].Quantity, 10)
			eq(t, "balance", s.totals(sh).Balance(), 100000)
			eq(t, "flash amount", NumberFormat(ref.Amount), "2 100,00") // «Возврат проведён: 2 100,00 KZT выданы из кассы.»
			eq(t, "refund button after", CanShowRefund(sale, true, true, sh), false)

			_, _, err = s.refund(owner, sale)
			wantErr(t, err, "refund", MsgAlreadyRefunded)

			lines := s.report(sh).Lines(sh.ID, People{Operator: "Владелец"})
			hasLine(t, lines, LabelNoGoods)
			hasLine(t, lines, "Возвраты (расход) · 1 · − 2 100,00 KZT")
		},
		// «a refund of a top-up takes the amount and its bonus back from the customer balance»
		"GS-005": func(t *testing.T) {
			s := newSim(t)
			s.bonus = topUpBonusFor([2]float64{1000, 10})
			owner := s.user("Владелец")
			c := s.customer("Клиент", 0, 0)
			s.mustOpen("0", owner)
			top, _ := s.mustTopUp(owner, c, FieldMain, "2000", PayCash)
			eq(t, "bonus balance", s.balances[c].Bonus, 20000)

			s.balances[c].Main = 150000
			_, _, err := s.refund(owner, top)
			wantErr(t, err, "refund", MsgTopUpSpent)

			s.balances[c].Main = 250000
			_, tr, err := s.refund(owner, top)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, "main", s.balances[c].Main, 50000)
			eq(t, "bonus", s.balances[c].Bonus, 0)
			eq(t, "bonus taken back", tr.BonusTakenBack, 20000)
		},
		// «cashiers refund only with the right from the owner; other income is not refunded»
		"GS-006": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			cashier := s.user("Кассир")
			sh := s.mustOpen("5000", owner)
			p := s.product("Товар", 10000, 5)
			sale := s.mustSale(owner, SaleItem{ProductID: p, Quantity: 1})
			other := s.mustTransact(owner, Income, "300", "Разное")
			eq(t, "no right: sale button", CanShowRefund(sale, false, false, sh), false)
			eq(t, "right: sale button", CanShowRefund(sale, false, true, sh), true)
			eq(t, "right: other button", CanShowRefund(other, false, true, sh), false)
			_, _, err := s.refund(cashier, other)
			wantErr(t, err, "refund", MsgNotRefundable)
			ref, _, err := s.refund(cashier, sale)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, "refund user", ref.UserID, cashier)
		},
		// CashRegisterTest.php — «cash shift can be opened and only one can remain open»
		"GS-007": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			s.mustOpen("10000", owner)
			_, err := s.open("5000", owner)
			wantErr(t, err, "opening_balance", MsgShiftAlreadyOpen)
			eq(t, "shifts", len(s.shifts), 1)
		},
		// «cash operations are included in X report»
		"GS-008": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			sh := s.mustOpen("10000", owner)
			s.mustTransact(owner, Income, "3000", "Продажа")
			s.mustTransact(owner, Expense, "1000", "Расход")
			eq(t, "ops", len(s.shiftOps(sh.ID)), 2)
			eq(t, "X-report allowed", CheckXReport(s.openShiftPtr()), true)
			hasLine(t, s.report(sh).Lines(sh.ID, People{}), "Итог в кассе · 12 000,00 KZT")
		},
		// «Z report must be printed before the shift can be closed»
		"GS-009": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			sh := s.mustOpen("5000", owner)
			s.mustTransact(owner, Income, "2000", "Приход")
			wantErr(t, s.close(sh, owner, false), "shift", MsgPrintZFirst)
			eq(t, "status", sh.Status, StatusOpen)
			if err := s.close(sh, owner, true); err != nil {
				t.Fatal(err)
			}
			eq(t, "status", sh.Status, StatusClosed)
			eq(t, "closing_balance", sh.ClosingBalance, 700000)
			wantErr(t, s.close(sh, owner, true), "shift", MsgShiftAlreadyClose)
			eq(t, "X-report on closed", CheckXReport(s.openShiftPtr()), false)
		},
		// «POS sale writes off products and records cash income»
		"GS-010": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			s.mustOpen("0", owner)
			p := s.product("Товар", 75000, 5)
			res, err := SellPOS(s.openShiftPtr(), []SaleItem{{ProductID: p, Quantity: 2}}, map[int64]Product{p: *s.products[p]}, nil)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, "amount", res.Amount, 150000)
			eq(t, "line qty", res.Lines[0].Quantity, 2)
			eq(t, "balance_after", res.WriteOffs[0].BalanceAfter, 3)
			eq(t, "description", res.Description(), POSSale)
		},
		// «cashier can top up a users main or bonus balance from the POS»
		"GS-012": func(t *testing.T) {
			s := newSim(t)
			cashier := s.user("Кассир")
			c := s.customer("Клиент POS", 10000, 2500)
			s.mustOpen("500", cashier)
			op, _ := s.mustTopUp(cashier, c, FieldMain, "750", PayCard)
			eq(t, "main", s.balances[c].Main, 85000)
			eq(t, "bonus", s.balances[c].Bonus, 2500)
			eq(t, "type", op.Type, Income)
			eq(t, "amount", op.Amount, 75000)
			eq(t, "description", op.Description, "Пополнение основной баланс: Клиент POS (карта)")
			eq(t, "kind", op.Kind, KindTopUp)
			eq(t, "payment_method (Q-013)", op.PaymentMethod, PayCard)
		},
		// «balance top-up validates user, amount, account, and payment method»
		"GS-013": func(t *testing.T) {
			_, err := ParseTopUpAmount("0")
			wantErr(t, err, "amount", RuleGt)
			open := &Shift{ID: 1, Status: StatusOpen}
			ok := TopUpAmount{Amount: 100, BonusBase: 100}
			_, err = PlanTopUp(open, 0, "", FieldMain, ok, PayCash, nil)
			wantErr(t, err, "user_id", RuleIn)
			_, err = PlanTopUp(open, 5, "", "unknown", ok, PayCash, nil)
			wantErr(t, err, "balance_type", RuleIn)
			_, err = PlanTopUp(open, 5, "", FieldMain, ok, "unknown", nil)
			wantErr(t, err, "payment_method", RuleIn)
		},
		// ShiftReportTest.php — «the X and Z report show what the shift consists of: income by kind, goods sold and staff»
		"GS-015": func(t *testing.T) {
			s := newSim(t)
			cashier := s.user("Айгерим")
			c := s.customer("Клиент", 0, 0)
			sh := s.mustOpen("1000", cashier)
			cola := s.product("Coca Cola", 70000, 20)
			chips := s.product("Чипсы", 50000, 20)
			eq(t, "sale 1", s.mustSale(cashier, SaleItem{ProductID: cola, Quantity: 3}, SaleItem{ProductID: chips, Quantity: 1}).Amount, 260000)
			eq(t, "sale 2", s.mustSale(cashier, SaleItem{ProductID: cola, Quantity: 2}).Amount, 140000)
			s.mustTopUp(cashier, c, FieldMain, "5000", PayCard)
			s.guest(cashier, 60000)
			s.mustTransact(cashier, Expense, "300", "Вода для кулера")

			for _, typ := range []ReportType{ReportX, ReportZ} {
				r := s.report(sh)
				lines := r.Lines(sh.ID, People{Cashier: "Айгерим", Operator: "Айгерим"})
				got := section(lines, "kinds")
				want := []string{
					"Продажа товаров · 2 · 4 000,00 KZT",
					"Пополнение балансов · 1 · 5 000,00 KZT",
					"Гостевые сессии · 1 · 600,00 KZT",
				}
				if !slices.Equal(got, want) {
					t.Fatalf("%s kinds = %q, want %q", typ, got, want)
				}
				eqLines(t, string(typ)+" items", section(lines, "items"), []string{"Coca Cola · 5 · 15", "Чипсы · 1 · 19", "Всего · 6"})
				eqLines(t, string(typ)+" staff", section(lines, "staff"), []string{"Айгерим · 5 · 9 600,00 KZT · 300,00 KZT"})
			}
		},
		// «an empty shift says no goods were sold»
		"GS-016": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			sh := s.mustOpen("0", owner)
			lines := s.report(sh).Lines(sh.ID, People{})
			eqLines(t, "items", section(lines, "items"), []string{LabelNoGoods})
			eqLines(t, "staff", section(lines, "staff"), nil)
			eq(t, "kinds rows", len(section(lines, "kinds")), 3)
		},
		// «the report names the cashier of the shift, who closed it and the operator who prints it»
		"GS-017": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Азат")
			cashier := s.user("Айгерим")
			sh := s.mustOpen("0", cashier)
			x := s.report(sh).Lines(sh.ID, ReportPeople(*sh, s.names, s.names[owner]))
			eqLines(t, "X info", section(x, "info"), []string{"Номер смены · #" + itoa(sh.ID), "Кассир · Айгерим", "Оператор · Азат"})
			if err := s.close(sh, owner, true); err != nil {
				t.Fatal(err)
			}
			z := s.report(sh).Lines(sh.ID, ReportPeople(*sh, s.names, s.names[owner]))
			eqLines(t, "Z info", section(z, "info"), []string{"Номер смены · #" + itoa(sh.ID), "Кассир · Айгерим", "Закрыл смену · Азат", "Оператор · Азат"})
		},
		// ClubServiceTest.php — «a manager keeps the services; a cashier sells them with goods in one receipt, without stock»
		"GS-141": func(t *testing.T) {
			s := newSim(t)
			cashier := s.user("Кассир")
			sh := s.mustOpen("0", cashier)
			ps5 := s.service("PS5 — 1 час", 150000)
			old := s.id() // «Старое» неактивна: нет в выборке активных услуг
			cola := s.product("Cola", 50000, 10)
			sale := s.mustSale(cashier, SaleItem{ServiceID: ps5, Quantity: 2}, SaleItem{ProductID: cola, Quantity: 1})
			eq(t, "sale", sale.Amount, 350000)
			eq(t, "cola", s.products[cola].Quantity, 9)
			hasLine(t, s.report(sh).Lines(sh.ID, People{}), "PS5 — 1 час · 2 · —")
			_, err := s.sale(cashier, SaleItem{ServiceID: old, Quantity: 1})
			wantErr(t, err, "items.0.service_id", RuleIn)
		},
		// «refunding a sale with a service returns the money and the goods only»
		"GS-142": func(t *testing.T) {
			s := newSim(t)
			cashier := s.user("Кассир")
			s.mustOpen("1000", cashier)
			printing := s.service("Распечатка", 5000)
			cola := s.product("Cola", 50000, 10)
			sale := s.mustSale(cashier, SaleItem{ServiceID: printing, Quantity: 10}, SaleItem{ProductID: cola, Quantity: 2})
			eq(t, "sale", sale.Amount, 150000)
			ref, _, err := s.refund(cashier, sale)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, "cola", s.products[cola].Quantity, 10)
			eq(t, "refund", ref.Amount, 150000)
		},
		// StaffApiTest.php — «a cashier works the whole shift: open, top-up with a bonus, sale, cash out, close with a Z-report»
		"GS-277": func(t *testing.T) {
			s := newSim(t)
			s.bonus = topUpBonusFor([2]float64{1000, 10})
			cashier := s.user("Кассир")
			guest := s.customer("Гость", 0, 0)
			cola := s.product("Cola", 35000, 10)
			_, _, err := s.topUp(cashier, guest, FieldMain, "2000", PayCard)
			wantErr(t, err, "amount", MsgOpenShiftFirst) // 422
			sh := s.mustOpen("5000", cashier)
			_, p := s.mustTopUp(cashier, guest, FieldMain, "2000", PayCard)
			eq(t, "bonus", p.Bonus.Tenge(), "200.00")
			eq(t, "balance", s.balances[guest].Main.Tenge(), "2000.00")
			eq(t, "bonus_balance", s.balances[guest].Bonus.Tenge(), "200.00")
			sale := s.mustSale(cashier, SaleItem{ProductID: cola, Quantity: 2})
			eq(t, "sale", sale.Amount.Tenge(), "700.00")
			eq(t, "line total", sale.Receipt.Items[0].Total.Tenge(), "700.00")
			s.mustTransact(cashier, Expense, "100", "Вода")
			_, err = s.transact(cashier, Expense, "99999", "Вода")
			wantErr(t, err, "amount", MsgNotEnoughCash)
			tot := s.totals(sh)
			eq(t, "income", tot.Income.Tenge(), "2700.00")
			eq(t, "expense", tot.Expense.Tenge(), "100.00")
			eq(t, "balance", tot.Balance().Tenge(), "7600.00")
			r := s.report(sh)
			eq(t, "items.0", r.Items[0].Name, "Cola")
			eq(t, "transactions", len(s.shiftOps(sh.ID)), 3)
			if err := s.close(sh, cashier, true); err != nil { // приложение закрывает без печати
				t.Fatal(err)
			}
			eq(t, "closing", sh.ClosingBalance.Tenge(), "7600.00")
			eq(t, "staff.0", s.report(sh).Staff[0].Name, "Кассир")
			eq(t, "open shift", s.openShiftPtr() == nil, true)
		},

		// --- GAP: CashRegisterService ---
		"GAP-022": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			c := s.customer("Клиент", 0, 0)
			sh := s.mustOpen("0", owner)
			top, _ := s.mustTopUp(owner, c, FieldMain, "1000", PayCash)
			s.mustTransact(owner, Expense, "800", "Расход")
			_, _, err := s.refund(owner, top)
			wantErr(t, err, "refund", MsgNotEnoughCash)
			eq(t, "main", s.balances[c].Main.Tenge(), "1000.00")
			eq(t, "bonus", s.balances[c].Bonus.Tenge(), "0.00")
			eq(t, "cash", s.totals(sh).Balance(), 20000)
		},
		"GAP-023": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			first := s.mustOpen("0", owner)
			cola := s.product("Cola", 70000, 10)
			sale := s.mustSale(owner, SaleItem{ProductID: cola, Quantity: 1})
			if err := s.close(first, owner, true); err != nil {
				t.Fatal(err)
			}
			s.mustOpen("0", owner)
			_, _, err := s.refund(owner, sale)
			wantErr(t, err, "refund", MsgRefundOpenShift)
			eq(t, "button on closed shift", CanShowRefund(sale, false, true, first), false)
		},
		"GAP-024": func(t *testing.T) {
			s := newSim(t)
			s.bonus = topUpBonusFor([2]float64{1000, 10})
			owner := s.user("Владелец")
			c := s.customer("Клиент", 0, 0)
			s.mustOpen("0", owner)
			top, _ := s.mustTopUp(owner, c, FieldMain, "2000", PayCash)
			eq(t, "after top-up", s.balances[c].Main.Tenge()+"/"+s.balances[c].Bonus.Tenge(), "2000.00/200.00")
			s.balances[c].Bonus = 5000
			ref, tr, err := s.refund(owner, top)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, "after refund", s.balances[c].Main.Tenge()+"/"+s.balances[c].Bonus.Tenge(), "0.00/0.00")
			eq(t, "bonus_taken_back", tr.BonusTakenBack, 5000)
			eq(t, "refund amount", ref.Amount.Tenge(), "2000.00")
		},
		"GAP-025": func(t *testing.T) {
			s := newSim(t)
			s.bonus = topUpBonusFor([2]float64{100, 10})
			owner := s.user("Владелец")
			c := s.customer("Клиент", 0, 0)
			s.mustOpen("0", owner)
			top, _ := s.mustTopUp(owner, c, FieldBonus, "500", PayCash)
			eq(t, "after top-up", s.balances[c].Main.Tenge()+"/"+s.balances[c].Bonus.Tenge(), "0.00/500.00")
			if _, _, err := s.refund(owner, top); err != nil {
				t.Fatal(err)
			}
			eq(t, "after refund", s.balances[c].Main.Tenge()+"/"+s.balances[c].Bonus.Tenge(), "0.00/0.00")
			second, _ := s.mustTopUp(owner, c, FieldBonus, "500", PayCash)
			s.balances[c].Bonus = 40000
			_, _, err := s.refund(owner, second)
			wantErr(t, err, "refund", MsgTopUpSpent)
		},
		"GAP-026": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			cola := s.product("Cola", 70000, 2)
			_, err := s.sale(owner, SaleItem{ProductID: cola, Quantity: 1})
			wantErr(t, err, "items", MsgOpenShiftFirst)
			s.mustOpen("0", owner)
			_, err = s.sale(owner, SaleItem{ProductID: cola, Quantity: 3})
			wantErr(t, err, "items", "Недостаточно товара: Cola.")
			eq(t, "stock", s.products[cola].Quantity, 2)
			eq(t, "transactions", len(s.ops), 0)
		},
		"GAP-027": func(t *testing.T) {
			s := newSim(t)
			owner := s.user("Владелец")
			c := s.customer("Клиент", 0, 0)
			sh := s.mustOpen("1000", owner)
			cola := s.product("Cola", 70000, 10)
			sale := s.mustSale(owner, SaleItem{ProductID: cola, Quantity: 3})
			s.mustTopUp(owner, c, FieldMain, "5000", PayCash)
			s.mustTransact(owner, Expense, "300", "Вода")
			if _, _, err := s.refund(owner, sale); err != nil {
				t.Fatal(err)
			}
			if err := s.close(sh, owner, true); err != nil {
				t.Fatal(err)
			}
			tot := s.totals(sh)
			eq(t, "income", tot.Income, 710000)
			eq(t, "expense", tot.Expense, 240000)
			eq(t, "balance", tot.Balance(), 570000)
			eq(t, "closing_balance", sh.ClosingBalance.Tenge(), "5700.00")
		},

		// --- GAP: ShiftReport ---
		"GAP-028": func(t *testing.T) {
			s := newSim(t)
			cashier := s.user("Айгерим")
			sh := s.mustOpen("0", cashier)
			cola := s.product("Coca Cola", 70000, 20)
			sale := s.mustSale(cashier, SaleItem{ProductID: cola, Quantity: 2})
			s.mustTransact(cashier, Income, "300", "Разное")
			s.guest(cashier, 60000)
			if _, _, err := s.refund(cashier, sale); err != nil {
				t.Fatal(err)
			}
			r := s.report(sh)
			wantKinds := []KindRow{
				{LabelKindGoods, 1, 140000}, {LabelKindTopUps, 0, 0}, {LabelKindGuests, 1, 60000}, {LabelKindOther, 1, 30000},
			}
			if !slices.Equal(r.Kinds, wantKinds) {
				t.Fatalf("kinds = %v, want %v", r.Kinds, wantKinds)
			}
			eq(t, "items", len(r.Items), 0)
			eq(t, "refunds", r.Refunds, RefundsRow{Count: 1, Amount: 140000})
			if !slices.Equal(r.Staff, []StaffRow{{UserID: cashier, Name: "Айгерим", Count: 4, Income: 230000, Expense: 140000}}) {
				t.Fatalf("staff = %v", r.Staff)
			}
		},
		"GAP-029": func(t *testing.T) {
			s := newSim(t)
			a := s.user("А")
			b := s.user("Б")
			sh := s.mustOpen("0", a)
			s.mustTransact(a, Income, "500", "А")
			s.mustTransact(b, Income, "1500", "Б")
			s.mustTransact(b, Expense, "100", "Б расход")
			r := s.report(sh)
			want := []StaffRow{{UserID: b, Name: "Б", Count: 2, Income: 150000, Expense: 10000}, {UserID: a, Name: "А", Count: 1, Income: 50000}}
			if !slices.Equal(r.Staff, want) {
				t.Fatalf("staff = %v, want %v", r.Staff, want)
			}
			eq(t, "discount", r.Discount, 0)
		},
	}
	for _, id := range sortedKeys(cases) {
		t.Run(id, cases[id])
	}
}

// Примеры из docs/business-rules.md §6 (BR-050…BR-057).
func TestBusinessRuleExamples(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"BR-051/пример": func(t *testing.T) {
			s := newSim(t)
			u := s.user("Кассир")
			c := s.customer("Клиент", 0, 0)
			sh := s.mustOpen("10000", u)
			p := s.product("Товар", 250000, 1)
			s.mustSale(u, SaleItem{ProductID: p, Quantity: 1})
			s.mustTopUp(u, c, FieldMain, "5000", PayCash)
			s.mustTransact(u, Expense, "1200", "Расход")
			eq(t, "balance", s.totals(sh).Balance().Tenge(), "16300.00")
		},
		"BR-052/пример": func(t *testing.T) {
			s := newSim(t)
			u := s.user("Кассир")
			s.mustOpen("0", u)
			cola := s.product("Cola", 65000, 10)
			pr := s.service("Печать", 5000)
			sale := s.mustSale(u, SaleItem{ProductID: cola, Quantity: 2}, SaleItem{ServiceID: pr, Quantity: 1})
			eq(t, "total", sale.Amount.Tenge(), "1350.00")
			eq(t, "cola", s.products[cola].Quantity, 8)
		},
		"BR-053/пример": func(t *testing.T) {
			s := newSim(t)
			s.bonus = topUpBonusFor([2]float64{5000, 7.5})
			u := s.user("Кассир")
			c := s.customer("Клиент", 0, 0)
			sh := s.mustOpen("0", u)
			s.mustTopUp(u, c, FieldMain, "5000.00", PayCash)
			eq(t, "main", s.balances[c].Main.Tenge(), "5000.00")
			eq(t, "bonus", s.balances[c].Bonus.Tenge(), "375.00")
			eq(t, "cash", s.totals(sh).Balance().Tenge(), "5000.00")
		},
		"BR-055/пример": func(t *testing.T) {
			s := newSim(t)
			s.bonus = topUpBonusFor([2]float64{5000, 7.5})
			u := s.user("Кассир")
			c := s.customer("Клиент", 0, 0)
			sh := s.mustOpen("0", u)
			top, _ := s.mustTopUp(u, c, FieldMain, "5000", PayCash)
			s.balances[c].Bonus = 7500 // потратил 300.00 бонусов
			_, tr, err := s.refund(u, top)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, "main", s.balances[c].Main, 0)
			eq(t, "bonus back", tr.BonusTakenBack.Tenge(), "75.00")
			eq(t, "cash", s.totals(sh).Balance(), 0)
		},
		"BR-057/пример": func(t *testing.T) {
			s := newSim(t)
			u := s.user("Кассир")
			c := s.customer("Клиент", 0, 0)
			sh := s.mustOpen("0", u)
			cola := s.product("Cola", 65000, 10)
			pr := s.service("Печать", 5000)
			s.mustSale(u, SaleItem{ProductID: cola, Quantity: 2}, SaleItem{ServiceID: pr, Quantity: 1})
			sale2 := s.mustSale(u, SaleItem{ProductID: cola, Quantity: 1})
			s.mustTopUp(u, c, FieldMain, "5000", PayCash)
			s.guest(u, 150000)
			s.mustTransact(u, Income, "1000", "размен")
			if _, _, err := s.refund(u, sale2); err != nil {
				t.Fatal(err)
			}
			r := s.report(sh)
			want := []KindRow{{LabelKindGoods, 2, 200000}, {LabelKindTopUps, 1, 500000}, {LabelKindGuests, 1, 150000}, {LabelKindOther, 1, 100000}}
			if !slices.Equal(r.Kinds, want) {
				t.Fatalf("kinds = %v", r.Kinds)
			}
			eq(t, "refunds", r.Refunds, RefundsRow{1, 65000})
			eq(t, "items", len(r.Items), 2)
			eq(t, "item 0", r.Items[0].Name+"/"+itoa(r.Items[0].Quantity)+"/"+r.Items[0].Amount.Tenge(), "Cola/2/1300.00")
			eq(t, "item 1", r.Items[1].Name+"/"+itoa(r.Items[1].Quantity), "Печать/1")
			eq(t, "items_total", r.ItemsTotal.Tenge(), "1350.00")
		},
	}
	for _, id := range sortedKeys(cases) {
		t.Run(id, cases[id])
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func section(lines []Line, name string) []string {
	var out []string
	for _, l := range lines {
		if l.Section == name {
			out = append(out, l.String())
		}
	}
	return out
}

func hasLine(t *testing.T, lines []Line, want string) {
	t.Helper()
	for _, l := range lines {
		if l.String() == want {
			return
		}
	}
	var all []string
	for _, l := range lines {
		all = append(all, l.String())
	}
	t.Fatalf("no line %q in:\n%s", want, strings.Join(all, "\n"))
}

func eqLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
