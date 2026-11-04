//go:build integration

// Package authtest — Valkey для интеграционных тестов пакетов auth/*: ZAFIRO_TEST_VALKEY_ADDR или контейнер
// testcontainers (образ как в docker-compose.dev.yml).
package authtest

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/valkey-io/valkey-go"
)

// ValkeyImage — образ Valkey.
const ValkeyImage = "valkey/valkey:9.1.3-alpine"

// StartValkey возвращает клиента и cleanup. Каждому тесту — свой префикс ключей, база общая.
func StartValkey(ctx context.Context) (valkey.Client, func(), error) {
	addr := os.Getenv("ZAFIRO_TEST_VALKEY_ADDR")
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	if addr == "" {
		ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        ValkeyImage,
				ExposedPorts: []string{"6379/tcp"},
				WaitingFor:   wait.ForLog("Ready to accept connections").WithStartupTimeout(60 * time.Second),
			},
			Started: true,
		})
		if err != nil {
			return nil, cleanup, fmt.Errorf("authtest: start valkey: %w", err)
		}
		cleanups = append(cleanups, func() { _ = testcontainers.TerminateContainer(ctr) })
		ep, err := ctr.PortEndpoint(ctx, "6379/tcp", "")
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		addr = ep
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, DisableCache: true})
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("authtest: valkey client: %w", err)
	}
	cleanups = append(cleanups, c.Close)
	return c, cleanup, nil
}
