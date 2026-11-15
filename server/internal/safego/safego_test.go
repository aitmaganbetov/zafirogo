package safego

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// BC-04: паникующий воркер не роняет процесс — паника в журнале со стеком, задача перезапущена с растущей
// задержкой и продолжает работать.
func TestRunRestartsPanickingWorker(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	before := Panics()
	var runs atomic.Int32
	var starts []time.Time
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Run(ctx, log, "burn-packages", Backoff{Min: 20 * time.Millisecond, Max: 50 * time.Millisecond}, func(ctx context.Context) error {
		starts = append(starts, time.Now())
		if runs.Add(1) <= 3 {
			clubs := make([]int, 1)
			_ = clubs[int(runs.Load())] // паника на данных одного клуба (индекс за пределами)
		}
		cancel() // четвёртый запуск — задача работает до остановки
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatalf("Run = %v, want nil after a clean stop", err)
	}
	if runs.Load() != 4 || Panics()-before != 3 {
		t.Fatalf("runs %d, panics %d; want 4 and 3", runs.Load(), Panics()-before)
	}
	// Задержки: 20 мс, 40 мс, 50 мс (потолок).
	for i, want := range []time.Duration{20 * time.Millisecond, 40 * time.Millisecond, 50 * time.Millisecond} {
		if d := starts[i+1].Sub(starts[i]); d < want {
			t.Errorf("restart %d after %v, want at least %v", i+1, d, want)
		}
	}
	out := buf.String()
	if strings.Count(out, "worker panic: restarting") != 3 || !strings.Contains(out, "worker=burn-packages") ||
		!strings.Contains(out, "index out of range") || !strings.Contains(out, "safego_test.go") {
		t.Fatalf("panic log: %s", out)
	}
}

// Обычная ошибка воркера возвращается как раньше (остановка приложения решает app.Run); остановка во время
// ожидания перезапуска — без ошибки.
func TestRunReturnsErrorsAndStops(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	boom := errors.New("boom")
	if err := Run(context.Background(), log, "w", Backoff{}, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, log, "w", Backoff{Min: time.Hour}, func(context.Context) error { panic("always") })
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stop while waiting to restart: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// Разовая фоновая задача: паника — в журнал, процесс жив; Wait ждёт задачи при остановке.
func TestGroup(t *testing.T) {
	var buf bytes.Buffer
	g := NewGroup(slog.New(slog.NewTextHandler(&buf, nil)))
	release := make(chan struct{})
	var finished atomic.Bool
	g.Go("club-config", func() { panic("nil hooks") })
	g.Go("broadcast", func() { <-release; finished.Store(true) })
	short, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if g.Wait(short) {
		t.Fatal("Wait returned before the running task finished")
	}
	close(release)
	if !g.Wait(context.Background()) || !finished.Load() {
		t.Fatal("Wait must wait for the tasks")
	}
	if out := buf.String(); !strings.Contains(out, "background task panic") || !strings.Contains(out, "job=club-config") ||
		!strings.Contains(out, "nil hooks") {
		t.Fatalf("panic log: %s", out)
	}
	// nil-группа — та же защита без учёта.
	var none *Group
	done := make(chan struct{})
	none.Go("x", func() { defer close(done); panic("x") })
	<-done
	if !none.Wait(context.Background()) {
		t.Fatal("nil group Wait")
	}
}
