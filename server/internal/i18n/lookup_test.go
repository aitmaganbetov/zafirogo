package i18n

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// countingBeginner — считает открытия транзакций; сама транзакция не нужна (ошибка).
type countingBeginner struct{ n int }

func (b *countingBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	b.n++
	return nil, errors.New("no db")
}

// S6-O4-3: перевод в пределах lookupVersionTTL — из памяти, без транзакции платформенного пула.
func TestLookupUsesMemoryWithinTTL(t *testing.T) {
	ctx := context.Background()
	b := &countingBeginner{}
	c := NewCatalog(b)
	c.enabled = []Language{{Code: "kk", Name: "Қазақша"}}
	c.raw["kk"] = map[string]string{"Привет": "Сәлем"}
	c.checkedAt = time.Now()

	if v, ok := c.Lookup(ctx, "kk", "Привет"); !ok || v != "Сәлем" {
		t.Fatalf("Lookup = %q, %v; want Сәлем", v, ok)
	}
	if _, ok := c.Lookup(ctx, "kk", "Нет такого"); ok {
		t.Fatal("unknown key found")
	}
	if b.n != 0 {
		t.Fatalf("BeginTx called %d times within TTL, want 0", b.n)
	}

	// Язык ещё не загружен — в базу.
	c.Lookup(ctx, "en", "Привет")
	if b.n != 1 {
		t.Fatalf("BeginTx = %d for an unloaded locale, want 1", b.n)
	}

	// Версия устарела — сверка с базой.
	c.checkedAt = time.Now().Add(-lookupVersionTTL - time.Second)
	c.Lookup(ctx, "kk", "Привет")
	if b.n != 2 {
		t.Fatalf("BeginTx = %d after TTL, want 2", b.n)
	}
}
