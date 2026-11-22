// Command zafiro-migrate — job миграций: goose CLI под защитой от параллельного запуска и долгих блокировок
// (аудит OPS-7, DB-1, OPS-14, OPS-20; docs/schema-changes.md §19.1, docs/deploy.md §7).
//
//	zafiro-migrate [аргументы goose]     # по умолчанию: up
//
// Что добавляет к `goose up`:
//
//  1. Сессионная advisory-блокировка на время работы goose (ключ — тот же, что у goose.Provider, lock.DefaultLockID):
//     второй job (повтор хука helm, ручной запуск во время выкладки) ждёт первого, а не применяет те же миграции
//     параллельно. Ждёт не дольше ZAFIRO_MIGRATE_LOCK_WAIT (10m).
//  2. lock_timeout и statement_timeout сессиям goose (PGOPTIONS): DDL не встаёт в очередь за долгой транзакцией,
//     блокируя все запросы к таблице. Умолчания: ZAFIRO_MIGRATE_LOCK_TIMEOUT=5s, ZAFIRO_MIGRATE_STATEMENT_TIMEOUT=30min
//     (0 — без ограничения; поднимать на время известной долгой миграции).
//  3. Повтор при 55P03 (lock_not_available): миграция в транзакции откатилась целиком — goose запускается снова,
//     до ZAFIRO_MIGRATE_RETRIES (5) раз через ZAFIRO_MIGRATE_RETRY_DELAY (15s).
//  4. DSN владельца схемы — из файла GOOSE_DBSTRING_FILE (секрет), а не из переменной окружения контейнера: значение
//     не видно в `docker inspect` и в конфигурации завершённого job. GOOSE_DBSTRING — запасной вариант.
//
// Остальное — как у goose: GOOSE_DRIVER, GOOSE_MIGRATION_DIR из окружения образа, коды выхода (0 — успех).
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// lockID — lock.DefaultLockID goose v3 (crc64 «goose»): тот же ключ, что у goose.Provider с SessionLocker.
const lockID int64 = 4097083626

type config struct {
	gooseBin         string
	args             []string
	dsn              string
	lockTimeout      string
	statementTimeout string
	lockWait         time.Duration
	retries          int
	retryDelay       time.Duration
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := loadConfig(os.Args[1:], os.Getenv)
	if err != nil {
		log.Error("zafiro-migrate: конфигурация", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, log, cfg, os.Stdout, os.Stderr))
}

func loadConfig(args []string, getenv func(string) string) (config, error) {
	cfg := config{
		gooseBin:         envOr(getenv, "ZAFIRO_MIGRATE_GOOSE", "/usr/local/bin/goose"),
		args:             args,
		lockTimeout:      envOr(getenv, "ZAFIRO_MIGRATE_LOCK_TIMEOUT", "5s"),
		statementTimeout: envOr(getenv, "ZAFIRO_MIGRATE_STATEMENT_TIMEOUT", "30min"),
	}
	if len(cfg.args) == 0 {
		cfg.args = []string{"up"}
	}
	var err error
	if cfg.lockWait, err = time.ParseDuration(envOr(getenv, "ZAFIRO_MIGRATE_LOCK_WAIT", "10m")); err != nil {
		return cfg, fmt.Errorf("ZAFIRO_MIGRATE_LOCK_WAIT: %w", err)
	}
	if cfg.retryDelay, err = time.ParseDuration(envOr(getenv, "ZAFIRO_MIGRATE_RETRY_DELAY", "15s")); err != nil {
		return cfg, fmt.Errorf("ZAFIRO_MIGRATE_RETRY_DELAY: %w", err)
	}
	if _, err = fmt.Sscan(envOr(getenv, "ZAFIRO_MIGRATE_RETRIES", "5"), &cfg.retries); err != nil || cfg.retries < 0 {
		return cfg, fmt.Errorf("ZAFIRO_MIGRATE_RETRIES: ожидается целое ≥ 0")
	}
	for name, v := range map[string]string{"LOCK_TIMEOUT": cfg.lockTimeout, "STATEMENT_TIMEOUT": cfg.statementTimeout} {
		if strings.ContainsAny(v, " \t'\"\\") || v == "" {
			return cfg, fmt.Errorf("ZAFIRO_MIGRATE_%s: недопустимое значение %q", name, v)
		}
	}
	if f := getenv("GOOSE_DBSTRING_FILE"); f != "" {
		b, err := os.ReadFile(f) // #nosec G304 -- путь к секрету задаёт оператор
		if err != nil {
			return cfg, fmt.Errorf("GOOSE_DBSTRING_FILE: %w", err)
		}
		cfg.dsn = strings.TrimSpace(string(b))
	} else {
		cfg.dsn = getenv("GOOSE_DBSTRING")
	}
	if cfg.dsn == "" {
		return cfg, errors.New("не задан DSN владельца схемы: GOOSE_DBSTRING_FILE (файл-секрет) или GOOSE_DBSTRING")
	}
	return cfg, nil
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// pgOptions добавляет таймауты к PGOPTIONS (pgx читает его, если в DSN нет параметра options). Заданные оператором
// lock_timeout/statement_timeout не переопределяются.
func pgOptions(existing, lockTimeout, statementTimeout string) string {
	opts := strings.TrimSpace(existing)
	add := func(name, v string) {
		if strings.Contains(opts, name+"=") {
			return
		}
		opts = strings.TrimSpace(opts + " -c " + name + "=" + v)
	}
	add("lock_timeout", lockTimeout)
	add("statement_timeout", statementTimeout)
	return opts
}

// lockNotAvailable — goose упал на lock_timeout (SQLSTATE 55P03): транзакция миграции откатилась, повтор безопасен.
func lockNotAvailable(output []byte) bool {
	return bytes.Contains(output, []byte("SQLSTATE 55P03"))
}

func run(ctx context.Context, log *slog.Logger, cfg config, stdout, stderr io.Writer) int {
	conn, err := connectWithRetry(ctx, log, cfg.dsn, cfg.lockWait)
	if err != nil {
		log.Error("zafiro-migrate: подключение к Postgres", "err", err)
		return 1
	}
	defer func() { _ = conn.Close(context.Background()) }()

	if err := acquire(ctx, log, conn, cfg.lockWait); err != nil {
		log.Error("zafiro-migrate: блокировка миграций", "err", err)
		return 1
	}
	defer func() {
		// Сессионная блокировка снимается и при закрытии соединения; явный unlock — чтобы не ждать таймаута TCP.
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID)
	}()

	env := append(os.Environ(),
		"GOOSE_DBSTRING="+cfg.dsn,
		"PGOPTIONS="+pgOptions(os.Getenv("PGOPTIONS"), cfg.lockTimeout, cfg.statementTimeout))
	for attempt := 0; ; attempt++ {
		var captured bytes.Buffer
		// -env=none: goose не читает .env из рабочего каталога (DSN и драйвер — только из окружения обёртки);
		// флаг оператора в cfg.args идёт позже и побеждает.
		args := append([]string{"-env=none"}, cfg.args...)
		cmd := exec.CommandContext(ctx, cfg.gooseBin, args...) // #nosec G204 -- аргументы goose задаёт оператор job
		cmd.Env = env
		cmd.Stdout = io.MultiWriter(stdout, &captured)
		cmd.Stderr = io.MultiWriter(stderr, &captured)
		err := cmd.Run()
		if err == nil {
			return 0
		}
		code := 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			code = exitErr.ExitCode()
		}
		if !lockNotAvailable(captured.Bytes()) || attempt >= cfg.retries || ctx.Err() != nil {
			log.Error("zafiro-migrate: goose завершился с ошибкой", "code", code, "attempt", attempt+1)
			return code
		}
		log.Warn("zafiro-migrate: lock_timeout (55P03) — таблицу держит другая транзакция; миграция откатилась, повтор",
			"attempt", attempt+1, "retries", cfg.retries, "delay", cfg.retryDelay)
		select {
		case <-ctx.Done():
			return code
		case <-time.After(cfg.retryDelay):
		}
	}
}

func connectWithRetry(ctx context.Context, log *slog.Logger, dsn string, wait time.Duration) (*pgx.Conn, error) {
	deadline := time.Now().Add(wait)
	for {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		conn, err := pgx.Connect(cctx, dsn)
		cancel()
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, err
		}
		log.Warn("zafiro-migrate: Postgres недоступен, повтор", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func acquire(ctx context.Context, log *slog.Logger, conn *pgx.Conn, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	logged := false
	for {
		var ok bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&ok); err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("другой job миграций держит блокировку дольше %s", wait)
		}
		if !logged {
			log.Info("zafiro-migrate: миграции уже выполняет другой job — жду его завершения")
			logged = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
