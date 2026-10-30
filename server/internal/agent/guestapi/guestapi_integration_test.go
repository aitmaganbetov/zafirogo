//go:build integration

package guestapi_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

func key() string { return uuid.NewString() }

func nowPB() *timestamppb.Timestamp { return timestamppb.Now() }

// Сквозной сценарий: Enroll → стрим (Welcome) → вход гостя (перехеш bcrypt) → тарифы → покупка тарифа
// (идемпотентный повтор) → баланс уменьшился один раз → заказ в магазине → BalanceChanged в стрим →
// завершение сессии → возврат.
func TestGuestEndToEnd(t *testing.T) {
	h := newHarness(t, harnessOpts{settings: `{"club": {"pc_action_after_session_end": "restart", "pc_restart_after_session_end": 1.5,
		"agent_idle_logout_minutes": 10}, "guests": {"show_qr_code_on_login": "1"}, "finances": {"currency": "KZT", "allow_mixed_payment": true}}`})
	phone := uniqPhone()
	user := h.customer("Арман", phone, "password", 200000, 50000)
	tariff := h.int(`INSERT INTO tariffs (club_id, name, duration_minutes, price, payment_method, is_active, created_at, updated_at)
		VALUES ($1, 'Час', 60, 100000, 'main_or_bonus', true, now(), now()) RETURNING id`, h.club)
	h.int(`INSERT INTO tariffs (club_id, name, duration_minutes, price, payment_method, is_active, created_at, updated_at)
		VALUES ($1, 'Выключен', 60, 1, 'main_or_bonus', false, now(), now()) RETURNING id`, h.club)
	cola := h.int(`INSERT INTO products (club_id, sku, name, category, price, quantity, created_at, updated_at)
		VALUES ($1, 'COLA', 'Cola', 'Напитки', 35000, 10, now(), now()) RETURNING id`, h.club)
	p := h.enrollPC()

	// Стрим: Welcome с ClubConfig из настроек клуба, сроком продления сертификата.
	as := p.connect("boot-1", user)
	w := as.welcome
	cfg := w.GetConfig()
	if cfg.GetAfterSessionAction() != agentv1.AfterSessionAction_AFTER_SESSION_ACTION_RESTART || cfg.GetAfterSessionDelay().AsDuration() != 90*time.Second ||
		cfg.GetIdleLogoutMinutes() != 10 || !cfg.GetGuestFeatures().GetQrLogin() || !cfg.GetGuestFeatures().GetShop() ||
		cfg.GetGuestFeatures().GetFriends() || cfg.GetCurrency() != "KZT" || cfg.GetStaffMinutes() != 15 || cfg.GetAutoBillingPauseMinutes() != 5 {
		t.Fatalf("club config = %+v", cfg)
	}
	if ra := w.GetCertificateRenewAfter().AsTime(); ra.Before(time.Now().Add(19*24*time.Hour)) || ra.After(time.Now().Add(21*24*time.Hour)) {
		t.Fatalf("certificate_renew_after = %v", ra)
	}

	// Вход: bcrypt старого пароля перехешируется в Argon2id.
	in, err := p.login(" 8 (7"+phone[3:5]+") "+phone[5:], "password")
	if err != nil {
		t.Fatal(err)
	}
	token := in.GetToken().GetAccessToken()
	if !strings.HasPrefix(token, "zgt_") || in.GetUser().GetUserId() != user || in.GetBalance().GetMainTiyn() != 200000 ||
		in.GetBalance().GetBonusTiyn() != 50000 || in.GetUser().GetMaskedPhone() != "+7 "+phone[2:5]+" ••• •• "+phone[10:] {
		t.Fatalf("sign in = %+v", in)
	}
	var hash string
	h.scan(&hash, "SELECT password FROM users WHERE id = $1", user)
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("password not rehashed: %.10s", hash)
	}
	ctx := withToken(token)

	tariffs, err := p.guest().ListTariffs(ctx, &agentv1.ListTariffsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tariffs.GetTariffs()) != 1 || tariffs.GetTariffs()[0].GetTariffId() != tariff || tariffs.GetTariffs()[0].GetPriceTiyn() != 100000 ||
		tariffs.GetTariffs()[0].GetMinutes() != 60 || !tariffs.GetTariffs()[0].GetMixedPayment() || tariffs.GetTariffs()[0].GetBonusMaxPercent() != 100 {
		t.Fatalf("tariffs = %+v", tariffs.GetTariffs())
	}

	// Покупка тарифа с основного; повтор с тем же ключом — тот же ответ, списание одно.
	k := key()
	buy := &agentv1.PurchaseTariffRequest{IdempotencyKey: k, TariffId: tariff, BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN}
	r1, err := p.guest().PurchaseTariff(ctx, buy)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := p.guest().PurchaseTariff(ctx, buy)
	if err != nil {
		t.Fatal(err)
	}
	if r1.GetPurchase().GetBalance().GetMainTiyn() != 100000 || r1.GetPurchase().GetChargedMainTiyn() != 100000 ||
		r1.GetPurchase().GetMinutesAdded() != 60 || r1.GetPurchase().GetSession().GetSessionId() == "" ||
		r2.GetPurchase().GetSession().GetSessionId() != r1.GetPurchase().GetSession().GetSessionId() || r1.GetPurchase().GetMessage() != "Тариф подключён." {
		t.Fatalf("purchase = %+v / %+v", r1.GetPurchase(), r2.GetPurchase())
	}
	h.wantBalances(user, 100000, 50000)
	// Журнал покупки (billing) — с IP соединения и версией агента из метаданных (RQ-10).
	var ip, ua string
	h.scan(&ip, "SELECT ip_address FROM activity_logs WHERE club_id = $1 AND event = 'tariff.purchased' AND user_id = $2", h.club, user)
	h.scan(&ua, "SELECT COALESCE(user_agent, '') FROM activity_logs WHERE club_id = $1 AND event = 'tariff.purchased' AND user_id = $2", h.club, user)
	if ip != "127.0.0.1" || ua != "ZafiroAgent/2.0.0" {
		t.Fatalf("activity ip/ua = %q %q", ip, ua)
	}
	// Тот же ключ, другой запрос — KEY_REUSED.
	_, err = p.guest().PurchaseTariff(ctx, &agentv1.PurchaseTariffRequest{IdempotencyKey: k, TariffId: tariff, BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_BONUS})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_IDEMPOTENCY_KEY_REUSED)
	// Без ключа — KEY_REQUIRED.
	_, err = p.guest().PurchaseTariff(ctx, &agentv1.PurchaseTariffRequest{TariffId: tariff})
	wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_IDEMPOTENCY_KEY_REQUIRED)

	prof, err := p.guest().GetProfile(ctx, &agentv1.GetProfileRequest{})
	if err != nil || prof.GetBalance().GetMainTiyn() != 100000 || prof.GetUser().GetName() != "Арман" {
		t.Fatalf("profile = %+v, %v", prof, err)
	}

	// Заказ в магазине: BalanceChanged приходит в стрим ПК, где вошёл гость (Status.signed_in_user_id).
	ord, err := p.shop().PlaceOrder(ctx, &agentv1.PlaceOrderRequest{IdempotencyKey: key(),
		Items: []*agentv1.OrderItem{{ProductId: cola, Quantity: 1}, {ProductId: cola, Quantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if ord.GetAmountTiyn() != 70000 || ord.GetBalance().GetMainTiyn() != 30000 || len(ord.GetLines()) != 1 || ord.GetLines()[0].GetQuantity() != 2 {
		t.Fatalf("order = %+v", ord)
	}
	bc := as.waitCommand(func(c *agentv1.Command) bool { return c.GetBalanceChanged() != nil })
	if bc.GetBalanceChanged().GetUserId() != user || bc.GetBalanceChanged().GetBalance().GetMainTiyn() != 30000 {
		t.Fatalf("balance changed = %+v", bc.GetBalanceChanged())
	}

	// Завершение через 30 минут: тариф без лимита использования не возвращается (BR-032), сессия снята.
	h.clock.advance(30 * time.Minute)
	sid := r1.GetPurchase().GetSession().GetSessionId()
	end, err := p.guest().EndSession(ctx, &agentv1.EndSessionRequest{IdempotencyKey: key(), SessionId: sid})
	if err != nil {
		t.Fatal(err)
	}
	if end.GetRefundedMainTiyn() != 0 || end.GetBalance().GetMainTiyn() != 30000 {
		t.Fatalf("end = %+v", end)
	}
	_, err = p.guest().EndSession(ctx, &agentv1.EndSessionRequest{IdempotencyKey: key(), SessionId: sid})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SESSION_MISMATCH)
	h.noDrift()

	// Выход: токен больше не принимается.
	if _, err := p.authc().Logout(ctx, &agentv1.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	_, err = p.guest().GetProfile(ctx, &agentv1.GetProfileRequest{})
	wantReason(t, err, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_GUEST_TOKEN_INVALID)
}

// GS-053: «игра на весь баланс» — агент узнаёт настройку, продаются минуты, которые оплачивает баланс
// (pricing.AffordableMinutes), через 60 минут завершение возвращает 1400.00. GS-055: автобиллинг и пауза.
func TestGS053WholeBalanceAndGS055AutoBilling(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	phone := uniqPhone()
	user := h.customer("Дана", phone, "password", 200000, 0)
	p := h.enrollPC()
	ctx := withToken(p.mustLogin(phone, "password"))

	off, err := p.guest().GetMinuteOffer(ctx, &agentv1.GetMinuteOfferRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if off.GetWholeBalance() || off.GetAutoBilling() || off.GetAutoBillingPauseMinutes() != 5 || !off.GetAvailable() ||
		off.GetPricePerHourTiyn() != 60000 || off.GetMaxMinutes() != 720 || off.GetZoneName() != "Hall" {
		t.Fatalf("GS-055 defaults: %+v", off)
	}
	h.exec(`UPDATE clubs SET settings = '{"finances": {"allow_whole_balance": true, "auto_billing": true, "auto_billing_pause_minutes": 3}}' WHERE id = $1`, h.club)
	on, err := p.guest().GetMinuteOffer(ctx, &agentv1.GetMinuteOfferRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !on.GetWholeBalance() || !on.GetAutoBilling() || on.GetAutoBillingPauseMinutes() != 3 {
		t.Fatalf("GS-053/055: %+v", on)
	}
	// Агент считает минуты на весь баланс так же, как pricing.WholeBalanceMinutes (Q-008).
	minutes := pricing.WholeBalanceMinutes(pricing.MinuteOffer{Available: on.GetAvailable(), PricePerHour: money.Tiyn(on.GetPricePerHourTiyn()),
		MaxMinutes: int64(on.GetMaxMinutes()), WholeBalance: on.GetWholeBalance()}, 200000)
	if minutes != 200 {
		t.Fatalf("whole balance minutes = %d", minutes)
	}
	buy, err := p.guest().PurchaseMinutes(ctx, &agentv1.PurchaseMinutesRequest{IdempotencyKey: key(), Minutes: int32(minutes),
		BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN})
	if err != nil {
		t.Fatal(err)
	}
	if buy.GetPurchase().GetBalance().GetMainTiyn() != 0 || buy.GetPurchase().GetMinutesAdded() != 200 || buy.GetPurchase().GetMessage() != "Время добавлено." {
		t.Fatalf("buy = %+v", buy.GetPurchase())
	}
	h.clock.advance(60 * time.Minute)
	end, err := p.guest().EndSession(ctx, &agentv1.EndSessionRequest{IdempotencyKey: key(), SessionId: buy.GetPurchase().GetSession().GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	if end.GetRefundedMainTiyn() != 140000 || end.GetBalance().GetMainTiyn() != 140000 {
		t.Fatalf("end = %+v", end)
	}
	h.wantBalances(user, 140000, 0)
	h.noDrift()
}

// GS-040: минуты не продаются вне 1…720, в зоне без цены часа и на чужой сессии; баланс не меняется.
func TestGS040MinutesRejections(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	phone, other := uniqPhone(), uniqPhone()
	user := h.customer("Арман", phone, "password", 1000000, 0)
	h.customer("Другой", other, "password", 1000000, 0)
	p := h.enrollPC()
	ctx := withToken(p.mustLogin(phone, "password"))
	for _, m := range []int32{0, 721} {
		_, err := p.guest().PurchaseMinutes(ctx, &agentv1.PurchaseMinutesRequest{IdempotencyKey: key(), Minutes: m})
		wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
	}
	// Чужая сессия на ПК.
	p2 := h.enrollPC()
	ctx2 := withToken(p2.mustLogin(other, "password"))
	if _, err := p2.guest().PurchaseMinutes(ctx2, &agentv1.PurchaseMinutesRequest{IdempotencyKey: key(), Minutes: 30}); err != nil {
		t.Fatal(err)
	}
	h.exec("UPDATE club_computers SET current_session_id = (SELECT current_session_id FROM club_computers WHERE id = $2), current_session_user_id = (SELECT current_session_user_id FROM club_computers WHERE id = $2), session_ends_at = (SELECT session_ends_at FROM club_computers WHERE id = $2) WHERE id = $1", p.id, p2.id)
	_, err := p.guest().PurchaseMinutes(ctx, &agentv1.PurchaseMinutesRequest{IdempotencyKey: key(), Minutes: 30})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SEAT_UNAVAILABLE)
	// Зона без цены часа.
	plain := h.int("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Plain', now(), now()) RETURNING id", h.club)
	h.exec("UPDATE club_computers SET club_zone_id = $2, current_session_id = NULL, current_session_user_id = NULL, session_ends_at = NULL WHERE id = $1", p.id, plain)
	off, err := p.guest().GetMinuteOffer(ctx, &agentv1.GetMinuteOfferRequest{})
	if err != nil || off.GetAvailable() {
		t.Fatalf("offer = %+v, %v", off, err)
	}
	_, err = p.guest().PurchaseMinutes(ctx, &agentv1.PurchaseMinutesRequest{IdempotencyKey: key(), Minutes: 30})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_TARIFF_UNAVAILABLE)
	if st, _ := status.FromError(err); st.Message() != "В этой зоне время поминутно не продаётся." {
		t.Fatalf("message = %q", st.Message())
	}
	h.wantBalances(user, 1000000, 0)
}

// GS-081: клиент вошёл на одном ПК — на другом нельзя, пока токен там живой (2 минуты) или ПК сообщает его
// в стриме; сотрудников правило не касается. Токен другого ПК и испорченный токен — GUEST_TOKEN_INVALID.
func TestGS081OnePCAtATimeAndTokens(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	phone, staff := uniqPhone(), uniqPhone()
	h.customer("Клиент", phone, "password", 0, 0)
	h.user("Оператор", staff, "password", "operator")
	pc1, pc2 := h.enrollPC(), h.enrollPC()
	tok1 := pc1.mustLogin(phone, "password")

	_, err := pc2.login(phone, "password")
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SIGNED_IN_ELSEWHERE)
	if st, _ := status.FromError(err); !strings.HasPrefix(st.Message(), "Вы уже вошли на другом ПК (") {
		t.Fatalf("message = %q", st.Message())
	}
	// Повторный вход на том же ПК — можно (прежний токен ПК отзывается).
	tok1b := pc1.mustLogin(phone, "password")
	_, err = pc1.guest().GetProfile(withToken(tok1), &agentv1.GetProfileRequest{})
	wantReason(t, err, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_GUEST_TOKEN_INVALID)
	// Токен ПК1 на ПК2 не принимается.
	_, err = pc2.guest().GetProfile(withToken(tok1b), &agentv1.GetProfileRequest{})
	wantReason(t, err, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_GUEST_TOKEN_INVALID)
	_, err = pc2.guest().GetProfile(withToken("zgt_garbage"), &agentv1.GetProfileRequest{})
	wantReason(t, err, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_GUEST_TOKEN_INVALID)
	_, err = pc2.guest().GetProfile(context.Background(), &agentv1.GetProfileRequest{})
	wantReason(t, err, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_GUEST_TOKEN_INVALID)

	// Через 5 минут без активности токена ПК1 (и ПК1 не в сети) — вход на ПК2 разрешён.
	h.clock.advance(5 * time.Minute)
	pc2.mustLogin(phone, "password")
	// Оператор с активным токеном на ПК1 входит на ПК2.
	pc1b := h.enrollPC()
	pc1b.mustLogin(staff, "password")
	h.enrollPC().mustLogin(staff, "password")
}

// Неверные данные входа, блокировка, не член клуба, неактивная подписка.
func TestLoginRejections(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	phone, blocked := uniqPhone(), uniqPhone()
	h.customer("Клиент", phone, "password", 0, 0)
	b := h.customer("Заблокирован", blocked, "password", 0, 0)
	h.exec("UPDATE users SET blocked_at = now() WHERE id = $1", b)
	p := h.enrollPC()
	_, err := p.login(phone, "wrong")
	wantReason(t, err, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_INVALID_CREDENTIALS)
	_, err = p.login("+70000000001", "password")
	wantReason(t, err, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_INVALID_CREDENTIALS)
	_, err = p.login(blocked, "password")
	wantReason(t, err, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_ACCOUNT_BLOCKED)

	// Клиент другого клуба.
	other := newHarness(t, harnessOpts{noLimits: true})
	otherPhone := uniqPhone()
	other.customer("Чужой", otherPhone, "password", 0, 0)
	_, err = p.login(otherPhone, "password")
	wantReason(t, err, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_NOT_CLUB_MEMBER)

	h.exec("UPDATE club_subscriptions SET status = 'expired' WHERE club_id = $1", h.club)
	_, err = p.login(phone, "password")
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SUBSCRIPTION_INACTIVE)
}

// Регистрация на ПК: новый аккаунт; аккаунт другого клуба вступает со своим паролем; неверный пароль;
// уже клиент; проверки полей.
func TestRegister(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	p := h.enrollPC()
	phone := uniqPhone()
	r, err := p.authc().Register(context.Background(), &agentv1.RegisterRequest{Name: " Новый ", Phone: "8" + phone[2:], Password: "secret1"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.GetAccountCreated() || r.GetSignIn().GetUser().GetName() != "Новый" || r.GetSignIn().GetBalance().GetMainTiyn() != 0 {
		t.Fatalf("register = %+v", r)
	}
	var role string
	h.scan(&role, "SELECT role FROM club_memberships WHERE club_id = $1 AND user_id = $2", h.club, r.GetSignIn().GetUser().GetUserId())
	if role != "client" {
		t.Fatalf("role = %q", role)
	}
	_, err = p.authc().Register(context.Background(), &agentv1.RegisterRequest{Name: "Новый", Phone: phone, Password: "secret1"})
	wantReason(t, err, codes.AlreadyExists, agentv1.ErrorCode_ERROR_CODE_ALREADY_CLUB_MEMBER)

	other := newHarness(t, harnessOpts{noLimits: true})
	foreign := uniqPhone()
	fid := other.customer("Чужой", foreign, "password", 0, 0)
	_, err = p.authc().Register(context.Background(), &agentv1.RegisterRequest{Name: "Чужой", Phone: foreign, Password: "wrongpass"})
	wantReason(t, err, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_WRONG_PASSWORD)
	j, err := p.authc().Register(context.Background(), &agentv1.RegisterRequest{Name: "Другое имя", Phone: foreign, Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	if j.GetAccountCreated() || j.GetSignIn().GetUser().GetUserId() != fid || j.GetSignIn().GetUser().GetName() != "Чужой" {
		t.Fatalf("join = %+v", j)
	}
	for _, c := range []struct {
		req   *agentv1.RegisterRequest
		field string
	}{
		{&agentv1.RegisterRequest{Name: "А", Phone: uniqPhone(), Password: "secret1"}, "name"},
		{&agentv1.RegisterRequest{Name: "Имя", Phone: "123", Password: "secret1"}, "phone"},
		{&agentv1.RegisterRequest{Name: "Имя", Phone: uniqPhone(), Password: "12345"}, "password"},
	} {
		_, err := p.authc().Register(context.Background(), c.req)
		wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
		if f := fieldOf(err); f != c.field {
			t.Fatalf("field = %q, want %q", f, c.field)
		}
	}
}

func fieldOf(err error) string {
	st, _ := status.FromError(err)
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok && len(br.GetFieldViolations()) > 0 {
			return br.GetFieldViolations()[0].GetField()
		}
	}
	return ""
}

// QR-вход: выключен в клубе → FEATURE_DISABLED; включён — PENDING, подтверждение с телефона (серверная часть)
// будит ожидание → APPROVED с токеном; повторное ожидание — EXPIRED (код одноразовый).
func TestQrLogin(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	phone := uniqPhone()
	user := h.customer("Арман", phone, "password", 0, 0)
	p := h.enrollPC()
	_, err := p.authc().CreateQrLogin(context.Background(), &agentv1.CreateQrLoginRequest{})
	wantReason(t, err, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_FEATURE_DISABLED)
	h.exec(`UPDATE clubs SET settings = '{"guests": {"show_qr_code_on_login": true}}' WHERE id = $1`, h.club)
	qr, err := p.authc().CreateQrLogin(context.Background(), &agentv1.CreateQrLoginRequest{})
	if err != nil {
		t.Fatal(err)
	}
	code := strings.TrimPrefix(qr.GetUrl(), "https://zafiro.test/pc-login/")
	if len(code) != 40 || qr.GetExpiresAt().AsTime().Sub(h.clock.Now()) != 5*time.Minute {
		t.Fatalf("qr = %+v", qr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w, err := p.authc().WatchQrLogin(ctx, &agentv1.WatchQrLoginRequest{LoginId: qr.GetLoginId()})
	if err != nil {
		t.Fatal(err)
	}
	first, err := w.Recv()
	if err != nil || first.GetStatus() != agentv1.QrLoginStatus_QR_LOGIN_STATUS_PENDING {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if err := h.srv.ApproveQrLogin(context.Background(), h.club, "wrong-code", user); status.Code(err) != codes.NotFound {
		t.Fatalf("approve wrong code: %v", err)
	}
	if err := h.srv.ApproveQrLogin(context.Background(), h.club, code, user); err != nil {
		t.Fatal(err)
	}
	ok, err := w.Recv()
	if err != nil || ok.GetStatus() != agentv1.QrLoginStatus_QR_LOGIN_STATUS_APPROVED || ok.GetSignIn().GetUser().GetUserId() != user {
		t.Fatalf("approved = %+v, %v", ok, err)
	}
	if _, err := p.guest().GetProfile(withToken(ok.GetSignIn().GetToken().GetAccessToken()), &agentv1.GetProfileRequest{}); err != nil {
		t.Fatal(err)
	}
	w2, err := p.authc().WatchQrLogin(ctx, &agentv1.WatchQrLoginRequest{LoginId: qr.GetLoginId()})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := w2.Recv(); err != nil || again.GetStatus() != agentv1.QrLoginStatus_QR_LOGIN_STATUS_EXPIRED {
		t.Fatalf("again = %+v, %v", again, err)
	}
	if n := h.int("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'auth.qr_login' AND user_id = $2", h.club, user); n != 1 {
		t.Fatalf("auth.qr_login entries = %d", n)
	}
}

// GS-072/073/074 через gRPC: оплата с основного, отказы без списания, магазин выключен в клубе; витрина без токена.
func TestShopOverGRPC(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	phone := uniqPhone()
	user := h.customer("Арман", phone, "password", 100000, 30000)
	cola := h.int(`INSERT INTO products (club_id, sku, name, category, price, quantity, created_at, updated_at) VALUES ($1, 'COLA', 'Cola', 'Напитки', 35000, 10, now(), now()) RETURNING id`, h.club)
	chips := h.int(`INSERT INTO products (club_id, sku, name, category, price, quantity, created_at, updated_at) VALUES ($1, 'CHIPS', 'Чипсы', 'Снеки', 25050, 1, now(), now()) RETURNING id`, h.club)
	p := h.enrollPC()
	list, err := p.shop().ListProducts(context.Background(), &agentv1.ListProductsRequest{})
	if err != nil || len(list.GetProducts()) != 2 || list.GetProducts()[0].GetName() != "Cola" || list.GetProducts()[1].GetPriceTiyn() != 25050 {
		t.Fatalf("products = %+v, %v", list, err)
	}
	ctx := withToken(p.mustLogin(phone, "password"))
	_, err = p.shop().PlaceOrder(ctx, &agentv1.PlaceOrderRequest{IdempotencyKey: key(), Items: []*agentv1.OrderItem{{ProductId: chips, Quantity: 2}}})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_OUT_OF_STOCK)
	_, err = p.shop().PlaceOrder(ctx, &agentv1.PlaceOrderRequest{IdempotencyKey: key(), Items: []*agentv1.OrderItem{{ProductId: cola, Quantity: 3}}})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_INSUFFICIENT_FUNDS)
	_, err = p.shop().PlaceOrder(ctx, &agentv1.PlaceOrderRequest{IdempotencyKey: key(), Items: []*agentv1.OrderItem{{ProductId: cola, Quantity: 51}}})
	wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
	h.wantBalances(user, 100000, 30000)
	ord, err := p.shop().PlaceOrder(ctx, &agentv1.PlaceOrderRequest{IdempotencyKey: key(),
		Items: []*agentv1.OrderItem{{ProductId: cola, Quantity: 2}, {ProductId: chips, Quantity: 1}}})
	if err != nil || ord.GetAmountTiyn() != 95050 || ord.GetBalance().GetMainTiyn() != 4950 || ord.GetBalance().GetBonusTiyn() != 30000 {
		t.Fatalf("order = %+v, %v", ord, err)
	}
	if n := h.int("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'shop.purchased' AND details->>'amount' = '950.50'", h.club); n != 1 {
		t.Fatalf("shop.purchased = %d", n)
	}
	hist, err := p.guest().ListHistory(ctx, &agentv1.ListHistoryRequest{Filter: agentv1.HistoryFilter_HISTORY_FILTER_PURCHASES})
	if err != nil || len(hist.GetEntries()) != 1 || hist.GetEntries()[0].GetMainDeltaTiyn() != -95050 || hist.GetEntries()[0].GetTitle() != "Заказ в магазине клуба" {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	h.exec(`UPDATE clubs SET settings = '{"club": {"agent_shop_enabled": "0"}}' WHERE id = $1`, h.club)
	_, err = p.shop().PlaceOrder(ctx, &agentv1.PlaceOrderRequest{IdempotencyKey: key(), Items: []*agentv1.OrderItem{{ProductId: cola, Quantity: 1}}})
	wantReason(t, err, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_FEATURE_DISABLED)
	h.noDrift()
}

// GS-075: перевод другу только с паролем аккаунта; заявка в друзья и принятие.
func TestGS075FriendTransfer(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: `{"guests": {"allow_adding_friends": true, "allow_balance_transfer_to_friends": true}}`})
	arman, dana := uniqPhone(), uniqPhone()
	a := h.customer("Арман", arman, "password", 10000, 0)
	d := h.customer("Дана", dana, "password", 0, 0)
	pa, pd := h.enrollPC(), h.enrollPC()
	ca, cd := withToken(pa.mustLogin(arman, "password")), withToken(pd.mustLogin(dana, "password"))
	add, err := pa.social().AddFriend(ca, &agentv1.AddFriendRequest{Phone: dana})
	if err != nil || add.GetAccepted() || len(add.GetFriends().GetOutgoing()) != 1 {
		t.Fatalf("add = %+v, %v", add, err)
	}
	fs, err := pd.social().ListFriends(cd, &agentv1.ListFriendsRequest{})
	if err != nil || len(fs.GetIncoming()) != 1 {
		t.Fatalf("incoming = %+v, %v", fs, err)
	}
	fid := fs.GetIncoming()[0].GetFriendshipId()
	if _, err := pd.social().AcceptFriend(cd, &agentv1.AcceptFriendRequest{FriendshipId: fid}); err != nil {
		t.Fatal(err)
	}
	_, err = pa.social().TransferBalance(ca, &agentv1.TransferBalanceRequest{IdempotencyKey: key(), FriendshipId: fid, AmountTiyn: 1000})
	wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
	_, err = pa.social().TransferBalance(ca, &agentv1.TransferBalanceRequest{IdempotencyKey: key(), FriendshipId: fid, AmountTiyn: 1000,
		Confirmation: &agentv1.TransferBalanceRequest_Password{Password: "wrong"}})
	wantReason(t, err, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_WRONG_PASSWORD)
	h.wantBalances(a, 10000, 0)
	k := key()
	tr := &agentv1.TransferBalanceRequest{IdempotencyKey: k, FriendshipId: fid, AmountTiyn: 1000,
		Confirmation: &agentv1.TransferBalanceRequest_Password{Password: "password"}}
	res, err := pa.social().TransferBalance(ca, tr)
	if err != nil || res.GetBalance().GetMainTiyn() != 9000 || res.GetAmountTiyn() != 1000 {
		t.Fatalf("transfer = %+v, %v", res, err)
	}
	if _, err := pa.social().TransferBalance(ca, tr); err != nil { // повтор с тем же ключом — без второго списания
		t.Fatal(err)
	}
	h.wantBalances(a, 9000, 0)
	h.wantBalances(d, 1000, 0)
	_, err = pa.social().TransferBalance(ca, &agentv1.TransferBalanceRequest{IdempotencyKey: key(), FriendshipId: fid, AmountTiyn: 100_000_001,
		Confirmation: &agentv1.TransferBalanceRequest_Password{Password: "password"}})
	wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_TRANSFER_LIMIT)
	hist, err := pd.guest().ListHistory(cd, &agentv1.ListHistoryRequest{Filter: agentv1.HistoryFilter_HISTORY_FILTER_TRANSFERS})
	if err != nil || len(hist.GetEntries()) != 1 || hist.GetEntries()[0].GetMainDeltaTiyn() != 1000 || hist.GetEntries()[0].GetTitle() != "Перевод от друга Арман" {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	_, err = pa.social().RequestTransferCode(ca, &agentv1.RequestTransferCodeRequest{FriendshipId: fid, AmountTiyn: 100})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("transfer code: %v", err)
	}
	h.noDrift()
}

// Переводы агента из БД: версия, not_modified по known_version, выключенный язык — NOT_FOUND.
func TestTranslations(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	code := "t" + strings.ToLower(uuid.NewString()[:6])
	code = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r
		}
		return 'q'
	}, code)
	lang := h.int("INSERT INTO languages (code, name, is_enabled, created_at, updated_at) VALUES ($1, 'Тест', true, now(), now()) RETURNING id", code)
	h.exec(`INSERT INTO translations (language_id, "group", key, key_hash, value, created_at, updated_at)
		VALUES ($1, 'agent', 'Войти', 'h1', 'Кіру', now(), now()), ($1, '*', 'Другое', 'h2', 'x', now(), now())`, lang)
	p := h.enrollPC()
	r, err := p.agent().GetTranslations(context.Background(), &agentv1.GetTranslationsRequest{Locale: code})
	if err != nil || r.GetLines()["Войти"] != "Кіру" || len(r.GetLines()) != 1 || r.GetVersion() == 0 {
		t.Fatalf("translations = %+v, %v", r, err)
	}
	again, err := p.agent().GetTranslations(context.Background(), &agentv1.GetTranslationsRequest{Locale: code, KnownVersion: r.GetVersion()})
	if err != nil || !again.GetNotModified() || len(again.GetLines()) != 0 {
		t.Fatalf("not modified = %+v, %v", again, err)
	}
	as := p.connect("boot-t", 0)
	if as.welcome.GetTranslationsVersion() < r.GetVersion() {
		t.Fatalf("welcome translations_version = %d", as.welcome.GetTranslationsVersion())
	}
	h.exec("UPDATE languages SET is_enabled = false WHERE id = $1", lang)
	_, err = p.agent().GetTranslations(context.Background(), &agentv1.GetTranslationsRequest{Locale: code})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("disabled language: %v", err)
	}
	_, err = p.agent().GetTranslations(context.Background(), &agentv1.GetTranslationsRequest{Locale: "../x"})
	wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
}

// PIN выхода: не задан, неверный, верный (техрежим на agent_staff_minutes), лимит agent-exit-pin 5 за 5 минут.
func TestVerifyStaffPin(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	phone := uniqPhone()
	op := h.user("Оператор", phone, "password", "operator")
	p := h.enrollPC()
	_, err := p.agent().VerifyStaffPin(context.Background(), &agentv1.VerifyStaffPinRequest{Pin: "1234"})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_PIN_NOT_CONFIGURED)
	pin, _ := bcrypt.GenerateFromPassword([]byte("4321"), 4)
	h.exec("UPDATE clubs SET agent_exit_pin_hash = $2, agent_staff_minutes = 20 WHERE id = $1", h.club, string(pin))
	ctx := withToken(p.mustLogin(phone, "password"))
	_, err = p.agent().VerifyStaffPin(ctx, &agentv1.VerifyStaffPinRequest{Pin: "0000"})
	wantReason(t, err, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_INVALID_PIN)
	ok, err := p.agent().VerifyStaffPin(ctx, &agentv1.VerifyStaffPinRequest{Pin: "4321"})
	if err != nil || ok.GetStaffMinutes() != 20 || ok.GetUntil().AsTime().Sub(h.clock.Now()) != 20*time.Minute {
		t.Fatalf("pin = %+v, %v", ok, err)
	}
	var kind string
	var staff int64
	h.scan(&kind, "SELECT staff_kind FROM club_computers WHERE id = $1", p.id)
	h.scan(&staff, "SELECT staff_user_id FROM club_computers WHERE id = $1", p.id)
	if kind != "pin" || staff != op {
		t.Fatalf("staff visit = %s %d", kind, staff)
	}
	if n := h.int("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event IN ('agent.staff_exit', 'agent.exit_pin_failed')", h.club); n != 2 {
		t.Fatalf("pin activity = %d", n)
	}
	// Уже 3 попытки; ещё 2 — и шестая отклоняется лимитом с RetryInfo.
	for i := 0; i < 2; i++ {
		_, _ = p.agent().VerifyStaffPin(context.Background(), &agentv1.VerifyStaffPinRequest{Pin: "0000"})
	}
	_, err = p.agent().VerifyStaffPin(context.Background(), &agentv1.VerifyStaffPinRequest{Pin: "4321"})
	wantReason(t, err, codes.ResourceExhausted, agentv1.ErrorCode_ERROR_CODE_RATE_LIMITED)
	st, _ := status.FromError(err)
	var retry bool
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok && ri.GetRetryDelay().AsDuration() > 0 {
			retry = true
		}
	}
	if !retry {
		t.Fatal("no RetryInfo")
	}
}

// Лимит customer-login: 5 попыток в минуту на (IP, телефон).
func TestLoginRateLimit(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	phone := uniqPhone()
	h.customer("Клиент", phone, "password", 0, 0)
	p := h.enrollPC()
	for i := 0; i < 5; i++ {
		_, err := p.login(phone, "wrong")
		wantReason(t, err, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_INVALID_CREDENTIALS)
	}
	_, err := p.login(phone, "password")
	wantReason(t, err, codes.ResourceExhausted, agentv1.ErrorCode_ERROR_CODE_RATE_LIMITED)
	// Другой телефон с того же ПК — свой счётчик.
	_, err = p.login(uniqPhone(), "x")
	wantReason(t, err, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_INVALID_CREDENTIALS)
}

// Чужой клуб: ПК клуба B не видит тариф и пакеты клуба A (RLS + фильтр club_id), токен клуба A на ПК B не принимается.
func TestOtherClubIsolation(t *testing.T) {
	a := newHarness(t, harnessOpts{noLimits: true})
	b := newHarness(t, harnessOpts{noLimits: true})
	phoneA, phoneB := uniqPhone(), uniqPhone()
	a.customer("A", phoneA, "password", 500000, 0)
	userB := b.customer("B", phoneB, "password", 500000, 0)
	tariffA := a.int(`INSERT INTO tariffs (club_id, name, duration_minutes, price, payment_method, is_active, created_at, updated_at)
		VALUES ($1, 'Час', 60, 1000, 'main_or_bonus', true, now(), now()) RETURNING id`, a.club)
	pa := a.enrollPC()
	tokA := pa.mustLogin(phoneA, "password")
	pb := b.enrollPC()
	_, err := pb.guest().GetProfile(withToken(tokA), &agentv1.GetProfileRequest{})
	wantReason(t, err, codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_GUEST_TOKEN_INVALID)
	ctxB := withToken(pb.mustLogin(phoneB, "password"))
	tariffs, err := pb.guest().ListTariffs(ctxB, &agentv1.ListTariffsRequest{})
	if err != nil || len(tariffs.GetTariffs()) != 0 {
		t.Fatalf("tariffs of B = %+v, %v", tariffs, err)
	}
	_, err = pb.guest().PurchaseTariff(ctxB, &agentv1.PurchaseTariffRequest{IdempotencyKey: key(), TariffId: tariffA})
	wantReason(t, err, codes.NotFound, agentv1.ErrorCode_ERROR_CODE_TARIFF_UNAVAILABLE)
	b.wantBalances(userB, 500000, 0)
}

// GS-077: свежая загрузка ПК сбрасывает гостевую сессию с кассы, платную сессию клиента — нет.
func TestGS077FreshBootResetsGuestSessionOnly(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	p := h.enrollPC()
	as := p.connect("boot-a", 0)
	as.cancel()
	h.exec("UPDATE club_computers SET current_session_id = 'guest-1', session_guest = true, session_ends_at = $2 WHERE id = $1", p.id, h.clock.Now().Add(time.Hour))
	// Тот же boot_id — не свежая загрузка.
	same := p.connect("boot-a", 0)
	if !same.welcome.GetSession().GetGuest() {
		t.Fatalf("session = %+v", same.welcome.GetSession())
	}
	same.cancel()
	fresh := p.connect("boot-b", 0)
	if fresh.welcome.GetSession() != nil {
		t.Fatalf("guest session not reset: %+v", fresh.welcome.GetSession())
	}
	if n := h.int("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'computer.guest_session_reset'", h.club); n != 1 {
		t.Fatalf("reset entries = %d", n)
	}
	fresh.cancel()
	phone := uniqPhone()
	user := h.customer("Клиент", phone, "password", 0, 0)
	h.exec("UPDATE club_computers SET current_session_id = 'paid-1', current_session_user_id = $3, session_guest = false, session_ends_at = $2 WHERE id = $1",
		p.id, h.clock.Now().Add(time.Hour), user)
	paid := p.connect("boot-c", 0)
	if paid.welcome.GetSession().GetSessionId() != "paid-1" {
		t.Fatalf("paid session = %+v", paid.welcome.GetSession())
	}
}

// Событие агента SessionEnded(GUEST) завершает сессию владельца с расчётом; GuestSignedOut отзывает токен ПК.
func TestAgentEvents(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	phone := uniqPhone()
	user := h.customer("Клиент", phone, "password", 100000, 0)
	p := h.enrollPC()
	tok := p.mustLogin(phone, "password")
	buy, err := p.guest().PurchaseMinutes(withToken(tok), &agentv1.PurchaseMinutesRequest{IdempotencyKey: key(), Minutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	h.wantBalances(user, 40000, 0)
	as := p.connect("boot-e", user)
	send := func(ev *agentv1.AgentEvent) {
		if err := as.st.Send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Event{Event: ev}}); err != nil {
			t.Fatal(err)
		}
	}
	send(&agentv1.AgentEvent{EventId: uuid.NewString(), OccurredAt: nowPB(), Kind: &agentv1.AgentEvent_SessionEnded{
		SessionEnded: &agentv1.SessionEnded{SessionId: buy.GetPurchase().GetSession().GetSessionId(), Reason: agentv1.SessionEndReason_SESSION_END_REASON_GUEST}}})
	send(&agentv1.AgentEvent{EventId: uuid.NewString(), OccurredAt: nowPB(), Kind: &agentv1.AgentEvent_GuestSignedOut{
		GuestSignedOut: &agentv1.GuestSignedOut{UserId: user, Reason: agentv1.SignOutReason_SIGN_OUT_REASON_USER}}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		m, _ := h.balances(user)
		_, perr := p.guest().GetProfile(withToken(tok), &agentv1.GetProfileRequest{})
		if m == 100000 && status.Code(perr) == codes.Unauthenticated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events not applied: main = %s, profile err = %v", m, perr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.noDrift()
}
