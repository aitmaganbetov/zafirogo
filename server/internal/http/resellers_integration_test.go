//go:build integration

package httpserver

// Тема Д (docs/upstream-2026-11.md): реселлеры — кабинет суперадмина, код в настройках клуба, кабинет реселлера на
// своём хосте — HTTP → Postgres. Сценарии GS-560…GS-565 (tests/Feature/ResellerTest.php старого проекта, Zafiro
// ca5c8f0, 2318bc7, 9c2df5f) и пробелы GAP-053, GAP-054; изоляция кабинета реселлера (чужие клубы и суммы).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
)

const resellerTestHost = "resseler.zafiro.test"

// newResellerEnv — клуб с N ПК, тариф «Базовый» 1000 ₸ за ПК, настроенный Kaspi (как billingClub() теста Laravel);
// domain — хост кабинета реселлера ("" — кабинет по /reseller на любом хосте). Лимиты маршрутов и проверка подписки
// включены.
func newResellerEnv(t *testing.T, now time.Time, computers int, domain string) *billingEnv {
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
	lim, err := ratelimit.New(ratelimit.Options{Client: vk, Prefix: fmt.Sprintf("rsl%d:", adminSeq.Add(1))})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetSecondFactor(testSecondFactor(t, d.Platform, vk, lim))
	box, _ := secretbox.New(strings.Repeat("s", 32))
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim, Passwords: h,
		Pool: d.App, PlatformPool: d.Platform, Presence: &fakePresence{states: map[int64][]presence.State{}},
		Now: func() time.Time { return now }, RequireSubscription: true,
		Superadmin: httpx.SuperadminDeps{Secrets: box}, ResellerDomain: domain,
	}))
	t.Cleanup(srv.Close)
	e := &billingEnv{adminEnv: &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("rsl%d", adminSeq.Add(1)))}, now: now}
	e.kaspi(kaspiSetting)
	t.Cleanup(func() { e.kaspi("") })
	e.plan = e.scalar(`INSERT INTO saas_plans (slug, name, price_monthly, trial_days, is_active, created_at, updated_at)
		VALUES ($1, 'Базовый', 100000, 0, true, now(), now()) RETURNING id`, fmt.Sprintf("rsl-base-%d", adminSeq.Add(1)))
	e.computers(e.club, computers)
	return e
}

// makeReseller — makeReseller() теста Laravel: реселлер со своим аккаунтом (код уникален в общей базе тестов).
// Возвращает id реселлера, его код, id и телефон аккаунта (пароль «pw»).
func makeReseller(e *billingEnv, bp, discount int, active bool) (id int64, code string, userID int64, phone string) {
	e.t.Helper()
	userID, phone = e.member("Arman", "", "", false)
	code = fmt.Sprintf("arman-games-%d", adminSeq.Add(1))
	id = e.scalar(`INSERT INTO resellers (user_id, code, name, commission_bp, discount_percent, is_active, created_at, updated_at)
		VALUES ($1, $2, 'Arman', $3, $4, $5, now(), now()) RETURNING id`, userID, code, bp, discount, active)
	return id, code, userID, phone
}

// superadminWithPhone — суперадмин, его токен и телефон (для свежих кодов TOTP: sfCode).
func superadminWithPhone(e *billingEnv) (id int64, tok, phone string) {
	e.t.Helper()
	id, phone = e.member("Супер Админ", "", "", true)
	return id, e.login(phone), phone
}

func (e *billingEnv) del(tok, path string, body any) resp {
	e.t.Helper()
	return do(e.t, "DELETE", e.srv.URL+path, body, bearer(tok))
}

func (e *billingEnv) patch(tok, path string, body any) resp {
	e.t.Helper()
	return do(e.t, "PATCH", e.srv.URL+path, body, bearer(tok))
}

// resellerCard — карточка реселлера с экрана «Реселлеры» (поиск по коду).
func (e *billingEnv) resellerCard(sa, code string) map[string]any {
	e.t.Helper()
	r := e.get(sa, "/api/superadmin/resellers?q="+code)
	expect(e.t, r, http.StatusOK, "resellers")
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		if obj(it)["code"] == code {
			return obj(it)
		}
	}
	e.t.Fatalf("реселлера %s нет в списке: %v", code, r.body)
	return nil
}

func TestResellers(t *testing.T) {
	now := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	trialEnds := now.Add(5 * 24 * time.Hour)

	// «the superadmin creates resellers: a new account, or an existing one by phone»
	t.Run("GS-560 суперадмин создаёт реселлера: новый аккаунт или существующий по телефону", func(t *testing.T) {
		e := newResellerEnv(t, now, 1, "")
		saID, sa, _ := superadminWithPhone(e)
		existingID, existingPhone := e.member("Club Partner", "owner", "", false)
		_, taken, _, _ := makeReseller(e, 0, 0, true)
		seq := adminSeq.Add(1)
		newPhone := fmt.Sprintf("70012%05d", seq%100000) // 10 цифр, как вводит суперадмин
		code := fmt.Sprintf("Arman-Games-%d", seq)
		lower := strings.ToLower(code)

		form := func(name, phone, pw, code string, bp, discount int) map[string]any {
			m := map[string]any{"name": name, "phone": phone, "code": code, "commission_bp": bp, "discount_percent": discount}
			if pw != "" {
				m["password"] = pw
			}
			return m
		}
		// Занятый код.
		expectField(t, e.post(sa, "/api/superadmin/resellers", form("Arman", newPhone, "secret12", taken, 1000, 15)), "code", "Этот код уже занят — выберите другой.")
		// Новый телефон без пароля.
		r := e.post(sa, "/api/superadmin/resellers", form("Arman", newPhone, "", lower, 1000, 15))
		if r.status != http.StatusUnprocessableEntity || fieldError(r, "password") == "" {
			t.Fatalf("новый аккаунт без пароля: %d %v", r.status, r.body)
		}
		// Обе ошибки формы сразу, как $request->validate().
		r = e.post(sa, "/api/superadmin/resellers", form("Arman", newPhone, "", taken, 1000, 15))
		if fieldError(r, "password") == "" || fieldError(r, "code") == "" {
			t.Fatalf("ошибки пароля и кода вместе: %v", r.body)
		}
		// Формат полей.
		for field, f := range map[string]map[string]any{
			"name":               form("", newPhone, "secret12", lower, 1000, 15),
			"phone":              form("Arman", "12345", "secret12", lower, 1000, 15),
			"password":           form("Arman", newPhone, "12345", lower, 1000, 15),
			"code":               form("Arman", newPhone, "secret12", "ab", 1000, 15),
			"commission_percent": form("Arman", newPhone, "secret12", lower, 10001, 15),
			"discount_percent":   form("Arman", newPhone, "secret12", lower, 1000, 101),
		} {
			if r := e.post(sa, "/api/superadmin/resellers", f); r.status != http.StatusUnprocessableEntity || fieldError(r, field) == "" {
				t.Fatalf("поле %s: %d %v", field, r.status, r.body)
			}
		}
		if n := e.scalar("SELECT count(*) FROM resellers WHERE code = $1", lower); n != 0 {
			t.Fatal("реселлер создан при ошибке формы")
		}

		// Новый аккаунт: телефон «8 700 …» и код в любом регистре.
		pretty := "8 " + newPhone[:3] + " " + newPhone[3:6] + "-" + newPhone[6:8] + "-" + newPhone[8:]
		r = e.post(sa, "/api/superadmin/resellers", form("Arman", pretty, "secret12", " "+code+" ", 1000, 15))
		expect(t, r, http.StatusCreated, "create")
		if msg, _ := r.body["message"].(string); !strings.Contains(msg, lower) || !strings.Contains(msg, "/reseller") {
			t.Fatalf("сообщение: %q", msg)
		}
		rid := e.scalar("SELECT id FROM resellers WHERE code = $1 AND commission_bp = 1000 AND discount_percent = 15 AND is_active", lower)
		if phone := e.text("SELECT u.phone FROM users u JOIN resellers r ON r.user_id = u.id WHERE r.id = $1", rid); phone != "+7"+newPhone {
			t.Fatalf("телефон нового аккаунта: %s", phone)
		}
		// Пароль нового аккаунта хранится хэшем Argon2id.
		if hash := e.text("SELECT u.password FROM users u JOIN resellers r ON r.user_id = u.id WHERE r.id = $1", rid); !strings.HasPrefix(hash, "$argon2id$") {
			t.Fatalf("хэш пароля: %.12s…", hash)
		}
		if n := e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.reseller_created' AND user_id = $1 AND subject_id = $2", saID, rid); n != 1 {
			t.Fatal("журнал платформы superadmin.reseller_created")
		}
		if n := e.scalar("SELECT count(*) FROM audit_log WHERE action = 'reseller.created' AND subject_id = $1 AND actor_user_id = $2", rid, saID); n != 1 {
			t.Fatal("audit_log reseller.created")
		}

		// Существующий аккаунт без пароля: пароль остаётся прежним, реселлером он становится один раз.
		partner := fmt.Sprintf("partner-%d", seq)
		before := e.text("SELECT password FROM users WHERE id = $1", existingID)
		expect(t, e.post(sa, "/api/superadmin/resellers", form("Club Partner", existingPhone, "", partner, 500, 0)), http.StatusCreated, "create existing")
		if n := e.scalar("SELECT count(*) FROM resellers WHERE user_id = $1 AND code = $2 AND commission_bp = 500", existingID, partner); n != 1 {
			t.Fatal("реселлер существующего аккаунта")
		}
		if after := e.text("SELECT password FROM users WHERE id = $1", existingID); after != before {
			t.Fatal("пароль существующего аккаунта изменился")
		}
		expectField(t, e.post(sa, "/api/superadmin/resellers", form("Again", existingPhone, "", fmt.Sprintf("again-%d", seq), 500, 0)), "phone", "Аккаунт с этим телефоном уже реселлер.")

		// Не суперадмин реселлеров не создаёт и не видит.
		owner := e.login(existingPhone)
		expect(t, e.post(owner, "/api/superadmin/resellers", form("X", "7001112233", "secret12", fmt.Sprintf("xx-%d", seq), 9000, 90)), http.StatusForbidden, "create by owner")
		expect(t, e.get(owner, "/api/superadmin/resellers"), http.StatusForbidden, "list by owner")
		expect(t, e.get("", "/api/superadmin/resellers"), http.StatusUnauthorized, "list by guest")
	})

	// «only resellers sign in on the reseller host; there is no sign-up there»
	t.Run("GS-561 на хосте реселлера входят только реселлеры, регистрации нет", func(t *testing.T) {
		e := newResellerEnv(t, now, 1, resellerTestHost)
		_, code, _, phone := makeReseller(e, 1000, 15, true)
		_, ownerPhone := e.member("Владелец", "owner", "", false)
		login := func(phone string) resp {
			return e.onHost(resellerTestHost, "POST", "", "/api/auth/login", map[string]any{"phone": phone, "password": "pw"})
		}

		// Не реселлер (сотрудник клуба с верным паролем) — ошибка поля phone, сессии нет (журнал неудачи с причиной
		// not_reseller — internal/auth TestResellerHostLogin).
		r := login(ownerPhone)
		if r.status != http.StatusUnprocessableEntity || fieldError(r, "phone") != "Здесь входят только реселлеры Zafiro." || len(r.cookies) != 0 {
			t.Fatalf("вход не реселлера: %d %v", r.status, r.body)
		}
		// Реселлер входит паролем (как в старом — без второго фактора) и попадает в кабинет: сессия без клуба.
		r = login(phone)
		expect(t, r, http.StatusOK, "вход реселлера")
		tok := r.body["access_token"].(string)
		if me := obj(r.body["me"]); me["club"] != nil {
			t.Fatalf("сессия реселлера с клубом: %v", me)
		}
		r = e.onHost(resellerTestHost, "GET", tok, "/api/reseller/dashboard", nil)
		expect(t, r, http.StatusOK, "кабинет")
		if r.body["code"] != code || num(obj(r.body["clubs"])["total"]) != 0 || len(obj(r.body["clubs"])["items"].([]any)) != 0 {
			t.Fatalf("кабинет: %v", r.body) // «Пока ни один клуб не ввёл ваш код.»
		}
		// Панели клуба, регистрации клуба и кабинета суперадмина на этом хосте нет.
		for _, p := range []string{"/api/layout/badges", "/api/settings", "/api/billing", "/api/superadmin/resellers"} {
			if r := e.onHost(resellerTestHost, "GET", tok, p, nil); r.status != http.StatusForbidden {
				t.Fatalf("%s на хосте реселлера: %d", p, r.status)
			}
		}
		if r := e.onHost(resellerTestHost, "POST", "", "/api/auth/register", map[string]any{"name": "X"}); r.status != http.StatusForbidden {
			t.Fatalf("регистрация на хосте реселлера: %d", r.status)
		}
		// На других хостах кабинета реселлера нет; вход там — по обычным правилам (у реселлера нет клубов).
		if r := e.get(tok, "/api/reseller/dashboard"); r.status != http.StatusNotFound {
			t.Fatalf("кабинет на хосте панели: %d", r.status)
		}
		r = do(t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": phone, "password": "pw"}, nil)
		if r.status != http.StatusUnprocessableEntity || fieldError(r, "phone") != "У аккаунта нет доступа ни к одному клубу." {
			t.Fatalf("вход реселлера на хосте панели: %d %v", r.status, r.body)
		}
		// Cookie сессии — только этого хоста (__Host-/__Secure- без Domain), CSP — WebSocket своего хоста.
		r = login(phone)
		for _, c := range r.cookies {
			if c.Domain != "" {
				t.Fatalf("cookie %s с Domain=%s", c.Name, c.Domain)
			}
		}
		if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "wss://"+resellerTestHost) {
			t.Fatalf("CSP: %s", csp)
		}

		// Суперадмин-реселлер и на этом хосте проходит второй фактор (его сессия несёт права суперадмина).
		saID, saPhone := e.member("Супер Реселлер", "", "", true)
		e.exec(`INSERT INTO resellers (user_id, code, name, created_at, updated_at) VALUES ($1, $2, 'SA', now(), now())`, saID, fmt.Sprintf("sa-%d", adminSeq.Add(1)))
		if r := login(saPhone); r.status != http.StatusAccepted || r.body["challenge"] == nil {
			t.Fatalf("суперадмин-реселлер без второго фактора: %d %v", r.status, r.body)
		}
		// Заблокированный реселлер не входит.
		e.exec("UPDATE users SET blocked_at = now() WHERE phone = $1", phone)
		if r := login(phone); r.status != http.StatusUnprocessableEntity {
			t.Fatalf("заблокированный реселлер: %d", r.status)
		}
	})

	// «a club enters the reseller code once; unknown or stopped codes are refused»
	t.Run("GS-562 клуб вводит код один раз; неизвестный и остановленный код не принимаются", func(t *testing.T) {
		e := newResellerEnv(t, now, 1, "")
		e.subscribe("trialing", &trialEnds, nil)
		ownerID, owner := e.staff("Владелец", "owner")
		_, cashier := e.staff("Кассир", "cashier")
		_, stopped, _, _ := makeReseller(e, 1000, 30, false)
		rid, code, _, _ := makeReseller(e, 1000, 15, true)
		enter := func(tok, code string) resp {
			return e.post(tok, "/api/settings/reseller", map[string]any{"reseller_code": code})
		}
		attachedTo := func() int64 { return e.scalar("SELECT coalesce(reseller_id, 0) FROM clubs WHERE id = $1", e.club) }

		if r := e.get(owner, "/api/settings/reseller"); r.status != http.StatusOK || r.body["reseller"] != nil {
			t.Fatalf("карточка без реселлера: %d %v", r.status, r.body)
		}
		const notFound = "Реселлер с таким кодом не найден."
		expectField(t, enter(owner, "nobody"), "reseller_code", notFound)
		expectField(t, enter(owner, stopped), "reseller_code", notFound)
		expectField(t, enter(owner, ""), "reseller_code", notFound)
		expectField(t, e.post(owner, "/api/settings/reseller", map[string]any{}), "reseller_code", notFound)
		// Право settings.update: кассир код не вводит.
		expect(t, enter(cashier, code), http.StatusForbidden, "кассир")
		if attachedTo() != 0 {
			t.Fatal("клуб привязан при ошибке")
		}

		// Регистр и пробелы по краям не важны.
		r := enter(owner, " "+strings.ToUpper(code)+" ")
		expect(t, r, http.StatusOK, "привязка")
		if r.body["message"] != "Клуб привязан к реселлеру Arman." || attachedTo() != rid {
			t.Fatalf("привязка: %v, reseller_id=%d", r.body, attachedTo())
		}
		if n := e.scalar("SELECT count(*) FROM clubs WHERE id = $1 AND reseller_attached_at = $2", e.club, now); n != 1 {
			t.Fatal("reseller_attached_at")
		}
		// «Клуб подключён через реселлера Arman (код arman-games).» + скидка.
		card := obj(e.get(owner, "/api/settings/reseller").body["reseller"])
		if card["name"] != "Arman" || card["code"] != code || num(card["discount_percent"]) != 15 || card["commission_bp"] != nil {
			t.Fatalf("карточка: %v", card)
		}
		if e.activityCount("club.reseller_attached", e.club) != 1 {
			t.Fatal("журнал клуба club.reseller_attached")
		}
		if n := e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'club.reseller_attached' AND user_id = $2", e.club, ownerID); n != 1 {
			t.Fatal("автор записи журнала")
		}
		if n := e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND action = 'reseller.attached' AND actor_user_id = $2", e.club, ownerID); n != 1 {
			t.Fatal("audit_log reseller.attached")
		}

		// Повторный ввод — ошибка, привязка прежняя (даже остановленным или тем же кодом).
		const already = "Клуб уже привязан к реселлеру. Сменить реселлера можно через поддержку Zafiro."
		expectField(t, enter(owner, stopped), "reseller_code", already)
		expectField(t, enter(owner, code), "reseller_code", already)
		if attachedTo() != rid || e.activityCount("club.reseller_attached", e.club) != 1 {
			t.Fatal("повторный ввод изменил привязку")
		}
		// Остановленный реселлер: клуб остаётся его клубом, но текста о скидке нет.
		e.exec("UPDATE resellers SET is_active = false WHERE id = $1", rid)
		if card := obj(e.get(owner, "/api/settings/reseller").body["reseller"]); card["code"] != code || num(card["discount_percent"]) != 0 {
			t.Fatalf("карточка остановленного: %v", card)
		}

		// throttle:10,1 — подбор кода: не больше 10 попыток в минуту (выше владелец сделал 7).
		passed := 0
		for passed < 5 && enter(owner, "guess").status != http.StatusTooManyRequests {
			passed++
		}
		if passed > 3 {
			t.Fatalf("лимит попыток не сработал: после 7 запросов прошло ещё %d", passed)
		}
	})

	t.Run("код реселлера: режим просмотра суперадмина и клуб без подписки", func(t *testing.T) {
		e := newResellerEnv(t, now, 1, "")
		_, owner := e.staff("Владелец", "owner")
		_, code, _, _ := makeReseller(e, 1000, 15, true)
		// settings.reseller — в группе subscription: без подписки — 402.
		if r := e.post(owner, "/api/settings/reseller", map[string]any{"reseller_code": code}); r.status != http.StatusPaymentRequired {
			t.Fatalf("без подписки: %d %v", r.status, r.body)
		}
		e.subscribe("trialing", &trialEnds, nil)
		_, sa, _ := superadminWithPhone(e)
		expect(t, e.post(sa, fmt.Sprintf("/api/superadmin/clubs/%d/view", e.club), nil), http.StatusOK, "view")
		r := e.post(sa, "/api/settings/reseller", map[string]any{"reseller_code": code})
		if r.status != http.StatusForbidden || r.body["error"] != "read_only" {
			t.Fatalf("режим просмотра: %d %v", r.status, r.body)
		}
		// И раздел «Реселлеры» кабинета в режиме просмотра только читается.
		expect(t, e.get(sa, "/api/superadmin/resellers"), http.StatusOK, "list in view mode")
		r = e.post(sa, "/api/superadmin/resellers", map[string]any{"name": "X", "phone": "7001112233", "password": "secret12", "code": "xx-view", "commission_bp": 0, "discount_percent": 0})
		if r.status != http.StatusForbidden || r.body["error"] != "read_only" {
			t.Fatalf("создание в режиме просмотра: %d %v", r.status, r.body)
		}
		if n := e.scalar("SELECT count(*) FROM clubs WHERE id = $1 AND reseller_id IS NOT NULL", e.club); n != 0 {
			t.Fatal("клуб привязан из режима просмотра")
		}
	})

	// «a club of a reseller pays with the reseller discount and the reseller earns their percent» — сквозной путь:
	// код в настройках → счёт со скидкой → оплата → начисление → выплата → кабинеты суперадмина и реселлера.
	t.Run("GS-563 клуб реселлера платит со скидкой, реселлер получает процент", func(t *testing.T) {
		e := newResellerEnv(t, now, 2, "")
		e.subscribe("trialing", &trialEnds, nil)
		// Своя скидка клуба меньше — действует скидка реселлера.
		e.exec("UPDATE club_subscriptions SET discount_percent = 5 WHERE club_id = $1", e.club)
		rid, code, _, phone := makeReseller(e, 1000, 20, true)
		_, owner := e.staff("Владелец", "owner")
		saID, sa, saPhone := superadminWithPhone(e)
		reseller := fmt.Sprintf("/api/superadmin/resellers/%d", rid)

		expect(t, e.post(owner, "/api/settings/reseller", map[string]any{"reseller_code": code}), http.StatusOK, "код реселлера")
		// 1 000 ₸ × 2 ПК × 3 месяца − 20 % = 4 800 ₸.
		id := e.invoiceID(e.open(owner, 3))
		if n := e.scalar("SELECT count(*) FROM subscription_invoices WHERE id = $1 AND amount = 480000 AND discount_percent = 20 AND reseller_id = $2 AND reseller_bp = 1000", id, rid); n != 1 {
			t.Fatal("счёт со скидкой и снимком реселлера")
		}
		// До оплаты начислений нет.
		if c := e.resellerCard(sa, code); num(c["earned_tiyn"]) != 0 || num(c["balance_tiyn"]) != 0 {
			t.Fatalf("до оплаты: %v", c)
		}
		expect(t, e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil), http.StatusOK, "оплата")
		// 4 800 ₸ × 10 % = 480 ₸.
		if n := e.scalar("SELECT count(*) FROM subscription_invoices WHERE id = $1 AND status = 'paid' AND reseller_amount = 48000", id); n != 1 {
			t.Fatal("начисление реселлеру")
		}

		// Выплата 300 ₸ («Kaspi»): нужен свежий код второго фактора и ключ идемпотентности формы.
		payout := map[string]any{"amount_tiyn": 30000, "note": "Kaspi"}
		if r := e.postKey(sa, reseller+"/payouts", "", payout); r.status != http.StatusBadRequest {
			t.Fatalf("выплата без Idempotency-Key: %d %v", r.status, r.body)
		}
		r := e.post(sa, reseller+"/payouts", payout)
		if r.status != http.StatusUnprocessableEntity || fieldError(r, "code") == "" {
			t.Fatalf("выплата без кода: %d %v", r.status, r.body)
		}
		payout["code"] = "000000"
		if r := e.post(sa, reseller+"/payouts", payout); r.status != http.StatusUnprocessableEntity || fieldError(r, "code") == "" {
			t.Fatalf("выплата с неверным кодом: %d %v", r.status, r.body)
		}
		for field, bad := range map[string]map[string]any{
			"amount": {"amount_tiyn": 0, "code": "123456"},
			"note":   {"amount_tiyn": 100, "note": strings.Repeat("я", 256), "code": "123456"},
		} {
			if r := e.post(sa, reseller+"/payouts", bad); r.status != http.StatusUnprocessableEntity || fieldError(r, field) == "" {
				t.Fatalf("выплата, поле %s: %d %v", field, r.status, r.body)
			}
		}
		if r := e.post(sa, reseller+"/payouts", map[string]any{"amount_tiyn": 99_999_999_901, "code": "123456"}); fieldError(r, "amount") == "" {
			t.Fatalf("выплата больше предела: %v", r.body)
		}
		if n := e.scalar("SELECT count(*) FROM reseller_payouts WHERE reseller_id = $1", rid); n != 0 {
			t.Fatal("выплата записана при ошибке")
		}
		key := newKey()
		payout["code"] = sfCode(t, saPhone)
		r = e.postKey(sa, reseller+"/payouts", key, payout)
		expect(t, r, http.StatusOK, "выплата")
		if r.body["message"] != "Выплата реселлеру "+code+" записана." {
			t.Fatalf("сообщение: %v", r.body)
		}
		pid := e.scalar("SELECT id FROM reseller_payouts WHERE reseller_id = $1 AND amount = 30000 AND note = 'Kaspi' AND created_by = $2", rid, saID)
		// Журнал денег платформы — в той же транзакции: reseller_payout:<id выплаты>.
		if n := e.scalar(`SELECT count(*) FROM platform_money_log WHERE idempotency_key = $1 AND kind = 'reseller_payout' AND amount = 30000
			AND reseller_id = $2 AND payout_id = $3 AND actor_user_id = $4 AND club_id IS NULL`, fmt.Sprintf("reseller_payout:%d", pid), rid, pid, saID); n != 1 {
			t.Fatal("platform_money_log reseller_payout")
		}
		if n := e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.reseller_payout' AND subject_id = $1 AND description LIKE '%300.00 ₸' AND (details->>'amount')::numeric = 300", rid); n != 1 {
			t.Fatal("журнал платформы superadmin.reseller_payout")
		}
		if n := e.scalar("SELECT count(*) FROM audit_log WHERE category = 'money' AND action = 'reseller.payout' AND subject_id = $1", pid); n != 1 {
			t.Fatal("audit_log reseller.payout")
		}
		// Двойной клик: тот же ключ и то же тело — второй выплаты нет; тот же ключ с другой суммой — 409.
		payout["code"] = sfCode(t, saPhone)
		expect(t, e.postKey(sa, reseller+"/payouts", key, payout), http.StatusOK, "повтор выплаты")
		other := map[string]any{"amount_tiyn": 30001, "note": "Kaspi", "code": sfCode(t, saPhone)}
		expect(t, e.postKey(sa, reseller+"/payouts", key, other), http.StatusConflict, "тот же ключ, другая сумма")
		if n := e.scalar("SELECT count(*) FROM reseller_payouts WHERE reseller_id = $1", rid); n != 1 {
			t.Fatalf("выплат: %d", n)
		}
		if n := e.scalar("SELECT count(*) FROM platform_money_log WHERE kind = 'reseller_payout' AND reseller_id = $1", rid); n != 1 {
			t.Fatalf("строк журнала денег о выплате: %d", n)
		}

		// Суперадмин: «Заработано 480 ₸, выплачено 300 ₸, к выплате 180 ₸» и клуб реселлера.
		c := e.resellerCard(sa, code)
		if num(c["earned_tiyn"]) != 48000 || num(c["paid_out_tiyn"]) != 30000 || num(c["balance_tiyn"]) != 18000 {
			t.Fatalf("карточка реселлера: %v", c)
		}
		clubs, _ := c["clubs"].([]any)
		if len(clubs) != 1 || num(obj(clubs[0])["id"]) != e.club || obj(clubs[0])["attached"] != "20.11.2026" {
			t.Fatalf("клубы реселлера: %v", clubs)
		}

		// Кабинет реселлера: те же суммы до тиына, имя клуба, начисление и выплата.
		// Без reseller.domain реселлер без клубов войти не может — токен выдаётся напрямую через членство клиента.
		e.exec("INSERT INTO club_memberships (club_id, user_id, role, created_at, updated_at) SELECT $1, id, 'cashier', now(), now() FROM users WHERE phone = $2", e.club, phone)
		tok := e.login(phone)
		d := e.get(tok, "/api/reseller/dashboard")
		expect(t, d, http.StatusOK, "кабинет реселлера")
		if d.body["code"] != code || num(d.body["commission_bp"]) != 1000 || num(d.body["discount_percent"]) != 20 || d.body["is_active"] != true ||
			num(d.body["earned_tiyn"]) != 48000 || num(d.body["paid_out_tiyn"]) != 30000 || num(d.body["balance_tiyn"]) != 18000 {
			t.Fatalf("кабинет: %v", d.body)
		}
		rows, _ := obj(d.body["clubs"])["items"].([]any)
		if len(rows) != 1 || obj(rows[0])["name"] != clubName(e) || obj(rows[0])["subscription"] != "active" ||
			obj(rows[0])["plan_name"] != "Базовый" || obj(rows[0])["attached"] != "20.11.2026" {
			t.Fatalf("клубы кабинета: %v", rows)
		}
		earnings, _ := obj(d.body["earnings"])["items"].([]any)
		if len(earnings) != 1 || num(obj(earnings[0])["amount_tiyn"]) != 480000 || num(obj(earnings[0])["reseller_amount_tiyn"]) != 48000 ||
			num(obj(earnings[0])["reseller_bp"]) != 1000 || num(obj(earnings[0])["months"]) != 3 || obj(earnings[0])["paid"] != "20.11.2026" ||
			obj(earnings[0])["club_name"] != clubName(e) {
			t.Fatalf("начисления: %v", earnings)
		}
		payouts, _ := d.body["payouts"].([]any)
		if len(payouts) != 1 || num(obj(payouts[0])["amount_tiyn"]) != 30000 || obj(payouts[0])["note"] != "Kaspi" || obj(payouts[0])["date"] != "20.11.2026" {
			t.Fatalf("выплаты: %v", payouts)
		}

		// GAP-053: выплата больше остатка не запрещена — «к выплате» становится отрицательным.
		expect(t, e.post(sa, reseller+"/payouts", map[string]any{"amount_tiyn": 50001, "code": sfCode(t, saPhone)}), http.StatusOK, "выплата больше остатка")
		if c := e.resellerCard(sa, code); num(c["paid_out_tiyn"]) != 80001 || num(c["balance_tiyn"]) != -32001 {
			t.Fatalf("после выплаты больше остатка: %v", c)
		}
		if d := e.get(tok, "/api/reseller/dashboard"); num(d.body["balance_tiyn"]) != -32001 {
			t.Fatalf("кабинет после выплаты больше остатка: %v", d.body["balance_tiyn"])
		}
		// Нет такого реселлера — 404.
		expect(t, e.post(sa, "/api/superadmin/resellers/999999999/payouts", map[string]any{"amount_tiyn": 100, "code": sfCode(t, saPhone)}), http.StatusNotFound, "выплата несуществующему")
	})

	// Изоляция: реселлер видит только свои клубы и суммы; не реселлер — 403.
	t.Run("кабинет реселлера: чужие клубы и суммы не видны", func(t *testing.T) {
		e := newResellerEnv(t, now, 1, resellerTestHost)
		ridA, codeA, _, phoneA := makeReseller(e, 1000, 10, true)
		ridB, codeB, _, phoneB := makeReseller(e, 2000, 0, true)
		clubB := e.db.Club(t, fmt.Sprintf("rslb%d", adminSeq.Add(1)))
		e.exec("UPDATE clubs SET reseller_id = $2, reseller_attached_at = now() WHERE id = $1", e.club, ridA)
		e.exec("UPDATE clubs SET reseller_id = $2, reseller_attached_at = now() WHERE id = $1", clubB, ridB)
		invoice := func(club, rid, amount, bp, earning int64) {
			e.exec(`INSERT INTO subscription_invoices (club_id, saas_plan_id, months, computers, price_per_computer, discount_percent, amount,
				reseller_id, reseller_bp, reseller_amount, status, paid_at, created_at, updated_at)
				VALUES ($1, $2, 1, 1, $3, 0, $3, $4, $5, $6, 'paid', $7, $7, $7)`, club, e.plan, amount, rid, bp, earning, now)
		}
		invoice(e.club, ridA, 100000, 1000, 10000)
		invoice(clubB, ridB, 700000, 2000, 140000)
		e.exec("INSERT INTO reseller_payouts (reseller_id, amount, note, created_at, updated_at) VALUES ($1, 2500, 'A', now(), now())", ridA)
		e.exec("INSERT INTO reseller_payouts (reseller_id, amount, note, created_at, updated_at) VALUES ($1, 77700, 'B', now(), now())", ridB)

		login := func(phone string) string {
			r := e.onHost(resellerTestHost, "POST", "", "/api/auth/login", map[string]any{"phone": phone, "password": "pw"})
			expect(t, r, http.StatusOK, "вход реселлера")
			return r.body["access_token"].(string)
		}
		cabinet := func(tok, query string) resp {
			return e.onHost(resellerTestHost, "GET", tok, "/api/reseller/dashboard"+query, nil)
		}
		// Вход — по разу на аккаунт (лимит входа — 5 в минуту на телефон).
		tokA, tokB := login(phoneA), login(phoneB)
		a := cabinet(tokA, "")
		expect(t, a, http.StatusOK, "кабинет A")
		if a.body["code"] != codeA || num(a.body["earned_tiyn"]) != 10000 || num(a.body["paid_out_tiyn"]) != 2500 || num(a.body["balance_tiyn"]) != 7500 {
			t.Fatalf("кабинет A: %v", a.body)
		}
		raw := fmt.Sprint(a.body)
		for _, foreign := range []string{codeB, "140000", "700000", "77700", e.text("SELECT name FROM clubs WHERE id = $1", clubB)} {
			if strings.Contains(raw, foreign) {
				t.Fatalf("кабинет A показывает чужое %q: %v", foreign, a.body)
			}
		}
		if n := len(obj(a.body["clubs"])["items"].([]any)); n != 1 || num(obj(a.body["clubs"])["total"]) != 1 {
			t.Fatalf("клубов в кабинете A: %d", n)
		}
		// Параметры запроса не выбирают другого реселлера или клуб: кабинет строится только по аккаунту сессии.
		for _, q := range []string{fmt.Sprintf("?reseller_id=%d", ridB), fmt.Sprintf("?club_id=%d", clubB), fmt.Sprintf("?user_id=%d&reseller=%s", ridB, codeB)} {
			if r := cabinet(tokA, q); num(r.body["earned_tiyn"]) != 10000 || r.body["code"] != codeA {
				t.Fatalf("кабинет A с %s: %v", q, r.body)
			}
		}
		b := cabinet(tokB, "")
		if b.body["code"] != codeB || num(b.body["earned_tiyn"]) != 140000 || num(b.body["balance_tiyn"]) != 62300 || strings.Contains(fmt.Sprint(b.body), codeA) {
			t.Fatalf("кабинет B: %v", b.body)
		}
		// Страница за пределами списка — пустая, а не чужая.
		if r := cabinet(tokA, "?clubs_page=5&earnings_page=5"); len(obj(r.body["clubs"])["items"].([]any)) != 0 || len(obj(r.body["earnings"])["items"].([]any)) != 0 || num(r.body["earned_tiyn"]) != 10000 {
			t.Fatalf("дальняя страница: %v", r.body)
		}
		// Гость — 401.
		expect(t, cabinet("", ""), http.StatusUnauthorized, "кабинет без входа")
		// Аккаунт перестал быть реселлером (удалён суперадмином) — действующая сессия кабинета не открывает: 403.
		e.exec("DELETE FROM resellers WHERE id = $1", ridA)
		r := cabinet(tokA, "")
		if r.status != http.StatusForbidden || r.body["message"] != "Этот аккаунт не реселлер Zafiro." {
			t.Fatalf("кабинет бывшего реселлера: %d %v", r.status, r.body)
		}
	})

	t.Run("кабинет реселлера без reseller.domain: только реселлер, без проверки подписки клуба", func(t *testing.T) {
		e := newResellerEnv(t, now, 1, "")
		_, owner := e.staff("Владелец", "owner") // клуб без подписки: панель отвечает 402
		r := e.get(owner, "/api/reseller/dashboard")
		if r.status != http.StatusForbidden || r.body["message"] != "Этот аккаунт не реселлер Zafiro." {
			t.Fatalf("не реселлер: %d %v", r.status, r.body)
		}
		ownerID := e.scalar("SELECT user_id FROM club_memberships WHERE club_id = $1 AND role = 'owner'", e.club)
		e.exec("INSERT INTO resellers (user_id, code, name, is_active, created_at, updated_at) VALUES ($1, $2, 'Own', false, now(), now())", ownerID, fmt.Sprintf("own-%d", adminSeq.Add(1)))
		r = e.get(owner, "/api/reseller/dashboard")
		if r.status != http.StatusOK || r.body["is_active"] != false {
			t.Fatalf("реселлер с клубом без подписки: %d %v", r.status, r.body)
		}
	})

	// «the superadmin sets the percent and discount, stops a code and unlinks a club; others cannot»
	t.Run("GS-564 суперадмин задаёт процент и скидку, останавливает код, отвязывает клуб", func(t *testing.T) {
		e := newResellerEnv(t, now, 1, "")
		e.subscribe("trialing", &trialEnds, nil)
		rid, code, _, _ := makeReseller(e, 0, 20, true)
		e.exec("UPDATE clubs SET reseller_id = $2, reseller_attached_at = now() WHERE id = $1", e.club, rid)
		_, owner := e.staff("Владелец", "owner")
		saID, sa, _ := superadminWithPhone(e)
		path := fmt.Sprintf("/api/superadmin/resellers/%d", rid)

		expect(t, e.patch(owner, path, map[string]any{"commission_bp": 5000, "discount_percent": 50, "is_active": true}), http.StatusForbidden, "изменение не суперадмином")
		if num(e.get(owner, "/api/billing").body["discount_percent"]) != 20 {
			t.Fatal("скидка действующего реселлера")
		}
		for field, bad := range map[string]map[string]any{
			"commission_percent": {"commission_bp": -1, "discount_percent": 10, "is_active": true},
			"discount_percent":   {"commission_bp": 100, "discount_percent": 101, "is_active": true},
		} {
			if r := e.patch(sa, path, bad); r.status != http.StatusUnprocessableEntity || fieldError(r, field) == "" {
				t.Fatalf("поле %s: %d %v", field, r.status, r.body)
			}
		}
		// 12,5 % = 1250 базисных пунктов; код остановлен.
		r := e.patch(sa, path, map[string]any{"commission_bp": 1250, "discount_percent": 10, "is_active": false})
		expect(t, r, http.StatusOK, "изменение")
		if r.body["message"] != "Реселлер "+code+" сохранён." {
			t.Fatalf("сообщение: %v", r.body)
		}
		if n := e.scalar("SELECT count(*) FROM resellers WHERE id = $1 AND commission_bp = 1250 AND discount_percent = 10 AND NOT is_active", rid); n != 1 {
			t.Fatal("условия реселлера")
		}
		if d := e.text("SELECT details::text FROM platform_logs WHERE event = 'superadmin.reseller_updated' AND subject_id = $1", rid); !strings.Contains(d, `"commission_percent": "12.5"`) || !strings.Contains(d, `"discount_percent": "10"`) || !strings.Contains(d, `"is_active": ""`) {
			t.Fatalf("детали журнала: %s", d)
		}
		if n := e.scalar("SELECT count(*) FROM audit_log WHERE category = 'tariff' AND action = 'reseller.updated' AND subject_id = $1 AND actor_user_id = $2", rid, saID); n != 1 {
			t.Fatal("audit_log reseller.updated")
		}
		// То же ещё раз — изменений нет, журнал не растёт.
		expect(t, e.patch(sa, path, map[string]any{"commission_bp": 1250, "discount_percent": 10, "is_active": false}), http.StatusOK, "без изменений")
		if n := e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.reseller_updated' AND subject_id = $1", rid); n != 1 {
			t.Fatalf("записей журнала об изменении: %d", n)
		}
		// Остановленный код не даёт скидки: discountFor(клуб) = 0.
		if d := num(e.get(owner, "/api/billing").body["discount_percent"]); d != 0 {
			t.Fatalf("скидка остановленного реселлера: %d", d)
		}
		expect(t, e.patch(sa, "/api/superadmin/resellers/999999999", map[string]any{"commission_bp": 1, "discount_percent": 1, "is_active": true}), http.StatusNotFound, "нет реселлера")

		// Отвязка: чужой клуб — 404; не суперадмин — 403; свой — reseller_id = NULL.
		other := e.db.Club(t, fmt.Sprintf("rslo%d", adminSeq.Add(1)))
		expect(t, e.del(sa, fmt.Sprintf("%s/clubs/%d", path, other), nil), http.StatusNotFound, "отвязка чужого клуба")
		expect(t, e.del(owner, fmt.Sprintf("%s/clubs/%d", path, e.club), nil), http.StatusForbidden, "отвязка не суперадмином")
		r = e.del(sa, fmt.Sprintf("%s/clubs/%d", path, e.club), nil)
		expect(t, r, http.StatusOK, "отвязка")
		if n := e.scalar("SELECT count(*) FROM clubs WHERE id = $1 AND reseller_id IS NULL AND reseller_attached_at IS NULL", e.club); n != 1 {
			t.Fatal("клуб не отвязан")
		}
		if n := e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.reseller_detached' AND club_id = $1 AND subject_id = $2", e.club, rid); n != 1 {
			t.Fatal("журнал платформы superadmin.reseller_detached")
		}
		expect(t, e.del(sa, fmt.Sprintf("%s/clubs/%d", path, e.club), nil), http.StatusNotFound, "повторная отвязка")
		// После отвязки клуб может ввести код снова (карточка показывает форму).
		if r := e.get(owner, "/api/settings/reseller"); r.body["reseller"] != nil {
			t.Fatalf("карточка после отвязки: %v", r.body)
		}
	})

	// BR-189, GAP-054: отвязка после выставления счёта — счёт хранит снимок, реселлер получает процент.
	t.Run("GAP-054 клуб отвязан между выставлением и оплатой счёта: начисление по снимку", func(t *testing.T) {
		e := newResellerEnv(t, now, 1, "")
		e.subscribe("trialing", &trialEnds, nil)
		rid, code, _, _ := makeReseller(e, 1235, 15, true)
		_, owner := e.staff("Владелец", "owner")
		_, sa, _ := superadminWithPhone(e)
		expect(t, e.post(owner, "/api/settings/reseller", map[string]any{"reseller_code": code}), http.StatusOK, "код")
		id := e.invoiceID(e.open(owner, 1)) // 1000 ₸ − 15 % = 850 ₸
		expect(t, e.del(sa, fmt.Sprintf("/api/superadmin/resellers/%d/clubs/%d", rid, e.club), nil), http.StatusOK, "отвязка")
		expect(t, e.post(sa, fmt.Sprintf("/api/superadmin/invoices/%d/pay", id), nil), http.StatusOK, "оплата")
		// 850 ₸ × 12,35 % = 104,975 → 104,98 ₸.
		if c := e.resellerCard(sa, code); num(c["earned_tiyn"]) != 10498 || len(c["clubs"].([]any)) != 0 {
			t.Fatalf("начисление по снимку: %v", c)
		}
		// Новый счёт клуба — уже без скидки и реселлера.
		id2 := e.invoiceID(e.open(owner, 1))
		if n := e.scalar("SELECT count(*) FROM subscription_invoices WHERE id = $1 AND amount = 100000 AND discount_percent = 0 AND reseller_id IS NULL", id2); n != 1 {
			t.Fatal("счёт после отвязки")
		}
	})

	// «the superadmin deletes a reseller: the account stays, clubs are unlinked, payouts go»
	t.Run("GS-565 удаление реселлера: аккаунт остаётся, клубы отвязаны, выплат нет", func(t *testing.T) {
		e := newResellerEnv(t, now, 1, "")
		rid, code, userID, phone := makeReseller(e, 1000, 15, true)
		e.exec("UPDATE clubs SET reseller_id = $2, reseller_attached_at = now() WHERE id = $1", e.club, rid)
		_, owner := e.staff("Владелец", "owner")
		saID, sa, saPhone := superadminWithPhone(e)
		path := fmt.Sprintf("/api/superadmin/resellers/%d", rid)
		// Оплаченный счёт с начислением и выплата 100 ₸, записанная через кабинет (со строкой журнала денег).
		e.exec(`INSERT INTO subscription_invoices (club_id, saas_plan_id, months, computers, price_per_computer, discount_percent, amount,
			reseller_id, reseller_bp, reseller_amount, status, paid_at, created_at, updated_at)
			VALUES ($1, $2, 1, 1, 100000, 15, 85000, $3, 1000, 8500, 'paid', $4, $4, $4)`, e.club, e.plan, rid, now)
		expect(t, e.post(sa, path+"/payouts", map[string]any{"amount_tiyn": 10000, "code": sfCode(t, saPhone)}), http.StatusOK, "выплата")

		expect(t, e.del(owner, path, map[string]any{"code": "123456"}), http.StatusForbidden, "удаление не суперадмином")
		// Удаление необратимо: без свежего кода второго фактора — 422.
		if r := e.del(sa, path, map[string]any{"code": ""}); r.status != http.StatusUnprocessableEntity || fieldError(r, "code") == "" {
			t.Fatalf("удаление без кода: %d %v", r.status, r.body)
		}
		if n := e.scalar("SELECT count(*) FROM resellers WHERE id = $1", rid); n != 1 {
			t.Fatal("реселлер удалён без кода")
		}
		r := e.del(sa, path, map[string]any{"code": sfCode(t, saPhone)})
		expect(t, r, http.StatusOK, "удаление")
		if r.body["message"] != "Реселлер "+code+" удалён." {
			t.Fatalf("сообщение: %v", r.body)
		}
		if e.scalar("SELECT count(*) FROM resellers WHERE id = $1", rid) != 0 || e.scalar("SELECT count(*) FROM users WHERE id = $1", userID) != 1 ||
			e.scalar("SELECT count(*) FROM clubs WHERE id = $1 AND reseller_id IS NULL AND reseller_attached_at IS NULL", e.club) != 1 ||
			e.scalar("SELECT count(*) FROM reseller_payouts WHERE reseller_id = $1", rid) != 0 {
			t.Fatal("удаление: реселлер, аккаунт, клуб или выплаты")
		}
		// В счёте реселлера больше нет, начисленное остаётся; суммы выплаты и её автор — в журнале денег платформы (UQ-10).
		if n := e.scalar("SELECT count(*) FROM subscription_invoices WHERE club_id = $1 AND reseller_id IS NULL AND reseller_amount = 8500", e.club); n != 1 {
			t.Fatal("счёт после удаления реселлера")
		}
		if n := e.scalar("SELECT count(*) FROM platform_money_log WHERE kind = 'reseller_payout' AND reseller_id = $1 AND amount = 10000 AND actor_user_id = $2", rid, saID); n != 1 {
			t.Fatal("журнал денег платформы потерял выплату удалённого реселлера")
		}
		if d := e.text("SELECT details::text FROM platform_logs WHERE event = 'superadmin.reseller_deleted' AND user_id = $1 ORDER BY id DESC LIMIT 1", saID); !strings.Contains(d, code) || !strings.Contains(d, `"clubs": 1`) {
			t.Fatalf("журнал платформы superadmin.reseller_deleted: %s", d)
		}
		if n := e.scalar("SELECT count(*) FROM audit_log WHERE action = 'reseller.deleted' AND subject_id = $1", rid); n != 1 {
			t.Fatal("audit_log reseller.deleted")
		}
		expect(t, e.del(sa, path, map[string]any{"code": sfCode(t, saPhone)}), http.StatusNotFound, "повторное удаление")

		// Тот же аккаунт снова становится реселлером с тем же кодом; прежние суммы к нему не возвращаются.
		expect(t, e.post(sa, "/api/superadmin/resellers", map[string]any{"name": "Arman", "phone": phone, "code": code, "commission_bp": 1000, "discount_percent": 15}), http.StatusCreated, "снова реселлер")
		if c := e.resellerCard(sa, code); num(c["earned_tiyn"]) != 0 || num(c["paid_out_tiyn"]) != 0 || len(c["clubs"].([]any)) != 0 {
			t.Fatalf("новый реселлер того же аккаунта: %v", c)
		}
	})

	t.Run("список реселлеров: поиск по коду и имени, страницы", func(t *testing.T) {
		e := newResellerEnv(t, now, 1, "")
		_, sa, _ := superadminWithPhone(e)
		_, code, _, _ := makeReseller(e, 1000, 15, true)
		mark := fmt.Sprintf("Поиск%d", adminSeq.Add(1))
		e.exec("UPDATE resellers SET name = $2 WHERE code = $1", code, mark+" Партнёр")
		for _, q := range []string{strings.ToUpper(code), strings.ToLower(mark), "партнёр"} {
			r := e.get(sa, "/api/superadmin/resellers?q="+q)
			found := false
			for _, it := range r.body["items"].([]any) {
				found = found || obj(it)["code"] == code
			}
			if !found {
				t.Fatalf("поиск %q не нашёл реселлера: %v", q, r.body)
			}
		}
		// Знаки шаблона LIKE — обычные символы.
		if r := e.get(sa, "/api/superadmin/resellers?q=%25_%25"); len(r.body["items"].([]any)) != 0 {
			t.Fatalf("поиск «%%_%%»: %v", r.body)
		}
		r := e.get(sa, "/api/superadmin/resellers")
		if num(r.body["per_page"]) != 30 || num(r.body["page"]) != 1 || num(r.body["total"]) < 1 || !strings.HasSuffix(r.body["host"].(string), "/reseller") {
			t.Fatalf("список: %v", r.body)
		}
		// Новые первыми.
		items := r.body["items"].([]any)
		for i := 1; i < len(items); i++ {
			if num(obj(items[i-1])["id"]) < num(obj(items[i])["id"]) {
				t.Fatal("порядок списка: новые первыми")
			}
		}
	})
}

// §3.6: два одновременных ввода кода — привязка одна, второй получает «Клуб уже привязан…».
func TestResellers_ConcurrentAttach(t *testing.T) {
	now := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	trialEnds := now.Add(5 * 24 * time.Hour)
	e := newResellerEnv(t, now, 1, "")
	e.subscribe("trialing", &trialEnds, nil)
	_, owner := e.staff("Владелец", "owner")
	_, codeA, _, _ := makeReseller(e, 1000, 15, true)
	_, codeB, _, _ := makeReseller(e, 1000, 15, true)
	results := make(chan int, 2)
	for _, code := range []string{codeA, codeB} {
		go func() {
			results <- e.post(owner, "/api/settings/reseller", map[string]any{"reseller_code": code}).status
		}()
	}
	a, b := <-results, <-results
	oneWon := (a == http.StatusOK && b == http.StatusUnprocessableEntity) || (b == http.StatusOK && a == http.StatusUnprocessableEntity)
	if !oneWon {
		t.Fatalf("два ввода кода: %d и %d", a, b)
	}
	if n := e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'club.reseller_attached'", e.club); n != 1 {
		t.Fatalf("записей журнала о привязке: %d", n)
	}
}

// Пул клуба не меняет привязку к реселлеру и не видит чужих реселлеров, выплат и журнала денег (0034, 0038).
func TestResellers_ClubPoolCannotTouch(t *testing.T) {
	now := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	e := newResellerEnv(t, now, 1, "")
	rid, _, _, _ := makeReseller(e, 1000, 15, true)
	ctx := context.Background()
	tx, err := e.db.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", fmt.Sprint(e.club)); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM resellers").Scan(&n); err != nil || n != 0 {
		t.Fatalf("пул клуба видит реселлеров: %d %v", n, err)
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT s"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE clubs SET reseller_id = $2 WHERE id = $1", e.club, rid); err == nil {
		t.Fatal("пул клуба привязал клуб к реселлеру")
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT s"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SELECT count(*) FROM reseller_payouts", "SELECT count(*) FROM platform_money_log"} {
		if err := tx.QueryRow(ctx, q).Scan(&n); err == nil {
			t.Fatalf("пул клуба читает: %s", q)
		}
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT s"); err != nil {
			t.Fatal(err)
		}
	}
}
