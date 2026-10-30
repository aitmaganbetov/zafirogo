package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
)

// testAuth — вместо middleware аутентификации: Principal из заголовков теста (только в тестах!).
func testAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-User") == "" {
			next.ServeHTTP(w, r)
			return
		}
		user, _ := strconv.ParseInt(r.Header.Get("X-Test-User"), 10, 64)
		club, _ := strconv.ParseInt(r.Header.Get("X-Test-Club"), 10, 64)
		var perms []string
		if s := r.Header.Get("X-Test-Perms"); s != "" {
			perms = strings.Split(s, ",")
		}
		p := principal.Principal{UserID: user, ClubID: club, Role: r.Header.Get("X-Test-Role"), Permissions: perms}
		next.ServeHTTP(w, r.WithContext(principal.WithPrincipal(r.Context(), p)))
	})
}

type who struct {
	user, club int64
	role       string
	perms      []string
}

func dial(ctx context.Context, t *testing.T, srvURL string, w who) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	if w.user > 0 {
		h.Set("X-Test-User", strconv.FormatInt(w.user, 10))
		h.Set("X-Test-Club", strconv.FormatInt(w.club, 10))
		h.Set("X-Test-Role", w.role)
		h.Set("X-Test-Perms", strings.Join(w.perms, ","))
	}
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(srvURL, "http"), &websocket.DialOptions{HTTPHeader: h})
}

func read(ctx context.Context, t *testing.T, c *websocket.Conn) (Message, error) {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, b, err := c.Read(rctx)
	if err != nil {
		return Message{}, err
	}
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m, nil
}

func waitConns(t *testing.T, h *Handler, club int64, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.Connections(club) != n {
		if time.Now().After(deadline) {
			t.Fatalf("club %d: %d connections, want %d", club, h.Connections(club), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func env(club int64, typ, id string) outbox.Envelope {
	return outbox.Envelope{ID: id, Type: typ, ClubID: club, CreatedAt: time.Now(), Data: json.RawMessage(`{"computer_id":7}`)}
}

func TestHubIsolationAndPermissions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, _ := NewHandler(Options{MaxConnsPerUser: 2})
	srv := httptest.NewServer(testAuth(h))
	defer srv.Close()

	a, _, err := dial(ctx, t, srv.URL, who{1, 10, "admin", []string{"club_map.view"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.CloseNow() }()
	cashier, _, err := dial(ctx, t, srv.URL, who{2, 10, "cashier", []string{"cash.view"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cashier.CloseNow() }()
	other, _, err := dial(ctx, t, srv.URL, who{3, 20, "admin", []string{"club_map.view"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.CloseNow() }()
	for _, c := range []*websocket.Conn{a, cashier, other} {
		if m, err := read(ctx, t, c); err != nil || m.Type != "hello" {
			t.Fatalf("hello: %+v %v", m, err)
		}
	}
	waitConns(t, h, 10, 2)

	h.Dispatch(env(10, "computer.state", "e1"))
	h.Dispatch(env(10, "cash.shift_changed", "e2"))
	h.Dispatch(env(20, "chat.message", "e3"))  // у сотрудника клуба 20 нет chat.view
	h.Dispatch(env(10, "unknown.thing", "e4")) // неизвестный раздел — никому

	if m, err := read(ctx, t, a); err != nil || m.ID != "e1" || m.Type != "computer.state" || string(m.Data) != `{"computer_id":7}` {
		t.Fatalf("a: %+v %v", m, err)
	}
	if m, err := read(ctx, t, cashier); err != nil || m.ID != "e2" {
		t.Fatalf("cashier: %+v %v", m, err)
	}
	h.Dispatch(env(10, "notifications.changed", "e5")) // всем сотрудникам
	if m, err := read(ctx, t, a); err != nil || m.ID != "e5" {
		t.Fatalf("a notifications: %+v %v", m, err)
	}
	if m, err := read(ctx, t, cashier); err != nil || m.ID != "e5" {
		t.Fatalf("cashier notifications: %+v %v", m, err)
	}
	if m, err := read(ctx, t, other); err == nil {
		t.Fatalf("club 20 got a foreign/forbidden event: %+v", m)
	}
}

func TestHubRejects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, _ := NewHandler(Options{MaxConnsPerUser: 1})
	srv := httptest.NewServer(testAuth(h))
	defer srv.Close()

	if _, resp, err := dial(ctx, t, srv.URL, who{}); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no principal: %v %v", resp, err)
	}
	if _, resp, err := dial(ctx, t, srv.URL, who{user: 1, club: 0, role: "admin"}); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no club: %v %v", resp, err)
	}
	c, _, err := dial(ctx, t, srv.URL, who{1, 10, "admin", nil})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	waitConns(t, h, 10, 1)
	if _, resp, err := dial(ctx, t, srv.URL, who{1, 10, "admin", nil}); err == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("limit: %v %v", resp, err)
	}
}

func TestHubSlowConsumerAndAccessChanged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, _ := NewHandler(Options{SendBuffer: 2})
	srv := httptest.NewServer(testAuth(h))
	defer srv.Close()

	// Медленный: не читает. Переполнение буфера → отключение 1008.
	slow, _, err := dial(ctx, t, srv.URL, who{1, 10, "admin", []string{"club_map.view"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slow.CloseNow() }()
	waitConns(t, h, 10, 1)
	for i := range 10000 {
		h.Dispatch(env(10, "computer.state", strconv.Itoa(i)))
		if h.Connections(10) == 0 {
			break
		}
	}
	waitConns(t, h, 10, 0)
	var err2 error
	for err2 == nil {
		_, err2 = read(ctx, t, slow)
	}
	if code := websocket.CloseStatus(err2); code != websocket.StatusPolicyViolation {
		t.Fatalf("slow consumer close = %v (%v)", code, err2)
	}

	// Права изменились — 4001, остальные сотрудники остаются.
	u1, _, err := dial(ctx, t, srv.URL, who{1, 10, "admin", nil})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u1.CloseNow() }()
	u2, _, err := dial(ctx, t, srv.URL, who{2, 10, "admin", nil})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u2.CloseNow() }()
	_, _ = read(ctx, t, u1)
	_, _ = read(ctx, t, u2)
	waitConns(t, h, 10, 2)
	h.Dispatch(outbox.Envelope{ID: "x", Type: AccessChangedEvent, ClubID: 10, Data: json.RawMessage(`{"user_id":1}`)})
	_, err = read(ctx, t, u1)
	if code := websocket.CloseStatus(err); code != CloseReauth {
		t.Fatalf("access changed close = %v (%v)", code, err)
	}
	waitConns(t, h, 10, 1)
}

func TestHubPingTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h, _ := NewHandler(Options{PingInterval: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond})
	srv := httptest.NewServer(testAuth(h))
	defer srv.Close()
	// Клиент, который не читает, не отвечает на ping (pong шлёт читатель) — сервер закрывает соединение.
	c, _, err := dial(ctx, t, srv.URL, who{1, 10, "admin", nil})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	waitConns(t, h, 10, 1)
	waitConns(t, h, 10, 0)

	// Читающий клиент отвечает на ping и остаётся.
	c2, _, err := dial(ctx, t, srv.URL, who{2, 10, "admin", nil})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.CloseNow() }()
	readErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := c2.Read(ctx); err != nil {
				readErr <- err
				return
			}
		}
	}()
	waitConns(t, h, 10, 1)
	time.Sleep(300 * time.Millisecond)
	if h.Connections(10) != 1 || len(readErr) > 0 {
		t.Fatal("reading client must stay connected")
	}
}

func TestAllowed(t *testing.T) {
	p := principal.Principal{UserID: 1, ClubID: 1, Role: "cashier", Permissions: []string{"cash.view", "orders.view"}}
	cases := map[string]bool{
		"cash.transaction":      true,
		"sale.completed":        true,
		"shop_order.created":    true,
		"notifications.changed": true,
		"computer.state":        false,
		"chat.message":          false,
		"todo.changed":          false,
		"staff.access_changed":  false,
		"":                      false,
	}
	for typ, want := range cases {
		if got := Allowed(p, typ); got != want {
			t.Errorf("Allowed(cashier, %q) = %v, want %v", typ, got, want)
		}
	}
	if Allowed(principal.Principal{Superadmin: true, ClubID: 1}, "chat.message") {
		t.Error("superadmin without a club role has no club rights")
	}
	viewer := principal.Principal{Superadmin: true, ReadOnly: true, ClubID: 1, Role: "owner", Permissions: []string{"chat.view"}}
	if !Allowed(viewer, "chat.message") {
		t.Error("superadmin viewing a club (owner, read-only) receives events")
	}
	if Allowed(principal.Principal{ClubID: 1}, "notifications.changed") {
		t.Error("no role — no notifications")
	}
}
