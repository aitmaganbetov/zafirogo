//go:build integration

package httpserver

// Этап 7: второй фактор входа суперадмина (TOTP) — HTTP → сервис входа → Postgres (пул zafiro_platform) + Valkey.
// Сценарии — docs/open-questions/stage7-totp.md.

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

	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/mfa"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/totp"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
)

// ---------------------------------------------------------------------------
// Помощники: второй фактор в тестах, где суперадмин просто входит
// ---------------------------------------------------------------------------

// testSecondFactor — второй фактор суперадмина для тестового сервера (ключ данных тестовый).
func testSecondFactor(t *testing.T, platform db.Beginner, vk valkey.Client, lim *ratelimit.Limiter, now ...func() time.Time) *mfa.Manager {
	t.Helper()
	box, err := secretbox.NewKeyring([]secretbox.Key{{ID: "t", Secret: strings.Repeat("k", 32)}}, "")
	if err != nil {
		t.Fatal(err)
	}
	o := mfa.Options{Platform: platform, Valkey: vk, Box: box, Limiter: lim}
	if len(now) > 0 {
		o.Now = now[0]
	}
	m, err := mfa.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var (
	// sfSecrets — ключ TOTP суперадмина по телефону (выдан при настройке в этом прогоне).
	sfSecrets sync.Map
	// sfSteps — последний использованный шаг по телефону: тот же код повторно сервер не примет.
	sfSteps sync.Map
)

// sfCode — код для следующего неиспользованного шага (текущий или следующий из окна ±1; иначе ждёт шаг).
func sfCode(t *testing.T, phone string) string {
	t.Helper()
	v, ok := sfSecrets.Load(phone)
	if !ok {
		t.Fatalf("second factor secret for %s is unknown", phone)
	}
	key, err := totp.DecodeSecret(v.(string))
	if err != nil {
		t.Fatal(err)
	}
	for {
		cur := totp.Step(time.Now())
		step := cur
		if last, ok := sfSteps.Load(phone); ok && last.(int64)+1 > step {
			step = last.(int64) + 1
		}
		if step <= cur+1 {
			sfSteps.Store(phone, step)
			return totp.Code(key, step)
		}
		time.Sleep(time.Until(time.Unix((cur+1)*totp.Period, 0)) + 50*time.Millisecond)
	}
}

// passSecondFactor — ответ 202 входа суперадмина → настройка (если нужна) и код → ответ как у входа 200
// (тело — AuthSession, cookie сессии).
func passSecondFactor(t *testing.T, base, phone string, r resp) resp {
	t.Helper()
	ch, _ := r.body["challenge"].(string)
	if ch == "" {
		t.Fatalf("no challenge: %v", r.body)
	}
	if r.body["setup_required"] == true {
		s := do(t, "POST", base+"/api/auth/second-factor/setup", map[string]any{"challenge": ch}, nil)
		if s.status != http.StatusOK {
			t.Fatalf("second factor setup: %d %v", s.status, s.body)
		}
		sfSecrets.Store(phone, s.body["secret"].(string))
	}
	v := do(t, "POST", base+"/api/auth/second-factor/verify", map[string]any{"challenge": ch, "code": sfCode(t, phone)}, nil)
	if v.status != http.StatusOK {
		t.Fatalf("second factor verify: %d %v", v.status, v.body)
	}
	return resp{status: http.StatusOK, header: v.header, cookies: v.cookies, body: v.body["session"].(map[string]any)}
}

// ---------------------------------------------------------------------------
// Сценарии
// ---------------------------------------------------------------------------

type sfEnv struct {
	*adminEnv
	svc *auth.Service
	sm  *session.Manager
	lim *ratelimit.Limiter
}

// resetTotpLimit — чистый счётчик superadmin-totp пользователя (окно лимита — настоящая минута).
func (e *sfEnv) resetTotpLimit(userID int64) {
	e.t.Helper()
	for _, ip := range []string{"127.0.0.1", "::1"} {
		if err := e.lim.Reset(context.Background(), ratelimit.SuperadminTOTP, ratelimit.Subject{IP: ip, UserID: userID}); err != nil {
			e.t.Fatal(err)
		}
	}
}

// newSFEnv — сервер со вторым фактором; disable — выключенные лимиты (сценарии с многими попытками подряд).
func newSFEnv(t *testing.T, disable ...string) *sfEnv {
	t.Helper()
	return newSFEnvAt(t, nil, disable...)
}

// sfClock — часы второго фактора в сценариях, где кодов нужно много: шаг TOTP (30 с) переводится вручную, а не
// ожиданием настоящего времени.
type sfClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *sfClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Next — следующий шаг TOTP.
func (c *sfClock) Next() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(totp.Period * time.Second)
}

// signInAt — вход суперадмина по часам clock: пароль → настройка (первый вход) → код текущего шага. Возвращает
// access-токен и ключ TOTP. После входа шаг часов использован — следующий код нужен на следующем шаге (clock.Next).
func (e *sfEnv) signInAt(clock *sfClock, phone string) (access, secret string) {
	e.t.Helper()
	r := e.password(phone)
	ch, _ := r.body["challenge"].(string)
	if r.status != http.StatusAccepted || ch == "" {
		e.t.Fatalf("login: %d %v", r.status, r.body)
	}
	if r.body["setup_required"] == true {
		s := e.setup(ch)
		if s.status != http.StatusOK {
			e.t.Fatalf("setup: %d %v", s.status, s.body)
		}
		sfSecrets.Store(phone, s.body["secret"].(string))
	}
	v, _ := sfSecrets.Load(phone)
	secret = v.(string)
	res := e.verify(ch, codeFor(e.t, secret, totp.Step(clock.Now())))
	if res.status != http.StatusOK {
		e.t.Fatalf("verify: %d %v", res.status, res.body)
	}
	return res.body["session"].(map[string]any)["access_token"].(string), secret
}

// newSFEnvAt — как newSFEnv, с часами второго фактора clock (nil — настоящее время).
func newSFEnvAt(t *testing.T, clock *sfClock, disable ...string) *sfEnv {
	t.Helper()
	var now []func() time.Time
	if clock != nil {
		now = append(now, clock.Now)
	}
	d, vk := sharedAdmin(t)
	h, _ := password.New(password.Options{})
	sm, err := session.New(session.Options{Client: vk, Keys: []session.Key{{ID: "t", Secret: bytes.Repeat([]byte{7}, 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	lim, err := ratelimit.New(ratelimit.Options{Client: vk, Prefix: fmt.Sprintf("sf%d:", adminSeq.Add(1)), Disable: disable})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(auth.Options{Pool: d.App, Hasher: h, Sessions: sm, SecondFactor: testSecondFactor(t, d.Platform, vk, lim, now...)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim,
		Pool: d.App, PlatformPool: d.Platform, Presence: &fakePresence{states: map[int64][]presence.State{}},
	}))
	t.Cleanup(srv.Close)
	e := &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("sf%d", adminSeq.Add(1)))}
	return &sfEnv{adminEnv: e, svc: svc, sm: sm, lim: lim}
}

func (e *sfEnv) password(phone string) resp {
	e.t.Helper()
	return do(e.t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": phone, "password": "pw"}, nil)
}

func (e *sfEnv) setup(ch string) resp {
	e.t.Helper()
	return do(e.t, "POST", e.srv.URL+"/api/auth/second-factor/setup", map[string]any{"challenge": ch}, nil)
}

func (e *sfEnv) verify(ch, code string) resp {
	e.t.Helper()
	return do(e.t, "POST", e.srv.URL+"/api/auth/second-factor/verify", map[string]any{"challenge": ch, "code": code}, nil)
}

func (e *sfEnv) logCount(event string, userID int64) int64 {
	return e.scalar("SELECT count(*) FROM platform_logs WHERE event = $1 AND subject_id = $2", event, userID)
}

func codeFor(t *testing.T, secret string, step int64) string {
	t.Helper()
	key, err := totp.DecodeSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	return totp.Code(key, step)
}

// Пароль → без кода сессии нет; принудительная настройка; верный код → сессия; повтор того же кода — отказ;
// код восстановления одноразовый.
func TestSecondFactorLoginFlow(t *testing.T) {
	e := newSFEnv(t, ratelimit.SuperadminTOTP) // лимит — TestSecondFactorRateLimit
	saID, phone := e.member("Супер Админ", "", "", true)

	// 1. Пароль: 202, полувход, без cookie и токена.
	r := e.password(phone)
	if r.status != http.StatusAccepted || r.body["setup_required"] != true || r.body["access_token"] != nil || len(r.cookies) != 0 ||
		num(r.body["expires_in"]) != 300 || r.header.Get("Cache-Control") != "no-store, private" {
		t.Fatalf("password step: %d %v %v", r.status, r.body, r.cookies)
	}
	ch := r.body["challenge"].(string)
	// Сессий у суперадмина нет.
	if n, err := e.sm.ActiveUsers(context.Background(), []int64{saID}, time.Now().Add(-time.Hour)); err != nil || len(n) != 0 {
		t.Fatalf("session before second factor: %v %v", n, err)
	}
	// Код до настройки (ключа нет) — неверный.
	if v := e.verify(ch, "123456"); v.status != http.StatusUnprocessableEntity {
		t.Fatalf("verify before setup: %d %v", v.status, v.body)
	}

	// 2. Настройка: ключ base32 и otpauth://; в базе — только шифротекст.
	s := e.setup(ch)
	secret, _ := s.body["secret"].(string)
	if s.status != http.StatusOK || len(secret) != 32 || !strings.HasPrefix(s.body["otpauth_uri"].(string), "otpauth://totp/Zafiro:") {
		t.Fatalf("setup: %d %v", s.status, s.body)
	}
	var stored string
	if err := e.db.Owner.QueryRow(context.Background(), "SELECT secret FROM superadmin_totp WHERE user_id = $1", saID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "v2:t:") || strings.Contains(stored, secret) {
		t.Fatalf("stored secret %q", stored)
	}
	cur := totp.Step(time.Now())
	// Неверный код — 422, в журнал.
	if v := e.verify(ch, badCode(t, secret)); v.status != http.StatusUnprocessableEntity || v.body["message"] != "Неверный код." {
		t.Fatalf("wrong code: %d %v", v.status, v.body)
	}
	if e.logCount(mfa.EventFailed, saID) < 1 {
		t.Fatal("failed attempt not logged")
	}

	// 3. Верный код: сессия, cookie, 10 кодов восстановления; полувход одноразовый.
	v := e.verify(ch, codeFor(t, secret, cur))
	if v.status != http.StatusOK || cookie(v, RefreshCookie) == nil {
		t.Fatalf("verify: %d %v", v.status, v.body)
	}
	codes, _ := v.body["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Fatalf("recovery codes: %v", v.body["recovery_codes"])
	}
	tok := v.body["session"].(map[string]any)["access_token"].(string)
	if r := e.send6("GET", tok, "/api/superadmin/accounts", nil); r.status != http.StatusOK {
		t.Fatalf("cabinet after second factor: %d", r.status)
	}
	if again := e.verify(ch, codeFor(t, secret, cur+1)); again.status != http.StatusGone {
		t.Fatalf("challenge reuse: %d %v", again.status, again.body)
	}
	if e.logCount(mfa.EventEnabled, saID) != 1 {
		t.Fatal("enable not logged")
	}
	var hashes string
	if err := e.db.Owner.QueryRow(context.Background(), "SELECT string_agg(code_hash, ',') FROM superadmin_recovery_codes WHERE user_id = $1", saID).Scan(&hashes); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hashes, codes[0].(string)) {
		t.Fatal("recovery codes stored in clear")
	}

	// 4. Второй вход: настройки нет; тот же код (тот же шаг) — отказ; следующий шаг — да.
	r = e.password(phone)
	if r.status != http.StatusAccepted || r.body["setup_required"] != false {
		t.Fatalf("second login: %d %v", r.status, r.body)
	}
	ch = r.body["challenge"].(string)
	if s := e.setup(ch); s.status != http.StatusConflict {
		t.Fatalf("setup when configured: %d", s.status)
	}
	if v := e.verify(ch, codeFor(t, secret, cur)); v.status != http.StatusUnprocessableEntity {
		t.Fatalf("replayed code: %d %v", v.status, v.body)
	}
	if v := e.verify(ch, codeFor(t, secret, cur+1)); v.status != http.StatusOK || len(v.body["recovery_codes"].([]any)) != 0 {
		t.Fatalf("next step: %d %v", v.status, v.body)
	}

	// 5. Код восстановления: один раз, в журнал.
	ch = e.password(phone).body["challenge"].(string)
	rc := strings.ToUpper(codes[3].(string))
	if v := e.verify(ch, rc); v.status != http.StatusOK {
		t.Fatalf("recovery code: %d %v", v.status, v.body)
	}
	if e.logCount(mfa.EventRecoveryUsed, saID) != 1 {
		t.Fatal("recovery use not logged")
	}
	ch = e.password(phone).body["challenge"].(string)
	if v := e.verify(ch, rc); v.status != http.StatusUnprocessableEntity {
		t.Fatalf("recovery code reuse: %d %v", v.status, v.body)
	}
	// Мусор и пусто.
	if v := e.verify(ch, ""); v.status != http.StatusUnprocessableEntity || v.body["message"] != "Введите код." {
		t.Fatalf("empty code: %d %v", v.status, v.body)
	}
	if v := e.verify("nope", "123456"); v.status != http.StatusGone {
		t.Fatalf("unknown challenge: %d %v", v.status, v.body)
	}
	// Сотрудник клуба входит без второго фактора, как раньше.
	_, owner := e.member("Владелец", "owner", "", false)
	if r := e.password(owner); r.status != http.StatusOK || r.body["access_token"] == nil {
		t.Fatalf("staff login: %d %v", r.status, r.body)
	}
}

// Сессия суперадмина без второго фактора (созданная до Этапа 7 или выданная до назначения суперадмином) не действует.
func TestSecondFactorLegacySessionRejected(t *testing.T) {
	e := newSFEnv(t)
	saID, _ := e.member("Супер Админ", "", "", true)
	tok, err := e.sm.Create(context.Background(), saID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if r := e.send6("GET", tok.Access, "/api/superadmin/accounts", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("legacy superadmin session: %d", r.status)
	}
	// Сессия отозвана целиком (refresh тоже).
	if _, err := e.sm.Refresh(context.Background(), tok.Refresh); err == nil {
		t.Fatal("legacy session refresh must fail")
	}
	// Выдали права суперадмина сотруднику с живой сессией — нужен новый вход с кодом.
	uid, phone := e.member("Владелец", "owner", "", false)
	staff := e.login(phone)
	e.exec("UPDATE users SET is_superadmin = true WHERE id = $1", uid)
	if r := e.get(staff, "/api/auth/me"); r.status != http.StatusUnauthorized {
		t.Fatalf("promoted user's old session: %d", r.status)
	}
}

// Сброс другим суперадмином: с подтверждением на фронте, в журнал и аудит, сеансы отозваны, следующий вход —
// настройка заново. Свой — нельзя; режим просмотра клуба — нельзя; не суперадмин — 403.
func TestSecondFactorResetBySuperadmin(t *testing.T) {
	clock := &sfClock{t: time.Now()}
	e := newSFEnvAt(t, clock)
	adminID, adminPhone := e.member("Главный", "", "", true)
	victimID, victimPhone := e.member("Второй", "", "", true)
	admin, adminSecret := e.signInAt(clock, adminPhone)
	victim, _ := e.signInAt(clock, victimPhone)
	// code — свежий код действующего суперадмина (аудит A-10): каждый раз новый шаг.
	code := func() map[string]any {
		clock.Next()
		return map[string]any{"code": codeFor(t, adminSecret, totp.Step(clock.Now()))}
	}

	list := e.send6("GET", admin, "/api/superadmin/accounts?filter=superadmin", nil)
	found := false
	for _, it := range list.body["items"].([]any) {
		a := it.(map[string]any)
		if num(a["id"]) == victimID {
			found = a["second_factor"] == true
		}
	}
	if !found {
		t.Fatalf("accounts list second_factor: %v", list.body)
	}

	// Свой — нельзя.
	if r := e.send6("DELETE", admin, fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", adminID), code()); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("self reset: %d %v", r.status, r.body)
	}
	// Режим просмотра клуба — нельзя.
	expect(t, e.post(admin, fmt.Sprintf("/api/superadmin/clubs/%d/view", e.club), nil), 200, "view")
	if r := e.send6("DELETE", admin, fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", victimID), code()); r.status != http.StatusForbidden || r.body["error"] != permissions.CodeReadOnly {
		t.Fatalf("reset in view mode: %d %v", r.status, r.body)
	}
	expect(t, e.post(admin, "/api/superadmin/leave-club", nil), 200, "leave")
	// Сотрудник клуба — 403.
	_, owner := e.staff("Владелец", "owner")
	if r := e.send6("DELETE", owner, fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", victimID), map[string]any{"code": "000000"}); r.status != http.StatusForbidden {
		t.Fatalf("staff reset: %d", r.status)
	}

	if r := e.send6("DELETE", admin, fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", victimID), code()); r.status != http.StatusOK ||
		r.body["message"] != "Двухфакторная аутентификация сброшена." {
		t.Fatalf("reset: %d %v", r.status, r.body)
	}
	if e.logCount(mfa.EventReset, victimID) != 1 ||
		e.scalar("SELECT count(*) FROM audit_log WHERE action = 'superadmin.totp_reset' AND subject_id = $1 AND actor_user_id = $2", victimID, adminID) != 1 {
		t.Fatal("reset not logged")
	}
	if e.scalar("SELECT count(*) FROM superadmin_recovery_codes WHERE user_id = $1", victimID) != 0 {
		t.Fatal("recovery codes left after reset")
	}
	if r := e.send6("GET", victim, "/api/superadmin/accounts", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("victim session after reset: %d", r.status)
	}
	if r := e.password(victimPhone); r.status != http.StatusAccepted || r.body["setup_required"] != true {
		t.Fatalf("login after reset: %d %v", r.status, r.body)
	}
	// Повтор сброса — нечего сбрасывать (настройка не начата).
	if r := e.send6("DELETE", admin, fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", victimID), code()); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("second reset: %d %v", r.status, r.body)
	}
}

// Лимит: 5 попыток в минуту на аккаунт (как web-login на телефон), неудачи — в журнал; 5 неверных кодов сжигают
// полувход.
func TestSecondFactorRateLimit(t *testing.T) {
	e := newSFEnv(t)
	saID, phone := e.member("Супер Админ", "", "", true)
	passSecondFactor(t, e.srv.URL, phone, e.password(phone))

	ch := e.password(phone).body["challenge"].(string)
	bad := badCode(t, mustSecret(t, phone))
	for i := range 4 { // 1 попытка уже ушла на вход в passSecondFactor
		if v := e.verify(ch, bad); v.status != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d: %d %v", i+1, v.status, v.body)
		}
	}
	if v := e.verify(ch, bad); v.status != http.StatusTooManyRequests || v.header.Get("Retry-After") == "" {
		t.Fatalf("6th attempt in a minute: %d %v", v.status, v.body)
	}
	if e.logCount(mfa.EventFailed, saID) != 4 {
		t.Fatalf("failed attempts logged: %d", e.logCount(mfa.EventFailed, saID))
	}
	// Лимит — на аккаунт: другой суперадмин с того же IP входит.
	_, phone2 := e.member("Второй", "", "", true)
	if r := passSecondFactor(t, e.srv.URL, phone2, e.password(phone2)); r.body["access_token"] == nil {
		t.Fatal("other account blocked by the limit")
	}
}

// badCode — 6 цифр, не совпадающие ни с одним кодом окна (±2 шага).
func badCode(t *testing.T, secret string) string {
	t.Helper()
	cur := totp.Step(time.Now())
	for _, c := range []string{"000000", "111111", "999999"} {
		ok := true
		for s := cur - 2; s <= cur+2; s++ {
			ok = ok && codeFor(t, secret, s) != c
		}
		if ok {
			return c
		}
	}
	t.Fatal("no bad code")
	return ""
}

func mustSecret(t *testing.T, phone string) string {
	t.Helper()
	v, ok := sfSecrets.Load(phone)
	if !ok {
		t.Fatal("no secret")
	}
	return v.(string)
}

// Аудит A-10: выдача и снятие прав суперадмина и сброс чужого второго фактора — только со свежим кодом TOTP
// действующего суперадмина. Без кода, с неверным, с кодом восстановления и с уже использованным (повтор шага) —
// отказ 422 по полю code и запись superadmin.totp_failed; с верным — успех. Режим просмотра клуба — 403.
func TestSuperadminActionsRequireFreshCode(t *testing.T) {
	clock := &sfClock{t: time.Now()}
	e := newSFEnvAt(t, clock)
	adminID, adminPhone := e.member("Главный", "", "", true)
	otherID, otherPhone := e.member("Второй", "", "", true)
	targetID, _ := e.member("Кандидат", "", "", false)
	admin, secret := e.signInAt(clock, adminPhone)
	e.signInAt(clock, otherPhone) // у второго суперадмина настроен TOTP — есть что сбрасывать
	loginStep := totp.Step(clock.Now())

	isSuper := func(id int64) bool {
		return e.scalar("SELECT count(*) FROM users WHERE id = $1 AND is_superadmin", id) == 1
	}
	hasTotp := func(id int64) bool {
		return e.scalar("SELECT count(*) FROM superadmin_totp WHERE user_id = $1", id) == 1
	}
	codeErr := func(r resp, what string) {
		t.Helper()
		errs, _ := r.body["errors"].(map[string]any)
		if r.status != http.StatusUnprocessableEntity || errs["code"] == nil {
			t.Fatalf("%s: %d %v", what, r.status, r.body)
		}
	}
	grant := fmt.Sprintf("/api/superadmin/accounts/%d/superadmin", targetID)
	reset := fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", otherID)
	// Код восстановления (формат как у выданных при настройке) не принимается: только приложение.
	recovery := "ABCDE-FGHJK"

	actions := []struct {
		name, method, path string
		done               func() bool
	}{
		{"выдача прав", "POST", grant, func() bool { return isSuper(targetID) }},
		{"снятие прав", "DELETE", grant, func() bool { return !isSuper(targetID) }},
		{"сброс второго фактора", "DELETE", reset, func() bool { return !hasTotp(otherID) }},
	}
	failed := e.logCount(mfa.EventFailed, adminID)
	for _, a := range actions {
		e.resetTotpLimit(adminID)
		// Без тела, с пустым кодом — «Введите код.»; в журнал неудач это не идёт (кода не было).
		if r := e.send6(a.method, admin, a.path, nil); r.status != http.StatusBadRequest && r.status != http.StatusUnprocessableEntity {
			t.Fatalf("%s без тела: %d %v", a.name, r.status, r.body)
		}
		codeErr(e.send6(a.method, admin, a.path, map[string]any{"code": ""}), a.name+" с пустым кодом")
		codeErr(e.send6(a.method, admin, a.path, map[string]any{"code": "  "}), a.name+" с пробелами")
		// Повтор шага: код, которым только что вошли (или подтвердили предыдущее действие).
		codeErr(e.send6(a.method, admin, a.path, map[string]any{"code": codeFor(t, secret, totp.Step(clock.Now()))}), a.name+" с использованным кодом")
		failed++
		clock.Next()
		good := codeFor(t, secret, totp.Step(clock.Now()))
		wrong := "000000"
		if wrong == good {
			wrong = "000001"
		}
		codeErr(e.send6(a.method, admin, a.path, map[string]any{"code": wrong}), a.name+" с неверным кодом")
		failed++
		codeErr(e.send6(a.method, admin, a.path, map[string]any{"code": recovery}), a.name+" с кодом восстановления")
		failed++
		// Код входа давно в прошлом окне.
		codeErr(e.send6(a.method, admin, a.path, map[string]any{"code": codeFor(t, secret, loginStep-5)}), a.name+" со старым кодом")
		failed++
		if a.done() {
			t.Fatalf("%s выполнено без верного кода", a.name)
		}
		if got := e.logCount(mfa.EventFailed, adminID); got != failed {
			t.Fatalf("%s: superadmin.totp_failed = %d, want %d", a.name, got, failed)
		}
		if r := e.send6(a.method, admin, a.path, map[string]any{"code": good}); r.status != http.StatusOK {
			t.Fatalf("%s с верным кодом: %d %v", a.name, r.status, r.body)
		}
		if !a.done() {
			t.Fatalf("%s: не выполнено", a.name)
		}
		// Тот же код второй раз — отказ (счётчик лимита — заново: окно лимита — минута настоящего времени).
		e.resetTotpLimit(adminID)
		codeErr(e.send6(a.method, admin, a.path, map[string]any{"code": good}), a.name+": повтор верного кода")
		failed++
	}
	if e.logCount("superadmin.granted", targetID) != 1 || e.logCount("superadmin.revoked", targetID) != 1 || e.logCount(mfa.EventReset, otherID) != 1 {
		t.Fatal("actions not logged")
	}

	// Лимит: шестой код за минуту — 429 с Retry-After, даже верный.
	e.resetTotpLimit(adminID)
	for i := 0; i < 5; i++ {
		codeErr(e.send6("POST", admin, grant, map[string]any{"code": "000000"}), "неверный код перед лимитом")
	}
	clock.Next()
	r := e.send6("POST", admin, grant, map[string]any{"code": codeFor(t, secret, totp.Step(clock.Now()))})
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("limit: %d %v", r.status, r.body)
	}
	if isSuper(targetID) {
		t.Fatal("granted over the limit")
	}
	e.resetTotpLimit(adminID)

	// Режим просмотра клуба: выдать и снять права нельзя даже с верным кодом.
	expect(t, e.post(admin, fmt.Sprintf("/api/superadmin/clubs/%d/view", e.club), nil), 200, "view")
	clock.Next()
	body := map[string]any{"code": codeFor(t, secret, totp.Step(clock.Now()))}
	for _, m := range []string{"POST", "DELETE"} {
		if r := e.send6(m, admin, grant, body); r.status != http.StatusForbidden || r.body["error"] != permissions.CodeReadOnly {
			t.Fatalf("%s in view mode: %d %v", m, r.status, r.body)
		}
	}
	expect(t, e.post(admin, "/api/superadmin/leave-club", nil), 200, "leave")
	// Код в режиме просмотра не проверялся и не израсходован.
	if r := e.send6("POST", admin, grant, body); r.status != http.StatusOK || !isSuper(targetID) {
		t.Fatalf("grant after view mode: %d %v", r.status, r.body)
	}
	// Сотрудник клуба — 403 до всякой проверки кода.
	_, owner := e.staff("Владелец", "owner")
	if r := e.send6("DELETE", owner, grant, body); r.status != http.StatusForbidden {
		t.Fatalf("staff revoke: %d", r.status)
	}
}
