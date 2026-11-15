package v1_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/apikey"
	v1 "github.com/aitmaganbetov/zafirogo/server/internal/http/sections/v1"
)

// S6-O1-4: весь /api/v1 приходит сюда; ключ, которого здесь нет, уходит в Laravel, свой — обслуживается здесь.
func TestLegacyUpstreamRouting(t *testing.T) {
	const (
		own     = "zfk_own00001_" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		foreign = "zfk_laravel1_" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		broken  = "zfk_dbfail01_" + "cccccccccccccccccccccccccccccccccccccccc"
	)
	known := func(_ context.Context, prefix string) (bool, error) {
		switch prefix {
		case "zfk_own00001":
			return true, nil
		case "zfk_dbfail01":
			return false, errors.New("db down")
		}
		return false, nil
	}
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Where", "laravel")
		_, _ = w.Write([]byte(r.Method + " " + r.URL.RequestURI() + " " + string(b)))
	})
	here := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Where", "here") })
	h := v1.UpstreamMiddleware(known, upstream, slog.New(slog.NewTextHandler(io.Discard, nil)))(here)

	for _, tc := range []struct {
		name, path, auth, apiKey, want string
		status                         int
	}{
		{"свой ключ — здесь", "/api/v1/me", "Bearer " + own, "", "here", 200},
		{"чужой ключ — Laravel", "/api/v1/products?page=2", "Bearer " + foreign, "", "laravel", 200},
		{"чужой ключ в X-Api-Key — Laravel", "/api/v1/stock-movements", "", foreign, "laravel", 200},
		{"без ключа — здесь (401 как в Laravel)", "/api/v1/me", "", "", "here", 200},
		{"ключ не того формата — здесь", "/api/v1/me", "Bearer zfk_short", "", "here", 200},
		{"не /api/v1 — дальше", "/api/v10/me", "Bearer " + foreign, "", "here", 200},
		{"ошибка базы — 500, не Laravel", "/api/v1/me", "Bearer " + broken, "", "", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"x":1}`))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			if tc.apiKey != "" {
				req.Header.Set("X-Api-Key", tc.apiKey)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status || rec.Header().Get("X-Where") != tc.want {
				t.Fatalf("status %d, where %q; want %d %q (body %s)", rec.Code, rec.Header().Get("X-Where"), tc.status, tc.want, rec.Body)
			}
			if tc.want == "laravel" && rec.Body.String() != "POST "+tc.path+` {"x":1}` {
				t.Errorf("proxied request = %q", rec.Body)
			}
		})
	}
}

// Аудит безопасности A-06: запрос с ключом, которого здесь нет, — после блокировки IP за подбор, с пределом тела и
// без чтения Postgres на каждый запрос (кэш префиксов с коротким сроком и ограниченным размером).
func TestLegacyUpstreamLimits(t *testing.T) {
	key := func(prefix string) string { return "zfk_" + prefix + "_" + strings.Repeat("b", 40) }
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	lookups := map[string]int{}
	known := func(_ context.Context, prefix string) (bool, error) {
		lookups[prefix]++
		return prefix == "zfk_own00001", nil
	}
	proxied := 0
	var gotBody int
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied++
		b, _ := io.ReadAll(r.Body)
		gotBody = len(b)
		if strings.Contains(r.Header.Get("Authorization"), "zfk_laravel1_") {
			w.WriteHeader(http.StatusOK) // настоящий ключ клуба, работающего в Laravel
			return
		}
		w.WriteHeader(http.StatusUnauthorized) // подбор: Laravel ключ не знает
	})
	here := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Where", "here") })
	limits := apikey.Limits{FailedAuthPerMinute: 20, MaxRequestBytes: 1024}
	h := v1.UpstreamGate(known, upstream, slog.New(slog.NewTextHandler(io.Discard, nil)), &apikey.MemoryCounter{Now: clock}, limits, clock)(here)
	send := func(ip, token string, body io.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/me", body)
		req.RemoteAddr = ip + ":4000"
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Настоящий ключ Laravel: 500 запросов — одно чтение Postgres, блокировки нет.
	for i := 0; i < 500; i++ {
		if rec := send("10.0.0.1", key("laravel1"), nil); rec.Code != http.StatusOK {
			t.Fatalf("laravel key, request %d: %d", i, rec.Code)
		}
	}
	if lookups["zfk_laravel1"] != 1 {
		t.Fatalf("postgres lookups for one foreign prefix: %d, want 1", lookups["zfk_laravel1"])
	}
	// Свой ключ: одно чтение, остальное — из кэша.
	for i := 0; i < 100; i++ {
		if rec := send("10.0.0.1", key("own00001"), nil); rec.Header().Get("X-Where") != "here" {
			t.Fatalf("own key went upstream")
		}
	}
	if lookups["zfk_own00001"] != 1 {
		t.Fatalf("postgres lookups for own prefix: %d, want 1", lookups["zfk_own00001"])
	}
	// Срок промаха истёк — префикс перечитывается (ключ могли создать здесь при переходе клуба).
	now = now.Add(11 * time.Second)
	send("10.0.0.1", key("laravel1"), nil)
	if lookups["zfk_laravel1"] != 2 {
		t.Fatalf("lookup after the miss TTL: %d, want 2", lookups["zfk_laravel1"])
	}

	// Подбор: 400 случайных префиксов с одного IP — в Laravel и в Postgres уходит не больше лимита, дальше 429.
	proxied = 0
	total := 0
	for _, n := range lookups {
		total += n
	}
	var tooMany int
	for i := 0; i < 400; i++ {
		rec := send("10.0.0.2", key(fmt.Sprintf("rnd%05d", i)), nil)
		switch rec.Code {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			tooMany++
			if rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "too_many_failed_attempts") {
				t.Fatalf("429 body/headers: %v %s", rec.Header(), rec.Body)
			}
		default:
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	after := 0
	for _, n := range lookups {
		after += n
	}
	if proxied != 20 || tooMany != 380 || after-total != 20 {
		t.Fatalf("proxied %d, 429 %d, postgres lookups %d; want 20, 380, 20", proxied, tooMany, after-total)
	}
	// Другой IP не заблокирован; через минуту блокировка снята.
	if rec := send("10.0.0.3", key("laravel1"), nil); rec.Code != http.StatusOK {
		t.Fatalf("another ip: %d", rec.Code)
	}
	now = now.Add(61 * time.Second)
	if rec := send("10.0.0.2", key("laravel1"), nil); rec.Code != http.StatusOK {
		t.Fatalf("after the window: %d", rec.Code)
	}

	// Предел тела: по Content-Length — 413 без обращения к Laravel; тело в пределах — проходит целиком.
	proxied = 0
	if rec := send("10.0.0.4", key("laravel1"), strings.NewReader(strings.Repeat("x", 1025))); rec.Code != http.StatusRequestEntityTooLarge || proxied != 0 {
		t.Fatalf("oversized body: %d, proxied %d", rec.Code, proxied)
	}
	if rec := send("10.0.0.4", key("laravel1"), strings.NewReader(strings.Repeat("x", 1024))); rec.Code != http.StatusOK || gotBody != 1024 {
		t.Fatalf("body within the limit: %d, %d bytes", rec.Code, gotBody)
	}
	// Тело без Content-Length (chunked) обрезается при чтении.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me", io.NopCloser(strings.NewReader(strings.Repeat("x", 5000))))
	req.ContentLength = -1
	req.RemoteAddr = "10.0.0.4:4000"
	req.Header.Set("Authorization", "Bearer "+key("laravel1"))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gotBody > 1024 {
		t.Fatalf("chunked body passed %d bytes, the limit is 1024", gotBody)
	}
}
