//go:build integration

package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
	"golang.org/x/crypto/bcrypt"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// laravelSecret — настоящий хэш Laravel: php -r 'echo password_hash("secret", PASSWORD_BCRYPT, ["cost"=>12]);'
const laravelSecret = "$2y$12$ctLLgcULumFLvY6JSuuVMuQBR2AwMHasyDWlpVVOqZi.dFt/wLjqy"

var (
	tdb    *dbtest.DB
	vk     valkey.Client
	hasher *password.Hasher
	seq    atomic.Int64
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
	hasher, _ = password.New(password.Options{})
	code := m.Run()
	cleanupVK()
	cleanupDB()
	os.Exit(code)
}

// recorder — Events для проверок журнала.
type recorder struct {
	NopEvents
	failed  []string
	success int
}

func (r *recorder) LoginFailed(_ context.Context, phone, reason string) {
	r.failed = append(r.failed, phone+" "+reason)
}
func (r *recorder) LoginSucceeded(context.Context, int64, int64, string) { r.success++ }

func newService(t *testing.T) (*Service, *recorder) {
	t.Helper()
	sm, err := session.New(session.Options{Client: vk, Prefix: fmt.Sprintf("a%d:", seq.Add(1)),
		Keys: []session.Key{{ID: "t", Secret: bytes.Repeat([]byte{7}, 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	s, err := NewService(Options{Pool: tdb.App, Hasher: hasher, Sessions: sm, Events: rec})
	if err != nil {
		t.Fatal(err)
	}
	return s, rec
}

// phone — уникальный номер +77XXXXXXXXX.
func phone() string { return fmt.Sprintf("+770%08d", seq.Add(1)) }

func cheapHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(string(h), "$2a$", "$2y$", 1)
}

func user(t *testing.T, ph, hash string, superadmin bool) int64 {
	t.Helper()
	var id int64
	if err := tdb.Owner.QueryRow(context.Background(),
		"INSERT INTO users (name, phone, password, is_superadmin, created_at, updated_at) VALUES ($1, $1, $2, $3, now(), now()) RETURNING id",
		ph, hash, superadmin).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func member(t *testing.T, clubID, userID int64, role, perms string) {
	t.Helper()
	var p any
	if perms != "" {
		p = perms
	}
	if _, err := tdb.Owner.Exec(context.Background(),
		"INSERT INTO club_memberships (club_id, user_id, role, permissions, created_at, updated_at) VALUES ($1, $2, $3, $4::jsonb, now(), now())",
		clubID, userID, role, p); err != nil {
		t.Fatal(err)
	}
}

func club(t *testing.T) int64 { return tdb.Club(t, fmt.Sprintf("club-%d", seq.Add(1))) }

func passwordOf(t *testing.T, id int64) string {
	t.Helper()
	var h string
	if err := tdb.Owner.QueryRow(context.Background(), "SELECT password FROM users WHERE id = $1", id).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

func fieldErr(t *testing.T, err error, field, msg string) {
	t.Helper()
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != field || fe.Message != msg {
		t.Fatalf("err = %v, want %s: %q", err, field, msg)
	}
}

// Вход со старым bcrypt-хэшем Laravel → хэш заменён на Argon2id, повторный вход по нему.
func TestLoginBcryptRehash(t *testing.T) {
	ctx := context.Background()
	s, rec := newService(t)
	c := club(t)
	ph := phone()
	uid := user(t, ph, laravelSecret, false)
	member(t, c, uid, permissions.RoleOwner, "")

	res, err := s.Login(ctx, LoginInput{Phone: ph, Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ClubID != c || res.Principal.Role != permissions.RoleOwner || len(res.Principal.Permissions) != len(permissions.All()) {
		t.Fatalf("principal %+v", res.Principal)
	}
	h := passwordOf(t, uid)
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("not rehashed: %s", h)
	}
	if _, err := s.Login(ctx, LoginInput{Phone: ph, Password: "secret"}); err != nil {
		t.Fatalf("login with argon2id: %v", err)
	}
	if passwordOf(t, uid) != h {
		t.Fatal("argon2id with current params must not be rehashed again")
	}
	fieldErr(t, func() error { _, err := s.Login(ctx, LoginInput{Phone: ph, Password: "Secret"}); return err }(), "phone", MsgInvalidCredentials)
	if rec.success != 2 || len(rec.failed) != 1 || rec.failed[0] != ph+" "+ReasonInvalidCredentials {
		t.Fatalf("events %+v", rec)
	}
}

func TestLoginErrors(t *testing.T) {
	ctx := context.Background()
	s, rec := newService(t)
	c := club(t)

	fieldErr(t, func() error { _, err := s.Login(ctx, LoginInput{Phone: " ", Password: "x"}); return err }(), "phone", MsgPhoneRequired)
	fieldErr(t, func() error { _, err := s.Login(ctx, LoginInput{Phone: "12345", Password: "x"}); return err }(), "phone", MsgPhoneInvalid)
	fieldErr(t, func() error { _, err := s.Login(ctx, LoginInput{Phone: "+77010000000", Password: ""}); return err }(), "password", MsgPasswordRequired)
	// Неизвестный номер — тот же текст, что и неверный пароль.
	fieldErr(t, func() error { _, err := s.Login(ctx, LoginInput{Phone: "+77999999999", Password: "x"}); return err }(), "phone", MsgInvalidCredentials)

	blocked := phone()
	bid := user(t, blocked, cheapHash(t, "pw"), false)
	member(t, c, bid, permissions.RoleCashier, "")
	if _, err := tdb.Owner.Exec(ctx, "UPDATE users SET blocked_at = now() WHERE id = $1", bid); err != nil {
		t.Fatal(err)
	}
	fieldErr(t, func() error { _, err := s.Login(ctx, LoginInput{Phone: blocked, Password: "pw"}); return err }(), "phone", MsgBlocked)

	lonely := phone()
	user(t, lonely, cheapHash(t, "pw"), false)
	fieldErr(t, func() error { _, err := s.Login(ctx, LoginInput{Phone: lonely, Password: "pw"}); return err }(), "phone", MsgNoClubs)

	client := phone()
	cid := user(t, client, cheapHash(t, "pw"), false)
	member(t, c, cid, permissions.RoleClient, "")
	fieldErr(t, func() error { _, err := s.Login(ctx, LoginInput{Phone: client, Password: "pw"}); return err }(), "phone", MsgStaffOnly)
	// Подтверждение входа на ПК (QR) — клиенту можно.
	if res, err := s.Login(ctx, LoginInput{Phone: client, Password: "pw", PCLogin: true}); err != nil || res.Principal.Role != permissions.RoleClient {
		t.Fatalf("pc login: %+v %v", res.Principal, err)
	}
	reasons := strings.Join(rec.failed, ";")
	for _, r := range []string{ReasonBlocked, ReasonNoClubAccess} {
		if !strings.Contains(reasons, r) {
			t.Errorf("reason %s not logged: %s", r, reasons)
		}
	}
	if strings.Contains(reasons, client) {
		t.Error("staff-only refusal is not logged in the old code")
	}
}

// Телефон в любом виде → +7XXXXXXXXXX (AuthController::normalizePhone).
func TestNormalizePhone(t *testing.T) {
	for in, want := range map[string]string{
		"8 (701) 111-22-33": "+787011112233", // 11 цифр, начинается с 8 — не 7: не обрезается (как в старом)
		"7 701 111 22 33":   "+77011112233",
		"+7 701 111 22 33":  "+77011112233",
		"701 111 22 33":     "+77011112233",
		"":                  "",
		"abc":               "",
	} {
		if got := NormalizePhone(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

// Сначала клуб, где пользователь — сотрудник (кассир, который клиент другого клуба).
func TestLoginPrefersStaffMembership(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	a, b := club(t), club(t)
	ph := phone()
	uid := user(t, ph, cheapHash(t, "pw"), false)
	member(t, a, uid, permissions.RoleClient, "")
	member(t, b, uid, permissions.RoleCashier, `{"cash.delete": true}`)
	res, err := s.Login(ctx, LoginInput{Phone: ph, Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ClubID != b || !res.Principal.Can("cash.delete") || res.Principal.Can("tariffs.view") {
		t.Fatalf("%+v", res.Principal)
	}
	me, err := s.Me(ctx, res.Principal)
	if err != nil || len(me.Clubs) != 2 || me.Clubs[0].ClubID != b {
		t.Fatalf("me clubs %+v %v", me.Clubs, err)
	}
	// Переход в клуб, где он клиент (как ClubController::switch — любая роль).
	p, err := s.SelectClub(ctx, res.Principal, a)
	if err != nil || p.ClubID != a || p.Role != permissions.RoleClient || len(p.Permissions) != 0 {
		t.Fatalf("select: %+v %v", p, err)
	}
	if _, err := s.SelectClub(ctx, res.Principal, club(t)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign club: %v", err)
	}
	// Сессия помнит клуб.
	p2, err := s.Authenticate(ctx, res.Tokens.Access)
	if err != nil || p2.ClubID != a {
		t.Fatalf("session club: %+v %v", p2, err)
	}
}

// BR-060: лимит кассиров на смене по активным сессиям в Valkey.
func TestLoginShiftLimit(t *testing.T) {
	ctx := context.Background()
	s, rec := newService(t)
	c := club(t)
	if _, err := tdb.Owner.Exec(ctx, `UPDATE clubs SET settings = '{"club": {"max_cashiers_on_shift": "1"}}' WHERE id = $1`, c); err != nil {
		t.Fatal(err)
	}
	ph1, ph2, op := phone(), phone(), phone()
	u1 := user(t, ph1, cheapHash(t, "pw"), false)
	u2 := user(t, ph2, cheapHash(t, "pw"), false)
	u3 := user(t, op, cheapHash(t, "pw"), false)
	member(t, c, u1, permissions.RoleCashier, "")
	member(t, c, u2, permissions.RoleCashier, "")
	member(t, c, u3, permissions.RoleOperator, "")

	r1, err := s.Login(ctx, LoginInput{Phone: ph1, Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	fieldErr(t, func() error { _, err := s.Login(ctx, LoginInput{Phone: ph2, Password: "pw"}); return err }(), "phone", MsgShiftFull)
	if !strings.Contains(strings.Join(rec.failed, ";"), ReasonShiftFull) {
		t.Error("shift_full not logged")
	}
	// Тот же кассир может войти ещё раз (сам себя не вытесняет), оператор — без лимита.
	if _, err := s.Login(ctx, LoginInput{Phone: ph1, Password: "pw"}); err != nil {
		t.Fatalf("same cashier: %v", err)
	}
	if _, err := s.Login(ctx, LoginInput{Phone: op, Password: "pw"}); err != nil {
		t.Fatalf("operator: %v", err)
	}
	// Коллега вышел на всех устройствах — место свободно.
	if err := s.LogoutAll(ctx, r1.Principal); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, LoginInput{Phone: ph2, Password: "pw"}); err != nil {
		t.Fatalf("after logout: %v", err)
	}
}

func TestAuthenticateBlockedAndLogout(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	c := club(t)
	ph := phone()
	uid := user(t, ph, cheapHash(t, "pw"), false)
	member(t, c, uid, permissions.RoleManager, "")
	res, _ := s.Login(ctx, LoginInput{Phone: ph, Password: "pw"})
	other, _ := s.Login(ctx, LoginInput{Phone: ph, Password: "pw"})

	if err := s.Logout(ctx, res.Principal); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, res.Tokens.Access); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("after logout: %v", err)
	}
	// Права меняются сразу (членство читается на каждом запросе).
	if _, err := tdb.Owner.Exec(ctx, `UPDATE club_memberships SET permissions = '{"tariffs.update": false}' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, other.Tokens.Access)
	if err != nil || p.Can("tariffs.update") || !p.Can("tariffs.view") {
		t.Fatalf("live permissions: %+v %v", p, err)
	}
	// Блокировка — сразу, сессии отзываются.
	if _, err := tdb.Owner.Exec(ctx, "UPDATE users SET blocked_at = now() WHERE id = $1", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, other.Tokens.Access); !errors.Is(err, ErrBlocked) {
		t.Fatalf("blocked: %v", err)
	}
	if _, err := s.Sessions().Refresh(ctx, other.Tokens.Refresh); err == nil {
		t.Fatal("blocked user's refresh must be revoked")
	}
}

// Суперадмин: без клуба входит в кабинет; просмотр клуба — владелец только для чтения.
func TestSuperadminViewing(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	c := club(t)
	ph := phone()
	user(t, ph, cheapHash(t, "pw"), true)
	res, err := s.Login(ctx, LoginInput{Phone: ph, Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ClubID != 0 || !res.Principal.Superadmin || res.Principal.Can("cash.view") {
		t.Fatalf("cabinet principal %+v", res.Principal)
	}
	v, err := s.ViewClub(ctx, res.Principal, c)
	if err != nil || !v.ReadOnly || v.Role != permissions.RoleOwner || !v.Can("tariffs.delete") || v.ClubID != c {
		t.Fatalf("view %+v %v", v, err)
	}
	p, err := s.Authenticate(ctx, res.Tokens.Access)
	if err != nil || !p.ReadOnly || p.ClubID != c {
		t.Fatalf("session view: %+v %v", p, err)
	}
	if _, _, _, ok := permissions.CheckClub(p, "POST"); ok {
		t.Fatal("read-only must refuse POST")
	}
	if _, err := s.SelectClub(ctx, p, c); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("switch in view mode: %v", err)
	}
	if _, err := s.ViewClub(ctx, res.Principal, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing club: %v", err)
	}
	if was, err := s.LeaveClub(ctx, p); err != nil || was != c {
		t.Fatalf("leave: %d %v", was, err)
	}
	p, _ = s.Authenticate(ctx, res.Tokens.Access)
	if p.ReadOnly || p.ClubID != 0 {
		t.Fatalf("after leave %+v", p)
	}
	// Не суперадмин не смотрит чужие клубы.
	if _, err := s.ViewClub(ctx, principal.Principal{UserID: 1, SessionID: "x"}, c); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}

// RLS: TenantTx берёт клуб из Principal; запрос с чужим club_id в параметре ничего не видит.
func TestTenantTxFromPrincipal(t *testing.T) {
	ctx := context.Background()
	a, b := club(t), club(t)
	ua := user(t, phone(), "x", false)
	ub := user(t, phone(), "x", false)
	member(t, a, ua, permissions.RoleCashier, "")
	member(t, b, ub, permissions.RoleCashier, "")

	if err := TenantTx(ctx, tdb.App, func(*store.Queries) error { return nil }); !errors.Is(err, ErrNoClub) {
		t.Fatalf("no principal: %v", err)
	}
	pctx := principal.WithPrincipal(ctx, principal.Principal{UserID: ua, ClubID: a})
	var own, foreign []int64
	if err := TenantTx(pctx, tdb.App, func(q *store.Queries) error {
		var err error
		if own, err = q.ListClubUserIDsByRole(ctx, store.ListClubUserIDsByRoleParams{ClubID: a, Role: "cashier"}); err != nil {
			return err
		}
		foreign, err = q.ListClubUserIDsByRole(ctx, store.ListClubUserIDsByRoleParams{ClubID: b, Role: "cashier"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 || own[0] != ua || len(foreign) != 0 {
		t.Fatalf("own %v foreign %v (RLS must hide club %d)", own, foreign, b)
	}
	if err := TenantTx(principal.WithPrincipal(ctx, principal.Principal{UserID: ua}), tdb.App, func(*store.Queries) error { return nil }); !errors.Is(err, ErrNoClub) {
		t.Fatalf("no club: %v", err)
	}
}

// Время «на смене» считается от часов Service: проверка, что окно IdleTTL совпадает с session.
func TestShiftWindowUsesIdleTTL(t *testing.T) {
	s, _ := newService(t)
	if s.sessions.IdleTTL() != 120*time.Minute {
		t.Fatalf("idle ttl %v (SESSION_LIFETIME = 120)", s.sessions.IdleTTL())
	}
}
