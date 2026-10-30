//go:build integration

package service

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
)

// ValkeyZNonces: SET EX + GETDEL — одноразово, с TTL, последний Put побеждает.
func TestValkeyZNonces(t *testing.T) {
	ctx := context.Background()
	c, cleanup, err := authtest.StartValkey(ctx)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	v := NewValkeyZNonces(c, "test:z-nonce:"+strconv.FormatInt(time.Now().UnixNano(), 10)+":", 2*time.Second)
	k := ZNonceKey{ClubID: 1, ShiftID: 2, UserID: 3}
	h1, h2 := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	if got, err := v.Take(ctx, k); err != nil || got != nil {
		t.Fatalf("empty: %x %v", got, err)
	}
	if err := v.Put(ctx, k, h1); err != nil {
		t.Fatal(err)
	}
	if err := v.Put(ctx, k, h2); err != nil {
		t.Fatal(err)
	}
	if got, err := v.Take(ctx, k); err != nil || !bytes.Equal(got, h2) {
		t.Fatalf("take: %x %v", got, err)
	}
	if got, _ := v.Take(ctx, k); got != nil {
		t.Fatal("one-time")
	}
	_ = v.Put(ctx, k, h1)
	time.Sleep(2100 * time.Millisecond)
	if got, _ := v.Take(ctx, k); got != nil {
		t.Fatal("ttl")
	}
}
