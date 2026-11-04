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
func testSecondFactor(t *testing.T, platform db.Beginner, vk valkey.Client, lim *ratelimit.Limiter) *mfa.Manager {
	t.Helper()
	box, err := secretbox.NewKeyring([]secretbox.Key{{ID: "t", Secret: strings.Repeat("k", 32)}}, "")
	if err != nil {
		t.Fatal(err)
	}
	m, err := mfa.New(mfa.Options{Platform: platform, Valkey: vk, Box: box, Limiter: lim})
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
}

// newSFEnv — сервер со вторым фактором; disable — выключенные лимиты (сценарии с многими попытками подряд).
func newSFEnv(t *testing.T, disable ...string) *sfEnv {
	t.Helper()
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
	svc, err := auth.NewService(auth.Options{Pool: d.App, Hasher: h, Sessions: sm, SecondFactor: testSecondFactor(t, d.Platform, vk, lim)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim,
		Pool: d.App, PlatformPool: d.Platform, Presence: &fakePresence{states: map[int64][]presence.State{}},
	}))
	t.Cleanup(srv.Close)
	e := &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("sf%d", adminSeq.Add(1)))}
	return &sfEnv{adminEnv: e, svc: svc, sm: sm}
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
	e := newSFEnv(t)
	adminID, adminPhone := e.member("Главный", "", "", true)
	victimID, victimPhone := e.member("Второй", "", "", true)
	admin := passSecondFactor(t, e.srv.URL, adminPhone, e.password(adminPhone)).body["access_token"].(string)
	victim := passSecondFactor(t, e.srv.URL, victimPhone, e.password(victimPhone)).body["access_token"].(string)

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
	if r := e.send6("DELETE", admin, fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", adminID), nil); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("self reset: %d %v", r.status, r.body)
	}
	// Режим просмотра клуба — нельзя.
	expect(t, e.post(admin, fmt.Sprintf("/api/superadmin/clubs/%d/view", e.club), nil), 200, "view")
	if r := e.send6("DELETE", admin, fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", victimID), nil); r.status != http.StatusForbidden || r.body["error"] != permissions.CodeReadOnly {
		t.Fatalf("reset in view mode: %d %v", r.status, r.body)
	}
	expect(t, e.post(admin, "/api/superadmin/leave-club", nil), 200, "leave")
	// Сотрудник клуба — 403.
	_, owner := e.staff("Владелец", "owner")
	if r := e.send6("DELETE", owner, fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", victimID), nil); r.status != http.StatusForbidden {
		t.Fatalf("staff reset: %d", r.status)
	}

	if r := e.send6("DELETE", admin, fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", victimID), nil); r.status != http.StatusOK ||
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
	if r := e.send6("DELETE", admin, fmt.Sprintf("/api/superadmin/accounts/%d/second-factor", victimID), nil); r.status != http.StatusUnprocessableEntity {
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
