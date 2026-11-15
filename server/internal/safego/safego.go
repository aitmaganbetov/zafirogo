// Package safego — фоновые горутины, которые не роняют процесс (аудит BC-04).
//
// Паника в горутине без recover завершает весь процесс: один инстанс падает для всех клубов и после перезапуска
// падает снова на тех же данных. Здесь:
//   - Run — постоянная задача (воркер): паника → запись в журнал со стеком и перезапуск задачи с растущей
//     задержкой; обычная ошибка задачи возвращается как раньше;
//   - Group — разовые фоновые задачи (рассылка после ответа панели и т.п.): паника → запись в журнал; Wait ждёт
//     их при остановке.
//
// Метрика — worker_errors_total{job}: обработчик журнала observability считает записи warn/error с атрибутом
// job/worker, отдельного счётчика не нужно; Panics — счётчик для тестов и /metrics процесса.
package safego

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// Умолчания перезапуска задачи после паники.
const (
	DefaultMinBackoff = time.Second
	DefaultMaxBackoff = time.Minute
	// stableAfter — задача проработала столько без паники: задержка следующего перезапуска снова минимальная.
	stableAfter = 5 * time.Minute
)

var panics atomic.Uint64

// Panics — сколько паник перехвачено с запуска процесса.
func Panics() uint64 { return panics.Load() }

// PanicError — паника фоновой задачи как ошибка.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("panic: %v", e.Value) }

// Call выполняет fn; паника возвращается как *PanicError (со стеком) и считается.
func Call(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			panics.Add(1)
			err = &PanicError{Value: r, Stack: debug.Stack()}
		}
	}()
	return fn()
}

// Backoff — задержки перезапуска (нули — умолчания).
type Backoff struct {
	Min, Max time.Duration
}

// Run выполняет постоянную задачу fn до её завершения. Паника — в журнал (error, атрибут worker — имя задачи) и
// перезапуск fn через задержку (Min, затем вдвое до Max; после stableAfter работы без паники — снова Min). Обычная
// ошибка и штатное завершение возвращаются сразу. Отмена ctx во время ожидания — nil.
func Run(ctx context.Context, log *slog.Logger, name string, b Backoff, fn func(ctx context.Context) error) error {
	if b.Min <= 0 {
		b.Min = DefaultMinBackoff
	}
	if b.Max < b.Min {
		b.Max = max(DefaultMaxBackoff, b.Min)
	}
	delay := b.Min
	for {
		started := time.Now()
		err := Call(func() error { return fn(ctx) })
		var pe *PanicError
		if !errors.As(err, &pe) {
			return err
		}
		if time.Since(started) >= stableAfter {
			delay = b.Min
		}
		log.ErrorContext(ctx, "worker panic: restarting", slog.String("worker", name), slog.Any("panic", pe.Value),
			slog.Duration("restart_in", delay), slog.String("stack", string(pe.Stack)))
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		delay = min(delay*2, b.Max)
	}
}

// Group — разовые фоновые горутины с recover и учётом при остановке.
type Group struct {
	log *slog.Logger
	wg  sync.WaitGroup
}

// NewGroup — группа фоновых задач; log nil — slog.Default().
func NewGroup(log *slog.Logger) *Group {
	if log == nil {
		log = slog.Default()
	}
	return &Group{log: log}
}

// Go запускает fn в горутине: паника не роняет процесс (журнал error с атрибутом job — имя задачи). g == nil —
// та же защита без учёта при остановке.
func (g *Group) Go(name string, fn func()) {
	log := slog.Default()
	if g != nil {
		log = g.log
		g.wg.Add(1)
	}
	go func() {
		if g != nil {
			defer g.wg.Done()
		}
		var pe *PanicError
		if err := Call(func() error { fn(); return nil }); errors.As(err, &pe) {
			log.Error("background task panic", slog.String("job", name), slog.Any("panic", pe.Value),
				slog.String("stack", string(pe.Stack)))
		}
	}()
}

// Wait ждёт завершения запущенных задач не дольше ctx; false — не дождались.
func (g *Group) Wait(ctx context.Context) bool {
	if g == nil {
		return true
	}
	done := make(chan struct{})
	go func() { g.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
