package db

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool открывает пул pgx и проверяет соединение. dsn — строка подключения логина
// с ролью zafiro_app (сервер) или zafiro_platform (суперадминка, фоновые задачи).
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := ParsePoolConfig(dsn)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// DefaultMaxConns — размер пула, если в DSN нет pool_max_conns: max(16, 4 × CPU).
//
// Умолчание pgx — max(4, CPU): на 4 vCPU это 4 соединения на пул, и нагрузочный тест O4 (docs/load-test.md §4)
// показал обвал при 5 000 ПК на одном инстансе (после перезапуска второго): пул zafiro_app занят целиком, сотни
// ожидающих (запись состояния ПК, карта клуба, покупки гостей), ответы 10–30 с вместо 20 мс. С 16 соединениями —
// без ошибок, занято в пике 5. Явный pool_max_conns в DSN (docs/deploy.md «Ресурсы») имеет приоритет.
func DefaultMaxConns() int32 {
	return int32(min(max(16, 4*runtime.NumCPU()), 256)) // #nosec G115 -- ≤ 256
}

// ParsePoolConfig — pgxpool.ParseConfig + размер пула по умолчанию (DefaultMaxConns), если DSN его не задаёт.
func ParsePoolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parse dsn: %w", err)
	}
	if !strings.Contains(dsn, "pool_max_conns") {
		cfg.MaxConns = DefaultMaxConns()
	}
	return cfg, nil
}

// PartitionedTables — таблицы с месячными партициями (0002_ledger.sql, 0003_audit.sql, 0011_events.sql, 0022_api_v1.sql,
// 0030_computer_health_events.sql, 0031_computer_health_hourly.sql).
var PartitionedTables = []string{"ledger_transactions", "ledger_entries", "ledger_minute_entries", "audit_log", "activity_logs",
	"api_request_logs", "computer_health_events", "computer_health_hourly"}

// EnsurePartitions создаёт месячные партиции (UTC) с месяца from на months месяцев вперёд для всех
// PartitionedTables; возвращает число созданных. Вызывает фоновая задача раз в сутки (Q-081) пулом
// с ролью zafiro_platform (EXECUTE на zafiro_create_monthly_partitions есть только у неё).
func EnsurePartitions(ctx context.Context, pool Beginner, from time.Time, months int) (int, error) {
	total := 0
	err := withRetry(ctx, func() error {
		total = 0
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		day := from.UTC().Format(time.DateOnly)
		for _, table := range PartitionedTables {
			var n int
			if err := tx.QueryRow(ctx, "SELECT zafiro_create_monthly_partitions($1, $2::date, $3)", table, day, months).Scan(&n); err != nil {
				return fmt.Errorf("db: partitions of %s: %w", table, err)
			}
			total += n
		}
		return tx.Commit(ctx)
	})
	return total, err
}

// RetentionTables — журналы со сроком хранения: только их партиции удаляет DropPartitionsBefore
// (белый список и в zafiro_drop_monthly_partitions, 0026_log_retention.sql). ledger_* и audit_log
// не удаляются никогда; activity_logs старый код хранил вечно. computer_health_events — история проблем здоровья ПК
// (Этап 7, agent.health.event_retention; 0030), computer_health_hourly — почасовая история здоровья ПК
// (agent.health.hourly_retention; 0031).
var RetentionTables = []string{"api_request_logs", "computer_health_events", "computer_health_hourly"}

// DropPartitionsBefore отсоединяет и удаляет (DETACH + DROP) месячные партиции table, весь месяц которых
// раньше before; возвращает число удалённых. Пул — zafiro_platform (EXECUTE на функцию есть только у неё).
// lock_timeout — чтобы DETACH (блокирует родителя) не держал очередь вставок журнала.
func DropPartitionsBefore(ctx context.Context, pool Beginner, table string, before time.Time) (int, error) {
	if !slices.Contains(RetentionTables, table) {
		return 0, fmt.Errorf("db: %s has no retention period", table)
	}
	dropped := 0
	err := withRetry(ctx, func() error {
		dropped = 0
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)",
			strconv.FormatInt(LockTimeout.Milliseconds(), 10)+"ms"); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT zafiro_drop_monthly_partitions($1, $2)", table, before.UTC()).Scan(&dropped); err != nil {
			return fmt.Errorf("db: drop partitions of %s: %w", table, err)
		}
		return tx.Commit(ctx)
	})
	return dropped, err
}
