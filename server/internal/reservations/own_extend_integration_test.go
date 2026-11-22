//go:build integration

package reservations_test

// Ревью 22.11.2026: оплаченное время брони, добавляемое к своей идущей сессии гостя, не заходит на чужую бронь
// этого ПК (тот же предел, что у продажи времени: конец сессии + интервал ≤ начало чужой брони). Не поместившееся
// время не теряется: по часам и тарифом без лимита использования — деньги назад, тарифом с лимитом — пакет.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
)

const gap30Settings = `{"club":{"booking_interval":30,"app_booking_enabled":true}}`

func (w *world) buyMinutes(user, pc, minutes int64) {
	w.t.Helper()
	if _, err := w.svc.PurchaseMinutes(context.Background(), service.MinutesPurchaseRequest{
		ClubID: w.club, UserID: user, ComputerID: pc, Minutes: minutes, IdempotencyKey: fmt.Sprintf("m-%d", seq()),
		Actor: service.Actor{UserID: user, Kind: "guest"},
	}); err != nil {
		w.t.Fatalf("покупка минут: %v", err)
	}
}

func TestAppBooking_OwnSessionDoesNotRunIntoNextBooking(t *testing.T) {
	// G: бронь 18:00–19:00, H: 19:30–20:30 на том же ПК, интервал 30 минут.
	setup := func(t *testing.T, bought int64, mk func(w *world) []bookOpt) (w *world, pc, g, h, booking int64) {
		w = newWorld(t, at(15, 17, 0), gap30Settings, "UTC")
		_, pcs := w.vip(1)
		g, h = w.guest(5000*tenge, 0), w.guest(5000*tenge, 0)
		var opts []bookOpt
		if mk != nil {
			opts = mk(w)
		}
		booking = w.mustBook(g, pcs, at(15, 18, 0), 60, opts...).ReservationIDs[0]
		w.mustBook(h, pcs, at(15, 19, 30), 60)
		w.buyMinutes(g, pcs[0], bought)
		w.now = at(15, 17, 30) // интервал до начала брони G подошёл
		return w, pcs[0], g, h, booking
	}

	t.Run("время брони целиком не помещается: сессия не продлевается, деньги возвращаются", func(t *testing.T) {
		w, pc, g, _, booking := setup(t, 120, nil) // сессия до 19:00
		w.wantBalances(g, 3200*tenge, 0)           // 5000 − 600 (бронь) − 1200 (2 часа)
		out, ok := w.signIn(pc, g)
		if !ok {
			t.Fatal("бронь не закрыта")
		}
		eq(t, "начато минут", out.Minutes, 0)
		eq(t, "не поместилось минут", out.CutMinutes, 60)
		state := w.pc(pc)
		if state.EndsAt == nil || !state.EndsAt.Equal(at(15, 19, 0)) {
			t.Fatalf("конец сессии = %v, ожидалось 19:00 (чужая бронь с 19:30, интервал 30)", state.EndsAt)
		}
		if w.reservation(booking).ArrivedAt == nil {
			t.Fatal("бронь не отмечена начатой")
		}
		if parts := w.parts(*state.SessionID); len(parts) != 1 {
			t.Fatalf("части сессии = %+v, ожидалась только покупка минут", parts)
		}
		w.wantBalances(g, 3800*tenge, 0)
		rows := w.ledgerOf(booking)
		last := rows[len(rows)-2:]
		if last[0].Kind != "reservation_refund" || last[0].Amount+last[1].Amount != 0 ||
			max(last[0].Amount, last[1].Amount) != int64(600*tenge) {
			t.Fatalf("проводки возврата = %+v", last)
		}
		eq(t, "журнал возврата", w.activityCount("reservation.refunded"), 1)
		w.noDrift()
		// Повторный вход ничего не меняет.
		if _, again := w.signIn(pc, g); again {
			t.Fatal("бронь начата второй раз")
		}
		w.wantBalances(g, 3800*tenge, 0)
	})

	t.Run("помещается часть: она начинается, остальное возвращается пропорционально", func(t *testing.T) {
		w, pc, g, _, _ := setup(t, 90, nil) // сессия до 18:30; +60 = 19:30 > 19:00
		out, ok := w.signIn(pc, g)
		if !ok {
			t.Fatal("бронь не начата")
		}
		eq(t, "начато минут", out.Minutes, 30)
		eq(t, "не поместилось минут", out.CutMinutes, 30)
		state := w.pc(pc)
		if state.EndsAt == nil || !state.EndsAt.Equal(at(15, 19, 0)) {
			t.Fatalf("конец сессии = %v, ожидалось 19:00", state.EndsAt)
		}
		parts := w.parts(*state.SessionID)
		if len(parts) != 2 || parts[1].Minutes != 30 || parts[1].Main != int64(300*tenge) {
			t.Fatalf("части сессии = %+v, ожидалась часть брони 30 минут / 300 ₸", parts)
		}
		w.wantBalances(g, 5000*tenge-600*tenge-900*tenge+300*tenge, 0)
		w.noDrift()
	})

	t.Run("бронь тарифом с лимитом использования: остаток — пакетом", func(t *testing.T) {
		var tariff int64
		w, pc, g, _, booking := setup(t, 90, func(w *world) []bookOpt {
			tariff = w.tariff(tariffSpec{name: "Час", minutes: 60, price: 800 * tenge, usageLimit: p64(600)})
			return []bookOpt{withTariff(tariff)}
		})
		w.wantBalances(g, 5000*tenge-800*tenge-900*tenge, 0)
		out, ok := w.signIn(pc, g)
		if !ok || out.Minutes != 30 || out.CutMinutes != 30 || out.KeptPackageID == 0 {
			t.Fatalf("запуск = %+v, %v; ожидалось 30 минут и пакет на 30", out, ok)
		}
		state := w.pc(pc)
		if state.EndsAt == nil || !state.EndsAt.Equal(at(15, 19, 0)) {
			t.Fatalf("конец сессии = %v, ожидалось 19:00", state.EndsAt)
		}
		parts := w.parts(*state.SessionID)
		if len(parts) != 2 || parts[1].Kind != "tariff" || parts[1].Minutes != 30 {
			t.Fatalf("части сессии = %+v", parts)
		}
		var left, pkgTariff int64
		var expires time.Time
		if err := fixDB.Owner.QueryRow(context.Background(),
			"SELECT minutes_left, tariff_id, expires_at FROM time_packages WHERE club_id = $1 AND id = $2", w.club, out.KeptPackageID).
			Scan(&left, &pkgTariff, &expires); err != nil {
			t.Fatal(err)
		}
		if left != 30 || pkgTariff != tariff || !expires.Equal(at(15, 17, 30).Add(600*time.Minute)) {
			t.Fatalf("пакет = %d мин, тариф %d, до %v", left, pkgTariff, expires)
		}
		eq(t, "операция package_kept", scalar[int64](t,
			"SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'package_kept' AND reservation_id = $2", w.club, booking), 1)
		w.wantBalances(g, 5000*tenge-800*tenge-900*tenge, 0) // деньги не возвращаются — время осталось пакетом
		w.noDrift()
	})

	t.Run("бронь тарифом без лимита использования: остаток — деньгами", func(t *testing.T) {
		w, pc, g, _, _ := setup(t, 90, func(w *world) []bookOpt {
			return []bookOpt{withTariff(w.tariff(tariffSpec{name: "Час", minutes: 60, price: 800 * tenge}))}
		})
		out, ok := w.signIn(pc, g)
		if !ok || out.Minutes != 30 || out.CutMinutes != 30 || out.KeptPackageID != 0 {
			t.Fatalf("запуск = %+v, %v", out, ok)
		}
		parts := w.parts(*w.pc(pc).SessionID)
		if len(parts) != 2 || parts[1].Kind != "tariff" || parts[1].Minutes != 30 || parts[1].Main != int64(400*tenge) {
			t.Fatalf("части сессии = %+v", parts)
		}
		w.wantBalances(g, 5000*tenge-800*tenge-900*tenge+400*tenge, 0)
		w.noDrift()
	})

	t.Run("чужой брони нет: время брони добавляется целиком", func(t *testing.T) {
		w := newWorld(t, at(15, 17, 0), gap30Settings, "UTC")
		_, pcs := w.vip(1)
		g := w.guest(5000*tenge, 0)
		w.mustBook(g, pcs, at(15, 18, 0), 60)
		w.buyMinutes(g, pcs[0], 120)
		w.now = at(15, 17, 30)
		out, ok := w.signIn(pcs[0], g)
		if !ok || out.Minutes != 60 || out.CutMinutes != 0 || !out.EndsAt.Equal(at(15, 20, 0)) {
			t.Fatalf("запуск = %+v, %v; ожидалось 60 минут до 20:00", out, ok)
		}
		w.wantBalances(g, 3200*tenge, 0)
	})
}

// Ревью 22.11.2026: часть сессии от брони не участвует в выборе зон пересадки — как в Laravel, где запуск брони
// пишет reservation.arrived, а SeatTransfers::allowedZoneIds() смотрит последнюю tariff.purchased|time.purchased.
func TestAppBooking_BookingPartDoesNotNarrowTransferZones(t *testing.T) {
	transfer := func(w *world, user, from, to int64) error {
		_, err := w.svc.TransferSeat(context.Background(), service.TransferRequest{ClubID: w.club, UserID: user, FromComputerID: from, TargetComputerID: to})
		return err
	}

	t.Run("тариф двух зон, потом время брони: пересадка во вторую зону тарифа остаётся", func(t *testing.T) {
		w := newWorld(t, at(16, 10, 0), bookingSettings, "UTC")
		vip, hall := w.zone("VIP", p64(60000)), w.zone("Общий", p64(30000))
		pc1, pc2 := w.computer(vip, 1), w.computer(hall, 2)
		g := w.guest(5000*tenge, 0)
		both := w.tariff(tariffSpec{name: "Час", minutes: 60, price: 500 * tenge, zones: []int64{vip, hall}})
		w.mustBook(g, []int64{pc1}, at(16, 10, 30), 60)
		if _, err := w.svc.PurchaseTariff(context.Background(), service.TariffPurchaseRequest{
			ClubID: w.club, UserID: g, TariffID: both, ComputerID: pc1, IdempotencyKey: fmt.Sprintf("t-%d", seq()),
			Actor: service.Actor{UserID: g, Kind: "guest"},
		}); err != nil {
			t.Fatalf("покупка тарифа: %v", err)
		}
		w.now = at(16, 10, 25)
		if out, ok := w.signIn(pc1, g); !ok || !out.Extends {
			t.Fatalf("время брони не добавлено к сессии: %+v", out)
		}
		if err := transfer(w, g, pc1, pc2); err != nil {
			t.Fatalf("пересадка в зону тарифа: %v", err)
		}
	})

	t.Run("сессия только из времени брони: зоны не ограничены", func(t *testing.T) {
		w := newWorld(t, at(16, 10, 0), bookingSettings, "UTC")
		vip, hall := w.zone("VIP", p64(60000)), w.zone("Общий", p64(30000))
		pc1, pc2 := w.computer(vip, 1), w.computer(hall, 2)
		g := w.guest(5000*tenge, 0)
		w.mustBook(g, []int64{pc1}, at(16, 10, 5), 60)
		if _, ok := w.signIn(pc1, g); !ok {
			t.Fatal("бронь не начата")
		}
		eq(t, "источник части", scalar[string](t, "SELECT source FROM session_purchases WHERE club_id = $1", w.club), "booking")
		if err := transfer(w, g, pc1, pc2); err != nil {
			t.Fatalf("пересадка: %v", err)
		}
	})
}
