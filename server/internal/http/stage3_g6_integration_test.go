//go:build integration

package httpserver

// Этап 3, группа 6: кабинет суперадмина — HTTP → Postgres (пул zafiro_platform), журнал платформы и аудит,
// отдельный домен кабинета (Q-069), версия переводов (PD-12), истечение подписок (Q-036).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
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
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker"
)

func newG6Env(t *testing.T, now time.Time, domain string) *adminEnv {
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
	lim, err := ratelimit.New(ratelimit.Options{Client: vk})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetSecondFactor(testSecondFactor(t, d.Platform, vk, lim))
	box, _ := secretbox.New(strings.Repeat("s", 32))
	pr := &fakePresence{states: map[int64][]presence.State{}}
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim,
		Pool: d.App, PlatformPool: d.Platform, Presence: pr, Now: func() time.Time { return now },
		Superadmin: httpx.SuperadminDeps{Domain: domain, Secrets: box},
	}))
	t.Cleanup(srv.Close)
	return &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("g6%d", adminSeq.Add(1))), presence: pr}
}

func (e *adminEnv) send6(method, tok, path string, body any) resp {
	e.t.Helper()
	return do(e.t, method, e.srv.URL+path, body, bearer(tok))
}

// onHost — запрос с другим заголовком Host (домен кабинета).
func (e *adminEnv) onHost(host, method, tok, path string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rd)
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	out := resp{status: res.StatusCode, header: res.Header}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func TestSuperadminCabinet(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e := newG6Env(t, now, "")
	saID, saPhone := e.member("Супер Админ", "", "", true)
	sa := e.login(saPhone)
	_, owner := e.staff("Владелец", "owner")

	// Не суперадмин — 403 на любой операции кабинета.
	if r := e.send6("GET", owner, "/api/superadmin/clubs", nil); r.status != http.StatusForbidden {
		t.Fatalf("owner: %d %v", r.status, r.body)
	}
	if r := e.send6("GET", sa, "/api/superadmin/dashboard?period=7d", nil); r.status != http.StatusOK || r.body["period"] != "7d" {
		t.Fatalf("dashboard: %d %v", r.status, r.body)
	}

	// Подписка: продление создаёт активную подписку, журнал платформы и аудит.
	plan := e.scalar("INSERT INTO saas_plans (slug, name, price_monthly, trial_days, is_active, created_at, updated_at) VALUES ($1, 'Базовый', 1500000, 14, true, now(), now()) RETURNING id",
		fmt.Sprintf("base%d", e.club))
	r := e.send6("POST", sa, fmt.Sprintf("/api/superadmin/clubs/%d/subscription/extend", e.club), map[string]any{"days": 30, "saas_plan_id": plan})
	if r.status != http.StatusOK || r.body["message"] != "Подписка продлена." {
		t.Fatalf("extend: %d %v", r.status, r.body)
	}
	if n := e.scalar("SELECT count(*) FROM club_subscriptions WHERE club_id = $1 AND status = 'active' AND current_period_ends_at = $2", e.club, now.AddDate(0, 0, 30)); n != 1 {
		t.Fatalf("subscription not extended: %d", n)
	}
	if n := e.scalar("SELECT count(*) FROM platform_logs WHERE club_id = $1 AND user_id = $2 AND event = 'superadmin.subscription_extended' AND description LIKE '%продлена на 30 дн.%'", e.club, saID); n != 1 {
		t.Fatalf("platform log: %d", n)
	}
	if n := e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND actor_kind = 'superadmin' AND action = 'subscription.extended'", e.club); n != 1 {
		t.Fatalf("audit: %d", n)
	}
	if r := e.send6("GET", sa, fmt.Sprintf("/api/superadmin/clubs?search=g6%d", e.club), nil); r.status != http.StatusOK {
		t.Fatalf("clubs: %d %v", r.status, r.body)
	}
	card := e.send6("GET", sa, fmt.Sprintf("/api/superadmin/clubs/%d", e.club), nil)
	if card.status != http.StatusOK || card.body["badge"].(map[string]any)["label"] != "Активна" {
		t.Fatalf("club card: %d %v", card.status, card.body)
	}
	if r := e.send6("POST", sa, fmt.Sprintf("/api/superadmin/clubs/%d/subscription/suspend", e.club), nil); r.status != http.StatusOK {
		t.Fatalf("suspend: %d %v", r.status, r.body)
	}
	if r := e.send6("GET", sa, "/api/superadmin/clubs/999999999", nil); r.status != http.StatusNotFound {
		t.Fatalf("missing club: %d", r.status)
	}

	// Тарифы платформы: все ошибки формы сразу; изменение пишет только изменённые поля.
	r = e.send6("POST", sa, "/api/superadmin/plans", map[string]any{"name": "", "slug": "bad slug", "trial_days": 400})
	if r.status != http.StatusUnprocessableEntity || len(r.body["errors"].(map[string]any)) != 3 {
		t.Fatalf("plan validation: %d %v", r.status, r.body)
	}
	slug := fmt.Sprintf("pro%d", e.club)
	if r := e.send6("POST", sa, "/api/superadmin/plans", map[string]any{"name": "Про", "slug": slug, "trial_days": 7, "price_monthly_tiyn": 2500050,
		"features": "Карта\n\n Касса ", "is_active": true}); r.status != http.StatusCreated {
		t.Fatalf("create plan: %d %v", r.status, r.body)
	}
	pro := e.scalar("SELECT id FROM saas_plans WHERE slug = $1", slug)
	if r := e.send6("PATCH", sa, fmt.Sprintf("/api/superadmin/plans/%d", pro), map[string]any{"name": "Про", "slug": slug, "trial_days": 7,
		"price_monthly_tiyn": 2500050, "features": "Карта\nКасса"}); r.status != http.StatusOK {
		t.Fatalf("update plan: %d %v", r.status, r.body)
	}
	var details string
	_ = e.db.Owner.QueryRow(context.Background(), "SELECT details::text FROM platform_logs WHERE event = 'superadmin.plan_updated' AND subject_id = $1", pro).Scan(&details)
	if !strings.Contains(details, `"is_active": ""`) || strings.Contains(details, "trial_days") {
		t.Fatalf("plan changes: %s", details)
	}

	// Пользователи: свой аккаунт заблокировать нельзя; заблокированный не входит.
	if r := e.send6("POST", sa, fmt.Sprintf("/api/superadmin/accounts/%d/block", saID), nil); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("self block: %d", r.status)
	}
	victim, victimPhone := e.member("Кассир", "cashier", "", false)
	if r := e.send6("POST", sa, fmt.Sprintf("/api/superadmin/accounts/%d/block", victim), nil); r.status != http.StatusOK {
		t.Fatalf("block: %d %v", r.status, r.body)
	}
	if r := do(t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": victimPhone, "password": "pw"}, nil); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("blocked login: %d", r.status)
	}
	if r := e.send6("GET", sa, "/api/superadmin/accounts?filter=blocked", nil); r.status != http.StatusOK || num(r.body["total"]) < 1 {
		t.Fatalf("accounts: %d %v", r.status, r.body)
	}

	// Журнал платформы — вкладка по умолчанию.
	logs := e.send6("GET", sa, fmt.Sprintf("/api/superadmin/logs?club=%d", e.club), nil)
	if logs.status != http.StatusOK || logs.body["tab"] != "platform" || num(logs.body["total"]) < 2 {
		t.Fatalf("logs: %d %v", logs.status, logs.body)
	}
}

func TestSuperadminTranslations(t *testing.T) {
	e := newG6Env(t, time.Now(), "")
	_, saPhone := e.member("Супер Админ", "", "", true)
	sa := e.login(saPhone)
	for _, l := range i18n.DefaultLanguages {
		e.exec("INSERT INTO languages (code, name, is_enabled, is_system, sort_order, created_at, updated_at) VALUES ($1, $2, true, true, $3, now(), now()) ON CONFLICT (code) DO NOTHING", l.Code, l.Name, l.Sort)
	}
	key := fmt.Sprintf("Тестовая строка :name %d", e.club)
	hash := i18n.HashKey("*", key)
	e.exec("INSERT INTO translations (language_id, \"group\", key, key_hash, value, created_at, updated_at) SELECT id, '*', $1, $2, $1, now(), now() FROM languages WHERE code = 'ru' ON CONFLICT DO NOTHING", key, hash)
	kk := e.scalar("SELECT id FROM languages WHERE code = 'kk'")

	pack := func() (string, string) {
		r := e.send6("GET", sa, "/api/i18n/kk", nil)
		b, _ := json.Marshal(r.body)
		return r.header.Get("ETag"), string(b)
	}
	etag0, _ := pack()
	r := e.send6("PUT", sa, fmt.Sprintf("/api/superadmin/languages/%d/translations", kk), map[string]any{"translations": map[string]any{hash: "Сынақ :name"}})
	if r.status != http.StatusOK || r.body["message"] != "Сохранён 1 перевод." {
		t.Fatalf("save: %d %v", r.status, r.body)
	}
	etag1, body := pack()
	if etag1 == etag0 || !strings.Contains(body, "Сынақ {{name}}") {
		t.Fatalf("pack not updated: %s %s", etag1, body)
	}
	// Редактор показывает формат Laravel (:name).
	list := e.send6("GET", sa, fmt.Sprintf("/api/superadmin/languages/%d/translations?search=%d", kk, e.club), nil)
	items, _ := list.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["value"] != "Сынақ :name" {
		t.Fatalf("editor: %v", list.body)
	}
	// Удаление перевода тоже меняет версию (PD-12).
	if r := e.send6("PUT", sa, fmt.Sprintf("/api/superadmin/languages/%d/translations", kk), map[string]any{"translations": map[string]any{hash: ""}}); r.status != http.StatusOK {
		t.Fatalf("delete: %d %v", r.status, r.body)
	}
	if etag2, _ := pack(); etag2 == etag1 {
		t.Fatal("version did not change after delete")
	}

	// Импорт CSV: подстановки должны совпасть.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("language", "kk")
	fw, _ := mw.CreateFormFile("file", "kk.csv")
	_, _ = fw.Write([]byte("id;group;ru;en;kk\n" + hash + ";*;x;;Без подстановки\n"))
	_ = mw.Close()
	req, _ := http.NewRequestWithContext(context.Background(), "POST", e.srv.URL+"/api/superadmin/languages/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+sa)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var imp map[string]any
	_ = json.NewDecoder(res.Body).Decode(&imp)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || num(imp["invalid_count"]) != 1 {
		t.Fatalf("import: %d %v", res.StatusCode, imp)
	}
	// Встроенный язык удалить нельзя.
	if r := e.send6("DELETE", sa, fmt.Sprintf("/api/superadmin/languages/%d", kk), nil); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("delete system language: %d", r.status)
	}
}

func TestSuperadminDomain(t *testing.T) {
	e := newG6Env(t, time.Now(), "admin.zafiro.test")
	_, saPhone := e.member("Супер Админ", "", "", true)
	_, ownerPhone := e.member("Владелец", "owner", "", false)
	host := "admin.zafiro.test"

	// Не на хосте кабинета маршрутов кабинета нет.
	sa := e.login(saPhone)
	if r := e.send6("GET", sa, "/api/superadmin/plans", nil); r.status != http.StatusNotFound {
		t.Fatalf("plans on club host: %d", r.status)
	}
	// На хосте кабинета входят только суперадмины.
	r := e.onHost(host, "POST", "", "/api/auth/login", map[string]any{"phone": ownerPhone, "password": "pw"})
	if r.status != http.StatusUnprocessableEntity || r.body["message"] != "Здесь входят только суперадмины Zafiro." {
		t.Fatalf("owner on cabinet: %d %v", r.status, r.body)
	}
	r = e.onHost(host, "POST", "", "/api/auth/login", map[string]any{"phone": saPhone, "password": "pw"})
	if r.status == http.StatusAccepted { // второй фактор (Этап 7)
		r = passSecondFactor(t, e.srv.URL, saPhone, r)
	}
	if r.status != http.StatusOK {
		t.Fatalf("superadmin on cabinet: %d %v", r.status, r.body)
	}
	tok := r.body["access_token"].(string)
	if r := e.onHost(host, "GET", tok, "/api/superadmin/plans", nil); r.status != http.StatusOK {
		t.Fatalf("plans on cabinet: %d", r.status)
	}
	// REST панели клуба на хосте кабинета — только в режиме просмотра.
	if r := e.onHost(host, "GET", tok, "/api/layout/badges", nil); r.status != http.StatusForbidden {
		t.Fatalf("panel API on cabinet: %d", r.status)
	}
	if n := e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'superadmin.login'"); n < 0 {
		t.Fatal(n)
	}
}

func TestExpireSubscriptionsWorker(t *testing.T) {
	e := newG6Env(t, time.Now(), "")
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	plan := e.scalar("INSERT INTO saas_plans (slug, name, trial_days, is_active, created_at, updated_at) VALUES ($1, 'P', 0, true, now(), now()) RETURNING id", fmt.Sprintf("exp%d", e.club))
	e.exec("INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, current_period_ends_at, created_at, updated_at) VALUES ($1, $2, 'active', $3, $4, now(), now())",
		e.club, plan, now.AddDate(0, -1, 0), now.Add(-time.Hour))
	other := e.db.Club(t, fmt.Sprintf("trial%d", e.club))
	e.exec("INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, trial_ends_at, created_at, updated_at) VALUES ($1, $2, 'trialing', $3, $4, now(), now())",
		other, plan, now.AddDate(0, -1, 0), now.Add(-time.Hour))
	w := &worker.ExpireSubscriptions{Platform: e.db.Platform, Now: func() time.Time { return now }}
	ran, n, err := w.Tick(context.Background())
	if err != nil || !ran || n < 1 {
		t.Fatalf("tick: %v %v %d", ran, err, n)
	}
	if s := e.scalar("SELECT count(*) FROM club_subscriptions WHERE club_id = $1 AND status = 'expired'", e.club); s != 1 {
		t.Fatal("not expired")
	}
	// Q-036: пробный период остаётся trialing.
	if s := e.scalar("SELECT count(*) FROM club_subscriptions WHERE club_id = $1 AND status = 'trialing'", other); s != 1 {
		t.Fatal("trial changed")
	}
	if s := e.scalar("SELECT count(*) FROM platform_logs WHERE club_id = $1 AND event = 'subscription.expired' AND user_id IS NULL AND description LIKE '%закончилась 15.10.2026'", e.club); s != 1 {
		t.Fatal("platform log")
	}
}
