package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
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

// BC-04: паника воркера не роняет процесс — HTTP продолжает отвечать, воркер перезапускается; при остановке
// приложение ждёт разовые фоновые задачи (рассылки после ответа панели).
func TestPanickingWorkerDoesNotStopApp(t *testing.T) {
	cfg, err := config.Load("", func() []string { return []string{"ZAFIRO_ENV=test", "ZAFIRO_HTTP_ADDR=127.0.0.1:0"} })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := Build(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	a.workers = append(a.workers, namedWorker{"panicking", func(ctx context.Context) error {
		if runs.Add(1) == 1 {
			panic("nil in the data of one club")
		}
		<-ctx.Done()
		return nil
	}})
	bgDone := make(chan struct{})
	var bgFinished atomic.Bool
	a.bg.Go("panicking-broadcast", func() { panic("broadcast") })
	a.bg.Go("slow-broadcast", func() { <-bgDone; bgFinished.Store(true) })
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for runs.Load() < 2 { // перезапущен после паники (задержка 1 с)
		if time.Now().After(deadline) {
			t.Fatalf("worker was not restarted after the panic: runs = %d", runs.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("app stopped after a worker panic: %v", err)
	default:
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+a.HTTPAddr()+"/healthz", nil)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz after a worker panic = %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Run returned before the background task finished: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(bgDone)
	select {
	case err := <-done:
		if err != nil || !bgFinished.Load() {
			t.Fatalf("Run: %v, background finished: %v", err, bgFinished.Load())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// Аудит OPS-16: медленный HTTP-клиент не съедает бюджет остановки воркеров. Запрос висит дольше бюджета; воркеру на
// остановку нужно 150 мс. Раньше HTTP Shutdown ждал до общего дедлайна, воркерам оставалось 0 и Run возвращал
// errShutdownTimeout («some workers did not stop»); теперь серверам — ⅔ бюджета, затем Close.
func TestSlowHTTPClientDoesNotStarveWorkers(t *testing.T) {
	cfg, err := config.Load("", func() []string {
		return []string{"ZAFIRO_ENV=test", "ZAFIRO_HTTP_ADDR=127.0.0.1:0", "ZAFIRO_SHUTDOWN_TIMEOUT=1200ms"}
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := Build(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{})
	a.httpSrv.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release // «медленный клиент»: запрос не завершается сам
	})
	var stopped atomic.Bool
	a.workers = append(a.workers, namedWorker{"slow-stop", func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(150 * time.Millisecond)
		stopped.Store(true)
		return nil
	}})
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+a.HTTPAddr()+"/slow", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow request did not reach the handler")
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if !stopped.Load() {
		t.Fatal("worker was not waited for")
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("shutdown took %v, budget 1.2s", took)
	}
}
