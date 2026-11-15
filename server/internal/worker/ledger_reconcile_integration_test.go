//go:build integration

package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/observability"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// reconcileEnv — база, часы и сверка для сценариев AC-3.
type reconcileEnv struct {
	t   *testing.T
	d   *dbtest.DB
	ctx context.Context
	mu  sync.Mutex
	now time.Time
	w   *worker.LedgerReconcile
}

func newReconcileEnv(t *testing.T) *reconcileEnv {
	t.Helper()
	e := &reconcileEnv{t: t, d: dbtest.New(t), ctx: context.Background(), now: time.Now().UTC().Truncate(time.Microsecond)}
	e.w = &worker.LedgerReconcile{Platform: e.d.Platform, App: e.d.App, Now: e.clock}
	return e
}

func (e *reconcileEnv) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *reconcileEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

// tick — проход сверки, который обязан состояться (лок свободен).
func (e *reconcileEnv) tick() worker.LedgerReconcileResult {
	e.t.Helper()
	res, err := e.w.Tick(e.ctx)
	if err != nil || !res.Ran {
		e.t.Fatalf("tick: %+v err=%v", res, err)
	}
	return res
}

// pass — проход через час (частый) или сутки (полный) после прошлого.
func (e *reconcileEnv) pass(after time.Duration) worker.LedgerReconcileResult {
	e.t.Helper()
	e.advance(after)
	res := e.tick()
	if res.Skipped {
		e.t.Fatalf("pass skipped: %+v", res)
	}
	return res
}

func (e *reconcileEnv) scalar(sql string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.d.Owner.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *reconcileEnv) logs(event string) int64 {
	return e.scalar("SELECT count(*) FROM platform_logs WHERE event = $1", event)
}

func (e *reconcileEnv) open() int64 {
	return e.scalar("SELECT count(*) FROM ledger_reconcile_findings WHERE resolved_at IS NULL")
}

// balance — начальный баланс клиента честной операцией ledger.
func (e *reconcileEnv) balance(club, user int64, main, bonus money.Tiyn) {
	e.t.Helper()
	if err := db.InTenantTx(e.ctx, e.d.App, club, func(q *store.Queries) error {
		_, err := billing.SetInitialBalance(e.ctx, q, billing.InitialBalanceParams{
			ClubID: club, UserID: user, Main: main, Bonus: bonus, Actor: billing.BalanceActor{Kind: "system"},
		})
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
}

// bypass — запись в обход журнала: суперпользователь с выключенными триггерами (сторож member_balances и
// append-only ledger в бою этого не дают).
func (e *reconcileEnv) bypass(sql string, args ...any) {
	e.t.Helper()
	tx, err := e.d.Owner.Begin(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(e.ctx) }()
	if _, err := tx.Exec(e.ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		e.t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	if err := tx.Commit(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

// tamper меняет баланс клиента мимо журнала; touched — как если бы запись обновила updated_at.
func (e *reconcileEnv) tamper(club, user, mainDelta, bonusDelta int64, touched bool) {
	e.t.Helper()
	if touched {
		e.bypass("UPDATE member_balances SET balance = balance + $3, bonus_balance = bonus_balance + $4, updated_at = $5 WHERE club_id = $1 AND user_id = $2",
			club, user, mainDelta, bonusDelta, e.clock())
		return
	}
	e.bypass("UPDATE member_balances SET balance = balance + $3, bonus_balance = bonus_balance + $4 WHERE club_id = $1 AND user_id = $2",
		club, user, mainDelta, bonusDelta)
}

// Чистая база — тишина: ни находок, ни записей журнала платформы; повторный тик раньше периода ничего не делает;
// первый проход и проход через сутки — полные, между ними — частые.
func TestLedgerReconcileCleanIsSilent(t *testing.T) {
	e := newReconcileEnv(t)
	club := e.d.Club(t, "clean")
	other := e.d.Club(t, "clean-2")
	for i, c := range []int64{club, club, other} {
		u := e.d.Customer(t, c, "Клиент")
		e.balance(c, u, money.Tiyn(100_000*(i+1)), money.Tiyn(5_000))
	}
	e.d.Customer(t, club, "Без операций") // нулевой баланс без проводок — не расхождение

	res := e.tick()
	if !res.Full || res.Skipped || res.Clubs != 2 || res.Found != 0 || res.Open != 0 {
		t.Fatalf("first pass: %+v", res)
	}
	if res := e.tick(); !res.Skipped {
		t.Fatalf("second tick within the interval must be skipped: %+v", res)
	}
	e.advance(59 * time.Minute)
	if res := e.tick(); !res.Skipped {
		t.Fatalf("tick before the interval must be skipped: %+v", res)
	}
	if res := e.pass(time.Minute); res.Full || res.Clubs != 2 || res.Found != 0 {
		t.Fatalf("hourly pass: %+v", res)
	}
	if res := e.pass(24 * time.Hour); !res.Full || res.Found != 0 {
		t.Fatalf("daily pass: %+v", res)
	}
	if n := e.scalar("SELECT count(*) FROM ledger_reconcile_findings"); n != 0 {
		t.Fatalf("findings on a clean base: %d", n)
	}
	if n := e.scalar("SELECT count(*) FROM platform_logs"); n != 0 {
		t.Fatalf("platform logs on a clean base: %d", n)
	}
	// Состояние: успех записан (по нему метрика и алерт «сверка не идёт»), ошибки нет.
	if n := e.scalar("SELECT count(*) FROM ledger_reconcile_state WHERE last_success_at = $1 AND last_full_at = $1 AND last_error = '' AND last_clubs = 2", e.clock()); n != 1 {
		t.Fatal("state after a successful pass")
	}
}

// Расхождение, созданное в обход журнала, находится и пишется в журнал платформы один раз: следующие проходы молчат,
// пока разница не изменится; изменилась — запись снова; исчезла — «больше не наблюдается». Сверка ничего не
// исправляет.
func TestLedgerReconcileFindsDriftOnce(t *testing.T) {
	e := newReconcileEnv(t)
	club := e.d.Club(t, "drift")
	user := e.d.Customer(t, club, "Айгерим")
	clean := e.d.Customer(t, club, "Чистый")
	e.balance(club, user, 100_000, 2_000)
	e.balance(club, clean, 50_000, 0)
	if res := e.tick(); res.Found != 0 {
		t.Fatalf("clean pass: %+v", res)
	}

	// +500 тиын к основному балансу мимо журнала.
	e.advance(30 * time.Minute)
	e.tamper(club, user, 500, 0, true)
	res := e.pass(30 * time.Minute)
	if res.Full || res.Found != 1 || res.Open != 1 || res.Resolved != 0 {
		t.Fatalf("pass after tamper: %+v", res)
	}
	var (
		logClub, logSubject  int64
		subjectType, desc    string
		rawDetails, rawFound []byte
		fingerprint          string
	)
	if err := e.d.Owner.QueryRow(e.ctx, `SELECT club_id, subject_id, subject_type, description, details FROM platform_logs
		WHERE event = 'ledger.drift_found'`).Scan(&logClub, &logSubject, &subjectType, &desc, &rawDetails); err != nil {
		t.Fatal(err)
	}
	if logClub != club || logSubject != user || subjectType != `App\Models\User` ||
		!strings.Contains(desc, "drift") || !strings.Contains(desc, "на 500 тиын") || !strings.Contains(desc, "бонусы — на 0 тиын") {
		t.Fatalf("platform log: club=%d subject=%d %s %q", logClub, logSubject, subjectType, desc)
	}
	var details map[string]any
	if err := json.Unmarshal(rawDetails, &details); err != nil {
		t.Fatal(err)
	}
	if details["diff_tiyn"] != float64(500) || details["balance_tiyn"] != float64(100_500) || details["ledger_tiyn"] != float64(100_000) || details["bonus_diff_tiyn"] != float64(0) {
		t.Fatalf("platform log details: %v", details)
	}
	if err := e.d.Owner.QueryRow(e.ctx, `SELECT fingerprint, details FROM ledger_reconcile_findings
		WHERE club_id = $1 AND kind = 'member_balance' AND subject_id = $2 AND resolved_at IS NULL`, club, user).Scan(&fingerprint, &rawFound); err != nil {
		t.Fatal(err)
	}
	if fingerprint != "main=500;bonus=0" {
		t.Fatalf("fingerprint %q", fingerprint)
	}
	// Ничего не исправлено.
	if main, bonus := e.d.Balances(t, club, user); main != 100_500 || bonus != 2_000 {
		t.Fatalf("balance changed by the reconcile: %d %d", main, bonus)
	}

	// Те же данные час за часом и в полный проход — тишина.
	for _, after := range []time.Duration{time.Hour, time.Hour, 24 * time.Hour} {
		if res := e.pass(after); res.Found != 0 || res.Open != 1 || res.Resolved != 0 {
			t.Fatalf("repeat pass: %+v", res)
		}
	}
	if e.logs("ledger.drift_found") != 1 || e.open() != 1 || e.scalar("SELECT count(*) FROM ledger_reconcile_findings") != 1 {
		t.Fatalf("drift logged %d times, findings %d", e.logs("ledger.drift_found"), e.open())
	}
	if n := e.scalar("SELECT count(*) FROM ledger_reconcile_findings WHERE last_seen_at = $1 AND first_seen_at < $1", e.clock()); n != 1 {
		t.Fatal("last_seen_at is not updated")
	}

	// Клиент продолжает платить: баланс и журнал двигаются вместе, разница та же — молчим.
	if err := db.InTenantTx(e.ctx, e.d.App, club, func(q *store.Queries) error {
		_, err := billing.OwnerAdjust(e.ctx, q, billing.OwnerAdjustParams{
			ClubID: club, UserID: user, Field: billing.FieldMain, Amount: 7_000,
			IdempotencyKey: "reconcile-test-1", Actor: billing.BalanceActor{Kind: "system"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if res := e.pass(time.Hour); res.Found != 0 || res.Open != 1 {
		t.Fatalf("pass after an honest operation: %+v", res)
	}
	if e.logs("ledger.drift_found") != 1 {
		t.Fatal("honest operation re-logged the drift")
	}

	// Разница изменилась (ещё −200 мимо журнала и бонусы +30) — запись снова, находка та же.
	e.tamper(club, user, -200, 30, false)
	if res := e.pass(time.Hour); res.Found != 1 || res.Open != 1 {
		t.Fatalf("pass after the drift changed: %+v", res)
	}
	if e.logs("ledger.drift_found") != 2 || e.scalar("SELECT count(*) FROM ledger_reconcile_findings WHERE fingerprint = 'main=300;bonus=30'") != 1 ||
		e.scalar("SELECT count(*) FROM ledger_reconcile_findings") != 1 {
		t.Fatal("changed drift must be logged once more and keep one finding")
	}

	// Расхождение исчезло (исправили вручную) — закрыто, одна запись «больше не наблюдается».
	e.tamper(club, user, -300, -30, false)
	if res := e.pass(time.Hour); res.Found != 0 || res.Resolved != 1 || res.Open != 0 {
		t.Fatalf("pass after the fix: %+v", res)
	}
	if e.logs("ledger.drift_resolved") != 1 || e.open() != 0 {
		t.Fatal("resolved drift")
	}
	if res := e.pass(time.Hour); res.Found != 0 || res.Resolved != 0 {
		t.Fatalf("pass after resolve: %+v", res)
	}
	if e.logs("ledger.drift_found") != 2 || e.logs("ledger.drift_resolved") != 1 {
		t.Fatal("log counts after resolve")
	}

	// То же расхождение появилось снова — это новая находка и новая запись.
	e.tamper(club, user, 500, 0, true)
	if res := e.pass(time.Hour); res.Found != 1 || res.Open != 1 {
		t.Fatalf("pass after the drift returned: %+v", res)
	}
	if e.logs("ledger.drift_found") != 3 || e.scalar("SELECT count(*) FROM ledger_reconcile_findings") != 2 {
		t.Fatal("returned drift")
	}
}

// Частый проход читает только клиентов, чей баланс менялся (updated_at); запись в обход, не тронувшую updated_at,
// находит полный проход. Клубы независимы; находки не видны пулу клуба.
func TestLedgerReconcileIncrementalAndFull(t *testing.T) {
	e := newReconcileEnv(t)
	clubA, clubB := e.d.Club(t, "inc-a"), e.d.Club(t, "inc-b")
	a := e.d.Customer(t, clubA, "А")
	b := e.d.Customer(t, clubB, "Б")
	e.balance(clubA, a, 10_000, 0)
	e.balance(clubB, b, 20_000, 0)
	e.tick()

	// Давнее изменение (updated_at не тронут) — частый проход его не видит…
	e.tamper(clubB, b, 0, 40, false)
	e.bypass("UPDATE member_balances SET updated_at = $2 WHERE club_id = $1", clubB, e.clock().Add(-48*time.Hour))
	if res := e.pass(time.Hour); res.Full || res.Found != 0 {
		t.Fatalf("hourly pass must not read untouched members: %+v", res)
	}
	// …а свежее в другом клубе — видит.
	e.tamper(clubA, a, -1, 0, true)
	if res := e.pass(time.Hour); res.Full || res.Found != 1 {
		t.Fatalf("hourly pass: %+v", res)
	}
	if n := e.scalar("SELECT count(*) FROM ledger_reconcile_findings WHERE club_id = $1 AND fingerprint = 'main=-1;bonus=0'", clubA); n != 1 {
		t.Fatal("club A finding")
	}
	// Полный проход находит и давнее; о первом — молчит.
	if res := e.pass(24 * time.Hour); !res.Full || res.Found != 1 || res.Open != 2 {
		t.Fatalf("full pass: %+v", res)
	}
	if n := e.scalar("SELECT count(*) FROM ledger_reconcile_findings WHERE club_id = $1 AND fingerprint = 'main=0;bonus=40'", clubB); n != 1 {
		t.Fatal("club B finding")
	}
	if e.logs("ledger.drift_found") != 2 {
		t.Fatalf("drift logs: %d", e.logs("ledger.drift_found"))
	}

	// Пулу клуба таблицы сверки недоступны (права отозваны, RLS — только платформа).
	for _, table := range []string{"ledger_reconcile_findings", "ledger_reconcile_state"} {
		err := db.InTenantTx(e.ctx, e.d.App, clubA, func(q *store.Queries) error {
			var n int64
			return e.d.App.QueryRow(e.ctx, "SELECT count(*) FROM "+table).Scan(&n)
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("%s must not be readable by the club pool: %v", table, err)
		}
	}
}

// Метрики Prometheus читаются из тех же таблиц (сборщик observability, пул zafiro_platform): до первой сверки —
// 0 расхождений и время 0; после — время последней успешной и число открытых находок. По ним — алерты
// ZafiroLedgerDrift и ZafiroLedgerReconcileStale.
func TestLedgerReconcileMetrics(t *testing.T) {
	e := newReconcileEnv(t)
	club := e.d.Club(t, "metrics")
	user := e.d.Customer(t, club, "Клиент")
	e.balance(club, user, 10_000, 0)

	scrape := func() string {
		t.Helper()
		// Свой Observer на каждый сбор: значения из базы кэшируются на 10 с.
		o, err := observability.New(e.ctx, observability.Options{Instance: "test"})
		if err != nil {
			t.Fatal(err)
		}
		o.SetSources(observability.Sources{Platform: e.d.Platform})
		rec := httptest.NewRecorder()
		o.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		return rec.Body.String()
	}
	has := func(body, line string) {
		t.Helper()
		if !strings.Contains(body, line+"\n") {
			t.Fatalf("/metrics has no %q", line)
		}
	}
	before := scrape()
	has(before, "zafiro_ledger_reconcile_open_findings 0")
	has(before, "zafiro_ledger_reconcile_last_success_timestamp_seconds 0")

	e.tamper(club, user, 5, 0, true)
	e.tick()
	after := scrape()
	has(after, "zafiro_ledger_reconcile_open_findings 1")
	if !strings.Contains(after, fmt.Sprintf("zafiro_ledger_reconcile_last_success_timestamp_seconds %s", strconv.FormatFloat(float64(e.clock().UnixMicro())/1e6, 'g', -1, 64))) {
		t.Fatalf("last success metric: want %v in\n%s", e.clock().Unix(), grepLines(after, "ledger_reconcile"))
	}
	if strings.Contains(after, `zafiro_observability_collect_errors_total{source="ledger_reconcile"}`) {
		t.Fatal("collector reported errors")
	}
}

func grepLines(body, sub string) string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// Остальные инварианты: операция с суммой проводок не ноль (частый проход — по новым операциям) и снимок остатка
// кассы, не совпадающий с журналом (полный проход). Предел записей на клуб.
func TestLedgerReconcileOtherInvariants(t *testing.T) {
	e := newReconcileEnv(t)
	club := e.d.Club(t, "inv")
	user := e.d.Customer(t, club, "Клиент")
	e.balance(club, user, 10_000, 0)
	e.tick()

	// Операция с одной проводкой по счёту клуба (+300): в бою её не пропустит отложенный триггер баланса операции.
	e.advance(30 * time.Minute)
	var account int64
	if err := e.d.Owner.QueryRow(e.ctx, "SELECT id FROM ledger_accounts WHERE club_id = $1 AND kind = 'owner_adjustment'", club).Scan(&account); err != nil {
		t.Fatal(err)
	}
	var txID int64
	if err := e.d.Owner.QueryRow(e.ctx, "SELECT nextval('ledger_transaction_id_seq')").Scan(&txID); err != nil {
		t.Fatal(err)
	}
	at := e.clock()
	e.bypass("INSERT INTO ledger_transactions (id, club_id, kind, idempotency_key, created_at) VALUES ($1, $2, 'cash_income', 'bypass', $3)", txID, club, at)
	e.bypass("INSERT INTO ledger_entries (club_id, transaction_id, account_id, amount, created_at) VALUES ($1, $2, $3, 300, $4)", club, txID, account, at)
	res := e.pass(30 * time.Minute)
	if res.Full || res.Found != 1 || res.Open != 1 {
		t.Fatalf("pass after an unbalanced operation: %+v", res)
	}
	if n := e.scalar(`SELECT count(*) FROM ledger_reconcile_findings WHERE club_id = $1 AND kind = 'unbalanced_transaction'
		AND subject_id = $2 AND fingerprint = 'sum=300' AND resolved_at IS NULL`, club, txID); n != 1 {
		t.Fatal("unbalanced transaction finding")
	}
	if n := e.scalar("SELECT count(*) FROM platform_logs WHERE event = 'ledger.drift_found' AND subject_id = $1 AND description LIKE '%сумма проводок 300 тиын%'", txID); n != 1 {
		t.Fatal("unbalanced transaction log")
	}
	// Частые проходы старую операцию не перечитывают, но и не закрывают находку; полный — видит её снова и молчит.
	for _, after := range []time.Duration{time.Hour, time.Hour, 24 * time.Hour} {
		if res := e.pass(after); res.Found != 0 || res.Resolved != 0 || res.Open != 1 {
			t.Fatalf("repeat pass: %+v", res)
		}
	}

	// Снимок остатка кассы закрытой смены: 1 000 тиын при пустом журнале кассы.
	cashier := e.d.Customer(t, club, "Кассир")
	var shift int64
	if err := e.d.Owner.QueryRow(e.ctx, `INSERT INTO cash_shifts (club_id, opened_by, closed_by, status, opening_balance, closing_balance,
			opened_at, closed_at, created_at, updated_at, drawer_snapshot_entry_id, drawer_snapshot_balance, drawer_snapshot_at)
		VALUES ($1, $2, $2, 'closed', 0, 1000, now(), now(), now(), now(), 0, 1000, now()) RETURNING id`, club, cashier).Scan(&shift); err != nil {
		t.Fatal(err)
	}
	if res := e.pass(time.Hour); res.Found != 0 {
		t.Fatalf("hourly pass must not read the cash drawer history: %+v", res)
	}
	if res := e.pass(24 * time.Hour); !res.Full || res.Found != 1 || res.Open != 2 {
		t.Fatalf("full pass with a wrong drawer snapshot: %+v", res)
	}
	if n := e.scalar(`SELECT count(*) FROM ledger_reconcile_findings WHERE club_id = $1 AND kind = 'cash_drawer_snapshot'
		AND subject_id = $2 AND fingerprint = 'diff=1000'`, club, shift); n != 1 {
		t.Fatal("cash drawer finding")
	}
	// Снимок исправили — находка закрывается полным проходом.
	e.bypass("UPDATE cash_shifts SET drawer_snapshot_balance = 0 WHERE id = $1", shift)
	if res := e.pass(24 * time.Hour); res.Resolved != 1 || res.Open != 1 {
		t.Fatalf("full pass after the snapshot fix: %+v", res)
	}

	// Предел: испорчено 5 балансов, за проход записывается не больше MaxFindings (2) — остальные позже.
	limited := e.d.Club(t, "inv-limit")
	for i := 0; i < 5; i++ {
		u := e.d.Customer(t, limited, "Клиент")
		e.tamper(limited, u, int64(10+i), 0, true)
	}
	e.w.MaxFindings = 2
	before := e.logs("ledger.drift_found")
	if res := e.pass(time.Hour); res.Found != 2 {
		t.Fatalf("limited pass: %+v", res)
	}
	if got := e.scalar("SELECT count(*) FROM ledger_reconcile_findings WHERE club_id = $1 AND resolved_at IS NULL", limited); got != 2 || e.logs("ledger.drift_found") != before+2 {
		t.Fatalf("limited findings: %d", got)
	}
}

// Один исполнитель на кластер: пока лок у другого инстанса, тик ничего не делает; из одновременных тиков проход
// выполняет один, и расхождение пишется в журнал один раз.
func TestLedgerReconcileSingleRunner(t *testing.T) {
	e := newReconcileEnv(t)
	club := e.d.Club(t, "leader")
	user := e.d.Customer(t, club, "Клиент")
	e.balance(club, user, 10_000, 0)
	e.tamper(club, user, 77, 0, true)

	lock, err := leader.TryAcquire(e.ctx, e.d.Platform, worker.LedgerReconcileJob)
	if err != nil || lock == nil {
		t.Fatalf("acquire: %v", err)
	}
	res, err := e.w.Tick(e.ctx)
	if err != nil || res.Ran || res.Found != 0 {
		t.Fatalf("tick under a foreign lock: %+v err=%v", res, err)
	}
	if e.open() != 0 || e.scalar("SELECT count(*) FROM ledger_reconcile_state WHERE last_started_at IS NOT NULL") != 0 {
		t.Fatal("reconcile ran without the lock")
	}
	lock.Release()

	const instances = 6
	results := make([]worker.LedgerReconcileResult, instances)
	errs := make([]error, instances)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := &worker.LedgerReconcile{Platform: e.d.Platform, App: e.d.App, Now: e.clock}
			results[i], errs[i] = w.Tick(e.ctx)
		}()
	}
	wg.Wait()
	passes := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("instance %d: %v", i, errs[i])
		}
		if r.Ran && !r.Skipped {
			passes++
		}
	}
	if passes != 1 {
		t.Fatalf("passes = %d, want 1: %+v", passes, results)
	}
	if e.logs("ledger.drift_found") != 1 || e.open() != 1 {
		t.Fatalf("drift logged %d times by %d instances", e.logs("ledger.drift_found"), instances)
	}
}
