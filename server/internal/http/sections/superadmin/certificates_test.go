package superadmin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// Аудит CODE-3: домены проверки — Certificates::hosts().
func TestCertificateHosts(t *testing.T) {
	d := &httpx.Deps{ResellerDomain: "resseler.zafiro.kz"}
	d.Superadmin.PublicURL = "https://zafiro.kz"
	d.Superadmin.Domain = "admin.zafiro.kz"
	if got, want := certificateHosts(d), []string{"zafiro.kz", "admin.zafiro.kz", "resseler.zafiro.kz"}; !reflect.DeepEqual(got, want) {
		t.Errorf("defaults: %v, want %v", got, want)
	}
	// localhost, *.localhost, пустые и повторы — отбрасываются.
	d = &httpx.Deps{ResellerDomain: "resseler.localhost"}
	d.Superadmin.PublicURL = "http://localhost:8080"
	d.Superadmin.Domain = "admin.localhost"
	if got := certificateHosts(d); len(got) != 0 {
		t.Errorf("local stand: %v", got)
	}
	// superadmin.ssl_hosts (MONITORING_SSL_HOSTS) заменяет домены по умолчанию.
	d.Superadmin.SSLHosts = []string{" zafiro.kz", "zafiro.kz", "", "wiki.zafiro.kz"}
	if got, want := certificateHosts(d), []string{"zafiro.kz", "wiki.zafiro.kz"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ssl_hosts: %v, want %v", got, want)
	}
}

// Экран не ждёт сети: первый показ — «Ещё не проверялся» и проверка в фоне; затем сроки из кэша (дни — floor, как
// в старом), недоступный хост — «Не удалось проверить»; повтор — не чаще раза в час.
func TestCertCheckerView(t *testing.T) {
	now := time.Now()
	var calls atomic.Int64
	c := &certChecker{log: slog.New(slog.NewTextHandler(io.Discard, nil)), expiresAt: func(_ context.Context, host string) (time.Time, error) {
		calls.Add(1)
		switch host {
		case "ok.test":
			return now.Add(20*24*time.Hour + time.Hour), nil
		case "expired.test":
			return now.Add(-36 * time.Hour), nil
		}
		return time.Time{}, errors.New("dial")
	}}
	hosts := []string{"ok.test", "expired.test", "down.test"}

	first := c.view(hosts, now)
	for _, r := range first {
		if r.Checked || r.Days != nil {
			t.Fatalf("before the first check: %+v", r)
		}
	}
	c.wait()
	if calls.Load() != 3 {
		t.Fatalf("dials %d", calls.Load())
	}
	rows := c.view(hosts, now.Add(time.Minute))
	if !rows[0].Checked || rows[0].Days == nil || *rows[0].Days != 20 {
		t.Errorf("ok.test: %+v", rows[0])
	}
	if rows[1].Days == nil || *rows[1].Days != -2 {
		t.Errorf("expired.test: %+v", rows[1])
	}
	if !rows[2].Checked || rows[2].Days != nil {
		t.Errorf("down.test: %+v", rows[2])
	}
	c.wait()
	if calls.Load() != 3 {
		t.Fatalf("refreshed within the hour: %d dials", calls.Load())
	}
	c.view(hosts, time.Now().Add(certRefreshEvery))
	c.wait()
	if calls.Load() != 6 {
		t.Fatalf("hourly refresh: %d dials", calls.Load())
	}
	// Нет доменов — нечего проверять.
	if rows := (&certChecker{}).view(nil, now); len(rows) != 0 {
		t.Fatal(rows)
	}
}

// Срок читается из сертификата по TLS (SNI, без проверки цепочки — самоподписанный тоже).
func TestDialCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	got, err := dialCertificateAddr(context.Background(), srv.Listener.Addr().String(), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if want := srv.Certificate().NotAfter; !got.Equal(want) {
		t.Errorf("NotAfter %v, want %v", got, want)
	}
	if _, err := dialCertificateAddr(context.Background(), "127.0.0.1:1", "x"); err == nil {
		t.Error("closed port: no error")
	}
}

func TestFloorDiv(t *testing.T) {
	for _, c := range [][3]int64{{86400 * 2, 86400, 2}, {86399, 86400, 0}, {-1, 86400, -1}, {-86400, 86400, -1}, {-86401, 86400, -2}} {
		if got := floorDiv(c[0], c[1]); got != c[2] {
			t.Errorf("floorDiv(%d, %d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
}
