package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// Mounts: обработчик под RequireAuth+RequireClub, без таймаута запроса (долгие соединения /ws).
func TestMounts(t *testing.T) {
	var hadDeadline bool
	ws := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadDeadline = r.Context().Deadline()
		w.WriteHeader(http.StatusTeapot)
	})
	router := NewRouter(Options{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		RequestTimeout: time.Second,
		Mounts:         map[string]http.Handler{"/ws": ws, "/nil": nil},
	})
	// Principal подставляет «внешний» слой (в проде — auth.Service.Middleware внутри роутера).
	var p *principal.Principal
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p != nil {
			r = r.WithContext(principal.WithPrincipal(r.Context(), *p))
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	call := func(method string) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), method, srv.URL+"/ws", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}

	if got := call(http.MethodGet); got != http.StatusUnauthorized {
		t.Fatalf("anon: %d", got)
	}
	p = &principal.Principal{UserID: 1}
	if got := call(http.MethodGet); got != http.StatusForbidden {
		t.Fatalf("no club: %d", got)
	}
	p = &principal.Principal{UserID: 1, ClubID: 7, Role: "owner", ReadOnly: true}
	if got := call(http.MethodPost); got != http.StatusForbidden {
		t.Fatalf("read-only POST: %d", got)
	}
	if got := call(http.MethodGet); got != http.StatusTeapot {
		t.Fatalf("read-only GET: %d", got)
	}
	p = &principal.Principal{UserID: 1, ClubID: 7, Role: "cashier"}
	if got := call(http.MethodGet); got != http.StatusTeapot {
		t.Fatalf("staff: %d", got)
	}
	if hadDeadline {
		t.Fatal("mounted handler must not inherit RequestTimeout")
	}
	// nil-обработчик не монтируется: путь уходит в общий 404.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/nil", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("nil mount: %d", res.StatusCode)
	}
}
