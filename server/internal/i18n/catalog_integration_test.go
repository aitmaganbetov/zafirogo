//go:build integration

package i18n_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

func seedCatalog(t *testing.T, d *dbtest.DB) int64 {
	t.Helper()
	ctx := context.Background()
	shipped := map[string]i18n.Shipped{
		"ru": {"*": {"Привет": "Привет", "Касса": "Касса"}},
		"kk": {"*": {"Привет": "Сәлем", "Касса": "Касса"}},
		"en": {"*": {"Привет": "Hello", "Касса": "Cash desk", "Только en": "Only en"}},
	}
	if _, err := i18n.Import(ctx, d.Owner, shipped); err != nil {
		t.Fatal(err)
	}
	club := d.Club(t, "i18n-club")
	if _, err := d.Owner.Exec(ctx, "UPDATE clubs SET locale = 'kk' WHERE id = $1", club); err != nil {
		t.Fatal(err)
	}
	return club
}

func waitLookup(t *testing.T, c *i18n.Catalog, locale, key, want string, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		if v, _ := c.Lookup(context.Background(), locale, key); v == want {
			return time.Since(start)
		}
		if time.Since(start) > within {
			v, ok := c.Lookup(context.Background(), locale, key)
			t.Fatalf("Lookup(%s, %q) = %q, %v after %v; want %q", locale, key, v, ok, within, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PB-01: обработчики держат транзакцию клуба и переводят текст (h.d.Tx → h.d.T). Пул из 3 соединений, 12 параллельных
// «обработчиков», каталог — на ТОМ ЖЕ пуле (худший случай; сервер даёт каталогу платформенный пул). Раньше сверка версии
// переводов (раз в 5 с, под общим мьютексом) брала второе соединение из занятого пула: все соединения оставались
// «idle in transaction» до таймаута запроса. Теперь перевод в базу не ходит — ни один «запрос» не ждёт.
func TestTranslateInsideTenantTxDoesNotStarvePool(t *testing.T) {
	d := dbtest.New(t)
	club := seedCatalog(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(d.AppDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	cat := i18n.NewCatalog(pool)
	waitLookup(t, cat, "kk", "Привет", "Сәлем", 10*time.Second) // прогрев, как при старте сервера

	const workers = 12
	var (
		wg      sync.WaitGroup
		slowest atomic.Int64
		failed  atomic.Int64
		wrong   atomic.Int64
		done    atomic.Int64
	)
	deadline := time.Now().Add(7 * time.Second) // дольше периода сверки версии (5 с раньше, 3 с теперь)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				start := time.Now()
				reqCtx, reqCancel := context.WithTimeout(ctx, 10*time.Second) // «таймаут запроса»
				err := db.InTenantTx(reqCtx, pool, club, func(q *store.Queries) error {
					if _, err := q.ClubLocaleTimezone(reqCtx, club); err != nil { // запрос в транзакции клуба
						return err
					}
					if got := cat.Translate(reqCtx, cat.ClubLocale(reqCtx, club), "Привет", nil); got != "Сәлем" {
						wrong.Add(1)
					}
					return nil
				})
				reqCancel()
				if err != nil {
					failed.Add(1)
				}
				done.Add(1)
				if el := int64(time.Since(start)); el > slowest.Load() {
					slowest.Store(el)
				}
			}
		}()
	}
	wg.Wait()
	if failed.Load() > 0 || wrong.Load() > 0 {
		t.Fatalf("%d of %d requests failed, %d untranslated", failed.Load(), done.Load(), wrong.Load())
	}
	if s := time.Duration(slowest.Load()); s > 2*time.Second {
		t.Fatalf("slowest request took %v: translations starve the pool", s)
	}
	t.Logf("%d requests, slowest %v", done.Load(), time.Duration(slowest.Load()))
}

// Правка перевода: в пакете i18next — сразу (другой ETag), в текстах сервера — не позже 5 с. Язык клуба — так же.
// Пакет не содержит записей «ключ = значение»; выключенный язык — нет пакета и нет перевода.
func TestCatalogSeesEdits(t *testing.T) {
	d := dbtest.New(t)
	club := seedCatalog(t, d)
	ctx := context.Background()
	cat := i18n.NewCatalog(d.Platform)

	p1, err := cat.Pack(ctx, "kk")
	if err != nil {
		t.Fatal(err)
	}
	var lines map[string]string
	if err := json.Unmarshal(p1.Body, &lines); err != nil {
		t.Fatal(err)
	}
	if lines["Привет"] != "Сәлем" || lines["Только en"] != "Only en" {
		t.Fatalf("kk pack = %v", lines)
	}
	if _, ok := lines["Касса"]; ok {
		t.Fatalf("identity entry in the pack: %v", lines)
	}
	ru, err := cat.Pack(ctx, "ru")
	if err != nil {
		t.Fatal(err)
	}
	if string(ru.Body) != "{}" {
		t.Fatalf("ru pack = %s, want {} (all entries are key = value)", ru.Body)
	}
	if got := cat.ClubLocale(ctx, club); got != "kk" {
		t.Fatalf("ClubLocale = %q", got)
	}
	if got := cat.Translate(ctx, "kk", "Касса", nil); got != "Касса" {
		t.Fatalf("Translate identity = %q", got)
	}

	if _, err := d.Owner.Exec(ctx, `UPDATE translations t SET value = 'Сәлеметсіз бе', updated_at = now() + interval '1 second'
		FROM languages l WHERE l.id = t.language_id AND l.code = 'kk' AND t.key = 'Привет'`); err != nil {
		t.Fatal(err)
	}
	p2, err := cat.Pack(ctx, "kk")
	if err != nil {
		t.Fatal(err)
	}
	if p2.ETag == p1.ETag {
		t.Fatal("ETag did not change after an edit")
	}
	if err := json.Unmarshal(p2.Body, &lines); err != nil || lines["Привет"] != "Сәлеметсіз бе" {
		t.Fatalf("pack after edit = %v, %v", lines["Привет"], err)
	}
	if took := waitLookup(t, cat, "kk", "Привет", "Сәлеметсіз бе", 5*time.Second); took > 5*time.Second {
		t.Fatalf("server text changed after %v", took)
	}

	// Язык клуба, изменённый мимо этого инстанса, — фоновой сверкой.
	if _, err := d.Owner.Exec(ctx, "UPDATE clubs SET locale = 'en' WHERE id = $1", club); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for cat.ClubLocale(ctx, club) != "en" {
		if time.Since(start) > 5*time.Second {
			t.Fatalf("ClubLocale = %q after 5s", cat.ClubLocale(ctx, club))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Язык выключен: пакета нет, перевода нет (и без похода в базу на каждый вызов).
	if _, err := d.Owner.Exec(ctx, "UPDATE languages SET is_enabled = false, updated_at = now() + interval '2 second' WHERE code = 'kk'"); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Pack(ctx, "kk"); !errors.Is(err, i18n.ErrUnknownLocale) {
		t.Fatalf("Pack of a disabled locale: %v", err)
	}
	langs, _, err := cat.Enabled(ctx)
	if err != nil || len(langs) != 2 {
		t.Fatalf("Enabled = %v, %v", langs, err)
	}
	if _, ok := cat.Lookup(ctx, "kk", "Привет"); ok {
		t.Fatal("disabled locale translated")
	}
}
