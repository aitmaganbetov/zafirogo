// Package leader — «один исполнитель на кластер» для фоновых задач (Q-086, docs/parts/50-jobs.md п. 17).
//
// Механизм — сессионный advisory lock Postgres pg_try_advisory_lock(hashtext('job:<name>')) на выделенном
// соединении пула. Не взяли — задачу сейчас выполняет другой инстанс. Лок живёт, пока живо соединение:
// при падении процесса или обрыве связи Postgres снимает его сам, и задачу подхватывает другой инстанс.
// Ключ совпадает с server/internal/worker/expire_sessions.go (hashtext('job:'+имя)), поэтому обе
// реализации исключают друг друга.
//
// Два способа:
//   - RunOnce — периодическая задача: взять лок в начале тика, выполнить, снять (как withoutOverlapping +
//     onOneServer в Laravel);
//   - Hold — постоянный воркер (публикатор outbox): ждать лидерства, держать его, пока работает fn.
//
// Пул должен подключаться ролью zafiro_platform (задачи по всем клубам). Advisory lock не зависит от RLS.
package leader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Key — строка, из которой Postgres считает ключ блокировки: hashtext('job:<name>').
func Key(name string) string { return "job:" + name }

// Lock — взятая блокировка и соединение, на котором она держится. Все запросы, которые должны выполняться
// «под лидерством», лучше делать на Conn(): тогда обрыв соединения одновременно снимает лок и прерывает работу.
type Lock struct {
	conn *pgxpool.Conn
	key  string
}

// Conn — выделенное соединение, держащее лок. Не использовать после Release.
func (l *Lock) Conn() *pgxpool.Conn { return l.conn }

// TryAcquire берёт выделенное соединение и пытается взять лок задачи name. Лок занят — (nil, nil).
func TryAcquire(ctx context.Context, pool *pgxpool.Pool, name string) (*Lock, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("leader: acquire connection: %w", err)
	}
	key := Key(name)
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", key).Scan(&locked); err != nil {
		conn.Release()
		return nil, fmt.Errorf("leader: advisory lock: %w", err)
	}
	if !locked {
		conn.Release()
		return nil, nil
	}
	return &Lock{conn: conn, key: key}, nil
}

// Release снимает лок и возвращает соединение в пул. Работает и при отменённом контексте вызывающего.
// Если снять лок не удалось (обрыв связи), соединение закрывается: в пул не должно вернуться соединение
// с невыясненным состоянием лока.
func (l *Lock) Release() {
	if l == nil || l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ok bool
	if err := l.conn.QueryRow(ctx, "SELECT pg_advisory_unlock(hashtext($1))", l.key).Scan(&ok); err != nil || !ok {
		_ = l.conn.Conn().Close(ctx)
	}
	l.conn.Release()
	l.conn = nil
}

// RunOnce — один тик периодической задачи под локом: ran = false, если задачу сейчас выполняет другой
// исполнитель. fn получает соединение, держащее лок (запросы тика лучше выполнять на нём).
func RunOnce(ctx context.Context, pool *pgxpool.Pool, name string, fn func(ctx context.Context, conn *pgxpool.Conn) error) (ran bool, err error) {
	lock, err := TryAcquire(ctx, pool, name)
	if err != nil || lock == nil {
		return false, err
	}
	defer lock.Release()
	return true, fn(ctx, lock.Conn())
}

// HoldOptions — параметры Hold.
type HoldOptions struct {
	// Retry — как часто пробовать взять лидерство, пока оно у другого инстанса (0 — 5 с).
	Retry time.Duration
	// Log — журнал (nil — slog.Default()).
	Log *slog.Logger
}

// Hold ждёт лидерства в задаче name и выполняет fn, пока она не вернётся; затем снимает лок и снова ждёт
// (если fn вернула ошибку — например, оборвалось соединение). Возвращается при отмене ctx.
//
// fn должна регулярно (не реже интервала своей работы) обращаться к соединению lock.Conn(): только так
// исполнитель узнаёт, что связь с Postgres (а значит, и лок) потеряна. Короткое раздвоение лидерства при
// обрыве связи возможно — работа под локом должна быть идемпотентной (outbox: SKIP LOCKED + дедупликация
// JetStream).
func Hold(ctx context.Context, pool *pgxpool.Pool, name string, opts HoldOptions, fn func(ctx context.Context, lock *Lock) error) error {
	retry := opts.Retry
	if retry <= 0 {
		retry = 5 * time.Second
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	for {
		lock, err := TryAcquire(ctx, pool, name)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("leader: acquire failed", "job", name, "err", err)
		case lock != nil:
			log.Info("leader: became leader", "job", name)
			ferr := fn(ctx, lock)
			lock.Release()
			if ctx.Err() != nil {
				return nil
			}
			if ferr != nil && !errors.Is(ferr, context.Canceled) {
				log.Warn("leader: worker stopped, re-electing", "job", name, "err", ferr)
			}
		}
		t := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}
