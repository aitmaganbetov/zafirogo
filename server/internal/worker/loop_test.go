package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/safego"
)

// tickLog — моменты тиков от начала теста (виртуальное время synctest).
type tickLog struct {
	start time.Time
	at    []time.Duration
}

func (l *tickLog) mark() { l.at = append(l.at, time.Since(l.start)) }

func runLoop(t *testing.T, l loop, total time.Duration, tick func(ctx context.Context, n int) error) ([]time.Duration, string) {
	t.Helper()
	var (
		marks []time.Duration
		buf   bytes.Buffer
	)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		log := &tickLog{start: time.Now()}
		l.log = slog.New(slog.NewTextHandler(&buf, nil))
		done := make(chan error, 1)
		go func() {
			done <- l.run(ctx, func(ctx context.Context) error {
				log.mark()
				return tick(ctx, len(log.at))
			})
		}()
		time.Sleep(total)
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run после отмены ctx = %v, want nil", err)
		}
		marks = log.at
	})
	return marks, buf.String()
}

func wantMarks(t *testing.T, got []time.Duration, want ...time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("тики %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("тики %v, want %v", got, want)
		}
	}
}

// BC-08: общий цикл воркеров сохраняет поведение прежних шести копий Run.
func TestLoop(t *testing.T) {
	boom := errors.New("boom")
	t.Run("первый тик сразу, дальше период после окончания тика", func(t *testing.T) {
		// Суточные и часовые задачи (сгорание пакетов, журналы, подписки, перешифрование): тик длится 10 с.
		got, _ := runLoop(t, loop{job: "j", failed: "tick failed", interval: time.Hour}, 3*time.Hour,
			func(context.Context, int) error { time.Sleep(10 * time.Second); return nil })
		wantMarks(t, got, 0, time.Hour+10*time.Second, 2*time.Hour+20*time.Second)
	})
	t.Run("ошибка тика: журнал и повтор через retry", func(t *testing.T) {
		// burn_packages: после ошибки — через 5 мин, после успеха — снова обычный период.
		got, out := runLoop(t, loop{job: "time-packages:burn-expired", failed: "burn expired packages tick failed",
			interval: 24 * time.Hour, retry: 5 * time.Minute}, 25*time.Hour,
			func(_ context.Context, n int) error {
				if n <= 2 {
					return boom
				}
				return nil
			})
		wantMarks(t, got, 0, 5*time.Minute, 10*time.Minute, 24*time.Hour+10*time.Minute)
		if strings.Count(out, `msg="burn expired packages tick failed"`) != 2 || !strings.Contains(out, "job=time-packages:burn-expired") ||
			!strings.Contains(out, "err=boom") {
			t.Fatalf("журнал: %s", out)
		}
	})
	t.Run("retry не длиннее периода", func(t *testing.T) {
		got, _ := runLoop(t, loop{job: "j", failed: "f", interval: time.Minute, retry: 5 * time.Minute}, 150*time.Second,
			func(context.Context, int) error { return boom })
		wantMarks(t, got, 0, time.Minute, 2*time.Minute)
	})
	t.Run("без retry ошибка не меняет период", func(t *testing.T) {
		got, out := runLoop(t, loop{job: "logs:retention", failed: "log retention tick failed", interval: time.Hour}, 150*time.Minute,
			func(context.Context, int) error { return boom })
		wantMarks(t, got, 0, time.Hour, 2*time.Hour)
		if strings.Count(out, "log retention tick failed") != 3 {
			t.Fatalf("журнал: %s", out)
		}
	})
	t.Run("fixedRate: сетка от начала тика", func(t *testing.T) {
		// Истечение сессий и автобиллинг: тик длится 20 с, но следующий — ровно через минуту от старта.
		got, out := runLoop(t, loop{job: "club-computers:expire-sessions", failed: "expire sessions tick failed",
			interval: time.Minute, fixedRate: true}, 150*time.Second,
			func(_ context.Context, n int) error {
				time.Sleep(20 * time.Second)
				if n == 2 {
					return boom
				}
				return nil
			})
		wantMarks(t, got, 0, time.Minute, 2*time.Minute)
		if strings.Count(out, "expire sessions tick failed") != 1 {
			t.Fatalf("журнал: %s", out)
		}
	})
	t.Run("ошибка из-за отмены ctx в журнал не идёт", func(t *testing.T) {
		_, out := runLoop(t, loop{job: "j", failed: "tick failed", interval: time.Hour}, 30*time.Minute,
			func(ctx context.Context, _ int) error { <-ctx.Done(); return ctx.Err() })
		if out != "" {
			t.Fatalf("журнал: %s", out)
		}
	})
	t.Run("паника тика: safego.Run перезапускает цикл, первый тик снова сразу", func(t *testing.T) {
		// Так воркеры запускает app.Run (BC-04): цикл сам панику не глотает.
		var marks []time.Duration
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			start := time.Now()
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			l := loop{job: "j", failed: "tick failed", interval: time.Hour, log: log}
			done := make(chan error, 1)
			go func() {
				done <- safego.Run(ctx, log, "j", safego.Backoff{Min: time.Second}, func(ctx context.Context) error {
					return l.run(ctx, func(context.Context) error {
						marks = append(marks, time.Since(start))
						if len(marks) == 1 {
							panic("nil в данных клуба")
						}
						return nil
					})
				})
			}()
			time.Sleep(90 * time.Minute)
			cancel()
			if err := <-done; err != nil {
				t.Errorf("err = %v", err)
			}
			if !strings.Contains(buf.String(), "worker panic: restarting") {
				t.Errorf("журнал: %s", buf.String())
			}
		})
		wantMarks(t, marks, 0, time.Second, time.Hour+time.Second)
	})
}

func TestIntervalOr(t *testing.T) {
	if intervalOr(0, time.Minute) != time.Minute || intervalOr(-1, time.Minute) != time.Minute || intervalOr(time.Second, time.Minute) != time.Second {
		t.Fatal("intervalOr")
	}
}
