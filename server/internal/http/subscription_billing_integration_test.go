//go:build integration

package httpserver

// Тема Б (docs/upstream-2026-11.md): счета за подписку, оплата по Kaspi, скидка клуба — HTTP → Postgres.
// Сценарии GS-536, GS-537, GS-552…GS-559 (SubscriptionPaymentTest.php, SuperadminCabinetTest.php старого проекта,
// Zafiro 4821225, 77ae201) и пробелы GAP-039, GAP-046, GAP-054.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/subscription"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram/telegramtest"
)

const kaspiSetting = `{"url": "https://pay.kaspi.kz/pay/zafiro", "recipient": "ИП Zafiro"}`

type billingEnv struct {
	*adminEnv
	now  time.Time
	tg   *telegramtest.Server
	plan int64
}

// newBillingEnv — billingClub() теста Laravel: клуб с N ПК, тариф «Базовый» 1000 ₸ за ПК, настроенный Kaspi.
// Проверка подписки включена (RequireSubscription), лимиты маршрутов — тоже.
func newBillingEnv(t *testing.T, now time.Time, computers int) *billingEnv {
	t.Helper()
	d, vk := sharedAdmin(t)
	h, _ := password.New(password.Options{})
	sm, err := session.New(session.Options{Client: vk, Keys: []session.Key{{ID: "t", Secret: bytes.Repeat([]byte{7}, 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(auth.Options{Pool: d.App, Hasher: h, Sessions: sm})
	if err != nil {
		t.Fatal(err)
	}
	lim, err := ratelimit.New(ratelimit.Options{Client: vk, Prefix: fmt.Sprintf("bill%d:", adminSeq.Add(1))})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetSecondFactor(testSecondFactor(t, d.Platform, vk, lim))
	box, _ := secretbox.New(strings.Repeat("s", 32))
	tg := telegramtest.New(t)
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim, Passwords: h,
		Pool: d.App, PlatformPool: d.Platform, Presence: &fakePresence{states: map[int64][]presence.State{}},
		Now: func() time.Time { return now }, RequireSubscription: true,
		Superadmin: httpx.SuperadminDeps{Secrets: box,
			TelegramAlerts: &telegram.Alerts{API: &telegram.BotClient{URL: tg.BotURL()}, Store: &telegram.Store{Pool: d.Platform, Box: box}}},
	}))
	t.Cleanup(srv.Close)
	e := &billingEnv{adminEnv: &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("bill%d", adminSeq.Add(1)))}, now: now, tg: tg}
	e.exec("DELETE FROM platform_settings WHERE key = $1", telegram.SettingAlerts)
	e.kaspi(kaspiSetting)
	t.Cleanup(func() { e.kaspi("") })
	e.plan = e.scalar(`INSERT INTO saas_plans (slug, name, price_monthly, trial_days, is_active, created_at, updated_at)
		VALUES ($1, 'Базовый', 100000, 0, true, now(), now()) RETURNING id`, fmt.Sprintf("bill-base-%d", adminSeq.Add(1)))
	e.computers(e.club, computers)
	return e
}

// kaspi — настройка billing_kaspi платформы ("" — оплата не настроена).
func (e *billingEnv) kaspi(js string) {
	e.t.Helper()
	e.exec("DELETE FROM platform_settings WHERE key = $1", subscription.KaspiSetting)
	if js != "" {
		e.exec("INSERT INTO platform_settings (key, value, created_at, updated_at) VALUES ($1, $2::jsonb, now(), now())", subscription.KaspiSetting, js)
	}
}

func (e *billingEnv) computers(club int64, n int) {
	e.t.Helper()
	if n == 0 {
		return
	}
	zone := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Hall', now(), now()) RETURNING id", club)
	for i := 1; i <= n; i++ {
		e.exec("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, $3, now(), now())", club, zone, i)
	}
}

// subscribe — строка подписки клуба на тарифе «Базовый».
func (e *billingEnv) subscribe(status string, trialEnds, periodEnds *time.Time) {
	e.t.Helper()
	e.exec(`INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, trial_ends_at, current_period_ends_at, created_at, updated_at)
		VALUES ($1, $2, $3, now(), $4, $5, now(), now())`, e.club, e.plan, status, trialEnds, periodEnds)
}

func (e *billingEnv) superadmin() (int64, string) {
	e.t.Helper()
	id, phone := e.member("Супер Админ", "", "", true)
	return id, e.login(phone)
}

func (e *billingEnv) open(tok string, months int) resp {
	e.t.Helper()
	return e.post(tok, "/api/billing/invoices", map[string]any{"saas_plan_id": e.plan, "months": months})
}

func (e *billingEnv) invoiceID(r resp) int64 {
	e.t.Helper()
	expect(e.t, r, http.StatusCreated, "open invoice")
	return int64(obj(r.body["invoice"])["id"].(float64))
}

func (e *billingEnv) invoices(club int64, status string) int64 {
	e.t.Helper()
	return e.scalar("SELECT count(*) FROM subscription_invoices WHERE club_id = $1 AND ($2 = '' OR status = $2)", club, status)
}

func (e *billingEnv) periodEnd() time.Time {
	e.t.Helper()
	var ends time.Time
	if err := e.db.Owner.QueryRow(context.Background(),
		"SELECT current_period_ends_at FROM club_subscriptions WHERE club_id = $1 AND status = 'active'", e.club).Scan(&ends); err != nil {
		e.t.Fatalf("подписка не активна: %v", err)
	}
	return ends.UTC()
}

func TestSubscriptionInvoices(t *testing.T) {
	now := time.Date(2026, 11, 15, 7, 0, 0, 0, time.UTC)
	in := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	const day = 24 * time.Hour

	// «the owner opens a bill for all the club PCs, less the discount, and sees how to pay it»
	t.Run("GS-552 владелец выставляет счёт за все ПК со скидкой", func(t *testing.T) {
		e := newBillingEnv(t, now, 3)
		e.subscribe("trialing", in(5*day), nil)
		e.exec("UPDATE club_subscriptions SET discount_percent = 10 WHERE club_id = $1", e.club)
		ownerID, owner := e.staff("Владелец", "owner")

		r := e.get(owner, "/api/billing")
		expect(t, r, http.StatusOK, "billing")
		if r.body["can_pay"] != true || num(r.body["computers"]) != 3 || num(r.body["discount_percent"]) != 10 || r.body["pending"] != nil {
			t.Fatalf("billing: %v", r.body)
		}
		// «К оплате» по срокам считает сервер: 1000 ₸ × 3 ПК × N мес − 10 %.
		var plan map[string]any
		for _, p := range r.body["plans"].([]any) {
			if num(obj(p)["id"]) == e.plan {
				plan = obj(p)
			}
		}
		if plan == nil || num(plan["price_monthly_tiyn"]) != 100000 {
			t.Fatalf("plans: %v", r.body["plans"])
		}
		want := map[int64]int64{1: 270000, 3: 810000, 6: 1620000, 12: 3240000}
		for _, a := range plan["amounts"].([]any) {
			if m := num(obj(a)["months"]); num(obj(a)["amount_tiyn"]) != want[m] {
				t.Fatalf("amounts: %v", plan["amounts"])
			}
		}
		sub := obj(r.body["subscription"])
		// 1000 ₸ − 10 % = 900 ₸ за ПК · 2700 ₸ за 3 ПК.
		if num(sub["monthly_price_tiyn"]) != 90000 || num(sub["monthly_total_tiyn"]) != 270000 || num(sub["discount_percent"]) != 10 || sub["discount_until"] != nil {
			t.Fatalf("subscription: %v", sub)
		}

		r = e.open(owner, 3)
		id := e.invoiceID(r)
		inv := obj(r.body["invoice"])
		number := fmt.Sprintf("ZF-%05d", id)
		if r.body["message"] != "Счёт выставлен. Оплатите его через Kaspi." || num(inv["amount_tiyn"]) != 810000 || num(inv["computers"]) != 3 ||
			num(inv["discount_percent"]) != 10 || inv["status"] != "pending" || inv["number"] != number {
			t.Fatalf("invoice: %v", r.body)
		}
		if n := e.scalar(`SELECT count(*) FROM subscription_invoices WHERE id = $1 AND club_id = $2 AND amount = 810000 AND computers = 3
			AND months = 3 AND price_per_computer = 100000 AND discount_percent = 10 AND status = 'pending' AND created_by = $3
			AND reseller_id IS NULL AND reseller_bp IS NULL`, id, e.club, ownerID); n != 1 {
			t.Fatal("счёт в базе не тот")
		}
		if e.activityCount("billing.invoice_opened", id) != 1 {
			t.Fatal("нет записи журнала billing.invoice_opened")
		}
		var description string
		if err := e.db.Owner.QueryRow(context.Background(), "SELECT description FROM activity_logs WHERE club_id = $1 AND event = 'billing.invoice_opened'", e.club).Scan(&description); err != nil ||
			description != "Выставлен счёт "+number+" на 8 100 ₸" {
			t.Fatalf("журнал: %q %v", description, err)
		}
		if n := e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND category = 'money' AND action = 'invoice.opened' AND subject_id = $2 AND (after ->> 'amount')::bigint = 810000", e.club, id); n != 1 {
			t.Fatal("нет записи аудита invoice.opened")
		}

		r = e.get(owner, "/api/billing")
		pending, kaspi := obj(r.body["pending"]), obj(r.body["kaspi"])
		if pending["number"] != number || num(pending["amount_tiyn"]) != 810000 || pending["plan_name"] != "Базовый" || num(pending["price_per_computer_tiyn"]) != 100000 ||
			kaspi["url"] != "https://pay.kaspi.kz/pay/zafiro" || kaspi["recipient"] != "ИП Zafiro" || len(r.body["invoices"].([]any)) != 1 {
			t.Fatalf("billing with pending: %v", r.body)
		}

		// Второй счёт, пока первый не оплачен и не отменён, — ошибка.
		expectField(t, e.open(owner, 1), "invoice", "У клуба уже есть неоплаченный счёт: оплатите или отмените его.")
		if e.invoices(e.club, "") != 1 {
			t.Fatal("счетов должно быть 1")
		}

		r = e.post(owner, fmt.Sprintf("/api/billing/invoices/%d/cancel", id), nil)
		if r.status != http.StatusOK || r.body["message"] != "Счёт отменён." || e.invoices(e.club, "canceled") != 1 {
			t.Fatalf("cancel: %d %v", r.status, r.body)
		}
		if e.activityCount("billing.invoice_canceled", id) != 1 {
			t.Fatal("нет записи журнала billing.invoice_canceled")
		}
		// Отменённый счёт закрыт; новый выставить можно.
		expectField(t, e.post(owner, fmt.Sprintf("/api/billing/invoices/%d/cancel", id), nil), "invoice", "Счёт уже закрыт.")
		e.invoiceID(e.open(owner, 1))
	})

	// «only allowed months, payable plans and a configured Kaspi link make a bill»
	t.Run("GS-553 срок, тариф с ценой и настроенный Kaspi", func(t *testing.T) {
		e := newBillingEnv(t, now, 3)
		e.subscribe("trialing", in(5*day), nil)
		_, owner := e.staff("Владелец", "owner")
		trial := e.scalar(`INSERT INTO saas_plans (slug, name, trial_days, is_active, created_at, updated_at)
			VALUES ($1, 'Пробный период', 14, true, now(), now()) RETURNING id`, fmt.Sprintf("bill-trial-%d", adminSeq.Add(1)))
		off := e.scalar(`INSERT INTO saas_plans (slug, name, price_monthly, trial_days, is_active, created_at, updated_at)
			VALUES ($1, 'Архивный', 50000, 0, false, now(), now()) RETURNING id`, fmt.Sprintf("bill-off-%d", adminSeq.Add(1)))

		expectField(t, e.open(owner, 2), "months", "The selected months is invalid.")
		expectField(t, e.post(owner, "/api/billing/invoices", map[string]any{"saas_plan_id": trial, "months": 1}), "saas_plan_id", "The selected saas plan id is invalid.")
		expectField(t, e.post(owner, "/api/billing/invoices", map[string]any{"saas_plan_id": off, "months": 1}), "saas_plan_id", "The selected saas plan id is invalid.")
		expectField(t, e.post(owner, "/api/billing/invoices", map[string]any{}), "saas_plan_id", "The saas plan id field is required.")

		e.kaspi("")
		expectField(t, e.open(owner, 1), "invoice", "Оплата пока не настроена. Напишите в поддержку Zafiro.")
		if r := e.get(owner, "/api/billing"); r.body["kaspi"] != nil {
			t.Fatalf("kaspi: %v", r.body["kaspi"])
		}
		// Настройка без ссылки — тоже «не настроено» (isset($value['url'])).
		e.kaspi(`{"recipient": "ИП Zafiro"}`)
		expectField(t, e.open(owner, 1), "invoice", "Оплата пока не настроена. Напишите в поддержку Zafiro.")
		if e.invoices(e.club, "") != 0 {
			t.Fatal("счетов быть не должно")
		}
	})

	// «staff without the pay right cannot open or cancel bills, and other clubs bills are out of reach»
	t.Run("GS-554 право billing.pay и чужой клуб", func(t *testing.T) {
		e := newBillingEnv(t, now, 3)
		e.subscribe("trialing", in(5*day), nil)
		_, owner := e.staff("Владелец", "owner")
		_, manager := e.staff("Менеджер", "manager")

		r := e.get(manager, "/api/billing")
		if r.status != http.StatusOK || r.body["can_pay"] != false {
			t.Fatalf("менеджер видит экран без права платить: %d %v", r.status, r.body)
		}
		expect(t, e.open(manager, 1), http.StatusForbidden, "менеджер выставляет счёт")
		id := e.invoiceID(e.open(owner, 1))
		expect(t, e.post(manager, fmt.Sprintf("/api/billing/invoices/%d/cancel", id), nil), http.StatusForbidden, "менеджер отменяет счёт")

		// Сотрудник, которому владелец выдал право (club_memberships.permissions), платит.
		_, trusted := e.staff("Доверенный", "manager", `{"billing.pay": true}`)
		if r := e.get(trusted, "/api/billing"); r.body["can_pay"] != true {
			t.Fatalf("выданное право: %v", r.body)
		}
		expect(t, e.post(trusted, fmt.Sprintf("/api/billing/invoices/%d/cancel", id), nil), http.StatusOK, "доверенный отменяет счёт")

		// Счёт другого клуба: для владельца его нет — 404, счёт остаётся неоплаченным.
		other := e.db.Club(t, fmt.Sprintf("bill-other%d", adminSeq.Add(1)))
		foreign := e.scalar(`INSERT INTO subscription_invoices (club_id, saas_plan_id, months, computers, price_per_computer, amount, created_at, updated_at)
			VALUES ($1, $2, 1, 1, 100000, 100000, now(), now()) RETURNING id`, other, e.plan)
		expect(t, e.post(owner, fmt.Sprintf("/api/billing/invoices/%d/cancel", foreign), nil), http.StatusNotFound, "чужой счёт")
		if e.invoices(other, "pending") != 1 {
			t.Fatal("чужой счёт должен остаться pending")
		}
		if r := e.get(owner, "/api/billing"); len(r.body["invoices"].([]any)) != 1 {
			t.Fatalf("в списке — только свои счета: %v", r.body["invoices"])
		}
	})

	// «an expired club can still pay»
	t.Run("GS-555 клуб с истёкшей подпиской может заплатить", func(t *testing.T) {
		e := newBillingEnv(t, now, 1)
		e.subscribe("trialing", in(-day), nil)
		_, owner := e.staff("Владелец", "owner")
		expect(t, e.get(owner, "/api/dashboard"), http.StatusPaymentRequired, "панель закрыта")
		expect(t, e.get(owner, "/api/billing"), http.StatusOK, "подписка открыта")
		id := e.invoiceID(e.open(owner, 1))
		expect(t, e.post(owner, fmt.Sprintf("/api/billing/invoices/%d/cancel", id), nil), http.StatusOK, "отмена без подписки")
	})

	// «the superadmin marks a bill paid and the subscription runs on from its current end»
	t.Run("GS-556 оплата продлевает подписку от конца текущего периода", func(t *testing.T) {
		e := newBillingEnv(t, now, 2)
		// Подписка на другом тарифе: оплата ставит тариф счёта.
		old := e.scalar(`INSERT INTO saas_plans (slug, name, price_monthly, trial_days, is_active, created_at, updated_at)
			VALUES ($1, 'Старый', 70000, 0, true, now(), now()) RETURNING id`, fmt.Sprintf("bill-old-%d", adminSeq.Add(1)))
		e.exec(`INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, current_period_ends_at, canceled_at, created_at, updated_at)
			VALUES ($1, $2, 'active', now(), $3, now(), now(), now())`, e.club, old, in(10*day))
		_, owner := e.staff("Владелец", "owner")
		saID, sa := e.superadmin()
		id := e.invoiceID(e.open(owner, 6))
		number := fmt.Sprintf("ZF-%05d", id)

		r := e.get(sa, "/api/superadmin/invoices")
		expect(t, r, http.StatusOK, "оплаты")
		var row map[string]any
		for _, p := range r.body["pending"].([]any) {
			if num(obj(p)["id"]) == id {
				row = obj(p)
			}
		}
		if row == nil || row["number"] != number || num(row["amount_tiyn"]) != 1200000 || num(row["club_id"]) != e.club || row["plan_name"] != "Базовый" ||
			num(row["computers"]) != 2 || num(row["months"]) != 6 || row["created"] != "15.11.2026 07:00" || obj(r.body["kaspi"])["recipient"] != "ИП Zafiro" {
			t.Fatalf("оплаты: %v", r.body)
		}

		// Клуб до кабинета не дотягивается.
		expect(t, e.post(owner, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil), http.StatusForbidden, "владелец отмечает оплату")

		want := time.Date(2027, 5, 25, 7, 0, 0, 0, time.UTC) // +10 дней, +6 месяцев (addMonthsNoOverflow)
		r = e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil)
		if r.status != http.StatusOK || r.body["message"] != "Счёт "+number+" оплачен, подписка продлена до 25.05.2027." {
			t.Fatalf("pay: %d %v", r.status, r.body)
		}
		if got := e.periodEnd(); !got.Equal(want) {
			t.Fatalf("конец периода %s, want %s", got, want)
		}
		if n := e.scalar("SELECT count(*) FROM club_subscriptions WHERE club_id = $1 AND status = 'active' AND saas_plan_id = $2 AND canceled_at IS NULL", e.club, e.plan); n != 1 {
			t.Fatal("подписка: тариф счёта, active, canceled_at = NULL")
		}
		if n := e.scalar(`SELECT count(*) FROM subscription_invoices WHERE id = $1 AND status = 'paid' AND confirmed_by = $2 AND paid_at = $3
			AND period_ends_at = $4 AND reseller_amount IS NULL`, id, saID, now, want); n != 1 {
			t.Fatal("счёт: paid, confirmed_by, paid_at, period_ends_at")
		}
		if n := e.scalar(`SELECT count(*) FROM platform_logs WHERE event = 'superadmin.invoice_paid' AND club_id = $1 AND user_id = $2
			AND description = $3 AND (details ->> 'months')::int = 6 AND (details ->> 'amount')::bigint = 12000`, e.club, saID,
			"Счёт "+number+" оплачен: подписка клуба "+clubName(e)+" продлена до 25.05.2027"); n != 1 {
			t.Fatal("журнал платформы superadmin.invoice_paid")
		}
		// Журнал денег платформы: одна строка invoice_paid:<id>, начисления реселлеру нет.
		if n := e.scalar(`SELECT count(*) FROM platform_money_log WHERE idempotency_key = $1 AND kind = 'invoice_paid' AND amount = 1200000
			AND club_id = $2 AND invoice_id = $3 AND actor_user_id = $4 AND reseller_id IS NULL`, fmt.Sprintf("invoice_paid:%d", id), e.club, id, saID); n != 1 {
			t.Fatal("platform_money_log invoice_paid")
		}
		if n := e.scalar("SELECT count(*) FROM platform_money_log WHERE invoice_id = $1", id); n != 1 {
			t.Fatalf("строк журнала денег: %d", n)
		}
		if n := e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND actor_kind = 'superadmin' AND category = 'money' AND action = 'invoice.paid' AND subject_id = $2", e.club, id); n != 1 {
			t.Fatal("аудит invoice.paid")
		}

		// Повтор: ошибка, срок и журнал не меняются.
		expectField(t, e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil), "invoice", "Счёт уже закрыт.")
		expectField(t, e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/cancel", id), nil), "invoice", "Счёт уже закрыт.")
		if got := e.periodEnd(); !got.Equal(want) || e.scalar("SELECT count(*) FROM platform_money_log WHERE invoice_id = $1", id) != 1 ||
			e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.invoice_paid' AND club_id = $1", e.club) != 1 {
			t.Fatalf("повторная оплата изменила срок или журнал: %s", got)
		}
		expect(t, e.post(sa, "/api/superadmin/invoices/999999999/pay", nil), http.StatusNotFound, "нет такого счёта")

		// Клуб видит оплаченный счёт и срок; история кабинета — тоже.
		r = e.get(owner, "/api/billing")
		paid := obj(r.body["invoices"].([]any)[0])
		if r.body["pending"] != nil || paid["status"] != "paid" || paid["period_ends_at"] != "2027-05-25T07:00:00Z" ||
			obj(r.body["subscription"])["plan_name"] != "Базовый" || obj(r.body["subscription"])["state"] != "active" {
			t.Fatalf("billing after pay: %v", r.body)
		}
		r = e.get(sa, "/api/superadmin/invoices")
		var closed map[string]any
		for _, c := range r.body["closed"].([]any) {
			if num(obj(c)["id"]) == id {
				closed = obj(c)
			}
		}
		if closed == nil || closed["status"] != "paid" || closed["paid"] != "15.11.2026" || closed["created"] != "15.11.2026" || num(r.body["per_page"]) != 30 {
			t.Fatalf("история: %v", r.body["closed"])
		}
	})

	// «a paid bill of an expired club counts from today»
	t.Run("GS-557 истёкшая подписка считается от сегодня", func(t *testing.T) {
		e := newBillingEnv(t, now, 1)
		e.subscribe("expired", nil, in(-30*day))
		_, owner := e.staff("Владелец", "owner")
		_, sa := e.superadmin()
		id := e.invoiceID(e.open(owner, 1))
		expect(t, e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil), http.StatusOK, "pay")
		if got, want := e.periodEnd(), time.Date(2026, 12, 15, 7, 0, 0, 0, time.UTC); !got.Equal(want) {
			t.Fatalf("конец периода %s, want %s", got, want)
		}
		expect(t, e.get(owner, "/api/dashboard"), http.StatusOK, "панель открыта после оплаты")
	})

	t.Run("GAP-039 оплата при разных состояниях подписки", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			setup  func(e *billingEnv)
			months int
			want   time.Time
		}{
			{"пробный период — от его конца", func(e *billingEnv) { e.subscribe("trialing", in(5*day), nil) }, 1, time.Date(2026, 12, 20, 7, 0, 0, 0, time.UTC)},
			{"бессрочная активная получает дату", func(e *billingEnv) { e.subscribe("active", nil, nil) }, 12, time.Date(2027, 11, 15, 7, 0, 0, 0, time.UTC)},
			{"приостановленная — от сегодня", func(e *billingEnv) { e.subscribe("suspended", nil, in(90*day)) }, 3, time.Date(2027, 2, 15, 7, 0, 0, 0, time.UTC)},
			{"нет строки подписки — создаётся", func(*billingEnv) {}, 6, time.Date(2027, 5, 15, 7, 0, 0, 0, time.UTC)},
			{"конец 31.01 + 1 месяц = 28.02", func(e *billingEnv) {
				ends := time.Date(2027, 1, 31, 10, 0, 0, 0, time.UTC)
				e.subscribe("active", nil, &ends)
			}, 1, time.Date(2027, 2, 28, 10, 0, 0, 0, time.UTC)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := newBillingEnv(t, now, 1)
				tc.setup(e)
				_, owner := e.staff("Владелец", "owner")
				_, sa := e.superadmin()
				id := e.invoiceID(e.open(owner, tc.months))
				expect(t, e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil), http.StatusOK, "pay")
				if got := e.periodEnd(); !got.Equal(tc.want) {
					t.Fatalf("конец периода %s, want %s", got, tc.want)
				}
				if n := e.scalar("SELECT count(*) FROM club_subscriptions WHERE club_id = $1 AND saas_plan_id = $2 AND started_at IS NOT NULL", e.club, e.plan); n != 1 {
					t.Fatal("строка подписки")
				}
			})
		}
	})

	// «the superadmin sets the Kaspi link and cancels bills; clubs cannot reach the cabinet»
	t.Run("GS-558 суперадмин настраивает Kaspi и отменяет счёт", func(t *testing.T) {
		e := newBillingEnv(t, now, 1)
		e.subscribe("trialing", in(5*day), nil)
		_, owner := e.staff("Владелец", "owner")
		saID, sa := e.superadmin()
		id := e.invoiceID(e.open(owner, 1))

		expect(t, e.post(owner, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil), http.StatusForbidden, "владелец: pay")
		expect(t, e.post(owner, fmt.Sprintf("/api/superadmin/invoices/%d/cancel", id), nil), http.StatusForbidden, "владелец: cancel")
		expect(t, e.post(owner, "/api/superadmin/invoices/kaspi", map[string]any{"url": "https://pay.kaspi.kz/pay/evil"}), http.StatusForbidden, "владелец: kaspi")
		expect(t, e.get(owner, "/api/superadmin/invoices"), http.StatusForbidden, "владелец: оплаты")

		for _, bad := range []string{"http://pay.kaspi.kz/pay/new", "javascript:alert(1)", "pay.kaspi.kz/pay/new", "https://"} {
			expectField(t, e.post(sa, "/api/superadmin/invoices/kaspi", map[string]any{"url": bad}), "url", "The url field must be a valid URL.")
		}
		expectField(t, e.post(sa, "/api/superadmin/invoices/kaspi", map[string]any{"url": "https://pay.kaspi.kz/" + strings.Repeat("a", 500)}),
			"url", "The url field must not be greater than 500 characters.")
		expectField(t, e.post(sa, "/api/superadmin/invoices/kaspi", map[string]any{"url": "https://pay.kaspi.kz/pay/new", "recipient": strings.Repeat("я", 201)}),
			"recipient", "The recipient field must not be greater than 200 characters.")

		r := e.post(sa, "/api/superadmin/invoices/kaspi", map[string]any{"url": "https://pay.kaspi.kz/pay/new", "recipient": "ТОО Zafiro"})
		if r.status != http.StatusOK || r.body["message"] != "Настройки оплаты сохранены." {
			t.Fatalf("kaspi: %d %v", r.status, r.body)
		}
		if n := e.scalar(`SELECT count(*) FROM platform_settings WHERE key = 'billing_kaspi' AND value ->> 'url' = 'https://pay.kaspi.kz/pay/new'
			AND value ->> 'recipient' = 'ТОО Zafiro' AND updated_by = $1`, saID); n != 1 {
			t.Fatal("настройка billing_kaspi")
		}
		if k := obj(e.get(owner, "/api/billing").body["kaspi"]); k["url"] != "https://pay.kaspi.kz/pay/new" || k["recipient"] != "ТОО Zafiro" {
			t.Fatalf("клуб видит новые реквизиты: %v", k)
		}
		if n := e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.billing_kaspi_changed' AND user_id = $1 AND description = 'Изменена ссылка на оплату Kaspi'", saID); n != 1 {
			t.Fatal("журнал платформы superadmin.billing_kaspi_changed")
		}

		r = e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/cancel", id), nil)
		if r.status != http.StatusOK || r.body["message"] != "Счёт отменён." || e.invoices(e.club, "canceled") != 1 {
			t.Fatalf("cancel: %d %v", r.status, r.body)
		}
		if n := e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.invoice_canceled' AND club_id = $1 AND description = $2", e.club,
			fmt.Sprintf("Отменён счёт ZF-%05d клуба %s", id, clubName(e))); n != 1 {
			t.Fatal("журнал платформы superadmin.invoice_canceled")
		}
		// Отменённый счёт оплатить нельзя: подписка не продлевается, денег в журнале нет.
		expectField(t, e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil), "invoice", "Счёт уже закрыт.")
		if e.scalar("SELECT count(*) FROM platform_money_log WHERE invoice_id = $1", id) != 0 {
			t.Fatal("оплата отменённого счёта попала в журнал денег")
		}

		// Пустая ссылка выключает оплату: настройка удаляется, клубы не могут выставить счёт.
		r = e.post(sa, "/api/superadmin/invoices/kaspi", map[string]any{"url": "", "recipient": "ТОО Zafiro"})
		if r.status != http.StatusOK || e.scalar("SELECT count(*) FROM platform_settings WHERE key = 'billing_kaspi'") != 0 {
			t.Fatalf("kaspi off: %d %v", r.status, r.body)
		}
		if n := e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.billing_kaspi_changed' AND description = 'Оплата через Kaspi отключена' AND user_id = $1", saID); n != 1 {
			t.Fatal("журнал платформы: оплата отключена")
		}
		expectField(t, e.open(owner, 1), "invoice", "Оплата пока не настроена. Напишите в поддержку Zafiro.")
		if r := e.get(sa, "/api/superadmin/invoices"); r.body["kaspi"] != nil {
			t.Fatalf("kaspi в кабинете: %v", r.body["kaspi"])
		}
	})

	// «a new bill is reported to the platform telegram chat»
	t.Run("GS-559 о счёте сообщает Telegram", func(t *testing.T) {
		e := newBillingEnv(t, now, 2)
		e.subscribe("trialing", in(5*day), nil)
		_, owner := e.staff("Владелец", "owner")
		_, sa := e.superadmin()
		sent := func() []telegramtest.Request {
			var out []telegramtest.Request
			for _, r := range e.tg.Requests() {
				if r.API == "bot" && r.Method == "sendMessage" {
					out = append(out, r)
				}
			}
			return out
		}
		// Бот не подключён: счёт выставляется, в Telegram ничего не уходит.
		first := e.invoiceID(e.open(owner, 1))
		time.Sleep(300 * time.Millisecond)
		if got := sent(); len(got) != 0 {
			t.Fatalf("без бота отправлено: %v", got)
		}
		expect(t, e.post(owner, fmt.Sprintf("/api/billing/invoices/%d/cancel", first), nil), http.StatusOK, "cancel")

		e.tg.OnBot(func(telegramtest.Request) telegramtest.Reply {
			return telegramtest.Reply{Body: map[string]any{"ok": true}}
		})
		if r := e.post(sa, "/api/superadmin/telegram/alerts", map[string]any{"bot_token": tgBotToken, "chat_id": "42"}); r.status != http.StatusOK {
			t.Fatalf("set alerts: %d %v", r.status, r.body)
		}
		t.Cleanup(func() { e.exec("DELETE FROM platform_settings WHERE key = $1", telegram.SettingAlerts) })
		before := len(sent()) // проверочное сообщение подключения
		id := e.invoiceID(e.open(owner, 1))
		deadline := time.Now().Add(5 * time.Second)
		for len(sent()) == before && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		got := sent()
		if len(got) != before+1 {
			t.Fatalf("сообщений о счёте: %d", len(got)-before)
		}
		msg := got[len(got)-1]
		want := fmt.Sprintf("💳 Счёт ZF-%05d: %s\nБазовый, 1 мес., 2 ПК\nК оплате 2 000 ₸\nhttps://%s/superadmin/invoices", id, clubName(e), strings.TrimPrefix(e.srv.URL, "http://"))
		if msg.Body["text"] != want || fmt.Sprint(msg.Body["chat_id"]) != "42" || msg.Body["disable_web_page_preview"] != true {
			t.Fatalf("сообщение:\n%v\nожидалось:\n%s", msg.Body["text"], want)
		}
	})

	t.Run("throttle 10 в минуту на выставление счёта", func(t *testing.T) {
		e := newBillingEnv(t, now, 1)
		e.subscribe("trialing", in(5*day), nil)
		_, owner := e.staff("Владелец", "owner")
		for i := 0; i < 10; i++ {
			expect(t, e.open(owner, 2), http.StatusUnprocessableEntity, "в пределах лимита")
		}
		expect(t, e.open(owner, 1), http.StatusTooManyRequests, "11-й запрос за минуту")
		if e.invoices(e.club, "") != 0 {
			t.Fatal("счёт сверх лимита")
		}
	})

	// Суперадмин, открывший панель клуба на просмотр, счёт выставить не может (запись запрещена, как любая).
	t.Run("просмотр клуба суперадмином — без записи", func(t *testing.T) {
		e := newBillingEnv(t, now, 1)
		e.subscribe("trialing", in(5*day), nil)
		_, sa := e.superadmin()
		r := e.post(sa, fmt.Sprintf("/api/superadmin/clubs/%d/view", e.club), nil)
		expect(t, r, http.StatusOK, "view club")
		tok, _ := r.body["access_token"].(string)
		if tok == "" {
			tok = sa
		}
		expect(t, e.get(tok, "/api/billing"), http.StatusOK, "экран в режиме просмотра")
		expect(t, e.open(tok, 1), http.StatusForbidden, "счёт в режиме просмотра")
		if e.invoices(e.club, "") != 0 {
			t.Fatal("счёт создан в режиме просмотра")
		}
	})
}

func clubName(e *billingEnv) string {
	e.t.Helper()
	var name string
	if err := e.db.Owner.QueryRow(context.Background(), "SELECT name FROM clubs WHERE id = $1", e.club).Scan(&name); err != nil {
		e.t.Fatal(err)
	}
	return name
}

// GAP-046 (BR-184): два одновременных счёта клуба без строки подписки. В старом коде блокировать было нечего, и оба
// проходили; здесь второй упирается в частичный уникальный индекс и получает тот же текст ошибки.
func TestSubscriptionInvoices_GAP046_ConcurrentOpen(t *testing.T) {
	now := time.Date(2026, 11, 15, 7, 0, 0, 0, time.UTC)
	e := newBillingEnv(t, now, 2) // строки подписки нет
	_, owner := e.staff("Владелец", "owner")
	const n = 8
	results := make([]resp, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = e.open(owner, 1)
		}()
	}
	wg.Wait()
	created := 0
	for _, r := range results {
		switch r.status {
		case http.StatusCreated:
			created++
		case http.StatusUnprocessableEntity:
			if fieldError(r, "invoice") != "У клуба уже есть неоплаченный счёт: оплатите или отмените его." {
				t.Fatalf("ошибка второго счёта: %v", r.body)
			}
		default:
			t.Fatalf("status %d %v", r.status, r.body)
		}
	}
	if created != 1 || e.invoices(e.club, "pending") != 1 || e.invoices(e.club, "") != 1 {
		t.Fatalf("выставлено %d, в базе %d", created, e.invoices(e.club, ""))
	}
}

// BR-181, BR-185, BR-186, GAP-054: реселлер клуба в счёте — скидка (большая из своей и реселлера) и снимок процента
// при выставлении, начисление при оплате; остановленный реселлер не даёт ни скидки, ни процента; удалённый до оплаты
// — начисления нет. Кабинет реселлера и привязка по коду — тема Д; здесь только счёт.
func TestSubscriptionInvoices_Reseller(t *testing.T) {
	now := time.Date(2026, 11, 15, 7, 0, 0, 0, time.UTC)
	trialEnds := now.Add(5 * 24 * time.Hour)
	reseller := func(e *billingEnv, bp, discount int, active bool) int64 {
		t.Helper()
		uid, _ := e.member("Реселлер", "", "", false)
		id := e.scalar(`INSERT INTO resellers (user_id, code, name, commission_bp, discount_percent, is_active, created_at, updated_at)
			VALUES ($1, $2, 'Партнёр', $3, $4, $5, now(), now()) RETURNING id`, uid, fmt.Sprintf("partner-%d", adminSeq.Add(1)), bp, discount, active)
		e.exec("UPDATE clubs SET reseller_id = $2, reseller_attached_at = now() WHERE id = $1", e.club, id)
		return id
	}

	t.Run("скидка реселлера больше своей: снимок и начисление", func(t *testing.T) {
		e := newBillingEnv(t, now, 3)
		e.subscribe("trialing", &trialEnds, nil)
		e.exec("UPDATE club_subscriptions SET discount_percent = 5 WHERE club_id = $1", e.club)
		rid := reseller(e, 1235, 15, true)
		_, owner := e.staff("Владелец", "owner")
		saID, sa := e.superadmin()

		r := e.get(owner, "/api/billing")
		// Скидка счёта — max(5, 15); цена «в месяц» на карточке подписки — только со своей скидкой (BR-182).
		if num(r.body["discount_percent"]) != 15 || num(obj(r.body["subscription"])["discount_percent"]) != 5 || num(obj(r.body["subscription"])["monthly_price_tiyn"]) != 95000 {
			t.Fatalf("billing: %v", r.body)
		}
		// 1000 ₸ × 3 ПК × 1 мес − 15 % = 2550 ₸.
		id := e.invoiceID(e.open(owner, 1))
		if n := e.scalar("SELECT count(*) FROM subscription_invoices WHERE id = $1 AND amount = 255000 AND discount_percent = 15 AND reseller_id = $2 AND reseller_bp = 1235 AND reseller_amount IS NULL", id, rid); n != 1 {
			t.Fatal("снимок реселлера в счёте")
		}
		// Процент меняется после выставления — счёт хранит снимок.
		e.exec("UPDATE resellers SET commission_bp = 5000, is_active = false WHERE id = $1", rid)
		expect(t, e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil), http.StatusOK, "pay")
		// 2550 ₸ × 12,35 % = 314,925 → 314,93 ₸.
		if n := e.scalar("SELECT count(*) FROM subscription_invoices WHERE id = $1 AND status = 'paid' AND reseller_amount = 31493", id); n != 1 {
			t.Fatal("начисление реселлеру в счёте")
		}
		if n := e.scalar(`SELECT count(*) FROM platform_money_log WHERE idempotency_key = $1 AND kind = 'reseller_accrual' AND amount = 31493
			AND club_id = $2 AND invoice_id = $3 AND reseller_id = $4 AND actor_user_id = $5`, fmt.Sprintf("reseller_accrual:%d", id), e.club, id, rid, saID); n != 1 {
			t.Fatal("platform_money_log reseller_accrual")
		}
		if n := e.scalar("SELECT count(*) FROM platform_money_log WHERE idempotency_key = $1 AND kind = 'invoice_paid' AND amount = 255000 AND reseller_id = $2", fmt.Sprintf("invoice_paid:%d", id), rid); n != 1 {
			t.Fatal("platform_money_log invoice_paid")
		}
	})

	t.Run("остановленный реселлер: ни скидки, ни процента", func(t *testing.T) {
		e := newBillingEnv(t, now, 1)
		e.subscribe("trialing", &trialEnds, nil)
		reseller(e, 1000, 30, false)
		_, owner := e.staff("Владелец", "owner")
		id := e.invoiceID(e.open(owner, 1))
		if n := e.scalar("SELECT count(*) FROM subscription_invoices WHERE id = $1 AND amount = 100000 AND discount_percent = 0 AND reseller_id IS NULL AND reseller_bp IS NULL", id); n != 1 {
			t.Fatal("счёт клуба остановленного реселлера")
		}
	})

	t.Run("GAP-054 реселлер удалён до оплаты: начисления нет", func(t *testing.T) {
		e := newBillingEnv(t, now, 1)
		e.subscribe("trialing", &trialEnds, nil)
		rid := reseller(e, 1000, 10, true)
		_, owner := e.staff("Владелец", "owner")
		_, sa := e.superadmin()
		id := e.invoiceID(e.open(owner, 1))
		e.exec("DELETE FROM resellers WHERE id = $1", rid) // reseller_id → NULL (ON DELETE SET NULL)
		expect(t, e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil), http.StatusOK, "pay")
		if n := e.scalar("SELECT count(*) FROM subscription_invoices WHERE id = $1 AND status = 'paid' AND amount = 90000 AND reseller_amount IS NULL", id); n != 1 {
			t.Fatal("счёт: скидка осталась, начисления нет")
		}
		if n := e.scalar("SELECT count(*) FROM platform_money_log WHERE invoice_id = $1", id); n != 1 {
			t.Fatalf("строк журнала денег: %d (ожидается только invoice_paid)", n)
		}
	})
}

// GS-536, GS-537 (SuperadminCabinetTest): индивидуальная скидка клуба.
func TestSubscriptionDiscount(t *testing.T) {
	now := time.Date(2026, 11, 15, 7, 0, 0, 0, time.UTC)

	// «the superadmin gives a club an individual discount»
	t.Run("GS-536 суперадмин даёт клубу скидку", func(t *testing.T) {
		e := newBillingEnv(t, now, 1)
		e.exec("UPDATE clubs SET timezone = 'Asia/Almaty' WHERE id = $1", e.club)
		pro := e.scalar(`INSERT INTO saas_plans (slug, name, price_monthly, trial_days, is_active, created_at, updated_at)
			VALUES ($1, 'Pro', 5000000, 0, true, now(), now()) RETURNING id`, fmt.Sprintf("bill-pro-%d", adminSeq.Add(1)))
		saID, sa := e.superadmin()
		_, owner := e.staff("Владелец", "owner")
		path := fmt.Sprintf("/api/superadmin/clubs/%d/subscription/discount", e.club)
		expect(t, e.post(sa, fmt.Sprintf("/api/superadmin/clubs/%d/subscription/extend", e.club), map[string]any{"days": 30, "saas_plan_id": pro}), http.StatusOK, "extend")

		expect(t, e.post(owner, path, map[string]any{"discount_percent": 20}), http.StatusForbidden, "владелец ставит себе скидку")
		expectField(t, e.post(sa, path, map[string]any{"discount_percent": 101}), "discount_percent", "The discount percent field must not be greater than 100.")
		expectField(t, e.post(sa, path, map[string]any{"discount_percent": -1}), "discount_percent", "The discount percent field must be at least 0.")
		expectField(t, e.post(sa, path, map[string]any{"discount_percent": 20, "discount_until": "2026-11-14"}), "discount_until",
			"The discount until field must be a date after or equal to today.")
		expectField(t, e.post(sa, path, map[string]any{"discount_percent": 20, "discount_until": "скоро"}), "discount_until", "The discount until field must be a valid date.")
		expectField(t, e.post(sa, path, map[string]any{"discount_percent": 20, "discount_note": strings.Repeat("я", 201)}), "discount_note",
			"The discount note field must not be greater than 200 characters.")

		r := e.post(sa, path, map[string]any{"discount_percent": 20, "discount_until": "2026-11-25", "discount_note": "Партнёр"})
		if r.status != http.StatusOK || r.body["message"] != "Скидка сохранена." {
			t.Fatalf("discount: %d %v", r.status, r.body)
		}
		if n := e.scalar("SELECT count(*) FROM club_subscriptions WHERE club_id = $1 AND discount_percent = 20 AND discount_until = '2026-11-25' AND discount_note = 'Партнёр'", e.club); n != 1 {
			t.Fatal("скидка в базе")
		}
		if n := e.scalar(`SELECT count(*) FROM platform_logs WHERE event = 'superadmin.subscription_discount_changed' AND club_id = $1 AND user_id = $2
			AND description = $3 AND (details ->> 'percent')::int = 20 AND details ->> 'until' = '2026-11-25'`, e.club, saID,
			"Скидка клуба "+clubName(e)+": 20% до 25.11.2026"); n != 1 {
			t.Fatal("журнал платформы superadmin.subscription_discount_changed")
		}
		if n := e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND category = 'tariff' AND action = 'subscription.discount_changed' AND (after ->> 'discount_percent')::int = 20", e.club); n != 1 {
			t.Fatal("аудит скидки")
		}

		// Список клубов — «−20%», карточка клуба и «Подписка» владельца — 40 000 ₸.
		r = e.get(sa, "/api/superadmin/clubs?search="+clubName(e))
		row := obj(r.body["items"].([]any)[0])
		if num(row["discount_percent"]) != 20 || row["discount_note"] != "Партнёр" {
			t.Fatalf("clubs row: %v", row)
		}
		r = e.get(sa, fmt.Sprintf("/api/superadmin/clubs/%d", e.club))
		sub := obj(r.body["subscription"])
		if num(sub["monthly_price_tiyn"]) != 4000000 || num(sub["plan_price_monthly_tiyn"]) != 5000000 || num(sub["discount_percent"]) != 20 ||
			sub["discount_until"] != "2026-11-25" || sub["discount_until_label"] != "25.11.2026" || sub["discount_note"] != "Партнёр" || r.body["today"] != "2026-11-15" {
			t.Fatalf("club card: %v", sub)
		}
		r = e.get(owner, "/api/billing")
		sub = obj(r.body["subscription"])
		if num(sub["monthly_price_tiyn"]) != 4000000 || num(sub["discount_percent"]) != 20 || sub["discount_until"] != "2026-11-25" || num(r.body["discount_percent"]) != 20 {
			t.Fatalf("billing: %v", r.body)
		}

		// 0 % снимает скидку вместе с датой и причиной.
		r = e.post(sa, path, map[string]any{"discount_percent": 0, "discount_note": "x"})
		if r.status != http.StatusOK || r.body["message"] != "Скидка снята." {
			t.Fatalf("discount off: %d %v", r.status, r.body)
		}
		if n := e.scalar("SELECT count(*) FROM club_subscriptions WHERE club_id = $1 AND discount_percent = 0 AND discount_until IS NULL AND discount_note IS NULL", e.club); n != 1 {
			t.Fatal("скидка не снята")
		}
		if n := e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.subscription_discount_changed' AND club_id = $1", e.club); n != 2 {
			t.Fatalf("событий скидки: %d", n)
		}
		if r := e.get(owner, "/api/billing"); num(obj(r.body["subscription"])["monthly_price_tiyn"]) != 5000000 {
			t.Fatalf("цена без скидки: %v", r.body["subscription"])
		}
		// Бессрочная скидка.
		expect(t, e.post(sa, path, map[string]any{"discount_percent": 7, "discount_until": "", "discount_note": " "}), http.StatusOK, "бессрочная")
		if n := e.scalar("SELECT count(*) FROM club_subscriptions WHERE club_id = $1 AND discount_percent = 7 AND discount_until IS NULL AND discount_note IS NULL", e.club); n != 1 {
			t.Fatal("бессрочная скидка")
		}
		if n := e.scalar("SELECT count(*) FROM platform_logs WHERE club_id = $1 AND description = $2", e.club, "Скидка клуба "+clubName(e)+": 7% бессрочно"); n != 1 {
			t.Fatal("журнал: бессрочно")
		}
	})

	// Последний день скидки — по часам клуба в панели клуба (счёт), по UTC в кабинете (GAP-055).
	t.Run("GS-536 GAP-055 последний день скидки", func(t *testing.T) {
		for _, tc := range []struct {
			name              string
			now               time.Time
			club, cabinet     int64
			wantInvoiceAmount int64
		}{
			// Клуб в Алматы (UTC+5), скидка по 25.11.
			{"за минуту до конца дня клуба", time.Date(2026, 11, 25, 18, 59, 0, 0, time.UTC), 20, 20, 80000},
			{"день клуба кончился, по UTC ещё 25-е", time.Date(2026, 11, 25, 19, 0, 0, 0, time.UTC), 0, 20, 100000},
			{"через два дня", time.Date(2026, 11, 27, 18, 59, 0, 0, time.UTC), 0, 0, 100000},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := newBillingEnv(t, tc.now, 1)
				e.exec("UPDATE clubs SET timezone = 'Asia/Almaty' WHERE id = $1", e.club)
				ends := tc.now.Add(240 * time.Hour)
				e.subscribe("active", nil, &ends)
				e.exec("UPDATE club_subscriptions SET discount_percent = 20, discount_until = '2026-11-25', discount_note = 'Партнёр' WHERE club_id = $1", e.club)
				_, owner := e.staff("Владелец", "owner")
				_, sa := e.superadmin()
				r := e.get(owner, "/api/billing")
				if num(obj(r.body["subscription"])["discount_percent"]) != tc.club || num(r.body["discount_percent"]) != tc.club {
					t.Fatalf("скидка в панели клуба: %v", r.body)
				}
				r = e.get(sa, fmt.Sprintf("/api/superadmin/clubs/%d", e.club))
				sub := obj(r.body["subscription"])
				if num(sub["discount_percent"]) != tc.cabinet || num(sub["stored_discount_percent"]) != 20 || sub["discount_until_label"] != "25.11.2026" {
					t.Fatalf("скидка в кабинете: %v", sub)
				}
				if got := num(obj(e.open(owner, 1).body["invoice"])["amount_tiyn"]); got != tc.wantInvoiceAmount {
					t.Fatalf("сумма счёта %d, want %d", got, tc.wantInvoiceAmount)
				}
			})
		}
	})

	// «a club without a subscription cannot get a discount»
	t.Run("GS-537 клубу без подписки скидку не дать", func(t *testing.T) {
		e := newBillingEnv(t, now, 1)
		_, sa := e.superadmin()
		expect(t, e.post(sa, fmt.Sprintf("/api/superadmin/clubs/%d/subscription/discount", e.club), map[string]any{"discount_percent": 10}), http.StatusNotFound, "без подписки")
		expect(t, e.post(sa, "/api/superadmin/clubs/999999999/subscription/discount", map[string]any{"discount_percent": 10}), http.StatusNotFound, "нет клуба")
	})
}
