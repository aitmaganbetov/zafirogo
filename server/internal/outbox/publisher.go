package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// JobName — имя задачи публикатора (ключ advisory lock: hashtext('job:outbox-publisher')).
const JobName = "outbox-publisher"

// NotifyChannel — канал LISTEN/NOTIFY: триггер outbox_events_notify (0011_events.sql) шлёт в него
// уведомление после вставки; доставляется при COMMIT транзакции действия.
const NotifyChannel = "zafiro_outbox"

// PublisherOptions — параметры публикатора.
type PublisherOptions struct {
	// Pool — пул логина zafiro_platform (публикатор читает outbox всех клубов).
	Pool *pgxpool.Pool
	// JS — JetStream.
	JS jetstream.JetStream
	// BatchSize — событий за транзакцию (0 — 100).
	BatchSize int
	// Interval — страховочный опрос, если NOTIFY потерялся (0 — 2 с).
	Interval time.Duration
	// RetryDelay — пауза после ошибки публикации в NATS (0 — 2 с).
	RetryDelay time.Duration
	// PublishTimeout — сколько ждать подтверждений JetStream на батч (0 — 5 с).
	PublishTimeout time.Duration
	// Retention — сколько хранить опубликованные события в Postgres (0 — 24 ч).
	Retention time.Duration
	// CleanupEvery — как часто чистить опубликованные (0 — 10 мин).
	CleanupEvery time.Duration
	// LeaderRetry — как часто пробовать стать лидером (0 — 5 с).
	LeaderRetry time.Duration
	// Log — журнал (nil — slog.Default()).
	Log *slog.Logger
	// Now — часы (nil — time.Now).
	Now func() time.Time
}

// Publisher переносит события из outbox_events в JetStream. Один активный публикатор на кластер
// (worker/leader): так события клуба уходят в порядке записи. Пробуждение — LISTEN zafiro_outbox на
// соединении, держащем лок лидерства, плюс страховочный интервал.
type Publisher struct {
	o PublisherOptions
	// afterPublish — хук тестов: вызывается после подтверждения JetStream и до пометки published_at;
	// ошибка имитирует падение публикатора посередине (транзакция откатывается).
	afterPublish func(published int) error
}

// errPublish — ошибка на стороне NATS (не Postgres): лидерство не сдаём, ждём и повторяем.
type errPublish struct{ err error }

func (e errPublish) Error() string { return "outbox: publish: " + e.err.Error() }
func (e errPublish) Unwrap() error { return e.err }

// NewPublisher проверяет параметры и подставляет умолчания.
func NewPublisher(o PublisherOptions) (*Publisher, error) {
	if o.Pool == nil || o.JS == nil {
		return nil, errors.New("outbox: publisher needs Pool and JS")
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 100
	}
	if o.Interval <= 0 {
		o.Interval = 2 * time.Second
	}
	if o.RetryDelay <= 0 {
		o.RetryDelay = 2 * time.Second
	}
	if o.PublishTimeout <= 0 {
		o.PublishTimeout = 5 * time.Second
	}
	if o.Retention <= 0 {
		o.Retention = 24 * time.Hour
	}
	if o.CleanupEvery <= 0 {
		o.CleanupEvery = 10 * time.Minute
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Publisher{o: o}, nil
}

// Run публикует события до отмены ctx (ждёт лидерства, держит его).
func (p *Publisher) Run(ctx context.Context) error {
	return leader.Hold(ctx, p.o.Pool, JobName, leader.HoldOptions{Retry: p.o.LeaderRetry, Log: p.o.Log}, p.lead)
}

func (p *Publisher) lead(ctx context.Context, lock *leader.Lock) error {
	conn := lock.Conn().Conn()
	if _, err := conn.Exec(ctx, "LISTEN "+NotifyChannel); err != nil {
		return fmt.Errorf("outbox: listen: %w", err)
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, "UNLISTEN "+NotifyChannel)
	}()
	nextCleanup := p.o.Now()
	for {
		// Выбираем всё, что накопилось.
		for {
			n, err := p.PublishBatch(ctx, conn)
			if err != nil {
				var pe errPublish
				if !errors.As(err, &pe) {
					return err // Postgres: соединение (и лок) могли пропасть — переизбрание
				}
				p.o.Log.Warn("outbox: publish failed, will retry", "err", err)
				if !sleep(ctx, p.o.RetryDelay) {
					return nil
				}
				break
			}
			if n < p.o.BatchSize {
				break
			}
		}
		if now := p.o.Now(); !now.Before(nextCleanup) {
			if err := p.cleanup(ctx, conn); err != nil {
				return err
			}
			nextCleanup = now.Add(p.o.CleanupEvery)
		}
		wctx, cancel := context.WithTimeout(ctx, p.o.Interval)
		_, err := conn.WaitForNotification(wctx)
		cancel()
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil && !errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("outbox: wait for notification: %w", err)
		}
	}
}

// PublishBatch публикует до BatchSize неопубликованных событий в одной транзакции на db и возвращает,
// сколько событий взято. Событие, которое JetStream не подтвердил, остаётся неопубликованным
// (attempts + 1, last_error) и уйдёт в следующем батче.
func (p *Publisher) PublishBatch(ctx context.Context, db interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}) (int, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("outbox: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := store.New(tx)
	rows, err := q.ClaimOutboxBatch(ctx, int32(p.o.BatchSize)) // #nosec G115 -- BatchSize задаёт конфиг, небольшое
	if err != nil {
		return 0, fmt.Errorf("outbox: claim: %w", err)
	}
	if len(rows) == 0 {
		return 0, tx.Commit(ctx)
	}

	futures := make([]jetstream.PubAckFuture, len(rows))
	var firstErr error
	for i, r := range rows {
		msg, err := message(r)
		if err != nil {
			return 0, err
		}
		f, err := p.o.JS.PublishMsgAsync(msg)
		if err != nil {
			firstErr = err
			break
		}
		futures[i] = f
	}

	wctx, cancel := context.WithTimeout(ctx, p.o.PublishTimeout)
	defer cancel()
	var okIDs, okClubs []int64
	type failure struct {
		row store.ClaimOutboxBatchRow
		err error
	}
	var failed []failure
	for i, f := range futures {
		if f == nil {
			failed = append(failed, failure{rows[i], firstErr})
			continue
		}
		select {
		case <-f.Ok():
			okIDs = append(okIDs, rows[i].ID)
			okClubs = append(okClubs, rows[i].ClubID)
		case err := <-f.Err():
			failed = append(failed, failure{rows[i], err})
		case <-wctx.Done():
			failed = append(failed, failure{rows[i], wctx.Err()})
		}
	}

	if p.afterPublish != nil {
		if err := p.afterPublish(len(okIDs)); err != nil {
			return 0, err
		}
	}
	if len(okIDs) > 0 {
		if _, err := q.MarkOutboxPublished(ctx, store.MarkOutboxPublishedParams{
			PublishedAt: p.o.Now(), Ids: okIDs, ClubIds: okClubs,
		}); err != nil {
			return 0, fmt.Errorf("outbox: mark published: %w", err)
		}
	}
	for _, f := range failed {
		msg := truncate(errText(f.err), 255)
		if err := q.MarkOutboxAttemptFailed(ctx, store.MarkOutboxAttemptFailedParams{
			LastError: &msg, ClubID: f.row.ClubID, ID: f.row.ID,
		}); err != nil {
			return 0, fmt.Errorf("outbox: mark failed: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("outbox: commit: %w", err)
	}
	if len(failed) > 0 {
		return len(rows), errPublish{fmt.Errorf("%d of %d events not acknowledged: %w", len(failed), len(rows), failed[0].err)}
	}
	return len(rows), nil
}

func (p *Publisher) cleanup(ctx context.Context, conn *pgx.Conn) error {
	q := store.New(conn)
	before := p.o.Now().Add(-p.o.Retention)
	for {
		n, err := q.DeletePublishedOutboxEvents(ctx, store.DeletePublishedOutboxEventsParams{
			PublishedBefore: before, BatchSize: 1000,
		})
		if err != nil {
			return fmt.Errorf("outbox: cleanup: %w", err)
		}
		if n < 1000 {
			return nil
		}
	}
}

// message — сообщение JetStream для строки outbox: subject club.<id>.<type>, Nats-Msg-Id = event_id.
func message(r store.ClaimOutboxBatchRow) (*nats.Msg, error) {
	id := uuid.UUID(r.EventID.Bytes).String()
	body, err := json.Marshal(Envelope{ID: id, Type: r.Type, ClubID: r.ClubID, CreatedAt: r.CreatedAt.UTC(), Data: r.Payload})
	if err != nil {
		return nil, fmt.Errorf("outbox: envelope: %w", err)
	}
	msg := nats.NewMsg(Subject(r.ClubID, r.Type))
	msg.Data = body
	msg.Header.Set(jetstream.MsgIDHeader, id)
	return msg, nil
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
