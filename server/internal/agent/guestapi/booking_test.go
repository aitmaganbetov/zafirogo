package guestapi

import (
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/reservations"
)

// Ошибки кода брони (422 поля code старого AgentBookingCodeController) → типизированные коды протокола; текст —
// старый, с подстановками; поле — code.
func TestBookingCodeStatus(t *testing.T) {
	cases := []struct {
		name    string
		err     *reservations.Error
		code    codes.Code
		reason  agentv1.ErrorCode
		message string
	}{
		{"format", &reservations.Error{Field: "code", Message: reservations.MsgCodeFormat},
			codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED, "Код брони — 6 цифр."},
		{"not found", &reservations.Error{Field: "code", Message: reservations.MsgCodeNotFound},
			codes.NotFound, agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_NOT_FOUND, "Код брони не найден или бронь уже закончилась."},
		{"other pc", &reservations.Error{Field: "code", Message: reservations.MsgCodeOtherPC},
			codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_OTHER_COMPUTER,
			"Этот код брони не для этого ПК: сядьте за забронированный ПК."},
		{"early", &reservations.Error{Field: "code", Message: reservations.MsgCodeEarly, Params: map[string]string{"time": "16.11 18:00", "count": "30"}},
			codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_BOOKING_NOT_STARTED,
			"Бронь начнётся в 16.11 18:00. Код заработает за 30 мин до начала."},
		{"busy", &reservations.Error{Field: "code", Message: reservations.MsgCodeBusy},
			codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_COMPUTER_BUSY, "На этом ПК уже идёт сессия."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := bookingCodeStatus(c.err)
			st, _ := status.FromError(err)
			if st.Code() != c.code || grpcserver.ReasonOf(err) != c.reason.String() || st.Message() != c.message {
				t.Fatalf("status = %v %s %q", st.Code(), grpcserver.ReasonOf(err), st.Message())
			}
			field := ""
			for _, d := range st.Details() {
				if info, ok := d.(*errdetails.ErrorInfo); ok {
					field = info.GetMetadata()["field"]
				}
			}
			if field != "code" {
				t.Fatalf("field = %q", field)
			}
		})
	}
}

// История гостя: оплата и возврат брони из приложения (GuestHistory::entry()).
func TestHistoryEntryBooking(t *testing.T) {
	e := historyEntry(historyRow{ID: 1, UserID: 5, Event: "reservation.paid", Details: map[string]any{
		"amount": "600.00", "main_balance_amount": "450.00", "bonus_balance_amount": "150.00", "computer": "PC-1", "starts_at": "16.11.2026 18:00",
	}}, 5, nil, nil)
	if e.GetTitle() != "Бронь ПК PC-1" || e.GetMainDeltaTiyn() != -45000 || e.GetBonusDeltaTiyn() != -15000 {
		t.Fatalf("paid = %+v", e)
	}
	// Без имени ПК — «—»; без сумм — без изменения балансов.
	if e := historyEntry(historyRow{Event: "reservation.paid"}, 5, nil, nil); e.GetTitle() != "Бронь ПК —" || e.GetMainDeltaTiyn() != 0 {
		t.Fatalf("paid without details = %+v", e)
	}
	e = historyEntry(historyRow{Event: "reservation.refunded", UserID: 9, Details: map[string]any{
		"main_balance_amount": "225.00", "bonus_balance_amount": "75.00",
	}}, 5, nil, nil)
	if e.GetTitle() != "Возврат за отменённую бронь" || e.GetMainDeltaTiyn() != 22500 || e.GetBonusDeltaTiyn() != 7500 {
		t.Fatalf("refunded = %+v", e)
	}
	for _, f := range [][2][]string{{historyOwnEvents, historyReceivedEvents}, historyFilters[agentv1.HistoryFilter_HISTORY_FILTER_PURCHASES]} {
		own, received := map[string]bool{}, map[string]bool{}
		for _, ev := range f[0] {
			own[ev] = true
		}
		for _, ev := range f[1] {
			received[ev] = true
		}
		if !own["reservation.paid"] || !own["reservation.refunded"] || !received["reservation.refunded"] || received["reservation.paid"] {
			t.Fatalf("events = %v / %v", f[0], f[1])
		}
	}
}
