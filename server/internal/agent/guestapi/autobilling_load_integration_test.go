//go:build integration

package guestapi_test

import (
	"context"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/guestapi"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
)

var seedPhone atomic.Int64

// seedDueGuests — n гостей клуba club без сессии и без денег, вошедших на свои ПК зоны zone: строки ПК, клиентов,
// членств и балансов (SQL), токен гостя (Tokens.Issue) и «устоявшееся» состояние автобиллинга (en=1, w=0 — как
// после первого пересмотра) со сроком now. Возвращает ПК.
func (h *harness) seedDueGuests(club, zone int64, n int) []int64 {
	h.t.Helper()
	ctx := context.Background()
	base := seedPhone.Add(int64(n)) - int64(n)
	var pcs, users []int64
	rows, err := env.db.Owner.Query(ctx, `INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at)
		SELECT $1, $2, 100000 + g, now(), now() FROM generate_series(1, $3::int) g RETURNING id`, club, zone, n)
	if err != nil {
		h.t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			h.t.Fatal(err)
		}
		pcs = append(pcs, id)
	}
	rows.Close()
	rows, err = env.db.Owner.Query(ctx, `INSERT INTO users (name, phone, password, created_at, updated_at)
		SELECT 'Гость ' || g, '+76' || lpad(($1::bigint + g)::text, 9, '0'), 'x', now(), now() FROM generate_series(1, $2::int) g
		RETURNING id`, base, n)
	if err != nil {
		h.t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			h.t.Fatal(err)
		}
		users = append(users, id)
	}
	rows.Close()
	if len(pcs) != n || len(users) != n {
		h.t.Fatalf("seeded %d computers, %d users", len(pcs), len(users))
	}
	h.exec(`INSERT INTO club_memberships (club_id, user_id, role, created_at, updated_at)
		SELECT $1, u, 'client', now(), now() FROM unnest($2::bigint[]) u`, club, users)
	h.exec(`INSERT INTO member_balances (club_id, user_id, created_at, updated_at)
		SELECT $1, u, now(), now() FROM unnest($2::bigint[]) u`, club, users)

	now := strconv.FormatInt(h.clock.Now().UnixMilli(), 10)
	prefix := h.prefix + "guest:"
	var wg sync.WaitGroup
	errs := make(chan error, 1)
	sem := make(chan struct{}, 64)
	for i := range pcs {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			pc, user := pcs[i], users[i]
			if _, _, err := h.srv.Tokens().Issue(ctx, identity.Identity{ClubID: club, ComputerID: pc}, user); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
			ps := strconv.FormatInt(pc, 10)
			vc := env.valkey
			for _, r := range vc.DoMulti(ctx,
				vc.B().Hset().Key(prefix+"ab:"+ps).FieldValue().FieldValue("club", strconv.FormatInt(club, 10)).
					FieldValue("user", strconv.FormatInt(user, 10)).FieldValue("signin", now).FieldValue("mode", "pause").
					FieldValue("pause", "0").FieldValue("step", "0").FieldValue("en", "1").FieldValue("w", "0").FieldValue("pe", "0").Build(),
				vc.B().Zadd().Key(prefix+"ab:due").ScoreMember().ScoreMember(float64(h.clock.Now().UnixMilli()), ps).Build()) {
				if err := r.Error(); err != nil {
					select {
					case errs <- err:
					default:
					}
				}
			}
		})
	}
	wg.Wait()
	select {
	case err := <-errs:
		h.t.Fatal(err)
	default:
	}
	return pcs
}

// dueNow — сколько ПК ждут пересмотра автобиллинга (срок ≤ часов теста).
func (h *harness) dueNow() int64 {
	h.t.Helper()
	n, err := env.valkey.Do(context.Background(), env.valkey.B().Zcount().Key(h.prefix+"guest:ab:due").Min("-inf").
		Max(strconv.FormatInt(h.clock.Now().UnixMilli(), 10)).Build()).AsInt64()
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

// S6-O4-10: просроченных больше пакета (AutoBillingBatch = 200) — один тик пересматривает всех (было: 200 за тик,
// остальные ждали следующих тиков). Пересмотр — настоящий (токен, тенантная транзакция, решение): каждый ПК получает
// новый срок через AutoBillingRecheck. Метрика отставания — по тому же zset.
func TestAutoBillingTickDrainsBacklog(t *testing.T) {
	// Бюджет тика — с запасом: тест проверяет «пакеты, пока есть просроченные», а не скорость машины (под полным
	// параллельным прогоном CI 800 мс на 450 ПК не хватало). Остановку по бюджету проверяет TestAutoBillingTickBudget.
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings,
		options: func(o *guestapi.Options) { o.AutoBillingTickBudget = time.Minute }})
	const n = 2*guestapi.AutoBillingBatch + 50
	pcs := h.seedDueGuests(h.club, h.zone, n)
	h.clock.advance(5 * time.Second)

	due, oldest, err := h.srv.AutoBillingBacklog(context.Background())
	if err != nil || due != n || oldest != 5*time.Second {
		t.Fatalf("backlog before = %d, %v, %v", due, oldest, err)
	}
	got, err := h.srv.TickAutoBilling(context.Background())
	if err != nil || got != n {
		t.Fatalf("tick = %d, %v; want %d", got, err, n)
	}
	if d := h.dueNow(); d != 0 {
		t.Fatalf("still due after one tick: %d", d)
	}
	// Каждый ПК пересмотрен: срок — через AutoBillingRecheck от часов теста.
	want := float64(h.clock.Now().Add(guestapi.AutoBillingRecheck).UnixMilli())
	for _, pc := range []int64{pcs[0], pcs[n/2], pcs[n-1]} {
		sc, err := env.valkey.Do(context.Background(), env.valkey.B().Zscore().Key(h.prefix+"guest:ab:due").
			Member(strconv.FormatInt(pc, 10)).Build()).AsFloat64()
		if err != nil || sc != want {
			t.Fatalf("pc %d due = %v, %v; want %v", pc, sc, err, want)
		}
	}
	if due, oldest, err := h.srv.AutoBillingBacklog(context.Background()); err != nil || due != 0 || oldest != 0 {
		t.Fatalf("backlog after = %d, %v, %v", due, oldest, err)
	}
}

// S6-O4-10, замер (docs/load-test.md «Автобиллинг на 50 000 ПК»): ZAFIRO_AB_LOAD=<число ПК> (например 50000) —
// столько гостей без сессии со сроком «сейчас» в 100 клубах; тики (бюджет 800 мс, как в проде) до полного разбора.

// Бюджет тика вышел — новые пакеты не берутся, начатый доделывается: при бюджете ≈ 0 тик пересматривает ровно
// один пакет, остальные ждут следующего тика.
func TestAutoBillingTickBudget(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings,
		options: func(o *guestapi.Options) { o.AutoBillingTickBudget = time.Nanosecond }})
	const n = guestapi.AutoBillingBatch + 30
	h.seedDueGuests(h.club, h.zone, n)
	h.clock.advance(5 * time.Second)
	got, err := h.srv.TickAutoBilling(context.Background())
	if err != nil || got != guestapi.AutoBillingBatch {
		t.Fatalf("tick = %d, %v; want %d (one batch)", got, err, guestapi.AutoBillingBatch)
	}
	if d := h.dueNow(); d != n-guestapi.AutoBillingBatch {
		t.Fatalf("due after budget-limited tick = %d, want %d", d, n-guestapi.AutoBillingBatch)
	}
}

// Без переменной пропускается (минута на подготовку данных — не для каждого прогона).
func TestAutoBillingLoad(t *testing.T) {
	total, _ := strconv.Atoi(os.Getenv("ZAFIRO_AB_LOAD"))
	if total <= 0 {
		t.Skip("ZAFIRO_AB_LOAD not set")
	}
	workers, _ := strconv.Atoi(os.Getenv("ZAFIRO_AB_WORKERS"))
	h := newHarness(t, harnessOpts{noLimits: true, settings: wholeAndAutoSettings, options: func(o *guestapi.Options) {
		o.AutoBillingWorkers = workers
	}})
	const clubs = 100
	seedStart := time.Now()
	for c := range clubs {
		club, zone := h.club, h.zone
		if c > 0 {
			club = env.db.Club(t, uniq("ab-load-"))
			h.exec("UPDATE clubs SET settings = $2::jsonb WHERE id = $1", club, wholeAndAutoSettings)
			h.scan(&zone, "INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, 'Hall', 60000, now(), now()) RETURNING id", club)
		}
		h.seedDueGuests(club, zone, total/clubs)
	}
	t.Logf("seeded %d guests in %v", total, time.Since(seedStart).Round(time.Millisecond))
	h.clock.advance(time.Second)

	var ticks []time.Duration
	processed := 0
	start := time.Now()
	for h.dueNow() > 0 && len(ticks) < 1000 {
		ts := time.Now()
		n, err := h.srv.TickAutoBilling(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ticks = append(ticks, time.Since(ts))
		processed += n
	}
	elapsed := time.Since(start)
	var maxTick time.Duration
	for _, d := range ticks {
		maxTick = max(maxTick, d)
	}
	if workers <= 0 {
		workers = guestapi.DefaultAutoBillingWorkers
	}
	t.Logf("workers=%d: %d computers in %d ticks, %v total, max tick %v, %.0f computers/s",
		workers, processed, len(ticks),
		elapsed.Round(time.Millisecond), maxTick.Round(time.Millisecond), float64(processed)/elapsed.Seconds())
	if processed != total {
		t.Fatalf("processed %d of %d", processed, total)
	}
}
