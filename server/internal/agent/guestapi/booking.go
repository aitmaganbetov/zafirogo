package guestapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Брони из гостевого приложения на ПК (тема Ж, docs/open-questions/upstream-agent.md): вход по коду брони и запуск
// оплаченной брони гостя при его входе. Правила — reservations.StartByCode / StartForSignedIn (BR-178).

// StartBookingByCode — код брони на экране входа ПК: оплаченное время брони начинается сессией без аккаунта.
//
// Laravel: app/Http/Controllers/Api/AgentBookingCodeController.php __invoke() (POST /api/agent/booking-code,
// middleware agent.device + throttle:agent-booking-code), app/Services/ComputerReservations.php startByCode().
// ПК и клуб — из сертификата mTLS; код чужого клуба здесь «не найден» (запросы и RLS — по клубу ПК). Подписка клуба
// проверяется, как у любого RPC гостя (UQ-12). Неудачные попытки — в системный журнал без самого кода (как неверный
// PIN выхода без вошедшего гостя): по ним виден перебор; сам перебор ограничивает лимит.
func (a *authService) StartBookingByCode(ctx context.Context, req *agentv1.StartBookingByCodeRequest) (*agentv1.StartBookingByCodeResponse, error) {
	s := a.s
	ctx, id, err := s.call(ctx, ratelimit.AgentBookingCode, nil)
	if err != nil {
		return nil, err
	}
	now := s.o.Now()
	ctx, hooks := billingsvc.WithCommitHooks(ctx)
	var started reservations.Started
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		hooks.Reset()
		if err := checkSubscription(ctx, q, id.ClubID, now); err != nil {
			return err
		}
		info, err := club.Load(ctx, q, id.ClubID)
		if err != nil {
			return err
		}
		started, err = reservations.StartByCode(ctx, q, info, id.ComputerID, req.GetCode(), reservations.Env{
			Now: now, Actor: billing.BalanceActor{Kind: "agent"}, Notifier: s.o.Notifier,
		})
		return err
	})
	if err != nil {
		var re *reservations.Error
		if errors.As(err, &re) {
			s.o.Log.WarnContext(ctx, "booking code rejected", "club_id", id.ClubID, "computer_id", id.ComputerID,
				"reason", bookingCodeReason(re).String())
			return nil, bookingCodeStatus(re)
		}
		if errors.Is(err, reservations.ErrNotFound) {
			return nil, rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_COMPUTER_NOT_FOUND, msgNotFound, nil, "")
		}
		return nil, s.fail(ctx, "StartBookingByCode", err)
	}
	// StartSession агенту — после коммита (ответ RPC мог не дойти до ПК).
	hooks.Run(context.WithoutCancel(ctx))
	s.o.Log.InfoContext(ctx, "booking opened with a code", "club_id", id.ClubID, "computer_id", id.ComputerID,
		"reservation_id", started.ReservationID)
	return &agentv1.StartBookingByCodeResponse{Session: &agentv1.ActiveSession{
		SessionId: started.SessionID, Guest: true, StartedAt: timestamppb.New(now), EndsAt: timestamppb.New(started.EndsAt),
	}}, nil
}

// bookingCodeReason — код протокола по тексту правила startByCode().
func bookingCodeReason(e *reservations.Error) agentv1.ErrorCode {
	switch e.Message {
	case reservations.MsgCodeNotFound:
		return agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_NOT_FOUND
	case reservations.MsgCodeOtherPC:
		return agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_OTHER_COMPUTER
	case reservations.MsgCodeEarly:
		return agentv1.ErrorCode_ERROR_CODE_BOOKING_NOT_STARTED
	case reservations.MsgCodeBusy:
		return agentv1.ErrorCode_ERROR_CODE_COMPUTER_BUSY
	}
	return agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED
}

// bookingCodeStatus — ошибка поля code (HTTP 422 старого контроллера) → статус gRPC с типизированным кодом.
func bookingCodeStatus(e *reservations.Error) error {
	reason := bookingCodeReason(e)
	switch reason {
	case agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED: // MsgCodeFormat
		return validation(e.Field, e.Message)
	case agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_NOT_FOUND:
		return rpcError(codes.NotFound, reason, e.Message, e.Params, e.Field)
	}
	return rpcError(codes.FailedPrecondition, reason, e.Message, e.Params, e.Field)
}

// startBookingOnSignIn — гость вошёл на ПК (пароль, регистрация, QR): его оплаченная бронь этого ПК начинается
// сразу, если уже пора, — не дожидаясь минутной задачи app-bookings:start (решение UQ-11).
//
// Laravel: app/Services/ComputerReservations.php startForSignedIn() — в старом на каждом heartbeat ПК с токеном
// клиента; здесь — событие входа. Ошибка входу не мешает (задача запустит бронь в пределах минуты) — в журнал.
// Сессию гость получает в ответе входа (GuestSignIn.session), поэтому отдельной команды StartSession нет.
func (s *Server) startBookingOnSignIn(ctx context.Context, id identity.Identity, userID int64) {
	now := s.o.Now()
	err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		info, err := club.Load(ctx, q, id.ClubID)
		if err != nil {
			return err
		}
		_, _, err = reservations.StartForSignedIn(ctx, q, info, id.ComputerID, userID, reservations.Env{
			Now: now, Actor: billing.BalanceActor{UserID: userID, Kind: "guest"},
		})
		return err
	})
	if err != nil {
		s.o.Log.WarnContext(ctx, "paid booking not started on sign-in", "club_id", id.ClubID, "computer_id", id.ComputerID,
			"user_id", userID, "err", err)
	}
}
