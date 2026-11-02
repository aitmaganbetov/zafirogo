package observability

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/valkey-io/valkey-go"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
)

// ---------------------------------------------------------------------------
// Valkey
// ---------------------------------------------------------------------------

// WrapValkey — клиент Valkey со счётчиками команд и ошибок (Do и DoMulti; остальные методы — как есть).
// «Нет ключа» (nil) и NOSCRIPT (повтор EVAL после EVALSHA внутри valkey.Lua) ошибками не считаются.
func (o *Observer) WrapValkey(c valkey.Client) valkey.Client {
	if o == nil || c == nil {
		return c
	}
	return &valkeyClient{Client: c, o: o}
}

type valkeyClient struct {
	valkey.Client
	o *Observer
}

func (c *valkeyClient) Do(ctx context.Context, cmd valkey.Completed) valkey.ValkeyResult {
	r := c.Client.Do(ctx, cmd)
	c.o.m.valkeyCommands.Inc()
	c.o.valkeyErr(r.Error())
	return r
}

func (c *valkeyClient) DoMulti(ctx context.Context, multi ...valkey.Completed) []valkey.ValkeyResult {
	rs := c.Client.DoMulti(ctx, multi...)
	c.o.m.valkeyCommands.Add(float64(len(multi)))
	for _, r := range rs {
		c.o.valkeyErr(r.Error())
	}
	return rs
}

func (o *Observer) valkeyErr(err error) {
	if err == nil || valkey.IsValkeyNil(err) {
		return
	}
	if ve, ok := valkey.IsValkeyErr(err); ok {
		if ve.IsNoScript() {
			return
		}
		o.m.valkeyErrors.WithLabelValues("server").Inc()
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		o.m.valkeyErrors.WithLabelValues("timeout").Inc()
		return
	}
	o.m.valkeyErrors.WithLabelValues("network").Inc()
}

// ---------------------------------------------------------------------------
// NATS
// ---------------------------------------------------------------------------

// NATSOptions — обработчики ошибок соединения NATS (async-ошибки и обрывы — nats_errors_total). Обработчики
// журнала app (DisconnectErrHandler, ReconnectHandler) задаются отдельно — эти идут после них и заменяют
// только ErrorHandler.
func (o *Observer) NATSOptions() []nats.Option {
	if o == nil {
		return nil
	}
	return []nats.Option{
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			if err != nil {
				o.m.natsErrors.WithLabelValues("async").Inc()
				o.log.Warn("nats async error", "err", err)
			}
		}),
	}
}

// NATSDisconnected — счётчик обрыва (вызывать из DisconnectErrHandler).
func (o *Observer) NATSDisconnected() {
	if o != nil {
		o.m.natsErrors.WithLabelValues("disconnect").Inc()
	}
}

// WrapJetStream — JetStream со спанами публикации (outbox) и обработки сообщений потребителями (вебхуки,
// WebSocket админки), счётчиками отказов публикации и обработанных сообщений. Спаны — корневые: публикатор
// outbox работает в фоне, связи с запросом, записавшим событие, нет (контекст трассы в событие не пишется).
func (o *Observer) WrapJetStream(js jetstream.JetStream) jetstream.JetStream {
	if o == nil || js == nil {
		return js
	}
	return &tracedJS{JetStream: js, o: o}
}

type tracedJS struct {
	jetstream.JetStream
	o *Observer
}

func (j *tracedJS) publishSpan(err error) {
	_, span := j.o.tracer.Start(context.Background(), "nats publish", trace.WithSpanKind(trace.SpanKindProducer))
	if err != nil {
		j.o.m.natsErrors.WithLabelValues("publish").Inc()
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, "publish failed")
	}
	span.End()
}

func (j *tracedJS) PublishMsgAsync(m *nats.Msg, opts ...jetstream.PublishOpt) (jetstream.PubAckFuture, error) {
	f, err := j.JetStream.PublishMsgAsync(m, opts...)
	j.publishSpan(err)
	return f, err
}

func (j *tracedJS) PublishMsg(ctx context.Context, m *nats.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	a, err := j.JetStream.PublishMsg(ctx, m, opts...)
	j.publishSpan(err)
	return a, err
}

func (j *tracedJS) CreateOrUpdateConsumer(ctx context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	c, err := j.JetStream.CreateOrUpdateConsumer(ctx, stream, cfg)
	if err != nil {
		return c, err
	}
	name := cfg.Durable
	if name == "" {
		name = cfg.Name
	}
	return &tracedConsumer{Consumer: c, o: j.o, name: token(j.o.g.consumer, name)}, nil
}

func (j *tracedJS) OrderedConsumer(ctx context.Context, stream string, cfg jetstream.OrderedConsumerConfig) (jetstream.Consumer, error) {
	c, err := j.JetStream.OrderedConsumer(ctx, stream, cfg)
	if err != nil {
		return c, err
	}
	return &tracedConsumer{Consumer: c, o: j.o, name: "ordered"}, nil
}

type tracedConsumer struct {
	jetstream.Consumer
	o    *Observer
	name string
}

func (c *tracedConsumer) Consume(handler jetstream.MessageHandler, opts ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	counter := c.o.m.natsConsumed.WithLabelValues(c.name)
	return c.Consumer.Consume(func(msg jetstream.Msg) {
		_, span := c.o.tracer.Start(context.Background(), "nats consume "+c.name, trace.WithSpanKind(trace.SpanKindConsumer))
		defer span.End()
		counter.Inc()
		handler(msg)
	}, opts...)
}

// ---------------------------------------------------------------------------
// Вход в панель
// ---------------------------------------------------------------------------

// WrapAuthEvents — события входа со счётчиком auth_logins_total (успех/отказ по причине). Телефон в метки не
// попадает.
func (o *Observer) WrapAuthEvents(e auth.Events) auth.Events {
	if o == nil || e == nil {
		return e
	}
	return &authEvents{Events: e, o: o}
}

type authEvents struct {
	auth.Events
	o *Observer
}

func (e *authEvents) LoginSucceeded(ctx context.Context, userID, clubID int64, userName string) {
	e.o.m.authLogins.WithLabelValues("succeeded", "").Inc()
	e.Events.LoginSucceeded(ctx, userID, clubID, userName)
}

func (e *authEvents) LoginFailed(ctx context.Context, phone, reason string) {
	e.o.m.authLogins.WithLabelValues("failed", token(e.o.g.reason, reason)).Inc()
	e.Events.LoginFailed(ctx, phone, reason)
}
