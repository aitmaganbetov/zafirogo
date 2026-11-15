// Package db — пул pgx и транзакции с тенантным контекстом для RLS (db/migrations/0004_rls.sql).
//
// Правила:
//   - запросы к данным клуба — только внутри InTenantTx: транзакция ставит app.club_id через
//     set_config(..., true) (= SET LOCAL), значение живёт до конца транзакции, соединение
//     возвращается в пул чистым. SET без LOCAL запрещён — значение «утекло» бы в чужой запрос;
//   - клуб берётся из аутентификации (сессия, сертификат ПК, API-ключ), никогда из параметров запроса;
//   - InPlatformTx — осознанный обход изоляции (суперадминка, фоновые задачи по всем клубам):
//     пул должен подключаться логином с ролью zafiro_platform;
//   - конкурентные списания сериализуются SELECT … FOR UPDATE по member_balances
//     (store.LockMemberBalances — аналог lockForUpdate() старого кода, BR-005); при deadlock или
//     serialization failure вся транзакция повторяется (fn должна быть повторяемой: всё, что она
//     делает, — в этой транзакции).
package db

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Beginner — то, что умеет открыть транзакцию (*pgxpool.Pool, *pgx.Conn).
type Beginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

var _ Beginner = (*pgxpool.Pool)(nil)

// MaxAttempts — сколько раз выполняется транзакция при deadlock/serialization failure.
const MaxAttempts = 3

// LockTimeout — сколько транзакция ждёт блокировку строки, прежде чем упасть (а не висеть).
const LockTimeout = 5 * time.Second

// Таймауты транзакции клуба на стороне Postgres (PB-17) — страховка там, где у контекста нет дедлайна (воркеры,
// агентские пути) или соединение «забыто» в открытой транзакции (PB-01: все соединения пула «idle in transaction»
// до таймаута запроса). Значения с запасом: HTTP-запрос ограничен 30 с, самые долгие запросы панели — секунды.
// Ставятся через set_config(…, true) в той же команде, что app.club_id, — только до конца транзакции, без лишнего
// обращения к серверу. Транзакции без клуба (InPlatformTx: суперадминка, воркеры по всем клубам, перенос данных,
// миграции) не ограничиваются — там бывают долгие законные операции.
const (
	// StatementTimeout — предел одной команды в транзакции клуба.
	StatementTimeout = 2 * time.Minute
	// IdleInTxTimeout — сколько транзакция клуба может простаивать между командами (сервер рвёт соединение).
	IdleInTxTimeout = time.Minute
)

// ErrInvalidClub — клуб не задан (0 или отрицательный id).
var ErrInvalidClub = errors.New("db: club id is required for a tenant transaction")

// Options — необязательные параметры тенантной транзакции.
type Options struct {
	// UserID ставит app.user_id (свои членства во всех клубах и свои коды переводов — политики 0004).
	UserID int64
	// NoTimeouts — не ставить StatementTimeout / IdleInTxTimeout (долгая законная операция в транзакции клуба).
	NoTimeouts bool
}

// InTenantTx выполняет fn в транзакции клуба clubID: BEGIN; set_config('app.club_id', …, true); fn; COMMIT.
// При deadlock (40P01) или serialization failure (40001) транзакция повторяется до MaxAttempts раз.
func InTenantTx(ctx context.Context, pool Beginner, clubID int64, fn func(q *store.Queries) error) error {
	return InTenantTxWith(ctx, pool, clubID, Options{}, fn)
}

// InTenantTxWith — InTenantTx с дополнительными параметрами.
func InTenantTxWith(ctx context.Context, pool Beginner, clubID int64, opts Options, fn func(q *store.Queries) error) error {
	if clubID <= 0 {
		return ErrInvalidClub
	}
	club := strconv.FormatInt(clubID, 10)
	user := ""
	if opts.UserID > 0 {
		user = strconv.FormatInt(opts.UserID, 10)
	}
	// BEGIN и set_config — одним обращением к Postgres (PB-10: было два на каждую тенантную транзакцию). pgx
	// отправляет запрос начала транзакции без аргументов простым протоколом, который принимает несколько команд в
	// одной строке. В строку попадают только десятичные числа из strconv.FormatInt (id клуба и пользователя) и
	// константа LockTimeout — данных из запроса клиента здесь нет, внедрить в неё нечего. is_local = true: только
	// до конца транзакции. Сбой любой из команд — pgx закрывает соединение (оно не возвращается в пул с открытой
	// транзакцией).
	begin := "BEGIN; SELECT set_config('app.club_id', '" + club + "', true), set_config('app.user_id', '" + user +
		"', true), set_config('lock_timeout', '" + strconv.FormatInt(LockTimeout.Milliseconds(), 10) + "ms', true)"
	if !opts.NoTimeouts {
		// PB-17: таймауты команды и простоя транзакции клуба (константы; см. StatementTimeout) — той же командой.
		begin += ", set_config('statement_timeout', '" + strconv.FormatInt(StatementTimeout.Milliseconds(), 10) +
			"ms', true), set_config('idle_in_transaction_session_timeout', '" +
			strconv.FormatInt(IdleInTxTimeout.Milliseconds(), 10) + "ms', true)"
	}
	return withRetry(ctx, func() error {
		return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{BeginQuery: begin}, func(tx pgx.Tx) error {
			return fn(NewQueries(tx)) // запросы с необязательными фильтрами — с планом под значения (plan.go, PB-02)
		})
	})
}

// InPlatformTx выполняет fn в транзакции без клуба — для пула с ролью zafiro_platform
// (суперадминка, фоновые задачи по всем клубам). Под ролью zafiro_app такая транзакция
// не видит ни одной строки тенантных таблиц.
func InPlatformTx(ctx context.Context, pool Beginner, fn func(q *store.Queries) error) error {
	return withRetry(ctx, func() error {
		return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
			return fn(NewQueries(tx))
		})
	})
}

// Retryable сообщает, что ошибку лечит повтор всей транзакции.
func Retryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "40001", // serialization_failure
		"40P01": // deadlock_detected
		return true
	}
	return false
}

func withRetry(ctx context.Context, run func() error) error {
	var err error
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		err = run()
		if err == nil || !Retryable(err) || attempt == MaxAttempts {
			return err
		}
		// Короткая растущая пауза: 10 мс, 20 мс.
		t := time.NewTimer(time.Duration(attempt) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return errors.Join(err, ctx.Err())
		case <-t.C:
		}
	}
	return err
}
