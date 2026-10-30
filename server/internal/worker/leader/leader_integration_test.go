//go:build integration

package leader_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

func TestSecondInstanceDoesNotTakeLock(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)

	first, err := leader.TryAcquire(ctx, d.Platform, "test-job")
	if err != nil || first == nil {
		t.Fatalf("first: %v %v", first, err)
	}
	// Второй экземпляр (отдельное соединение) лок не берёт.
	second, err := leader.TryAcquire(ctx, d.Platform, "test-job")
	if err != nil || second != nil {
		t.Fatalf("second must not get the lock: %v %v", second, err)
	}
	ran, err := leader.RunOnce(ctx, d.Platform, "test-job", func(context.Context, *pgxpool.Conn) error {
		t.Fatal("must not run")
		return nil
	})
	if ran || err != nil {
		t.Fatalf("RunOnce while locked: %v %v", ran, err)
	}
	// Другая задача — свой лок.
	if other, err := leader.TryAcquire(ctx, d.Platform, "other-job"); err != nil || other == nil {
		t.Fatalf("other job: %v %v", other, err)
	} else {
		other.Release()
	}

	first.Release()
	ran, err = leader.RunOnce(ctx, d.Platform, "test-job", func(context.Context, *pgxpool.Conn) error { return errors.New("tick") })
	if !ran || err == nil || err.Error() != "tick" {
		t.Fatalf("RunOnce after release: %v %v", ran, err)
	}
	// RunOnce снял лок и после ошибки.
	if again, err := leader.TryAcquire(ctx, d.Platform, "test-job"); err != nil || again == nil {
		t.Fatalf("lock leaked: %v %v", again, err)
	} else {
		again.Release()
	}
}

// Ключ совместим с server/internal/worker/expire_sessions.go: пока лок держит leader, тик не выполняется.
func TestCompatibleWithExpireSessions(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	lock, err := leader.TryAcquire(ctx, d.Platform, worker.ExpireSessionsJob)
	if err != nil || lock == nil {
		t.Fatal(err)
	}
	job := &worker.ExpireSessions{Pool: d.Platform}
	if ran, _, err := job.Tick(ctx); ran || err != nil {
		t.Fatalf("expire sessions ran under a foreign lock: %v %v", ran, err)
	}
	lock.Release()
	if ran, _, err := job.Tick(ctx); !ran || err != nil {
		t.Fatalf("expire sessions after release: %v %v", ran, err)
	}
}

// Hold: лидер один; после его остановки лидерство переходит ко второму.
func TestHoldFailover(t *testing.T) {
	d := dbtest.New(t)
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	var leading atomic.Int32
	var who atomic.Int32
	work := func(id int32) func(ctx context.Context, l *leader.Lock) error {
		return func(ctx context.Context, l *leader.Lock) error {
			if leading.Add(1) != 1 {
				t.Error("two leaders at once")
			}
			who.Store(id)
			defer leading.Add(-1)
			<-ctx.Done()
			return nil
		}
	}
	opts := leader.HoldOptions{Retry: 50 * time.Millisecond}
	done1 := make(chan struct{})
	go func() { _ = leader.Hold(ctx1, d.Platform, "hold-job", opts, work(1)); close(done1) }()
	waitUntil(t, func() bool { return who.Load() == 1 })
	go func() { _ = leader.Hold(ctx2, d.Platform, "hold-job", opts, work(2)) }()
	time.Sleep(200 * time.Millisecond)
	if who.Load() != 1 {
		t.Fatal("second instance took leadership while the first holds it")
	}
	cancel1()
	<-done1
	waitUntil(t, func() bool { return who.Load() == 2 })
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
