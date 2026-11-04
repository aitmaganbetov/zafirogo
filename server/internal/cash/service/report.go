package service

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ReportParams — X- или Z-отчёт. X — по открытой смене (ShiftID не нужен), Z — по смене ShiftID (любой).
type ReportParams struct {
	ClubID       int64
	Type         cash.ReportType
	ShiftID      int64
	OperatorName string // кто формирует отчёт (шапка «Оператор»)
}

// ReportResult — отчёт смены: данные, шапка и строки в порядке Blade-шаблона.
type ReportResult struct {
	Shift  store.CashShift
	Report cash.Report
	People cash.People
	Lines  []cash.Line
}

// Report — X/Z-отчёт смены (BR-057).
//
// Laravel: app/Http/Controllers/CashRegisterController.php xReport() (открытая смена firstOrFail → 404), zReport();
// app/Services/ShiftReport.php for(); CashRegisterService::totals(). Агрегаты (итоги, виды прихода, возвраты, сотрудники)
// считает SQL; строки «Продано товаров» — по чекам невозвращённых продаж POS смены через cash.BuildReport (там
// float-суммы и сортировка как в PHP). Вид прихода — cash_transactions.income_kind (OQ-BR-04).
func (s *Service) Report(ctx context.Context, q *store.Queries, p ReportParams) (ReportResult, error) {
	var (
		row store.CashShift
		err error
	)
	switch p.Type {
	case cash.ReportX:
		row, err = q.GetOpenCashShift(ctx, p.ClubID)
	case cash.ReportZ:
		row, err = q.GetCashShift(ctx, store.GetCashShiftParams{ClubID: p.ClubID, ID: p.ShiftID})
	default:
		return ReportResult{}, fmt.Errorf("cash: unknown report type %q", p.Type)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ReportResult{}, ErrNotFound
	}
	if err != nil {
		return ReportResult{}, fmt.Errorf("cash: report shift: %w", err)
	}
	report, names, err := BuildShiftReport(ctx, q, p.ClubID, &row)
	if err != nil {
		return ReportResult{}, err
	}
	shift := ShiftOf(&row)
	people := cash.ReportPeople(*shift, names, p.OperatorName)
	return ReportResult{Shift: row, Report: report, People: people, Lines: report.Lines(row.ID, people)}, nil
}

// BuildShiftReport — состав отчёта смены из БД (ShiftReport::for() + totals()) и имена сотрудников смены
// (включая открывшего и закрывшего).
func BuildShiftReport(ctx context.Context, q *store.Queries, clubID int64, shift *store.CashShift) (cash.Report, map[int64]string, error) {
	k, err := q.CashShiftReportKinds(ctx, store.CashShiftReportKindsParams{ClubID: clubID, CashShiftID: shift.ID})
	if err != nil {
		return cash.Report{}, nil, fmt.Errorf("cash: report kinds: %w", err)
	}
	r := cash.Report{
		Totals: cash.Totals{
			Opening: money.Tiyn(shift.OpeningBalance), Income: money.Tiyn(k.IncomeAmount), Expense: money.Tiyn(k.ExpenseAmount),
		},
		Kinds: []cash.KindRow{
			{Label: cash.LabelKindGoods, Count: k.GoodsCount, Amount: money.Tiyn(k.GoodsAmount)},
			{Label: cash.LabelKindTopUps, Count: k.TopUpCount, Amount: money.Tiyn(k.TopUpAmount)},
			{Label: cash.LabelKindGuests, Count: k.GuestCount, Amount: money.Tiyn(k.GuestAmount)},
		},
		Refunds: cash.RefundsRow{Count: k.RefundCount, Amount: money.Tiyn(k.RefundAmount)},
	}
	counted := k.GoodsCount + k.TopUpCount + k.GuestCount
	if k.IncomeCount > counted {
		// Laravel: ShiftReport::for() — $countedAmount += (float) $sum->amount по видам;
		// 'amount' => round((float) $all->amount - $countedAmount, 2) (float, как в cash.BuildReport).
		countedAmount := 0.0
		for _, row := range r.Kinds {
			countedAmount += row.Amount.Float()
		}
		r.Kinds = append(r.Kinds, cash.KindRow{
			Label: cash.LabelKindOther, Count: k.IncomeCount - counted,
			Amount: money.Tiyn(money.PHPIntCast(math.Round(money.PHPRound(money.Tiyn(k.IncomeAmount).Float()-countedAmount, 2) * 100))),
		})
	}

	// Продано товаров: невозвращённые продажи POS смены (чеки), остатки товаров сейчас (включая удалённые).
	sales, err := q.ListCashShiftSalesForReport(ctx, store.ListCashShiftSalesForReportParams{ClubID: clubID, CashShiftID: shift.ID})
	if err != nil {
		return cash.Report{}, nil, fmt.Errorf("cash: report sales: %w", err)
	}
	ops := make([]cash.Operation, 0, len(sales))
	var productIDs []int64
	for _, sale := range sales {
		receipt, err := decodeReceipt(sale.ReceiptData)
		if err != nil {
			return cash.Report{}, nil, err
		}
		ops = append(ops, cash.Operation{ID: sale.ID, ShiftID: shift.ID, Type: cash.Income, Description: cash.POSSale, Receipt: receipt})
		if receipt != nil {
			for _, l := range receipt.Items {
				if l.ProductID != 0 {
					productIDs = append(productIDs, l.ProductID)
				}
			}
		}
	}
	stockLeft := map[int64]int64{}
	if len(productIDs) > 0 {
		rows, err := q.ListProductQuantities(ctx, store.ListProductQuantitiesParams{ClubID: clubID, Ids: productIDs})
		if err != nil {
			return cash.Report{}, nil, fmt.Errorf("cash: report stock: %w", err)
		}
		for _, row := range rows {
			stockLeft[row.ID] = row.Quantity
		}
	}
	goods := cash.BuildReport(cash.ReportInput{Operations: ops, Stock: stockLeft})
	r.Items, r.ItemsTotal, r.Discount = goods.Items, goods.ItemsTotal, goods.Discount

	staff, err := q.CashShiftReportStaff(ctx, store.CashShiftReportStaffParams{ClubID: clubID, CashShiftID: shift.ID})
	if err != nil {
		return cash.Report{}, nil, fmt.Errorf("cash: report staff: %w", err)
	}
	ids := []int64{deref(shift.OpenedBy), deref(shift.ClosedBy)}
	for _, st := range staff {
		ids = append(ids, st.UserID)
	}
	names, err := userNames(ctx, q, clubID, ids...)
	if err != nil {
		return cash.Report{}, nil, err
	}
	r.Staff = make([]cash.StaffRow, 0, len(staff))
	for _, st := range staff {
		name := cash.NoName // $names[$row->user_id] ?? '—'
		if n, ok := names[st.UserID]; ok && st.UserID != 0 {
			name = n
		}
		r.Staff = append(r.Staff, cash.StaffRow{
			UserID: st.UserID, Name: name, Count: st.Count, Income: money.Tiyn(st.Income), Expense: money.Tiyn(st.Expense),
		})
	}
	return r, names, nil
}
