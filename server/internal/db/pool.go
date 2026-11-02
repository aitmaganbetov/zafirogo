package db

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool открывает пул pgx и проверяет соединение. dsn — строка подключения логина
// с ролью zafiro_app (сервер) или zafiro_platform (суперадминка, фоновые задачи).
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parse dsn: %w", err)
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

// PartitionedTables — таблицы с месячными партициями (0002_ledger.sql, 0003_audit.sql, 0011_events.sql, 0022_api_v1.sql).
var PartitionedTables = []string{"ledger_transactions", "ledger_entries", "ledger_minute_entries", "audit_log", "activity_logs",
	"api_request_logs"}

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
// не удаляются никогда; activity_logs старый код хранил вечно.
var RetentionTables = []string{"api_request_logs"}

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
