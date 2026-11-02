package v1_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
