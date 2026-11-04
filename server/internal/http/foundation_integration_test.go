//go:build integration

package httpserver

// Этап 3, F1: регистрация владельца с клубом, приглашения, билет WebSocket, переводы.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

type foundationEnv struct {
	t   *testing.T
	db  *dbtest.DB
	srv *httptest.Server
}

func newFoundationEnv(t *testing.T, enable ...string) *foundationEnv {
	t.Helper()
	d := dbtest.New(t)
	vk, cleanup, err := authtest.StartValkey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	h, _ := password.New(password.Options{})
	prefix := fmt.Sprintf("f%d:", authSeq.Add(1))
	sm, err := session.New(session.Options{Client: vk, Prefix: prefix, Keys: []session.Key{{ID: "t", Secret: bytes.Repeat([]byte{4}, 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(auth.Options{Pool: d.App, Hasher: h, Sessions: sm})
	if err != nil {
		t.Fatal(err)
	}
	lim, err := ratelimit.New(ratelimit.Options{Client: vk, Prefix: prefix + "rl:", Enable: enable})
	if err != nil {
		t.Fatal(err)
	}
	// /ws — эхо Principal (настоящий хаб тестирует пакет realtime).
	ws := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := principal.FromContext(r.Context())
		_, _ = fmt.Fprintf(w, "%d:%d", p.UserID, p.ClubID)
	})
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim, Pool: d.App,
		PlatformPool: d.Platform, Valkey: vk, Passwords: h, Mounts: map[string]http.Handler{"/ws": ws},
	}))
	t.Cleanup(srv.Close)
	return &foundationEnv{t: t, db: d, srv: srv}
}

func (e *foundationEnv) url(p string) string { return e.srv.URL + p }

func TestRegisterOwnerClub(t *testing.T) {
	e := newFoundationEnv(t)

	// Ошибки — как $request->validate() в AuthController::register().
	r := do(t, "POST", e.url("/api/auth/register"), map[string]any{"phone": "123", "password": "12345", "password_confirmation": "x"}, nil)
	if r.status != 422 {
		t.Fatalf("validation: %d %v", r.status, r.body)
	}
	errs := r.body["errors"].(map[string]any)
	for field, want := range map[string]string{
		"name": "Имя обязательно.", "club_name": "Название клуба обязательно.",
		"phone": "Введите корректный номер телефона.", "password": "Пароль должен содержать минимум 6 символов.",
	} {
		if got := errs[field].([]any)[0]; got != want {
			t.Errorf("%s: %v", field, got)
		}
	}
	if got := errs["password"].([]any); len(got) != 2 || got[1] != "Пароли не совпадают." {
		t.Errorf("password: %v", got)
	}

	body := map[string]any{"name": "Владелец", "club_name": "Клуб Звезда", "phone": "+7 (701) 111-22-33", "password": "secret1", "password_confirmation": "secret1"}
	r = do(t, "POST", e.url("/api/auth/register"), body, nil)
	if r.status != 201 {
		t.Fatalf("register: %d %v", r.status, r.body)
	}
	me := r.body["me"].(map[string]any)
	if *(roleOf(me)) != "owner" || me["club"].(map[string]any)["name"] != "Клуб Звезда" || me["club"].(map[string]any)["locale"] != "ru" {
		t.Fatalf("me: %v", me)
	}
	if cookie(r, RefreshCookie) == nil || cookie(r, CSRFCookie) == nil {
		t.Fatal("session cookies")
	}
	clubID := int64(me["club"].(map[string]any)["id"].(float64))
	var status, slug string
	if err := e.db.Owner.QueryRow(context.Background(),
		`SELECT s.status, c.slug FROM club_subscriptions s JOIN clubs c ON c.id = s.club_id WHERE s.club_id = $1`, clubID).Scan(&status, &slug); err != nil {
		t.Fatal(err)
	}
	if status != "trialing" || !strings.HasPrefix(slug, "klub-zvezda-") || len(slug) != len("klub-zvezda-")+8 {
		t.Fatalf("subscription %s, slug %s", status, slug)
	}
	var logs int
	_ = e.db.Owner.QueryRow(context.Background(), `SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'auth.registered'`, clubID).Scan(&logs)
	if logs != 1 {
		t.Fatalf("activity: %d", logs)
	}

	// Тот же номер — «уже зарегистрирован».
	r = do(t, "POST", e.url("/api/auth/register"), body, nil)
	if r.status != 422 || r.body["errors"].(map[string]any)["phone"].([]any)[0] != "Этот номер телефона уже зарегистрирован." {
		t.Fatalf("duplicate: %d %v", r.status, r.body)
	}
	// Межсайтовый запрос — 403 (Fetch Metadata).
	r = do(t, "POST", e.url("/api/auth/register"), body, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if r.status != 403 {
		t.Fatalf("cross-site: %d", r.status)
	}
}

func roleOf(me map[string]any) *string {
	s, _ := me["role"].(string)
	return &s
}

func TestRegisterRateLimit(t *testing.T) {
	e := newFoundationEnv(t, ratelimit.WebRegister)
	var last int
	for range 30 {
		last = do(t, "POST", e.url("/api/auth/register"), map[string]any{}, nil).status
		if last == 429 {
			break
		}
	}
	if last != 429 {
		t.Fatalf("web-register: %d", last)
	}
}

func (e *foundationEnv) invite(clubID int64, role string, email *string, expires time.Time) string {
	e.t.Helper()
	token := fmt.Sprintf("tok%d%s", authSeq.Add(1), strings.Repeat("x", 40))
	sum := sha256.Sum256([]byte(token))
	if _, err := e.db.Owner.Exec(context.Background(),
		`INSERT INTO club_invitations (club_id, email, role, token_hash, expires_at, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, now(), now())`,
		clubID, email, role, hex.EncodeToString(sum[:]), expires); err != nil {
		e.t.Fatal(err)
	}
	return token
}

func TestInvitationAcceptGuestAndMember(t *testing.T) {
	e := newFoundationEnv(t)
	clubID := e.db.Club(t, fmt.Sprintf("inv%d", authSeq.Add(1)))
	tok := e.invite(clubID, "cashier", nil, time.Now().Add(time.Hour))

	r := do(t, "GET", e.url("/api/invitations/"+tok), nil, nil)
	if r.status != 200 || r.body["role"] != "cashier" || r.body["already_member"] != false {
		t.Fatalf("show: %d %v", r.status, r.body)
	}
	if r := do(t, "GET", e.url("/api/invitations/nope"), nil, nil); r.status != 404 {
		t.Fatalf("unknown: %d", r.status)
	}

	// Гость: стандартные тексты валидатора (accept() не задаёт свои).
	r = do(t, "POST", e.url("/api/invitations/"+tok+"/accept"), map[string]any{"password": "123"}, nil)
	if r.status != 422 {
		t.Fatalf("422: %d %v", r.status, r.body)
	}
	errs := r.body["errors"].(map[string]any)
	if errs["name"].([]any)[0] != "The name field is required." || errs["phone"].([]any)[0] != "The phone field format is invalid." ||
		errs["password"].([]any)[0] != "The password field must be at least 6 characters." {
		t.Fatalf("errors: %v", errs)
	}

	r = do(t, "POST", e.url("/api/invitations/"+tok+"/accept"), map[string]any{
		"name": "Кассир", "phone": "+7 702 000 11 22", "password": "secret1", "password_confirmation": "secret1",
	}, nil)
	if r.status != 201 {
		t.Fatalf("accept guest: %d %v", r.status, r.body)
	}
	me := r.body["me"].(map[string]any)
	if *(roleOf(me)) != "cashier" || int64(me["club"].(map[string]any)["id"].(float64)) != clubID {
		t.Fatalf("me: %v", me)
	}
	// Повторно — приглашение уже принято: 404 (findValidInvitation).
	if r := do(t, "POST", e.url("/api/invitations/"+tok+"/accept"), map[string]any{}, nil); r.status != 404 {
		t.Fatalf("reuse: %d", r.status)
	}

	// Вошедший пользователь: вступает и переходит в клуб; уже состоящий — already_member.
	access := r.body["access_token"].(string)
	other := e.db.Club(t, fmt.Sprintf("inv%d", authSeq.Add(1)))
	tok2 := e.invite(other, "operator", nil, time.Now().Add(time.Hour))
	r = do(t, "POST", e.url("/api/invitations/"+tok2+"/accept"), map[string]any{}, bearer(access))
	if r.status != 200 || int64(r.body["club"].(map[string]any)["id"].(float64)) != other || *(roleOf(r.body)) != "operator" {
		t.Fatalf("accept member: %d %v", r.status, r.body)
	}
	tok3 := e.invite(other, "admin", nil, time.Now().Add(time.Hour))
	if r := do(t, "GET", e.url("/api/invitations/"+tok3), nil, bearer(access)); r.body["already_member"] != true {
		t.Fatalf("already member: %v", r.body)
	}
	if r := do(t, "POST", e.url("/api/invitations/"+tok3+"/accept"), map[string]any{}, bearer(access)); r.status != 409 ||
		r.body["message"] != "Аккаунт уже состоит в этом клубе." {
		t.Fatalf("409: %d %v", r.status, r.body)
	}
	// Приглашение на другой email — 403.
	mail := "boss@example.com"
	tok4 := e.invite(e.db.Club(t, fmt.Sprintf("inv%d", authSeq.Add(1))), "admin", &mail, time.Now().Add(time.Hour))
	if r := do(t, "POST", e.url("/api/invitations/"+tok4+"/accept"), map[string]any{}, bearer(access)); r.status != 403 ||
		r.body["message"] != "Это приглашение предназначено для другого email." {
		t.Fatalf("email: %d %v", r.status, r.body)
	}
	// Истёкшее — 404.
	tok5 := e.invite(clubID, "admin", nil, time.Now().Add(-time.Minute))
	if r := do(t, "GET", e.url("/api/invitations/"+tok5), nil, nil); r.status != 404 {
		t.Fatalf("expired: %d", r.status)
	}
}

func TestWsTicket(t *testing.T) {
	e := newFoundationEnv(t)
	r := do(t, "POST", e.url("/api/auth/register"), map[string]any{
		"name": "W", "club_name": "WS", "phone": fmt.Sprintf("+7709%07d", authSeq.Add(1)), "password": "secret1", "password_confirmation": "secret1",
	}, nil)
	if r.status != 201 {
		t.Fatalf("register: %d %v", r.status, r.body)
	}
	access := r.body["access_token"].(string)
	if r := do(t, "POST", e.url("/api/ws-ticket"), nil, nil); r.status != 401 {
		t.Fatalf("anon ticket: %d", r.status)
	}
	r = do(t, "POST", e.url("/api/ws-ticket"), nil, bearer(access))
	if r.status != 200 || r.body["expires_in"] != float64(30) {
		t.Fatalf("ticket: %d %v", r.status, r.body)
	}
	ticket := r.body["ticket"].(string)
	get := func(q string) (int, string) {
		res, err := http.Get(e.url("/ws" + q)) //nolint:noctx // тест
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, _ := get(""); st != 401 {
		t.Fatalf("no ticket: %d", st)
	}
	st, body := get("?ticket=" + ticket)
	if st != 200 || strings.HasPrefix(body, "0:") || strings.HasSuffix(body, ":0") {
		t.Fatalf("with ticket: %d %s", st, body)
	}
	// Одноразовый.
	if st, _ := get("?ticket=" + ticket); st != 401 {
		t.Fatalf("reuse: %d", st)
	}
}

func TestI18nPack(t *testing.T) {
	e := newFoundationEnv(t)
	shipped := map[string]i18n.Shipped{
		"ru": {"*": {"Касса": "Касса", ":count минута|:count минуты|:count минут": ":count минута|:count минуты|:count минут"}},
		"kk": {"*": {"Касса": "Касса (kk)", "Создан товар: :product": "Тауар құрылды: :product"}},
		"en": {"*": {"Касса": "Cash desk", "Только en": "Only en"}},
	}
	n, err := i18n.Import(context.Background(), e.db.Platform, shipped)
	if err != nil || n != 6 {
		t.Fatalf("import: %d %v", n, err)
	}
	if n, _ := i18n.Import(context.Background(), e.db.Platform, shipped); n != 0 {
		t.Fatalf("second import: %d", n)
	}
	r := do(t, "GET", e.url("/api/i18n"), nil, nil)
	if r.status != 200 || len(r.body["languages"].([]any)) != 3 {
		t.Fatalf("languages: %d %v", r.status, r.body)
	}
	r = do(t, "GET", e.url("/api/i18n/kk"), nil, nil)
	if r.status != 200 || r.body["Касса"] != "Касса (kk)" || r.body["Создан товар: {{product}}"] != "Тауар құрылды: {{product}}" ||
		r.body["Только en"] != "Only en" {
		t.Fatalf("kk: %d %v", r.status, r.body)
	}
	etag := r.header.Get("ETag")
	if r := do(t, "GET", e.url("/api/i18n/kk"), nil, map[string]string{"If-None-Match": etag}); r.status != 304 {
		t.Fatalf("304: %d", r.status)
	}
	r = do(t, "GET", e.url("/api/i18n/ru"), nil, nil)
	if r.body["{{count}} минута|{{count}} минуты|{{count}} минут_few"] != "{{count}} минуты" || r.body["Только en"] != nil {
		t.Fatalf("ru: %v", r.body)
	}
	// Правка в суперадминке меняет ETag.
	if _, err := e.db.Owner.Exec(context.Background(), `UPDATE translations SET value = 'Касса!', updated_at = now() + interval '1 second' WHERE value = 'Касса (kk)'`); err != nil {
		t.Fatal(err)
	}
	if r := do(t, "GET", e.url("/api/i18n/kk"), nil, map[string]string{"If-None-Match": etag}); r.status != 200 || r.body["Касса"] != "Касса!" {
		t.Fatalf("after edit: %d %v", r.status, r.body)
	}
	if r := do(t, "GET", e.url("/api/i18n/de"), nil, nil); r.status != 404 {
		t.Fatalf("de: %d", r.status)
	}
}
