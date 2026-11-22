package guestapi

import (
	"context"
	"strconv"
	"sync"

	"github.com/google/uuid"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Морф-классы предметов журнала — как subject_type перенесённых строк Laravel (getMorphClass()).
const (
	subjectUser     = `App\Models\User`
	subjectComputer = `App\Models\ClubComputer`
)

// record — запись журнала действий в транзакции q (ActivityLogger::record()). Без IP запроса (тесты сервиса
// без gRPC) запись пропускается: журнал требует ip_address.
func (s *Server) record(ctx context.Context, q *store.Queries, e activity.Entry) error {
	if _, ok := activity.RequestMetaFromContext(ctx); !ok {
		return nil
	}
	_, err := activity.Record(ctx, q, e)
	return err
}

// waiters — ожидания QR-входа на этом инстансе (WatchQrLogin): подтверждение будит их сразу.
type waiters struct {
	mu sync.Mutex
	m  map[uuid.UUID]map[chan struct{}]struct{}
}

func (w *waiters) add(id uuid.UUID) chan struct{} {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.m == nil {
		w.m = map[uuid.UUID]map[chan struct{}]struct{}{}
	}
	if w.m[id] == nil {
		w.m[id] = map[chan struct{}]struct{}{}
	}
	w.m[id][ch] = struct{}{}
	return ch
}

func (w *waiters) remove(id uuid.UUID, ch chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.m[id], ch)
	if len(w.m[id]) == 0 {
		delete(w.m, id)
	}
}

func (w *waiters) wake(id uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.m[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
