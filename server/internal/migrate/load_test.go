//go:build integration && load

package migrate_test

// Замер объёмов (S5-M1-О3 → S5-M3-5): синтетический клуб в MySQL старой схемы — 50 000 клиентов, 1 000 000 записей
// activity_logs, 300 000 кассовых операций, 200 ПК — plan/apply/verify/purge с замером времени, памяти Go, объёма WAL
// транзакции и блокировок. Не в CI (тег load):
//
//	cd server && TESTCONTAINERS_RYUK_DISABLED=true go test -tags integration,load -run TestLoad -timeout 60m -v ./internal/migrate/
//
// Размеры можно уменьшить: ZAFIRO_LOAD_SCALE=0.1 (десятая часть). Числа — tools/migrate-from-mysql/README.md «Объёмы».

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate/migratetest"
)

const loadClub = 3

type loadSizes struct{ Clients, Logs, Cash, PCs int }

func loadScale(t *testing.T) loadSizes {
	s := loadSizes{Clients: 50_000, Logs: 1_000_000, Cash: 300_000, PCs: 200}
	if v := os.Getenv("ZAFIRO_LOAD_SCALE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 {
			t.Fatalf("ZAFIRO_LOAD_SCALE=%q", v)
		}
		s = loadSizes{Clients: int(float64(s.Clients) * f), Logs: int(float64(s.Logs) * f), Cash: int(float64(s.Cash) * f), PCs: s.PCs}
	}
	return s
}

// seedLoadClub — синтетический клуб старой схемы (те же таблицы, что пишет Laravel): клиенты с балансами, смены,
// приходы/расходы (пополнения с receipt_data.top_up, продажи с позициями чека, расходы), журнал за 24 месяца.
func seedLoadClub(t *testing.T, my *sql.DB, s loadSizes) {
	t.Helper()
	start := time.Now()
	q := func(query string, args ...any) {
		t.Helper()
		if _, err := my.Exec(query, args...); err != nil {
			t.Fatalf("seed: %s: %v", query, err)
		}
	}
	q("CREATE TABLE load_digits (d INT PRIMARY KEY)")
	q("INSERT INTO load_digits VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)")
	q("CREATE TABLE load_seq (n INT PRIMARY KEY)")
	q(`INSERT INTO load_seq SELECT a.d + 10*b.d + 100*c.d + 1000*d.d + 10000*e.d + 100000*f.d + 1
		FROM load_digits a, load_digits b, load_digits c, load_digits d, load_digits e, load_digits f`)
	const (
		userBase  = 1_000_000
		zoneBase  = 1_000
		pcBase    = 10_000
		shiftBase = 100_000
		shifts    = 1_000
	)
	q(`INSERT INTO clubs (id, name, slug, settings, created_at, updated_at, locale, timezone, agent_staff_minutes)
		VALUES (?, 'Нагрузочный клуб', 'load-club', NULL, '2024-01-01 00:00:00', '2024-01-01 00:00:00', 'ru', 'Asia/Almaty', 15)`, loadClub)
	q(`INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, current_period_ends_at, canceled_at, created_at, updated_at)
		VALUES (?, 1, 'suspended', '2024-01-01 00:00:00', '2030-01-01 00:00:00', NOW(), NOW(), NOW())`, loadClub)
	q(`INSERT INTO users (id, name, phone, password, created_at, updated_at)
		SELECT ? + n, CONCAT('Клиент ', n), CONCAT('+7790', LPAD(n, 7, '0')), '$2y$12$abcdefghijklmnopqrstuuJ9C3rI6P0E1wq8b8yQe7m3p6aYF7xS2', NOW(), NOW()
		FROM load_seq WHERE n <= ?`, userBase, s.Clients)
	q(`INSERT INTO club_memberships (club_id, user_id, role, discount, total_spent, created_at, updated_at)
		SELECT ?, ? + n, IF(n <= 5, ELT(n, 'owner', 'admin', 'manager', 'cashier', 'operator'), 'client'), 0, (n % 500) * 10, NOW(), NOW()
		FROM load_seq WHERE n <= ?`, loadClub, userBase, s.Clients)
	q(`INSERT INTO member_balances (club_id, user_id, balance, bonus_balance, created_at, updated_at)
		SELECT ?, ? + n, (n % 997) * 10 + 0.25, (n % 13) * 5, NOW(), NOW() FROM load_seq WHERE n <= ?`, loadClub, userBase, s.Clients)
	q(`INSERT INTO club_zones (id, club_id, name, price_per_hour, created_at, updated_at)
		SELECT ? + n, ?, CONCAT('Зона ', n), 400 + n * 100, NOW(), NOW() FROM load_seq WHERE n <= 4`, zoneBase, loadClub)
	q(`INSERT INTO club_computers (id, club_id, club_zone_id, number, status, created_at, updated_at)
		SELECT ? + n, ?, ? + 1 + n % 4, n, 'available', NOW(), NOW() FROM load_seq WHERE n <= ?`, pcBase, loadClub, zoneBase, s.PCs)
	q(`INSERT INTO tariffs (id, club_id, name, duration_minutes, price, is_active, payment_method, currency, created_at, updated_at)
		SELECT 5000 + n, ?, CONCAT('Тариф ', n), 60 * n, 500 * n, 1, 'main_or_bonus', 'KZT', NOW(), NOW() FROM load_seq WHERE n <= 5`, loadClub)
	q(`INSERT INTO product_categories (id, club_id, name, created_at, updated_at) VALUES (900, ?, 'Напитки', NOW(), NOW())`, loadClub)
	q(`INSERT INTO products (id, club_id, product_category_id, sku, name, category, price, quantity, created_at, updated_at)
		SELECT 9000 + n, ?, 900, CONCAT('LOAD-', n), CONCAT('Напиток ', n), 'Напитки', 300 + n * 50, 1000, NOW(), NOW()
		FROM load_seq WHERE n <= 10`, loadClub)
	// Смены: 1000 закрытых и одна открытая (последняя); операции — равномерно по сменам.
	q(`INSERT INTO cash_shifts (id, club_id, opened_by, closed_by, status, open_marker, opening_balance, closing_balance, opened_at, closed_at, created_at, updated_at)
		SELECT ? + n, ?, ? + 4, IF(n = ?, NULL, ? + 4), IF(n = ?, 'open', 'closed'), IF(n = ?, 1, NULL), 10000, IF(n = ?, NULL, 10000),
		       DATE_SUB(NOW(), INTERVAL (? - n) DAY), IF(n = ?, NULL, DATE_SUB(NOW(), INTERVAL (? - n) DAY) + INTERVAL 12 HOUR), NOW(), NOW()
		FROM load_seq WHERE n <= ?`,
		shiftBase, loadClub, userBase, shifts, userBase, shifts, shifts, shifts, shifts, shifts, shifts, shifts)
	q(`INSERT INTO cash_transactions (cash_shift_id, user_id, type, amount, description, receipt_data, created_at, updated_at, club_id)
		SELECT ? + 1 + n % ?, ? + 4, IF(n % 3 = 2, 'expense', 'income'), 100 + n % 5000,
		       CASE n % 3 WHEN 0 THEN 'Пополнение основной баланс' WHEN 1 THEN 'Продажа POS' ELSE 'Закупка' END,
		       CASE n % 3
		         WHEN 0 THEN JSON_OBJECT('top_up', JSON_OBJECT('user_id', ? + 1 + n % ?, 'balance_type', 'balance', 'bonus', 0))
		         WHEN 1 THEN JSON_OBJECT('items', JSON_ARRAY(JSON_OBJECT('product_id', 9001 + n % 10, 'name', CONCAT('Напиток ', 1 + n % 10),
		                     'price', 350 + (n % 10) * 50, 'quantity', 1, 'total', 350 + (n % 10) * 50)))
		         ELSE NULL END,
		       DATE_SUB(NOW(), INTERVAL (n % 700) DAY), NOW(), ?
		FROM load_seq WHERE n <= ?`,
		shiftBase, shifts, userBase, userBase, s.Clients, loadClub, s.Cash)
	// Журнал: вход, покупка тарифа (детали с суммами — для Q-005 и tariff_sales), пополнение, прочее; 24 месяца.
	q(`INSERT INTO activity_logs (event, description, user_id, subject_type, subject_id, details, ip_address, created_at, updated_at, club_id)
		SELECT ELT(1 + n % 4, 'auth.login', 'tariff.purchased', 'computer.session_ended', 'user.updated'),
		       'Событие нагрузочного теста', ? + 1 + n % ?,
		       IF(n % 4 = 1, 'App\\Models\\Tariff', NULL), IF(n % 4 = 1, 5001 + n % 5, NULL),
		       IF(n % 4 = 1, JSON_OBJECT('amount', '500.00', 'main_balance_amount', '500.00', 'bonus_balance_amount', '0.00',
		                                 'computer_id', ? + 1 + n % ?, 'session_id', CONCAT('s', n)), NULL),
		       '10.0.0.1', DATE_SUB(NOW(), INTERVAL (n % 720) DAY), NOW(), ?
		FROM load_seq WHERE n <= ?`, userBase, s.Clients, pcBase, s.PCs, loadClub, s.Logs)
	q("DROP TABLE load_seq")
	q("DROP TABLE load_digits")
	t.Logf("MySQL: синтетический клуб за %s (%d клиентов, %d журнала, %d касса, %d ПК)", time.Since(start).Round(time.Second),
		s.Clients, s.Logs, s.Cash, s.PCs)
}

// memPeak — пик памяти Go (HeapInuse, Sys) за время замера.
type memPeak struct {
	stop       chan struct{}
	done       sync.WaitGroup
	heap, sys  atomic.Uint64
	startAlloc uint64
}

func watchMem() *memPeak {
	runtime.GC()
	m := &memPeak{stop: make(chan struct{})}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.startAlloc = ms.TotalAlloc
	m.done.Add(1)
	go func() {
		defer m.done.Done()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > m.heap.Load() {
				m.heap.Store(ms.HeapInuse)
			}
			if ms.Sys > m.sys.Load() {
				m.sys.Store(ms.Sys)
			}
			select {
			case <-m.stop:
				return
			case <-tick.C:
			}
		}
	}()
	return m
}

func (m *memPeak) String() string {
	close(m.stop)
	m.done.Wait()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return fmt.Sprintf("пик HeapInuse %d МБ, Sys %d МБ, выделено всего %d МБ",
		m.heap.Load()>>20, m.sys.Load()>>20, (ms.TotalAlloc-m.startAlloc)>>20)
}

// lockWatch — пока идёт apply: сколько блокировок держит транзакция переноса и берёт ли она ACCESS EXCLUSIVE
// (блокировал бы работу других клубов); заодно — что запись другого клуба в журнал не ждёт переноса.
type lockWatch struct {
	stop                   chan struct{}
	done                   sync.WaitGroup
	maxLocks, exclusive    atomic.Int64
	maxOtherWrite, samples atomic.Int64
}

func watchLocks(ctx context.Context, t *testing.T, owner, platform *pgxpool.Pool) *lockWatch {
	w := &lockWatch{stop: make(chan struct{})}
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-tick.C:
			}
			var n, excl int64
			if err := owner.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE mode = 'AccessExclusiveLock' AND locktype = 'relation')
				FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
				WHERE a.state <> 'idle' AND a.pid <> pg_backend_pid() AND a.xact_start IS NOT NULL`).Scan(&n, &excl); err != nil {
				continue
			}
			if n > w.maxLocks.Load() {
				w.maxLocks.Store(n)
			}
			w.exclusive.Add(excl)
			// Другой клуб пишет журнал (партиционированная таблица) во время переноса.
			began := time.Now()
			if _, err := platform.Exec(ctx, `INSERT INTO activity_logs (club_id, event, description, user_id, ip_address, created_at, updated_at)
				SELECT 1, 'auth.login', 'Вход', 2, '127.0.0.1', now(), now() WHERE EXISTS (SELECT 1 FROM club_memberships WHERE club_id = 1 AND user_id = 2)`); err != nil {
				t.Logf("запись другого клуба: %v", err)
			}
			if d := time.Since(began).Milliseconds(); d > w.maxOtherWrite.Load() {
				w.maxOtherWrite.Store(d)
			}
			w.samples.Add(1)
		}
	}()
	return w
}

func (w *lockWatch) String() string {
	close(w.stop)
	w.done.Wait()
	return fmt.Sprintf("блокировок у транзакции до %d, ACCESS EXCLUSIVE на таблицах: %d замеров; запись другого клуба: до %d мс (%d замеров)",
		w.maxLocks.Load(), w.exclusive.Load(), w.maxOtherWrite.Load(), w.samples.Load())
}

func walBytes(t *testing.T, pool *pgxpool.Pool) func() string {
	t.Helper()
	var start string
	if err := pool.QueryRow(context.Background(), "SELECT pg_current_wal_lsn()::text").Scan(&start); err != nil {
		t.Fatal(err)
	}
	return func() string {
		var n int64
		if err := pool.QueryRow(context.Background(), "SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), $1::pg_lsn)::bigint", start).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("WAL %d МБ", n>>20)
	}
}

func TestLoad(t *testing.T) {
	ctx := context.Background()
	s := loadScale(t)
	my, _ := migratetest.StartMySQL(t, "--skip-log-bin", "--innodb-buffer-pool-size=1G")
	pgdb := dbtest.New(t)
	owner := pgdb.Owner
	seedLoadClub(t, my, s)
	// Клуб 1 демо-данных — «другой клуб» для замера блокировок (пишет журнал во время переноса).
	now := time.Now().UTC()
	if r, err := migrate.Apply(ctx, my, owner, migrate.Options{Club: "1", IDReserve: idReserve, Now: now,
		Extensions: migrate.DefaultExtensions()}); err != nil {
		t.Fatalf("apply клуба 1: %v\n%s", err, reportText(r))
	}
	opts := migrate.Options{Club: "load-club", IDReserve: idReserve, Now: now, ThawSubscription: "active",
		Extensions: migrate.DefaultExtensions()}

	mem := watchMem()
	began := time.Now()
	plan, err := migrate.Plan(ctx, my, owner, opts)
	if err != nil || plan.Count(migrate.Blocker) != 0 {
		t.Fatalf("plan: %v\n%s", err, reportText(plan))
	}
	t.Logf("plan: %s; %s", time.Since(began).Round(time.Millisecond), mem)

	mem = watchMem()
	wal := walBytes(t, owner)
	locks := watchLocks(ctx, t, owner, pgdb.Platform)
	began = time.Now()
	applied, err := migrate.Apply(ctx, my, owner, opts)
	took := time.Since(began)
	lockStats := locks.String()
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, reportText(applied))
	}
	t.Logf("apply (одна транзакция, со сверкой до коммита): %s; %s; %s; %s", took.Round(time.Millisecond), mem, wal(), lockStats)
	logDurations(t, applied)

	mem = watchMem()
	began = time.Now()
	verified, err := migrate.Verify(ctx, my, owner, opts)
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, reportText(verified))
	}
	t.Logf("verify: %s; %s", time.Since(began).Round(time.Millisecond), mem)
	logDurations(t, verified)
	for _, name := range []string{"users", "club_memberships", "activity_logs", "cash_transactions", "club_computers"} {
		st := verified.Stat(name)
		t.Logf("  %-18s MySQL %8d  Postgres %8d", name, st.Source, st.Target)
	}
	t.Logf("  Q-005: %s", verified.Totals["Q-005: остаток = сумма движений истории"])

	var size string
	if err := owner.QueryRow(ctx, "SELECT pg_size_pretty(pg_database_size(current_database()))").Scan(&size); err != nil {
		t.Fatal(err)
	}
	t.Logf("размер базы Postgres после переноса: %s", size)

	wal = walBytes(t, owner)
	began = time.Now()
	purged, err := migrate.Purge(ctx, owner, "load-club", "load-club")
	if err != nil {
		t.Fatalf("purge: %v\n%s", err, reportText(purged))
	}
	t.Logf("purge: %s; %s; удалено журнала %d, аккаунтов %d", time.Since(began).Round(time.Millisecond), wal(),
		purged.Stat("activity_logs").Target, purged.Stat("users").Target)
}

func logDurations(t *testing.T, r *migrate.Report) {
	type step struct {
		name string
		ms   int64
	}
	var list []step
	for k, v := range r.Durations {
		if v >= 200 {
			list = append(list, step{k, v})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ms > list[j].ms })
	for _, s := range list {
		t.Logf("    %-40s %8d мс", s.name, s.ms)
	}
}
