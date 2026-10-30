//go:build integration

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
)

var authSeq atomic.Int64

type authEnv struct {
	t      *testing.T
	db     *dbtest.DB
	svc    *auth.Service
	srv    *httptest.Server
	stubs  *httptest.Server
	clubID int64
}

func newAuthEnv(t *testing.T, enable ...string) *authEnv {
	t.Helper()
	d := dbtest.New(t)
	vk, cleanup, err := authtest.StartValkey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	h, _ := password.New(password.Options{})
	sm, err := session.New(session.Options{Client: vk, Keys: []session.Key{{ID: "t", Secret: bytes.Repeat([]byte{3}, 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(auth.Options{Pool: d.App, Hasher: h, Sessions: sm})
	if err != nil {
		t.Fatal(err)
	}
	lim, err := ratelimit.New(ratelimit.Options{Client: vk, Enable: enable})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(NewRouter(Options{Logger: logger, Auth: svc, Limiter: lim}))
	t.Cleanup(srv.Close)

	// Эндпоинты-заглушки с правами (касса, баланс клиента, тарифы, права, работники).
	r := chi.NewRouter()
	r.Use(svc.Middleware)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, right := range []string{"cash.view", "cash.create", "cash.delete", "customer_money.update", "tariffs.view",
		"tariffs.update", "tariffs.delete", "workers.update", "warehouse.delete", "chat.create"} {
		r.With(permissions.RequirePermission(right)).Get("/stub/"+right, ok)
		r.With(permissions.RequirePermission(right)).Post("/stub/"+right, ok)
	}
	r.With(permissions.RequireRole(permissions.RoleAdmin)).Get("/stub/settings.permissions", ok)
	r.With(permissions.RequireSuperadmin).Get("/stub/superadmin", ok)
	stubs := httptest.NewServer(r)
	t.Cleanup(stubs.Close)

	return &authEnv{t: t, db: d, svc: svc, srv: srv, stubs: stubs, clubID: d.Club(t, fmt.Sprintf("c%d", authSeq.Add(1)))}
}

func (e *authEnv) staff(role, perms string, superadmin bool) (phone string) {
	e.t.Helper()
	phone = fmt.Sprintf("+771%08d", authSeq.Add(1))
	h, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	var id int64
	if err := e.db.Owner.QueryRow(context.Background(),
		"INSERT INTO users (name, phone, password, is_superadmin, created_at, updated_at) VALUES ($1, $1, $2, $3, now(), now()) RETURNING id",
		phone, string(h), superadmin).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	if role != "" {
		var p any
		if perms != "" {
			p = perms
		}
		if _, err := e.db.Owner.Exec(context.Background(),
			"INSERT INTO club_memberships (club_id, user_id, role, permissions, created_at, updated_at) VALUES ($1, $2, $3, $4::jsonb, now(), now())",
			e.clubID, id, role, p); err != nil {
			e.t.Fatal(err)
		}
	}
	return phone
}

type resp struct {
	status  int
	header  http.Header
	cookies []*http.Cookie
	body    map[string]any
}

func do(t *testing.T, method, url string, body any, hdr map[string]string, cookies ...*http.Cookie) resp {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, url, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := resp{status: res.StatusCode, header: res.Header, cookies: res.Cookies()}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func cookie(r resp, name string) *http.Cookie {
	for _, c := range r.cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (e *authEnv) login(phone string) (access string, r resp) {
	e.t.Helper()
	r = do(e.t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": phone, "password": "pw"}, nil)
	if r.status != http.StatusOK {
		e.t.Fatalf("login %s: %d %v", phone, r.status, r.body)
	}
	return r.body["access_token"].(string), r
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func TestAuthHTTPFlow(t *testing.T) {
	e := newAuthEnv(t)
	ph := e.staff(permissions.RoleCashier, "", false)

	access, lr := e.login(ph)
	rt, csrf := cookie(lr, RefreshCookie), cookie(lr, CSRFCookie)
	if rt == nil || !rt.HttpOnly || !rt.Secure || rt.SameSite != http.SameSiteStrictMode || rt.Path != "/api/auth" {
		t.Fatalf("refresh cookie %+v", rt)
	}
	if csrf == nil || csrf.HttpOnly || !csrf.Secure || csrf.Path != "/" || csrf.Value != lr.body["csrf_token"] {
		t.Fatalf("csrf cookie %+v", csrf)
	}
	if strings.Contains(fmt.Sprint(lr.body), rt.Value) {
		t.Fatal("refresh token must not be in the body")
	}
	me := lr.body["me"].(map[string]any)
	if me["role"] != "cashier" || me["read_only"] != false || me["club"].(map[string]any)["id"].(float64) != float64(e.clubID) {
		t.Fatalf("me %v", me)
	}

	// /me с access-токеном; без — 401.
	if r := do(t, "GET", e.srv.URL+"/api/auth/me", nil, bearer(access)); r.status != 200 || r.body["user"].(map[string]any)["phone"] != ph {
		t.Fatalf("me: %d %v", r.status, r.body)
	}
	if r := do(t, "GET", e.srv.URL+"/api/auth/me", nil, nil); r.status != 401 || r.body["message"] != "Unauthenticated." {
		t.Fatalf("anon me: %d %v", r.status, r.body)
	}
	if r := do(t, "GET", e.srv.URL+"/api/auth/me", nil, bearer("garbage")); r.status != 401 {
		t.Fatalf("bad token: %d", r.status)
	}

	// Refresh: без CSRF — отказ; с CSRF — новый токен и новая cookie.
	if r := do(t, "POST", e.srv.URL+"/api/auth/refresh", nil, map[string]string{"X-CSRF-Token": "wrong"}, rt); r.status != 403 {
		t.Fatalf("refresh bad csrf: %d", r.status)
	}
	if r := do(t, "POST", e.srv.URL+"/api/auth/refresh", nil, map[string]string{"X-CSRF-Token": csrf.Value, "Sec-Fetch-Site": "cross-site"}, rt); r.status != 403 {
		t.Fatalf("refresh cross-site: %d", r.status)
	}
	rr := do(t, "POST", e.srv.URL+"/api/auth/refresh", nil, map[string]string{"X-CSRF-Token": csrf.Value}, rt)
	if rr.status != 200 {
		t.Fatalf("refresh: %d %v", rr.status, rr.body)
	}
	rt2 := cookie(rr, RefreshCookie)
	if rt2 == nil || rt2.Value == rt.Value {
		t.Fatal("refresh cookie must rotate")
	}
	access2 := rr.body["access_token"].(string)
	// Сразу повторить старый — гонка вкладок: 409, сессия жива.
	if r := do(t, "POST", e.srv.URL+"/api/auth/refresh", nil, map[string]string{"X-CSRF-Token": csrf.Value}, rt); r.status != 409 {
		t.Fatalf("race: %d", r.status)
	}

	// Выход по refresh-cookie (access-токен потерян) — сессия отозвана, cookie удалены.
	lo := do(t, "POST", e.srv.URL+"/api/auth/logout", nil, map[string]string{"X-CSRF-Token": csrf.Value}, rt2)
	if lo.status != 204 || cookie(lo, RefreshCookie) == nil || cookie(lo, RefreshCookie).MaxAge >= 0 {
		t.Fatalf("logout: %d %+v", lo.status, lo.cookies)
	}
	if r := do(t, "GET", e.srv.URL+"/api/auth/me", nil, bearer(access2)); r.status != 401 {
		t.Fatalf("after logout: %d", r.status)
	}

	// Ошибки входа — 422 с полем и текстом старого кода.
	r := do(t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": ph, "password": "bad"}, nil)
	if r.status != 422 || r.body["message"] != auth.MsgInvalidCredentials || r.body["errors"].(map[string]any)["phone"] == nil {
		t.Fatalf("bad password: %d %v", r.status, r.body)
	}
	if r := do(t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": ph, "password": "pw"}, map[string]string{"Sec-Fetch-Site": "cross-site"}); r.status != 403 {
		t.Fatalf("cross-site login: %d", r.status)
	}
	// Health не сломан.
	if r := do(t, "GET", e.srv.URL+"/healthz", nil, nil); r.status != 200 {
		t.Fatalf("healthz %d", r.status)
	}
}

// Права по матрице на эндпоинтах-заглушках: касса, баланс клиента, тарифы, права, работники.
func TestPermissionStubs(t *testing.T) {
	e := newAuthEnv(t)
	tokens := map[string]string{}
	for _, role := range []string{"owner", "admin", "manager", "cashier", "operator", "client"} {
		ph := e.staff(role, "", false)
		if role == "client" {
			r := do(t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": ph, "password": "pw", "pc_login": true}, nil)
			tokens[role] = r.body["access_token"].(string)
			continue
		}
		tokens[role], _ = e.login(ph)
	}
	// Кассир с правом возвратов от владельца (GS-006) и менеджер без тарифов.
	cashierRefund, _ := e.login(e.staff("cashier", `{"cash.delete": true}`, false))
	managerNoTariffs, _ := e.login(e.staff("manager", `{"tariffs.update": false}`, false))

	cases := []struct {
		who, right string
		want       int
	}{
		{"owner", "tariffs.delete", 204}, {"admin", "tariffs.delete", 403}, {"manager", "tariffs.update", 204},
		{"cashier", "tariffs.view", 403}, {"cashier", "cash.create", 204}, {"cashier", "cash.delete", 403},
		{"operator", "cash.view", 403}, {"operator", "chat.create", 204}, {"manager", "chat.create", 403},
		{"manager", "customer_money.update", 204}, {"cashier", "customer_money.update", 403},
		{"admin", "workers.update", 204}, {"manager", "workers.update", 403}, {"manager", "warehouse.delete", 403},
		{"client", "cash.view", 403}, {"client", "tariffs.view", 403},
		{"cashierRefund", "cash.delete", 204}, {"managerNoTariffs", "tariffs.update", 403}, {"managerNoTariffs", "tariffs.view", 204},
	}
	tokens["cashierRefund"], tokens["managerNoTariffs"] = cashierRefund, managerNoTariffs
	for _, c := range cases {
		r := do(t, "POST", e.stubs.URL+"/stub/"+c.right, nil, bearer(tokens[c.who]))
		if r.status != c.want {
			t.Errorf("%s %s: %d, want %d", c.who, c.right, r.status, c.want)
		}
		if c.want == 403 && r.body["message"] != permissions.MsgNoRight {
			t.Errorf("%s %s: message %v", c.who, c.right, r.body["message"])
		}
	}
	// Страница «Права» — club-role:owner,admin (GS-251/252).
	for who, want := range map[string]int{"owner": 204, "admin": 204, "manager": 403, "cashier": 403} {
		if r := do(t, "GET", e.stubs.URL+"/stub/settings.permissions", nil, bearer(tokens[who])); r.status != want {
			t.Errorf("permissions page %s: %d, want %d", who, r.status, want)
		}
	}
	if r := do(t, "GET", e.stubs.URL+"/stub/cash.view", nil, nil); r.status != 401 {
		t.Errorf("anon: %d", r.status)
	}
}

// Суперадмин смотрит клуб: GET — да, POST — 403 с текстом старого кода; выход из просмотра.
func TestSuperadminViewHTTP(t *testing.T) {
	e := newAuthEnv(t)
	tok, lr := e.login(e.staff("", "", true))
	if lr.body["me"].(map[string]any)["club"] != nil {
		t.Fatalf("cabinet: %v", lr.body["me"])
	}
	if r := do(t, "GET", e.stubs.URL+"/stub/superadmin", nil, bearer(tok)); r.status != 204 {
		t.Fatalf("superadmin stub: %d", r.status)
	}
	if r := do(t, "GET", e.stubs.URL+"/stub/cash.view", nil, bearer(tok)); r.status != 403 || r.body["message"] != permissions.MsgNoClubs {
		t.Fatalf("no club: %d %v", r.status, r.body)
	}
	v := do(t, "POST", fmt.Sprintf("%s/api/superadmin/clubs/%d/view", e.srv.URL, e.clubID), nil, bearer(tok))
	if v.status != 200 || v.body["read_only"] != true || v.body["role"] != "owner" {
		t.Fatalf("view: %d %v", v.status, v.body)
	}
	if r := do(t, "GET", e.stubs.URL+"/stub/tariffs.view", nil, bearer(tok)); r.status != 204 {
		t.Fatalf("view GET: %d", r.status)
	}
	if r := do(t, "POST", e.stubs.URL+"/stub/cash.create", nil, bearer(tok)); r.status != 403 || r.body["message"] != permissions.MsgReadOnly {
		t.Fatalf("view POST: %d %v", r.status, r.body)
	}
	if r := do(t, "PUT", e.srv.URL+"/api/auth/club", map[string]any{"club_id": e.clubID}, bearer(tok)); r.status != 403 || r.body["error"] != "read_only" {
		t.Fatalf("switch in view: %d %v", r.status, r.body)
	}
	if r := do(t, "POST", e.srv.URL+"/api/superadmin/leave-club", nil, bearer(tok)); r.status != 200 || r.body["read_only"] != false {
		t.Fatalf("leave: %d %v", r.status, r.body)
	}
	// Не суперадмин — 403.
	cashier, _ := e.login(e.staff("cashier", "", false))
	if r := do(t, "POST", fmt.Sprintf("%s/api/superadmin/clubs/%d/view", e.srv.URL, e.clubID), nil, bearer(cashier)); r.status != 403 {
		t.Fatalf("cashier view: %d", r.status)
	}
}

// Лимит web-login (выключен по умолчанию): включённый — 6-я попытка с одного IP+телефона → 429.
func TestLoginRateLimit(t *testing.T) {
	off := newAuthEnv(t)
	ph := off.staff("cashier", "", false)
	for i := range 7 {
		if r := do(t, "POST", off.srv.URL+"/api/auth/login", map[string]any{"phone": ph, "password": "bad"}, nil); r.status != 422 {
			t.Fatalf("disabled limit, attempt %d: %d", i+1, r.status)
		}
	}

	on := newAuthEnv(t, ratelimit.WebLogin)
	ph = on.staff("cashier", "", false)
	for i := range 5 {
		if r := do(t, "POST", on.srv.URL+"/api/auth/login", map[string]any{"phone": ph, "password": "bad"}, nil); r.status != 422 {
			t.Fatalf("attempt %d: %d", i+1, r.status)
		}
	}
	r := do(t, "POST", on.srv.URL+"/api/auth/login", map[string]any{"phone": ph, "password": "pw"}, nil)
	if r.status != 429 || r.body["message"] != ratelimit.TooManyAttemptsMessage || r.header.Get("Retry-After") == "" {
		t.Fatalf("429: %d %v %v", r.status, r.body, r.header)
	}
	// Другой телефон с того же IP проходит.
	if _, lr := on.login(on.staff("cashier", "", false)); lr.status != 200 {
		t.Fatal("other phone")
	}
}
