// Команда migrate-from-mysql — поклубный перенос данных из MySQL старого Laravel-проекта Zafiro в Postgres
// zafirogo (решение 1 в CLAUDE.md). Описание и порядок перевода клуба — tools/migrate-from-mysql/README.md.
//
//	migrate-from-mysql plan   --club <id|slug>   # сухой прогон: отчёт о данных, ничего не пишет
//	migrate-from-mysql apply  --club <id|slug>   # перенос одной транзакцией (со сверкой до коммита)
//	migrate-from-mysql verify --club <id|slug>   # сверка перенесённого клуба с MySQL
//
// Строки подключения — из окружения (секреты не в аргументах команды: их видно в списке процессов):
// ZAFIRO_MIGRATE_MYSQL_DSN (go-sql-driver: user:pass@tcp(host:3306)/db, лучше логин только для чтения) и
// ZAFIRO_MIGRATE_PG_DSN (логин владельца схемы с членством в zafiro_platform, PD-9). Флаги --mysql/--pg —
// для локальной отладки.
//
// Коды выхода: 0 — успех; 1 — ошибка; 2 — неверные аргументы; 3 — блокеры в данных (ничего не записано);
// 4 — расхождение сверки (apply откатан); 5 — клуб уже перенесён (apply ничего не делает).
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate"
)

const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitBlocked  = 3
	exitMismatch = 4
	exitMigrated = 5
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "plan" && args[0] != "apply" && args[0] != "verify") {
		fmt.Fprintln(stderr, "usage: migrate-from-mysql plan|apply|verify --club <id|slug> [flags]")
		return exitUsage
	}
	mode := args[0]
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(stderr)
	clubRef := fs.String("club", "", "id или slug клуба в MySQL")
	mysqlDSN := fs.String("mysql", os.Getenv("ZAFIRO_MIGRATE_MYSQL_DSN"), "DSN MySQL (по умолчанию ZAFIRO_MIGRATE_MYSQL_DSN)")
	pgDSN := fs.String("pg", os.Getenv("ZAFIRO_MIGRATE_PG_DSN"), "DSN Postgres владельца схемы (по умолчанию ZAFIRO_MIGRATE_PG_DSN)")
	sourceTZ := fs.String("source-tz", "UTC", "часовой пояс строк времени в MySQL: config('app.timezone') Laravel")
	retention := fs.Duration("api-log-retention", 2160*time.Hour, "срок хранения api_request_logs (integrations.request_log_retention)")
	reserve := fs.Int64("id-reserve", 1_000_000_000, "запас последовательностей id над максимальным id MySQL (Q-029)")
	legacySettings := fs.String("legacy-settings", "", "storage/app/settings.json старого проекта для клуба legacy без settings (Q-022)")
	batch := fs.Int("batch-size", 2000, "строк в одной вставке")
	jsonOut := fs.String("json", "", "файл для отчёта в JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if *clubRef == "" || *mysqlDSN == "" || *pgDSN == "" {
		fmt.Fprintln(stderr, "нужны --club, DSN MySQL и DSN Postgres (ZAFIRO_MIGRATE_MYSQL_DSN, ZAFIRO_MIGRATE_PG_DSN)")
		return exitUsage
	}
	loc, err := time.LoadLocation(*sourceTZ)
	if err != nil {
		fmt.Fprintf(stderr, "--source-tz: %v\n", err)
		return exitUsage
	}
	opts := migrate.Options{Club: *clubRef, SourceTZ: loc, APILogRetention: *retention, IDReserve: *reserve, BatchSize: *batch}
	if *legacySettings != "" {
		b, err := os.ReadFile(*legacySettings) // #nosec G304 G703 -- путь задаёт оператор переноса
		if err != nil {
			fmt.Fprintf(stderr, "--legacy-settings: %v\n", err)
			return exitUsage
		}
		opts.LegacySettingsFile = b
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	my, err := openMySQL(*mysqlDSN)
	if err != nil {
		fmt.Fprintf(stderr, "mysql: %v\n", err)
		return exitError
	}
	defer func() { _ = my.Close() }()
	pg, err := db.NewPool(ctx, *pgDSN)
	if err != nil {
		fmt.Fprintf(stderr, "postgres: %v\n", err)
		return exitError
	}
	defer pg.Close()

	var report *migrate.Report
	switch mode {
	case "plan":
		report, err = migrate.Plan(ctx, my, pg, opts)
	case "apply":
		report, err = migrate.Apply(ctx, my, pg, opts)
	case "verify":
		report, err = migrate.Verify(ctx, my, pg, opts)
	}
	if report != nil {
		if werr := report.WriteText(stdout); werr != nil {
			slog.Error("report", "err", werr)
		}
		if *jsonOut != "" {
			if werr := writeJSON(*jsonOut, report); werr != nil {
				fmt.Fprintf(stderr, "--json: %v\n", werr)
			}
		}
	}
	switch {
	case errors.Is(err, migrate.ErrAlreadyMigrated):
		fmt.Fprintln(stderr, "клуб уже перенесён: apply ничего не изменил")
		return exitMigrated
	case errors.Is(err, migrate.ErrBlocked):
		fmt.Fprintln(stderr, "найдены блокеры: ничего не записано")
		return exitBlocked
	case errors.Is(err, migrate.ErrVerifyMismatch):
		fmt.Fprintln(stderr, "сверка нашла расхождения")
		return exitMismatch
	case err != nil:
		fmt.Fprintf(stderr, "ошибка: %v\n", err)
		return exitError
	case mode == "plan" && report.Count(migrate.Blocker) > 0:
		return exitBlocked
	}
	return exitOK
}

// openMySQL — подключение к MySQL старого проекта: время и деньги читаются строками (parseTime выключен),
// кодировка utf8mb4 как в config/database.php старого проекта.
func openMySQL(dsn string) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	cfg.ParseTime = false
	cfg.Collation = "utf8mb4_unicode_ci"
	cfg.MultiStatements = false
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	my := sql.OpenDB(connector)
	my.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := my.PingContext(ctx); err != nil {
		_ = my.Close()
		return nil, err
	}
	return my, nil
}

func writeJSON(path string, report *migrate.Report) error {
	f, err := os.Create(path) // #nosec G304 G703 -- путь задаёт оператор переноса
	if err != nil {
		return err
	}
	if err := report.WriteJSON(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
