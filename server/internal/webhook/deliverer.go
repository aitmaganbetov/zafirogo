package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// DelivererOptions — параметры доставки.
type DelivererOptions struct {
	// Platform — пул логина zafiro_platform: выборка доставок всех клубов (SKIP LOCKED + аренда).
	Platform *pgxpool.Pool
	// App — пул логина zafiro_app: чтение и запись результата в транзакции клуба доставки.
	App *pgxpool.Pool
	// Guard — SSRF-проверка URL (нулевое значение — HTTP разрешён, частные сети разрешены: задайте явно!).
	Guard Guard
	// Backoff — паузы между попытками (nil — DefaultBackoff).
	Backoff []time.Duration
	// Timeout — таймаут запроса (0 — 5 с); ConnectTimeout — соединения (0 — 3 с).
	Timeout, ConnectTimeout time.Duration
	// DisableAfter — после стольких неудачных доставок подряд endpoint выключается (0 — 20).
	DisableAfter int
	// Lease — на сколько «арендуется» доставка на время отправки (0 — 1 мин; > Timeout + ConnectTimeout).
	Lease time.Duration
	// Workers — параллельных отправок на инстанс (0 — 8); BatchSize — доставок за выборку (0 — 50).
	Workers, BatchSize int
	// PollInterval — страховочный опрос очереди (0 — 5 с), кроме пробуждения Wake().
	PollInterval time.Duration
	// OnEndpointDisabled — endpoint выключен после DisableAfter неудач (сброс кэша слушателей,
	// WebhookDispatcher::forgetClub()).
	OnEndpointDisabled func(clubID, endpointID int64)
	// Log — журнал (nil — slog.Default()); Now — часы (nil — time.Now).
	Log *slog.Logger
	Now func() time.Time
}

// Deliverer отправляет доставки webhook_deliveries. Не singleton: инстансы делят очередь через
// FOR UPDATE SKIP LOCKED и аренду next_attempt_at.
// Laravel: app/Jobs/DeliverWebhook.php DeliverWebhook::handle().
type Deliverer struct {
	o      DelivererOptions
	sender sender
	wake   chan struct{}
}

// NewDeliverer проверяет параметры и подставляет умолчания.
func NewDeliverer(o DelivererOptions) (*Deliverer, error) {
	if o.Platform == nil || o.App == nil {
		return nil, errors.New("webhook: deliverer needs Platform and App pools")
	}
	if o.Backoff == nil {
		o.Backoff = DefaultBackoff
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = DefaultConnectTimeout
	}
	if o.DisableAfter <= 0 {
		o.DisableAfter = DefaultDisableAfter
	}
	if o.Lease <= 0 {
		o.Lease = time.Minute
	}
	if o.Workers <= 0 {
		o.Workers = 8
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 50
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 5 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Deliverer{
		o:      o,
		sender: sender{Timeout: o.Timeout, ConnectTimeout: o.ConnectTimeout},
		wake:   make(chan struct{}, 1),
	}, nil
}

// Wake будит цикл доставки (новые доставки созданы на этом инстансе).
func (d *Deliverer) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run отправляет доставки до отмены ctx.
func (d *Deliverer) Run(ctx context.Context) error {
	for {
		n, err := d.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			d.o.Log.Error("webhook: delivery round failed", "err", err)
		}
		if n >= d.o.BatchSize && err == nil {
			continue
		}
		t := time.NewTimer(d.o.PollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-d.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// RunOnce арендует до BatchSize доставок, которым пора, и отправляет их (до Workers параллельно).
// Возвращает число взятых доставок.
func (d *Deliverer) RunOnce(ctx context.Context) (int, error) {
	now := d.o.Now()
	var due []store.ClaimDueWebhookDeliveriesRow
	err := db.InPlatformTx(ctx, d.o.Platform, func(q *store.Queries) error {
		var err error
		due, err = q.ClaimDueWebhookDeliveries(ctx, store.ClaimDueWebhookDeliveriesParams{
			LeaseUntil: now.Add(d.o.Lease), Now: now, BatchSize: int32(d.o.BatchSize), // #nosec G115 -- небольшое значение конфига
		})
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("webhook: claim: %w", err)
	}
	sem := make(chan struct{}, d.o.Workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for _, row := range due {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := d.Deliver(ctx, row.ClubID, row.ID); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return len(due), errors.Join(errs...)
}

// Deliver — одна попытка доставки (DeliverWebhook::handle()).
func (d *Deliverer) Deliver(ctx context.Context, clubID, deliveryID int64) error {
	var (
		del  store.GetWebhookDeliveryRow
		ep   store.GetWebhookEndpointForDeliveryRow
		done bool
	)
	err := db.InTenantTx(ctx, d.o.App, clubID, func(q *store.Queries) error {
		done = false
		var err error
		del, err = q.GetWebhookDelivery(ctx, store.GetWebhookDeliveryParams{ClubID: clubID, ID: deliveryID})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && del.Status != StatusPending {
			done = true // удалена или уже финальна
			return nil
		}
		if err != nil {
			return err
		}
		ep, err = q.GetWebhookEndpointForDelivery(ctx, store.GetWebhookEndpointForDeliveryParams{ClubID: clubID, ID: del.WebhookEndpointID})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !ep.IsActive {
			// Endpoint удалён или выключен — skipped.
			done = true
			return q.SkipWebhookDelivery(ctx, store.SkipWebhookDeliveryParams{Now: d.o.Now(), ClubID: clubID, ID: deliveryID})
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("webhook: load delivery %d: %w", deliveryID, err)
	}
	if done {
		return nil
	}

	status, sendErr := d.send(ctx, ep, del)

	now := d.o.Now()
	attempt := del.Attempts + 1
	p := store.SaveWebhookAttemptParams{Attempts: attempt, Now: now, ClubID: clubID, ID: deliveryID}
	if status > 0 {
		s := int32(status) // #nosec G115 -- HTTP-статус
		p.LastStatusCode = &s
	}
	if sendErr != "" {
		e := truncateRunes(sendErr, 255) // mb_substr($error, 0, 255)
		p.LastError = &e
	}
	var disabled bool
	err = db.InTenantTx(ctx, d.o.App, clubID, func(q *store.Queries) error {
		disabled = false
		switch {
		case sendErr == "":
			p.Status, p.DeliveredAt = StatusDelivered, &now
			if err := q.SaveWebhookAttempt(ctx, p); err != nil {
				return err
			}
			return q.WebhookEndpointSucceeded(ctx, store.WebhookEndpointSucceededParams{Now: now, ClubID: clubID, ID: ep.ID})
		case int(attempt) <= len(d.o.Backoff):
			next := now.Add(d.o.Backoff[attempt-1])
			p.Status, p.NextAttemptAt = StatusPending, &next
			return q.SaveWebhookAttempt(ctx, p)
		default:
			p.Status = StatusFailed
			if err := q.SaveWebhookAttempt(ctx, p); err != nil {
				return err
			}
			row, err := q.WebhookEndpointFailed(ctx, store.WebhookEndpointFailedParams{
				Now: now, DisableAfter: int64(d.o.DisableAfter), ClubID: clubID, ID: ep.ID,
			})
			if err != nil {
				return err
			}
			disabled = !row.IsActive
			return nil
		}
	})
	if err != nil {
		return fmt.Errorf("webhook: save attempt %d: %w", deliveryID, err)
	}
	if disabled {
		d.o.Log.Warn("webhook: endpoint disabled after failures", "club_id", clubID, "endpoint_id", ep.ID)
		if d.o.OnEndpointDisabled != nil {
			d.o.OnEndpointDisabled(clubID, ep.ID)
		}
	}
	return nil
}

func (d *Deliverer) send(ctx context.Context, ep store.GetWebhookEndpointForDeliveryRow, del store.GetWebhookDeliveryRow) (int, string) {
	target, reason := d.o.Guard.Check(ctx, ep.Url)
	if reason != "" {
		return 0, "blocked: " + reason
	}
	// Тело — payload как есть, компактно (json_encode(..., JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES)).
	// Порядок ключей jsonb (по длине, затем по байтам) совпадает с порядком JSON-колонки MySQL старой схемы.
	var body bytes.Buffer
	if err := json.Compact(&body, del.Payload); err != nil {
		return 0, "invalid payload"
	}
	return d.sender.send(ctx, request{
		URL: ep.Url, Target: target, Secret: ep.Secret, Event: del.Event,
		Delivery: uuid.UUID(del.EventID.Bytes).String(), Body: body.Bytes(), Timestamp: d.o.Now().Unix(),
	})
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
