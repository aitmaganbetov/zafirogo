package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func newTestServer(t *testing.T, o Options) *httptest.Server {
	t.Helper()
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	srv := httptest.NewServer(NewRouter(o))
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // тестовый URL httptest
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp, body
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t, Options{})
	resp, body := getJSON(t, srv.URL+"/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if body["status"] != "ok" {
		t.Fatalf("body %v", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Referrer-Policy"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("security header %s missing", h)
		}
	}
}

func TestReadyzNoCheckers(t *testing.T) {
	srv := newTestServer(t, Options{})
	resp, body := getJSON(t, srv.URL+"/readyz")
	if resp.StatusCode != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("status %d body %v", resp.StatusCode, body)
	}
}

func TestReadyzFailingChecker(t *testing.T) {
	srv := newTestServer(t, Options{Checkers: []Checker{
		CheckFunc{CheckName: "postgres", Fn: func(context.Context) error {
			return errors.New("dial tcp 10.0.0.5:5432: password=secret")
		}},
		CheckFunc{CheckName: "valkey", Fn: func(context.Context) error { return nil }},
	}})
	resp, body := getJSON(t, srv.URL+"/readyz")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
	checks, _ := body["checks"].(map[string]any)
	if body["status"] != "fail" || checks["postgres"] != "fail" || checks["valkey"] != "ok" {
		t.Fatalf("body %v", body)
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	srv := newTestServer(t, Options{Frontend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("spa"))
	})})
	resp, body := getJSON(t, srv.URL+"/api/nope")
	if resp.StatusCode != http.StatusNotFound || body["error"] != "not_found" {
		t.Fatalf("status %d body %v", resp.StatusCode, body)
	}
	// /api/v1 — интеграционный API: конверт ошибки ApiError ({"error": {"code", "message"}}).
	resp, body = getJSON(t, srv.URL+"/api/v1/nope")
	if e, _ := body["error"].(map[string]any); resp.StatusCode != http.StatusNotFound || e["code"] != "not_found" {
		t.Fatalf("v1: status %d body %v", resp.StatusCode, body)
	}
}

func TestFrontendFallback(t *testing.T) {
	srv := newTestServer(t, Options{Frontend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("spa"))
	})})
	resp, err := http.Get(srv.URL + "/clubs/1") //nolint:gosec,noctx // тестовый URL httptest
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "spa" {
		t.Fatalf("status %d body %q", resp.StatusCode, b)
	}
}

func TestRecoverer(t *testing.T) {
	h := Recoverer(slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestRealIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	var got string
	h := RealIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.RemoteAddr }))

	cases := []struct {
		name, remote, xff, xrip, want string
	}{
		{"untrusted peer ignores headers", "203.0.113.7:5555", "1.2.3.4", "", "203.0.113.7:5555"},
		{"trusted peer uses rightmost untrusted XFF", "10.0.0.2:5555", "1.2.3.4, 198.51.100.9, 10.0.0.3", "", "198.51.100.9:0"},
		{"trusted peer uses X-Real-IP", "10.0.0.2:5555", "", "198.51.100.10", "198.51.100.10:0"},
		{"garbage XFF keeps peer", "10.0.0.2:5555", "not-an-ip", "", "10.0.0.2:5555"},
		{"all hops trusted keeps peer", "10.0.0.2:5555", "10.1.1.1", "", "10.0.0.2:5555"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remote
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if c.xrip != "" {
				r.Header.Set("X-Real-IP", c.xrip)
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			if got != c.want {
				t.Fatalf("RemoteAddr %q, want %q", got, c.want)
			}
		})
	}
}
