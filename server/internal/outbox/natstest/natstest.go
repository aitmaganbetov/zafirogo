//go:build integration

// Package natstest — NATS с JetStream для интеграционных тестов (testcontainers) и поток zafiro-events.
//
// Сервер — ZAFIRO_TEST_NATS_URL или контейнер nats:2.15.2-alpine (при 429 Docker Hub —
// ZAFIRO_TEST_NATS_IMAGE=mirror.gcr.io/library/nats:2.15.2-alpine).
package natstest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"

	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
)

// Image — образ NATS (как docker-compose.dev.yml).
const Image = "nats:2.15.2-alpine"

// NATS — подключение и JetStream с созданным потоком.
type NATS struct {
	URL    string
	Conn   *nats.Conn
	JS     jetstream.JetStream
	Stream jetstream.Stream
}

// New поднимает NATS (или берёт ZAFIRO_TEST_NATS_URL) и создаёт поток событий с именем stream.
func New(t testing.TB, stream string) *NATS {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("ZAFIRO_TEST_NATS_URL")
	if url == "" {
		img := os.Getenv("ZAFIRO_TEST_NATS_IMAGE")
		if img == "" {
			img = Image
		}
		ctr, err := tcnats.Run(ctx, img)
		if err != nil {
			t.Fatalf("natstest: start nats: %v", err)
		}
		t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
		if url, err = ctr.ConnectionString(ctx); err != nil {
			t.Fatal(err)
		}
	}
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("natstest: connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	s, err := outbox.EnsureStream(ctx, js, outbox.StreamOptions{Name: stream})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), stream) })
	return &NATS{URL: url, Conn: nc, JS: js, Stream: s}
}
