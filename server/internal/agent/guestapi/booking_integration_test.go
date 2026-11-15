//go:build integration

package guestapi_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// Брони из гостевого приложения на ПК (тема Ж): вход по коду брони (GuestAuthService.StartBookingByCode) и запуск
// оплаченной брони гостя при входе. Сценарии — docs/golden-scenarios.md GS-544, GS-551 (GuestBookingTest.php), здесь
// — тот же сервис reservations через gRPC агента с mTLS.

// booking — оплаченная бронь из приложения на ПК p: начало через startIn от часов теста, длительность dur.
func (h *harness) booking(p *pc, user int64, code string, startIn, dur time.Duration, main, bonus int64) int64 {
	h.t.Helper()
	from := h.clock.Now().Add(startIn)
	return h.int(`INSERT INTO computer_reservations (club_id, club_computer_id, user_id, starts_at, ends_at, from_app,
			paid_main_cents, paid_bonus_cents, code, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, true, $6, $7, $8, now(), now()) RETURNING id`,
		h.club, p.id, user, from, from.Add(dur), main, bonus, code)
}

func (p *pc) bookingCode(code string) (*agentv1.StartBookingByCodeResponse, error) {
	return p.authc().StartBookingByCode(withToken(""), &agentv1.StartBookingByCodeRequest{Code: code})
}

// GS-551 через gRPC агента: успех на забронированном ПК, отказы (формат, не найден, чужой ПК, рано, занято), повтор кода, код чужого клуба.
func TestBookingCode(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	user := h.customer("Арман", uniqPhone(), "password", 500000, 0)
	booked, other, early, busy := h.enrollPC(), h.enrollPC(), h.enrollPC(), h.enrollPC()
	// Бронь началась 10 минут назад на час: начинается остаток — 50 минут (PlanStart, BR-178).
	res := h.booking(booked, user, "123456", -10*time.Minute, time.Hour, 60000, 0)
	h.booking(early, user, "222222", 2*time.Hour, time.Hour, 60000, 0)
	h.booking(busy, user, "333333", 0, time.Hour, 60000, 0)
	h.exec("UPDATE club_computers SET current_session_id = 'cash-1', session_guest = true, session_ends_at = $2 WHERE id = $1",
		busy.id, h.clock.Now().Add(time.Hour))

	for _, code := range []string{"", "12345", "1234567", "12345a", " 123456"} {
		_, err := booked.bookingCode(code)
		wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
		if st, _ := status.FromError(err); st.Message() != "Код брони — 6 цифр." {
			t.Fatalf("format message = %q", st.Message())
		}
	}
	_, err := booked.bookingCode("000000")
	wantReason(t, err, codes.NotFound, agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_NOT_FOUND)
	_, err = other.bookingCode("123456")
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_OTHER_COMPUTER)
	_, err = early.bookingCode("222222")
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_BOOKING_NOT_STARTED)
	if st, _ := status.FromError(err); !strings.Contains(st.Message(), "Код заработает за 30 мин до начала.") || strings.Contains(st.Message(), ":time") {
		t.Fatalf("early message = %q", st.Message())
	}
	_, err = busy.bookingCode("333333")
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_COMPUTER_BUSY)
	// Код клуба A на ПК клуба B — «не найден» (клуб — из сертификата ПК).
	foreign := newHarness(t, harnessOpts{noLimits: true}).enrollPC()
	_, err = foreign.bookingCode("123456")
	wantReason(t, err, codes.NotFound, agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_NOT_FOUND)
	if n := h.int("SELECT count(*) FROM computer_reservations WHERE club_id = $1 AND arrived_at IS NOT NULL", h.club); n != 0 {
		t.Fatalf("started after rejections = %d", n)
	}

	as := booked.connect("boot-code", 0)
	ok, err := booked.bookingCode("123456")
	if err != nil {
		t.Fatal(err)
	}
	s := ok.GetSession()
	if s.GetSessionId() == "" || !s.GetGuest() || s.GetUserId() != 0 || s.GetEndsAt().AsTime().Sub(h.clock.Now()) != 50*time.Minute {
		t.Fatalf("session = %+v", s)
	}
	var sid string
	var guest bool
	h.scan(&sid, "SELECT current_session_id FROM club_computers WHERE id = $1", booked.id)
	h.scan(&guest, "SELECT session_guest AND current_session_user_id IS NULL FROM club_computers WHERE id = $1", booked.id)
	if sid != s.GetSessionId() || !guest {
		t.Fatalf("computer session = %q guest %v", sid, guest)
	}
	if n := h.int("SELECT count(*) FROM computer_reservations WHERE id = $1 AND arrived_at IS NOT NULL", res); n != 1 {
		t.Fatal("reservation not arrived")
	}
	// Журнал — от имени гостя брони, с by_code; части сессии нет (время не возвращается, UQ-15); деньги не двигались.
	if n := h.int(`SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'reservation.arrived' AND user_id = $2
		AND details->>'by_code' = 'true'`, h.club, user); n != 1 {
		t.Fatalf("reservation.arrived entries = %d", n)
	}
	if n := h.int("SELECT count(*) FROM session_purchases WHERE club_id = $1", h.club); n != 0 {
		t.Fatalf("session parts = %d", n)
	}
	h.wantBalances(user, 500000, 0)
	h.noDrift()
	// Та же сессия — командой StartSession (ответ RPC мог не дойти до ПК).
	cmd := as.waitCommand(func(c *agentv1.Command) bool { return c.GetStartSession() != nil })
	if got := cmd.GetStartSession().GetSession(); got.GetSessionId() != s.GetSessionId() || !got.GetGuest() {
		t.Fatalf("StartSession = %+v", got)
	}
	// Welcome при переподключении несёт ту же гостевую сессию.
	as.cancel()
	if w := booked.connect("boot-code", 0).welcome.GetSession(); w.GetSessionId() != s.GetSessionId() || !w.GetGuest() {
		t.Fatalf("welcome session = %+v", w)
	}
	// Повтор кода после старта: бронь уже не ожидающая — на своём ПК и на чужом «не найден», сессия та же.
	_, err = booked.bookingCode("123456")
	wantReason(t, err, codes.NotFound, agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_NOT_FOUND)
	_, err = other.bookingCode("123456")
	wantReason(t, err, codes.NotFound, agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_NOT_FOUND)
	h.scan(&sid, "SELECT current_session_id FROM club_computers WHERE id = $1", booked.id)
	if sid != s.GetSessionId() {
		t.Fatalf("session changed by a repeated code: %q", sid)
	}
}

// Подписка клуба не активна — код не принимается (UQ-12), бронь остаётся ожидающей.
func TestBookingCodeSubscriptionInactive(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	user := h.customer("Арман", uniqPhone(), "password", 0, 0)
	p := h.enrollPC()
	res := h.booking(p, user, "123456", 0, time.Hour, 60000, 0)
	h.exec("UPDATE club_subscriptions SET status = 'expired', current_period_ends_at = now() - interval '1 day' WHERE club_id = $1", h.club)
	_, err := p.bookingCode("123456")
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SUBSCRIPTION_INACTIVE)
	if n := h.int("SELECT count(*) FROM computer_reservations WHERE id = $1 AND arrived_at IS NULL", res); n != 1 {
		t.Fatal("reservation started without a subscription")
	}
}

// Перебор кода: 5 попыток в минуту на ПК (agent-booking-code) — шестая отклоняется лимитом даже с верным кодом;
// другой ПК клуба считает свои попытки.
func TestBookingCodeRateLimit(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	user := h.customer("Арман", uniqPhone(), "password", 0, 0)
	p, second := h.enrollPC(), h.enrollPC()
	res := h.booking(p, user, "123456", 0, time.Hour, 60000, 0)
	h.booking(second, user, "654321", 0, time.Hour, 60000, 0)
	for i := 0; i < 5; i++ {
		_, err := p.bookingCode("00000" + string(rune('0'+i)))
		wantReason(t, err, codes.NotFound, agentv1.ErrorCode_ERROR_CODE_BOOKING_CODE_NOT_FOUND)
	}
	_, err := p.bookingCode("123456")
	wantReason(t, err, codes.ResourceExhausted, agentv1.ErrorCode_ERROR_CODE_RATE_LIMITED)
	if n := h.int("SELECT count(*) FROM computer_reservations WHERE id = $1 AND arrived_at IS NULL", res); n != 1 {
		t.Fatal("reservation started over the limit")
	}
	if _, err := second.bookingCode("654321"); err != nil {
		t.Fatalf("second PC: %v", err)
	}
}

// Гонка: один код на двух забронированных вместе ПК и на третьем, не забронированном. Каждый забронированный ПК
// открывается ровно один раз (остальные одновременные попытки отклоняются), чужой — никогда.
func TestBookingCodeRace(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	user := h.customer("Арман", uniqPhone(), "password", 0, 0)
	a, b, stranger := h.enrollPC(), h.enrollPC(), h.enrollPC()
	h.booking(a, user, "123456", 0, time.Hour, 60000, 0)
	h.booking(b, user, "123456", 0, time.Hour, 60000, 0)
	const tries = 6
	type result struct {
		pc  *pc
		err error
	}
	results := make(chan result, 3*tries)
	var wg sync.WaitGroup
	for _, p := range []*pc{a, b, stranger} {
		for i := 0; i < tries; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := p.bookingCode("123456")
				results <- result{p, err}
			}()
		}
	}
	wg.Wait()
	close(results)
	okBy := map[int64]int{}
	for r := range results {
		if r.err == nil {
			okBy[r.pc.id]++
			continue
		}
		// Проигравшие и чужой ПК: пока бронь другого ПК группы ещё ожидает — «не для этого ПК», потом — «не найден».
		if c := status.Code(r.err); c != codes.FailedPrecondition && c != codes.NotFound {
			t.Fatalf("pc %d: %v", r.pc.id, r.err)
		}
	}
	if okBy[a.id] != 1 || okBy[b.id] != 1 || okBy[stranger.id] != 0 {
		t.Fatalf("starts = %v", okBy)
	}
	if n := h.int("SELECT count(*) FROM club_computers WHERE club_id = $1 AND session_guest AND current_session_id IS NOT NULL", h.club); n != 2 {
		t.Fatalf("guest sessions = %d", n)
	}
	if n := h.int("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'reservation.arrived'", h.club); n != 2 {
		t.Fatalf("reservation.arrived entries = %d", n)
	}
}

// GS-544 через gRPC агента: вход гостя на забронированный ПК сразу начинает его оплаченную бронь (UQ-11): сессия — в ответе входа, часть
// сессии несёт суммы для возврата; на другом ПК и раньше интервала клуба — не начинает.
func TestSignInStartsPaidBooking(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	phone, latePhone := uniqPhone(), uniqPhone()
	user := h.customer("Арман", phone, "password", 100000, 0)
	late := h.customer("Дана", latePhone, "password", 0, 0)
	booked, other, far := h.enrollPC(), h.enrollPC(), h.enrollPC()
	// Через 20 минут (интервал клуба — 30): начинается вся длительность, 60 минут.
	res := h.booking(booked, user, "123456", 20*time.Minute, time.Hour, 45000, 15000)
	h.booking(far, late, "222222", 2*time.Hour, time.Hour, 60000, 0)

	// Не на своём ПК — сессии нет, бронь ждёт.
	s, err := other.login(phone, "password")
	if err != nil || s.GetSession() != nil {
		t.Fatalf("login on another PC = %+v, %v", s.GetSession(), err)
	}
	if _, err := other.authc().Logout(withToken(s.GetToken().GetAccessToken()), &agentv1.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	// Рано — не начинается.
	if s, err := far.login(latePhone, "password"); err != nil || s.GetSession() != nil {
		t.Fatalf("early login = %+v, %v", s.GetSession(), err)
	}

	s, err = booked.login(phone, "password")
	if err != nil {
		t.Fatal(err)
	}
	ses := s.GetSession()
	if ses.GetSessionId() == "" || ses.GetGuest() || ses.GetUserId() != user || ses.GetEndsAt().AsTime().Sub(h.clock.Now()) != time.Hour ||
		ses.GetTariffName() != "Бронь" {
		t.Fatalf("session = %+v", ses)
	}
	if n := h.int(`SELECT count(*) FROM session_purchases WHERE club_id = $1 AND session_id = $2 AND user_id = $3 AND minutes = 60
		AND main_cents = 45000 AND bonus_cents = 15000`, h.club, ses.GetSessionId(), user); n != 1 {
		t.Fatalf("session part = %d", n)
	}
	if n := h.int("SELECT count(*) FROM computer_reservations WHERE id = $1 AND arrived_at IS NOT NULL", res); n != 1 {
		t.Fatal("reservation not arrived")
	}
	if n := h.int(`SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'reservation.arrived' AND user_id = $2
		AND details->>'by_code' IS NULL`, h.club, user); n != 1 {
		t.Fatalf("reservation.arrived entries = %d", n)
	}
	// Запуск денег не двигает: бронь оплачена при бронировании.
	h.wantBalances(user, 100000, 0)
	h.noDrift()
	// Повторный вход — та же сессия, второй раз бронь не начинается.
	again, err := booked.login(phone, "password")
	if err != nil || again.GetSession().GetSessionId() != ses.GetSessionId() || !again.GetSession().GetEndsAt().AsTime().Equal(ses.GetEndsAt().AsTime()) {
		t.Fatalf("second login = %+v, %v", again.GetSession(), err)
	}
	if n := h.int("SELECT count(*) FROM session_purchases WHERE club_id = $1", h.club); n != 1 {
		t.Fatalf("session parts after the second login = %d", n)
	}
}

// Подтверждение QR-входа начинает оплаченную бронь так же, как вход по паролю.
func TestQrLoginStartsPaidBooking(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: `{"guests": {"show_qr_code_on_login": true}}`})
	user := h.customer("Арман", uniqPhone(), "password", 0, 0)
	p := h.enrollPC()
	h.booking(p, user, "123456", 0, 30*time.Minute, 30000, 0)
	qr, err := p.authc().CreateQrLogin(context.Background(), &agentv1.CreateQrLoginRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w, err := p.authc().WatchQrLogin(ctx, &agentv1.WatchQrLoginRequest{LoginId: qr.GetLoginId()})
	if err != nil {
		t.Fatal(err)
	}
	if first, err := w.Recv(); err != nil || first.GetStatus() != agentv1.QrLoginStatus_QR_LOGIN_STATUS_PENDING {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if err := h.srv.ApproveQrLogin(context.Background(), h.club, strings.TrimPrefix(qr.GetUrl(), "https://zafiro.test/pc-login/"), user); err != nil {
		t.Fatal(err)
	}
	ok, err := w.Recv()
	if err != nil || ok.GetStatus() != agentv1.QrLoginStatus_QR_LOGIN_STATUS_APPROVED {
		t.Fatalf("approved = %+v, %v", ok, err)
	}
	if ses := ok.GetSignIn().GetSession(); ses.GetUserId() != user || ses.GetEndsAt().AsTime().Sub(h.clock.Now()) != 30*time.Minute {
		t.Fatalf("session = %+v", ses)
	}
}

// История гостя в агенте: оплата брони (свои события) и возврат, сделанный сотрудником (события над аккаунтом).
func TestHistoryBookingEvents(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	phone := uniqPhone()
	user := h.customer("Арман", phone, "password", 0, 0)
	p := h.enrollPC()
	log := func(actor int64, event, details string) {
		h.exec(`INSERT INTO activity_logs (club_id, user_id, event, description, subject_type, subject_id, details, ip_address, created_at)
			VALUES ($1, $2, $3, '', 'App\Models\User', $4, $5::jsonb, '127.0.0.1', now())`, h.club, actor, event, user, details)
	}
	log(user, "reservation.paid", `{"amount": "600.00", "main_balance_amount": "450.00", "bonus_balance_amount": "150.00", "computer": "PC-1, PC-2", "starts_at": "16.11.2026 18:00"}`)
	log(h.owner, "reservation.refunded", `{"main_balance_amount": "225.00", "bonus_balance_amount": "75.00"}`)
	ctx := withToken(p.mustLogin(phone, "password"))
	for _, filter := range []agentv1.HistoryFilter{agentv1.HistoryFilter_HISTORY_FILTER_UNSPECIFIED, agentv1.HistoryFilter_HISTORY_FILTER_PURCHASES} {
		resp, err := p.guest().ListHistory(ctx, &agentv1.ListHistoryRequest{Filter: filter})
		if err != nil {
			t.Fatal(err)
		}
		var paid, refunded *agentv1.HistoryEntry
		for _, e := range resp.GetEntries() {
			switch e.GetEvent() {
			case "reservation.paid":
				paid = e
			case "reservation.refunded":
				refunded = e
			}
		}
		if paid.GetTitle() != "Бронь ПК PC-1, PC-2" || paid.GetMainDeltaTiyn() != -45000 || paid.GetBonusDeltaTiyn() != -15000 {
			t.Fatalf("filter %v: paid = %+v", filter, paid)
		}
		if refunded.GetTitle() != "Возврат за отменённую бронь" || refunded.GetMainDeltaTiyn() != 22500 || refunded.GetBonusDeltaTiyn() != 7500 {
			t.Fatalf("filter %v: refunded = %+v", filter, refunded)
		}
	}
	resp, err := p.guest().ListHistory(ctx, &agentv1.ListHistoryRequest{Filter: agentv1.HistoryFilter_HISTORY_FILTER_TOP_UPS})
	if err != nil || len(resp.GetEntries()) != 0 {
		t.Fatalf("top-ups = %+v, %v", resp.GetEntries(), err)
	}
}
