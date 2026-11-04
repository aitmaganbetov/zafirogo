//go:build integration

package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
)

func (e *env) sellGuest(pcs []int64, tariff *int64, minutes int64, method string) (service.GuestSessionResult, error) {
	return e.svc.SellGuestSession(context.Background(), service.GuestSessionRequest{
		ClubID: e.club, ComputerIDs: pcs, TariffID: tariff, Minutes: minutes, PaymentMethod: method,
		CashierID: 1, IdempotencyKey: key("guest"),
	})
}

// Гостевая сессия с кассы: смена, цена, ПК, приход (через CashRecorder) и ledger guest_session.
func TestGuestSession(t *testing.T) {
	t.Run("GS-091", func(t *testing.T) {
		// 10:00 UTC; Hall, ПК №1 и №2; «Час» 60 мин, 700 ₸.
		e := newEnv(t, "")
		hall := e.zone("Hall", nil)
		pc1, pc2 := e.computer(hall, 1), e.computer(hall, 2)
		hour := e.tariff(tariffSpec{name: "Час", minutes: 60, price: 700 * tenge})
		_, err := e.sellGuest([]int64{pc1}, &hour, 0, "cash")
		wantFieldError(t, err, "payment_method", "Сначала откройте смену в кассе.")

		e.cash.shiftID = 77
		r := must[service.GuestSessionResult](t)(e.sellGuest([]int64{pc1, pc2}, &hour, 0, "card"))
		eq(t, "amount", r.Amount.Tenge(), "1400.00")
		eq(t, "minutes", r.Minutes, 60)
		eq(t, "description", r.Description, "Гостевая сессия: тариф «Час», ПК PC-01, PC-02 (карта)")
		for _, pc := range []int64{pc1, pc2} {
			st := e.pc(pc)
			eq(t, "guest", st.Guest, true)
			eq(t, "no user", st.UserID == nil, true)
			eq(t, "ends", st.EndsAt.UTC(), t0.Add(time.Hour))
		}
		eq(t, "cash records", len(e.cash.records), 1)
		eq(t, "cash amount", e.cash.records[0].Amount, 1400*tenge)
		eq(t, "shift", e.cash.records[0].ShiftID, 77)
		eq(t, "cash_drawer", scalar[int64](t, `SELECT sum(e.amount)::bigint FROM ledger_entries e JOIN ledger_accounts a ON a.id = e.account_id
			WHERE e.club_id = $1 AND a.kind = 'cash_drawer'`, e.club), -140000)
		// Лимит количества тарифа кассой не расходуется (Q-007).
		eq(t, "tariff_sales", scalar[int64](t, "SELECT count(*) FROM tariff_sales WHERE club_id = $1", e.club), 0)

		first := *e.pc(pc1).SessionID
		r = must[service.GuestSessionResult](t)(e.sellGuest([]int64{pc1}, &hour, 0, "cash"))
		eq(t, "extends", r.Computers[0].Extends, true)
		eq(t, "same id", *e.pc(pc1).SessionID, first)
		eq(t, "ends extended", e.pc(pc1).EndsAt.UTC(), t0.Add(2*time.Hour))
	})

	t.Run("GS-092", func(t *testing.T) {
		// VIP 600 ₸/час: ПК1 свободен, ПК3 занят клиентом до 11:00, ПК4 бронь 10:20–12:00; Plain без цены — ПК2.
		e := newEnv(t, "")
		e.cash.shiftID = 1
		vip, plain := e.zone("VIP", ptr(int64(60000))), e.zone("Plain", nil)
		pc1, pc2, pc3, pc4 := e.computer(vip, 1), e.computer(plain, 2), e.computer(vip, 3), e.computer(vip, 4)
		u := e.customer("Клиент", 1000*tenge, 0)
		must[service.PurchaseResult](t)(e.buyMinutes(u, pc3, 60, pricing.ChoiceMain))
		exec(t, `INSERT INTO computer_reservations (club_id, club_computer_id, starts_at, ends_at, created_at, updated_at)
			VALUES ($1, $2, '2026-10-15 10:20:00+00', '2026-10-15 12:00:00+00', now(), now())`, e.club, pc4)

		r := must[service.GuestSessionResult](t)(e.sellGuest([]int64{pc1}, nil, 30, "qr"))
		eq(t, "amount", r.Amount.Tenge(), "300.00")
		_, err := e.sellGuest([]int64{pc2}, nil, 30, "cash")
		wantFieldError(t, err, "minutes", "В зоне ПК :computer время поминутно не продаётся.")
		_, err = e.sellGuest([]int64{pc3}, nil, 30, "cash")
		wantFieldError(t, err, "computer_ids", "ПК :computer занят или забронирован.")
		_, err = e.sellGuest([]int64{pc4}, nil, 30, "cash")
		wantFieldError(t, err, "computer_ids", "ПК :computer забронирован с :time.")
		eq(t, "cash records", len(e.cash.records), 1)
	})

	t.Run("GAP-030", func(t *testing.T) {
		e := newEnv(t, "")
		e.cash.shiftID = 1
		pc1 := e.computer(e.zone("VIP", ptr(int64(60000))), 1)
		pc2 := e.computer(e.zone("Hall", ptr(int64(45000))), 2)
		r := must[service.GuestSessionResult](t)(e.sellGuest([]int64{pc2, pc1, pc1}, nil, 90, "card"))
		eq(t, "amount", r.Amount.Tenge(), "1575.00")
		eq(t, "minutes", r.Description, "Гостевая сессия: 90 минут, ПК PC-01, PC-02 (карта)")
	})

	t.Run("GAP-031", func(t *testing.T) {
		e := newEnv(t, "")
		e.cash.shiftID = 1
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		u := e.customer("Клиент", 1000*tenge, 0)
		must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 60, pricing.ChoiceMain))
		_, err := e.sellGuest([]int64{pc}, nil, 30, "cash")
		var ve *pricing.Error
		if !errors.As(err, &ve) || ve.Params["computer"] != "PC-01" {
			t.Fatalf("err = %v", err)
		}
		eq(t, "cash records", len(e.cash.records), 0)
	})

	t.Run("GAP-031/завершение гостевой", func(t *testing.T) {
		// Завершение гостевой сессии сотрудником денег не возвращает.
		e := newEnv(t, "")
		e.cash.shiftID = 1
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		r := must[service.GuestSessionResult](t)(e.sellGuest([]int64{pc}, nil, 60, "cash"))
		e.clock.travel(10 * time.Minute)
		res := must[service.EndSessionResult](t)(e.end(pc, r.Computers[0].SessionID, 1, true))
		eq(t, "refund", res.RefundMain+res.RefundBonus, 0)
		eq(t, "customer", res.CustomerID, 0)
	})

	t.Run("GS-066", func(t *testing.T) {
		// Среда 23:00; «День» действует ср 10:00–17:00 → «Тариф недоступен.»
		e := newEnv(t, "")
		e.cash.shiftID = 1
		e.clock.set(time.Date(2026, 10, 28, 23, 0, 0, 0, time.UTC))
		pc := e.computer(e.zone("Hall", nil), 1)
		day := e.tariff(tariffSpec{name: "День", minutes: 60, price: 100 * tenge,
			validity: `[{"start_day": "wed", "start_time": "10:00", "end_day": "wed", "end_time": "17:00"}]`})
		_, err := e.sellGuest([]int64{pc}, &day, 0, "cash")
		wantFieldError(t, err, "tariff_id", "Тариф недоступен.")
		// Тариф проверяется раньше ПК: неизвестный ПК — та же ошибка тарифа.
		_, err = e.sellGuest([]int64{pc, 999999}, &day, 0, "cash")
		wantFieldError(t, err, "tariff_id", "Тариф недоступен.")
	})

	t.Run("GS-094", func(t *testing.T) {
		e := newEnv(t, "")
		e.cash.shiftID = 1
		hall, vip := e.zone("Hall", nil), e.zone("VIP", nil)
		hallOnly := e.tariff(tariffSpec{name: "Зальный", minutes: 60, price: 700 * tenge, zones: []int64{hall}})
		_, err := e.sellGuest([]int64{e.computer(vip, 1)}, &hallOnly, 0, "cash")
		wantFieldError(t, err, "tariff_id", "Тариф не действует в зоне ПК :computer.")
	})
}

// Конкурентность и идемпотентность.
func TestConcurrency(t *testing.T) {
	t.Run("две покупки с одного баланса", func(t *testing.T) {
		e := newEnv(t, "")
		hall := e.zone("Hall", nil)
		pc1, pc2 := e.computer(hall, 1), e.computer(hall, 2)
		tr := e.tariff(tariffSpec{name: "Час", minutes: 60, price: 600 * tenge})
		u := e.customer("Клиент", 1000*tenge, 0)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, pc := range []int64{pc1, pc2} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = e.buyTariff(u, tr, pc, pricing.ChoiceMain)
			}()
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, pricing.ErrInsufficientFunds):
			default:
				t.Fatalf("unexpected err: %v", err)
			}
		}
		eq(t, "succeeded", ok, 1)
		e.wantBalances(u, 400*tenge, 0)
		e.wantTotalSpent(u, 600*tenge)
		eq(t, "parts", e.count("session_purchases"), 1)
		e.noDrift()
	})

	t.Run("повтор с тем же ключом", func(t *testing.T) {
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		u := e.customer("Клиент", 1000*tenge, 0)
		req := service.MinutesPurchaseRequest{ClubID: e.club, UserID: u, ComputerID: pc, Minutes: 30, Balance: pricing.ChoiceMain, IdempotencyKey: key("same")}
		var wg sync.WaitGroup
		results := make([]service.PurchaseResult, 4)
		errs := make([]error, 4)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = e.svc.PurchaseMinutes(context.Background(), req)
			}()
		}
		wg.Wait()
		replays := 0
		for i := range results {
			if errs[i] != nil {
				t.Fatalf("err: %v", errs[i])
			}
			if results[i].Replayed {
				replays++
			}
			eq(t, "same session", results[i].SessionID, results[0].SessionID)
			eq(t, "same balance", results[i].NewBalance, 700*tenge)
		}
		eq(t, "replays", replays, 3)
		e.wantBalances(u, 700*tenge, 0)
		eq(t, "parts", e.count("session_purchases"), 1)

		// После завершения сессии повтор отдаёт прежний ответ, а не покупает снова.
		must[service.EndSessionResult](t)(e.end(pc, results[0].SessionID, u, false))
		again := must[service.PurchaseResult](t)(e.svc.PurchaseMinutes(context.Background(), req))
		eq(t, "replayed", again.Replayed, true)
		e.wantBalances(u, 1000*tenge, 0)

		// Тот же ключ с другим телом — конфликт.
		req.Minutes = 31
		_, err := e.svc.PurchaseMinutes(context.Background(), req)
		if !errors.Is(err, service.ErrIdempotencyConflict) {
			t.Fatalf("err = %v, want conflict", err)
		}
		// Без ключа — ошибка запроса.
		req.IdempotencyKey = ""
		_, err = e.svc.PurchaseMinutes(context.Background(), req)
		if !errors.Is(err, service.ErrInvalidInput) {
			t.Fatalf("err = %v, want invalid input", err)
		}
	})

	t.Run("отказ не занимает ключ", func(t *testing.T) {
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		u := e.customer("Клиент", 100*tenge, 0)
		req := service.MinutesPurchaseRequest{ClubID: e.club, UserID: u, ComputerID: pc, Minutes: 30, Balance: pricing.ChoiceMain, IdempotencyKey: key("retry")}
		_, err := e.svc.PurchaseMinutes(context.Background(), req)
		wantFieldError(t, err, "balance", "Недостаточно средств для покупки тарифа.")
		exec(t, "UPDATE club_zones SET price_per_hour = 10000 WHERE club_id = $1", e.club)
		r := must[service.PurchaseResult](t)(e.svc.PurchaseMinutes(context.Background(), req))
		eq(t, "replayed", r.Replayed, false)
		e.wantBalances(u, 50*tenge, 0)
	})
}
