//go:build integration

package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
)

var client valkey.Client

func TestMain(m *testing.M) {
	c, cleanup, err := authtest.StartValkey(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client = c
	code := m.Run()
	cleanup()
	os.Exit(code)
}

var prefixSeq atomic.Int64

type clock struct{ t atomic.Int64 }

func (c *clock) now() time.Time      { return time.UnixMilli(c.t.Load()) }
func (c *clock) add(d time.Duration) { c.t.Add(d.Milliseconds()) }
func newClock() *clock               { c := &clock{}; c.t.Store(time.Now().UnixMilli()); return c }
func keys() []Key                    { return []Key{{ID: "k1", Secret: bytes.Repeat([]byte{9}, 32)}} }
func prefix() string                 { return fmt.Sprintf("t%d:", prefixSeq.Add(1)) }

func newManager(t *testing.T, c *clock) *Manager {
	t.Helper()
	m, err := New(Options{Client: client, Keys: keys(), Prefix: prefix(), Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCreateAuthenticate(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	m := newManager(t, c)
	tok, err := m.Create(ctx, 42, 7, false)
	if err != nil {
		t.Fatal(err)
	}
	if tok.RefreshMaxAge != 120*time.Minute {
		t.Errorf("max age %v", tok.RefreshMaxAge)
	}
	s, err := m.Authenticate(ctx, tok.Access)
	if err != nil || s.UserID != 42 || s.ClubID != 7 || s.ID != tok.SessionID {
		t.Fatalf("Authenticate = %+v, %v", s, err)
	}
	// Access-токен истекает через AccessTTL.
	c.add(11 * time.Minute)
	if _, err := m.Authenticate(ctx, tok.Access); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired access: %v", err)
	}
}

// Ротация: каждый refresh выдаёт новый токен; повтор старого (вне окна гонки) отзывает всё семейство.
func TestRefreshRotationAndReuse(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	m := newManager(t, c)
	t0, _ := m.Create(ctx, 1, 2, false)
	t1, err := m.Refresh(ctx, t0.Refresh)
	if err != nil {
		t.Fatal(err)
	}
	if t1.Refresh == t0.Refresh || t1.SessionID != t0.SessionID {
		t.Fatal("refresh must rotate within the same session")
	}
	// Гонка вкладок: тот же старый токен сразу после ротации — 409, сессия жива.
	if _, err := m.Refresh(ctx, t0.Refresh); !errors.Is(err, ErrRefreshRace) {
		t.Fatalf("race: %v", err)
	}
	t2, err := m.Refresh(ctx, t1.Refresh)
	if err != nil {
		t.Fatal(err)
	}
	// t0 — уже не «предыдущий» токен: повтор = кража, семейство отзывается.
	c.add(time.Second)
	if _, err := m.Refresh(ctx, t0.Refresh); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := m.Refresh(ctx, t2.Refresh); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("family must be revoked: %v", err)
	}
	if _, err := m.Authenticate(ctx, t2.Access); !errors.Is(err, ErrNotFound) {
		t.Fatalf("access of revoked family: %v", err)
	}
}

func TestRefreshReuseOfPreviousAfterGrace(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	m := newManager(t, c)
	t0, _ := m.Create(ctx, 1, 0, false)
	t1, _ := m.Refresh(ctx, t0.Refresh)
	c.add(11 * time.Second)
	if _, err := m.Refresh(ctx, t0.Refresh); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("reuse after grace: %v", err)
	}
	if _, err := m.Refresh(ctx, t1.Refresh); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("revoked: %v", err)
	}
}

// Неизвестный токен с чужим sid не отзывает сессию (нельзя разлогинить, зная только sid).
func TestRefreshGuessDoesNotRevoke(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, newClock())
	t0, _ := m.Create(ctx, 1, 0, false)
	guess := t0.SessionID + "." + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := m.Refresh(ctx, guess); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatal(err)
	}
	if err := m.RevokeRefresh(ctx, guess); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(ctx, t0.Refresh); err != nil {
		t.Fatalf("session must survive: %v", err)
	}
}

func TestIdleAndAbsoluteExpiry(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	m, _ := New(Options{Client: client, Keys: keys(), Prefix: prefix(), Now: c.now, MaxLifetime: time.Hour, IdleTTL: 2 * time.Hour})
	t0, _ := m.Create(ctx, 1, 0, true)
	if t0.RefreshMaxAge != time.Hour {
		t.Fatalf("ttl capped by max lifetime: %v", t0.RefreshMaxAge)
	}
	c.add(61 * time.Minute)
	if _, err := m.Refresh(ctx, t0.Refresh); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("absolute expiry: %v", err)
	}
}

func TestRevokeAndRevokeUser(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, newClock())
	a, _ := m.Create(ctx, 5, 1, false)
	b, _ := m.Create(ctx, 5, 1, true)
	other, _ := m.Create(ctx, 6, 1, false)
	if err := m.Revoke(ctx, a.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authenticate(ctx, a.Access); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := m.Authenticate(ctx, b.Access); err != nil {
		t.Fatalf("other session: %v", err)
	}
	if err := m.RevokeUser(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authenticate(ctx, b.Access); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke user: %v", err)
	}
	if _, err := m.Refresh(ctx, b.Refresh); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("revoke user refresh: %v", err)
	}
	if _, err := m.Authenticate(ctx, other.Access); err != nil {
		t.Fatalf("another user: %v", err)
	}
	// Выход по refresh-cookie.
	if err := m.RevokeRefresh(ctx, other.Refresh); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authenticate(ctx, other.Access); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoke by refresh")
	}
}

func TestSetClubAndViewing(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, newClock())
	a, _ := m.Create(ctx, 5, 1, false)
	if err := m.SetClub(ctx, a.SessionID, 9); err != nil {
		t.Fatal(err)
	}
	if err := m.SetViewing(ctx, a.SessionID, 3); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Authenticate(ctx, a.Access)
	if s.ClubID != 9 || s.ViewingClubID != 3 {
		t.Fatalf("%+v", s)
	}
	_ = m.Revoke(ctx, a.SessionID)
	if err := m.SetClub(ctx, a.SessionID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set on revoked: %v", err)
	}
	// Ключ не воскрес без TTL.
	n, _ := client.Do(ctx, client.B().Exists().Key(m.sessKey(a.SessionID)).Build()).AsInt64()
	if n != 0 {
		t.Fatal("ghost session key")
	}
}

// BR-060 / Q-035: «на смене» — активность за IdleTTL; выход снимает со смены.
func TestActiveUsers(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	m := newManager(t, c)
	a, _ := m.Create(ctx, 10, 1, false)
	_, _ = m.Create(ctx, 11, 1, true)
	got, err := m.ActiveUsers(ctx, []int64{10, 11, 12}, c.now().Add(-m.IdleTTL()))
	if err != nil || len(got) != 2 {
		t.Fatalf("ActiveUsers = %v, %v", got, err)
	}
	// 121 минута без активности: никого (remember-сессия жива, но «на смене» считается по активности, как last_activity).
	c.add(121 * time.Minute)
	got, _ = m.ActiveUsers(ctx, []int64{10, 11}, c.now().Add(-m.IdleTTL()))
	if len(got) != 0 {
		t.Fatalf("idle users counted: %v", got)
	}
	// Запрос пользователя 11 обновляет активность (раз в TouchEvery).
	b, _ := m.Create(ctx, 11, 1, false)
	c.add(2 * time.Minute)
	if _, err := m.Authenticate(ctx, b.Access); err != nil {
		t.Fatal(err)
	}
	got, _ = m.ActiveUsers(ctx, []int64{10, 11}, c.now().Add(-time.Minute))
	if len(got) != 1 || got[0] != 11 {
		t.Fatalf("touch: %v", got)
	}
	_ = m.Revoke(ctx, b.SessionID)
	got, _ = m.ActiveUsers(ctx, []int64{11}, c.now().Add(-m.IdleTTL()))
	if len(got) != 0 {
		t.Fatalf("logout must leave the shift: %v", got)
	}
	_ = a
}

func TestCSRF(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, newClock())
	a, _ := m.Create(ctx, 1, 0, false)
	b, _ := m.Create(ctx, 1, 0, false)
	if !m.CheckCSRF(a.Refresh, m.CSRFToken(a.SessionID)) {
		t.Fatal("own csrf")
	}
	if m.CheckCSRF(a.Refresh, m.CSRFToken(b.SessionID)) || m.CheckCSRF(a.Refresh, "") || m.CheckCSRF("garbage", "x") {
		t.Fatal("foreign csrf accepted")
	}
}
