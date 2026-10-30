//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox/natstest"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

func addEvents(t *testing.T, d *dbtest.DB, club int64, n int, typ string) []string {
	t.Helper()
	var ids []string
	err := db.InTenantTx(context.Background(), d.App, club, func(q *store.Queries) error {
		for i := range n {
			a, err := outbox.Add(context.Background(), q, outbox.Event{ClubID: club, Type: typ, Data: map[string]any{"n": i}})
			if err != nil {
				return err
			}
			ids = append(ids, a.EventID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// streamIDs читает все сообщения потока (новый ordered consumer с начала) и возвращает id событий.
func streamIDs(t *testing.T, js jetstream.JetStream) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := js.Stream(ctx, outbox.StreamName)
	if err != nil {
		t.Fatal(err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	total := int(info.State.Msgs)
	if total == 0 {
		return nil
	}
	cons, err := s.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(total, jetstream.FetchMaxWait(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for m := range batch.Messages() {
		env, err := outbox.DecodeEnvelope(m.Data())
		if err != nil {
			t.Fatal(err)
		}
		if m.Headers().Get(jetstream.MsgIDHeader) != env.ID || m.Subject() != outbox.Subject(env.ClubID, env.Type) {
			t.Fatalf("message %s: header %q subject %q", env.ID, m.Headers().Get(jetstream.MsgIDHeader), m.Subject())
		}
		ids = append(ids, env.ID)
	}
	return ids
}

func unpublished(t *testing.T, d *dbtest.DB) int {
	t.Helper()
	var n int
	if err := d.Owner.QueryRow(context.Background(), "SELECT count(*) FROM outbox_events WHERE published_at IS NULL").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOutboxCrashMidwayNoLossNoDuplicates(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	n := natstest.New(t, outbox.StreamName)
	club := d.Club(t, "outbox-crash")
	ids := addEvents(t, d, club, 5, "computer.state")

	// Откат действия — события нет.
	errRollback := errors.New("rollback")
	_ = db.InTenantTx(ctx, d.App, club, func(q *store.Queries) error {
		if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: club, Type: "chat.message"}); err != nil {
			return err
		}
		return errRollback
	})
	if unpublished(t, d) != 5 {
		t.Fatalf("unpublished = %d, want 5", unpublished(t, d))
	}

	// Публикатор «падает» после отправки 3 событий в JetStream, но до пометки published_at.
	crashing, err := outbox.NewPublisher(outbox.PublisherOptions{Pool: d.Platform, JS: n.JS, BatchSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("crash")
	crashing.SetAfterPublish(func(int) error { return crash })
	if _, err := crashing.PublishBatch(ctx, d.Platform); !errors.Is(err, crash) {
		t.Fatalf("crash: %v", err)
	}
	if got := unpublished(t, d); got != 5 {
		t.Fatalf("after crash unpublished = %d, want 5 (транзакция откатилась)", got)
	}
	if got := streamIDs(t, n.JS); len(got) != 3 {
		t.Fatalf("stream after crash: %d messages, want 3", len(got))
	}

	// Новый публикатор переотправляет всё: JetStream отбрасывает дубли по Nats-Msg-Id.
	p, err := outbox.NewPublisher(outbox.PublisherOptions{Pool: d.Platform, JS: n.JS, BatchSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	for {
		k, err := p.PublishBatch(ctx, d.Platform)
		if err != nil {
			t.Fatal(err)
		}
		if k == 0 {
			break
		}
	}
	if got := unpublished(t, d); got != 0 {
		t.Fatalf("unpublished = %d, want 0", got)
	}
	got := streamIDs(t, n.JS)
	if fmt.Sprint(got) != fmt.Sprint(ids) {
		t.Fatalf("stream ids = %v, want %v (без потерь, без дублей, по порядку)", got, ids)
	}
}

func TestPublisherWakesOnNotifyAndSingleLeader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := dbtest.New(t)
	n := natstest.New(t, outbox.StreamName)
	club := d.Club(t, "outbox-notify")

	// Интервал опроса — минута: событие за пару секунд может доставить только NOTIFY.
	opts := outbox.PublisherOptions{Pool: d.Platform, JS: n.JS, Interval: time.Minute, LeaderRetry: 100 * time.Millisecond}
	p1, _ := outbox.NewPublisher(opts)
	p2, _ := outbox.NewPublisher(opts)
	done := make(chan struct{}, 2)
	go func() { _ = p1.Run(ctx); done <- struct{}{} }()
	go func() { _ = p2.Run(ctx); done <- struct{}{} }()

	// Лидер ровно один: лок занят.
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := leader.TryAcquire(ctx, d.Platform, outbox.JobName)
		if err != nil {
			t.Fatal(err)
		}
		if lock == nil {
			break
		}
		lock.Release()
		if time.Now().After(deadline) {
			t.Fatal("no publisher became leader")
		}
		time.Sleep(50 * time.Millisecond)
	}

	ids := addEvents(t, d, club, 2, "todo.changed")
	deadline = time.Now().Add(5 * time.Second)
	for unpublished(t, d) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("event was not published after NOTIFY")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := streamIDs(t, n.JS); fmt.Sprint(got) != fmt.Sprint(ids) {
		t.Fatalf("stream = %v, want %v", got, ids)
	}
	cancel()
	<-done
	<-done
}

func TestOutboxRLS(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	a, b := d.Club(t, "outbox-a"), d.Club(t, "outbox-b")
	// Запись в чужой клуб из транзакции клуба a — отказ RLS.
	err := db.InTenantTx(ctx, d.App, a, func(q *store.Queries) error {
		_, err := outbox.Add(ctx, q, outbox.Event{ClubID: b, Type: "chat.message"})
		return err
	})
	if err == nil {
		t.Fatal("RLS must reject an event of another club")
	}
	var s string
	if err := d.Owner.QueryRow(ctx, "SELECT coalesce(string_agg(t, ','), '') FROM zafiro_tables_without_rls() t").Scan(&s); err != nil || s != "" {
		t.Fatalf("tables without RLS: %q %v", s, err)
	}
}
