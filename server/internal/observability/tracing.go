package observability

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// tracerName — имя инструментирующей библиотеки в спанах.
const tracerName = "github.com/aitmaganbetov/zafirogo/server/internal/observability"

var noopTracer = noop.NewTracerProvider().Tracer(tracerName)

// newTracing — провайдер трасс. Пустой endpoint — noop (трассировка выключена). Глобальный провайдер и
// пропагатор не ставятся: входящие заголовки traceparent от клиентов не принимаются (публичный край; агенты и
// браузер их не шлют), исходящие (вебхуки) — не пропагируются (решение O3, docs/observability.md §4).
func newTracing(ctx context.Context, o Options) (trace.Tracer, func(context.Context) error, error) {
	if o.OTLPEndpoint == "" {
		return noopTracer, nil, nil
	}
	u, err := url.Parse(o.OTLPEndpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, nil, fmt.Errorf("observability.otlp_endpoint: ожидается http(s)://host:port")
	}
	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(o.OTLPEndpoint),
		otlptracehttp.WithTimeout(5*time.Second),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("observability: otlp exporter: %w", err)
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", "zafiro"),
		attribute.String("service.instance.id", o.Instance),
	)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithMaxQueueSize(4096), sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(o.SampleRatio))),
		sdktrace.WithResource(res),
	)
	return tp.Tracer(tracerName), tp.Shutdown, nil
}
