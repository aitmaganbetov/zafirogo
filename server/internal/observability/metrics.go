package observability

import (
	"regexp"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Пространство имён всех метрик сервера.
const namespace = "zafiro"

// labelOther — значение метки сверх предела различных значений (labelGuard) или не прошедшее проверку.
const labelOther = "other"

var (
	httpBuckets   = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	dbBuckets     = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}
	workerBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60, 300, 900}
)

// metrics — все метрики, которые пишет код (счётчики и гистограммы). Показатели, читаемые при сборе (пулы,
// очереди, присутствие, сертификаты), — в sourceCollector (collectors.go).
type metrics struct {
	httpRequests *prometheus.CounterVec   // method, route, code
	httpDuration *prometheus.HistogramVec // method, route
	httpInFlight prometheus.Gauge

	grpcRequests      *prometheus.CounterVec   // method, code
	grpcDuration      *prometheus.HistogramVec // method (unary)
	grpcStreamsActive *prometheus.GaugeVec     // method
	grpcStreamMsgs    *prometheus.CounterVec   // method

	rateLimited *prometheus.CounterVec // channel, route
	authLogins  *prometheus.CounterVec // result, reason

	dbQueries  *prometheus.CounterVec   // pool, query
	dbErrors   *prometheus.CounterVec   // pool, query, sqlstate
	dbDuration *prometheus.HistogramVec // pool

	ledgerOps    *prometheus.CounterVec // kind, result
	ledgerErrors *prometheus.CounterVec // query, reason

	agentCommands   *prometheus.CounterVec // event, result
	webhookAttempts *prometheus.CounterVec // result
	outboxFailures  prometheus.Counter

	workerRuns     *prometheus.CounterVec   // job
	workerDuration *prometheus.HistogramVec // job
	workerLastRun  *prometheus.GaugeVec     // job
	workerErrors   *prometheus.CounterVec   // job
	workerUp       *prometheus.GaugeVec     // worker

	valkeyCommands prometheus.Counter
	valkeyErrors   *prometheus.CounterVec // kind
	natsErrors     *prometheus.CounterVec // kind
	natsConsumed   *prometheus.CounterVec // consumer, result
	natsPublished  prometheus.Counter

	logDropped *prometheus.CounterVec // level
	collectErr *prometheus.CounterVec // source
}

func newMetrics(reg prometheus.Registerer, instance string) *metrics {
	f := func(c prometheus.Collector) { reg.MustRegister(c) }
	m := &metrics{}

	info := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace, Name: "instance_info",
		Help: "Инстанс сервера (agent.instance_id); значение всегда 1."}, []string{"instance_id"})
	f(info)
	info.WithLabelValues(instance).Set(1)

	m.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "http_requests_total",
		Help: "HTTP-запросы по шаблону маршрута chi (не по пути с id) и коду ответа."}, []string{"method", "route", "code"})
	m.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Name: "http_request_duration_seconds",
		Help: "Длительность HTTP-запросов (без WebSocket /ws).", Buckets: httpBuckets}, []string{"method", "route"})
	m.httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: namespace, Name: "http_requests_in_flight",
		Help: "HTTP-запросы в обработке (включая открытые WebSocket)."})
	f(m.httpRequests)
	f(m.httpDuration)
	f(m.httpInFlight)

	m.grpcRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "grpc_requests_total",
		Help: "Вызовы gRPC (unary и завершённые потоки) по методу и коду."}, []string{"method", "code"})
	m.grpcDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Name: "grpc_request_duration_seconds",
		Help: "Длительность unary-вызовов gRPC.", Buckets: httpBuckets}, []string{"method"})
	m.grpcStreamsActive = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace, Name: "grpc_streams_active",
		Help: "Открытые потоки gRPC на инстансе по методу."}, []string{"method"})
	m.grpcStreamMsgs = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "grpc_stream_messages_received_total",
		Help: "Входящие сообщения потоков gRPC (Status агента и т. п.)."}, []string{"method"})
	f(m.grpcRequests)
	f(m.grpcDuration)
	f(m.grpcStreamsActive)
	f(m.grpcStreamMsgs)

	m.rateLimited = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "rate_limited_total",
		Help: "Отказы по лимиту частоты: HTTP 429 и gRPC RESOURCE_EXHAUSTED."}, []string{"channel", "route"})
	m.authLogins = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "auth_logins_total",
		Help: "Входы в панель: result=succeeded|failed, reason — причина отказа (auth.Reason*)."}, []string{"result", "reason"})
	f(m.rateLimited)
	f(m.authLogins)

	m.dbQueries = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "db_queries_total",
		Help: "Запросы Postgres по имени запроса sqlc (raw — без имени; begin/commit/rollback — транзакции)."}, []string{"pool", "query"})
	m.dbErrors = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "db_query_errors_total",
		Help: "Ошибки запросов Postgres (sqlstate — код ошибки сервера, client — ошибка на стороне клиента/сети)."},
		[]string{"pool", "query", "sqlstate"})
	m.dbDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Name: "db_query_duration_seconds",
		Help: "Длительность запросов Postgres.", Buckets: dbBuckets}, []string{"pool"})
	f(m.dbQueries)
	f(m.dbErrors)
	f(m.dbDuration)

	m.ledgerOps = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "ledger_operations_total",
		Help: "Операции журнала денег (ledger) по виду: committed — транзакция зафиксирована, rolled_back — откат."},
		[]string{"kind", "result"})
	m.ledgerErrors = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "ledger_errors_total",
		Help: "Ошибки запросов журнала денег (Ledger*): reason — имя ограничения (ledger_member_balance_non_negative — " +
			"нехватка средств), иначе sqlstate или client."},
		[]string{"query", "reason"})
	f(m.ledgerOps)
	f(m.ledgerErrors)

	m.agentCommands = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "agent_commands_total",
		Help: "Команды агентам: issued — записана, redelivered — переотправлена после переподключения, accepted, " +
			"completed (result=succeeded|failed|duplicate), expired."}, []string{"event", "result"})
	m.webhookAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "webhook_delivery_attempts_total",
		Help: "Попытки доставки вебхуков: delivered, retry (ошибка, будет повтор), failed (исчерпаны попытки), skipped."},
		[]string{"result"})
	m.outboxFailures = prometheus.NewCounter(prometheus.CounterOpts{Namespace: namespace, Name: "outbox_publish_failures_total",
		Help: "События outbox, публикация которых в JetStream не удалась (будет повтор)."})
	f(m.agentCommands)
	f(m.webhookAttempts)
	f(m.outboxFailures)

	m.workerRuns = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "worker_runs_total",
		Help: "Завершённые запуски периодических задач под advisory lock (этот инстанс был исполнителем)."}, []string{"job"})
	m.workerDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Name: "worker_run_duration_seconds",
		Help: "Длительность запуска периодической задачи (от взятия лока до снятия).", Buckets: workerBuckets}, []string{"job"})
	m.workerLastRun = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace, Name: "worker_last_run_timestamp_seconds",
		Help: "Время (Unix) последнего завершённого запуска задачи на этом инстансе."}, []string{"job"})
	m.workerErrors = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "worker_errors_total",
		Help: "Ошибки фоновых задач (записи журнала уровня warn/error с атрибутом job или worker)."}, []string{"job"})
	m.workerUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace, Name: "worker_up",
		Help: "Горутина фоновой задачи работает (1) или завершилась (0)."}, []string{"worker"})
	f(m.workerRuns)
	f(m.workerDuration)
	f(m.workerLastRun)
	f(m.workerErrors)
	f(m.workerUp)

	m.valkeyCommands = prometheus.NewCounter(prometheus.CounterOpts{Namespace: namespace, Name: "valkey_commands_total",
		Help: "Команды Valkey (Do/DoMulti — по числу команд)."})
	m.valkeyErrors = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "valkey_errors_total",
		Help: "Ошибки Valkey: server — ответ-ошибка, timeout — истёк контекст, network — связь."}, []string{"kind"})
	m.natsErrors = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "nats_errors_total",
		Help: "Ошибки NATS: async — асинхронные ошибки соединения, disconnect — обрывы, publish — отказ публикации."},
		[]string{"kind"})
	m.natsConsumed = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "nats_messages_consumed_total",
		Help: "Сообщения JetStream, обработанные потребителями (consumer — durable-имя или ordered)."},
		[]string{"consumer"})
	m.natsPublished = prometheus.NewCounter(prometheus.CounterOpts{Namespace: namespace, Name: "nats_messages_published_total",
		Help: "Сообщения, отправленные в JetStream (outbox)."})
	f(m.valkeyCommands)
	f(m.valkeyErrors)
	f(m.natsErrors)
	f(m.natsConsumed)
	f(m.natsPublished)

	m.logDropped = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "log_records_dropped_total",
		Help: "Записи журнала, отброшенные выборкой шумных сообщений (только debug/info)."}, []string{"level"})
	m.collectErr = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: "observability_collect_errors_total",
		Help: "Ошибки чтения показателей при сборе (outbox, webhooks, presence, leader)."}, []string{"source"})
	f(m.logDropped)
	f(m.collectErr)
	return m
}

// ---------------------------------------------------------------------------
// Ограничение кардинальности
// ---------------------------------------------------------------------------

// labelGuard пропускает не больше max различных значений метки; остальные — labelOther. Чтение без блокировки.
type labelGuard struct {
	max  int
	seen sync.Map
	mu   sync.Mutex
	n    int
}

func (g *labelGuard) value(v string) string {
	if _, ok := g.seen.Load(v); ok {
		return v
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.seen.Load(v); ok {
		return v
	}
	if g.n >= g.max {
		return labelOther
	}
	g.n++
	g.seen.Store(v, struct{}{})
	return v
}

type guards struct {
	route, method, query, job, reason, consumer *labelGuard
}

func newGuards() guards {
	return guards{
		route:    &labelGuard{max: 1000},
		method:   &labelGuard{max: 200},
		query:    &labelGuard{max: 2000},
		job:      &labelGuard{max: 64},
		reason:   &labelGuard{max: 32},
		consumer: &labelGuard{max: 32},
	}
}

// tokenRe — допустимое значение «имени из кода» (имя задачи, причина, имя потребителя): короткое, без пробелов.
var tokenRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9:_.-]{0,63}$`)

func token(g *labelGuard, v string) string {
	if !tokenRe.MatchString(v) {
		return labelOther
	}
	return g.value(v)
}
