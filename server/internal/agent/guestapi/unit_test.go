package guestapi

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

func TestNormalizePhone(t *testing.T) {
	// PhoneNumber::normalize() (app/Support/PhoneNumber.php).
	for in, want := range map[string]string{
		"+7 700 123-45-67": "+77001234567",
		"8 700 123 45 67":  "+77001234567",
		"7001234567":       "+77001234567",
		"77001234567":      "+77001234567",
		"":                 "",
		"123":              "+7123",
	} {
		if got := NormalizePhone(in); got != want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	// Friends::maskPhone().
	for in, want := range map[string]string{"+77011112233": "+7 701 ••• •• 33", "+1555": "•••", "": ""} {
		if got := MaskPhone(in); got != want {
			t.Errorf("MaskPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClubSettings(t *testing.T) {
	cs := parseClubSettings([]byte(`{"guests": {"show_qr_code_on_login": "yes", "allow_adding_friends": 1, "allow_guest_self_transfer": "nope",
		"allow_balance_transfer_to_friends": true}, "club": {"agent_shop_enabled": null, "agent_idle_logout_minutes": "2.1",
		"pc_action_after_session_end": "shutdown", "pc_restart_after_session_end": "0.75"}, "finances": {"currency": "USD"}}`))
	f := cs.guestFeatures()
	if !f.GetQrLogin() || !f.GetFriends() || f.GetSelfTransfer() || !f.GetBalanceTransfer() || !f.GetShop() {
		t.Fatalf("features = %+v", f)
	}
	if got := cs.idleLogoutMinutes(); got != 3 { // ceil(2.1)
		t.Fatalf("idle = %d", got)
	}
	if a, d := cs.afterSession(); a != agentv1.AfterSessionAction_AFTER_SESSION_ACTION_SHUTDOWN || d != 45*time.Second {
		t.Fatalf("after session = %v %v", a, d)
	}
	if cs.currency != "USD" {
		t.Fatalf("currency = %q", cs.currency)
	}
	def := parseClubSettings(nil)
	if a, d := def.afterSession(); a != agentv1.AfterSessionAction_AFTER_SESSION_ACTION_NONE || d != 30*time.Second {
		t.Fatalf("defaults = %v %v", a, d)
	}
	if def.idleLogoutMinutes() != DefaultIdleLogoutMinutes || !def.shopEnabled() || def.feature(featureQRLogin) || def.currency != "KZT" {
		t.Fatal("defaults")
	}
	off := parseClubSettings([]byte(`{"club": {"agent_shop_enabled": "0", "agent_idle_logout_minutes": "abc", "pc_restart_after_session_end": "x"}}`))
	if off.shopEnabled() || off.idleLogoutMinutes() != DefaultIdleLogoutMinutes {
		t.Fatal("shop/idle")
	}
	if _, d := off.afterSession(); d != 30*time.Second {
		t.Fatalf("delay = %v", d)
	}
}

func TestPHPBool(t *testing.T) {
	for raw, want := range map[string]bool{`true`: true, `"1"`: true, `"On"`: true, `" yes "`: true, `1`: true, `0`: false,
		`"0"`: false, `"abc"`: false, `false`: false, `[]`: false, `2`: false} {
		if got := phpBool(json.RawMessage(raw)); got != want {
			t.Errorf("phpBool(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestClubLocale(t *testing.T) {
	langs := []store.GuestEnabledLanguagesRow{{Code: "kk"}, {Code: "ru"}}
	kk, en := "kk", "en"
	if clubLocale(&kk, langs) != "kk" || clubLocale(&en, langs) != "ru" || clubLocale(nil, langs) != "ru" {
		t.Fatal("locale")
	}
	if clubLocale(&en, []store.GuestEnabledLanguagesRow{{Code: "kk"}}) != "kk" || clubLocale(&en, nil) != "en" {
		t.Fatal("fallback")
	}
}

func TestToStatus(t *testing.T) {
	cases := []struct {
		err    error
		code   codes.Code
		reason agentv1.ErrorCode
		msg    string
	}{
		{pricing.ErrInsufficientFunds, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_INSUFFICIENT_FUNDS, "Недостаточно средств для покупки тарифа."},
		{pricing.ErrTariffNotForComputer, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_TARIFF_UNAVAILABLE, "Тариф недоступен для этого компьютера."},
		{pricing.ErrMainBalanceOnly, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED, "Этот тариф оплачивается только с основного баланса."},
		{pricing.ErrComputerReserved, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_COMPUTER_RESERVED, ""},
		{session.ErrTransferUnavailable, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SEAT_UNAVAILABLE, ""},
		{session.ErrPackageExpired, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_PACKAGE_UNAVAILABLE, "Срок абонемента истёк."},
		{billingsvc.ErrNoSuchSession, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SESSION_MISMATCH, "На компьютере нет такой активной сессии."},
		{billingsvc.ErrForbidden, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_SESSION_MISMATCH, ""},
		{billingsvc.ErrIdempotencyConflict, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_IDEMPOTENCY_KEY_REUSED, ""},
		{&billing.BalanceError{Field: "amount", Message: "Недостаточно средств на основном счёте."}, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_INSUFFICIENT_FUNDS, ""},
		{&billing.BalanceError{Field: "friendship", Message: "Переводить можно только друзьям."}, codes.NotFound, agentv1.ErrorCode_ERROR_CODE_FRIENDSHIP_NOT_FOUND, ""},
		{&shop.Error{Code: shop.ErrNotEnoughStock.Code, Field: "items", Message: shop.MsgNotEnoughStock, Params: map[string]string{"product": "Cola"}},
			codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_OUT_OF_STOCK, "Недостаточно товара: Cola."},
		{errors.New("boom"), codes.Internal, agentv1.ErrorCode_ERROR_CODE_INTERNAL, "Внутренняя ошибка сервера."},
	}
	for _, c := range cases {
		_, err := toStatus(c.err)
		st, _ := status.FromError(err)
		if st.Code() != c.code || grpcserver.ReasonOf(err) != c.reason.String() || (c.msg != "" && st.Message() != c.msg) {
			t.Errorf("%v → %v %s %q", c.err, st.Code(), grpcserver.ReasonOf(err), st.Message())
		}
	}
}

func TestHistoryEntry(t *testing.T) {
	sub := int64(7)
	e := historyEntry(historyRow{ID: 1, UserID: 5, Event: "tariff.purchased", SubjectID: &sub,
		Details: map[string]any{"amount": "1000.00", "main_balance_amount": "700.00", "bonus_balance_amount": "300.00"}}, 5, map[int64]string{7: "Час"}, nil)
	if e.GetTitle() != "Покупка тарифа «Час»" || e.GetMainDeltaTiyn() != -70000 || e.GetBonusDeltaTiyn() != -30000 {
		t.Fatalf("tariff = %+v", e)
	}
	e = historyEntry(historyRow{Event: "user.balance_topped_up", Details: map[string]any{"amount": 500.0, "bonus_amount": "50", "balance_type": "основной баланс"}}, 5, nil, nil)
	if e.GetMainDeltaTiyn() != 50000 || e.GetBonusDeltaTiyn() != 5000 {
		t.Fatalf("top up = %+v", e)
	}
	e = historyEntry(historyRow{Event: "balance.transferred", UserID: 9, Details: map[string]any{"amount": "10.00", "to_user_id": 5.0}}, 5, nil, map[int64]string{9: "Дана"})
	if e.GetTitle() != "Перевод от друга Дана" || e.GetMainDeltaTiyn() != 1000 {
		t.Fatalf("transfer = %+v", e)
	}
	e = historyEntry(historyRow{Event: "auth.api_login"}, 5, nil, nil)
	if e.GetTitle() != "Вход в аккаунт" {
		t.Fatalf("login = %+v", e)
	}
}

func TestRandomCode(t *testing.T) {
	c, err := randomCode(40)
	if err != nil || len(c) != 40 {
		t.Fatal(c, err)
	}
	for _, r := range c {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			t.Fatalf("bad char %q", r)
		}
	}
}

// S7-AB-2: срок пересмотра «не положено» — слот клуба: позже now, не позже now + AutoBillingRecheck, у ПК одного
// клуба одинаковый, от момента внутри периода не зависит (тот же слот), дальше — ровно через период; у разных клубов
// слоты разные.
func TestAutoBillingRecheckSlot(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 123e6, time.UTC)
	slots := map[int64]int64{}
	for club := int64(1); club <= 1000; club++ {
		at := abRecheckAt(club, now)
		if !at.After(now) || at.After(now.Add(AutoBillingRecheck)) {
			t.Fatalf("club %d: %v not in (now, now+%v]", club, at.Sub(now), AutoBillingRecheck)
		}
		if again := abRecheckAt(club, now.Add(at.Sub(now)/2)); !again.Equal(at) {
			t.Fatalf("club %d: slot moved %v → %v", club, at, again)
		}
		if next := abRecheckAt(club, at); !next.Equal(at.Add(AutoBillingRecheck)) {
			t.Fatalf("club %d: next %v, want %v", club, next, at.Add(AutoBillingRecheck))
		}
		slots[at.UnixMilli()%AutoBillingRecheck.Milliseconds()]++
	}
	if len(slots) != 1000 {
		t.Fatalf("1000 clubs share %d slots", len(slots))
	}
}
