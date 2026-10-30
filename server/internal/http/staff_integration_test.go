//go:build integration

package httpserver

// Сквозные тесты экранов «Пользователи», «Работники», приглашений и «Права» (Этап 3, G3):
// UserController::store/update/resetPassword, WorkerController, ClubInvitationController::store, PermissionController.

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

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
)

// staffEnv — adminEnv с пулом платформы и хешером (создание клиентов, сброс пароля, «сотрудник другого клуба»).
type staffEnv struct {
	*adminEnv
	mu       sync.Mutex
	signOuts []int64
}

func newStaffEnv(t *testing.T) *staffEnv {
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
	n := &fakeNotifier{}
	cashSvc := &cashsvc.Service{Notifier: n}
	bill := billingsvc.New(d.App, nil, cashSvc)
	bill.SetNotifier(n)
	e := &staffEnv{}
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim,
		Pool: d.App, PlatformPool: d.Platform, Passwords: h, Billing: bill, Cash: cashSvc,
		SignOutGuest: func(_ context.Context, _, userID int64, _ string) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.signOuts = append(e.signOuts, userID)
		},
	}))
	t.Cleanup(srv.Close)
	e.adminEnv = &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("stf%d", adminSeq.Add(1))), notifier: n}
	return e
}

func (e *staffEnv) send(method, tok, path, key string, body any) resp {
	e.t.Helper()
	h := bearer(tok)
	if key != "" {
		h["Idempotency-Key"] = key
	}
	return do(e.t, method, e.srv.URL+path, body, h)
}

func (e *staffEnv) phone() string { return fmt.Sprintf("70%08d", adminSeq.Add(1)) }

func TestCustomersScreen(t *testing.T) {
	e := newStaffEnv(t)
	_, owner := e.staff("Владелец", "owner")
	_, cashier := e.staff("Кассир", "cashier")

	var created int64
	t.Run("создание с начальным балансом через ledger, телефон с 8", func(t *testing.T) {
		ph := e.phone()
		r := e.send("POST", owner, "/api/customers", newKey(), map[string]any{
			"name": "Новый Клиент", "phone": "8" + ph, "email": "new@example.test", "password": "secret1",
			"password_confirmation": "secret1", "discount": "7.5", "balance_tiyn": 150050, "bonus_balance_tiyn": 1000,
		})
		if r.status != http.StatusCreated {
			t.Fatalf("%d %v", r.status, r.body)
		}
		c := r.body["customer"].(map[string]any)
		created = int64(c["user_id"].(float64))
		if c["phone"] != "+7"+ph || c["discount"] != "7.50" || c["balance_tiyn"].(float64) != 150050 || r.body["message"] != "Пользователь добавлен." {
			t.Fatalf("%v", r.body)
		}
		if m, b := e.balances(created); m != 150050 || b != 1000 {
			t.Fatalf("balances %d %d", m, b)
		}
		if n := e.scalar("SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'initial_balance'", e.club); n != 1 {
			t.Fatalf("ledger ops %d", n)
		}
		if n := e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'user.created'", e.club); n != 1 {
			t.Fatalf("activity %d", n)
		}
		// Повтор того же ключа — не создаёт второго клиента; тот же телефон с новым ключом — 422 unique.
		r = e.send("POST", owner, "/api/customers", newKey(), map[string]any{
			"name": "Другой", "phone": ph, "password": "secret1", "password_confirmation": "secret1",
		})
		if r.status != 422 || fieldError(r, "phone") == "" {
			t.Fatalf("dup phone: %d %v", r.status, r.body)
		}
	})

	t.Run("все ошибки формы сразу", func(t *testing.T) {
		r := e.send("POST", owner, "/api/customers", newKey(), map[string]any{
			"name": "", "phone": "123", "email": "bad", "password": "123", "password_confirmation": "123", "discount": "101",
		})
		if r.status != 422 {
			t.Fatalf("%d %v", r.status, r.body)
		}
		for _, f := range []string{"name", "phone", "email", "password", "discount"} {
			if fieldError(r, f) == "" {
				t.Fatalf("no error for %s: %v", f, r.body)
			}
		}
	})

	t.Run("кассир без customer_money: баланс — 403, без баланса — можно", func(t *testing.T) {
		r := e.send("POST", cashier, "/api/customers", newKey(), map[string]any{
			"name": "Х", "phone": e.phone(), "password": "secret1", "password_confirmation": "secret1", "balance_tiyn": 100,
		})
		if r.status != 403 || r.body["message"] != "Скидку и баланс клиента задаёт менеджер; пополнение — через кассу." {
			t.Fatalf("%d %v", r.status, r.body)
		}
		r = e.send("POST", cashier, "/api/customers", newKey(), map[string]any{
			"name": "Х", "phone": e.phone(), "password": "secret1", "password_confirmation": "secret1",
		})
		if r.status != 201 {
			t.Fatalf("%d %v", r.status, r.body)
		}
	})

	t.Run("изменение: имя, скидка и пополнение владельцем одной транзакцией", func(t *testing.T) {
		path := fmt.Sprintf("/api/customers/%d", created)
		r := e.send("PATCH", owner, path, newKey(), map[string]any{
			"name": "Переименован", "email": "new@example.test", "discount": "10",
			"top_up_amount_tiyn": 5000, "top_up_balance": "balance", "balance_action": "top_up",
		})
		if r.status != 200 || r.body["message"] != "Данные клиента сохранены. Баланс пополнен на 50.00 без кассы." {
			t.Fatalf("%d %v", r.status, r.body)
		}
		if m, _ := e.balances(created); m != 155050 {
			t.Fatalf("main %d", m)
		}
		// Списание больше остатка — 422, имя не меняется (откат всей транзакции, как DB::transaction).
		r = e.send("PATCH", owner, path, newKey(), map[string]any{
			"name": "Не сохранится", "top_up_amount_tiyn": 99999999, "top_up_balance": "balance", "balance_action": "write_off",
		})
		if r.status != 422 || !strings.HasPrefix(fieldError(r, "top_up_amount"), "На балансе клиента меньше суммы списания") {
			t.Fatalf("%d %v", r.status, r.body)
		}
		var name string
		_ = e.db.Owner.QueryRow(context.Background(), "SELECT name FROM users WHERE id = $1", created).Scan(&name)
		if name != "Переименован" {
			t.Fatalf("name %q", name)
		}
		// Пополнение без ключа идемпотентности — 400.
		r = e.send("PATCH", owner, path, "", map[string]any{"name": "Переименован", "top_up_amount_tiyn": 100, "top_up_balance": "balance"})
		if r.status != 400 {
			t.Fatalf("%d %v", r.status, r.body)
		}
		// Кассир: скидка и пополнение — prohibited.
		r = e.send("PATCH", cashier, path, "", map[string]any{"name": "Переименован", "discount": "5", "top_up_amount_tiyn": 100})
		if r.status != 422 || fieldError(r, "discount") != "Скидку клиента задаёт менеджер." ||
			fieldError(r, "top_up_amount") != "Пополнять и списывать баланс без кассы может только владелец клуба." {
			t.Fatalf("%d %v", r.status, r.body)
		}
	})

	t.Run("сотрудник другого клуба: имя не меняется, пароль не сбрасывается", func(t *testing.T) {
		id, _ := e.member("Чужой Админ", "client", "", false)
		other := e.db.Club(t, fmt.Sprintf("oth%d", adminSeq.Add(1)))
		e.exec("INSERT INTO club_memberships (club_id, user_id, role, created_at, updated_at) VALUES ($1, $2, 'admin', now(), now())", other, id)
		list := e.get(owner, "/api/customers?q=Чужой")
		items := list.body["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["account_locked"] != true {
			t.Fatalf("%v", list.body)
		}
		r := e.send("PATCH", owner, fmt.Sprintf("/api/customers/%d", id), "", map[string]any{"name": "Новое имя"})
		if r.status != 422 || !strings.HasPrefix(fieldError(r, "name"), "Это сотрудник другого клуба") {
			t.Fatalf("%d %v", r.status, r.body)
		}
		r = e.send("POST", owner, fmt.Sprintf("/api/customers/%d/password", id), "", nil)
		if r.status != 403 {
			t.Fatalf("%d %v", r.status, r.body)
		}
		// Сотрудник этого клуба — 403 «Сотрудников меняют на странице «Работники».»
		staffID, _ := e.staff("Оператор", "operator")
		r = e.send("PATCH", owner, fmt.Sprintf("/api/customers/%d", staffID), "", map[string]any{"name": "x"})
		if r.status != 403 || r.body["message"] != "Сотрудников меняют на странице «Работники»." {
			t.Fatalf("%d %v", r.status, r.body)
		}
	})

	t.Run("сброс пароля: один раз на экран, no-store, выход на ПК", func(t *testing.T) {
		r := e.send("POST", owner, fmt.Sprintf("/api/customers/%d/password", created), "", nil)
		pw, _ := r.body["password"].(string)
		if r.status != 200 || len(pw) != 8 || r.header.Get("Cache-Control") != "no-store, private" {
			t.Fatalf("%d %v %v", r.status, r.body, r.header)
		}
		var hash string
		_ = e.db.Owner.QueryRow(context.Background(), "SELECT password FROM users WHERE id = $1", created).Scan(&hash)
		h, _ := password.New(password.Options{})
		if _, err := h.Verify(context.Background(), pw, hash); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if n := e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND action = 'user.password_reset'", e.club); n != 1 {
			t.Fatalf("audit %d", n)
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if len(e.signOuts) != 1 || e.signOuts[0] != created {
			t.Fatalf("sign outs %v", e.signOuts)
		}
	})

	t.Run("keyset вперёд и назад, всего", func(t *testing.T) {
		r := e.get(owner, "/api/customers?limit=2")
		total := r.body["total"].(float64)
		if total < 3 || r.body["prev_before_id"] != nil || r.body["next_after_id"] == nil {
			t.Fatalf("%v", r.body)
		}
		next := int64(r.body["next_after_id"].(float64))
		r2 := e.get(owner, fmt.Sprintf("/api/customers?limit=2&after_id=%d", next))
		if r2.body["prev_before_id"] == nil {
			t.Fatalf("%v", r2.body)
		}
		back := e.get(owner, fmt.Sprintf("/api/customers?limit=2&before_id=%d", int64(r2.body["prev_before_id"].(float64))))
		if fmt.Sprint(back.body["items"]) != fmt.Sprint(r.body["items"]) || back.body["prev_before_id"] != nil {
			t.Fatalf("back %v\nfirst %v", back.body, r.body)
		}
	})
}

func TestWorkersScreen(t *testing.T) {
	e := newStaffEnv(t)
	ownerID, owner := e.staff("Владелец", "owner")
	_, admin := e.staff("Админ", "admin")
	_ = ownerID
	clientID, clientPhone := e.member("Будущий кассир", "client", "", false)

	t.Run("список по ролям, назначаемые роли", func(t *testing.T) {
		r := e.get(owner, "/api/workers")
		items := r.body["items"].([]any)
		if r.status != 200 || len(items) != 2 || items[0].(map[string]any)["role"] != "owner" || items[0].(map[string]any)["manageable"] != false {
			t.Fatalf("%d %v", r.status, r.body)
		}
		if n := len(r.body["assignable_roles"].([]any)); n != 4 {
			t.Fatalf("owner assignable %d", n)
		}
		if n := len(e.get(admin, "/api/workers").body["assignable_roles"].([]any)); n != 3 {
			t.Fatalf("admin assignable %d", n)
		}
	})

	var membership int64
	t.Run("добавить по телефону", func(t *testing.T) {
		r := e.send("POST", owner, "/api/workers", "", map[string]any{"phone": "+7 999 000 00 00", "role": "cashier"})
		if r.status != 422 || fieldError(r, "phone") != "Пользователь с таким номером не зарегистрирован в Zafiro. Отправьте ему ссылку-приглашение." {
			t.Fatalf("%d %v", r.status, r.body)
		}
		r = e.send("POST", owner, "/api/workers", "", map[string]any{"phone": "12", "role": "cashier"})
		if r.status != 422 || fieldError(r, "phone") != "Введите 10 цифр номера после +7." {
			t.Fatalf("%d %v", r.status, r.body)
		}
		r = e.send("POST", admin, "/api/workers", "", map[string]any{"phone": clientPhone, "role": "admin"})
		if r.status != 422 || fieldError(r, "role") == "" {
			t.Fatalf("admin gives admin: %d %v", r.status, r.body)
		}
		r = e.send("POST", owner, "/api/workers", "", map[string]any{"phone": clientPhone, "role": "cashier"})
		if r.status != 200 || r.body["message"] != "Работник добавлен." {
			t.Fatalf("%d %v", r.status, r.body)
		}
		membership = e.scalar("SELECT id FROM club_memberships WHERE club_id = $1 AND user_id = $2 AND role = 'cashier'", e.club, clientID)
		r = e.send("POST", owner, "/api/workers", "", map[string]any{"phone": clientPhone, "role": "operator"})
		if r.status != 422 || fieldError(r, "phone") != "Этот пользователь уже работает в клубе." {
			t.Fatalf("%d %v", r.status, r.body)
		}
		if n := e.scalar("SELECT count(*) FROM outbox_events WHERE club_id = $1 AND type = 'staff.access_changed'", e.club); n != 1 {
			t.Fatalf("outbox %d", n)
		}
	})

	t.Run("смена роли и запреты", func(t *testing.T) {
		r := e.send("PATCH", owner, fmt.Sprintf("/api/workers/%d", membership), "", map[string]any{"role": "operator"})
		if r.status != 200 || r.body["message"] != "Роль изменена." {
			t.Fatalf("%d %v", r.status, r.body)
		}
		adminMembership := e.scalar("SELECT id FROM club_memberships WHERE club_id = $1 AND role = 'admin'", e.club)
		r = e.send("PATCH", admin, fmt.Sprintf("/api/workers/%d", adminMembership), "", map[string]any{"role": "manager"})
		if r.status != 403 || r.body["message"] != "Свою роль изменить нельзя." {
			t.Fatalf("%d %v", r.status, r.body)
		}
		ownerMembership := e.scalar("SELECT id FROM club_memberships WHERE club_id = $1 AND role = 'owner'", e.club)
		if r = e.send("PATCH", admin, fmt.Sprintf("/api/workers/%d", ownerMembership), "", map[string]any{"role": "manager"}); r.status != 404 {
			t.Fatalf("%d %v", r.status, r.body)
		}
		if n := e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND category = 'permission' AND action = 'worker.role_changed'", e.club); n != 1 {
			t.Fatalf("audit %d", n)
		}
	})

	t.Run("убрать: роль client, сессии отозваны", func(t *testing.T) {
		tok := e.login(clientPhone)
		if r := e.get(tok, "/api/customers"); r.status != 403 && r.status != 200 {
			t.Fatalf("before: %d", r.status)
		}
		r := e.send("DELETE", owner, fmt.Sprintf("/api/workers/%d", membership), "", nil)
		if r.status != 200 || r.body["message"] != "Работник убран из сотрудников. Аккаунт остался клиентом клуба." {
			t.Fatalf("%d %v", r.status, r.body)
		}
		if n := e.scalar("SELECT count(*) FROM club_memberships WHERE id = $1 AND role = 'client'", membership); n != 1 {
			t.Fatal("role not client")
		}
		if r := e.get(tok, "/api/auth/me"); r.status != 401 {
			t.Fatalf("session alive: %d", r.status)
		}
	})

	t.Run("приглашение", func(t *testing.T) {
		r := e.send("POST", owner, "/api/club-invitations", "", map[string]any{"email": "inv@example.test", "role": "operator"})
		path, _ := r.body["path"].(string)
		if r.status != 201 || !strings.HasPrefix(path, "/invitations/") || len(path) != len("/invitations/")+64 ||
			r.header.Get("Cache-Control") != "no-store, private" {
			t.Fatalf("%d %v", r.status, r.body)
		}
		if n := e.scalar("SELECT count(*) FROM club_invitations WHERE club_id = $1 AND accepted_at IS NULL AND role = 'operator'", e.club); n != 1 {
			t.Fatalf("invitations %d", n)
		}
		_, manager := e.staff("Менеджер", "manager", `{"workers.create": true}`)
		if r := e.send("POST", manager, "/api/club-invitations", "", map[string]any{"role": "admin"}); r.status != 403 {
			t.Fatalf("manager admin invite: %d %v", r.status, r.body)
		}
	})
}

func TestPermissionsScreen(t *testing.T) {
	e := newStaffEnv(t)
	_, owner := e.staff("Владелец", "owner")
	_, admin := e.staff("Админ", "admin")
	_, cashierPhone := e.member("Кассир", "cashier", "", false)
	cashierTok := e.login(cashierPhone)
	cashier := e.scalar("SELECT id FROM club_memberships WHERE club_id = $1 AND role = 'cashier'", e.club)
	adminM := e.scalar("SELECT id FROM club_memberships WHERE club_id = $1 AND role = 'admin'", e.club)

	r := e.get(admin, "/api/settings/permissions")
	if r.status != 200 || len(r.body["items"].([]any)) != 2 {
		t.Fatalf("%d %v", r.status, r.body)
	}
	for _, it := range r.body["items"].([]any) {
		m := it.(map[string]any)
		if (m["role"] == "admin") == (m["editable"] == true) {
			t.Fatalf("editable %v", m)
		}
	}
	if r := e.get(admin, fmt.Sprintf("/api/settings/permissions/%d", adminM)); r.status != 403 || r.body["message"] != "Права администратора задаёт только владелец клуба." {
		t.Fatalf("%d %v", r.status, r.body)
	}
	if r := e.get(cashierTok, "/api/settings/permissions"); r.status != 403 {
		t.Fatalf("cashier: %d", r.status)
	}

	m := e.get(owner, fmt.Sprintf("/api/settings/permissions/%d", cashier))
	if m.status != 200 || len(m.body["sections"].([]any)) != 18 || len(m.body["actions"].([]any)) != 4 {
		t.Fatalf("%d %v", m.status, m.body)
	}
	// Права роли кассира, но без customers.view и с cash.delete.
	rights := map[string]map[string]bool{}
	for _, s := range m.body["sections"].([]any) {
		sec := s.(map[string]any)
		name := sec["section"].(string)
		rights[name] = map[string]bool{}
		for _, c := range sec["cells"].([]any) {
			cell := c.(map[string]any)
			rights[name][cell["action"].(string)] = cell["allowed"].(bool)
		}
	}
	rights["customers"]["view"] = false
	rights["cash"]["delete"] = true
	u := e.send("PUT", owner, fmt.Sprintf("/api/settings/permissions/%d", cashier), "", map[string]any{"rights": rights})
	if u.status != 200 || u.body["message"] != "Права сохранены." {
		t.Fatalf("%d %v", u.status, u.body)
	}
	var stored string
	_ = e.db.Owner.QueryRow(context.Background(), "SELECT permissions::text FROM club_memberships WHERE id = $1", cashier).Scan(&stored)
	if stored != `{"cash.delete": true, "customers.view": false}` {
		t.Fatalf("stored %s", stored)
	}
	// Права действуют со следующего запроса (та же сессия).
	if r := e.get(cashierTok, "/api/customers"); r.status != 403 {
		t.Fatalf("customers after revoke: %d", r.status)
	}
	if n := e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND action = 'permissions.updated'", e.club); n != 1 {
		t.Fatalf("audit %d", n)
	}
	if n := e.scalar("SELECT count(*) FROM outbox_events WHERE club_id = $1 AND type = 'staff.access_changed'", e.club); n != 1 {
		t.Fatalf("outbox %d", n)
	}
	cells := e.get(owner, fmt.Sprintf("/api/settings/permissions/%d", cashier))
	if cells.body["member"].(map[string]any)["customized"] != true {
		t.Fatalf("%v", cells.body["member"])
	}

	if r := e.send("POST", owner, fmt.Sprintf("/api/settings/permissions/%d/reset", cashier), "", nil); r.status != 200 || r.body["message"] != "Права сотрудника снова по роли." {
		t.Fatalf("%d %v", r.status, r.body)
	}
	if r := e.get(cashierTok, "/api/customers"); r.status != 200 {
		t.Fatalf("customers after reset: %d", r.status)
	}
}
