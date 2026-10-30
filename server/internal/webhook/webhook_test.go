package webhook

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestSignMatchesPHP(t *testing.T) {
	// php -r 'echo hash_hmac("sha256", "1700000000.{\"id\":\"x\",\"data\":{\"a\":\"Привет/1\"}}", "s3cr3t");'
	body := []byte(`{"id":"x","data":{"a":"Привет/1"}}`)
	want := "v1=d77824ec48bb41beb6b7f0592e0c35d72da67ad6d94d02605f0e0a444451d286"
	if got := Sign("s3cr3t", 1700000000, body); got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
	now := time.Unix(1700000100, 0)
	if !Verify("s3cr3t", "1700000000", want, body, now, DefaultSignatureTTL) {
		t.Fatal("Verify rejected a valid signature")
	}
	if Verify("s3cr3t", "1700000000", want, body, now.Add(10*time.Minute), DefaultSignatureTTL) {
		t.Fatal("Verify accepted a stale timestamp")
	}
	if Verify("other", "1700000000", want, body, now, DefaultSignatureTTL) {
		t.Fatal("Verify accepted a wrong secret")
	}
}

func TestGuard(t *testing.T) {
	resolve := func(m map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
		return func(_ context.Context, host string) ([]netip.Addr, error) {
			var out []netip.Addr
			for _, s := range m[host] {
				out = append(out, netip.MustParseAddr(s))
			}
			if len(out) == 0 {
				return nil, errors.New("no such host")
			}
			return out, nil
		}
	}
	g := Guard{RequireHTTPS: true, Resolve: resolve(map[string][]string{
		"hooks.example.com": {"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"},
		"rebind.example":    {"93.184.216.34", "10.0.0.5"},
		"local.example":     {"127.0.0.1"},
	})}
	cases := []struct {
		url, reason string
		ip          string
		port        int
	}{
		{"https://hooks.example.com/x", "", "93.184.216.34", 443},
		{"https://HOOKS.example.com:8443/x", "", "93.184.216.34", 8443},
		{"http://hooks.example.com/x", ReasonHTTPSRequired, "", 0},
		{"ftp://hooks.example.com/x", ReasonInvalidURL, "", 0},
		{"https://user:pw@hooks.example.com/x", ReasonInvalidURL, "", 0},
		{"https:///x", ReasonInvalidURL, "", 0},
		{"https://hooks.example.com:22/x", ReasonPortNotAllowed, "", 0},
		{"https://unknown.example/x", ReasonNotResolved, "", 0},
		{"https://rebind.example/x", ReasonPrivateAddress, "", 0},
		{"https://local.example/x", ReasonPrivateAddress, "", 0},
		{"https://127.0.0.1/x", ReasonPrivateAddress, "", 0},
		{"https://[::1]/x", ReasonPrivateAddress, "", 0},
		{"https://169.254.169.254/latest", ReasonPrivateAddress, "", 0},
		{"https://192.168.1.1/x", ReasonPrivateAddress, "", 0},
		{"https://100.64.0.1/x", ReasonPrivateAddress, "", 0},
		{"https://[::ffff:10.0.0.1]/x", ReasonPrivateAddress, "", 0},
		{"https://8.8.8.8/x", "", "8.8.8.8", 443},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			target, reason := g.Check(context.Background(), c.url)
			if reason != c.reason {
				t.Fatalf("reason = %q, want %q", reason, c.reason)
			}
			if c.reason == "" && (target.IP.String() != c.ip || target.Port != c.port) {
				t.Fatalf("target = %+v, want %s:%d", target, c.ip, c.port)
			}
		})
	}
	// Частные сети разрешены настройкой.
	open := Guard{AllowPrivate: true, Ports: []int{}}
	if _, reason := open.Check(context.Background(), "http://127.0.0.1:12345/x"); reason != "" {
		t.Fatalf("allow private: reason %q", reason)
	}
}

func testTarget(t *testing.T, srvURL string) Target {
	t.Helper()
	u, _ := url.Parse(srvURL)
	port, _ := strconv.Atoi(u.Port())
	return Target{Host: u.Hostname(), Port: port, IP: netip.MustParseAddr(u.Hostname())}
}

func TestSenderHeadersAndStatus(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	s := sender{Timeout: 2 * time.Second, ConnectTimeout: time.Second}
	req := request{URL: srv.URL + "/hook", Target: testTarget(t, srv.URL), Secret: "k", Event: "sale.completed",
		Delivery: "d-1", Body: []byte(`{"a":1}`), Timestamp: 1700000000}

	code, errText := s.send(context.Background(), req)
	if code != 200 || errText != "" {
		t.Fatalf("send = %d %q", code, errText)
	}
	if gotHeaders.Get(HeaderSignature) != Sign("k", 1700000000, []byte(`{"a":1}`)) ||
		gotHeaders.Get(HeaderEvent) != "sale.completed" || gotHeaders.Get(HeaderDelivery) != "d-1" ||
		gotHeaders.Get(HeaderTimestamp) != "1700000000" || gotHeaders.Get("User-Agent") != UserAgent ||
		gotHeaders.Get("Content-Type") != "application/json" || string(gotBody) != `{"a":1}` {
		t.Fatalf("headers/body: %v %s", gotHeaders, gotBody)
	}

	status = http.StatusInternalServerError
	if code, errText := s.send(context.Background(), req); code != 500 || errText != "HTTP 500" {
		t.Fatalf("500: %d %q", code, errText)
	}

	// Редирект не выполняется: 302 — неуспех.
	req.URL = srv.URL + "/redirect"
	if code, errText := s.send(context.Background(), req); code != 302 || errText != "HTTP 302" {
		t.Fatalf("redirect: %d %q", code, errText)
	}

	// Соединение — только на пришпиленный адрес: имя хоста в URL не резолвится.
	req.URL = "http://does-not-resolve.invalid:" + strconv.Itoa(req.Target.Port) + "/hook"
	status = http.StatusOK
	if code, errText := s.send(context.Background(), req); code != 200 || errText != "" {
		t.Fatalf("pinned: %d %q", code, errText)
	}

	srv.Close()
	if code, errText := s.send(context.Background(), req); code != 0 || errText != "connection failed: ConnectionException" {
		t.Fatalf("closed: %d %q", code, errText)
	}
}

func TestDeliveryIDDeterministic(t *testing.T) {
	a := DeliveryID("11111111-1111-1111-1111-111111111111", 5)
	if a != DeliveryID("11111111-1111-1111-1111-111111111111", 5) || a == DeliveryID("11111111-1111-1111-1111-111111111111", 6) {
		t.Fatal("DeliveryID must depend only on event and endpoint")
	}
}
