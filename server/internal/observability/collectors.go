package observability

import (
	"context"
	"crypto/x509"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
)

// Sources — источники показателей, читаемых при сборе (scrape). Все поля необязательны.
type Sources struct {
	// Pools — пулы pgx по имени метки (app, platform).
	Pools map[string]*pgxpool.Pool
	// Platform — пул zafiro_platform для запросов очередей (outbox, вебхуки) и pg_locks.
	Platform *pgxpool.Pool
	// LocalStreams — открытые потоки агентов на этом инстансе (stream.Service.LocalStreams).
	LocalStreams func() int
	// OnlineComputers — ПК на связи по всей платформе (агрегат, без разбивки по клубам).
	OnlineComputers func(ctx context.Context) (int64, error)
	// NATS — соединение NATS (состояние и число переподключений).
	NATS *nats.Conn
	// Certificates — сертификаты, чей срок нужно отслеживать: метка cert → сертификат.
	Certificates map[string]*x509.Certificate
}

// SetSources задаёт источники (вызывается один раз после сборки App).
func (o *Observer) SetSources(s Sources) {
	if o == nil {
		return
	}
	o.srcMu.Lock()
	defer o.srcMu.Unlock()
	o.src = s
}

func (o *Observer) sources() Sources {
	o.srcMu.Lock()
	defer o.srcMu.Unlock()
	return o.src
}

// Кэш запросов к базе и Valkey: сбор Prometheus каждые 15 с с каждого инстанса не должен множить запросы.
const (
	dbCacheTTL     = 10 * time.Second
	collectTimeout = 3 * time.Second
)

type dbCache struct {
	mu   sync.Mutex
	at   time.Time
	vals dbValues
}

type dbValues struct {
	outboxOK                                       bool
	outboxPending, outboxOldest                    float64
	webhookOK                                      bool
	webhookPending, webhookRetrying, webhookOldest float64
	onlineOK                                       bool
	online                                         float64
	leaders                                        map[string]float64
}

var (
	descPoolAcquired  = desc("db_pool_acquired_connections", "Занятые соединения пула.", "pool")
	descPoolIdle      = desc("db_pool_idle_connections", "Свободные соединения пула.", "pool")
	descPoolTotal     = desc("db_pool_total_connections", "Все соединения пула.", "pool")
	descPoolMax       = desc("db_pool_max_connections", "Предел соединений пула.", "pool")
	descPoolWaits     = desc("db_pool_acquire_waits_total", "Захваты соединения, которым пришлось ждать (пул был пуст).", "pool")
	descPoolWaitTime  = desc("db_pool_acquire_wait_seconds_total", "Суммарное время ожидания соединения в пустом пуле.", "pool")
	descPoolCanceled  = desc("db_pool_acquire_canceled_total", "Захваты соединения, отменённые до получения (таймаут запроса).", "pool")
	descStreams       = desc("agent_streams_active", "Открытые потоки Connect агентов на этом инстансе.")
	descOnline        = desc("agent_online_computers", "ПК на связи по всей платформе (присутствие в Valkey; агрегат).")
	descOutboxPending = desc("outbox_pending_events", "Неопубликованные события outbox (все клубы).")
	descOutboxOldest  = desc("outbox_oldest_unpublished_age_seconds", "Возраст самого старого неопубликованного события outbox.")
	descWebhookPend   = desc("webhook_deliveries_pending", "Доставки вебхуков в очереди (status=pending).")
	descWebhookRetry  = desc("webhook_deliveries_retrying", "Доставки в очереди после неудачной попытки (attempts > 0).")
	descWebhookOldest = desc("webhook_oldest_pending_age_seconds", "Возраст самой старой доставки вебхука в очереди.")
	descLeader        = desc("worker_leader", "Этот инстанс держит advisory lock задачи (лидер постоянного воркера).", "job")
	descNATSConnected = desc("nats_connected", "Соединение NATS установлено (1) или нет (0).")
	descNATSReconn    = desc("nats_reconnects_total", "Переподключения NATS с запуска.")
	descCertExpiry    = desc("tls_certificate_expiry_timestamp_seconds",
		"Срок действия сертификата (Unix): agent_ca — CA агентов, agent_server — серверный TLS gRPC.", "cert")
)

func desc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(namespace+"_"+name, help, labels, nil)
}

// sourceCollector читает Sources при сборе.
type sourceCollector struct{ o *Observer }

func (c *sourceCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descPoolAcquired, descPoolIdle, descPoolTotal, descPoolMax, descPoolWaits,
		descPoolWaitTime, descPoolCanceled, descStreams, descOnline, descOutboxPending, descOutboxOldest, descWebhookPend,
		descWebhookRetry, descWebhookOldest, descLeader, descNATSConnected, descNATSReconn, descCertExpiry} {
		ch <- d
	}
}

func (c *sourceCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.o.sources()
	for name, p := range s.Pools {
		if p == nil {
			continue
		}
		st := p.Stat()
		ch <- prometheus.MustNewConstMetric(descPoolAcquired, prometheus.GaugeValue, float64(st.AcquiredConns()), name)
		ch <- prometheus.MustNewConstMetric(descPoolIdle, prometheus.GaugeValue, float64(st.IdleConns()), name)
		ch <- prometheus.MustNewConstMetric(descPoolTotal, prometheus.GaugeValue, float64(st.TotalConns()), name)
		ch <- prometheus.MustNewConstMetric(descPoolMax, prometheus.GaugeValue, float64(st.MaxConns()), name)
		ch <- prometheus.MustNewConstMetric(descPoolWaits, prometheus.CounterValue, float64(st.EmptyAcquireCount()), name)
		ch <- prometheus.MustNewConstMetric(descPoolWaitTime, prometheus.CounterValue, st.EmptyAcquireWaitTime().Seconds(), name)
		ch <- prometheus.MustNewConstMetric(descPoolCanceled, prometheus.CounterValue, float64(st.CanceledAcquireCount()), name)
	}
	if s.LocalStreams != nil {
		ch <- prometheus.MustNewConstMetric(descStreams, prometheus.GaugeValue, float64(s.LocalStreams()))
	}
	if s.NATS != nil {
		connected := 0.0
		if s.NATS.IsConnected() {
			connected = 1
		}
		ch <- prometheus.MustNewConstMetric(descNATSConnected, prometheus.GaugeValue, connected)
		ch <- prometheus.MustNewConstMetric(descNATSReconn, prometheus.CounterValue, float64(s.NATS.Stats().Reconnects))
	}
	for name, cert := range s.Certificates {
		if cert != nil {
			ch <- prometheus.MustNewConstMetric(descCertExpiry, prometheus.GaugeValue, float64(cert.NotAfter.Unix()), name)
		}
	}

	v := c.o.cached(s)
	if v.onlineOK {
		ch <- prometheus.MustNewConstMetric(descOnline, prometheus.GaugeValue, v.online)
	}
	if v.outboxOK {
		ch <- prometheus.MustNewConstMetric(descOutboxPending, prometheus.GaugeValue, v.outboxPending)
		ch <- prometheus.MustNewConstMetric(descOutboxOldest, prometheus.GaugeValue, v.outboxOldest)
	}
	if v.webhookOK {
		ch <- prometheus.MustNewConstMetric(descWebhookPend, prometheus.GaugeValue, v.webhookPending)
		ch <- prometheus.MustNewConstMetric(descWebhookRetry, prometheus.GaugeValue, v.webhookRetrying)
		ch <- prometheus.MustNewConstMetric(descWebhookOldest, prometheus.GaugeValue, v.webhookOldest)
	}
	for job, held := range v.leaders {
		ch <- prometheus.MustNewConstMetric(descLeader, prometheus.GaugeValue, held, job)
	}
}

// Запросы при сборе — платформенный пул (все клубы, только агрегаты). Индексы: outbox_events_unpublished_index,
// webhook_deliveries_status_next_attempt_at_index. Счёт outbox — как store.CountUnpublishedOutboxEvents.
const (
	sqlOutboxLag = `SELECT count(*)::float8, COALESCE(EXTRACT(EPOCH FROM now() - min(created_at)), 0)::float8
FROM outbox_events WHERE published_at IS NULL`
	sqlWebhookQueue = `SELECT count(*)::float8, (count(*) FILTER (WHERE attempts > 0))::float8,
       COALESCE(EXTRACT(EPOCH FROM now() - min(created_at)), 0)::float8
FROM webhook_deliveries WHERE status = 'pending'`
	// Лок задачи: pg_try_advisory_lock(hashtext('job:'||имя)) — ключ bigint из int4 (знаковое расширение):
	// classid — старшие 32 бита, objid — младшие, objsubid = 1.
	sqlLeaders = `SELECT DISTINCT t.job
FROM unnest($1::text[], $2::int4[]) AS t(job, pid)
JOIN pg_locks l ON l.locktype = 'advisory' AND l.granted AND l.pid = t.pid AND l.objsubid = 1
 AND l.objid = ((hashtext('job:' || t.job)::bigint) & 4294967295)::oid
 AND l.classid = (((hashtext('job:' || t.job)::bigint) >> 32) & 4294967295)::oid`
)

// cached — значения из базы и Valkey не чаще раза в dbCacheTTL. Ошибка источника — показатель не выдаётся
// (Prometheus видит пропуск, алерт absent/устаревание), счётчик observability_collect_errors_total растёт.
func (o *Observer) cached(s Sources) dbValues {
	o.cache.mu.Lock()
	defer o.cache.mu.Unlock()
	if !o.cache.at.IsZero() && time.Since(o.cache.at) < dbCacheTTL {
		return o.cache.vals
	}
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	var v dbValues
	if s.OnlineComputers != nil {
		if n, err := s.OnlineComputers(ctx); err == nil {
			v.onlineOK, v.online = true, float64(n)
		} else {
			o.m.collectErr.WithLabelValues("presence").Inc()
		}
	}
	if s.Platform != nil {
		if err := s.Platform.QueryRow(ctx, sqlOutboxLag).Scan(&v.outboxPending, &v.outboxOldest); err == nil {
			v.outboxOK = true
		} else {
			o.m.collectErr.WithLabelValues("outbox").Inc()
		}
		if err := s.Platform.QueryRow(ctx, sqlWebhookQueue).Scan(&v.webhookPending, &v.webhookRetrying, &v.webhookOldest); err == nil {
			v.webhookOK = true
		} else {
			o.m.collectErr.WithLabelValues("webhooks").Inc()
		}
		v.leaders = o.collectLeaders(ctx, s.Platform)
	}
	o.cache.at, o.cache.vals = time.Now(), v
	return v
}

func (o *Observer) collectLeaders(ctx context.Context, pool *pgxpool.Pool) map[string]float64 {
	jobs, pids, known := o.leaders.snapshot()
	if len(known) == 0 {
		return nil
	}
	out := make(map[string]float64, len(known))
	for _, j := range known {
		out[j] = 0
	}
	if len(jobs) == 0 {
		return out
	}
	rows, err := pool.Query(ctx, sqlLeaders, jobs, pids)
	if err != nil {
		o.m.collectErr.WithLabelValues("leader").Inc()
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var job string
		if rows.Scan(&job) == nil {
			out[job] = 1
		}
	}
	if rows.Err() != nil {
		o.m.collectErr.WithLabelValues("leader").Inc()
		return nil
	}
	return out
}

// TrackWorker оборачивает горутину фоновой задачи: worker_up = 1, пока она работает; ошибка завершения (кроме
// остановки) — worker_errors_total.
func (o *Observer) TrackWorker(name string, run func(ctx context.Context) error) func(ctx context.Context) error {
	if o == nil {
		return run
	}
	label := token(o.g.job, name)
	return func(ctx context.Context) error {
		up := o.m.workerUp.WithLabelValues(label)
		up.Set(1)
		defer up.Set(0)
		err := run(ctx)
		if err != nil && ctx.Err() == nil {
			o.m.workerErrors.WithLabelValues(label).Inc()
		}
		return err
	}
}
