package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// SaleParams — продажа POS: строки товаров/услуг (StorePosSaleRequest) и ключ запроса кассы.
type SaleParams struct {
	ClubID         int64
	Items          []cash.SaleItem
	IdempotencyKey string
	Actor          Actor
}

// SaleResult — записанная продажа.
type SaleResult struct {
	Transaction store.CashTransaction
	Sale        cash.Sale             // сумма, строки чека, списания (при повторе — строки из чека, без списаний)
	Movements   []store.StockMovement // движения write_off (вебхуки stock.changed, журнал stock.written_off)
	Ledger      *ledger.Result        // nil — нулевая сумма
	Replayed    bool
}

// Sale — продажа товаров и услуг на кассе (BR-052).
//
// Laravel: app/Services/CashRegisterService.php sale() (~стр. 19–85): открытая смена FOR UPDATE (нет → «Сначала откройте
// смену.», поле items); по строкам по порядку: услуга (активная, findOrFail) или товар FOR UPDATE (findOrFail) с проверкой
// остатка «Недостаточно товара: :product.»; списание (quantity −, stock_movements write_off «Продажа POS»); приход смены
// {amount = Σ, description 'Продажа POS', receipt_data.items}. Покупатель и способ оплаты не пишутся (Q-013: NULL).
// Проводка pos_sale: cash_drawer −S, revenue_pos +S.
func (s *Service) Sale(ctx context.Context, q *store.Queries, p SaleParams) (SaleResult, error) {
	// StorePosSaleRequest (форма) — до сервиса.
	if err := cash.ValidateSaleItems(p.Items); err != nil {
		return SaleResult{}, err
	}
	shiftRow, err := LockOpenShift(ctx, q, p.ClubID)
	if err != nil {
		return SaleResult{}, err
	}
	if prev, err := findByKey(ctx, q, p.ClubID, p.IdempotencyKey); err != nil || prev != nil {
		if err != nil {
			return SaleResult{}, err
		}
		return replaySale(*prev, p.Items)
	}
	shift := ShiftOf(shiftRow)
	if err := cash.RequireOpen(shift, "items"); err != nil {
		return SaleResult{}, err
	}

	// Блокировки в порядке строк запроса, как цикл sale(); услуги — без блокировки.
	products := map[int64]cash.Product{}
	var serviceIDs []int64
	for _, it := range p.Items {
		if it.ServiceID != 0 {
			serviceIDs = append(serviceIDs, it.ServiceID)
			continue
		}
		pr, err := stock.LockProduct(ctx, q, p.ClubID, it.ProductID)
		if errors.Is(err, stock.ErrProductNotFound) {
			continue // SellPOS: ошибка поля items.N.product_id (Rule::exists формы)
		}
		if err != nil {
			return SaleResult{}, err
		}
		products[pr.ID] = cash.Product{ID: pr.ID, SKU: pr.Sku, Name: pr.Name, Price: tiyn(pr.Price), Quantity: pr.Quantity}
	}
	services := map[int64]cash.Service{}
	if len(serviceIDs) > 0 {
		rows, err := q.ListActiveClubServicesByIDs(ctx, store.ListActiveClubServicesByIDsParams{ClubID: p.ClubID, Ids: serviceIDs})
		if err != nil {
			return SaleResult{}, fmt.Errorf("cash: services: %w", err)
		}
		for _, r := range rows {
			services[r.ID] = cash.Service{ID: r.ID, Name: r.Name, Price: tiyn(r.Price)}
		}
	}
	sale, err := cash.SellPOS(shift, p.Items, products, services)
	if err != nil {
		return SaleResult{}, err
	}

	row, err := s.insertTransaction(ctx, q, p.ClubID, newTransaction{
		ShiftID: shift.ID, UserID: p.Actor.UserID, Type: cash.Income, Amount: sale.Amount,
		Description: sale.Description(), Receipt: &cash.Receipt{Items: sale.Lines}, Key: p.IdempotencyKey,
	})
	if err != nil {
		return SaleResult{}, err
	}
	out := SaleResult{Transaction: row, Sale: sale}
	now := s.now()
	for _, w := range sale.WriteOffs {
		m := stock.Movement{
			ProductID: w.ProductID, Type: stock.WriteOff, Quantity: w.Quantity, BalanceAfter: w.BalanceAfter,
			Note: cash.SaleNote, Change: -w.Quantity,
		}
		mv, err := stock.ApplyMovement(ctx, q, p.ClubID, p.Actor.UserID, m, now)
		if err != nil {
			return SaleResult{}, err
		}
		out.Movements = append(out.Movements, mv)
	}
	if out.Ledger, err = postDrawer(ctx, q, p.ClubID, ledger.KindPOSSale, ledgerKey(p.IdempotencyKey, row.ID), sale.Amount,
		ledger.AccountRevenuePOS, ledger.Refs{CashShiftID: shift.ID, CashTransactionID: row.ID}, nil, p.Actor); err != nil {
		return SaleResult{}, err
	}
	return out, audit(ctx, q, p.ClubID, p.Actor, "cash.pos_sale", "cash_transaction", row.ID, out.Ledger,
		map[string]any{"amount": int64(sale.Amount), "item_count": len(sale.Lines)})
}

// replaySale — повтор продажи с тем же ключом: та же операция, если строки запроса совпадают с чеком.
func replaySale(prev store.CashTransaction, items []cash.SaleItem) (SaleResult, error) {
	op, err := OperationOf(prev)
	if err != nil {
		return SaleResult{}, err
	}
	if !op.IsPOSSale() || op.Receipt == nil || len(op.Receipt.Items) != len(items) {
		return SaleResult{}, ErrIdempotencyConflict
	}
	for i, l := range op.Receipt.Items {
		it := items[i]
		if l.Quantity != it.Quantity {
			return SaleResult{}, ErrIdempotencyConflict
		}
		if it.ServiceID != 0 {
			if l.ServiceID != it.ServiceID {
				return SaleResult{}, ErrIdempotencyConflict
			}
		} else if l.ProductID != it.ProductID || l.ServiceID != 0 {
			return SaleResult{}, ErrIdempotencyConflict
		}
	}
	return SaleResult{
		Transaction: prev, Replayed: true,
		Sale: cash.Sale{Amount: op.Amount, Lines: op.Receipt.Items},
	}, nil
}
