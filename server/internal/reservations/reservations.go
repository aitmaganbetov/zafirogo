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
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты ComputerReservations / контроллера (ключи перевода).
const (
	MsgConflict = "ПК уже забронирован с :from до :to (между бронями нужно :count мин)."
	MsgSession  = "На ПК идёт сессия до :time."
	MsgClosed   = "Эта бронь уже закрыта."
	// MsgStartedOrClosed — GuestBookings::cancel(): гость отменяет бронь только до её начала.
	MsgStartedOrClosed = "Бронь уже началась или закрыта — отменить её нельзя."
	MsgPast            = "Нельзя забронировать ПК на прошедшее время."
	MsgPickClient      = "Выберите клиента из пользователей клуба."
	logCreated         = "ПК :computer забронирован на :from – :to"
	logCancelled       = "Бронь ПК :computer на :from отменена"
	logArrived         = "Клиент пришёл по брони ПК :computer на :from"
	subjectBooking     = `App\Models\ComputerReservation`
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
	// Бронь из гостевого приложения (GuestBookings::book()): доля оплаты этого ПК в тиынах, тариф (nil — по часам)
	// и код группы ПК. У брони с кассы — нули (CHECK computer_reservations_app_only_check).
	FromApp   bool
	PaidMain  money.Tiyn
	PaidBonus money.Tiyn
	TariffID  *int64
	Code      *string
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
// reservation.created, события reservation.changed и computer.state (карта клуба).
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
		FromApp: b.FromApp, PaidMainCents: int64(b.PaidMain), PaidBonusCents: int64(b.PaidBonus), TariffID: b.TariffID, Code: b.Code,
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

// Cancel — ComputerReservations::cancel(): отмена оплаченной брони возвращает деньги гостю (BR-179).
func Cancel(ctx context.Context, q *store.Queries, info club.Info, id int64, env Env) error {
	_, err := closeBooking(ctx, q, info, id, env, false, false)
	return err
}

// Arrive — ComputerReservations::arrive(): клиент пришёл, ПК больше не удерживается; оплаченное время брони из
// приложения начинается на ПК сразу — в любой момент до конца брони (UQ-16).
func Arrive(ctx context.Context, q *store.Queries, info club.Info, id int64, env Env) error {
	_, err := closeBooking(ctx, q, info, id, env, true, false)
	return err
}

// closeBooking — close(): закрыть можно только ожидающую или идущую бронь. Порядок блокировок как в старом:
// ПК («The PC first, as bookings and purchases on it lock it»), затем бронь; состояние проверяется под блокировкой.
// onlyPending — отмена гостем: только бронь, которая ещё не началась (GuestBookings::cancel()).
//
// Laravel: app/Services/ComputerReservations.php close():
//
//	if ($column === 'arrived_at' && $reservation->isPaid()) { startPaidSession() или ошибка «На ПК идёт сессия другого клиента…» }
//	else { $reservation->update([$column => now()]); }
//	if ($column === 'cancelled_at' && $reservation->isPaid()) { $this->refund($reservation, $actor); }
func closeBooking(ctx context.Context, q *store.Queries, info club.Info, id int64, env Env, arrive, onlyPending bool) (store.LockReservationRow, error) {
	now := env.Now
	var r store.LockReservationRow
	pcID, err := q.ReservationComputerID(ctx, store.ReservationComputerIDParams{ClubID: info.ID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	pc, err := q.LockComputer(ctx, store.LockComputerParams{ClubID: info.ID, ID: pcID})
	if err != nil {
		return r, err
	}
	r, err = q.LockReservation(ctx, store.LockReservationParams{ClubID: info.ID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	state := State(r.StartsAt, r.EndsAt, r.ArrivedAt, r.CancelledAt, now)
	if onlyPending && state != "pending" {
		return r, &Error{Field: "reservation", Message: MsgStartedOrClosed}
	}
	if state != "pending" && state != "active" {
		return r, &Error{Field: "reservation", Message: MsgClosed}
	}
	paid := IsPaid(r.PaidMainCents, r.PaidBonusCents)
	computer := club.ComputerName(pc.DisplayName, pc.DeviceName, pc.Number)
	event, key, action := "reservation.cancelled", logCancelled, "cancelled" //nolint:misspell // код события старого журнала
	switch {
	case arrive && paid:
		event, key, action = "reservation.arrived", logArrived, "arrived"
		_, ok, err := startPaidSession(ctx, q, info.ID, r, pc, false, env)
		if err != nil {
			return r, err
		}
		if !ok {
			return r, &Error{Field: "reservation", Message: MsgBusyOther}
		}
	case arrive:
		event, key, action = "reservation.arrived", logArrived, "arrived"
		err = q.ArriveReservation(ctx, store.ArriveReservationParams{ClubID: info.ID, ID: r.ID, Now: &now})
	default:
		err = q.CancelReservation(ctx, store.CancelReservationParams{ClubID: info.ID, ID: r.ID, Now: &now})
	}
	if err != nil {
		return r, err
	}
	if !arrive && paid {
		if err := refund(ctx, q, info, r, computer, env); err != nil {
			return r, err
		}
	}
	from := r.StartsAt.In(info.Loc)
	if _, err := activity.Record(ctx, q, activity.Entry{
		ClubID: info.ID, Event: event, UserID: env.Actor.UserID, SubjectType: subjectBooking, SubjectID: r.ID, At: now, Meta: env.Meta,
		Details: map[string]any{"computer": computer, "starts_at": from.Format("02.01.2006 15:04")},
	}.Translated(key, map[string]string{"computer": computer, "from": from.Format("02.01.2006 15:04")})); err != nil {
		return r, err
	}
	return r, changed(ctx, q, info, r.ID, r.ClubComputerID, action, from)
}

// changed — reservation.changed (экран «Брони»: перечитать день) и computer.state (карта клуба: перечитать ПК —
// бронь «сейчас» меняет статус; docs/realtime.md). Вебхук computer.reservation_changed бронь не шлёт: в старом коде
// его отправляли только флаг брони ПК (ClubComputerController::control(reserve), API v1 PUT …/reservation) с
// данными {computer_ids, reserved}; ReservationController вебхуков не отправлял (Q-057, блок H1).
func changed(ctx context.Context, q *store.Queries, info club.Info, id, computerID int64, action string, startsLocal time.Time) error {
	if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: info.ID, Type: "computer.state", Data: map[string]any{
		"computer_id": computerID,
	}}); err != nil {
		return err
	}
	_, err := outbox.Add(ctx, q, outbox.Event{ClubID: info.ID, Type: "reservation.changed", Data: map[string]any{
		"reservation_id": id, "computer_id": computerID, "action": action, "date": startsLocal.Format("2006-01-02"),
	}})
	return err
}
