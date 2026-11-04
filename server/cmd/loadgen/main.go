//go:build loadgen

// Команда loadgen — нагрузочный тест всей системы на staging (Этап 6, блок O4; отчёт — docs/load-test.md).
// Не входит в CI и в обычную сборку (тег loadgen). Работает против поднятого deploy/staging через балансировщик:
// агенты — gRPC с mTLS через nginx L4 (PROXY protocol), админка — HTTPS и WebSocket через nginx.
//
//	cd server
//	go run -tags loadgen ./cmd/loadgen fixture -clubs 50 -pcs 100 -out /tmp/load.json
//	go run ./cmd/visual-seed -fixture /tmp/load.json -db "$OWNER_DSN"         # клубы, ПК, гости, балансы (ledger)
//	go run -tags loadgen ./cmd/loadgen prepare -staging ../deploy/staging -certs /tmp/load-certs.json
//	go run -tags loadgen ./cmd/loadgen run -staging ../deploy/staging -certs /tmp/load-certs.json -duration 30m
//	go run -tags loadgen ./cmd/loadgen check -staging ../deploy/staging
//
// Подробно — tools/loadgen/README.md.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "fixture":
		err = cmdFixture(os.Args[2:])
	case "prepare":
		err = cmdPrepare(ctx, os.Args[2:])
	case "run":
		err = cmdRun(ctx, os.Args[2:])
	case "check":
		err = cmdCheck(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: loadgen fixture|prepare|run|check [flags]  (tools/loadgen/README.md)")
	os.Exit(2)
}

// stagingEnv — переменные deploy/staging/.env.staging (пароли, порты).
type stagingEnv struct {
	dir  string
	vars map[string]string
}

func loadStaging(dir string) (*stagingEnv, error) {
	f, err := os.Open(filepath.Clean(filepath.Join(dir, ".env.staging")))
	if err != nil {
		return nil, fmt.Errorf("staging env: %w (task staging:up)", err)
	}
	defer f.Close()
	e := &stagingEnv{dir: dir, vars: map[string]string{}}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			e.vars[k] = v
		}
	}
	return e, sc.Err()
}

func (e *stagingEnv) v(k, def string) string {
	if v := e.vars[k]; v != "" {
		return v
	}
	return def
}

// ownerDSN — владелец схемы (как visual-seed и job миграций): подготовка данных и проверка инвариантов.
func (e *stagingEnv) ownerDSN() string {
	return fmt.Sprintf("postgres://%s:%s@127.0.0.1:15432/%s?sslmode=disable", e.v("STG_DB_OWNER_USER", "zafiro_owner"),
		e.v("STG_DB_OWNER_PASSWORD", ""), e.v("STG_POSTGRES_DB", "zafiro"))
}

// superDSN — суперпользователь контейнера: только чтение статистики (pg_stat_activity, pg_locks) во время теста.
func (e *stagingEnv) superDSN() string {
	return fmt.Sprintf("postgres://%s:%s@127.0.0.1:15432/%s?sslmode=disable", e.v("STG_POSTGRES_USER", "postgres"),
		e.v("STG_POSTGRES_PASSWORD", ""), e.v("STG_POSTGRES_DB", "zafiro"))
}
