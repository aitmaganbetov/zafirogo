//go:build integration

package mfa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/totp"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var (
	tdb *dbtest.DB
	vk  valkey.Client
	seq atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	d, cleanupDB, err := dbtest.Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	c, cleanupVK, err := authtest.StartValkey(ctx)
	if err != nil {
		cleanupDB()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tdb, vk = d, c
	code := m.Run()
	cleanupVK()
	cleanupDB()
	os.Exit(code)
}

func ring(t *testing.T, secret string) *secretbox.Box {
	t.Helper()
	b, err := secretbox.NewKeyring([]secretbox.Key{{ID: "t", Secret: strings.Repeat(secret, 32)}}, "")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newManager(t *testing.T, mod func(*Options)) *Manager {
	t.Helper()
	n := seq.Add(1)
	// Лимит аккаунта выключен: здесь проверяются попытки на полувход и повтор шага (лимит — в HTTP-тестах).
	lim, err := ratelimit.New(ratelimit.Options{Client: vk, Prefix: fmt.Sprintf("l%d:", n), Disable: []string{ratelimit.SuperadminTOTP}})
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Platform: tdb.Platform, Valkey: vk, Box: ring(t, "k"), Limiter: lim, Prefix: fmt.Sprintf("m%d:", n)}
	if mod != nil {
		mod(&o)
	}
	m, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func superadmin(t *testing.T) Pending {
	t.Helper()
	ph := fmt.Sprintf("+779%08d", seq.Add(1))
	var id int64
	if err := tdb.Owner.QueryRow(context.Background(),
		"INSERT INTO users (name, phone, password, is_superadmin, created_at, updated_at) VALUES ('SA', $1, 'x', true, now(), now()) RETURNING id",
		ph).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return Pending{UserID: id, Name: "SA", Phone: ph}
}

// enroll — настройка TOTP: ключ (base32) и коды восстановления.
func enroll(t *testing.T, m *Manager, p Pending) (string, []string) {
	t.Helper()
	ctx := context.Background()
	ch, err := m.Begin(ctx, p)
	if err != nil || !ch.SetupRequired {
		t.Fatalf("begin: %+v %v", ch, err)
	}
	st, err := m.Setup(ctx, ch.Token)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Verify(ctx, ch.Token, code(t, st.Secret, totp.Step(time.Now())-1))
	if err != nil || len(r.RecoveryCodes) != totp.RecoveryCount {
		t.Fatalf("confirm: %v %+v", err, r)
	}
	return st.Secret, r.RecoveryCodes
}

func code(t *testing.T, secret string, step int64) string {
	t.Helper()
	key, err := totp.DecodeSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	return totp.Code(key, step)
}

func begin(t *testing.T, m *Manager, p Pending) string {
	t.Helper()
	ch, err := m.Begin(context.Background(), p)
	if err != nil || ch.SetupRequired {
		t.Fatalf("begin: %+v %v", ch, err)
	}
	return ch.Token
}

// Пять неверных кодов сжигают полувход; полувход истекает по TTL.
func TestChallengeAttemptsAndExpiry(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, func(o *Options) { o.ChallengeTTL = 300 * time.Millisecond })
	p := superadmin(t)
	secret, _ := enroll(t, newManager(t, nil), p)

	tok := begin(t, m, p)
	for i := range 5 {
		if _, err := m.Verify(ctx, tok, "abc"); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if _, err := m.Verify(ctx, tok, code(t, secret, totp.Step(time.Now()))); !errors.Is(err, ErrChallenge) {
		t.Fatalf("6th attempt: %v", err)
	}

	tok = begin(t, m, p)
	time.Sleep(400 * time.Millisecond)
	if _, err := m.Verify(ctx, tok, code(t, secret, totp.Step(time.Now()))); !errors.Is(err, ErrChallenge) {
		t.Fatalf("expired challenge: %v", err)
	}
}

// Повтор шага: без AllowReplay — отказ; с AllowReplay (dev) — принимается, но чужой код — нет.
func TestReplayAndAllowReplay(t *testing.T) {
	ctx := context.Background()
	p := superadmin(t)
	m := newManager(t, nil)
	secret, _ := enroll(t, m, p)
	cur := totp.Step(time.Now())
	if _, err := m.Verify(ctx, begin(t, m, p), code(t, secret, cur)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(ctx, begin(t, m, p), code(t, secret, cur)); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("replay: %v", err)
	}
	dev := newManager(t, func(o *Options) { o.AllowReplay = true })
	for range 3 {
		if _, err := dev.Verify(ctx, begin(t, dev, p), code(t, secret, cur)); err != nil {
			t.Fatalf("dev replay: %v", err)
		}
	}
	if _, err := dev.Verify(ctx, begin(t, dev, p), code(t, secret, cur+5)); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("dev: code outside the window: %v", err)
	}
}

// Ключ данных потерян (другая связка) — ErrSecretUnreadable, а не «неверный код»; сброс — заново настройка.
func TestUnreadableSecretAndReset(t *testing.T) {
	ctx := context.Background()
	p := superadmin(t)
	enroll(t, newManager(t, nil), p)
	other := newManager(t, func(o *Options) { o.Box = ring(t, "z") })
	if _, err := other.Verify(ctx, begin(t, other, p), "123456"); !errors.Is(err, ErrSecretUnreadable) {
		t.Fatalf("unreadable: %v", err)
	}
	if err := resetUser(ctx, p.UserID); err != nil {
		t.Fatal(err)
	}
	ch, err := other.Begin(ctx, p)
	if err != nil || !ch.SetupRequired {
		t.Fatalf("after reset: %+v %v", ch, err)
	}
	// Вход начат до сброса (полувход без настройки) — после сброса недействителен.
	m := newManager(t, nil)
	secret, _ := enroll(t, m, p)
	tok := begin(t, m, p)
	if err := resetUser(ctx, p.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(ctx, tok, code(t, secret, totp.Step(time.Now()))); !errors.Is(err, ErrChallenge) {
		t.Fatalf("challenge after reset: %v", err)
	}
}

func resetUser(ctx context.Context, userID int64) error {
	tx, err := tdb.Platform.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := Reset(ctx, store.New(tx), userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
