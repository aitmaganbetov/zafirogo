package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// RequestMeta: IP — после доверенного прокси (X-Forwarded-For), UA и префикс API-ключа — в контексте обработчика.
func TestRequestMeta(t *testing.T) {
	var got activity.RequestMeta
	var ok bool
	router := NewRouter(Options{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
		APIKeyPrefix:   func(r *http.Request) string { return r.Header.Get("X-Test-Key") },
		Mounts: map[string]http.Handler{"/probe": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok = activity.RequestMetaFromContext(r.Context())
			w.WriteHeader(http.StatusNoContent)
		})},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(principal.WithPrincipal(r.Context(), principal.Principal{UserID: 1, ClubID: 1, Role: "owner"}))
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/probe", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("User-Agent", "probe/1")
	req.Header.Set("X-Test-Key", "zk_abc")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if !ok || got.IP != "203.0.113.9" || got.UserAgent != "probe/1" || got.APIKeyPrefix != "zk_abc" {
		t.Fatalf("meta %+v ok=%v", got, ok)
	}
}
