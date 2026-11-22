package legacy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Аудит SEC-9: запрос каталога старого сервера (ответ — в общий кэш) не берёт Host клиента: без mobile.upstream_host —
// хост адреса апстрима, с ним — он.
func TestUpstreamCatalogHost(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Host
		_, _ = w.Write([]byte(`{}`))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	from := catalogClientOf(&http.Request{RemoteAddr: "192.0.2.7:5000", Host: "evil.example", Header: http.Header{}})
	for _, tc := range []struct{ upstreamHost, want string }{{"", u.Host}, {"zafiro.kz", "zafiro.kz"}} {
		a := &API{o: Options{Upstream: u, UpstreamHost: tc.upstreamHost, Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
			upstreamClient: up.Client()}
		if _, err := a.upstreamCatalog(context.Background(), from, "", "ru"); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("upstream_host %q: Host %q, want %q", tc.upstreamHost, got, tc.want)
		}
	}
}
