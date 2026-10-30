// Package reservations — брони ПК: создание с проверкой интервала между бронями и идущей сессии, отмена,
// «клиент пришёл». Laravel: app/Services/ComputerReservations.php create(), cancel(), arrive(), close();
// app/Models/ComputerReservation.php state(), customerLabel(). Блокировка покупок времени бронью — в billing
// (GetBlockingReservation, MarkReservationsArrived). Вызывающий даёт тенантную транзакцию (q).
package reservations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты ComputerReservations / контроллера (ключи перевода).
const (
	MsgConflict    = "ПК уже забронирован с :from до :to (между бронями нужно :count мин)."
	MsgSession     = "На ПК идёт сессия до :time."
	MsgClosed      = "Эта бронь уже закрыта."
	MsgPast        = "Нельзя забронировать ПК на прошедшее время."
	MsgPickClient  = "Выберите клиента из пользователей клуба."
	logCreated     = "ПК :computer забронирован на :from – :to"
	logCancelled   = "Бронь ПК :computer на :from отменена"
	logArrived     = "Клиент пришёл по брони ПК :computer на :from"
	subjectBooking = `App\Models\ComputerReservation`
)

// ErrNotFound — бронь или ПК не найдены в клубе (findOrFail).
var ErrNotFound = errors.New("reservations: not found")

// Error — ошибка формы (ValidationException::withMessages): поле, ключ перевода, подстановки.
type Error struct {
	Field   string
	Message string
	Params  map[string]string
}

func (e *Error) Error() string { return "reservations: " + e.Field + ": " + e.Message }

// Booking — новая бронь.
type Booking struct {
	ComputerID    int64
	UserID        int64
	CustomerName  string
	CustomerPhone *string
	Note          *string
	StartsAt      time.Time
	EndsAt        time.Time
}

// State — ComputerReservation::state() на момент now.
func State(startsAt, endsAt time.Time, arrivedAt, cancelledAt *time.Time, now time.Time) string {
	switch {
	case cancelledAt != nil:
		return "cancelled" //nolint:misspell // состояние брони старого кода (ComputerReservation::state())
	case arrivedAt != nil:
		return "arrived"
	case endsAt.Before(now):
		return "finished"
	case startsAt.Before(now):
		return "active"
	default:
		return "pending"
	}
}

// CustomerLabel — customerLabel(): «имя · телефон» (имя и телефон аккаунта, иначе сохранённые с бронью).
func CustomerLabel(userName, userPhone, customerName, customerPhone *string) string {
	pick := func(a, b *string) string {
		if a != nil {
			return *a
		}
		if b != nil {
			return *b
		}
		return ""
	}
	var parts []string
	for _, s := range []string{pick(userName, customerName), pick(userPhone, customerPhone)} {
		if s != "" && s != "0" { // array_filter
			parts = append(parts, s)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " · "))
}

// Create — ComputerReservations::create(): строка ПК блокируется; пересечение с ожидающей бронью ±интервал или
// идущая оплаченная сессия, которая заходит в интервал до начала, — ошибка поля starts_at. Журнал
// reservation.created, события reservation.changed и computer.reservation_changed (вебхук).
func Create(ctx context.Context, q *store.Queries, info club.Info, actorID int64, b Booking, now time.Time) (int64, error) {
	pc, err := q.LockReservationComputer(ctx, store.LockReservationComputerParams{ClubID: info.ID, ID: b.ComputerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	gap := time.Duration(info.BookingGapMinutes()) * time.Minute
	conflict, err := q.FirstOverlappingReservation(ctx, store.FirstOverlappingReservationParams{
		ClubID: info.ID, ComputerID: pc.ID, FromAt: b.StartsAt.Add(-gap), ToAt: b.EndsAt.Add(gap),
	})
	switch {
	case err == nil:
		return 0, &Error{Field: "starts_at", Message: MsgConflict, Params: map[string]string{
			"from":  conflict.StartsAt.In(info.Loc).Format("02.01 15:04"),
			"to":    conflict.EndsAt.In(info.Loc).Format("15:04"),
			"count": fmt.Sprint(info.BookingGapMinutes()),
		}}
	case !errors.Is(err, pgx.ErrNoRows):
		return 0, err
	}
	// hasPaidSession() && session_ends_at > startsAt − gap.
	paid := pc.CurrentSessionID != nil && (pc.CurrentSessionUserID != nil || pc.SessionGuest) &&
		pc.SessionEndsAt != nil && pc.SessionEndsAt.After(now)
	if paid && pc.SessionEndsAt.After(b.StartsAt.Add(-gap)) {
		return 0, &Error{Field: "starts_at", Message: MsgSession, Params: map[string]string{
			"time": pc.SessionEndsAt.In(info.Loc).Format("02.01 15:04"),
		}}
	}
	uid := b.UserID
	name := b.CustomerName
	id, err := q.InsertReservation(ctx, store.InsertReservationParams{
		ClubID: info.ID, ComputerID: pc.ID, UserID: &uid, CustomerName: &name, CustomerPhone: b.CustomerPhone, Note: b.Note,
		StartsAt: b.StartsAt, EndsAt: b.EndsAt, CreatedBy: &actorID, Now: &now,
	})
	if err != nil {
		return 0, err
	}
	computer := club.ComputerName(pc.DisplayName, pc.DeviceName, pc.Number)
	from := b.StartsAt.In(info.Loc)
	to := b.EndsAt.In(info.Loc)
	if _, err := activity.Record(ctx, q, activity.Entry{
		ClubID: info.ID, Event: "reservation.created", UserID: actorID, SubjectType: subjectBooking, SubjectID: id, At: now,
		Details: map[string]any{
			"computer": computer, "starts_at": from.Format("02.01.2006 15:04"), "ends_at": to.Format("02.01.2006 15:04"),
		},
	}.Translated(logCreated, map[string]string{
		"computer": computer, "from": from.Format("02.01.2006 15:04"), "to": to.Format("15:04"),
	})); err != nil {
		return 0, err
	}
	return id, changed(ctx, q, info, id, pc.ID, "created", from)
}

// Cancel — ComputerReservations::cancel().
func Cancel(ctx context.Context, q *store.Queries, info club.Info, actorID, id int64, now time.Time) error {
	return closeBooking(ctx, q, info, actorID, id, now, false)
}

// Arrive — ComputerReservations::arrive(): клиент пришёл, ПК больше не удерживается.
func Arrive(ctx context.Context, q *store.Queries, info club.Info, actorID, id int64, now time.Time) error {
	return closeBooking(ctx, q, info, actorID, id, now, true)
}

// closeBooking — close(): закрыть можно только ожидающую или идущую бронь.
func closeBooking(ctx context.Context, q *store.Queries, info club.Info, actorID, id int64, now time.Time, arrive bool) error {
	r, err := q.LockReservation(ctx, store.LockReservationParams{ClubID: info.ID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if s := State(r.StartsAt, r.EndsAt, r.ArrivedAt, r.CancelledAt, now); s != "pending" && s != "active" {
		return &Error{Field: "reservation", Message: MsgClosed}
	}
	event, key, action := "reservation.cancelled", logCancelled, "cancelled" //nolint:misspell // код события старого журнала
	if arrive {
		event, key, action = "reservation.arrived", logArrived, "arrived"
		err = q.ArriveReservation(ctx, store.ArriveReservationParams{ClubID: info.ID, ID: r.ID, Now: &now})
	} else {
		err = q.CancelReservation(ctx, store.CancelReservationParams{ClubID: info.ID, ID: r.ID, Now: &now})
	}
	if err != nil {
		return err
	}
	computer := "—"
	if r.ComputerNumber != nil {
		computer = club.ComputerName(r.ComputerDisplayName, r.ComputerDeviceName, *r.ComputerNumber)
	}
	from := r.StartsAt.In(info.Loc)
	if _, err := activity.Record(ctx, q, activity.Entry{
		ClubID: info.ID, Event: event, UserID: actorID, SubjectType: subjectBooking, SubjectID: r.ID, At: now,
		Details: map[string]any{"computer": computer, "starts_at": from.Format("02.01.2006 15:04")},
	}.Translated(key, map[string]string{"computer": computer, "from": from.Format("02.01.2006 15:04")})); err != nil {
		return err
	}
	return changed(ctx, q, info, r.ID, r.ClubComputerID, action, from)
}

// changed — reservation.changed (экран «Брони»: перечитать день) и computer.reservation_changed (карта клуба,
// вебхук интеграций; docs/realtime.md).
func changed(ctx context.Context, q *store.Queries, info club.Info, id, computerID int64, action string, startsLocal time.Time) error {
	if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: info.ID, Type: "computer.reservation_changed", Data: map[string]any{
		"computer_id": computerID, "reservation_id": id, "action": action,
	}}); err != nil {
		return err
	}
	_, err := outbox.Add(ctx, q, outbox.Event{ClubID: info.ID, Type: "reservation.changed", Data: map[string]any{
		"reservation_id": id, "computer_id": computerID, "action": action, "date": startsLocal.Format("2006-01-02"),
	}})
	return err
}
