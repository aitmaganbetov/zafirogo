//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// PB-10: запрос принципала одним обращением (db.TenantRow) и BEGIN + set_config одной строкой (db.InTenantTx)
// дают ту же изоляцию, что прежняя транзакция: RLS клуба действует, настройки клуба не остаются на соединении.
func TestTenantRow(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	clubA, clubB := d.Club(t, "once-a"), d.Club(t, "once-b")
	alice := d.Customer(t, clubA, "Алиса") // член клуба A
	bob := d.Customer(t, clubB, "Боб")     // член клуба B

	// Одно соединение: что бы ни осталось после запроса, следующий запрос это увидит.
	c, err := d.App.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	conn := c.Conn()
	settings := func() (club, user string) {
		t.Helper()
		if err := conn.QueryRow(ctx, "SELECT COALESCE(current_setting('app.club_id', true), ''), COALESCE(current_setting('app.user_id', true), '')").
			Scan(&club, &user); err != nil {
			t.Fatal(err)
		}
		return club, user
	}
	principal := func(clubID, userID int64) (store.GetPrincipalAuthRow, error) {
		return store.New(db.TenantRow(conn, clubID, db.Options{UserID: userID})).
			GetPrincipalAuth(ctx, store.GetPrincipalAuthParams{ClubID: clubID, UserID: userID})
	}

	// Свой клуб — пользователь и членство одной строкой.
	row, err := principal(clubA, alice)
	if err != nil || row.ID != alice || row.MemberRole == nil || *row.MemberRole != "client" {
		t.Fatalf("own club: %+v, %v", row, err)
	}
	if club, user := settings(); club != "" || user != "" {
		t.Fatalf("settings leaked to the connection after TenantRow: club %q user %q", club, user)
	}
	// Чужой клуб — пользователь есть, членства нет.
	row, err = principal(clubB, alice)
	if err != nil || row.ID != alice || row.MemberRole != nil {
		t.Fatalf("foreign club: %+v, %v", row, err)
	}
	// Нет пользователя — ErrNoRows; соединение после ошибки чистое и рабочее.
	if _, err = principal(clubA, 1<<40); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing user: %v", err)
	}
	if club, user := settings(); club != "" || user != "" {
		t.Fatalf("settings leaked after ErrNoRows: club %q user %q", club, user)
	}
	// RLS в пачке действует: без контекста клуба и пользователя чужое членство не видно — и с контекстом клуба A
	// членство Боба в клубе B тоже (политики tenant_isolation и member_self_read).
	var n int
	if err := db.TenantRow(conn, clubA, db.Options{UserID: alice}).
		QueryRow(ctx, "SELECT count(*) FROM club_memberships WHERE user_id = $1", bob).Scan(&n); err != nil || n != 0 {
		t.Fatalf("another club's membership visible through TenantRow: %d, %v", n, err)
	}
	if err := db.TenantRow(conn, clubB, db.Options{}).
		QueryRow(ctx, "SELECT count(*) FROM club_memberships WHERE user_id = $1", bob).Scan(&n); err != nil || n != 1 {
		t.Fatalf("own club's membership through TenantRow: %d, %v", n, err)
	}
	// Клуб не задан, второй запрос, запись — отказ.
	if err := db.TenantRow(conn, 0, db.Options{}).QueryRow(ctx, "SELECT 1").Scan(&n); !errors.Is(err, db.ErrInvalidClub) {
		t.Fatalf("no club: %v", err)
	}
	once := db.TenantRow(conn, clubA, db.Options{})
	if err := once.QueryRow(ctx, "SELECT 1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := once.QueryRow(ctx, "SELECT 1").Scan(&n); err == nil {
		t.Fatal("TenantRow must serve exactly one query")
	}
	if _, err := db.TenantRow(conn, clubA, db.Options{}).Exec(ctx, "SELECT 1"); err == nil {
		t.Fatal("TenantRow must not Exec")
	}

	// InTenantTx (BEGIN + set_config одной строкой): контекст клуба и пользователя внутри, чисто после.
	err = db.InTenantTxWith(ctx, conn, clubA, db.Options{UserID: alice}, func(q *store.Queries) error {
		club, user := settings()
		if club == "" || user == "" {
			t.Errorf("no tenant context inside the transaction: club %q user %q", club, user)
		}
		var timeout string
		if err := conn.QueryRow(ctx, "SHOW lock_timeout").Scan(&timeout); err != nil || timeout != "5s" {
			t.Errorf("lock_timeout = %q, %v", timeout, err)
		}
		m, err := q.GetMembershipAuth(ctx, store.GetMembershipAuthParams{ClubID: clubA, UserID: alice})
		if err != nil || m.Role != "client" {
			t.Errorf("membership in tx: %+v, %v", m, err)
		}
		if _, err := q.GetMembershipAuth(ctx, store.GetMembershipAuthParams{ClubID: clubB, UserID: bob}); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("another club's membership visible in tx: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if club, user := settings(); club != "" || user != "" {
		t.Fatalf("settings leaked after InTenantTx: club %q user %q", club, user)
	}
	// Откат по ошибке fn — тоже чисто.
	boom := errors.New("boom")
	if err := db.InTenantTx(ctx, conn, clubA, func(*store.Queries) error { return boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if club, _ := settings(); club != "" {
		t.Fatalf("settings leaked after rollback: %q", club)
	}
}
