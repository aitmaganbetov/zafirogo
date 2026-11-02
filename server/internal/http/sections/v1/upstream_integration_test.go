//go:build integration

package v1_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/apikey"
	httpserver "github.com/aitmaganbetov/zafirogo/server/internal/http"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	v1 "github.com/aitmaganbetov/zafirogo/server/internal/http/sections/v1"
)

// S6-O1-4: весь /api/v1 — в zafirogo; ключ клуба, ещё работающего в Laravel (его префикса нет в api_keys),
// проксируется туда без изменений; свой ключ (и отозванный) обслуживается здесь; без ключа — 401 здесь.
func TestV1LegacyUpstream(t *testing.T) {
	var (
		mu  sync.Mutex
		got []*http.Request
	)
	laravel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.Clone(r.Context()))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"from":"laravel"}}`))
	}))
	t.Cleanup(laravel.Close)
	up, _ := url.Parse(laravel.URL)
	e := newEnvWith(t, apikey.Limits{}, func(o *httpserver.Options) {
		o.Legacy = func(d *httpx.Deps, _ http.Handler) func(http.Handler) http.Handler {
			return v1.LegacyUpstream(d, up, "zafiro.kz")
		}
	})
	_, own := e.issueKey(e.club, e.owner, keyOpts{})
	revokedID, revoked := e.issueKey(e.club, e.owner, keyOpts{})
	e.exec("UPDATE api_keys SET revoked_at = now() WHERE id = $1", revokedID)
	foreign := "zfk_" + randomString(8, 36) + "_" + randomString(40, len(alnum))

	if r := e.get("/api/v1/me", "/me", own); r.status != http.StatusOK || r.data()["from"] != nil {
		t.Fatalf("own key: %d %s", r.status, r.raw)
	}
	r := e.get("/api/v1/products?per_page=5", "", foreign)
	if r.status != http.StatusOK || r.data()["from"] != "laravel" {
		t.Fatalf("foreign key: %d %s", r.status, r.raw)
	}
	mu.Lock()
	if len(got) != 1 || got[0].Host != "zafiro.kz" || got[0].URL.RequestURI() != "/api/v1/products?per_page=5" ||
		got[0].Header.Get("Authorization") != "Bearer "+foreign || got[0].Header.Get("X-Forwarded-For") != e.ip {
		t.Fatalf("proxied: %d requests, %+v", len(got), got)
	}
	mu.Unlock()
	expect(t, e.get("/api/v1/me", "/me", revoked), http.StatusUnauthorized, "api_key_revoked")
	expect(t, e.get("/api/v1/me", "/me", ""), http.StatusUnauthorized, "invalid_api_key")
	// Запрос в Laravel не пишется в журнал этого сервера (там свой); свой — пишется.
	time.Sleep(50 * time.Millisecond)
	if n := e.scalar("SELECT count(*) FROM api_request_logs WHERE key_prefix = $1", foreign[:12]); n != 0 {
		t.Errorf("proxied request logged here: %d", n)
	}
	mu.Lock()
	if len(got) != 1 {
		t.Errorf("own/revoked/no key must not be proxied: %d requests", len(got))
	}
	mu.Unlock()
}
