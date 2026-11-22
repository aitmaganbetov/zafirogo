//go:build integration

package reservations_test

// Ревью 22.11.2026: ПК и зону нельзя удалить, пока на ПК есть оплаченная ожидающая бронь, которая ещё не закончилась:
// внешний ключ computer_reservations → club_computers удаляет брони каскадом, и деньги гостя пропали бы без возврата.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

func TestPaidBookingBlocksComputerAndZoneDelete(t *testing.T) {
	w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
	zone, pcs := w.vip(2)
	admin := w.staff()
	g := w.guest(2000*tenge, 0)
	booking := w.mustBook(g, pcs[:1], at(15, 12, 0), 60).ReservationIDs[0]
	env := func() clubmap.Env { return clubmap.Env{ClubID: w.club, UserID: admin, Now: w.now} }
	deleteComputer := func(id int64) error {
		return w.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := clubmap.DeleteComputer(ctx, q, env(), id)
			return err
		})
	}
	deleteZone := func() error {
		return w.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := clubmap.DeleteZone(ctx, q, env(), zone)
			return err
		})
	}
	blocked := func(err error, field, message string) {
		t.Helper()
		var e *clubmap.Error
		if !errors.As(err, &e) || e.Status != http.StatusUnprocessableEntity || e.Field != field || e.Message != message || e.Params["computer"] != "PC-01" {
			t.Fatalf("ошибка = %#v, ожидался отказ 422 поля %s: %s", err, field, message)
		}
		if w.reservation(booking).CancelledAt != nil {
			t.Fatal("бронь изменена")
		}
		w.wantBalances(g, 1400*tenge, 0)
	}

	blocked(deleteComputer(pcs[0]), "computer", clubmap.MsgComputerHasPaidBooking)
	blocked(deleteZone(), "zone", clubmap.MsgZoneHasPaidBooking)
	// ПК без оплаченной брони удаляется; бронь с кассы (неоплаченная) удалению не мешает.
	if err := w.tx(func(ctx context.Context, q *store.Queries) error {
		_, err := reservations.Create(ctx, q, w.info(ctx, q), admin, reservations.Booking{
			ComputerID: pcs[1], UserID: g, CustomerName: "Гость", StartsAt: at(15, 14, 0), EndsAt: at(15, 15, 0),
		}, w.now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := deleteComputer(pcs[1]); err != nil {
		t.Fatalf("удаление ПК с неоплаченной бронью: %v", err)
	}
	// Отмена возвращает деньги — после неё зона (и ПК) удаляются.
	if err := w.staffCancel(admin, booking); err != nil {
		t.Fatal(err)
	}
	w.wantBalances(g, 2000*tenge, 0)
	if err := deleteZone(); err != nil {
		t.Fatalf("удаление зоны после отмены брони: %v", err)
	}
	w.noDrift()

	t.Run("бронь закончилась (неявка) — удалению не мешает", func(t *testing.T) {
		w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
		_, pcs := w.vip(1)
		admin, g := w.staff(), w.guest(2000*tenge, 0)
		w.mustBook(g, pcs, at(15, 12, 0), 60)
		w.now = at(15, 13, 0)
		if err := w.tx(func(ctx context.Context, q *store.Queries) error {
			_, err := clubmap.DeleteComputer(ctx, q, clubmap.Env{ClubID: w.club, UserID: admin, Now: w.now}, pcs[0])
			return err
		}); err != nil {
			t.Fatalf("удаление ПК после конца брони: %v", err)
		}
	})
}

// Ревью 22.11.2026: отмена группы гостем блокирует все её ПК по возрастанию id до работы с бронями — тот же порядок,
// что у Book (иначе у группы, где id броней идут не в порядке id ПК, — например перенесённой из Laravel, — отмена и
// встречная бронь тех же ПК блокируют друг друга накрест).
func TestCancelByGuestLocksComputersInIDOrder(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, at(15, 10, 0), bookingSettings, "UTC")
	_, pcs := w.vip(2)
	g := w.guest(5000*tenge, 0)
	out := w.mustBook(g, pcs, at(15, 12, 0), 60)
	// Как у перенесённой группы: первая (по id) бронь — на ПК с большим id.
	exec(t, "UPDATE computer_reservations SET club_computer_id = $2, starts_at = starts_at + interval '1 day', ends_at = ends_at + interval '1 day' WHERE id = $1", out.ReservationIDs[0], pcs[1])
	exec(t, "UPDATE computer_reservations SET club_computer_id = $2 WHERE id = $1", out.ReservationIDs[1], pcs[0])
	exec(t, "UPDATE computer_reservations SET starts_at = starts_at - interval '1 day', ends_at = ends_at - interval '1 day' WHERE id = $1", out.ReservationIDs[0])

	other, err := fixDB.Owner.Begin(ctx) // встречная операция: держит ПК с меньшим id
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback(ctx) // после Commit — no-op
	if _, err := other.Exec(ctx, "SELECT id FROM club_computers WHERE id = $1 FOR UPDATE", pcs[0]); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- w.guestCancel(g, out.ReservationIDs[0]) }()
	// Ждём, пока отмена встанет на блокировке ПК.
	deadline := time.Now().Add(10 * time.Second)
	for scalar[int64](t, "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("отмена не дошла до блокировки ПК")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Отмена ждёт ПК с меньшим id и ещё не держит ПК с большим: встречная операция берёт его без ожидания.
	if _, err := other.Exec(ctx, "SELECT id FROM club_computers WHERE id = $1 FOR UPDATE NOWAIT", pcs[1]); err != nil {
		t.Fatalf("отмена заблокировала ПК с большим id раньше меньшего: %v", err)
	}
	if err := other.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("отмена: %v", err)
	}
	w.wantBalances(g, 5000*tenge, 0)
}
