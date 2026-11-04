//go:build integration

// Package migratetest — MySQL старого проекта для интеграционных тестов переноса: НАСТОЯЩАЯ схема (миграции
// Laravel, php artisan migrate) и демо-данные, записанные старым кодом (tools/visual/old/seed.php) —
// server/cmd/migrate-from-mysql/testdata, генерирует tools/migrate-from-mysql/dump-schema.sh.
package migratetest

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Image — версия MySQL старого проекта (как в dump-schema.sh).
const Image = "mirror.gcr.io/library/mysql:8.4.13"

// Testdata — путь к файлу server/cmd/migrate-from-mysql/testdata.
func Testdata(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "cmd", "migrate-from-mysql", "testdata", name)
}

// StartMySQL поднимает MySQL со схемой и демо-данными; возвращает пул и DSN (go-sql-driver). Закрывается в t.Cleanup.
// extraArgs — дополнительные параметры mysqld (замер объёмов: --skip-log-bin, чтобы binlog не занимал диск).
func StartMySQL(t testing.TB, extraArgs ...string) (*sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, Image,
		testcontainers.WithEnv(map[string]string{"MYSQL_ALLOW_EMPTY_PASSWORD": "yes", "MYSQL_DATABASE": "zafiro"}),
		// Кодировка и сравнение — как config/database.php старого проекта.
		testcontainers.WithCmd(append([]string{"--character-set-server=utf8mb4", "--collation-server=utf8mb4_unicode_ci"}, extraArgs...)...),
		testcontainers.WithExposedPorts("3306/tcp"),
		testcontainers.WithFiles(
			// Клиент mysql скрипта инициализации — utf8mb4 (в дампе нет SET NAMES: без этого кириллица в демо-данных
			// загружалась бы как latin1 → «Ð½Ð°…»).
			testcontainers.ContainerFile{Reader: strings.NewReader("[client]\ndefault-character-set=utf8mb4\n"),
				ContainerFilePath: "/etc/mysql/conf.d/client-utf8mb4.cnf", FileMode: 0o644},
			testcontainers.ContainerFile{HostFilePath: Testdata("schema.sql"), ContainerFilePath: "/docker-entrypoint-initdb.d/01-schema.sql", FileMode: 0o644},
			testcontainers.ContainerFile{HostFilePath: Testdata("demo-data.sql"), ContainerFilePath: "/docker-entrypoint-initdb.d/02-data.sql", FileMode: 0o644},
		),
		// «port: 3306» — окончательный сервер (временный сервер инициализации слушает только сокет).
		testcontainers.WithWaitStrategy(wait.ForAll(
			wait.ForLog("port: 3306").WithStartupTimeout(3*time.Minute),
			wait.ForListeningPort("3306/tcp"),
		)),
	)
	if err != nil {
		t.Fatalf("start mysql: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Net, cfg.Addr, cfg.DBName = "root", "tcp", fmt.Sprintf("%s:%s", host, port.Port()), "zafiro"
	cfg.Collation = "utf8mb4_unicode_ci"
	dsn := cfg.FormatDSN()
	my, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = my.Close() })
	return my, dsn
}
