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
	"fmt"
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

// ErrInvalidClub — клуб не задан (0 или отрицательный id).
var ErrInvalidClub = errors.New("db: club id is required for a tenant transaction")

// Options — необязательные параметры тенантной транзакции.
type Options struct {
	// UserID ставит app.user_id (свои членства во всех клубах и свои коды переводов — политики 0004).
	UserID int64
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
	return withRetry(ctx, func() error {
		return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
			// Параметры — аргументами, не склейкой строки. is_local = true: только до конца транзакции.
			if _, err := tx.Exec(ctx,
				"SELECT set_config('app.club_id', $1, true), set_config('app.user_id', $2, true), set_config('lock_timeout', $3, true)",
				club, user, strconv.FormatInt(LockTimeout.Milliseconds(), 10)+"ms"); err != nil {
				return fmt.Errorf("db: set tenant: %w", err)
			}
			return fn(store.New(tx))
		})
	})
}

// InPlatformTx выполняет fn в транзакции без клуба — для пула с ролью zafiro_platform
// (суперадминка, фоновые задачи по всем клубам). Под ролью zafiro_app такая транзакция
// не видит ни одной строки тенантных таблиц.
func InPlatformTx(ctx context.Context, pool Beginner, fn func(q *store.Queries) error) error {
	return withRetry(ctx, func() error {
		return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
			return fn(store.New(tx))
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
