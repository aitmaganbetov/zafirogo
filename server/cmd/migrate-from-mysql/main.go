// Команда migrate-from-mysql — поклубный перенос данных из MySQL старого Laravel-проекта Zafiro в Postgres
// zafirogo (решение 1 в CLAUDE.md). Описание и порядок перевода клуба — tools/migrate-from-mysql/README.md.
//
//	migrate-from-mysql plan   --club <id|slug>   # сухой прогон: отчёт о данных, ничего не пишет
//	migrate-from-mysql apply  --club <id|slug>   # перенос одной транзакцией (со сверкой до коммита)
//	migrate-from-mysql verify --club <id|slug>   # сверка перенесённого клуба с MySQL
//	migrate-from-mysql platform plan|apply|verify # платформа: языки, переводы, настройки, новости, тарифы, суперадмины
//	migrate-from-mysql go-live --club <id|slug>   # открыть перенесённый клуб (imported → live, подписка — статус до заморозки)
//	migrate-from-mysql purge  --club <id|slug> --confirm <slug> # удалить перенесённый клуб до go-live (повторный перенос)
//
// Строки подключения — из окружения (секреты не в аргументах команды: их видно в списке процессов):
// ZAFIRO_MIGRATE_MYSQL_DSN (go-sql-driver: user:pass@tcp(host:3306)/db, лучше логин только для чтения) и
// ZAFIRO_MIGRATE_PG_DSN (логин владельца схемы с членством в zafiro_platform, PD-9). Флаги --mysql/--pg —
// для локальной отладки. Секреты (решение Q-050) — тоже из окружения: ZAFIRO_MIGRATE_LARAVEL_APP_KEY (APP_KEY старого
// Laravel, "base64:…"), ZAFIRO_MIGRATE_LARAVEL_PREVIOUS_KEYS (APP_PREVIOUS_KEYS, если ключ ротировали) и
// ZAFIRO_SECURITY_DATA_KEYS (ключи данных нового сервера, та же переменная, что у сервера: "kid:секрет,…").
//
// Коды выхода: 0 — успех; 1 — ошибка; 2 — неверные аргументы; 3 — блокеры в данных (ничего не записано);
// 4 — расхождение сверки (apply откатан); 5 — клуб уже перенесён (apply ничего не делает); 6 — состояние клуба не
// позволяет (purge после go-live или при операциях в zafirogo, go-live открытого клуба).
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
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/migrate"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
)

const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitBlocked  = 3
	exitMismatch = 4
	exitMigrated = 5
	exitState    = 6
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "purge" || args[0] == "go-live") {
		return runLifecycle(args, stdout, stderr)
	}
	platform := len(args) > 0 && args[0] == "platform"
	if platform {
		args = args[1:]
	}
	if len(args) == 0 || (args[0] != "plan" && args[0] != "apply" && args[0] != "verify") {
		fmt.Fprintln(stderr, "usage: migrate-from-mysql plan|apply|verify --club <id|slug> [flags]")
		fmt.Fprintln(stderr, "       migrate-from-mysql platform plan|apply|verify [flags]")
		fmt.Fprintln(stderr, "       migrate-from-mysql go-live --club <id|slug>")
		fmt.Fprintln(stderr, "       migrate-from-mysql purge --club <id|slug> --confirm <slug>")
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
	storage := fs.String("laravel-storage", "", "диск 'local' старого Laravel (storage/app/private): логотипы, картинки входа и новостей")
	seed := fs.String("translations-seed", "", "platform: каталог db/seed с translations-new.{ru,kk,en}.json (строки, которых нет)")
	thaw := fs.String("thaw-subscription", "", "статус подписки до заморозки (active|trialing): приостановленная подписка переносится в нём")
	coreOnly := fs.Bool("core-only", false, "только ядро (без расширений M2) — для отладки")
	batch := fs.Int("batch-size", 2000, "строк в одной вставке")
	jsonOut := fs.String("json", "", "файл для отчёта в JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if (*clubRef == "" && !platform) || *mysqlDSN == "" || *pgDSN == "" {
		fmt.Fprintln(stderr, "нужны --club, DSN MySQL и DSN Postgres (ZAFIRO_MIGRATE_MYSQL_DSN, ZAFIRO_MIGRATE_PG_DSN)")
		return exitUsage
	}
	loc, err := time.LoadLocation(*sourceTZ)
	if err != nil {
		fmt.Fprintf(stderr, "--source-tz: %v\n", err)
		return exitUsage
	}
	if *storage != "" {
		if st, err := os.Stat(*storage); err != nil || !st.IsDir() { // #nosec G304 G703 -- путь задаёт оператор переноса
			fmt.Fprintln(stderr, "--laravel-storage: нет такого каталога")
			return exitUsage
		}
	}
	if *thaw != "" && *thaw != "active" && *thaw != "trialing" {
		fmt.Fprintln(stderr, "--thaw-subscription: active или trialing")
		return exitUsage
	}
	laravelKeys, dataKeys, err := secretKeys()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	opts := migrate.Options{Club: *clubRef, SourceTZ: loc, APILogRetention: *retention, IDReserve: *reserve, BatchSize: *batch,
		LaravelKeys: laravelKeys, DataKeys: dataKeys, LaravelStorage: *storage, ThawSubscription: *thaw}
	if !*coreOnly {
		opts.Extensions = migrate.DefaultExtensions()
	}
	if *legacySettings != "" {
		b, err := os.ReadFile(*legacySettings) // #nosec G304 G703 -- путь задаёт оператор переноса
		if err != nil {
			fmt.Fprintf(stderr, "--legacy-settings: %v\n", err)
			return exitUsage
		}
		opts.LegacySettingsFile = b
	}
	popts := migrate.PlatformOptions{SourceTZ: loc, IDReserve: *reserve, BatchSize: *batch, LaravelKeys: laravelKeys,
		DataKeys: dataKeys, LaravelStorage: *storage}
	if platform && *seed != "" {
		if popts.TranslationsSeed, err = i18n.LoadSeed(*seed); err != nil {
			fmt.Fprintf(stderr, "--translations-seed: %v\n", err)
			return exitUsage
		}
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
	switch {
	case platform && mode == "plan":
		report, err = migrate.PlatformPlan(ctx, my, pg, popts)
	case platform && mode == "apply":
		report, err = migrate.PlatformApply(ctx, my, pg, popts)
	case platform:
		report, err = migrate.PlatformVerify(ctx, my, pg, popts)
	case mode == "plan":
		report, err = migrate.Plan(ctx, my, pg, opts)
	case mode == "apply":
		report, err = migrate.Apply(ctx, my, pg, opts)
	default:
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
	case mode == "plan" && report != nil && report.Count(migrate.Blocker) > 0:
		return exitBlocked
	}
	return exitOK
}

// runLifecycle — go-live и purge перенесённого клуба (только Postgres: MySQL не нужен).
func runLifecycle(args []string, stdout, stderr io.Writer) int {
	mode := args[0]
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(stderr)
	clubRef := fs.String("club", "", "id или slug клуба в zafirogo")
	confirm := fs.String("confirm", "", "purge: slug клуба (подтверждение)")
	pgDSN := fs.String("pg", os.Getenv("ZAFIRO_MIGRATE_PG_DSN"), "DSN Postgres владельца схемы (по умолчанию ZAFIRO_MIGRATE_PG_DSN)")
	jsonOut := fs.String("json", "", "файл для отчёта в JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if *clubRef == "" || *pgDSN == "" || (mode == "purge" && *confirm == "") {
		fmt.Fprintln(stderr, "нужны --club и DSN Postgres (ZAFIRO_MIGRATE_PG_DSN); для purge — --confirm <slug клуба>")
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pg, err := db.NewPool(ctx, *pgDSN)
	if err != nil {
		fmt.Fprintf(stderr, "postgres: %v\n", err)
		return exitError
	}
	defer pg.Close()
	var report *migrate.Report
	if mode == "purge" {
		report, err = migrate.Purge(ctx, pg, *clubRef, *confirm)
	} else {
		report, err = migrate.GoLive(ctx, pg, *clubRef)
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
	case errors.Is(err, migrate.ErrConfirm):
		fmt.Fprintln(stderr, "--confirm не совпадает со slug клуба: ничего не удалено")
		return exitUsage
	case errors.Is(err, migrate.ErrClubState), errors.Is(err, migrate.ErrClubInUse):
		fmt.Fprintln(stderr, "состояние клуба не позволяет: ничего не изменено")
		return exitState
	case err != nil:
		fmt.Fprintf(stderr, "ошибка: %v\n", err)
		return exitError
	}
	return exitOK
}

// secretKeys — ключи из окружения (не из аргументов: их видно в списке процессов). Пустые переменные — nil
// (секреты не переносятся: при наличии секретов в данных — блокер в отчёте).
func secretKeys() (*migrate.LaravelKeys, *secretbox.Box, error) {
	var laravel *migrate.LaravelKeys
	if v := os.Getenv("ZAFIRO_MIGRATE_LARAVEL_APP_KEY"); v != "" {
		k, err := migrate.ParseLaravelKeys(v, os.Getenv("ZAFIRO_MIGRATE_LARAVEL_PREVIOUS_KEYS"))
		if err != nil {
			return nil, nil, errors.New("ZAFIRO_MIGRATE_LARAVEL_APP_KEY / _PREVIOUS_KEYS: ожидается \"base64:…\" на 32 байта (AES-256-CBC)")
		}
		laravel = k
	}
	var box *secretbox.Box
	if v := os.Getenv("ZAFIRO_SECURITY_DATA_KEYS"); v != "" {
		var keys []secretbox.Key
		for part := range strings.SplitSeq(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, secret, ok := strings.Cut(part, ":")
			if !ok || id == "" || secret == "" {
				return nil, nil, errors.New("ZAFIRO_SECURITY_DATA_KEYS: ожидается kid:секрет[,kid:секрет]")
			}
			keys = append(keys, secretbox.Key{ID: id, Secret: secret})
		}
		b, err := secretbox.NewKeyring(keys, "")
		if err != nil {
			return nil, nil, errors.New("ZAFIRO_SECURITY_DATA_KEYS: " + err.Error())
		}
		box = b
	}
	return laravel, box, nil
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
