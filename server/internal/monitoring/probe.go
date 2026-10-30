package monitoring

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Snapshot — снимок машины (ServerProbe::snapshot()): nil — значение недоступно.
type Snapshot struct {
	Load1, Load5, Load15 *float64
	CPUCount             *int32
	MemoryTotalMB        *int64
	MemoryUsedMB         *int64
	DiskTotalMB          *int64
	DiskUsedMB           *int64
	UptimeSeconds        *int64
	DatabaseMB           *int64
	// Очередь: в новом сервере нет таблицы jobs Laravel — фоновые задачи идут воркерами и NATS (pending/failed 0).
	QueuePending, QueueFailed int64
	QueueOldestSeconds        *int64
}

// Probe читает показатели машины (Linux /proc и statfs) и размер базы.
type Probe struct {
	// Root — каталог, чей диск показывается (base_path() в старом); пусто — текущий каталог.
	Root string
	// Proc — корень procfs (для тестов); пусто — /proc.
	Proc string
}

func (p Probe) proc(name string) []byte {
	root := p.Proc
	if root == "" {
		root = "/proc"
	}
	b, err := os.ReadFile(root + "/" + name) // #nosec G304 -- фиксированные имена procfs
	if err != nil {
		return nil
	}
	return b
}

var (
	memTotalRe = regexp.MustCompile(`(?m)^MemTotal:\s+(\d+)`)
	memAvailRe = regexp.MustCompile(`(?m)^MemAvailable:\s+(\d+)`)
	cpuRe      = regexp.MustCompile(`(?m)^processor\s*:`)
)

// Snapshot — ServerProbe::snapshot(); pool — для размера базы (nil — нет).
func (p Probe) Snapshot(ctx context.Context, pool db.Beginner) Snapshot {
	var s Snapshot
	if f := strings.Fields(string(p.proc("loadavg"))); len(f) >= 3 {
		vals := make([]*float64, 3)
		for i := range 3 {
			if v, err := strconv.ParseFloat(f[i], 64); err == nil {
				r := money.PHPRound(v, 2) // round($load[0], 2)
				vals[i] = &r
			}
		}
		s.Load1, s.Load5, s.Load15 = vals[0], vals[1], vals[2]
	}
	if n := len(cpuRe.FindAll(p.proc("cpuinfo"), -1)); n > 0 {
		c := int32(n) // #nosec G115 -- число процессоров
		s.CPUCount = &c
	}
	info := p.proc("meminfo")
	if t, a := memTotalRe.FindSubmatch(info), memAvailRe.FindSubmatch(info); t != nil && a != nil {
		total, _ := strconv.ParseInt(string(t[1]), 10, 64)
		avail, _ := strconv.ParseInt(string(a[1]), 10, 64)
		tm, um := total/1024, (total-avail)/1024 // intdiv(…, 1024)
		s.MemoryTotalMB, s.MemoryUsedMB = &tm, &um
	}
	root := p.Root
	if root == "" {
		root = "."
	}
	var st syscall.Statfs_t
	if syscall.Statfs(root, &st) == nil && st.Blocks > 0 {
		bs := uint64(st.Bsize)                               // #nosec G115 -- размер блока положителен
		total := int64(st.Blocks * bs / 1048576)             // #nosec G115
		used := int64((st.Blocks - st.Bfree) * bs / 1048576) // #nosec G115 -- disk_total_space − disk_free_space
		s.DiskTotalMB, s.DiskUsedMB = &total, &used
	}
	if up := p.proc("uptime"); len(up) > 0 {
		if f, _, ok := bytes.Cut(up, []byte(" ")); ok {
			if v, err := strconv.ParseFloat(string(f), 64); err == nil {
				u := int64(v)
				s.UptimeSeconds = &u
			}
		}
	}
	if pool != nil {
		var mb int64
		if err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT (pg_database_size(current_database()) / 1048576)::bigint`).Scan(&mb)
		}); err == nil {
			s.DatabaseMB = &mb
		}
	}
	return s
}

// Table — крупная таблица (ServerProbe::biggestTables()).
type Table struct {
	Name      string
	Megabytes float64
	Rows      int64
}

// BiggestTables — 8 крупнейших таблиц схемы public (размер с индексами, МБ с одним знаком; строки — оценка).
func BiggestTables(ctx context.Context, pool db.Beginner) []Table {
	var out []Table
	_ = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT c.relname::text, round(pg_total_relation_size(c.oid) / 1048576.0, 1)::float8,
		       GREATEST(c.reltuples, 0)::bigint
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		ORDER BY pg_total_relation_size(c.oid) DESC LIMIT 8`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Table
			if err := rows.Scan(&t.Name, &t.Megabytes, &t.Rows); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out
}
