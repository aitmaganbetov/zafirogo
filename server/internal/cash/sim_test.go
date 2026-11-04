package cash

import (
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// sim — модель кассы клуба в памяти для сценариев GS/GAP: делает то же, что CashRegisterService
// (порядок проверок и записей), но вызывает чистые функции пакета. БД и ledger не участвуют.
type sim struct {
	t        *testing.T
	nextID   int64
	shifts   []*Shift
	ops      []Operation
	products map[int64]*Product
	deleted  map[int64]bool
	services map[int64]Service
	balances map[int64]*MemberBalance
	names    map[int64]string
	bonus    BonusFunc
}

func newSim(t *testing.T) *sim {
	t.Helper()
	return &sim{
		t: t, nextID: 100,
		products: map[int64]*Product{}, deleted: map[int64]bool{}, services: map[int64]Service{},
		balances: map[int64]*MemberBalance{}, names: map[int64]string{},
	}
}

func (s *sim) id() int64 { s.nextID++; return s.nextID }

func (s *sim) user(name string) int64 {
	id := s.id()
	s.names[id] = name
	return id
}

func (s *sim) customer(name string, main, bonus money.Tiyn) int64 {
	id := s.user(name)
	s.balances[id] = &MemberBalance{Main: main, Bonus: bonus}
	return id
}

func (s *sim) product(name string, price money.Tiyn, qty int64) int64 {
	id := s.id()
	s.products[id] = &Product{ID: id, SKU: "SKU-" + name, Name: name, Price: price, Quantity: qty}
	return id
}

func (s *sim) service(name string, price money.Tiyn) int64 {
	id := s.id()
	s.services[id] = Service{ID: id, Name: name, Price: price}
	return id
}

func (s *sim) openShiftPtr() *Shift {
	for _, sh := range s.shifts {
		if sh.IsOpen() {
			return sh
		}
	}
	return nil
}

func (s *sim) shiftOps(id int64) []Operation {
	var out []Operation
	for _, op := range s.ops {
		if op.ShiftID == id {
			out = append(out, op)
		}
	}
	return out
}

func (s *sim) totals(sh *Shift) Totals { return ComputeTotals(sh.OpeningBalance, s.shiftOps(sh.ID)) }

func (s *sim) add(op Operation) Operation {
	op.ID = s.id()
	s.ops = append(s.ops, op)
	return op
}

// open — CashRegisterController::open(): ParseOpeningBalance → CashRegisterService::open().
func (s *sim) open(raw string, by int64) (*Shift, error) {
	amount, err := ParseOpeningBalance(raw)
	if err != nil {
		return nil, err
	}
	if err := CheckOpen(s.openShiftPtr()); err != nil {
		return nil, err
	}
	sh := &Shift{ID: s.id(), Status: StatusOpen, OpenedBy: by, OpeningBalance: amount}
	s.shifts = append(s.shifts, sh)
	return sh, nil
}

func (s *sim) mustOpen(raw string, by int64) *Shift {
	s.t.Helper()
	sh, err := s.open(raw, by)
	if err != nil {
		s.t.Fatalf("open(%s): %v", raw, err)
	}
	return sh
}

func (s *sim) sale(by int64, items ...SaleItem) (Operation, error) {
	open := s.openShiftPtr()
	products := map[int64]Product{}
	for id, p := range s.products {
		if !s.deleted[id] {
			products[id] = *p
		}
	}
	res, err := SellPOS(open, items, products, s.services)
	if err != nil {
		return Operation{}, err
	}
	for _, w := range res.WriteOffs {
		s.products[w.ProductID].Quantity = w.BalanceAfter
	}
	return s.add(Operation{ShiftID: open.ID, UserID: by, Type: Income, Amount: res.Amount, Description: res.Description(), Receipt: &Receipt{Items: res.Lines}}), nil
}

func (s *sim) mustSale(by int64, items ...SaleItem) Operation {
	s.t.Helper()
	op, err := s.sale(by, items...)
	if err != nil {
		s.t.Fatalf("sale: %v", err)
	}
	return op
}

func (s *sim) topUp(by, user int64, field BalanceField, raw string, method PaymentMethod) (Operation, TopUpPlan, error) {
	open := s.openShiftPtr()
	amount, err := ParseTopUpAmount(raw)
	if err != nil {
		return Operation{}, TopUpPlan{}, err
	}
	plan, err := PlanTopUp(open, user, s.names[user], field, amount, method, s.bonus)
	if err != nil {
		return Operation{}, TopUpPlan{}, err
	}
	b := s.balances[user]
	if field == FieldMain {
		b.Main += plan.Amount
	} else {
		b.Bonus += plan.Amount
	}
	b.Bonus += plan.Bonus
	rec := plan.Receipt
	op := s.add(Operation{ShiftID: open.ID, UserID: by, Type: Income, Amount: plan.Amount, Description: plan.Description, Receipt: &rec, Kind: plan.Kind, PaymentMethod: plan.PaymentMethod})
	return op, plan, nil
}

func (s *sim) mustTopUp(by, user int64, field BalanceField, raw string, method PaymentMethod) (Operation, TopUpPlan) {
	s.t.Helper()
	op, plan, err := s.topUp(by, user, field, raw, method)
	if err != nil {
		s.t.Fatalf("topUp(%s): %v", raw, err)
	}
	return op, plan
}

func (s *sim) transact(by int64, typ Type, raw, desc string) (Operation, error) {
	open := s.openShiftPtr()
	amount, err := ParseTransactionAmount(raw)
	if err != nil {
		return Operation{}, err
	}
	var t Totals
	if open != nil {
		t = s.totals(open)
	}
	op, err := PlanTransaction(open, t, typ, amount, desc, by)
	if err != nil {
		return Operation{}, err
	}
	return s.add(op), nil
}

func (s *sim) mustTransact(by int64, typ Type, raw, desc string) Operation {
	s.t.Helper()
	op, err := s.transact(by, typ, raw, desc)
	if err != nil {
		s.t.Fatalf("transact(%s %s): %v", typ, raw, err)
	}
	return op
}

// guest — приход гостевой сессии (GuestSessions::sell(): formatCents(total), журнал cash.income с details.computers).
func (s *sim) guest(by int64, amount money.Tiyn) Operation {
	open := s.openShiftPtr()
	return s.add(Operation{ShiftID: open.ID, UserID: by, Type: Income, Amount: amount, Description: "Гостевая сессия", Kind: KindGuestSession})
}

func (s *sim) refunded(id int64) bool {
	for _, op := range s.ops {
		if op.RefundOfID == id {
			return true
		}
	}
	return false
}

func (s *sim) find(id int64) Operation {
	for _, op := range s.ops {
		if op.ID == id {
			return op
		}
	}
	s.t.Fatalf("no operation %d", id)
	return Operation{}
}

// refund — CashRegisterService::refund().
func (s *sim) refund(by int64, income Operation) (Operation, TopUpRefund, error) {
	open := s.openShiftPtr()
	income = s.find(income.ID)
	var t Totals
	if open != nil {
		t = s.totals(open)
	}
	kind, err := CheckRefund(income, open, s.refunded(income.ID), t)
	if err != nil {
		return Operation{}, TopUpRefund{}, err
	}
	var tr TopUpRefund
	if kind == RefundSale {
		for _, r := range SaleStockReturns(income.Receipt.Items) {
			p := s.productFor(r)
			if p != nil {
				p.Quantity += r.Quantity
			}
		}
	} else {
		top := *income.Receipt.TopUp
		var bal *MemberBalance
		if b, ok := s.balances[top.UserID]; ok {
			c := *b
			bal = &c
		}
		if tr, err = PlanTopUpRefund(top, income.Amount, bal); err != nil {
			return Operation{}, TopUpRefund{}, err
		}
		b := s.balances[top.UserID]
		if tr.Field == FieldMain {
			b.Main = tr.NewBalance
		}
		b.Bonus = tr.NewBonusBalance
	}
	return s.add(RefundOperation(income, open, by)), tr, nil
}

func (s *sim) productFor(r StockReturn) *Product {
	if r.ProductID != 0 {
		if s.deleted[r.ProductID] {
			return nil
		}
		return s.products[r.ProductID]
	}
	var best *Product
	for id, p := range s.products {
		if !s.deleted[id] && p.Name == r.Name && (best == nil || id < best.ID) {
			best = p
		}
	}
	return best
}

func (s *sim) close(sh *Shift, by int64, printed bool) error {
	closing, err := Close(sh, s.totals(sh), printed)
	if err != nil {
		return err
	}
	sh.Status, sh.ClosedBy, sh.ClosingBalance = StatusClosed, by, closing
	return nil
}

func (s *sim) report(sh *Shift) Report {
	stock := map[int64]int64{}
	for id, p := range s.products {
		stock[id] = p.Quantity // withTrashed: и удалённые
	}
	return BuildReport(ReportInput{Opening: sh.OpeningBalance, Operations: s.shiftOps(sh.ID), Stock: stock, Names: s.names})
}

// topUpBonusFor — модель TopUpBonus::for() для тестов (сам расчёт — пакет бонусов):
// percent = max процентов достигнутых уровней (PercentLevels::percentAt(): $cents >= (int) round(amount * 100));
// bonus = intdiv($cents * (int) round($percent * 100) + 5000, 10000).
func topUpBonusFor(levels ...[2]float64) BonusFunc {
	return func(base money.Tiyn) (money.Tiyn, error) {
		percent := 0.0
		for _, l := range levels {
			if int64(base) >= money.PHPIntCast(money.PHPRound(l[0]*100, 0)) {
				percent = max(percent, l[1])
			}
		}
		return money.PercentOfHalfUp(base, money.BasisPoints(percent))
	}
}

func wantErr(t *testing.T, err error, field, msg string) {
	t.Helper()
	e, ok := AsError(err)
	if !ok {
		t.Fatalf("want error %s %q, got %v", field, msg, err)
	}
	if e.Field != field || (msg != "" && e.Text() != msg && e.Rule != msg) {
		t.Fatalf("want error %s %q, got %s %q (rule %q)", field, msg, e.Field, e.Text(), e.Rule)
	}
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}
