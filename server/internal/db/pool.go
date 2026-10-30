package db

import (
	"context"
	"fmt"
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

// PartitionedTables — таблицы с месячными партициями (0002_ledger.sql, 0003_audit.sql).
var PartitionedTables = []string{"ledger_transactions", "ledger_entries", "ledger_minute_entries", "audit_log"}

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
