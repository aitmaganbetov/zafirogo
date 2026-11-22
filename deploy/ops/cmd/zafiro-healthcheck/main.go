// Command zafiro-healthcheck — HEALTHCHECK образа сервера (аудит OPS-15): в distroless нет shell/curl.
//
//	zafiro-healthcheck            # GET http://127.0.0.1:8080/healthz, таймаут 2 с; код 0 — жив, 1 — нет
//
// ZAFIRO_HEALTHCHECK_URL меняет адрес (другой http.listen). Проверяется liveness (/healthz), не /readyz: недоступный
// Postgres не повод перезапускать процесс — его выводит из балансировки readiness оркестратора/балансировщика.
// Прокси из окружения не используется: проверка — только локального процесса.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	url := os.Getenv("ZAFIRO_HEALTHCHECK_URL")
	if url == "" {
		url = "http://127.0.0.1:8080/healthz"
	}
	if err := check(context.Background(), url, 2*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "zafiro-healthcheck:", err)
		os.Exit(1)
	}
}

func check(ctx context.Context, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// #nosec G704 -- адрес задаёт образ/оператор (ZAFIRO_HEALTHCHECK_URL), запрос — к локальному процессу
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req) // #nosec G704 -- см. выше
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}
