package db

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// BatchSender — то, что умеет отправить пачку запросов одним обращением (*pgxpool.Pool, *pgx.Conn).
type BatchSender interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

var _ BatchSender = (*pgxpool.Pool)(nil)

// TenantRow — ОДИН читающий запрос :one в контексте клуба одним обращением к Postgres (PB-10) вместо четырёх
// (BEGIN, set_config, запрос, COMMIT). Пачка pgx без явного BEGIN выполняется сервером как одна неявная транзакция,
// поэтому set_config(…, true) (= SET LOCAL) действует на запрос пачки и исчезает вместе с ней — соединение
// возвращается в пул чистым, как после InTenantTx (проверяет TestTenantRow).
//
// Только для одного запроса QueryRow (метод :one sqlc), который ничего не пишет: повтора при deadlock и
// lock_timeout здесь нет. Всё остальное — InTenantTx.
//
//	row, err := store.New(db.TenantRow(pool, clubID, db.Options{UserID: userID})).GetPrincipalAuth(ctx, …)
func TenantRow(pool BatchSender, clubID int64, opts Options) store.DBTX {
	return &tenantOnce{pool: pool, clubID: clubID, userID: opts.UserID}
}

type tenantOnce struct {
	pool   BatchSender
	clubID int64
	userID int64
	used   bool
}

// errTenantRowMisuse — TenantRow использован не для одного QueryRow.
var errTenantRowMisuse = errors.New("db: TenantRow serves exactly one QueryRow")

func (t *tenantOnce) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errTenantRowMisuse
}

func (t *tenantOnce) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errTenantRowMisuse
}

func (t *tenantOnce) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if t.used {
		return errRow{errTenantRowMisuse}
	}
	t.used = true
	if t.clubID <= 0 {
		return errRow{ErrInvalidClub}
	}
	user := ""
	if t.userID > 0 {
		user = strconv.FormatInt(t.userID, 10)
	}
	b := &pgx.Batch{}
	// Параметры — аргументами, не склейкой строки. is_local = true: только до конца (неявной) транзакции пачки.
	b.Queue("SELECT set_config('app.club_id', $1, true), set_config('app.user_id', $2, true)",
		strconv.FormatInt(t.clubID, 10), user)
	b.Queue(sql, args...)
	br := t.pool.SendBatch(ctx, b)
	if _, err := br.Exec(); err != nil {
		_ = br.Close()
		return errRow{err}
	}
	return &batchRow{br: br}
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// batchRow — строка второго запроса пачки; после Scan пачка закрывается (соединение возвращается в пул).
type batchRow struct{ br pgx.BatchResults }

func (r *batchRow) Scan(dest ...any) error {
	err := r.br.QueryRow().Scan(dest...)
	if cerr := r.br.Close(); err == nil {
		err = cerr
	}
	return err
}
