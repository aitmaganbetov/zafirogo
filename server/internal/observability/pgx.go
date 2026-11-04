package observability

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
)

// Имена запросов sqlc (db/queries), по которым трассировщик считает события предметной области. Так метрики
// ledger, команд агентам, вебхуков и outbox получаются без правок этих пакетов: каждый запрос sqlc начинается
// с «-- name: Имя». Позиции аргументов проверяет pgx_test.go на сгенерированном коде store (переименование
// запроса или перестановка колонок ломает тест, а не метрику).
const (
	qInsertLedgerTransaction  = "InsertLedgerTransaction"  // $3 — kind
	qInsertAgentCommand       = "InsertAgentCommand"       //
	qListPendingAgentCommands = "ListPendingAgentCommands" // строки — переотправка после переподключения
	qAcceptAgentCommand       = "AcceptAgentCommand"       //
	qCompleteAgentCommand     = "CompleteAgentCommand"     // $1 — status
	qExpireAgentCommands      = "ExpireAgentCommands"      //
	qSaveWebhookAttempt       = "SaveWebhookAttempt"       // $1 — status
	qSkipWebhookDelivery      = "SkipWebhookDelivery"      // #nosec G101 -- имя запроса sqlc, не секрет
	qMarkOutboxAttemptFailed  = "MarkOutboxAttemptFailed"  //
	qMarkOutboxPublished      = "MarkOutboxPublished"      // строки — опубликованные события

	qBegin    = "begin"
	qCommit   = "commit"
	qRollback = "rollback"
	qLock     = "advisory_lock"
	qUnlock   = "advisory_unlock"
	qRaw      = "raw"
)

// Индексы аргументов (от нуля) в сгенерированном коде store.
const (
	argLedgerKind     = 2
	argCommandStatus  = 0
	argWebhookStatus  = 0
	argAdvisoryJobKey = 0
)

var sqlcNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// queryName — короткое имя запроса для метки: имя sqlc, begin/commit/rollback, захват/снятие advisory lock
// задачи (worker/leader), иначе raw. Текст запроса в метку не попадает никогда.
func queryName(sql string) string {
	s := strings.TrimLeft(sql, " \t\r\n")
	if rest, ok := strings.CutPrefix(s, "-- name: "); ok {
		name, _, _ := strings.Cut(rest, " ")
		if sqlcNameRe.MatchString(name) {
			return name
		}
		return qRaw
	}
	word, _, _ := strings.Cut(s, " ")
	switch strings.ToLower(word) {
	case "begin", "start":
		return qBegin
	case "commit", "end":
		return qCommit
	case "rollback", "abort":
		return qRollback
	}
	switch {
	case strings.Contains(s, "pg_try_advisory_lock(hashtext($1))"):
		return qLock
	case strings.Contains(s, "pg_advisory_unlock(hashtext($1))"):
		return qUnlock
	}
	return qRaw
}

// PgxTracer — pgx.QueryTracer пула: метрики запросов, события предметной области (ledger, команды агентам,
// вебхуки, outbox, запуски задач), спаны транзакций и запросов (если родительский спан записывается).
// Ставится в ConnConfig.Tracer; BeforeClose пула должен вызывать Forget (состояние соединения).
type PgxTracer struct {
	o     *Observer
	pool  string
	conns sync.Map // *pgx.Conn → *connState
}

var _ pgx.QueryTracer = (*PgxTracer)(nil)

// NewPgxTracer — трассировщик пула pool (метка: app | platform). nil Observer — nil (без трассировщика).
func (o *Observer) NewPgxTracer(pool string) *PgxTracer {
	if o == nil {
		return nil
	}
	return &PgxTracer{o: o, pool: pool}
}

// connState — состояние одного соединения (соединение pgx не используется конкурентно — мьютекс не нужен).
type connState struct {
	inTx    bool
	ledger  []string // виды операций ledger, записанных в текущей транзакции
	txSpan  trace.Span
	txStart time.Time
	locks   map[string]time.Time // задача → время захвата лока (worker/leader)
}

type queryInfoKey struct{}

type queryInfo struct {
	name  string
	start time.Time
	args  []any
	span  trace.Span
	conn  *pgx.Conn
}

func (t *PgxTracer) state(c *pgx.Conn) *connState {
	if v, ok := t.conns.Load(c); ok {
		st, _ := v.(*connState)
		return st
	}
	v, _ := t.conns.LoadOrStore(c, &connState{})
	st, _ := v.(*connState)
	return st
}

// Forget — соединение закрыто (pgxpool BeforeClose): незавершённая транзакция считается откатом.
func (t *PgxTracer) Forget(c *pgx.Conn) {
	if t == nil {
		return
	}
	v, ok := t.conns.LoadAndDelete(c)
	if !ok {
		return
	}
	if st, ok := v.(*connState); ok {
		t.finishTx(st, false)
	}
	t.o.leaders.forgetPID(pidOf(c))
}

// TraceQueryStart реализует pgx.QueryTracer.
func (t *PgxTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	info := &queryInfo{name: queryName(data.SQL), start: time.Now(), args: data.Args, conn: c}
	st := t.state(c)
	switch info.name {
	case qBegin:
		t.finishTx(st, false) // незакрытая транзакция (не должно быть) — откат
		st.inTx = true
		st.txStart = info.start
		if parent := trace.SpanFromContext(ctx); parent.IsRecording() {
			_, st.txSpan = t.o.tracer.Start(ctx, "db.tx", trace.WithSpanKind(trace.SpanKindClient),
				trace.WithAttributes(attribute.String("db.system", "postgresql"), attribute.String("zafiro.db.pool", t.pool)))
		}
	case qLock:
		if job, ok := jobFromArgs(data.Args); ok {
			if st.locks == nil {
				st.locks = map[string]time.Time{}
			}
			st.locks[job] = info.start
			t.o.leaders.attempt(job, pidOf(c))
		}
	}
	parent := trace.SpanFromContext(ctx)
	if st.txSpan != nil && info.name != qBegin {
		parent = st.txSpan
	}
	if parent.IsRecording() {
		_, info.span = t.o.tracer.Start(trace.ContextWithSpan(ctx, parent), "db "+info.name,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attribute.String("db.system", "postgresql"), attribute.String("db.operation.name", info.name)))
	}
	return context.WithValue(ctx, queryInfoKey{}, info)
}

// TraceQueryEnd реализует pgx.QueryTracer.
func (t *PgxTracer) TraceQueryEnd(ctx context.Context, c *pgx.Conn, data pgx.TraceQueryEndData) {
	info, _ := ctx.Value(queryInfoKey{}).(*queryInfo)
	if info == nil {
		return
	}
	m := t.o.m
	name := t.o.g.query.value(info.name)
	m.dbQueries.WithLabelValues(t.pool, name).Inc()
	m.dbDuration.WithLabelValues(t.pool).Observe(time.Since(info.start).Seconds())
	if data.Err != nil {
		reason, sqlstate := errReason(data.Err)
		m.dbErrors.WithLabelValues(t.pool, name, sqlstate).Inc()
		if strings.Contains(info.name, "Ledger") {
			m.ledgerErrors.WithLabelValues(name, token(t.o.g.reason, reason)).Inc()
		}
	}
	if info.span != nil {
		if data.Err != nil {
			info.span.RecordError(data.Err)
			info.span.SetStatus(otelcodes.Error, "query failed")
		}
		info.span.End()
	}
	t.event(t.state(c), info, data)
}

// event — события предметной области по имени запроса.
func (t *PgxTracer) event(st *connState, info *queryInfo, data pgx.TraceQueryEndData) {
	m := t.o.m
	ok := data.Err == nil
	rows := data.CommandTag.RowsAffected()
	switch info.name {
	case qCommit:
		// COMMIT в прерванной транзакции Postgres отвечает тегом ROLLBACK.
		t.finishTx(st, ok && data.CommandTag.String() == "COMMIT")
	case qRollback:
		t.finishTx(st, false)
	case qInsertLedgerTransaction:
		if !ok {
			return
		}
		kind := labelOther
		if k, isStr := stringArg(info.args, argLedgerKind); isStr && ledger.Kind(k).Valid() {
			kind = k
		}
		if st.inTx {
			st.ledger = append(st.ledger, kind)
		} else {
			m.ledgerOps.WithLabelValues(kind, "committed").Inc()
		}
	case qInsertAgentCommand:
		if ok {
			m.agentCommands.WithLabelValues("issued", "").Inc()
		}
	case qListPendingAgentCommands:
		if ok && rows > 0 {
			m.agentCommands.WithLabelValues("redelivered", "").Add(float64(rows))
		}
	case qAcceptAgentCommand:
		if ok && rows > 0 {
			m.agentCommands.WithLabelValues("accepted", "").Add(float64(rows))
		}
	case qCompleteAgentCommand:
		if ok && rows > 0 {
			res, _ := stringArg(info.args, argCommandStatus)
			m.agentCommands.WithLabelValues("completed", commandResult(res)).Add(float64(rows))
		}
	case qExpireAgentCommands:
		if ok && rows > 0 {
			m.agentCommands.WithLabelValues("expired", "").Add(float64(rows))
		}
	case qSaveWebhookAttempt:
		if ok {
			res, _ := stringArg(info.args, argWebhookStatus)
			m.webhookAttempts.WithLabelValues(webhookResult(res)).Inc()
		}
	case qSkipWebhookDelivery:
		if ok {
			m.webhookAttempts.WithLabelValues("skipped").Inc()
		}
	case qMarkOutboxAttemptFailed:
		if ok {
			m.outboxFailures.Inc()
		}
	case qMarkOutboxPublished:
		if ok && rows > 0 {
			m.natsPublished.Add(float64(rows))
		}
	case qUnlock:
		job, isJob := jobFromArgs(info.args)
		if !isJob {
			return
		}
		t.o.leaders.release(job, pidOf(info.conn))
		started, held := st.locks[job]
		if !held {
			return
		}
		delete(st.locks, job)
		label := token(t.o.g.job, job)
		m.workerRuns.WithLabelValues(label).Inc()
		m.workerDuration.WithLabelValues(label).Observe(time.Since(started).Seconds())
		m.workerLastRun.WithLabelValues(label).Set(float64(time.Now().Unix()))
	}
}

// finishTx закрывает транзакцию соединения: операции ledger — committed или rolled_back, спан транзакции.
func (t *PgxTracer) finishTx(st *connState, committed bool) {
	if !st.inTx {
		return
	}
	result := "rolled_back"
	if committed {
		result = "committed"
	}
	for _, kind := range st.ledger {
		t.o.m.ledgerOps.WithLabelValues(kind, result).Inc()
	}
	if st.txSpan != nil {
		if len(st.ledger) > 0 {
			st.txSpan.SetName("db.tx ledger")
			st.txSpan.SetAttributes(attribute.StringSlice("zafiro.ledger.kinds", st.ledger))
		}
		st.txSpan.SetAttributes(attribute.String("zafiro.db.tx_result", result))
		if !committed {
			st.txSpan.SetStatus(otelcodes.Error, result)
		}
		st.txSpan.End()
	}
	st.inTx, st.ledger, st.txSpan = false, nil, nil
}

// errReason — (имя ограничения или sqlstate, sqlstate) ошибки запроса; ошибка клиента/сети — client.
func errReason(err error) (reason, sqlstate string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.ConstraintName != "" {
			return pgErr.ConstraintName, pgErr.Code
		}
		return pgErr.Code, pgErr.Code
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled", "canceled"
	}
	return "client", "client"
}

func stringArg(args []any, i int) (string, bool) {
	if i >= len(args) {
		return "", false
	}
	switch v := args[i].(type) {
	case string:
		return v, true
	case *string:
		if v != nil {
			return *v, true
		}
	case pgtype.Text:
		return v.String, v.Valid
	case interface{ String() string }:
		return v.String(), true
	}
	return "", false
}

// pidOf — PID серверного процесса соединения (0 — неизвестен).
func pidOf(c *pgx.Conn) uint32 {
	if c == nil || c.PgConn() == nil {
		return 0
	}
	return c.PgConn().PID()
}

// jobFromArgs — имя задачи из ключа advisory lock 'job:<имя>' (worker/leader.Key).
func jobFromArgs(args []any) (string, bool) {
	key, ok := stringArg(args, argAdvisoryJobKey)
	if !ok {
		return "", false
	}
	job, ok := strings.CutPrefix(key, "job:")
	return job, ok && job != ""
}

func commandResult(status string) string {
	switch status {
	case "succeeded", "failed", "duplicate":
		return status
	}
	return labelOther
}

// webhookResult — статус SaveWebhookAttempt: delivered | pending (ошибка, будет повтор) | failed.
func webhookResult(status string) string {
	switch status {
	case "delivered":
		return "delivered"
	case "pending":
		return "retry"
	case "failed":
		return "failed"
	}
	return labelOther
}

// ---------------------------------------------------------------------------
// Кандидаты в лидеры (кто держит лок задачи — сборщик проверяет по pg_locks)
// ---------------------------------------------------------------------------

type leaderCandidates struct {
	mu   sync.Mutex
	jobs map[string]map[uint32]struct{} // задача → PID соединений этого инстанса, пытавшихся взять лок
	seen map[string]struct{}            // задачи, лок которых этот инстанс когда-либо пытался взять
}

func (l *leaderCandidates) init() {
	l.jobs = map[string]map[uint32]struct{}{}
	l.seen = map[string]struct{}{}
}

func (l *leaderCandidates) attempt(job string, pid uint32) {
	if !tokenRe.MatchString(job) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.seen[job]; !ok && len(l.seen) >= 64 {
		return
	}
	l.seen[job] = struct{}{}
	pids := l.jobs[job]
	if pids == nil {
		pids = map[uint32]struct{}{}
		l.jobs[job] = pids
	}
	pids[pid] = struct{}{}
}

func (l *leaderCandidates) release(job string, pid uint32) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.jobs[job], pid)
}

func (l *leaderCandidates) forgetPID(pid uint32) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, pids := range l.jobs {
		delete(pids, pid)
	}
}

// snapshot — пары (задача, PID) для запроса к pg_locks и все известные задачи.
func (l *leaderCandidates) snapshot() (jobs []string, pids []int32, known []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for job, ps := range l.jobs {
		for pid := range ps {
			jobs = append(jobs, job)
			pids = append(pids, int32(pid)) // #nosec G115 -- PID Postgres помещается в int32
		}
	}
	for job := range l.seen {
		known = append(known, job)
	}
	return jobs, pids, known
}
