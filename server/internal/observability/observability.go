// Package observability — эксплуатационная наблюдаемость сервера (Этап 6, блок O3): метрики Prometheus на
// отдельном внутреннем порту (observability.metrics_listen), трассировка OpenTelemetry с OTLP-экспортёром
// (observability.otlp_endpoint, по умолчанию выключена), обогащение и защита журналов slog (request_id, trace_id,
// маскирование секретов, выборка шумных записей).
//
// Отличие от internal/monitoring: тот переносит мониторинг старого проекта (экран «Сервер» суперадминки, запись
// в request_metrics, алерты Telegram, PD-10) и не меняется; этот пакет — стандартный стек для дежурных
// (Prometheus, Alertmanager, Grafana; deploy/observability) и ничего не пишет в базу.
//
// Правило меток (docs/observability.md §1): только конечные наборы значений, заданные кодом, — шаблон маршрута chi,
// метод gRPC, имя запроса sqlc, вид операции ledger, имя фоновой задачи, код ответа. Никаких club_id, user_id,
// номеров ПК, телефонов, путей с id и сумм. Каждая метка проходит labelGuard: после предела различных значений
// новые склеиваются в "other" — ошибка в коде не превратится во взрыв числа рядов.
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/trace"
)

// Options — параметры наблюдаемости (config.Observability).
type Options struct {
	// Instance — id инстанса (agent.instance_id): ресурс трасс и zafiro_instance_info.
	Instance string
	// OTLPEndpoint — адрес OTLP/HTTP коллектора трасс (http(s)://host:4318). Пусто — трассировка выключена
	// (span-контексты пустые, накладных расходов нет).
	OTLPEndpoint string
	// SampleRatio — доля трасс, начинаемых этим инстансом (0..1); у дочерних спанов — решение родителя.
	SampleRatio float64
	// Log — журнал ошибок экспорта и сбора (nil — slog.Default()).
	Log *slog.Logger
}

// Observer — метрики, трассировка и хуки одного процесса. Нулевой (nil) Observer допустим везде: все методы
// становятся пустыми (роутеры и серверы в тестах собираются без него).
type Observer struct {
	reg    *prometheus.Registry
	m      *metrics
	tracer trace.Tracer
	stopTP func(context.Context) error
	log    *slog.Logger

	g guards

	// Состояние для сборщиков (SetSources) и кандидатов в лидеры (pgx-трассировщик).
	srcMu   sync.Mutex
	src     Sources
	cache   dbCache
	leaders leaderCandidates
}

// New создаёт реестр метрик (свой, не глобальный: в тестах в одном процессе несколько App) и, если задан
// OTLPEndpoint, провайдер трасс. ctx — только для создания экспортёра.
func New(ctx context.Context, o Options) (*Observer, error) {
	if o.SampleRatio < 0 || o.SampleRatio > 1 {
		return nil, errors.New("observability: sample ratio must be within 0..1")
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	obs := &Observer{reg: reg, log: log, g: newGuards()}
	obs.m = newMetrics(reg, o.Instance)
	reg.MustRegister(&sourceCollector{o: obs})

	tracer, stop, err := newTracing(ctx, o)
	if err != nil {
		return nil, err
	}
	obs.tracer, obs.stopTP = tracer, stop
	obs.leaders.init()
	return obs, nil
}

// Registry — реестр метрик (тесты, дополнительные сборщики).
func (o *Observer) Registry() *prometheus.Registry {
	if o == nil {
		return nil
	}
	return o.reg
}

// Tracer — трассировщик (noop, если трассировка выключена).
func (o *Observer) Tracer() trace.Tracer {
	if o == nil {
		return noopTracer
	}
	return o.tracer
}

// Handler — обработчик /metrics: только GET, не больше 4 одновременных сборов, таймаут 10 с.
func (o *Observer) Handler() http.Handler {
	h := promhttp.InstrumentMetricHandler(o.reg, promhttp.HandlerFor(o.reg, promhttp.HandlerOpts{
		ErrorLog:            slog.NewLogLogger(o.log.Handler(), slog.LevelWarn),
		ErrorHandling:       promhttp.ContinueOnError,
		MaxRequestsInFlight: 4,
		Timeout:             10 * time.Second,
	}))
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", h)
	return mux
}

// NewServer — HTTP-сервер внутреннего порта метрик (только /metrics; не через публичный роутер: там нет
// аутентификации, и порт не должен быть доступен из интернета — docs/observability.md §2).
func (o *Observer) NewServer() *http.Server {
	return &http.Server{
		Handler:           o.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(o.log.Handler(), slog.LevelWarn),
	}
}

// Serve обслуживает lis до srv.Close/Shutdown (http.ErrServerClosed — не ошибка).
func Serve(srv *http.Server, lis net.Listener) error {
	if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown выгружает накопленные спаны (не дольше ctx).
func (o *Observer) Shutdown(ctx context.Context) error {
	if o == nil || o.stopTP == nil {
		return nil
	}
	return o.stopTP(ctx)
}

// ---------------------------------------------------------------------------
// Request id
// ---------------------------------------------------------------------------

type requestIDKey struct{}

// WithRequestID кладёт id запроса в контекст (его добавляет в каждую запись журнала LogHandler).
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID — id запроса из контекста ("" — нет).
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// NewRequestID — случайный id (16 hex-символов) для вызовов gRPC.
func NewRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
