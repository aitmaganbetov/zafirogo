//go:build integration

package httpserver

// Сквозные тесты REST админки: HTTP → сервис → Postgres (RLS, роль zafiro_app) + Valkey (сессии, лимиты).
// Сценарии — docs/golden-scenarios.md (ID в имени подтеста).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
	"golang.org/x/crypto/bcrypt"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ---------------------------------------------------------------------------
// Общие Postgres и Valkey на все тесты админки (по клубу на тест)
// ---------------------------------------------------------------------------

var (
	adminOnce    sync.Once
	adminDB      *dbtest.DB
	adminVK      valkey.Client
	adminCleanup []func()
	adminErr     error
	adminSeq     atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	for i := len(adminCleanup) - 1; i >= 0; i-- {
		adminCleanup[i]()
	}
	os.Exit(code)
}

func sharedAdmin(t *testing.T) (*dbtest.DB, valkey.Client) {
	t.Helper()
	adminOnce.Do(func() {
		d, c1, err := dbtest.Start(context.Background())
		if err != nil {
			adminErr = err
			return
		}
		adminCleanup = append(adminCleanup, c1)
		vk, c2, err := authtest.StartValkey(context.Background())
		if err != nil {
			adminErr = err
			return
		}
		adminCleanup = append(adminCleanup, c2)
		adminDB, adminVK = d, vk
	})
	if adminErr != nil {
		t.Fatal(adminErr)
	}
	return adminDB, adminVK
}

// fakePresence — присутствие агентов для карты клуба.
type fakePresence struct {
	mu     sync.Mutex
	states map[int64][]presence.State
}

func (f *fakePresence) List(_ context.Context, clubID int64) ([]presence.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[clubID], nil
}

// fakeNotifier — уведомления агента (после коммита).
type fakeNotifier struct {
	mu       sync.Mutex
	started  []int64 // computer_id
	ended    []int64
	balances []int64 // user_id
}

func (n *fakeNotifier) SessionStarted(_ context.Context, _, computerID int64, _ *agentv1.ActiveSession, _ int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.started = append(n.started, computerID)
	return nil
}

func (n *fakeNotifier) SessionEnded(_ context.Context, _, computerID int64, _ *agentv1.EndSession, _ int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ended = append(n.ended, computerID)
	return nil
}

func (n *fakeNotifier) BalanceChanged(_ context.Context, _, userID int64, _ *agentv1.Balance) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.balances = append(n.balances, userID)
	return nil
}

func (n *fakeNotifier) balanceCount(userID int64) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, id := range n.balances {
		if id == userID {
			c++
		}
	}
	return c
}

type adminEnv struct {
	t        *testing.T
	db       *dbtest.DB
	srv      *httptest.Server
	club     int64
	presence *fakePresence
	notifier *fakeNotifier
	category int64
}

func newAdminEnv(t *testing.T, enableLimits ...string) *adminEnv {
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
	lim, err := ratelimit.New(ratelimit.Options{Client: vk, Enable: enableLimits})
	if err != nil {
		t.Fatal(err)
	}
	n := &fakeNotifier{}
	cashSvc := &cashsvc.Service{Notifier: n}
	bill := billingsvc.New(d.App, nil, cashSvc)
	bill.SetNotifier(n)
	pr := &fakePresence{states: map[int64][]presence.State{}}
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim,
		Pool: d.App, Billing: bill, Cash: cashSvc, Presence: pr,
	}))
	t.Cleanup(srv.Close)
	return &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("adm%d", adminSeq.Add(1))), presence: pr, notifier: n}
}

// member — пользователь с ролью в клубе (perms — права вне матрицы, JSON). Возвращает id и телефон.
func (e *adminEnv) member(name, role, perms string, superadmin bool) (int64, string) {
	e.t.Helper()
	phone := fmt.Sprintf("+772%08d", adminSeq.Add(1))
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	var id int64
	if err := e.db.Owner.QueryRow(context.Background(),
		"INSERT INTO users (name, phone, password, is_superadmin, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now()) RETURNING id",
		name, phone, string(hash), superadmin).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	if role != "" {
		var p any
		if perms != "" {
			p = perms
		}
		e.exec("INSERT INTO club_memberships (club_id, user_id, role, permissions, created_at, updated_at) VALUES ($1, $2, $3, $4::jsonb, now(), now())",
			e.club, id, role, p)
		e.exec("INSERT INTO member_balances (club_id, user_id, created_at, updated_at) VALUES ($1, $2, now(), now())", e.club, id)
	}
	return id, phone
}

// login — access-токен сотрудника.
func (e *adminEnv) login(phone string) string {
	e.t.Helper()
	r := do(e.t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": phone, "password": "pw"}, nil)
	if r.status != http.StatusOK {
		e.t.Fatalf("login %s: %d %v", phone, r.status, r.body)
	}
	return r.body["access_token"].(string)
}

// staff — сотрудник с ролью и его токен.
func (e *adminEnv) staff(name, role string, perms ...string) (int64, string) {
	p := ""
	if len(perms) > 0 {
		p = perms[0]
	}
	id, phone := e.member(name, role, p, false)
	return id, e.login(phone)
}

func (e *adminEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Owner.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *adminEnv) scalar(sql string, args ...any) int64 {
	e.t.Helper()
	var v int64
	if err := e.db.Owner.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (e *adminEnv) settings(js string) {
	e.exec("UPDATE clubs SET settings = $2::jsonb WHERE id = $1", e.club, js)
}

func (e *adminEnv) product(name string, price, qty int64) int64 {
	e.t.Helper()
	if e.category == 0 {
		e.category = e.scalar("INSERT INTO product_categories (club_id, name, created_at, updated_at) VALUES ($1, 'Напитки', now(), now()) RETURNING id", e.club)
	}
	return e.scalar(`INSERT INTO products (club_id, sku, name, category, product_category_id, price, quantity, created_at, updated_at)
		VALUES ($1, $2, $3, 'Напитки', $4, $5, $6, now(), now()) RETURNING id`,
		e.club, fmt.Sprintf("SKU-%d", adminSeq.Add(1)), name, e.category, price, qty)
}

func (e *adminEnv) service(name string, price int64, active bool) int64 {
	return e.scalar("INSERT INTO club_services (club_id, name, price, is_active, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now()) RETURNING id",
		e.club, name, price, active)
}

// customer — клиент с начальными балансами (тиыны) через REST владельца.
func (e *adminEnv) customer(owner, name string, main, bonus int64) int64 {
	e.t.Helper()
	id, _ := e.member(name, "client", "", false)
	if main != 0 || bonus != 0 {
		r := e.post(owner, fmt.Sprintf("/api/customers/%d/initial-balance", id), map[string]any{"balance_tiyn": main, "bonus_balance_tiyn": bonus})
		if r.status != 200 {
			e.t.Fatalf("initial balance: %d %v", r.status, r.body)
		}
	}
	return id
}

func (e *adminEnv) balances(userID int64) (int64, int64) { return e.db.Balances(e.t, e.club, userID) }

var keySeq atomic.Int64

func newKey() string { return fmt.Sprintf("key-%d-%d", time.Now().UnixNano(), keySeq.Add(1)) }

// post — POST с новым ключом идемпотентности.
func (e *adminEnv) post(tok, path string, body any) resp {
	return e.postKey(tok, path, newKey(), body)
}

func (e *adminEnv) postKey(tok, path, key string, body any) resp {
	e.t.Helper()
	h := bearer(tok)
	if key != "" {
		h["Idempotency-Key"] = key
	}
	return do(e.t, "POST", e.srv.URL+path, body, h)
}

func (e *adminEnv) get(tok, path string) resp {
	e.t.Helper()
	return do(e.t, "GET", e.srv.URL+path, nil, bearer(tok))
}

// raw — тело ответа целиком (для сравнения повторов).
func (e *adminEnv) raw(method, tok, path, key string, body any) (int, string) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func expect(t *testing.T, r resp, status int, what string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("%s: status %d, want %d: %v", what, r.status, status, r.body)
	}
}

// fieldError — текст ошибки поля в 422.
func fieldError(r resp, field string) string {
	errs := obj(r.body["errors"])
	list, _ := errs[field].([]any)
	if len(list) == 0 {
		return ""
	}
	s, _ := list[0].(string)
	return s
}

func expectField(t *testing.T, r resp, field, message string) {
	t.Helper()
	if r.status != http.StatusUnprocessableEntity || fieldError(r, field) != message {
		t.Fatalf("want 422 %s=%q, got %d %v", field, message, r.status, r.body)
	}
}

// activityCount — записи журнала события в клубе (с предметом, если subjectID > 0).
func (e *adminEnv) activityCount(event string, subjectID int64) int64 {
	if subjectID > 0 {
		return e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = $2 AND subject_id = $3", e.club, event, subjectID)
	}
	return e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = $2", e.club, event)
}

func (e *adminEnv) activityDetails(event string) map[string]any {
	e.t.Helper()
	var raw []byte
	if err := e.db.Owner.QueryRow(context.Background(),
		"SELECT details FROM activity_logs WHERE club_id = $1 AND event = $2 ORDER BY id DESC LIMIT 1", e.club, event).Scan(&raw); err != nil {
		e.t.Fatalf("activity %s: %v", event, err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func (e *adminEnv) outboxCount(typ string) int64 {
	return e.scalar("SELECT count(*) FROM outbox_events WHERE club_id = $1 AND type = $2", e.club, typ)
}

func (e *adminEnv) openShift(tok string, opening int64) int64 {
	e.t.Helper()
	r := e.post(tok, "/api/cash/shifts", map[string]any{"opening_balance_tiyn": opening})
	expect(e.t, r, 201, "open shift")
	return num(obj(r.body["shift"])["id"])
}

// ---------------------------------------------------------------------------
// Касса: золотые сценарии на уровне HTTP
// ---------------------------------------------------------------------------

func TestAdminCashHTTP(t *testing.T) {
	t.Run("GS-007", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		key := newKey()
		st1, b1 := e.raw("POST", owner, "/api/cash/shifts", key, map[string]any{"opening_balance_tiyn": 1000000})
		if st1 != 201 {
			t.Fatalf("open: %d %s", st1, b1)
		}
		// Повтор с тем же ключом — тот же ответ, смена одна.
		if st2, b2 := e.raw("POST", owner, "/api/cash/shifts", key, map[string]any{"opening_balance_tiyn": 1000000}); st2 != 201 || b2 != b1 {
			t.Fatalf("replay: %d %s != %s", st2, b2, b1)
		}
		// Тот же ключ, другое тело — 409.
		if st, _ := e.raw("POST", owner, "/api/cash/shifts", key, map[string]any{"opening_balance_tiyn": 500000}); st != 409 {
			t.Fatalf("conflict: %d", st)
		}
		r := e.post(owner, "/api/cash/shifts", map[string]any{"opening_balance_tiyn": 500000})
		expectField(t, r, "opening_balance", cash.MsgShiftAlreadyOpen)
		if n := e.scalar("SELECT count(*) FROM cash_shifts WHERE club_id = $1 AND status = 'open'", e.club); n != 1 {
			t.Fatalf("open shifts %d", n)
		}
		if e.activityCount("cash.shift_opened", 0) != 1 || e.outboxCount("cash.shift_changed") != 1 {
			t.Fatal("journal/outbox of shift opening")
		}
		// Отрицательный остаток — правило min.
		r = e.post(owner, "/api/cash/shifts", map[string]any{"opening_balance_tiyn": -1})
		expectField(t, r, "opening_balance", "The opening balance field must be at least 0.")
		// Без ключа — 400.
		if r := e.postKey(owner, "/api/cash/shifts", "", map[string]any{"opening_balance_tiyn": 1}); r.status != 400 || r.body["error"] != "idempotency_key_required" {
			t.Fatalf("no key: %d %v", r.status, r.body)
		}
	})

	t.Run("GS-008", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		e.openShift(owner, 1000000)
		expect(t, e.post(owner, "/api/cash/transactions", map[string]any{"type": "income", "amount_tiyn": 300000, "description": "Продажа"}), 201, "income")
		r := e.post(owner, "/api/cash/transactions", map[string]any{"type": "expense", "amount_tiyn": 100000, "description": "Расход"})
		expect(t, r, 201, "expense")
		if r.body["message"] != "Операция проведена" {
			t.Fatalf("message %v", r.body["message"])
		}
		x := e.get(owner, "/api/cash/reports/x")
		expect(t, x, 200, "x report")
		if b := num(obj(x.body["totals"])["balance_tiyn"]); b != 1200000 {
			t.Fatalf("balance %d", b)
		}
		if e.activityCount("cash.income", 0) != 1 || e.activityCount("cash.expense", 0) != 1 {
			t.Fatal("journal")
		}
		if d := e.activityDetails("cash.expense"); fmt.Sprint(d["amount"]) != "1000" {
			t.Fatalf("details %v", d)
		}
		// Расход больше остатка.
		r = e.post(owner, "/api/cash/transactions", map[string]any{"type": "expense", "amount_tiyn": 1300000, "description": "Много"})
		expectField(t, r, "amount", cash.MsgNotEnoughCash)
		// Решение Q-002 (docs/DECISIONS.md): сумма в API — целые тиыны; доля тиына (ввод с > 2 знаками тенге) не
		// округляется, запрос отклоняется целиком.
		r = e.post(owner, "/api/cash/transactions", map[string]any{"type": "income", "amount_tiyn": 100.5, "description": "Доля"})
		if r.status != http.StatusBadRequest {
			t.Fatalf("fractional tiyn: status %d, want 400", r.status)
		}
	})

	t.Run("GS-009", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		shift := e.openShift(owner, 500000)
		expect(t, e.post(owner, "/api/cash/transactions", map[string]any{"type": "income", "amount_tiyn": 200000, "description": "Приход"}), 201, "income")
		closePath := fmt.Sprintf("/api/cash/shifts/%d/close", shift)
		expectField(t, e.post(owner, closePath, nil), "shift", cash.MsgPrintZFirst)
		expect(t, e.get(owner, fmt.Sprintf("/api/cash/shifts/%d/z-report", shift)), 200, "z report")
		printed := fmt.Sprintf("/api/cash/shifts/%d/z-report/printed", shift)
		if r := e.post(owner, printed, map[string]any{"nonce": "wrong"}); r.status != 419 {
			t.Fatalf("wrong nonce: %d", r.status)
		}
		n := e.post(owner, fmt.Sprintf("/api/cash/shifts/%d/z-report/nonce", shift), nil)
		expect(t, n, 200, "nonce")
		expect(t, e.post(owner, printed, map[string]any{"nonce": n.body["nonce"]}), 200, "printed")
		if r := e.post(owner, printed, map[string]any{"nonce": n.body["nonce"]}); r.status != 419 {
			t.Fatalf("nonce reuse: %d", r.status)
		}
		r := e.post(owner, closePath, nil)
		expect(t, r, 200, "close")
		sh := obj(r.body["shift"])
		if sh["status"] != "closed" || num(sh["closing_balance_tiyn"]) != 700000 {
			t.Fatalf("closed shift %v", sh)
		}
		if e.activityCount("cash.z_report_printed", shift) != 1 || e.activityCount("cash.shift_closed", shift) != 1 {
			t.Fatal("journal")
		}
		if d := e.activityDetails("cash.shift_closed"); fmt.Sprint(d["closing_balance"]) != "7000" {
			t.Fatalf("closing details %v", d)
		}
		// Смена закрыта — отметка печати 422, X-отчёт 404.
		if r := e.post(owner, printed, map[string]any{"nonce": "x"}); r.status != 422 {
			t.Fatalf("closed printed: %d", r.status)
		}
		expect(t, e.get(owner, "/api/cash/reports/x"), 404, "x without shift")
	})

	t.Run("GS-010", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		e.openShift(owner, 0)
		p := e.product("Coca Cola", 75000, 5)
		r := e.post(owner, "/api/cash/sales", map[string]any{"items": []map[string]any{{"product_id": p, "quantity": 2}}})
		expect(t, r, 201, "sale")
		tx := obj(r.body["transaction"])
		items, _ := tx["items"].([]any)
		if num(tx["amount_tiyn"]) != 150000 || len(items) != 1 || num(obj(items[0])["quantity"]) != 2 || r.body["message"] != "Продажа проведена. Распечатайте чек." {
			t.Fatalf("sale %v", r.body)
		}
		if q := e.scalar("SELECT quantity FROM products WHERE id = $1", p); q != 3 {
			t.Fatalf("stock %d", q)
		}
		rec := e.get(owner, fmt.Sprintf("/api/cash/receipts/%d", num(tx["id"])))
		expect(t, rec, 200, "receipt")
		if rec.body["refundable"] != true || rec.body["can_refund"] != true {
			t.Fatalf("receipt %v", rec.body)
		}
		if e.activityCount("stock.written_off", p) != 1 || e.activityCount("product.purchased", num(tx["id"])) != 1 {
			t.Fatal("journal")
		}
		if d := e.activityDetails("product.purchased"); fmt.Sprint(d["amount"]) != "1500" || num(d["item_count"]) != 1 {
			t.Fatalf("details %v", d)
		}
		if e.outboxCount("sale.completed") != 1 || e.outboxCount("stock.changed") != 1 || e.outboxCount("cash.transaction") != 1 {
			t.Fatal("outbox")
		}
		var payload map[string]any
		var raw []byte
		if err := e.db.Owner.QueryRow(context.Background(), "SELECT payload FROM outbox_events WHERE club_id = $1 AND type = 'sale.completed'", e.club).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &payload)
		if payload["amount"] != "1500.00" || num(payload["amount_tiyn"]) != 150000 {
			t.Fatalf("sale.completed %v", payload)
		}
		// Недостаточно товара.
		r = e.post(owner, "/api/cash/sales", map[string]any{"items": []map[string]any{{"product_id": p, "quantity": 4}}})
		expectField(t, r, "items", "Недостаточно товара: Coca Cola.")
	})

	t.Run("GS-012", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		c := e.customer(owner, "Клиент POS", 10000, 2500)
		e.openShift(owner, 50000)
		r := e.post(owner, "/api/cash/balance-top-ups", map[string]any{"user_id": c, "amount_tiyn": 75000, "balance_type": "balance", "payment_method": "card"})
		expect(t, r, 201, "top up")
		if r.body["message"] != "Баланс пользователя пополнен." || num(r.body["new_balance_tiyn"]) != 85000 || num(r.body["new_bonus_balance_tiyn"]) != 2500 {
			t.Fatalf("top up %v", r.body)
		}
		tx := obj(r.body["transaction"])
		if num(tx["amount_tiyn"]) != 75000 || !strings.Contains(fmt.Sprint(tx["description"]), "Клиент POS") {
			t.Fatalf("tx %v", tx)
		}
		if e.activityCount("user.balance_topped_up", c) != 1 || e.activityCount("cash.income", num(tx["id"])) != 1 {
			t.Fatal("journal")
		}
		if e.outboxCount("balance.changed") < 1 {
			t.Fatal("balance.changed")
		}
		if e.notifier.balanceCount(c) < 1 {
			t.Fatal("agent notifier after commit")
		}
		// Повтор с тем же ключом — тот же ответ, баланс не меняется.
		key := newKey()
		body := map[string]any{"user_id": c, "amount_tiyn": 1000, "balance_type": "balance", "payment_method": "cash"}
		_, b1 := e.raw("POST", owner, "/api/cash/balance-top-ups", key, body)
		_, b2 := e.raw("POST", owner, "/api/cash/balance-top-ups", key, body)
		if b1 != b2 {
			t.Fatalf("replay %s != %s", b1, b2)
		}
		if m, _ := e.balances(c); m != 86000 {
			t.Fatalf("balance after replay %d", m)
		}
	})

	t.Run("GS-013", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		e.openShift(owner, 0)
		r := e.post(owner, "/api/cash/balance-top-ups", map[string]any{"user_id": 999999, "amount_tiyn": 0, "balance_type": "unknown", "payment_method": "unknown"})
		if r.status != 422 {
			t.Fatalf("status %d", r.status)
		}
		for field, msg := range map[string]string{
			"user_id": "The selected user id is invalid.", "amount": "The amount field must be greater than 0.",
			"balance_type": "The selected balance type is invalid.", "payment_method": "The selected payment method is invalid.",
		} {
			if fieldError(r, field) != msg {
				t.Errorf("%s: %q", field, fieldError(r, field))
			}
		}
		if n := e.scalar("SELECT count(*) FROM cash_transactions WHERE club_id = $1", e.club); n != 0 {
			t.Fatalf("transactions %d", n)
		}
	})

	t.Run("GS-002", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		e.settings(`{"finances": {"topup_bonus_enabled": true, "topup_bonus_levels": [{"amount": 2000, "percent": 5}, {"amount": 10000, "percent": 10}]}}`)
		c := e.customer(owner, "Бонус", 10000, 2500)
		e.openShift(owner, 0)
		up := func(amount int64) resp {
			r := e.post(owner, "/api/cash/balance-top-ups", map[string]any{"user_id": c, "amount_tiyn": amount, "balance_type": "balance", "payment_method": "cash"})
			expect(t, r, 201, "top up")
			return r
		}
		if r := up(100000); r.body["message"] != "Баланс пользователя пополнен." {
			t.Fatalf("1000: %v", r.body["message"])
		}
		if r := up(250000); r.body["message"] != "Баланс пользователя пополнен. Бонус за пополнение: 125.00." {
			t.Fatalf("2500: %v", r.body["message"])
		}
		if m, b := e.balances(c); m != 360000 || b != 15000 {
			t.Fatalf("after 2500: %d %d", m, b)
		}
		d := e.activityDetails("user.balance_topped_up")
		if fmt.Sprint(d["amount"]) != "2500" || fmt.Sprint(d["bonus_amount"]) != "125" || fmt.Sprint(d["bonus_percent"]) != "5" {
			t.Fatalf("details %v", d)
		}
		up(1000055)
		if m, b := e.balances(c); m != 1360055 || b != 115006 {
			t.Fatalf("after 10000.55: %d %d", m, b)
		}
		if s := e.scalar("SELECT sum(amount)::bigint FROM cash_transactions WHERE club_id = $1", e.club); s != 1350055 {
			t.Fatalf("cash %d", s)
		}
	})

	t.Run("GS-003", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		e.settings(`{"finances": {"topup_bonus_enabled": false, "topup_bonus_levels": [{"amount": 100, "percent": 50}]}}`)
		c := e.customer(owner, "Бонус", 10000, 2500)
		e.openShift(owner, 0)
		expect(t, e.post(owner, "/api/cash/balance-top-ups", map[string]any{"user_id": c, "amount_tiyn": 100000, "balance_type": "balance", "payment_method": "cash"}), 201, "main")
		if _, b := e.balances(c); b != 2500 {
			t.Fatalf("bonus %d", b)
		}
		e.settings(`{"finances": {"topup_bonus_enabled": true, "topup_bonus_levels": [{"amount": 100, "percent": 50}]}}`)
		expect(t, e.post(owner, "/api/cash/balance-top-ups", map[string]any{"user_id": c, "amount_tiyn": 100000, "balance_type": "bonus_balance", "payment_method": "cash"}), 201, "bonus")
		if _, b := e.balances(c); b != 102500 {
			t.Fatalf("bonus %d", b)
		}
	})

	t.Run("GS-004", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		e.openShift(owner, 100000)
		p := e.product("Coca Cola", 70000, 10)
		s := e.post(owner, "/api/cash/sales", map[string]any{"items": []map[string]any{{"product_id": p, "quantity": 3}}})
		expect(t, s, 201, "sale")
		saleID := num(obj(s.body["transaction"])["id"])
		ops := e.get(owner, "/api/cash/operations")
		expect(t, ops, 200, "operations")
		if items, _ := ops.body["items"].([]any); len(items) != 1 {
			t.Fatalf("operations %v", ops.body)
		}
		key := newKey()
		path := fmt.Sprintf("/api/cash/receipts/%d/refund", saleID)
		st, b1 := e.raw("POST", owner, path, key, nil)
		if st != 200 || !strings.Contains(b1, "Возврат проведён: 2 100,00 KZT выданы из кассы.") {
			t.Fatalf("refund: %d %s", st, b1)
		}
		if st2, b2 := e.raw("POST", owner, path, key, nil); st2 != 200 || b2 != b1 {
			t.Fatalf("replay: %d %s", st2, b2)
		}
		expectField(t, e.post(owner, path, nil), "refund", cash.MsgAlreadyRefunded)
		if q := e.scalar("SELECT quantity FROM products WHERE id = $1", p); q != 10 {
			t.Fatalf("stock %d", q)
		}
		if n := e.scalar("SELECT count(*) FROM stock_movements WHERE club_id = $1 AND product_id = $2 AND type = 'receipt'", e.club, p); n != 1 {
			t.Fatalf("movements %d", n)
		}
		rec := e.get(owner, fmt.Sprintf("/api/cash/receipts/%d", saleID))
		if rec.body["refund"] == nil {
			t.Fatalf("receipt refund %v", rec.body)
		}
		x := e.get(owner, "/api/cash/reports/x")
		if num(obj(x.body["totals"])["balance_tiyn"]) != 100000 || num(obj(x.body["refunds"])["count"]) != 1 || num(obj(x.body["refunds"])["amount_tiyn"]) != 210000 {
			t.Fatalf("x %v", x.body)
		}
		if e.activityCount("cash.refund", 0) != 1 || e.activityCount("stock.received", p) != 1 {
			t.Fatal("journal")
		}
	})

	t.Run("GS-005", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		e.settings(`{"finances": {"topup_bonus_enabled": true, "topup_bonus_levels": [{"amount": 1000, "percent": 10}]}}`)
		c := e.customer(owner, "Клиент", 0, 0)
		e.openShift(owner, 0)
		t1 := e.post(owner, "/api/cash/balance-top-ups", map[string]any{"user_id": c, "amount_tiyn": 200000, "balance_type": "balance", "payment_method": "cash"})
		expect(t, t1, 201, "top up")
		if _, b := e.balances(c); b != 20000 {
			t.Fatalf("bonus %d", b)
		}
		// Клиент потратил: основной 1500 (списание владельцем без кассы).
		expect(t, e.post(owner, fmt.Sprintf("/api/customers/%d/balance-adjustments", c), map[string]any{"balance_type": "balance", "action": "write_off", "amount_tiyn": 50000}), 200, "spend")
		path := fmt.Sprintf("/api/cash/receipts/%d/refund", num(obj(t1.body["transaction"])["id"]))
		expectField(t, e.post(owner, path, nil), "refund", cash.MsgTopUpSpent)
		expect(t, e.post(owner, fmt.Sprintf("/api/customers/%d/balance-adjustments", c), map[string]any{"balance_type": "balance", "action": "top_up", "amount_tiyn": 100000}), 200, "top up back")
		expect(t, e.post(owner, path, nil), 200, "refund")
		if m, b := e.balances(c); m != 50000 || b != 0 {
			t.Fatalf("balances %d %d", m, b)
		}
		if e.activityCount("user.balance_refunded", c) != 1 {
			t.Fatal("user.balance_refunded")
		}
	})

	t.Run("GS-006", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		cashierID, cashier := e.staff("Айгерим", "cashier")
		e.openShift(owner, 500000)
		p := e.product("Вода", 10000, 5)
		s := e.post(owner, "/api/cash/sales", map[string]any{"items": []map[string]any{{"product_id": p, "quantity": 1}}})
		other := e.post(owner, "/api/cash/transactions", map[string]any{"type": "income", "amount_tiyn": 30000, "description": "Разное"})
		saleID, otherID := num(obj(s.body["transaction"])["id"]), num(obj(other.body["transaction"])["id"])
		ops := e.get(cashier, "/api/cash/operations")
		if items, _ := ops.body["items"].([]any); len(items) != 2 || obj(items[0])["can_refund"] != false {
			t.Fatalf("operations %v", ops.body)
		}
		r := e.post(cashier, fmt.Sprintf("/api/cash/receipts/%d/refund", saleID), nil)
		if r.status != 403 || r.body["message"] != cash.MsgNoRefundRight {
			t.Fatalf("no right: %d %v", r.status, r.body)
		}
		e.exec("UPDATE club_memberships SET permissions = '{\"cash.delete\": true}' WHERE club_id = $1 AND user_id = $2", e.club, cashierID)
		// Права читаются при входе — новый токен.
		var ph string
		if err := e.db.Owner.QueryRow(context.Background(), "SELECT phone FROM users WHERE id = $1", cashierID).Scan(&ph); err != nil {
			t.Fatal(err)
		}
		cashier = e.login(ph)
		expectField(t, e.post(cashier, fmt.Sprintf("/api/cash/receipts/%d/refund", otherID), nil), "refund", cash.MsgNotRefundable)
		ok := e.post(cashier, fmt.Sprintf("/api/cash/receipts/%d/refund", saleID), nil)
		expect(t, ok, 200, "refund with right")
		if num(obj(ok.body["refund"])["user_id"]) != cashierID {
			t.Fatalf("refund user %v", ok.body)
		}
	})

	t.Run("GS-141", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		_, cashier := e.staff("Айгерим", "cashier")
		ps := e.service("PS5 — 1 час", 150000, true)
		old := e.service("Старое", 1000, false)
		cola := e.product("Cola", 50000, 10)
		e.openShift(owner, 0)
		r := e.post(cashier, "/api/cash/sales", map[string]any{"items": []map[string]any{{"service_id": ps, "quantity": 2}, {"product_id": cola, "quantity": 1}}})
		expect(t, r, 201, "sale")
		if num(obj(r.body["transaction"])["amount_tiyn"]) != 350000 {
			t.Fatalf("amount %v", r.body)
		}
		if q := e.scalar("SELECT quantity FROM products WHERE id = $1", cola); q != 9 {
			t.Fatalf("cola %d", q)
		}
		x := e.get(owner, "/api/cash/reports/x")
		found := false
		for _, it := range x.body["items"].([]any) {
			if m := obj(it); m["name"] == "PS5 — 1 час" && num(m["quantity"]) == 2 && m["stock"] == nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("x items %v", x.body["items"])
		}
		r = e.post(cashier, "/api/cash/sales", map[string]any{"items": []map[string]any{{"service_id": old, "quantity": 1}}})
		if r.status != 422 || fieldError(r, "items.0.service_id") == "" {
			t.Fatalf("inactive service: %d %v", r.status, r.body)
		}
	})

	t.Run("GS-142", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		pr := e.service("Распечатка", 5000, true)
		cola := e.product("Cola", 50000, 10)
		e.openShift(owner, 100000)
		s := e.post(owner, "/api/cash/sales", map[string]any{"items": []map[string]any{{"service_id": pr, "quantity": 10}, {"product_id": cola, "quantity": 2}}})
		expect(t, s, 201, "sale")
		r := e.post(owner, fmt.Sprintf("/api/cash/receipts/%d/refund", num(obj(s.body["transaction"])["id"])), nil)
		expect(t, r, 200, "refund")
		if num(obj(r.body["refund"])["amount_tiyn"]) != 150000 {
			t.Fatalf("refund %v", r.body)
		}
		if q := e.scalar("SELECT quantity FROM products WHERE id = $1", cola); q != 10 {
			t.Fatalf("cola %d", q)
		}
	})

	t.Run("GS-016", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		shift := e.openShift(owner, 0)
		z := e.get(owner, fmt.Sprintf("/api/cash/shifts/%d/z-report", shift))
		if items, _ := z.body["items"].([]any); z.status != 200 || len(items) != 0 {
			t.Fatalf("empty z: %d %v", z.status, z.body)
		}
	})

	t.Run("GS-017", func(t *testing.T) {
		e := newAdminEnv(t)
		_, owner := e.staff("Азат", "owner")
		_, cashier := e.staff("Айгерим", "cashier")
		shift := e.openShift(cashier, 0)
		x := e.get(owner, "/api/cash/reports/x")
		if x.body["cashier_name"] != "Айгерим" || x.body["operator_name"] != "Азат" || x.body["closed_by_name"] != "" {
			t.Fatalf("x %v", x.body)
		}
		n := e.post(owner, fmt.Sprintf("/api/cash/shifts/%d/z-report/nonce", shift), nil)
		expect(t, e.post(owner, fmt.Sprintf("/api/cash/shifts/%d/z-report/printed", shift), map[string]any{"nonce": n.body["nonce"]}), 200, "printed")
		expect(t, e.post(owner, fmt.Sprintf("/api/cash/shifts/%d/close", shift), nil), 200, "close")
		z := e.get(owner, fmt.Sprintf("/api/cash/shifts/%d/z-report", shift))
		if z.body["cashier_name"] != "Айгерим" || z.body["closed_by_name"] != "Азат" || z.body["operator_name"] != "Азат" {
			t.Fatalf("z %v", z.body)
		}
	})
}

// ---------------------------------------------------------------------------
// Права, режим просмотра, чужой клуб, лимит
// ---------------------------------------------------------------------------

// По одному allow/deny на эндпоинт по матрице ролей (ClubPermissions::ROLE_DEFAULTS).
func TestAdminPermissionsMatrix(t *testing.T) {
	e := newAdminEnv(t)
	_, owner := e.staff("Владелец", "owner")
	tokens := map[string]string{"owner": owner}
	for _, role := range []string{"admin", "manager", "cashier", "operator"} {
		_, tokens[role] = e.staff(role, role)
	}
	// Клиенты видны всем сотрудникам по умолчанию; отказ — владелец снял право.
	_, tokens["noCustomers"] = e.staff("Без клиентов", "operator", `{"customers.view": false}`)
	cust := e.customer(owner, "Клиент", 0, 0)
	p := e.product("Вода", 10000, 5)

	cases := []struct {
		method, path string
		allow, deny  string
		body         any
	}{
		{"GET", "/api/cash/shift", "cashier", "operator", nil},
		{"GET", "/api/cash/operations", "cashier", "operator", nil},
		{"POST", "/api/cash/transactions", "cashier", "operator", map[string]any{"type": "income", "amount_tiyn": 100, "description": "x"}},
		{"POST", "/api/cash/sales", "cashier", "operator", map[string]any{"items": []map[string]any{{"product_id": p, "quantity": 1}}}},
		{"POST", "/api/cash/receipts/1/refund", "manager", "cashier", nil},
		{"GET", "/api/warehouse/products", "manager", "cashier", nil},
		{"POST", "/api/warehouse/stock-movements", "manager", "cashier", map[string]any{"product_id": p, "type": "receipt", "quantity": 1}},
		{"GET", "/api/customers", "operator", "noCustomers", nil},
		{"GET", fmt.Sprintf("/api/customers/%d/ledger", cust), "cashier", "noCustomers", nil},
		{"GET", "/api/tariffs", "manager", "cashier", nil},
		{"GET", "/api/club-map/state", "operator", "", nil},
		{"POST", "/api/club-computers/1/end-session", "operator", "cashier", map[string]any{"session_id": "x"}},
	}
	for _, c := range cases {
		check := func(who string, wantAllowed bool) {
			var r resp
			if c.method == "GET" {
				r = e.get(tokens[who], c.path)
			} else {
				r = e.post(tokens[who], c.path, c.body)
			}
			denied := r.status == 403 && (r.body["message"] == permissions.MsgNoRight || r.body["message"] == cash.MsgNoRefundRight)
			if wantAllowed == denied {
				t.Errorf("%s %s as %s: %d %v", c.method, c.path, who, r.status, r.body)
			}
		}
		check(c.allow, true)
		if c.deny != "" {
			check(c.deny, false)
		}
	}
	// Без входа — 401.
	if r := do(t, "GET", e.srv.URL+"/api/cash/shift", nil, nil); r.status != 401 {
		t.Fatalf("anon: %d", r.status)
	}
	// Корректировка без кассы — только владелец (422 с текстом старого кода), менеджер — по праву customers.update.
	r := e.post(tokens["manager"], fmt.Sprintf("/api/customers/%d/balance-adjustments", cust), map[string]any{"balance_type": "balance", "action": "top_up", "amount_tiyn": 100})
	expectField(t, r, "top_up_amount", "Пополнять и списывать баланс без кассы может только владелец клуба.")
	_, noUpdate := e.staff("Без правки", "cashier", `{"customers.update": false}`)
	r = e.post(noUpdate, fmt.Sprintf("/api/customers/%d/balance-adjustments", cust), map[string]any{"balance_type": "balance", "action": "top_up", "amount_tiyn": 100})
	if r.status != 403 || r.body["message"] != permissions.MsgNoRight {
		t.Fatalf("no customers.update: %d %v", r.status, r.body)
	}
	expect(t, e.post(owner, fmt.Sprintf("/api/customers/%d/balance-adjustments", cust), map[string]any{"balance_type": "balance", "action": "top_up", "amount_tiyn": 12345}), 200, "owner adjust")
	if e.activityCount("user.balance_topped_up", cust) != 1 {
		t.Fatal("owner top up journal")
	}
}

// Суперадмин в режиме просмотра: чтение — да, запись — 403 read_only.
func TestAdminSuperadminReadOnly(t *testing.T) {
	e := newAdminEnv(t)
	_, owner := e.staff("Владелец", "owner")
	e.openShift(owner, 0)
	_, phone := e.member("Супер", "", "", true)
	tok := e.login(phone)
	expect(t, e.post(tok, fmt.Sprintf("/api/superadmin/clubs/%d/view", e.club), nil), 200, "view")
	expect(t, e.get(tok, "/api/cash/shift"), 200, "read")
	expect(t, e.get(tok, "/api/customers"), 200, "read customers")
	r := e.post(tok, "/api/cash/transactions", map[string]any{"type": "income", "amount_tiyn": 100, "description": "x"})
	if r.status != 403 || r.body["error"] != permissions.CodeReadOnly || r.body["message"] != permissions.MsgReadOnly {
		t.Fatalf("write in view: %d %v", r.status, r.body)
	}
}

// Чужой клуб: id из другого клуба — 404 (RLS + фильтр club_id).
func TestAdminOtherClubNotFound(t *testing.T) {
	a := newAdminEnv(t)
	_, ownerA := a.staff("A", "owner")
	a.openShift(ownerA, 0)
	pa := a.product("A-товар", 1000, 5)
	sale := a.post(ownerA, "/api/cash/sales", map[string]any{"items": []map[string]any{{"product_id": pa, "quantity": 1}}})
	saleID := num(obj(sale.body["transaction"])["id"])
	custA := a.customer(ownerA, "Клиент A", 0, 0)

	b := newAdminEnv(t)
	_, ownerB := b.staff("B", "owner")
	b.openShift(ownerB, 100000)
	expect(t, b.get(ownerB, fmt.Sprintf("/api/cash/receipts/%d", saleID)), 404, "receipt")
	expect(t, b.post(ownerB, fmt.Sprintf("/api/cash/receipts/%d/refund", saleID), nil), 404, "refund")
	expect(t, b.get(ownerB, fmt.Sprintf("/api/customers/%d", custA)), 404, "customer")
	expect(t, b.get(ownerB, fmt.Sprintf("/api/customers/%d/ledger", custA)), 404, "ledger")
	r := b.post(ownerB, "/api/cash/balance-top-ups", map[string]any{"user_id": custA, "amount_tiyn": 100, "balance_type": "balance", "payment_method": "cash"})
	if r.status != 422 || fieldError(r, "user_id") == "" {
		t.Fatalf("top up other club: %d %v", r.status, r.body)
	}
	expect(t, do(t, "PATCH", b.srv.URL+fmt.Sprintf("/api/warehouse/products/%d", pa), map[string]any{"name": "x"}, bearer(ownerB)), 404, "product")
	if list := b.get(ownerB, "/api/warehouse/products"); len(list.body["items"].([]any)) != 0 {
		t.Fatalf("products of other club: %v", list.body)
	}
}

// ---------------------------------------------------------------------------
// Склад, клиенты, тарифы, сессии, карта
// ---------------------------------------------------------------------------

func TestAdminWarehouse(t *testing.T) {
	e := newAdminEnv(t)
	_, owner := e.staff("Владелец", "owner")
	e.product("Чипсы", 50000, 1)
	r := e.post(owner, "/api/warehouse/products", map[string]any{"sku": "COLA-1", "name": "Cola", "product_category_id": e.category, "price_tiyn": 70000, "quantity": 10})
	expect(t, r, 201, "create")
	id := num(r.body["id"])
	if e.activityCount("product.created", id) != 1 || e.activityCount("stock.received", id) != 1 || e.outboxCount("product.created") != 1 {
		t.Fatal("create journal")
	}
	expectField(t, e.post(owner, "/api/warehouse/products", map[string]any{"sku": "COLA-1", "name": "Cola2", "product_category_id": e.category, "price_tiyn": 1, "quantity": 0}),
		"sku", "The sku has already been taken.")
	u := do(t, "PATCH", e.srv.URL+fmt.Sprintf("/api/warehouse/products/%d", id), map[string]any{"price_tiyn": 75000}, bearer(owner))
	expect(t, u, 200, "update")
	if e.activityCount("product.updated", id) != 1 || fmt.Sprint(e.activityDetails("product.updated")["price"]) != "700.00 → 750.00" {
		t.Fatalf("update journal %v", e.activityDetails("product.updated"))
	}
	expectField(t, e.post(owner, "/api/warehouse/stock-movements", map[string]any{"product_id": id, "type": "write_off", "quantity": 11}), "quantity", "Нельзя списать больше, чем есть на складе.")
	m := e.post(owner, "/api/warehouse/stock-movements", map[string]any{"product_id": id, "type": "write_off", "quantity": 4, "note": "Брак"})
	expect(t, m, 201, "write off")
	if num(m.body["balance_after"]) != 6 || e.activityDetails("stock.written_off")["note"] != "Брак" {
		t.Fatalf("movement %v", m.body)
	}
	// Поиск и keyset.
	l := e.get(owner, "/api/warehouse/products?q=col")
	if items := l.body["items"].([]any); len(items) != 1 || obj(items[0])["name"] != "Cola" {
		t.Fatalf("search %v", l.body)
	}
	p1 := e.get(owner, "/api/warehouse/products?limit=1")
	cur, _ := p1.body["next_cursor"].(string)
	p2 := e.get(owner, "/api/warehouse/products?limit=1&cursor="+cur)
	p3 := e.get(owner, "/api/warehouse/products?limit=1&cursor="+p2.body["next_cursor"].(string))
	n1, n2 := obj(p1.body["items"].([]any)[0])["name"], obj(p2.body["items"].([]any)[0])["name"]
	if cur == "" || n1 == n2 || len(p3.body["items"].([]any)) != 0 || p3.body["next_cursor"] != nil {
		t.Fatalf("pages %v %v %v", p1.body, p2.body, p3.body)
	}
	expect(t, e.get(owner, "/api/warehouse/products?cursor=bm90LWpzb24"), 400, "bad cursor")
}

func TestAdminCustomers(t *testing.T) {
	e := newAdminEnv(t)
	_, owner := e.staff("Владелец", "owner")
	_, manager := e.staff("Менеджер", "manager")
	_, cashier := e.staff("Кассир", "cashier")
	a := e.customer(owner, "Алия", 150000, 4000)
	e.customer(owner, "Бекзат", 0, 0)
	l := e.get(cashier, "/api/customers?q=али")
	if items := l.body["items"].([]any); len(items) != 1 || num(obj(items[0])["balance_tiyn"]) != 150000 {
		t.Fatalf("search %v", l.body)
	}
	all := e.get(cashier, "/api/customers?limit=1")
	if all.body["next_after_id"] == nil {
		t.Fatalf("paging %v", all.body)
	}
	// Начальный баланс: кассир без customer_money.update — 403; менеджер — можно.
	c, _ := e.member("Новый", "client", "", false)
	r := e.post(cashier, fmt.Sprintf("/api/customers/%d/initial-balance", c), map[string]any{"balance_tiyn": 100, "bonus_balance_tiyn": 0})
	if r.status != 403 {
		t.Fatalf("cashier initial: %d %v", r.status, r.body)
	}
	expect(t, e.post(manager, fmt.Sprintf("/api/customers/%d/initial-balance", c), map[string]any{"balance_tiyn": 100, "bonus_balance_tiyn": 5}), 200, "manager initial")
	// История баланса из ledger.
	lg := e.get(cashier, fmt.Sprintf("/api/customers/%d/ledger", a))
	expect(t, lg, 200, "ledger")
	if items := lg.body["items"].([]any); len(items) != 2 {
		t.Fatalf("ledger %v", lg.body)
	}
	// Списание больше баланса.
	r = e.post(owner, fmt.Sprintf("/api/customers/%d/balance-adjustments", a), map[string]any{"balance_type": "bonus_balance", "action": "write_off", "amount_tiyn": 5000})
	expectField(t, r, "top_up_amount", "На балансе клиента меньше суммы списания: 40.00.")
	r = e.post(owner, fmt.Sprintf("/api/customers/%d/balance-adjustments", a), map[string]any{"balance_type": "balance", "action": "write_off", "amount_tiyn": 50000})
	expect(t, r, 200, "write off")
	if r.body["message"] != "Данные клиента сохранены. С баланса списано 500.00 без кассы." || num(obj(r.body["customer"])["balance_tiyn"]) != 100000 {
		t.Fatalf("write off %v", r.body)
	}
	if e.activityCount("user.balance_written_off", a) != 1 {
		t.Fatal("journal")
	}
	expect(t, e.get(cashier, "/api/customers/999999"), 404, "missing customer")
}

func TestAdminTariffsSessionsMap(t *testing.T) {
	e := newAdminEnv(t, "club-computers.guest-session")
	_, owner := e.staff("Владелец", "owner")
	_, operator := e.staff("Оператор", "operator")
	zone := e.scalar("INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, 'Hall', 60000, now(), now()) RETURNING id", e.club)
	pc1 := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, last_seen_at, created_at, updated_at) VALUES ($1, $2, 1, now(), now(), now()) RETURNING id", e.club, zone)
	pc2 := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, last_seen_at, created_at, updated_at) VALUES ($1, $2, 2, now(), now(), now()) RETURNING id", e.club, zone)
	tariff := e.scalar(`INSERT INTO tariffs (club_id, name, duration_minutes, price, payment_method, created_at, updated_at)
		VALUES ($1, 'Час', 60, 50000, 'main_or_bonus', now(), now()) RETURNING id`, e.club)
	e.exec("INSERT INTO club_zone_tariff (club_id, club_zone_id, tariff_id) VALUES ($1, $2, $3)", e.club, zone, tariff)

	tl := e.get(owner, "/api/tariffs")
	expect(t, tl, 200, "tariffs")
	if ts := tl.body["tariffs"].([]any); len(ts) != 1 || num(obj(ts[0])["price_tiyn"]) != 50000 || len(obj(ts[0])["zone_ids"].([]any)) != 1 {
		t.Fatalf("tariffs %v", tl.body)
	}
	if zs := tl.body["zones"].([]any); len(zs) != 1 || num(obj(zs[0])["price_per_hour_tiyn"]) != 60000 {
		t.Fatalf("zones %v", tl.body)
	}

	// Гостевая сессия: без смены — ошибка старого кода; со сменой — приход в кассу, журнал, событие карты.
	r := e.post(owner, "/api/club-computers/guest-session", map[string]any{"computer_ids": []int64{pc1}, "minutes": 60, "payment_method": "cash"})
	expectField(t, r, "payment_method", "Сначала откройте смену в кассе.")
	e.openShift(owner, 0)
	key := newKey()
	body := map[string]any{"computer_ids": []int64{pc1}, "minutes": 60, "payment_method": "cash"}
	st1, b1 := e.raw("POST", owner, "/api/club-computers/guest-session", key, body)
	if st1 != 201 || !strings.Contains(b1, `"amount_tiyn":60000`) {
		t.Fatalf("guest: %d %s", st1, b1)
	}
	if st2, b2 := e.raw("POST", owner, "/api/club-computers/guest-session", key, body); st2 != 201 || b2 != b1 {
		t.Fatalf("guest replay: %d %s", st2, b2)
	}
	if e.activityCount("computer.guest_session", 0) != 1 || e.activityCount("cash.income", 0) != 1 || e.outboxCount("computer.state") != 1 {
		t.Fatal("guest journal/outbox")
	}
	if d := e.activityDetails("computer.guest_session"); d["amount"] != "600.00" || d["payment_method"] != "наличные" || d["tariff"] != "—" {
		t.Fatalf("guest details %v", d)
	}
	if len(e.notifier.started) != 1 {
		t.Fatalf("agent start %v", e.notifier.started)
	}
	// Гостевой режим выключен — 403.
	e.settings(`{"guests": {"guest_mode_enabled": false}}`)
	if r := e.post(owner, "/api/club-computers/guest-session", body); r.status != 403 || r.body["message"] != "Гостевой режим отключён в настройках клуба." {
		t.Fatalf("guest off: %d %v", r.status, r.body)
	}
	e.settings(`{}`)

	// Карта: сессия из БД + присутствие.
	e.presence.mu.Lock()
	e.presence.states[e.club] = []presence.State{{ClubID: e.club, ComputerID: pc2, Online: true, AgentVersion: "1.2.3", LastSeenAt: time.Now()}}
	e.presence.mu.Unlock()
	m := e.get(operator, "/api/club-map/state")
	expect(t, m, 200, "map")
	var sid string
	for _, c := range m.body["computers"].([]any) {
		pc := obj(c)
		switch num(pc["id"]) {
		case pc1:
			if pc["has_session"] != true || pc["session_guest"] != true || pc["session_user"] != "Гость без аккаунта" || pc["connection"] != "offline" {
				t.Fatalf("pc1 %v", pc)
			}
			sid, _ = pc["session_id"].(string)
		case pc2:
			if pc["connection"] != "online" || pc["agent_version"] != "1.2.3" || pc["name"] != "PC-02" {
				t.Fatalf("pc2 %v", pc)
			}
		}
	}

	// Завершение сотрудником: повтор с тем же ключом — тот же ответ; журнал computer.session_ended.
	endKey := newKey()
	endPath := fmt.Sprintf("/api/club-computers/%d/end-session", pc1)
	expectField(t, e.post(operator, endPath, map[string]any{"session_id": "wrong"}), "session_id", "На компьютере нет такой активной сессии.")
	st, eb1 := e.raw("POST", operator, endPath, endKey, map[string]any{"session_id": sid})
	if st != 200 {
		t.Fatalf("end: %d %s", st, eb1)
	}
	if st, eb2 := e.raw("POST", operator, endPath, endKey, map[string]any{"session_id": sid}); st != 200 || eb2 != eb1 {
		t.Fatalf("end replay: %d %s", st, eb2)
	}
	if e.activityCount("computer.session_ended", pc1) != 1 || len(e.notifier.ended) != 1 {
		t.Fatal("end journal/notifier")
	}
	// Решение RQ-6 (docs/DECISIONS.md): пересадки сотрудником нет (новая функция) — маршрута нет.
	r = do(t, "POST", e.srv.URL+fmt.Sprintf("/api/club-computers/%d/transfer", pc2), map[string]any{"target_computer_id": pc1}, bearer(operator))
	if r.status != http.StatusNotFound && r.status != http.StatusMethodNotAllowed {
		t.Fatalf("staff transfer route: status %d, want 404/405", r.status)
	}
}

// loginEvents — журнал входа как app/hooks.go (activity.Record в тенантной транзакции клуба).
type loginEvents struct {
	auth.NopEvents
	t  *testing.T
	db *dbtest.DB
}

func (e loginEvents) LoginSucceeded(ctx context.Context, userID, clubID int64, userName string) {
	err := db.InTenantTx(ctx, e.db.App, clubID, func(q *store.Queries) error {
		_, err := activity.Record(ctx, q, activity.Entry{ClubID: clubID, Event: "auth.login", Description: "Вход в систему: " + userName, UserID: userID})
		return err
	})
	if err != nil {
		e.t.Errorf("auth.login: %v", err)
	}
}

// Вход через HTTP: в журнале auth.login с IP клиента (RequestMeta роутера, W-5).
func TestLoginActivityHasIP(t *testing.T) {
	d, vk := sharedAdmin(t)
	h, _ := password.New(password.Options{})
	sm, err := session.New(session.Options{Client: vk, Keys: []session.Key{{ID: "t", Secret: bytes.Repeat([]byte{9}, 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(auth.Options{Pool: d.App, Hasher: h, Sessions: sm, Events: loginEvents{t: t, db: d}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewRouter(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc}))
	t.Cleanup(srv.Close)
	e := &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("adm%d", adminSeq.Add(1)))}
	id, phone := e.member("Азат", "owner", "", false)
	r := do(t, "POST", srv.URL+"/api/auth/login", map[string]any{"phone": phone, "password": "pw"}, map[string]string{"User-Agent": "login-test"})
	expect(t, r, 200, "login")
	var ip, ua string
	if err := d.Owner.QueryRow(context.Background(),
		"SELECT ip_address, COALESCE(user_agent, '') FROM activity_logs WHERE club_id = $1 AND event = 'auth.login' AND user_id = $2", e.club, id).Scan(&ip, &ua); err != nil {
		t.Fatal(err)
	}
	if ip != "127.0.0.1" || ua != "login-test" {
		t.Fatalf("auth.login ip=%q ua=%q", ip, ua)
	}
}
