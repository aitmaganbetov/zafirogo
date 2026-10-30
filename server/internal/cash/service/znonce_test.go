package service

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// Q-015: nonce одноразовый (Take забирает), новый Put заменяет прежний, истёкший не действует,
// ключ — клуб + смена + сотрудник (nonce другого сотрудника не подходит).
func TestMemoryZNonces(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	m := &MemoryZNonces{TTL: time.Minute, Now: func() time.Time { return now }}
	k := ZNonceKey{ClubID: 1, ShiftID: 2, UserID: 3}
	h1, h2 := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)

	if got, _ := m.Take(ctx, k); got != nil {
		t.Fatalf("empty store: %x", got)
	}
	_ = m.Put(ctx, k, h1)
	_ = m.Put(ctx, k, h2)
	if got, _ := m.Take(ctx, ZNonceKey{ClubID: 1, ShiftID: 2, UserID: 4}); got != nil {
		t.Fatal("other user must not see the nonce")
	}
	if got, _ := m.Take(ctx, k); !bytes.Equal(got, h2) {
		t.Fatalf("take = %x, want latest", got)
	}
	if got, _ := m.Take(ctx, k); got != nil {
		t.Fatal("nonce must be one-time")
	}
	_ = m.Put(ctx, k, h1)
	now = now.Add(time.Minute)
	if got, _ := m.Take(ctx, k); got != nil {
		t.Fatal("expired nonce must not be returned")
	}
}
