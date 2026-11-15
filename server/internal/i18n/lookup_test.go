package i18n

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// countingBeginner — считает открытия транзакций; сама транзакция не нужна (ошибка).
type countingBeginner struct{ n atomic.Int64 }

func (b *countingBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	b.n.Add(1)
	return nil, errors.New("no db")
}

func testSnapshot() *snapshot {
	return &snapshot{
		version: "1-1",
		enabled: []Language{{Code: "kk", Name: "Қазақша"}},
		raw:     map[string]map[string]string{"kk": {"Привет": "Сәлем"}},
		packs:   map[string]Pack{},
		clubs:   map[int64]string{7: "kk"},
	}
}

// PB-01 (раньше S6-O4-3): перевод и язык клуба на пути запроса — только из снимка в памяти: ни одной транзакции
// из вызывающей горутины, в том числе для выключенного языка и неизвестного клуба (отрицательный результат — тоже
// из памяти).
func TestLookupNeverOpensTransaction(t *testing.T) {
	ctx := context.Background()
	b := &countingBeginner{}
	c := NewCatalog(b)
	c.running.Store(true) // без фоновой сверки: проверяем только путь запроса
	c.snap.Store(testSnapshot())

	if v, ok := c.Lookup(ctx, "kk", "Привет"); !ok || v != "Сәлем" {
		t.Fatalf("Lookup = %q, %v; want Сәлем", v, ok)
	}
	if _, ok := c.Lookup(ctx, "kk", "Нет такого"); ok {
		t.Fatal("unknown key found")
	}
	if _, ok := c.Lookup(ctx, "en", "Привет"); ok { // язык не включён
		t.Fatal("disabled locale translated")
	}
	if got := c.ClubLocale(ctx, 7); got != "kk" {
		t.Fatalf("ClubLocale(7) = %q", got)
	}
	if got := c.ClubLocale(ctx, 8); got != SourceLocale {
		t.Fatalf("ClubLocale(8) = %q", got)
	}
	if got := c.Translate(ctx, "kk", "Привет", nil); got != "Сәлем" {
		t.Fatalf("Translate = %q", got)
	}
	if n := b.n.Load(); n != 0 {
		t.Fatalf("BeginTx called %d times on the request path, want 0", n)
	}
}

// Язык клуба, изменённый этим инстансом, виден сразу и переживает публикацию снимка, загруженного до правки.
func TestSetClubLocale(t *testing.T) {
	ctx := context.Background()
	c := NewCatalog(&countingBeginner{})
	c.running.Store(true)
	c.snap.Store(testSnapshot())

	c.SetClubLocale(8, "kk")
	c.SetClubLocale(7, "ru")
	if got := c.ClubLocale(ctx, 8); got != "kk" {
		t.Fatalf("ClubLocale(8) = %q after SetClubLocale", got)
	}
	if got := c.ClubLocale(ctx, 7); got != SourceLocale {
		t.Fatalf("ClubLocale(7) = %q after SetClubLocale(ru)", got)
	}
	c.mu.Lock()
	c.publish(testSnapshot()) // снимок из базы «до правки»
	c.mu.Unlock()
	if got := c.ClubLocale(ctx, 8); got != "kk" {
		t.Fatalf("ClubLocale(8) = %q after a stale snapshot", got)
	}
	if got := c.ClubLocale(ctx, 7); got != SourceLocale {
		t.Fatalf("ClubLocale(7) = %q after a stale snapshot", got)
	}
}

// База недоступна, снимка нет: перевод отвечает «нет» быстро и не открывает транзакцию на каждый вызов.
func TestLookupColdCatalogWithoutDatabase(t *testing.T) {
	ctx := context.Background()
	b := &countingBeginner{}
	c := NewCatalog(b)
	c.running.Store(true)
	start := time.Now()
	for range 200 {
		if _, ok := c.Lookup(ctx, "kk", "Привет"); ok {
			t.Fatal("translated without a snapshot")
		}
	}
	if d := time.Since(start); d > coldWait {
		t.Fatalf("200 lookups took %v", d)
	}
	if n := b.n.Load(); n > 3 {
		t.Fatalf("BeginTx called %d times for 200 lookups (want ≤ 3: backoff)", n)
	}
}

// Пакет i18next: записи «ключ = значение» не передаются (FE-04), формы множественного числа — всегда.
func TestBuildPackSkipsIdentity(t *testing.T) {
	raw := map[string]string{
		"Сохранить":           "Сохранить",
		"Привет, :name":       "Привет, :name",
		"Касса":               "Касса!",
		":count ПК|:count ПК": ":count ПК|:count ПК",
	}
	p, err := buildPack("ru", raw)
	if err != nil {
		t.Fatal(err)
	}
	got := string(p.Body)
	for _, absent := range []string{`"Сохранить"`, `"Привет, {{name}}"`} {
		if strings.Contains(got, absent) {
			t.Errorf("pack has identity entry %s: %s", absent, got)
		}
	}
	for _, present := range []string{`"Касса":"Касса!"`, `_one":"{{count}} ПК"`, `_few":"{{count}} ПК"`, `_many":"{{count}} ПК"`} {
		if !strings.Contains(got, present) {
			t.Errorf("pack lacks %s: %s", present, got)
		}
	}
	if p.ETag == "" {
		t.Error("no ETag")
	}
}
