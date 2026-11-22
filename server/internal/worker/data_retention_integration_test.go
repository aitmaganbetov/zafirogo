//go:build integration

package worker_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// retentionEnv — база с двумя клубами: работающим (live) и перенесённым, ещё не открытым (imported).
type retentionEnv struct {
	t        *testing.T
	ctx      context.Context
	d        *dbtest.DB
	now      time.Time
	live     int64
	imported int64
	user     int64
}

func newRetentionEnv(t *testing.T) *retentionEnv {
	t.Helper()
	e := &retentionEnv{t: t, ctx: context.Background(), d: dbtest.New(t), now: time.Now().UTC().Truncate(time.Second)}
	e.live = e.d.Club(t, "live")
	e.imported = e.scalar(`INSERT INTO clubs (name, slug, migration_state, created_at, updated_at)
		VALUES ('imported', 'imported', 'imported', now(), now()) RETURNING id`)
	e.user = e.d.Customer(t, e.live, "Клиент")
	e.d.Join(t, e.imported, e.user)
	return e
}

func (e *retentionEnv) scalar(sql string, args ...any) int64 {
	e.t.Helper()
	var v int64
	if err := e.d.Owner.QueryRow(e.ctx, sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (e *retentionEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.d.Owner.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// ids — значения колонки col оставшихся строк таблицы (по порядку col).
func (e *retentionEnv) ids(table, col string) []string {
	e.t.Helper()
	rows, err := e.d.Owner.Query(e.ctx, fmt.Sprintf("SELECT %s::text FROM %s ORDER BY 1", col, table))
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *retentionEnv) ago(d time.Duration) time.Time { return e.now.Add(-d) }

func (e *retentionEnv) tick(batch int32) map[string]int64 {
	e.t.Helper()
	w := &worker.DataRetention{Platform: e.d.Platform, Batch: batch, Now: func() time.Time { return e.now }}
	ran, deleted, err := w.Tick(e.ctx)
	if err != nil || !ran {
		e.t.Fatalf("tick: ran=%v err=%v", ran, err)
	}
	return deleted
}

func (e *retentionEnv) computer(club int64, number int) int64 {
	zone := e.scalar(`INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at)
		VALUES ($1, $2, 0, now(), now()) RETURNING id`, club, fmt.Sprintf("Z%d", number))
	return e.scalar(`INSERT INTO club_computers (club_id, club_zone_id, number, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'online', now(), now()) RETURNING id`, club, zone, number)
}

const day = 24 * time.Hour

// GS-375: integrations:prune удаляет ключи идемпотентности старше 24 ч и финальные доставки вебхуков старше 30 дней;
// pending и свежие остаются (журнал запросов — партициями, TestLogRetention).
func TestIntegrationsPrune(t *testing.T) {
	t.Run("GS-375", func(t *testing.T) {
		e := newRetentionEnv(t)
		key := func(club int64) int64 {
			return e.scalar(`INSERT INTO api_keys (club_id, created_by, name, prefix, key_hash, scopes, created_at, updated_at)
				VALUES ($1, $2, 'k', $3, repeat('0', 64), '[]', now(), now()) RETURNING id`, club, e.user, fmt.Sprintf("zk_%d", club))
		}
		liveKey, importedKey := key(e.live), key(e.imported)
		idem := func(club, apiKey int64, name string, at time.Time) {
			e.exec(`INSERT INTO api_idempotency_keys (club_id, api_key_id, idempotency_key, request_hash, created_at)
				VALUES ($1, $2, $3, repeat('0', 64), $4)`, club, apiKey, name, at)
		}
		idem(e.live, liveKey, "old", e.ago(25*time.Hour))
		idem(e.live, liveKey, "new", e.now)
		idem(e.live, liveKey, "edge", e.ago(23*time.Hour))
		idem(e.imported, importedKey, "imported-old", e.ago(25*time.Hour))

		endpoint := func(club int64) int64 {
			return e.scalar(`INSERT INTO webhook_endpoints (club_id, created_by, url, secret, events, created_at, updated_at)
				VALUES ($1, $2, 'https://example.test/hook', 'v1:test:AAAA', '["*"]', now(), now()) RETURNING id`, club, e.user)
		}
		liveHook, importedHook := endpoint(e.live), endpoint(e.imported)
		delivery := func(club, hook int64, event, status string, at time.Time) {
			e.exec(`INSERT INTO webhook_deliveries (club_id, webhook_endpoint_id, event_id, event, payload, status, created_at, updated_at)
				VALUES ($1, $2, gen_random_uuid(), $3, '{}', $4, $5, $5)`, club, hook, event, status, at)
		}
		delivery(e.live, liveHook, "delivered", "delivered", e.ago(31*day))
		delivery(e.live, liveHook, "pending", "pending", e.ago(31*day))
		delivery(e.live, liveHook, "failed", "failed", e.ago(2*day))
		delivery(e.live, liveHook, "failed-old", "failed", e.ago(31*day))
		delivery(e.live, liveHook, "skipped-old", "skipped", e.ago(31*day))
		delivery(e.imported, importedHook, "imported-delivered", "delivered", e.ago(31*day))

		deleted := e.tick(0)
		if got, want := e.ids("api_idempotency_keys", "idempotency_key"), []string{"edge", "imported-old", "new"}; !slices.Equal(got, want) {
			t.Fatalf("idempotency keys = %v, want %v", got, want)
		}
		if got, want := e.ids("webhook_deliveries", "event"), []string{"failed", "imported-delivered", "pending"}; !slices.Equal(got, want) {
			t.Fatalf("webhook deliveries = %v, want %v", got, want)
		}
		if deleted["api_idempotency_keys"] != 1 || deleted["webhook_deliveries"] != 3 {
			t.Fatalf("deleted = %v", deleted)
		}
	})
}

// BR-034, session-purchases:prune: части сессий старше 8 дней удаляются, кроме частей идущих сессий (длинная сессия
// не теряет части); части сессий, кончившихся по времени (GAP-005), больше не остаются навсегда.
func TestSessionPurchasesPrune(t *testing.T) {
	e := newRetentionEnv(t)
	pc := e.computer(e.live, 1)
	e.exec("UPDATE club_computers SET current_session_id = 'long-session', current_session_user_id = $2 WHERE id = $1", pc, e.user)
	purchase := func(club int64, session, name string, at time.Time) {
		e.exec(`INSERT INTO session_purchases (club_id, session_id, user_id, kind, name, minutes, created_at, updated_at)
			VALUES ($1, $2, $3, 'minutes', $4, 60, $5, $5)`, club, session, e.user, name, at)
	}
	purchase(e.live, "expired-session", "old-1", e.ago(9*day))
	purchase(e.live, "expired-session", "old-2", e.ago(8*day+time.Minute))
	purchase(e.live, "long-session", "long-old", e.ago(9*day))
	purchase(e.live, "long-session", "long-new", e.ago(time.Hour))
	purchase(e.live, "recent-session", "recent", e.ago(8*day-time.Minute))
	purchase(e.imported, "imported-session", "imported-old", e.ago(9*day))

	deleted := e.tick(0)
	if got, want := e.ids("session_purchases", "name"), []string{"imported-old", "long-new", "long-old", "recent"}; !slices.Equal(got, want) {
		t.Fatalf("session purchases = %v, want %v", got, want)
	}
	if deleted["session_purchases"] != 2 {
		t.Fatalf("deleted = %v", deleted)
	}

	// Сессия закончилась — её старые части удаляет следующий тик.
	e.exec("UPDATE club_computers SET current_session_id = NULL, current_session_user_id = NULL WHERE id = $1", pc)
	e.tick(0)
	if got, want := e.ids("session_purchases", "name"), []string{"imported-old", "long-new", "recent"}; !slices.Equal(got, want) {
		t.Fatalf("after session end = %v, want %v", got, want)
	}
}

// Очистка идёт пачками: старых строк больше пачки — удаляются все за один тик, свежие остаются.
func TestDataRetentionBatches(t *testing.T) {
	e := newRetentionEnv(t)
	for i := range 7 {
		e.exec(`INSERT INTO session_purchases (club_id, session_id, user_id, kind, name, minutes, created_at, updated_at)
			VALUES ($1, $2, $3, 'minutes', $4, 60, $5, $5)`, e.live, fmt.Sprintf("s%d", i), e.user, fmt.Sprintf("old-%d", i), e.ago(10*day))
	}
	e.exec(`INSERT INTO session_purchases (club_id, session_id, user_id, kind, name, minutes, created_at, updated_at)
		VALUES ($1, 'fresh', $2, 'minutes', 'fresh', 60, $3, $3)`, e.live, e.user, e.now)
	if deleted := e.tick(2); deleted["session_purchases"] != 7 {
		t.Fatalf("deleted = %v, want 7 session purchases", deleted)
	}
	if got := e.ids("session_purchases", "name"); !slices.Equal(got, []string{"fresh"}) {
		t.Fatalf("left = %v", got)
	}
	// Повтор — удалять нечего.
	if deleted := e.tick(2); deleted["session_purchases"] != 0 {
		t.Fatalf("second tick deleted %v", deleted)
	}

	// Лок задачи у другого исполнителя — тик пропускается.
	lock, err := leader.TryAcquire(e.ctx, e.d.Platform, worker.DataRetentionJob)
	if err != nil || lock == nil {
		t.Fatalf("acquire: %v %v", lock, err)
	}
	defer lock.Release()
	w := &worker.DataRetention{Platform: e.d.Platform}
	if ran, _, err := w.Tick(e.ctx); ran || err != nil {
		t.Fatalf("tick under foreign lock: ran=%v err=%v", ran, err)
	}
}

// agent-qr-logins:prune (истёкшие больше суток назад) и решение A2 §8: коды регистрации ПК старше 30 дней,
// сертификаты, истёкшие больше 90 дней назад (действующие и отозванные).
func TestAgentDataPrune(t *testing.T) {
	e := newRetentionEnv(t)
	livePC, importedPC := e.computer(e.live, 1), e.computer(e.imported, 1)

	qr := func(club, pc int64, hash string, expires time.Time) {
		e.exec(`INSERT INTO agent_qr_logins (id, club_id, club_computer_id, code_hash, expires_at, created_at, updated_at)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, $4, $4)`, club, pc, hash, expires)
	}
	qr(e.live, livePC, "qr-old", e.ago(25*time.Hour))
	qr(e.live, livePC, "qr-day", e.ago(23*time.Hour))
	qr(e.live, livePC, "qr-open", e.now.Add(5*time.Minute))
	qr(e.imported, importedPC, "qr-imported", e.ago(25*time.Hour))

	code := func(club int64, created, expires time.Time) int64 {
		h := make([]byte, 32)
		if _, err := rand.Read(h); err != nil {
			t.Fatal(err)
		}
		return e.scalar(`INSERT INTO agent_enrollment_codes (club_id, code_hash, created_at, expires_at)
			VALUES ($1, $2, $3, $4) RETURNING id`, club, h, created, expires)
	}
	oldCode := code(e.live, e.ago(31*day), e.ago(31*day-15*time.Minute))
	code(e.live, e.ago(29*day), e.ago(29*day-15*time.Minute))
	code(e.live, e.ago(31*day), e.now.Add(time.Hour)) // ещё действует — не удаляется
	code(e.imported, e.ago(31*day), e.ago(31*day-15*time.Minute))

	cert := func(club, pc int64, serial string, notAfter time.Time, revoked bool, code *int64) {
		e.exec(`INSERT INTO agent_certificates (serial, club_id, club_computer_id, spki_sha256, not_before, not_after, issued_at,
				issued_via, enrollment_code_id, revoked_at, revoke_reason)
			VALUES ($1, $2, $3, $4, $5::timestamptz - interval '30 days', $5, $5::timestamptz - interval '30 days', 'enroll', $6,
				CASE WHEN $7 THEN $5::timestamptz - interval '1 day' END, CASE WHEN $7 THEN 'staff' END)`,
			serial, club, pc, make([]byte, 32), notAfter, code, revoked)
	}
	cert(e.live, livePC, "a1", e.ago(91*day), false, &oldCode)
	cert(e.live, livePC, "a2", e.ago(91*day), true, nil)
	cert(e.live, livePC, "a3", e.ago(89*day), true, nil)
	cert(e.live, livePC, "a4", e.now.Add(300*day), false, nil)
	cert(e.imported, importedPC, "b1", e.ago(91*day), false, nil)

	deleted := e.tick(0)
	if got, want := e.ids("agent_qr_logins", "code_hash"), []string{"qr-day", "qr-imported", "qr-open"}; !slices.Equal(got, want) {
		t.Fatalf("qr logins = %v, want %v", got, want)
	}
	if got := e.ids("agent_enrollment_codes", "id"); len(got) != 3 || slices.Contains(got, fmt.Sprint(oldCode)) {
		t.Fatalf("enrollment codes = %v, want 3 without %d", got, oldCode)
	}
	if got, want := e.ids("agent_certificates", "serial"), []string{"a3", "a4", "b1"}; !slices.Equal(got, want) {
		t.Fatalf("certificates = %v, want %v", got, want)
	}
	if deleted["agent_qr_logins"] != 1 || deleted["agent_enrollment_codes"] != 1 || deleted["agent_certificates"] != 2 {
		t.Fatalf("deleted = %v", deleted)
	}
}
