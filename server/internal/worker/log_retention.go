package worker

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// LogRetentionJob — имя задачи и ключ advisory lock.
const LogRetentionJob = "logs:retention"

// LogRetentionInterval — Schedule::command('integrations:prune')->hourly() (routes/console.php).
const LogRetentionInterval = time.Hour

// LogRetention удаляет месячные партиции журналов старше срока хранения целиком (DETACH + DROP,
// db.DropPartitionsBefore). Сроки — из старого кода: api_request_logs — 90 дней
// (app/Console/Commands/PruneIntegrationData.php handle(), integrations.request_log_retention_days;
// ключ integrations.request_log_retention). Партиция удаляется, когда весь её месяц старше срока, поэтому
// строки живут не меньше срока (до месяца дольше, чем в старом построчном удалении). ledger_*, audit_log
// не удаляются никогда, activity_logs старый код хранил вечно — их в Retention быть не может
// (db.RetentionTables, белый список функции 0026_log_retention.sql). Один исполнитель на кластер (Q-086).
type LogRetention struct {
	Platform *pgxpool.Pool
	Interval time.Duration
	// Retention — таблица → срок хранения; 0 или меньше — не удалять.
	Retention map[string]time.Duration
	Now       func() time.Time
	Log       *slog.Logger
}

// Run выполняет тики до отмены ctx (первый — сразу).
func (w *LogRetention) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = LogRetentionInterval
	}
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
				w.log().Error("log retention tick failed", "job", LogRetentionJob, "err", err)
			}
			t.Reset(interval)
		}
	}
}

// Tick — один запуск: dropped — сколько партиций удалено.
func (w *LogRetention) Tick(ctx context.Context) (ran bool, dropped int, err error) {
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	tables := make([]string, 0, len(w.Retention))
	for table, keep := range w.Retention {
		if keep > 0 {
			tables = append(tables, table)
		}
	}
	sort.Strings(tables)
	ran, err = leader.RunOnce(ctx, w.Platform, LogRetentionJob, func(ctx context.Context, conn *pgxpool.Conn) error {
		for _, table := range tables {
			n, err := db.DropPartitionsBefore(ctx, conn, table, now.Add(-w.Retention[table]))
			if err != nil {
				return err
			}
			if n > 0 {
				w.log().Info("expired log partitions dropped", "job", LogRetentionJob, "table", table, "count", n)
			}
			dropped += n
		}
		return nil
	})
	return ran, dropped, err
}

func (w *LogRetention) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
