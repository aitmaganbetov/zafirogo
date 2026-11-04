//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Гостевая сессия через billing-сервис покупок с настоящей кассой (CashRecorder = *service.Service):
// приход в смену, вид «Гостевые сессии» в X/Z, ledger guest_session (GS-091, GS-092).

func (e *env) zone(name string, pricePerHour *int64) int64 {
	e.t.Helper()
	return e.count("INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, $2, $3, now(), now()) RETURNING id",
		e.club, name, pricePerHour)
}

func (e *env) computer(zone, number int64, now time.Time) int64 {
	e.t.Helper()
	return e.count(`INSERT INTO club_computers (club_id, club_zone_id, number, last_seen_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), now()) RETURNING id`, e.club, zone, number, now)
}

func (e *env) hourTariff(name string, price money.Tiyn) int64 {
	e.t.Helper()
	return e.count(`INSERT INTO tariffs (club_id, name, duration_minutes, price, payment_method, is_active, created_at, updated_at)
		VALUES ($1, $2, 60, $3, 'main_or_bonus', true, now(), now()) RETURNING id`, e.club, name, int64(price))
}

func TestSellGuestSessionWithCash(t *testing.T) {
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	newBilling := func(e *env) *billingsvc.Service {
		_, err := fixDB.Owner.Exec(context.Background(), "UPDATE clubs SET timezone = 'UTC' WHERE id = $1", e.club)
		e.must(err)
		return billingsvc.New(fixDB.App, billingsvc.ClockFunc(func() time.Time { return now }), svc)
	}
	sell := func(b *billingsvc.Service, e *env, cashier int64, pcs []int64, tariff *int64, minutes int64, method string) (billingsvc.GuestSessionResult, error) {
		return b.SellGuestSession(context.Background(), billingsvc.GuestSessionRequest{
			ClubID: e.club, ComputerIDs: pcs, TariffID: tariff, Minutes: minutes, PaymentMethod: method,
			CashierID: cashier, IdempotencyKey: seq("guest"), Actor: actor(cashier),
		})
	}
	wantField := func(t *testing.T, err error, field, msg string) {
		t.Helper()
		switch e := err.(type) { //nolint:errorlint // ошибки сервиса и кассы приходят без обёртки
		case *billingsvc.Error:
			if e.Field == field && e.Message == msg {
				return
			}
		case *cash.Error:
			if e.Field == field && e.Message == msg {
				return
			}
		case *pricing.Error:
			if e.Field == field && e.Message == msg {
				return
			}
		}
		t.Fatalf("err = %v (%T), want %s: %s", err, err, field, msg)
	}

	t.Run("GS-091", func(t *testing.T) {
		e := newEnv(t)
		b := newBilling(e)
		cashier := e.user("Кассир")
		hall := e.zone("Hall", nil)
		pc1, pc2 := e.computer(hall, 1, now), e.computer(hall, 2, now)
		hour := e.hourTariff("Час", 70000)
		_, err := sell(b, e, cashier, []int64{pc1}, &hour, 0, "cash")
		wantField(t, err, "payment_method", service.MsgGuestOpenShiftFirst)

		sh := e.open(cashier, "0")
		r, err := sell(b, e, cashier, []int64{pc1, pc2}, &hour, 0, "card")
		e.must(err)
		eq(t, "amount", r.Amount.Tenge(), "1400.00")
		eq(t, "ledger op", r.LedgerTransactionID != 0, true)
		tx := e.allOps(sh.ID)
		eq(t, "operations", len(tx), 1)
		eq(t, "cash tx", tx[0].ID, r.CashTransactionID)
		eq(t, "kind", tx[0].Kind, cash.KindGuestSession)
		eq(t, "payment method", tx[0].PaymentMethod, cash.PayCard)
		eq(t, "description", tx[0].Description, r.Description)
		eq(t, "cashier", tx[0].UserID, cashier)
		eq(t, "revenue_time", e.account(ledger.AccountRevenueTime), 140000)
		e.checkLedger(140000)

		// Продление гостевой сессии — второй приход.
		_, err = sell(b, e, cashier, []int64{pc1}, &hour, 0, "cash")
		e.must(err)
		x := e.report(cash.ReportX, 0, "Кассир")
		hasLine(t, x, "Гостевые сессии · 2 · 2 100,00 KZT", "Кассир · 2 · 2 100,00 KZT · 0,00 KZT")
		// Гостевую сессию вернуть нельзя.
		_, err = e.refund(cashier, r.CashTransactionID)
		wantCashErr(t, err, "refund", cash.MsgNotRefundable)
		e.checkLedger(210000)
	})

	t.Run("GS-092", func(t *testing.T) {
		e := newEnv(t)
		b := newBilling(e)
		cashier := e.user("Кассир")
		e.open(cashier, "0")
		vipPrice, plain := int64(60000), e.zone("Plain", nil)
		vip := e.zone("VIP", &vipPrice)
		pc1, pc2 := e.computer(vip, 1, now), e.computer(plain, 2, now)
		r, err := sell(b, e, cashier, []int64{pc1}, nil, 30, "qr")
		e.must(err)
		eq(t, "amount", r.Amount.Tenge(), "300.00")
		_, err = sell(b, e, cashier, []int64{pc2}, nil, 30, "cash")
		wantField(t, err, "minutes", "В зоне ПК :computer время поминутно не продаётся.")
		eq(t, "cash transactions", e.count("SELECT count(*) FROM cash_transactions WHERE club_id = $1", e.club), 1)
		e.checkLedger(30000)
	})
}
