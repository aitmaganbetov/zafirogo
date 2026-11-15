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
	svc.SetSecondFactor(testSecondFactor(t, d.Platform, vk, lim))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Pool — боевые маршруты разделов доходят до обработчиков (тест прав идёт по ним, а не по заглушкам, BC-11).
	srv := httptest.NewServer(NewRouter(Options{Logger: logger, Auth: svc, Limiter: lim, Pool: d.App, PlatformPool: d.Platform}))
	t.Cleanup(srv.Close)

	return &authEnv{t: t, db: d, svc: svc, srv: srv, clubID: d.Club(t, fmt.Sprintf("c%d", authSeq.Add(1)))}
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
	if r.status == http.StatusAccepted { // суперадмин: второй фактор (Этап 7)
		r = passSecondFactor(e.t, e.srv.URL, phone, r)
	}
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

// rightRoute — боевой маршрут раздела, который открывает право (httpx.Access по таблице RequireClubArea::ROUTES).
type rightRoute struct {
	method, path string
	// denied — текст отказа в праве (у возврата и ответа в чате — свой, как в Laravel).
	denied string
}

var rightRoutes = map[string]rightRoute{
	"tariffs.view":     {"GET", "/api/tariffs", permissions.MsgNoRight},
	"tariffs.update":   {"PATCH", "/api/tariffs/1", permissions.MsgNoRight},
	"tariffs.delete":   {"DELETE", "/api/tariffs/1", permissions.MsgNoRight},
	"cash.view":        {"GET", "/api/cash/shift", permissions.MsgNoRight},
	"cash.create":      {"POST", "/api/cash/sales", permissions.MsgNoRight},
	"cash.delete":      {"POST", "/api/cash/receipts/1/refund", "Нет права на возвраты. Его даёт владелец клуба в «Настройки → Права»."},
	"chat.create":      {"POST", "/api/chat/1/messages", "Нет права отвечать в чате. Его даёт владелец клуба в «Настройки → Права»."},
	"customers.update": {"POST", "/api/customers/1/balance-adjustments", permissions.MsgNoRight},
	"workers.update":   {"PATCH", "/api/workers/1", permissions.MsgNoRight},
	"warehouse.delete": {"DELETE", "/api/warehouse/products/1", permissions.MsgNoRight},
}

// callRight — запрос к боевому маршруту права. passed — проверку прав httpx.Access запрос прошёл (дальше обработчик
// отвечает своё: 404 несуществующего объекта, 422 пустого тела, 503 неподключённого сервиса — но не отказ в праве).
func (e *authEnv) callRight(right, token string) (r resp, passed bool) {
	e.t.Helper()
	rt, ok := rightRoutes[right]
	if !ok {
		e.t.Fatalf("no route for right %q", right)
	}
	var body any
	if rt.method != "GET" && rt.method != "DELETE" {
		body = map[string]any{}
	}
	hdr := map[string]string{"Idempotency-Key": fmt.Sprintf("k-%d", authSeq.Add(1))}
	if token != "" {
		hdr["Authorization"] = "Bearer " + token
	}
	r = do(e.t, rt.method, e.srv.URL+rt.path, body, hdr)
	return r, r.status != 401 && r.status != 403
}

// Права по матрице на БОЕВЫХ маршрутах разделов (httpx.Access → permissions.RightFor), а не на заглушках с
// отдельными middleware (BC-11): касса, возвраты, тарифы, чат, клиенты, работники, склад, страница «Права».
func TestPermissionMatrixOnRealRoutes(t *testing.T) {
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
	tokens["cashierRefund"], _ = e.login(e.staff("cashier", `{"cash.delete": true}`, false))
	tokens["managerNoTariffs"], _ = e.login(e.staff("manager", `{"tariffs.update": false}`, false))
	// Кассир, у которого владелец отнял право менять клиентов.
	tokens["cashierNoCustomers"], _ = e.login(e.staff("cashier", `{"customers.update": false}`, false))

	cases := []struct {
		who, right string
		allowed    bool
	}{
		{"owner", "tariffs.delete", true}, {"admin", "tariffs.delete", false}, {"manager", "tariffs.update", true},
		{"cashier", "tariffs.view", false}, {"cashier", "cash.create", true}, {"cashier", "cash.delete", false},
		{"operator", "cash.view", false}, {"operator", "chat.create", true}, {"manager", "chat.create", false},
		{"manager", "customers.update", true}, {"cashierNoCustomers", "customers.update", false}, {"client", "customers.update", false},
		{"admin", "workers.update", true}, {"manager", "workers.update", false}, {"manager", "warehouse.delete", false},
		{"owner", "warehouse.delete", true},
		{"client", "cash.view", false}, {"client", "tariffs.view", false}, {"client", "chat.create", false},
		{"cashierRefund", "cash.delete", true}, {"managerNoTariffs", "tariffs.update", false}, {"managerNoTariffs", "tariffs.view", true},
	}
	for _, c := range cases {
		// Таблица ролей — та же, что отдаёт permissions.For (матрица ClubPermissions): случай теста не расходится с ней.
		r, passed := e.callRight(c.right, tokens[c.who])
		if passed != c.allowed {
			t.Errorf("%s %s: status %d %v, allowed = %v, want %v", c.who, c.right, r.status, r.body, passed, c.allowed)
		}
		if !c.allowed && (r.status != 403 || r.body["message"] != rightRoutes[c.right].denied) {
			t.Errorf("%s %s: %d message %v", c.who, c.right, r.status, r.body["message"])
		}
	}
	// Страница «Права» — club-role:owner,admin (GS-251/252).
	for who, allowed := range map[string]bool{"owner": true, "admin": true, "manager": false, "cashier": false, "client": false} {
		r := do(t, "GET", e.srv.URL+"/api/settings/permissions", nil, bearer(tokens[who]))
		if (r.status == 200) != allowed || (!allowed && r.status != 403) {
			t.Errorf("permissions page %s: %d, allowed want %v", who, r.status, allowed)
		}
	}
	if r, _ := e.callRight("cash.view", ""); r.status != 401 {
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
	if r := do(t, "GET", e.srv.URL+"/api/superadmin/plans", nil, bearer(tok)); r.status != 200 {
		t.Fatalf("superadmin cabinet: %d %v", r.status, r.body)
	}
	if r, _ := e.callRight("cash.view", tok); r.status != 403 || r.body["message"] != permissions.MsgNoClubs {
		t.Fatalf("no club: %d %v", r.status, r.body)
	}
	v := do(t, "POST", fmt.Sprintf("%s/api/superadmin/clubs/%d/view", e.srv.URL, e.clubID), nil, bearer(tok))
	if v.status != 200 || v.body["read_only"] != true || v.body["role"] != "owner" {
		t.Fatalf("view: %d %v", v.status, v.body)
	}
	if r, passed := e.callRight("tariffs.view", tok); !passed || r.status != 200 {
		t.Fatalf("view GET: %d %v", r.status, r.body)
	}
	if r, _ := e.callRight("cash.create", tok); r.status != 403 || r.body["message"] != permissions.MsgReadOnly {
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

// Лимит web-login включён по умолчанию (решение Q-052, docs/DECISIONS.md): 6-я попытка с одного IP+телефона → 429.
func TestLoginRateLimit(t *testing.T) {
	on := newAuthEnv(t)
	ph := on.staff("cashier", "", false)
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
