//go:build integration

package guestapi_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Настройки клуба GS-053/GS-055: «на весь баланс» и автобиллинг с паузой 3 минуты.
const wholeAndAutoSettings = `{"finances": {"allow_whole_balance": true, "auto_billing": true, "auto_billing_pause_minutes": 3}}`

// AG-12, GS-053: «на весь баланс» считает сервер — GetMinuteOffer показывает минуты и цену, PlayWholeBalance покупает
// их с основного баланса (числа те же, что в pricing.TestWholeBalanceMinutes: 2000 ₸ при 600 ₸/час — 200 минут);
// повтор ключа не списывает второй раз; выключено в клубе — FEATURE_DISABLED; денег нет — INSUFFICIENT_FUNDS.
func TestAG12PlayWholeBalance(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings})
	phone := uniqPhone()
	user := h.customer("Дана", phone, "password", 200000, 5000)
	p := h.enrollPC()
	ctx := withToken(p.mustLogin(phone, "password"))

	off, err := p.guest().GetMinuteOffer(ctx, &agentv1.GetMinuteOfferRequest{})
	if err != nil {
		t.Fatal(err)
	}
	want := pricing.WholeBalanceMinutes(pricing.MinuteOffer{Available: true, PricePerHour: 60000, MaxMinutes: 720, WholeBalance: true}, 200000)
	if want != 200 || off.GetWholeBalanceMinutes() != int32(want) || off.GetWholeBalancePriceTiyn() != 200000 {
		t.Fatalf("GS-053 offer: minutes %d price %d (pricing %d)", off.GetWholeBalanceMinutes(), off.GetWholeBalancePriceTiyn(), want)
	}
	k := key()
	start := h.clock.Now()
	buy, err := p.guest().PlayWholeBalance(ctx, &agentv1.PlayWholeBalanceRequest{IdempotencyKey: k})
	if err != nil {
		t.Fatal(err)
	}
	pur := buy.GetPurchase()
	if pur.GetMinutesAdded() != 200 || pur.GetChargedMainTiyn() != 200000 || pur.GetChargedBonusTiyn() != 0 ||
		!pur.GetSession().GetEndsAt().AsTime().Equal(start.Add(200*time.Minute)) || pur.GetBalance().GetMainTiyn() != 0 ||
		pur.GetSession().GetTariffName() != "Hall" {
		t.Fatalf("GS-053 purchase = %+v", pur)
	}
	again, err := p.guest().PlayWholeBalance(ctx, &agentv1.PlayWholeBalanceRequest{IdempotencyKey: k})
	if err != nil || again.GetPurchase().GetSession().GetSessionId() != pur.GetSession().GetSessionId() {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	h.wantBalances(user, 0, 5000)
	if n := h.int("SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'minutes_purchase'", h.club); n != 1 {
		t.Fatalf("minutes purchases = %d", n)
	}
	// Денег на минуту нет — INSUFFICIENT_FUNDS, кнопку агент не показывает.
	off, err = p.guest().GetMinuteOffer(ctx, &agentv1.GetMinuteOfferRequest{})
	if err != nil || off.GetWholeBalanceMinutes() != 0 {
		t.Fatalf("offer after = %+v, %v", off, err)
	}
	_, err = p.guest().PlayWholeBalance(ctx, &agentv1.PlayWholeBalanceRequest{IdempotencyKey: key()})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_INSUFFICIENT_FUNDS)

	// Клуб не разрешил игру на весь баланс.
	h.exec(`UPDATE clubs SET settings = '{"finances": {"auto_billing": true}}' WHERE id = $1`, h.club)
	_, err = p.guest().PlayWholeBalance(ctx, &agentv1.PlayWholeBalanceRequest{IdempotencyKey: key()})
	wantReason(t, err, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_FEATURE_DISABLED)
	h.noDrift()
}

// tick — один проход автобиллинга (то, что делает worker.AutoBilling у лидера).
func (h *harness) tick() {
	h.t.Helper()
	if _, err := h.srv.TickAutoBilling(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) autoBilling(ctx context.Context, p *pc) *agentv1.AutoBillingStatus {
	h.t.Helper()
	r, err := p.guest().GetAutoBilling(ctx, &agentv1.GetAutoBillingRequest{})
	if err != nil {
		h.t.Fatal(err)
	}
	return r.GetStatus()
}

// AG-12, GS-055: автобиллинг на сервере — пауза после входа, покупка на весь баланс (числа как в
// pricing.TestAutoBilling: 1400 ₸ при 600 ₸/час — 140 минут), после конца тарифа — сразу, без паузы; денег нет —
// не покупает; гость сам завершил сессию — стоп до нового входа; агенту — AutoBillingChanged.
func TestAG12AutoBilling(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings})
	h.billing.SetNotifier(h.srv.WrapNotifier(h.stream.Notifier()))
	phone := uniqPhone()
	user := h.customer("Арман", phone, "password", 140000, 0)
	p := h.enrollPC()
	ctx := withToken(p.mustLogin(phone, "password"))
	as := p.connect("boot-ab", user)
	t0 := h.clock.Now()

	h.tick()
	st := h.autoBilling(ctx, p)
	if !st.GetEnabled() || !st.GetWaiting() || st.GetStopped() || !st.GetPauseEndsAt().AsTime().Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("GS-055 pause: %+v", st)
	}
	c := as.waitCommand(func(c *agentv1.Command) bool { return c.GetAutoBillingChanged() != nil })
	if c.GetAutoBillingChanged().GetUserId() != user || !c.GetAutoBillingChanged().GetStatus().GetWaiting() {
		t.Fatalf("AutoBillingChanged = %+v", c)
	}
	// Пауза идёт — не покупает.
	h.clock.advance(2*time.Minute + 59*time.Second)
	h.tick()
	h.wantBalances(user, 140000, 0)
	// Пауза кончилась — 140 минут на весь баланс; повторный тик в тот же момент второй покупки не делает.
	h.clock.advance(time.Second)
	h.tick()
	h.tick()
	h.wantBalances(user, 0, 0)
	sid, ends := h.pcSession(p.id)
	if sid == "" || !ends.Equal(t0.Add(3*time.Minute+140*time.Minute)) {
		t.Fatalf("session = %q until %v", sid, ends)
	}
	as.waitCommand(func(c *agentv1.Command) bool {
		return c.GetStartSession().GetSession().GetSessionId() == sid && c.GetStartSession().GetSession().GetTariffName() == "Hall"
	})
	if st := h.autoBilling(ctx, p); st.GetWaiting() || st.GetStopped() {
		t.Fatalf("in session: %+v", st)
	}

	// Время тарифа вышло, пополнили на 300 ₸ — сразу 30 минут, без паузы (autoAfterTariff).
	h.clock.advance(140 * time.Minute)
	h.customerTopUp(user, 30000)
	h.tick()
	h.wantBalances(user, 0, 0)
	if n := h.int("SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'minutes_purchase'", h.club); n != 2 {
		t.Fatalf("minutes purchases = %d", n)
	}
	if _, ends := h.pcSession(p.id); !ends.Equal(h.clock.Now().Add(30 * time.Minute)) {
		t.Fatalf("after-tariff session until %v", ends)
	}

	// Время вышло, денег нет — ничего не покупает и не ждёт.
	h.clock.advance(30 * time.Minute)
	h.tick()
	if st := h.autoBilling(ctx, p); st.GetWaiting() || !st.GetEnabled() {
		t.Fatalf("no money: %+v", st)
	}
	h.noDrift()
}

// pcSession — сессия ПК в базе.
func (h *harness) pcSession(pcID int64) (string, time.Time) {
	h.t.Helper()
	var id *string
	var ends *time.Time
	if err := env.db.Owner.QueryRow(context.Background(), "SELECT current_session_id, session_ends_at FROM club_computers WHERE id = $1", pcID).Scan(&id, &ends); err != nil {
		h.t.Fatal(err)
	}
	if id == nil || ends == nil {
		return "", time.Time{}
	}
	return *id, *ends
}

// customerTopUp — пополнение основного баланса владельцем (ledger owner_top_up).
func (h *harness) customerTopUp(user int64, amount money.Tiyn) {
	h.t.Helper()
	if err := db.InTenantTx(context.Background(), env.db.App, h.club, func(q *store.Queries) error {
		_, err := billing.OwnerAdjust(context.Background(), q, billing.OwnerAdjustParams{ClubID: h.club, UserID: user,
			Field: billing.FieldMain, Amount: amount, IdempotencyKey: key(), Actor: billing.BalanceActor{UserID: h.owner, Kind: "staff"}})
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
}

// AG-12: гость сам завершил сессию — автобиллинг стоит до нового входа (autoStopped), новый вход — снова пауза.
func TestAG12AutoBillingStopsAfterGuestEnd(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings})
	h.billing.SetNotifier(h.srv.WrapNotifier(h.stream.Notifier()))
	phone := uniqPhone()
	user := h.customer("Ерлан", phone, "password", 100000, 0)
	p := h.enrollPC()
	ctx := withToken(p.mustLogin(phone, "password"))
	buy, err := p.guest().PurchaseMinutes(ctx, &agentv1.PurchaseMinutesRequest{IdempotencyKey: key(), Minutes: 60,
		BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(10 * time.Minute)
	if _, err := p.guest().EndSession(ctx, &agentv1.EndSessionRequest{IdempotencyKey: key(), SessionId: buy.GetPurchase().GetSession().GetSessionId()}); err != nil {
		t.Fatal(err)
	}
	main, _ := h.balances(user)
	h.clock.advance(time.Hour)
	h.tick()
	if st := h.autoBilling(ctx, p); !st.GetStopped() || st.GetWaiting() {
		t.Fatalf("stopped: %+v", st)
	}
	if m, _ := h.balances(user); m != main {
		t.Fatalf("auto billing bought after guest end: %s → %s", main, m)
	}
	// Новый вход — снова пауза.
	ctx = withToken(p.mustLogin(phone, "password"))
	h.tick()
	if st := h.autoBilling(ctx, p); st.GetStopped() || !st.GetWaiting() {
		t.Fatalf("after re-login: %+v", st)
	}
	// Вышел — состояние забыто, покупки нет.
	if _, err := p.authc().Logout(ctx, &agentv1.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Hour)
	h.tick()
	if m, _ := h.balances(user); m != main {
		t.Fatalf("auto billing bought after logout: %s → %s", main, m)
	}
	h.noDrift()
}

// AG-12: повтор шага автобиллинга с тем же ключом (сбой после покупки) — ответ из billing_requests, без списания.
func TestAG12AutoBillingIdempotent(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings})
	phone := uniqPhone()
	user := h.customer("Айгерим", phone, "password", 60000, 0)
	p := h.enrollPC()
	p.mustLogin(phone, "password")
	req := billingsvc.WholeBalanceRequest{ClubID: h.club, UserID: user, ComputerID: p.id, IdempotencyKey: "autobill:test:1",
		Actor: billingsvc.Actor{UserID: user, Kind: "agent"}, AutoBilling: true, MinutesHint: 60}
	first, err := h.billing.PurchaseWholeBalance(context.Background(), req)
	if err != nil || first.MinutesAdded != 60 || first.Replayed {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := h.billing.PurchaseWholeBalance(context.Background(), req)
	if err != nil || !second.Replayed || second.SessionID != first.SessionID {
		t.Fatalf("second = %+v, %v", second, err)
	}
	h.wantBalances(user, 0, 0)
	h.noDrift()
}

// AG-13: картинки клуба по сертификату ПК: логотип, картинка входа, новость своего клуба и платформы; чужая
// новость — NOT_FOUND; etag — not_modified.
func TestAG13GetImage(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	other := newHarness(t, harnessOpts{noLimits: true})
	png := []byte("\x89PNG\r\n\x1a\nfake")
	h.exec("UPDATE clubs SET logo_path = 'club-logos/1/a.png' WHERE id = $1", h.club)
	h.exec("INSERT INTO club_images (club_id, kind, content_type, data, created_at) VALUES ($1, 'logo', 'image/png', $2, now())", h.club, png)
	own := h.int("INSERT INTO news (club_id, title, body, image_path, is_published, created_at) VALUES ($1, 'Т', 'Т', 'news/a.jpg', true, now()) RETURNING id", h.club)
	h.exec("INSERT INTO news_images (news_id, club_id, content_type, data, created_at) VALUES ($1, $2, 'image/jpeg', $3, now())", own, h.club, []byte{0xff, 0xd8, 0xff})
	foreign := h.int("INSERT INTO news (club_id, title, body, image_path, is_published, created_at) VALUES ($1, 'Т', 'Т', 'news/b.jpg', true, now()) RETURNING id", other.club)
	h.exec("INSERT INTO news_images (news_id, club_id, content_type, data, created_at) VALUES ($1, $2, 'image/jpeg', $3, now())", foreign, other.club, []byte{0xff, 0xd8, 0xff})
	p := h.enrollPC()
	ctx := context.Background()

	logo, err := p.agent().GetImage(ctx, &agentv1.GetImageRequest{Kind: agentv1.ImageKind_IMAGE_KIND_CLUB_LOGO})
	if err != nil || string(logo.GetData()) != string(png) || logo.GetContentType() != "image/png" || logo.GetEtag() == "" {
		t.Fatalf("logo = %+v, %v", logo, err)
	}
	again, err := p.agent().GetImage(ctx, &agentv1.GetImageRequest{Kind: agentv1.ImageKind_IMAGE_KIND_CLUB_LOGO, IfNoneMatch: logo.GetEtag()})
	if err != nil || !again.GetNotModified() || len(again.GetData()) != 0 {
		t.Fatalf("not modified = %+v, %v", again, err)
	}
	news, err := p.agent().GetImage(ctx, &agentv1.GetImageRequest{Kind: agentv1.ImageKind_IMAGE_KIND_NEWS, Id: own})
	if err != nil || news.GetContentType() != "image/jpeg" || len(news.GetData()) != 3 {
		t.Fatalf("news = %+v, %v", news, err)
	}
	_, err = p.agent().GetImage(ctx, &agentv1.GetImageRequest{Kind: agentv1.ImageKind_IMAGE_KIND_NEWS, Id: foreign})
	wantReason(t, err, codes.NotFound, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
	_, err = p.agent().GetImage(ctx, &agentv1.GetImageRequest{Kind: agentv1.ImageKind_IMAGE_KIND_SIGNIN})
	wantReason(t, err, codes.NotFound, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
	_, err = p.agent().GetImage(ctx, &agentv1.GetImageRequest{})
	wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
}

// AG-16: внутренняя деталь с ключом перевода не уходит агенту без перехватчика тоже не ломает ответ: текст —
// русский с подстановкой (перевод — LocalizeErrors, unit-тест locale_test.go).
func TestAG16ErrorTextWithParams(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	phone := uniqPhone()
	h.customer("Гость", phone, "password", 0, 0)
	p1, p2 := h.enrollPC(), h.enrollPC()
	p1.mustLogin(phone, "password")
	_, err := p2.login(phone, "password")
	st, _ := status.FromError(err)
	if grpcErrCode(err) != agentv1.ErrorCode_ERROR_CODE_SIGNED_IN_ELSEWHERE.String() || st.Message() == "" ||
		st.Message() == "Вы уже вошли на другом ПК (:computer). Выйдите там, чтобы войти здесь." {
		t.Fatalf("err = %v", err)
	}
}

func grpcErrCode(err error) string {
	st, _ := status.FromError(err)
	for _, d := range st.Details() {
		if info, ok := d.(interface{ GetReason() string }); ok && info.GetReason() != "" {
			return info.GetReason()
		}
	}
	return ""
}
