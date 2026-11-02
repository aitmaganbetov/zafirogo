package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/config"
)

func TestSubstitute(t *testing.T) {
	got := substitute("ПК :computer_name (:computer) в зоне :zone", map[string]string{
		"computer": "7", "computer_name": "VIP-7", "zone": "VIP",
	})
	if want := "ПК VIP-7 (7) в зоне VIP"; got != want {
		t.Errorf("substitute = %q, want %q", got, want)
	}
	if got := substitute("Вход в систему: :user", nil); got != "Вход в систему: :user" {
		t.Errorf("no params = %q", got)
	}
}

// Режим без зависимостей (смоук бинарника в CI): только HTTP, healthz/readyz, остановка по ctx.
func TestStandalone(t *testing.T) {
	cfg, err := config.Load("", func() []string { return []string{"ZAFIRO_ENV=test", "ZAFIRO_HTTP_ADDR=127.0.0.1:0"} })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Standalone() {
		t.Fatal("expected standalone config")
	}
	ctx, cancel := context.WithCancel(context.Background())
	a, err := Build(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if a.GRPCAddr() != "" || a.Stream != nil {
		t.Fatal("standalone must not start gRPC or agent stream")
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	client := &http.Client{Timeout: 5 * time.Second}
	for _, path := range []string{"/healthz", "/readyz"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+a.HTTPAddr()+path, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d", path, resp.StatusCode)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
	if err := a.Run(context.Background()); err == nil {
		t.Error("second Run must fail")
	}
}

func TestWithoutWorkers(t *testing.T) {
	noop := func(context.Context) error { return nil }
	all := []namedWorker{{"monitoring-flush", noop}, {"monitoring-clubs", noop}, {"outbox-publisher", noop}}

	got, err := withoutWorkers(all, nil)
	if err != nil || len(got) != 3 {
		t.Fatalf("no disable: got %d workers, err %v", len(got), err)
	}
	got, err = withoutWorkers(all, []string{"monitoring-clubs", "monitoring-flush"})
	if err != nil || len(got) != 1 || got[0].name != "outbox-publisher" {
		t.Fatalf("disable monitoring: got %v, err %v", got, err)
	}
	if _, err = withoutWorkers(all, []string{"monitoring-flsh"}); err == nil {
		t.Fatal("unknown worker name must fail")
	}
}

// OBS-9: ошибка Build на любом шаге после открытия портов закрывает уже созданное и не паникует
// (замыкания closers не должны обращаться к именованному результату a — он уже nil).
func TestBuildErrorClosesResources(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	free := func(t *testing.T) string {
		t.Helper()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		return addr
	}
	cases := []struct {
		name    string
		env     func(httpAddr string) []string
		wantErr string
	}{
		{"http listen", func(string) []string {
			return []string{"ZAFIRO_HTTP_ADDR=" + busy.Addr().String()}
		}, "http listen"},
		{"metrics listen после http", func(h string) []string {
			return []string{"ZAFIRO_HTTP_ADDR=" + h, "ZAFIRO_OBSERVABILITY_METRICS_LISTEN=" + busy.Addr().String()}
		}, "observability.metrics_listen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			httpAddr := free(t)
			env := append([]string{"ZAFIRO_ENV=test"}, tc.env(httpAddr)...)
			cfg, err := config.Load("", func() []string { return env })
			if err != nil {
				t.Fatal(err)
			}
			a, err := Build(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err == nil || a != nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Build = %v, %v; want error %q", a, err, tc.wantErr)
			}
			// Порт HTTP, открытый до ошибки, освобождён.
			if httpAddr != busy.Addr().String() {
				l, err := net.Listen("tcp", httpAddr)
				if err != nil {
					t.Fatalf("http port not released: %v", err)
				}
				_ = l.Close()
			}
		})
	}
}
