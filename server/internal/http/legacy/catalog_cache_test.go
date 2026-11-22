package legacy

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// Общий расчёт каталога не зависит от запроса, который его начал: обрыв первого клиента не даёт ошибку ждущим
// (ревью 22.11.2026).
func TestCatalogCacheFirstRequestCanceled(t *testing.T) {
	var c catalogCache
	started, release := make(chan struct{}), make(chan struct{})
	build := func(ctx context.Context) ([]byte, bool, error) {
		close(started)
		<-release
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		return []byte("ok"), true, nil
	}
	first, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.get(first, "k", build)
		firstDone <- err
	}()
	<-started
	type result struct {
		body []byte
		err  error
	}
	second := make(chan result, 1)
	go func() {
		body, err := c.get(context.Background(), "k", func(context.Context) ([]byte, bool, error) {
			t.Error("второй запрос не должен считать сам")
			return nil, false, nil
		})
		second <- result{body, err}
	}()
	// Второй запрос должен успеть встать в ожидание того же расчёта.
	waitFlightWaiters(t, &c, "k")
	cancelFirst()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("первый запрос: err = %v, нужен context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("первый запрос не вернулся после отмены своего контекста")
	}
	close(release)
	select {
	case r := <-second:
		if r.err != nil || string(r.body) != "ok" {
			t.Fatalf("ждущий запрос: body = %q, err = %v", r.body, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ждущий запрос не получил ответ")
	}
	// Результат запомнен: следующий запрос не считает.
	body, err := c.get(context.Background(), "k", func(context.Context) ([]byte, bool, error) {
		return nil, false, errors.New("расчёт не нужен")
	})
	if err != nil || string(body) != "ok" {
		t.Fatalf("кэш: body = %q, err = %v", body, err)
	}
}

// waitFlightWaiters — ждёт, пока расчёт по ключу появится и горутина теста успеет в него встать.
func waitFlightWaiters(t *testing.T, c *catalogCache, key string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		_, ok := c.flights[key]
		c.mu.Unlock()
		if ok {
			time.Sleep(20 * time.Millisecond)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("расчёт не начался")
}

// Паника в расчёте не оставляет его «вечным»: ждущие получают ошибку, следующий запрос считает заново.
func TestCatalogCacheBuildPanic(t *testing.T) {
	var c catalogCache
	_, err := c.get(context.Background(), "k", func(context.Context) ([]byte, bool, error) { panic("boom") })
	if err == nil {
		t.Fatal("паника расчёта должна вернуться ошибкой")
	}
	c.mu.Lock()
	left := len(c.flights)
	c.mu.Unlock()
	if left != 0 {
		t.Fatalf("после паники осталось расчётов: %d", left)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		body, err := c.get(context.Background(), "k", func(context.Context) ([]byte, bool, error) { return []byte("ok"), true, nil })
		if err != nil || string(body) != "ok" {
			t.Errorf("после паники: body = %q, err = %v", body, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("запрос после паники завис")
	}
}

// Расчёт ограничен своим таймаутом, а не контекстом запроса.
func TestCatalogCacheBuildHasDeadline(t *testing.T) {
	var c catalogCache
	_, err := c.get(context.Background(), "k", func(ctx context.Context) ([]byte, bool, error) {
		if _, ok := ctx.Deadline(); !ok {
			return nil, false, errors.New("нет срока")
		}
		return nil, false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// «Клуба нет» запоминается на короткое время; число таких записей ограничено.
func TestCatalogCacheRemembersMissingClub(t *testing.T) {
	var c catalogCache
	var builds atomic.Int64
	miss := func(context.Context) ([]byte, bool, error) {
		builds.Add(1)
		return nil, true, errCatalogNoClub
	}
	for range 3 {
		if _, err := c.get(context.Background(), "club|7|ru", miss); !errors.Is(err, errCatalogNoClub) {
			t.Fatalf("err = %v", err)
		}
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("расчётов: %d, нужен 1", n)
	}
	// Срок вышел — считаем заново.
	c.mu.Lock()
	c.missing["club|7|ru"] = time.Now().Add(-time.Second)
	c.mu.Unlock()
	_, _ = c.get(context.Background(), "club|7|ru", miss)
	if n := builds.Load(); n != 2 {
		t.Fatalf("после срока расчётов: %d, нужно 2", n)
	}
	// Перебор номеров не раздувает память и не вытесняет готовые ответы.
	if _, err := c.get(context.Background(), "club|1|ru", func(context.Context) ([]byte, bool, error) { return []byte("x"), true, nil }); err != nil {
		t.Fatal(err)
	}
	for i := range catalogMissMax + 50 {
		_, _ = c.get(context.Background(), "club|"+strconv.Itoa(1000+i)+"|ru", miss)
	}
	c.mu.Lock()
	missing, _, kept := len(c.missing), len(c.entries), c.entries["club|1|ru"].body
	c.mu.Unlock()
	if missing > catalogMissMax {
		t.Fatalf("записей «клуба нет»: %d > %d", missing, catalogMissMax)
	}
	if string(kept) != "x" {
		t.Fatal("готовый ответ вытеснен записями «клуба нет»")
	}
	// Другие ошибки не запоминаются.
	builds.Store(0)
	fail := func(context.Context) ([]byte, bool, error) {
		builds.Add(1)
		return nil, true, errors.New("db down")
	}
	_, _ = c.get(context.Background(), "club|9|ru", fail)
	_, _ = c.get(context.Background(), "club|9|ru", fail)
	if n := builds.Load(); n != 2 {
		t.Fatalf("ошибка базы запомнена: расчётов %d", n)
	}
}
