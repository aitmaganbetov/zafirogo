//go:build integration

// Package dbtest — Postgres для интеграционных тестов: свежая база с миграциями db/migrations (секции
// goose Up), логины с ролями zafiro_app и zafiro_platform (0004_rls.sql) и простые фикстуры.
//
// База — ZAFIRO_TEST_DATABASE_URL (в ней создаётся отдельная временная база) или контейнер testcontainers.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
)

// PostgresImage совпадает с docker-compose.dev.yml.
const PostgresImage = "postgres:18.6"

// PartitionMonthsBack — на сколько месяцев назад от текущего создаются партиции журналов
// (db.PartitionedTables). Миграции создают их с текущего месяца, а тесты с замороженными датами
// (например, 2026-10-15) пишут журнал в прошлые месяцы — без партиции вставка падает
// («no partition of relation …»). В проде прошлых месяцев нет: данные пишутся «сейчас»
// (перенос старых данных создаёт партиции сам — tools/migrate-from-mysql/README.md).
const PartitionMonthsBack = 24

// DB — тестовая база.
type DB struct {
	// Owner — владелец схемы (суперпользователь: обходит RLS) — только для фикстур и проверок.
	Owner *pgxpool.Pool
	// App — логин с ролью zafiro_app (как сервер).
	App *pgxpool.Pool
	// Platform — логин с ролью zafiro_platform (суперадминка, фоновые задачи).
	Platform *pgxpool.Pool
	// AppDSN — строка подключения логина приложения.
	AppDSN string
}

// New поднимает базу, применяет миграции и создаёт логины. Всё закрывается в t.Cleanup.
func New(t testing.TB) *DB {
	t.Helper()
	d, cleanup, err := Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return d
}

// Start — то же для TestMain: одна база на пакет; cleanup закрывает пулы и контейнер.
func Start(ctx context.Context) (d *DB, cleanup func(), err error) {
	var cleanups []func()
	cleanup = func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	adminDSN := os.Getenv("ZAFIRO_TEST_DATABASE_URL")
	if adminDSN == "" {
		ctr, err := tcpostgres.Run(ctx, PostgresImage,
			tcpostgres.WithDatabase("zafiro_test"),
			tcpostgres.WithUsername("zafiro"),
			tcpostgres.WithPassword("zafiro"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).WithStartupTimeout(60*time.Second)),
		)
		if err != nil {
			return nil, cleanup, fmt.Errorf("dbtest: start postgres: %w", err)
		}
		cleanups = append(cleanups, func() { _ = testcontainers.TerminateContainer(ctr) })
		if adminDSN, err = ctr.ConnectionString(ctx, "sslmode=disable"); err != nil {
			return nil, cleanup, err
		}
	}
	dsn, err := freshDatabase(ctx, adminDSN)
	if err != nil {
		return nil, cleanup, err
	}
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, cleanup, fmt.Errorf("dbtest: connect: %w", err)
	}
	cleanups = append(cleanups, owner.Close)
	if err := Migrate(ctx, owner); err != nil {
		return nil, cleanup, err
	}
	// Тем же механизмом, что и фоновая задача (db.EnsurePartitions): с PartitionMonthsBack назад
	// до трёх месяцев вперёд (как миграции).
	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month()-PartitionMonthsBack, 1, 0, 0, 0, 0, time.UTC)
	if _, err := db.EnsurePartitions(ctx, owner, from, PartitionMonthsBack+4); err != nil {
		return nil, cleanup, fmt.Errorf("dbtest: partitions: %w", err)
	}

	suffix, password := randomHex(4), randomHex(12)
	appUser, platformUser := "app_"+suffix, "platform_"+suffix
	for _, stmt := range []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE zafiro_app", appUser, password),
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE zafiro_platform", platformUser, password),
	} {
		if _, err := owner.Exec(ctx, stmt); err != nil {
			return nil, cleanup, fmt.Errorf("dbtest: %w", err)
		}
	}
	appDSN, err := withUser(dsn, appUser, password)
	if err != nil {
		return nil, cleanup, err
	}
	app, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, app.Close)
	platformDSN, err := withUser(dsn, platformUser, password)
	if err != nil {
		return nil, cleanup, err
	}
	platform, err := pgxpool.New(ctx, platformDSN)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, platform.Close)
	return &DB{Owner: owner, App: app, Platform: platform, AppDSN: appDSN}, cleanup, nil
}

// MigrationsDir — каталог db/migrations репозитория.
func MigrationsDir() string {
	_, file, _, _ := runtime.Caller(0)
	// server/internal/db/dbtest/dbtest.go → корень репозитория
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "db", "migrations")
}

// Migrate применяет секции «-- +goose Up» всех миграций по порядку (как goose up).
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	files, err := filepath.Glob(filepath.Join(MigrationsDir(), "*.sql"))
	if err != nil || len(files) == 0 {
		return fmt.Errorf("dbtest: migrations not found in %s: %w", MigrationsDir(), err)
	}
	sort.Strings(files)
	for _, f := range files {
		body, err := os.ReadFile(f) // #nosec G304 -- файлы миграций репозитория
		if err != nil {
			return err
		}
		// Exec без аргументов — простой протокол: несколько команд в одной строке.
		if _, err := pool.Exec(ctx, upSection(string(body))); err != nil {
			return fmt.Errorf("dbtest: migration %s: %w", filepath.Base(f), err)
		}
	}
	return nil
}

func upSection(sql string) string {
	var b strings.Builder
	inUp := false
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "-- +goose Up"):
			inUp = true
			continue
		case strings.HasPrefix(trimmed, "-- +goose Down"):
			inUp = false
			continue
		case strings.HasPrefix(trimmed, "-- +goose"):
			continue
		}
		if inUp {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// freshDatabase создаёт отдельную базу (миграции не мешают другим тестам на том же сервере).
func freshDatabase(ctx context.Context, adminDSN string) (string, error) {
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		return "", fmt.Errorf("dbtest: connect admin: %w", err)
	}
	defer admin.Close()
	name := "zafiro_t_" + randomHex(6)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return "", fmt.Errorf("dbtest: create database: %w", err)
	}
	u, err := url.Parse(adminDSN)
	if err != nil {
		return "", err
	}
	u.Path = "/" + name
	return u.String(), nil
}

func withUser(dsn, user, password string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read не возвращает ошибку (Go 1.24+)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Фикстуры (через владельца, в обход RLS)
// ---------------------------------------------------------------------------

// Club создаёт клуб и возвращает его id.
func (d *DB) Club(t testing.TB, slug string) int64 {
	t.Helper()
	var id int64
	if err := d.Owner.QueryRow(context.Background(),
		"INSERT INTO clubs (name, slug, created_at, updated_at) VALUES ($1, $1, now(), now()) RETURNING id", slug).Scan(&id); err != nil {
		t.Fatalf("dbtest: club: %v", err)
	}
	return id
}

// Customer создаёт пользователя, членство role=client в клубе и нулевой member_balances.
func (d *DB) Customer(t testing.TB, clubID int64, name string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := d.Owner.QueryRow(ctx,
		"INSERT INTO users (name, phone, password, created_at, updated_at) VALUES ($1, '+7' || lpad(floor(random()*1e10)::bigint::text, 10, '0'), 'x', now(), now()) RETURNING id",
		name).Scan(&id); err != nil {
		t.Fatalf("dbtest: user: %v", err)
	}
	d.Join(t, clubID, id)
	return id
}

// Join добавляет существующего пользователя клиентом клуба (членство + нулевой баланс).
func (d *DB) Join(t testing.TB, clubID, userID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := d.Owner.Exec(ctx,
		"INSERT INTO club_memberships (club_id, user_id, role, created_at, updated_at) VALUES ($1, $2, 'client', now(), now())",
		clubID, userID); err != nil {
		t.Fatalf("dbtest: membership: %v", err)
	}
	if _, err := d.Owner.Exec(ctx,
		"INSERT INTO member_balances (club_id, user_id, created_at, updated_at) VALUES ($1, $2, now(), now())",
		clubID, userID); err != nil {
		t.Fatalf("dbtest: balance: %v", err)
	}
}

// Friendship создаёт дружбу (accepted — принятая) и возвращает id.
func (d *DB) Friendship(t testing.TB, clubID, requesterID, addresseeID int64, accepted bool) int64 {
	t.Helper()
	var id int64
	if err := d.Owner.QueryRow(context.Background(),
		`INSERT INTO friendships (club_id, requester_id, addressee_id, accepted_at, created_at, updated_at)
		 VALUES ($1, $2, $3, CASE WHEN $4 THEN now() END, now(), now()) RETURNING id`,
		clubID, requesterID, addresseeID, accepted).Scan(&id); err != nil {
		t.Fatalf("dbtest: friendship: %v", err)
	}
	return id
}

// Balances возвращает (основной, бонусный) баланс клиента в тиынах.
func (d *DB) Balances(t testing.TB, clubID, userID int64) (int64, int64) {
	t.Helper()
	var main, bonus int64
	if err := d.Owner.QueryRow(context.Background(),
		"SELECT balance, bonus_balance FROM member_balances WHERE club_id = $1 AND user_id = $2", clubID, userID).Scan(&main, &bonus); err != nil {
		t.Fatalf("dbtest: balances: %v", err)
	}
	return main, bonus
}
