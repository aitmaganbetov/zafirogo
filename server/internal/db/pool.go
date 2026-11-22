package db

import (
	"context"
	"fmt"
	"net"
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

// DefaultMaxConns — размер пула, если в DSN нет pool_max_conns: max(16, 4 × GOMAXPROCS), не больше 256.
//
// GOMAXPROCS, а не NumCPU (аудит OPS-8): с Go 1.25 GOMAXPROCS учитывает квоту CPU контейнера (cgroup), а NumCPU —
// только ядра узла. На узле с 64 ядрами и лимитом 2 CPU NumCPU дал бы 256 соединений на пул.
//
// Умолчание pgx — max(4, CPU): на 4 vCPU это 4 соединения на пул, и нагрузочный тест O4 (docs/load-test.md §4)
// показал обвал при 5 000 ПК на одном инстансе (после перезапуска второго): пул zafiro_app занят целиком, сотни
// ожидающих (запись состояния ПК, карта клуба, покупки гостей), ответы 10–30 с вместо 20 мс. С 16 соединениями —
// без ошибок, занято в пике 5. Явный pool_max_conns в DSN (docs/deploy.md «Ресурсы») имеет приоритет.
func DefaultMaxConns() int32 {
	return int32(min(max(16, 4*runtime.GOMAXPROCS(0)), 256)) // #nosec G115 -- ≤ 256
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
	cfg.ConnConfig.DialFunc = keepaliveDialer(cfg.ConnConfig.ConnectTimeout).DialContext
	return cfg, nil
}

// Параметры TCP соединений с Postgres (аудит OPS-3, клиентская сторона): обрыв без FIN/RST (гибель хоста, разрыв
// сети) клиент замечает за KeepAliveIdle + KeepAliveCount × KeepAliveInterval ≈ 60 с на простаивающем соединении
// и за UserTimeout — на соединении с неподтверждённой отправкой (иначе — повторы TCP ≈ 15 мин). Так бывший лидер
// фоновой задачи узнаёт о потере лока, а запрос не висит на мёртвом соединении. Серверная сторона (лок держателя,
// пропавшего без FIN) — tcp_keepalives_* и client_connection_check_interval Postgres (docs/deploy.md).
const (
	KeepAliveIdle     = 30 * time.Second
	KeepAliveInterval = 10 * time.Second
	KeepAliveCount    = 3
	UserTimeout       = 60 * time.Second
)

// keepaliveDialer — net.Dialer pgconn (connect_timeout из DSN сохраняется) с KeepAliveConfig и TCP_USER_TIMEOUT (Linux).
func keepaliveDialer(connectTimeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout: connectTimeout,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable: true, Idle: KeepAliveIdle, Interval: KeepAliveInterval, Count: KeepAliveCount,
		},
		Control: setUserTimeout,
	}
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
