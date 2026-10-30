//go:build integration

package realtime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox/natstest"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Сквозной путь: действие + outbox.Add в транзакции клуба → публикатор → JetStream → хаб → WebSocket.
// Сотрудник получает событие своего клуба и не получает чужого.
func TestOutboxToWebSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d := dbtest.New(t)
	n := natstest.New(t, outbox.StreamName)
	clubA, clubB := d.Club(t, "rt-a"), d.Club(t, "rt-b")

	h, err := NewHandler(Options{JS: n.JS})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- h.Run(ctx) }()
	select {
	case <-h.Ready():
	case err := <-runDone:
		t.Fatal(err)
	}
	pub, err := outbox.NewPublisher(outbox.PublisherOptions{Pool: d.Platform, JS: n.JS, Interval: 100 * time.Millisecond, LeaderRetry: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = pub.Run(ctx) }()

	srv := httptest.NewServer(testAuth(h))
	defer srv.Close()
	perms := []string{"club_map.view", "chat.view"}
	a, _, err := dial(ctx, t, srv.URL, who{1, clubA, "admin", perms})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.CloseNow() }()
	b, _, err := dial(ctx, t, srv.URL, who{2, clubB, "admin", perms})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.CloseNow() }()
	if m, err := read(ctx, t, a); err != nil || m.Type != "hello" {
		t.Fatalf("hello a: %+v %v", m, err)
	}
	if m, err := read(ctx, t, b); err != nil || m.Type != "hello" {
		t.Fatalf("hello b: %+v %v", m, err)
	}
	waitConns(t, h, clubA, 1)
	waitConns(t, h, clubB, 1)

	var eventID string
	if err := db.InTenantTx(ctx, d.App, clubA, func(q *store.Queries) error {
		added, err := outbox.Add(ctx, q, outbox.Event{ClubID: clubA, Type: "computer.state",
			Data: map[string]any{"computer_id": 5, "status": "busy"}})
		eventID = added.EventID
		return err
	}); err != nil {
		t.Fatal(err)
	}

	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	m, err := read(rctx, t, a)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		ComputerID int64  `json:"computer_id"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(m.Data, &data); err != nil {
		t.Fatal(err)
	}
	if m.ID != eventID || m.Type != "computer.state" || data.ComputerID != 5 || data.Status != "busy" {
		t.Fatalf("club A got %+v (data %s)", m, m.Data)
	}
	if m, err := read(ctx, t, b); err == nil {
		t.Fatalf("club B got a foreign event: %+v", m)
	}
}
