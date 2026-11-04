//go:build integration

package realtime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

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

// Живые обновления (Этап 7, computer.load): core NATS live.club.> → хаб → WebSocket сотрудника своего клуба с правом
// club_map.view; поток событий JetStream (club.>) их не сохраняет.
func TestLiveToWebSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	n := natstest.New(t, outbox.StreamName)
	h, err := NewHandler(Options{JS: n.JS, Live: n.Conn})
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
	srv := httptest.NewServer(testAuth(h))
	defer srv.Close()
	const clubA, clubB = 71, 72
	a, _, err := dial(ctx, t, srv.URL, who{1, clubA, "admin", []string{"club_map.view"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.CloseNow() }()
	noRight, _, err := dial(ctx, t, srv.URL, who{2, clubA, "cashier", []string{"chat.view"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = noRight.CloseNow() }()
	b, _, err := dial(ctx, t, srv.URL, who{3, clubB, "admin", []string{"club_map.view"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.CloseNow() }()
	for _, c := range []*websocket.Conn{a, noRight, b} {
		if m, err := read(ctx, t, c); err != nil || m.Type != "hello" {
			t.Fatalf("hello: %+v %v", m, err)
		}
	}
	waitConns(t, h, clubA, 2)
	waitConns(t, h, clubB, 1)

	env, _ := json.Marshal(outbox.Envelope{ID: "0199f0a4-7b6e-7c3a-9d2e-1f2a3b4c5d6e", Type: "computer.load", ClubID: clubA,
		CreatedAt: time.Now().UTC(), Data: json.RawMessage(`{"computer_id":7,"load":{"cpu_bp":4200,"ram_bp":5100,"idle_seconds":3},"problems":["mouse"]}`)})
	// Подделка служебного события через живой канал не отключает сотрудников.
	fake, _ := json.Marshal(outbox.Envelope{ID: "0199f0a4-7b6e-7c3a-9d2e-1f2a3b4c5d6f", Type: AccessChangedEvent, ClubID: clubA,
		CreatedAt: time.Now().UTC(), Data: json.RawMessage(`{"user_id":1}`)})
	if err := n.Conn.Publish(LiveSubject(clubA, AccessChangedEvent), fake); err != nil {
		t.Fatal(err)
	}
	if err := n.Conn.Publish(LiveSubject(clubA, "computer.load"), env); err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	m, err := read(rctx, t, a)
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != "computer.load" || !strings.Contains(string(m.Data), `"cpu_bp":4200`) {
		t.Fatalf("club A got %+v (data %s)", m, m.Data)
	}
	if m, err := read(ctx, t, noRight); err == nil {
		t.Fatalf("staff without club_map.view got %+v", m)
	}
	if m, err := read(ctx, t, b); err == nil {
		t.Fatalf("club B got a foreign event: %+v", m)
	}
	info, err := n.Stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 0 {
		t.Fatalf("live updates must not be stored in JetStream: %d messages", info.State.Msgs)
	}
}
