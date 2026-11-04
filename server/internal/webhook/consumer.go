package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ConsumerName — durable consumer JetStream (общий для всех инстансов: каждое событие обрабатывает один).
const ConsumerName = "webhooks"

// deliveryNamespace — пространство имён uuid v5 для id доставок.
var deliveryNamespace = uuid.MustParse("5b0c5c8e-6f1f-4f4e-9a57-7a1f0e9b2c11")

// DeliveryID — id доставки события eventID на endpoint endpointID (payload.id, X-Zafiro-Delivery).
func DeliveryID(eventID string, endpointID int64) uuid.UUID {
	return uuid.NewSHA1(deliveryNamespace, []byte(eventID+":"+strconv.FormatInt(endpointID, 10)))
}

// ConsumerOptions — параметры потребителя событий.
type ConsumerOptions struct {
	// JS — JetStream.
	JS jetstream.JetStream
	// Stream — поток (пусто — outbox.StreamName).
	Stream string
	// App — пул логина zafiro_app: доставки пишутся в транзакции клуба события (RLS).
	App *pgxpool.Pool
	// Events — типы-вебхуки (nil — DefaultEvents).
	Events []string
	// Wake — вызывается после создания доставок (Deliverer.Wake), чтобы отправить сразу, а не по опросу.
	Wake func()
	// RetryDelay — пауза перед повторной обработкой сообщения после ошибки (0 — 5 с).
	RetryDelay time.Duration
	// Log — журнал (nil — slog.Default()).
	Log *slog.Logger
	// Now — часы (nil — time.Now).
	Now func() time.Time
}

// Consumer превращает события JetStream в строки webhook_deliveries.
// Laravel: WebhookDispatcher::dispatch().
type Consumer struct{ o ConsumerOptions }

// NewConsumer проверяет параметры.
func NewConsumer(o ConsumerOptions) (*Consumer, error) {
	if o.JS == nil || o.App == nil {
		return nil, errors.New("webhook: consumer needs JS and App pool")
	}
	if o.Stream == "" {
		o.Stream = outbox.StreamName
	}
	if o.Events == nil {
		o.Events = DefaultEvents
	}
	if o.RetryDelay <= 0 {
		o.RetryDelay = 5 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Consumer{o: o}, nil
}

// Run создаёт (обновляет) durable consumer и обрабатывает сообщения до отмены ctx.
func (c *Consumer) Run(ctx context.Context) error {
	filters := make([]string, len(c.o.Events))
	for i, e := range c.o.Events {
		filters[i] = outbox.SubjectPrefix + "*." + e
	}
	cons, err := c.o.JS.CreateOrUpdateConsumer(ctx, c.o.Stream, jetstream.ConsumerConfig{
		Durable:        ConsumerName,
		Description:    "Вебхуки интеграций: событие → строки webhook_deliveries",
		FilterSubjects: filters,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        30 * time.Second,
		MaxDeliver:     -1,
		MaxAckPending:  1000,
	})
	if err != nil {
		return fmt.Errorf("webhook: consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) { c.handle(ctx, msg) })
	if err != nil {
		return fmt.Errorf("webhook: consume: %w", err)
	}
	<-ctx.Done()
	cc.Drain()
	<-cc.Closed()
	return nil
}

func (c *Consumer) handle(ctx context.Context, msg jetstream.Msg) {
	env, err := outbox.DecodeEnvelope(msg.Data())
	if err != nil {
		c.o.Log.Error("webhook: bad event, dropped", "subject", msg.Subject(), "err", err)
		_ = msg.Term()
		return
	}
	n, err := c.Enqueue(ctx, env)
	if errors.Is(err, errBadPayload) {
		c.o.Log.Error("webhook: event data cannot be sent, dropped", "event", env.Type, "event_id", env.ID, "err", err)
		_ = msg.Term()
		return
	}
	if err != nil {
		c.o.Log.Warn("webhook: enqueue failed, will retry", "event", env.Type, "event_id", env.ID, "err", err)
		_ = msg.NakWithDelay(c.o.RetryDelay)
		return
	}
	if err := msg.Ack(); err != nil {
		// Повторная доставка сообщения безопасна: доставки не задвоятся (UNIQUE event_id).
		c.o.Log.Warn("webhook: ack failed", "event_id", env.ID, "err", err)
	}
	if n > 0 && c.o.Wake != nil {
		c.o.Wake()
	}
}

// payload — тело вебхука, как в WebhookDispatcher::dispatch(): id, event, created_at, club_id, data.
type payload struct {
	ID        string          `json:"id"`
	Event     string          `json:"event"`
	CreatedAt string          `json:"created_at"`
	ClubID    int64           `json:"club_id"`
	Data      json.RawMessage `json:"data"`
}

// iso8601 — Carbon::toIso8601String() (часовой пояс приложения UTC → "+00:00").
const iso8601 = "2006-01-02T15:04:05-07:00"

// Enqueue создаёт доставки события для всех активных endpoint'ов клуба, подписанных на его тип, и
// возвращает число новых доставок.
func (c *Consumer) Enqueue(ctx context.Context, env outbox.Envelope) (int, error) {
	// Решение RQ-3 (docs/DECISIONS.md): суммы — строки "N.NN" + *_tiyn, без float.
	data, err := normalizeMoney(env.Data)
	if err != nil {
		return 0, err
	}
	created := 0
	err = db.InTenantTx(ctx, c.o.App, env.ClubID, func(q *store.Queries) error {
		created = 0
		endpoints, err := q.ListWebhookListeners(ctx, store.ListWebhookListenersParams{ClubID: env.ClubID, Event: env.Type})
		if err != nil {
			return err
		}
		now := c.o.Now()
		for _, endpointID := range endpoints {
			id := DeliveryID(env.ID, endpointID)
			body, err := json.Marshal(payload{
				ID: id.String(), Event: env.Type, CreatedAt: env.CreatedAt.UTC().Format(iso8601),
				ClubID: env.ClubID, Data: data,
			})
			if err != nil {
				return err
			}
			n, err := q.InsertWebhookDelivery(ctx, store.InsertWebhookDeliveryParams{
				ClubID: env.ClubID, WebhookEndpointID: endpointID, EventID: pgtype.UUID{Bytes: id, Valid: true},
				Event: env.Type, Payload: body, Now: now,
			})
			if err != nil {
				return err
			}
			created += int(n)
		}
		return nil
	})
	return created, err
}
