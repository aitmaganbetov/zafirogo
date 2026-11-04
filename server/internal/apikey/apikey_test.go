package apikey

import (
	"context"
	"testing"
	"time"
)

// TestHashCompatibleWithLaravel — hash('sha256', $plain) старого ApiKeyManager (значение снято PHP 8.4): перенесённые
// ключи проверяются без перевыпуска.
func TestHashCompatibleWithLaravel(t *testing.T) {
	const key = "zfk_ab12cd34_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCD"
	const php = "f9a6e459d8e4a177cedea0ad53d61f8511df2263390986e9b35b730fb5b9bb17"
	if Hash(key) != php || !HashEqual(php, key) || HashEqual(php, key+"x") {
		t.Fatal("hash")
	}
	if KeyPrefix(key) != "zfk_ab12cd34" {
		t.Fatal(KeyPrefix(key))
	}
	for _, bad := range []string{"hello", "1|abcdefghijklmnopqrstuvwxyz", key + "x", "zfk_AB12cd34_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCD"} {
		if KeyPrefix(bad) != "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLimits(t *testing.T) {
	l := Limits{}.WithDefaults()
	if l.KeyDefault != 120 || l.KeyMax != 1000 || l.Club != 600 || l.FailedAuthPerMinute != 20 || l.MaxRequestBytes != 256*1024 ||
		l.MaxBulkItems != 50 || l.IdempotencyRetention != 24*time.Hour {
		t.Fatalf("%+v", l)
	}
	big := int32(5000)
	if l.KeyLimit(nil) != 120 || l.KeyLimit(&big) != 1000 {
		t.Fatal("key limit")
	}
}

func TestMemoryCounter(t *testing.T) {
	now := time.Unix(1000, 0)
	c := &MemoryCounter{Now: func() time.Time { return now }}
	ctx := context.Background()
	if n, _ := c.Incr(ctx, "a", time.Minute); n != 1 {
		t.Fatal(n)
	}
	if n, _ := c.Incr(ctx, "a", time.Minute); n != 2 {
		t.Fatal(n)
	}
	if ok, _ := c.SetNX(ctx, "f", time.Minute); !ok {
		t.Fatal("setnx")
	}
	if ok, _ := c.SetNX(ctx, "f", time.Minute); ok {
		t.Fatal("setnx twice")
	}
	now = now.Add(61 * time.Second)
	if n, _ := c.Get(ctx, "a"); n != 0 {
		t.Fatal("expired", n)
	}
}
