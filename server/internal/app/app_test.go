package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
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
