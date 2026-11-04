//go:build integration

package ratelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
)

var client valkey.Client

func TestMain(m *testing.M) {
	c, cleanup, err := authtest.StartValkey(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client = c
	code := m.Run()
	cleanup()
	os.Exit(code)
}

var seq atomic.Int64

func newLimiter(t *testing.T, o Options) *Limiter {
	t.Helper()
	o.Client = client
	o.Prefix = fmt.Sprintf("rl%d:", seq.Add(1))
	l, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// customer-login: 5 попыток в минуту на IP+телефон, 300 на IP; шестая — отказ с Retry-After ≤ 60 с.
func TestCustomerLoginLimit(t *testing.T) {
	ctx := context.Background()
	l := newLimiter(t, Options{})
	s := Subject{IP: "203.0.113.7", Phone: "+77011112233"}
	for i := range 5 {
		res, err := l.Allow(ctx, CustomerLogin, s)
		if err != nil || !res.Allowed {
			t.Fatalf("attempt %d: %+v, %v", i+1, res, err)
		}
		if res.Limit != 300 || res.Remaining != 300-i-1 {
			t.Errorf("headers of last limit (IP): %+v", res)
		}
	}
	res, err := l.Allow(ctx, CustomerLogin, s)
	if err != nil || res.Allowed {
		t.Fatalf("6th: %+v, %v", res, err)
	}
	if res.Limit != 5 || res.RetryAfter <= 0 || res.RetryAfter > time.Minute {
		t.Fatalf("429 result: %+v", res)
	}
	// Другой телефон с того же IP — свой счётчик (клуб за одним IP).
	if res, _ := l.Allow(ctx, CustomerLogin, Subject{IP: s.IP, Phone: "87012223344"}); !res.Allowed {
		t.Fatal("other phone must pass")
	}
}

// Скользящее окно: границу нельзя «перепрыгнуть» на стыке окон; отклонённые запросы не считаются.
func TestSlidingWindow(t *testing.T) {
	ctx := context.Background()
	l := newLimiter(t, Options{Rules: []Rule{{Name: "t", Limits: []Limit{{Max: 3, Window: 400 * time.Millisecond, Key: byIP}}}}})
	s := Subject{IP: "x"}
	for range 3 {
		if res, _ := l.Allow(ctx, "t", s); !res.Allowed {
			t.Fatal("within limit")
		}
	}
	for range 5 {
		if res, _ := l.Allow(ctx, "t", s); res.Allowed {
			t.Fatal("over limit")
		}
	}
	time.Sleep(450 * time.Millisecond)
	if res, _ := l.Allow(ctx, "t", s); !res.Allowed || res.Remaining != 2 {
		t.Fatalf("after window (denied requests must not count): %+v", res)
	}
}

// Атомарность: из 50 параллельных запросов проходит ровно Max.
func TestConcurrentExactlyMax(t *testing.T) {
	ctx := context.Background()
	l := newLimiter(t, Options{Rules: []Rule{{Name: "c", Limits: []Limit{{Max: 10, Window: time.Minute, Key: byIP}}}}})
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if res, err := l.Allow(ctx, "c", Subject{IP: "p"}); err == nil && res.Allowed {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("allowed %d, want 10", allowed.Load())
	}
}

// Решение Q-052 (docs/DECISIONS.md): web-login и web-register ограничены по умолчанию (5 в минуту на телефон и IP);
// выключаются только явно (ratelimit.disable), Enable снова включает выключенное правило.
func TestWebLimitsOnByDefault(t *testing.T) {
	ctx := context.Background()
	l := newLimiter(t, Options{})
	for _, rule := range []string{WebLogin, WebRegister} {
		if !l.Enabled(rule) {
			t.Fatalf("%s must be enabled by default", rule)
		}
		for range 5 {
			if res, _ := l.Allow(ctx, rule, Subject{IP: "a", Phone: "1"}); !res.Allowed {
				t.Fatalf("%s: first 5 attempts must pass", rule)
			}
		}
		if res, _ := l.Allow(ctx, rule, Subject{IP: "a", Phone: "1"}); res.Allowed {
			t.Fatalf("%s must limit by default", rule)
		}
	}
	l = newLimiter(t, Options{Disable: []string{WebLogin}})
	for range 10 {
		if res, _ := l.Allow(ctx, WebLogin, Subject{IP: "b", Phone: "2"}); !res.Allowed {
			t.Fatal("explicitly disabled web-login must not limit")
		}
	}
	l = newLimiter(t, Options{Rules: []Rule{{Name: "x", Limits: []Limit{perMinute(1, byIP)}, Disabled: true}}, Enable: []string{"x"}})
	_, _ = l.Allow(ctx, "x", Subject{IP: "c"})
	if res, _ := l.Allow(ctx, "x", Subject{IP: "c"}); res.Allowed {
		t.Fatal("Enable must turn on a disabled rule")
	}
	if _, err := New(Options{Client: client, Enable: []string{"nope"}}); err == nil {
		t.Fatal("unknown rule in Enable")
	}
}

// HTTP: 429 с текстом Laravel и заголовками ThrottleRequests.
func TestMiddleware429(t *testing.T) {
	l := newLimiter(t, Options{Enable: []string{WebLogin}})
	h := l.Middleware(WebLogin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		_ = json.NewDecoder(r.Body).Decode(&v)
		if v["phone"] != "+77015550011" {
			http.Error(w, "body lost", 500)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	do := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/auth/login", bytes.NewBufferString(`{"phone":"+77015550011","password":"x"}`))
		r.RemoteAddr = "198.51.100.1:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for i := range 5 {
		w := do()
		if w.Code != http.StatusNoContent {
			t.Fatalf("attempt %d: %d %s", i+1, w.Code, w.Body)
		}
		if w.Header().Get("X-RateLimit-Limit") != "300" {
			t.Errorf("X-RateLimit-Limit %q", w.Header().Get("X-RateLimit-Limit"))
		}
	}
	w := do()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d", w.Code)
	}
	var body struct{ Error, Message string }
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Message != "Too Many Attempts." || body.Error != "too_many_requests" {
		t.Fatalf("body %s", w.Body)
	}
	ra, _ := strconv.Atoi(w.Header().Get("Retry-After"))
	if ra < 1 || ra > 60 || w.Header().Get("X-RateLimit-Remaining") != "0" || w.Header().Get("X-RateLimit-Limit") != "5" ||
		w.Header().Get("X-RateLimit-Reset") == "" {
		t.Fatalf("headers %v", w.Header())
	}
}

// Valkey недоступен: по умолчанию отказ (503), с FailOpen — пропуск.
func TestValkeyDown(t *testing.T) {
	dead, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{"127.0.0.1:1"}, DisableCache: true,
		ForceSingleClient: true, ConnWriteTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Skip("valkey-go refuses unreachable address at construction:", err)
	}
	defer dead.Close()
	l, _ := New(Options{Client: dead})
	if _, err := l.Allow(context.Background(), CustomerLogin, Subject{IP: "a"}); err == nil {
		t.Fatal("fail closed expected")
	}
}
