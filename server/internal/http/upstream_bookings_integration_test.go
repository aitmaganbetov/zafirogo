//go:build integration

package httpserver

// Экран «Брони» панели и оплаченные брони из гостевого приложения (тема Г, docs/upstream-2026-11.md): признак «из
// приложения», оплата и код в списке; «Отменить» возвращает деньги гостю (GS-543), «Клиент пришёл» запускает
// оплаченное время на ПК (GS-545) и шлёт агенту сессию после коммита. Laravel: reservations/index.blade.php,
// ComputerReservations::close().

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

func TestUpstreamAppBookingsInPanel(t *testing.T) {
	now := time.Date(2026, 11, 15, 5, 0, 0, 0, time.UTC) // 10:00 Алматы
	e, _ := newG4Env(t, now)
	e.exec(`UPDATE clubs SET settings = '{"club": {"booking_interval": 10, "app_booking_enabled": true, "app_booking_cancel": false}}' WHERE id = $1`, e.club)
	_, admin := e.staff("Админ", "admin")
	_, pc1 := e.zoneWithPC("VIP", 60000, 1)
	_, pc2 := e.zoneWithPC("Зал", 30000, 2)
	guest, _ := e.member("Алия", "client", "", false)
	ctx := activity.WithRequestMeta(context.Background(), activity.RequestMeta{IP: "127.0.0.1"})
	env := reservations.Env{Now: now, Actor: billing.BalanceActor{UserID: guest, Kind: "guest"}}
	var first, second reservations.BookResult
	if err := db.InTenantTx(ctx, e.db.App, e.club, func(q *store.Queries) error {
		if _, err := billing.SetInitialBalance(ctx, q, billing.InitialBalanceParams{ClubID: e.club, UserID: guest, Main: 2000_00}); err != nil {
			return err
		}
		minutes := int64(60)
		var err error
		// 12:00 Алматы = 07:00 UTC.
		if first, err = reservations.Book(ctx, q, reservations.BookRequest{ClubID: e.club, UserID: guest, ComputerIDs: []int64{pc1},
			StartsAt: now.Add(2 * time.Hour), Minutes: &minutes}, env); err != nil {
			return err
		}
		second, err = reservations.Book(ctx, q, reservations.BookRequest{ClubID: e.club, UserID: guest, ComputerIDs: []int64{pc2},
			StartsAt: now.Add(2 * time.Hour), Minutes: &minutes}, env)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	balance := func() int64 { main, _ := e.db.Balances(t, e.club, guest); return main }
	if balance() != 1100_00 {
		t.Fatalf("баланс после двух броней = %d", balance())
	}

	// Список: признак «из приложения», оплата этого ПК в тиынах, код.
	l := e.get(admin, "/api/reservations?date=2026-11-15")
	items, _ := l.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("список: %v", l.body)
	}
	for i, want := range []struct {
		id, paid int64
		code     string
	}{{first.ReservationIDs[0], 600_00, first.Code}, {second.ReservationIDs[0], 300_00, second.Code}} {
		it := obj(items[i])
		if num(it["id"]) != want.id || it["from_app"] != true || num(it["paid"]) != want.paid || it["code"] != want.code {
			t.Fatalf("бронь %d в списке: %v", i, it)
		}
	}

	// GS-543: сотрудник отменяет оплаченную бронь — деньги возвращаются (хотя клуб запретил отмену гостям).
	expect(t, e.post(admin, fmt.Sprintf("/api/reservations/%d/cancel", second.ReservationIDs[0]), nil), 200, "cancel")
	if balance() != 1400_00 {
		t.Fatalf("баланс после отмены сотрудником = %d", balance())
	}
	if e.activityCount("reservation.cancelled", second.ReservationIDs[0]) != 1 || e.activityDetails("reservation.refunded")["main_balance_amount"] != "300.00" {
		t.Fatalf("журнал отмены: %v", e.activityDetails("reservation.refunded"))
	}
	if e.notifier.balanceCount(guest) != 1 {
		t.Fatalf("BalanceChanged агенту после возврата: %d", e.notifier.balanceCount(guest))
	}
	expectField(t, e.post(admin, fmt.Sprintf("/api/reservations/%d/cancel", second.ReservationIDs[0]), nil), "reservation", "Эта бронь уже закрыта.")
	if balance() != 1400_00 {
		t.Fatalf("повторная отмена изменила баланс: %d", balance())
	}

	// GS-545: «Клиент пришёл» по оплаченной брони запускает её время на ПК (UQ-16: в любой момент до конца брони).
	expect(t, e.post(admin, fmt.Sprintf("/api/reservations/%d/arrive", first.ReservationIDs[0]), nil), 200, "arrive")
	if e.scalar("SELECT count(*) FROM club_computers WHERE id = $1 AND current_session_user_id = $2 AND session_ends_at = $3", pc1, guest, now.Add(time.Hour)) != 1 {
		t.Fatal("сессия гостя на ПК не началась")
	}
	if e.scalar("SELECT count(*) FROM session_purchases WHERE club_id = $1 AND user_id = $2 AND kind = 'minutes' AND minutes = 60 AND main_cents = 60000", e.club, guest) != 1 {
		t.Fatal("часть сессии брони")
	}
	e.notifier.mu.Lock()
	started := append([]int64(nil), e.notifier.started...)
	e.notifier.mu.Unlock()
	if len(started) != 1 || started[0] != pc1 {
		t.Fatalf("StartSession агенту: %v", started)
	}
	if balance() != 1400_00 {
		t.Fatalf("запуск времени изменил баланс: %d", balance())
	}
	l = e.get(admin, "/api/reservations?date=2026-11-15")
	items, _ = l.body["items"].([]any)
	if obj(items[0])["state"] != "arrived" || obj(items[1])["state"] != "cancelled" { //nolint:misspell // состояние брони старого кода
		t.Fatalf("состояния: %v", items)
	}
}
