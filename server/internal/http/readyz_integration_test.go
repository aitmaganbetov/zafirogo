//go:build integration

package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// postgresImage совпадает с docker-compose.dev.yml.
const postgresImage = "postgres:18.6"

// databaseURL возвращает DSN тестовой базы: ZAFIRO_TEST_DATABASE_URL (CI-сервис postgres)
// или поднимает контейнер через testcontainers.
func databaseURL(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("ZAFIRO_TEST_DATABASE_URL"); dsn != "" {
		return dsn
	}
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("zafiro_test"),
		tcpostgres.WithUsername("zafiro"),
		tcpostgres.WithPassword("zafiro"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

func TestReadyzWithPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	srv := newTestServer(t, Options{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Checkers: []Checker{CheckFunc{CheckName: "postgres", Fn: pool.Ping}},
	})
	resp, body := getJSON(t, srv.URL+"/readyz")
	if resp.StatusCode != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("status %d body %v", resp.StatusCode, body)
	}

	pool.Close()
	resp, body = getJSON(t, srv.URL+"/readyz")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("closed pool: status %d body %v", resp.StatusCode, body)
	}
}
