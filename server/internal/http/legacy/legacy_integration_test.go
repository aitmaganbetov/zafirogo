//go:build integration

package legacy_test

// Сквозные тесты старого REST мобильных приложений: HTTP (httpserver.NewRouter с Options.Legacy) → сервисы →
// Postgres (RLS, роль zafiro_app) + Valkey (лимиты). Формат ответов сверен с моделями fromJson приложений
// (mobileclient/lib/api/models.dart, mobileemployee/lib/api/models.dart) и Pest-тестами старого проекта
// (tests/Feature/ApiAuthTest.php — GS-226…, StaffApiTest.php — GS-274…GS-281, GuestFeaturesTest.php — GS-098…,
// MobileAppTest.php — GS-519…). ID сценария — в имени подтеста.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/valkey-io/valkey-go"
	"golang.org/x/crypto/bcrypt"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	httpserver "github.com/aitmaganbetov/zafirogo/server/internal/http"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/legacy"
)

var (
	tdb *dbtest.DB
	tvk valkey.Client
	seq atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	d, c1, err := dbtest.Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	vk, c2, err := authtest.StartValkey(ctx)
	if err != nil {
		c1()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tdb, tvk = d, vk
	code := m.Run()
	c2()
	c1()
	os.Exit(code)
}

type env struct {
	t        *testing.T
	srv      *httptest.Server
	club     int64
	upstream *httptest.Server
	upHits   atomic.Int64
	// notifier — команды агенту после коммита (BalanceChanged и т. п.), как stream.Notifier в app.
	notifier *recNotifier
}

type option func(*legacy.Options)

func withUpstream(e *env) option {
	return func(o *legacy.Options) {
		e.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e.upHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"upstream":true,"path":"` + r.URL.Path + `"}`))
		}))
		e.t.Cleanup(e.upstream.Close)
		o.Upstream, _ = url.Parse(e.upstream.URL)
	}
}

func newEnv(t *testing.T, opts ...func(*env) option) *env {
	t.Helper()
	e := &env{t: t}
	h, _ := password.New(password.Options{})
	sm, err := session.New(session.Options{Client: tvk, Keys: []session.Key{{ID: "t", Secret: bytes.Repeat([]byte{7}, 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(auth.Options{Pool: tdb.App, Hasher: h, Sessions: sm})
	if err != nil {
		t.Fatal(err)
	}
	// Префикс ключей лимитов — свой на тест: счётчики не мешают друг другу.
	lim, err := ratelimit.New(ratelimit.Options{Client: tvk, Prefix: fmt.Sprintf("t%d:", seq.Add(1))})
	if err != nil {
		t.Fatal(err)
	}
	e.notifier = &recNotifier{}
	cash := &cashsvc.Service{Notifier: e.notifier}
	bill := billingsvc.New(tdb.App, nil, cash)
	lo := legacy.Options{Limiter: lim, Passwords: h, PublicBaseURL: "https://zafiro.test"}
	for _, f := range opts {
		f(e)(&lo)
	}
	e.srv = httptest.NewServer(httpserver.NewRouter(httpserver.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim,
		Pool: tdb.App, PlatformPool: tdb.Platform, Billing: bill, Cash: cash, Passwords: h,
		Legacy: func(d *httpx.Deps, root http.Handler) func(http.Handler) http.Handler {
			return legacy.New(lo, d, root).Middleware
		},
	}))
	t.Cleanup(e.srv.Close)
	e.club = e.newClub(true)
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := tdb.Owner.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) scalar(sql string, args ...any) int64 {
	e.t.Helper()
	var v int64
	if err := tdb.Owner.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

// newClub — клуб (active — с активной подпиской).
func (e *env) newClub(active bool) int64 {
	id := tdb.Club(e.t, fmt.Sprintf("mob%d", seq.Add(1)))
	plan := e.scalar(`INSERT INTO saas_plans (slug, name, features, created_at, updated_at) VALUES ($1, 'План', '{}'::jsonb, now(), now()) RETURNING id`,
		fmt.Sprintf("plan%d", seq.Add(1)))
	status := "active"
	if !active {
		status = "expired"
	}
	e.exec(`INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, current_period_ends_at)
		VALUES ($1, $2, $3, now() - interval '40 days', now() + interval '30 days')`, id, plan, status)
	return id
}

// user — аккаунт с паролем "pw" и членством role в клубе (balance — тиыны).
func (e *env) user(club int64, name, role string, _ int64) (int64, string) {
	e.t.Helper()
	phone := fmt.Sprintf("+771%08d", seq.Add(1))
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	id := e.scalar("INSERT INTO users (name, phone, password, created_at, updated_at) VALUES ($1, $2, $3, now(), now()) RETURNING id",
		name, phone, string(hash))
	e.join(club, id, role, 0)
	return id, phone
}

// join — членство и нулевой баланс (баланс меняет только ledger: пополнения — через кассу в тестах).
func (e *env) join(club, userID int64, role string, _ int64) {
	e.exec("INSERT INTO club_memberships (club_id, user_id, role, created_at, updated_at) VALUES ($1, $2, $3, now(), now())", club, userID, role)
	e.exec("INSERT INTO member_balances (club_id, user_id, created_at, updated_at) VALUES ($1, $2, now(), now())", club, userID)
}

type resp struct {
	status int
	body   map[string]any
	raw    string
	header http.Header
}

func (e *env) do(method, path, token string, body any, headers ...string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: string(raw), header: res.Header}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e *env) login(phone, device string) resp {
	return e.do("POST", "/api/auth/login", "", map[string]any{"phone": phone, "password": "pw", "device_name": device})
}

func (e *env) token(phone, device string) string {
	e.t.Helper()
	r := e.login(phone, device)
	if r.status != 200 {
		e.t.Fatalf("login: %d %s", r.status, r.raw)
	}
	return r.body["access_token"].(string)
}

// path — значение по пути "a.b.0.c".
func path(v any, p string) any {
	for _, k := range strings.Split(p, ".") {
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			var i int
			fmt.Sscan(k, &i)
			if i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

func (r resp) at(p string) any { return path(r.body, p) }

func expect(t *testing.T, r resp, status int, checks map[string]any) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d: %s", r.status, status, r.raw)
	}
	for p, want := range checks {
		got := r.at(p)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s = %v (%T), want %v; body %s", p, got, got, want, r.raw)
		}
	}
}

var sanctumRe = regexp.MustCompile(`^\d+\|[A-Za-z0-9]{48}$`)

// ---------------------------------------------------------------------------
// Вход, профиль, клубы (ApiAuthTest.php, MobileAppTest.php)
// ---------------------------------------------------------------------------

func TestAuth(t *testing.T) {
	e := newEnv(t)
	uid, phone := e.user(e.club, "Арман", "client", 0)

	t.Run("GS-226 mobile login, profile and logout", func(t *testing.T) {
		r := e.login(phone, "mobileclient")
		expect(t, r, 200, map[string]any{"token_type": "Bearer", "user.id": uid, "user.name": "Арман", "user.role": "client",
			"user.currency": "KZT", "club_id": e.club, "clubs.0.id": e.club, "clubs.0.current": true, "clubs.0.role": "client"})
		tok := r.body["access_token"].(string)
		if !sanctumRe.MatchString(tok) || r.body["token"] != tok {
			t.Fatalf("token %q", tok)
		}
		// Деньги — float тенге как json_encode((float)…): 0.0 (Profile.fromJson: _num).
		if !strings.Contains(r.raw, `"balance":0.0`) || !strings.Contains(r.raw, `"discount":0.0`) {
			t.Errorf("balances not PHP floats: %s", r.raw)
		}
		expect(t, e.do("GET", "/api/auth/me", tok, nil), 200, map[string]any{"user.id": uid, "user.phone": phone})
		expect(t, e.do("GET", "/api/auth/clubs", tok, nil), 200, map[string]any{"clubs.0.id": e.club, "clubs.0.current": true})
		expect(t, e.do("POST", "/api/auth/logout", tok, nil), 200, map[string]any{"message": "Сессия завершена."})
		expect(t, e.do("GET", "/api/auth/me", tok, nil), 401, map[string]any{"message": "Unauthenticated."})
		if n := e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event IN ('auth.api_login', 'auth.api_logout')", e.club); n != 2 {
			t.Errorf("activity rows %d", n)
		}
	})

	t.Run("GS-227 invalid credentials", func(t *testing.T) {
		r := e.do("POST", "/api/auth/login", "", map[string]any{"phone": phone, "password": "bad", "device_name": "mobileclient"})
		expect(t, r, 401, map[string]any{"message": "Неверный номер телефона или пароль."})
		r = e.do("POST", "/api/auth/login", "", map[string]any{"phone": "abc", "password": "pw", "device_name": "x"})
		if r.status != 422 || r.at("errors.phone") == nil {
			t.Fatalf("validation: %d %s", r.status, r.raw)
		}
	})

	t.Run("GS-228 profile requires a bearer token", func(t *testing.T) {
		expect(t, e.do("GET", "/api/staff/me", "", nil), 401, map[string]any{"message": "Unauthenticated."})
		expect(t, e.do("GET", "/api/auth/clubs", "", nil), 401, map[string]any{"message": "Unauthenticated."})
		expect(t, e.do("GET", "/api/guest/news", "1|"+strings.Repeat("a", 48), nil), 401, map[string]any{"message": "Unauthenticated."})
	})

	t.Run("GS-230 inactive subscription", func(t *testing.T) {
		other := e.newClub(false)
		_, p2 := e.user(other, "Б", "client", 0)
		expect(t, e.login(p2, "mobileclient"), 402, map[string]any{"message": "Подписка клуба не активна."})
	})

	t.Run("GS-520 switch club and club list", func(t *testing.T) {
		second := e.newClub(true)
		e.join(second, uid, "client", 0)
		r := e.login(phone, "mobileclient")
		if n := len(r.at("clubs").([]any)); n != 2 {
			t.Fatalf("clubs %d", n)
		}
		tok := r.body["access_token"].(string)
		sw := e.do("POST", "/api/auth/switch-club", tok, map[string]any{"club_id": second})
		expect(t, sw, 200, map[string]any{"club_id": second, "user.club_id": second})
		expect(t, e.do("GET", "/api/auth/me", tok, nil), 401, nil) // старый токен отозван
		expect(t, e.do("POST", "/api/auth/switch-club", sw.body["token"].(string), map[string]any{"club_id": 999999}), 403,
			map[string]any{"message": "У аккаунта нет доступа к выбранному клубу."})
	})

	t.Run("migrated Sanctum token keeps working", func(t *testing.T) {
		// personal_access_tokens.id и sha256 токена переносятся как есть (docs/mobile-compat.md §2.3).
		plain := strings.Repeat("Q", 40)
		sum := sha256.Sum256([]byte(plain))
		id := 1000 + seq.Add(1)
		e.exec(`INSERT INTO mobile_api_tokens (id, club_id, user_id, name, token_hash, created_at, expires_at)
			VALUES ($1, $2, $3, 'mobileclient', $4, now(), now() + interval '1 day')`, id, e.club, uid, hex.EncodeToString(sum[:]))
		expect(t, e.do("GET", "/api/auth/me", fmt.Sprintf("%d|%s", id, plain), nil), 200, map[string]any{"user.id": uid})
		// Чужой X-Club-Id — 403 (SetCurrentClub).
		expect(t, e.do("GET", "/api/auth/me", fmt.Sprintf("%d|%s", id, plain), nil, "X-Club-Id", "999999"), 403,
			map[string]any{"message": "API-токен привязан к другому клубу."})
		e.exec("UPDATE mobile_api_tokens SET expires_at = now() - interval '1 second' WHERE id = $1", id)
		expect(t, e.do("GET", "/api/auth/me", fmt.Sprintf("%d|%s", id, plain), nil), 401, nil)
	})

	t.Run("panel login is not intercepted", func(t *testing.T) {
		// Без device_name — вход панели (клиенту туда нельзя, ответ панели — 422 по полю phone, не 401 Laravel API).
		r := e.do("POST", "/api/auth/login", "", map[string]any{"phone": phone, "password": "bad"})
		if r.status == 401 || r.body["error"] == nil {
			t.Fatalf("panel login handled by legacy: %d %s", r.status, r.raw)
		}
	})

	t.Run("password recovery without Telegram", func(t *testing.T) {
		expect(t, e.do("POST", "/api/auth/password/code", "", map[string]any{"phone": "8 700 000"}), 422,
			map[string]any{"errors.phone.0": "Введите корректный номер телефона."})
		expect(t, e.do("POST", "/api/auth/password/code", "", map[string]any{"phone": phone}), 422,
			map[string]any{"errors.phone.0": "Восстановление пароля через Telegram пока не подключено. Обратитесь к администратору клуба."})
		expect(t, e.do("POST", "/api/auth/password/reset", "", map[string]any{"phone": phone, "code": "12", "password": "123456", "password_confirmation": "1"}), 422,
			map[string]any{"errors.code.0": "Код — 6 цифр из сообщения в Telegram.", "errors.password.0": "Пароли не совпадают."})
	})
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	_, phone := e.user(e.club, "Л", "client", 0)
	// throttle:customer-login — 5 в минуту на IP + телефон.
	for i := 0; i < 5; i++ {
		if r := e.do("POST", "/api/auth/login", "", map[string]any{"phone": phone, "password": "bad", "device_name": "m"}); r.status != 401 {
			t.Fatalf("attempt %d: %d", i, r.status)
		}
	}
	r := e.do("POST", "/api/auth/login", "", map[string]any{"phone": phone, "password": "pw", "device_name": "m"})
	expect(t, r, 429, map[string]any{"message": "Too Many Attempts."})
	if r.header.Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
}

// ---------------------------------------------------------------------------
// Маршрутизация по клубу при поклубном переходе (Q-058)
// ---------------------------------------------------------------------------

func TestUpstreamRouting(t *testing.T) {
	var e *env
	e = newEnv(t, func(x *env) option { e = x; return withUpstream(x) })
	_, phone := e.user(e.club, "Местный", "client", 0)

	// Токен, которого здесь нет (клуб на Laravel), — на старый сервер без изменений.
	r := e.do("GET", "/api/guest/news", "77|"+strings.Repeat("z", 48), nil)
	expect(t, r, 200, map[string]any{"upstream": true, "path": "/api/guest/news"})
	// Вход по телефону без клубов на этом сервере — туда же.
	r = e.do("POST", "/api/auth/login", "", map[string]any{"phone": "+77779998877", "password": "pw", "device_name": "mobileclient"})
	expect(t, r, 200, map[string]any{"upstream": true})
	// Свой клуб — здесь.
	tok := e.token(phone, "mobileclient")
	expect(t, e.do("GET", "/api/guest/news", tok, nil), 200, map[string]any{"upstream": nil})
	// Отозванный свой токен на старый сервер не уходит.
	e.do("POST", "/api/auth/logout", tok, nil)
	expect(t, e.do("GET", "/api/guest/news", tok, nil), 401, nil)
	if n := e.upHits.Load(); n != 2 {
		t.Fatalf("upstream hits %d", n)
	}
}

// ---------------------------------------------------------------------------
// mobileclient: новости, журнал, друзья, QR (GuestFeaturesTest.php, MobileAppTest.php)
// ---------------------------------------------------------------------------

func TestGuest(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE clubs SET settings = '{"guests": {"allow_adding_friends": true, "allow_balance_transfer_to_friends": true}}'::jsonb WHERE id = $1`, e.club)
	_, aPhone := e.user(e.club, "Айгерим", "client", 0)
	_, bPhone := e.user(e.club, "Бауыржан", "client", 0)
	a, b := e.token(aPhone, "mobileclient"), e.token(bPhone, "mobileclient")

	t.Run("GS-099 friends by phone, masked, accepted", func(t *testing.T) {
		r := e.do("POST", "/api/guest/friends", a, map[string]any{"phone": "8" + bPhone[2:]})
		expect(t, r, 201, map[string]any{"message": "Заявка отправлена.", "outgoing.0.name": "Бауыржан",
			"outgoing.0.phone": "+7 " + bPhone[2:5] + " ••• •• " + bPhone[10:]})
		fid := int64(r.at("outgoing.0.id").(float64))
		r = e.do("GET", "/api/guest/friends", b, nil)
		expect(t, r, 200, map[string]any{"features.friends": true, "features.balance_transfer": true, "features.qr_login": false,
			"incoming.0.id": fid})
		expect(t, e.do("POST", fmt.Sprintf("/api/guest/friends/%d/accept", fid), a, nil), 422,
			map[string]any{"errors.friendship.0": "Заявка не найдена."})
		expect(t, e.do("POST", fmt.Sprintf("/api/guest/friends/%d/accept", fid), b, nil), 200,
			map[string]any{"message": "Вы теперь друзья.", "friends.0.name": "Айгерим"})
		expect(t, e.do("POST", "/api/guest/friends", a, map[string]any{"phone": bPhone}), 422,
			map[string]any{"errors.phone.0": "Этот клиент уже у вас в друзьях."})
		// Код перевода без Telegram — ответ старого кода.
		expect(t, e.do("POST", fmt.Sprintf("/api/guest/friends/%d/transfer/code", fid), a, map[string]any{"amount": "100"}), 422,
			map[string]any{"errors.code.0": "Подтверждение кодом из Telegram пока не подключено."})
		expect(t, e.do("POST", fmt.Sprintf("/api/guest/friends/%d/transfer/confirm", fid), a, map[string]any{"amount": "100", "code": "12"}), 422,
			map[string]any{"errors.code.0": "Код — 6 цифр из сообщения в Telegram."})
		expect(t, e.do("POST", fmt.Sprintf("/api/guest/friends/%d/transfer/confirm", fid), a, map[string]any{"amount": "100", "code": "123456"}), 422,
			map[string]any{"errors.code.0": "Код неверный или устарел. Запросите новый код."})
		expect(t, e.do("POST", fmt.Sprintf("/api/guest/friends/%d/remove", fid), a, nil), 200, map[string]any{"message": "Готово."})
		expect(t, e.do("POST", "/api/guest/friends/99999999/remove", a, nil), 404,
			map[string]any{"message": `No query results for model [App\Models\Friendship] 99999999`})
	})

	t.Run("GS-101 friends of another club and switched off", func(t *testing.T) {
		other := e.newClub(true)
		_, cPhone := e.user(other, "Чужой", "client", 0)
		expect(t, e.do("POST", "/api/guest/friends", a, map[string]any{"phone": cPhone}), 422,
			map[string]any{"errors.phone.0": "Клиент с таким номером в клубе не найден."})
		e.exec(`UPDATE clubs SET settings = '{}'::jsonb WHERE id = $1`, e.club)
		expect(t, e.do("POST", "/api/guest/friends", a, map[string]any{"phone": bPhone}), 403,
			map[string]any{"message": "Эта возможность отключена в клубе."})
	})

	t.Run("GS-519 news and history", func(t *testing.T) {
		e.exec("INSERT INTO news (club_id, title, body, image_path, is_published, created_at) VALUES ($1, 'Клубная', 'Текст', 'news/a.jpg', true, now())", e.club)
		r := e.do("GET", "/api/guest/news", a, nil)
		expect(t, r, 200, map[string]any{"club.0.title": "Клубная"})
		if u, _ := r.at("club.0.image_url").(string); !strings.HasSuffix(u, "/a.jpg") {
			t.Errorf("image_url %v", r.at("club.0.image_url"))
		}
		r = e.do("GET", "/api/guest/history", a, nil)
		expect(t, r, 200, map[string]any{"data.0.kind": "login", "data.0.title": "Вход в аккаунт", "data.0.note": "mobileclient", "next_before": nil})
		expect(t, e.do("GET", "/api/guest/history?filter=bad", a, nil), 422, nil)
	})

	t.Run("GS-522 QR codes: expired and other club", func(t *testing.T) {
		expect(t, e.do("GET", "/api/guest/qr-login/"+strings.Repeat("A", 40), a, nil), 410, map[string]any{"code": "qr_expired"})
		other := e.newClub(true)
		zone := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Z', now(), now()) RETURNING id", other)
		pc := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 7, now(), now()) RETURNING id", other, zone)
		code := strings.Repeat("B", 40)
		sum := sha256.Sum256([]byte(code))
		e.exec(`INSERT INTO agent_qr_logins (id, club_id, club_computer_id, code_hash, expires_at, created_at)
			VALUES (gen_random_uuid(), $1, $2, $3, now() + interval '5 minutes', now())`, other, pc, hex.EncodeToString(sum[:]))
		expect(t, e.do("GET", "/api/guest/qr-login/"+code, a, nil), 403, map[string]any{"code": "other_club"})
		expect(t, e.do("GET", "/api/guest/qr-login/short", a, nil), 404, nil)
	})
}

// ---------------------------------------------------------------------------
// mobileemployee (StaffApiTest.php)
// ---------------------------------------------------------------------------

func TestStaff(t *testing.T) {
	e := newEnv(t)
	_, clientPhone := e.user(e.club, "Клиент", "client", 0)
	cashierID, cashierPhone := e.user(e.club, "Кассир", "cashier", 0)
	_, operatorPhone := e.user(e.club, "Оператор", "operator", 0)
	_, managerPhone := e.user(e.club, "Менеджер", "manager", 0)
	client := e.token(clientPhone, "Zafiro Staff")
	cashier := e.token(cashierPhone, "Zafiro Staff")
	operator := e.token(operatorPhone, "Zafiro Staff")
	manager := e.token(managerPhone, "Zafiro Staff")

	t.Run("GS-274 only staff; me tells role, permissions and counters", func(t *testing.T) {
		expect(t, e.do("GET", "/api/staff/me", client, nil), 403, nil)
		expect(t, e.do("GET", "/api/staff/computers", client, nil), 403, nil)
		r := e.do("GET", "/api/staff/me", cashier, nil)
		expect(t, r, 200, map[string]any{"role": "cashier", "user.name": "Кассир", "club.currency": "KZT", "counters.orders_new": 0, "shift": nil,
			"permissions.cash": true, "permissions.guest_session": true, "permissions.end_session": false, "permissions.control": true,
			"permissions.chat_reply": true, "permissions.stock_receive": true, "permissions.stock_manage": false, "permissions.reports": false})
		expect(t, e.do("GET", "/api/staff/me", operator, nil), 200, map[string]any{"permissions.cash": false, "permissions.end_session": true})
		expect(t, e.do("GET", "/api/staff/cash", operator, nil), 403, map[string]any{"message": "Нет права на этот раздел. Его даёт владелец клуба в «Настройки → Права»."})
		expect(t, e.do("GET", "/api/staff/reports/today", cashier, nil), 403, nil)
		expect(t, e.do("POST", "/api/staff/stock/movements", cashier, map[string]any{}), 403, nil)
	})

	t.Run("GS-275 sign-in tells the role in each club", func(t *testing.T) {
		expect(t, e.login(cashierPhone, "Zafiro Staff"), 200, map[string]any{"user.role": "cashier", "clubs.0.role": "cashier"})
	})

	t.Run("GS-277 a cashier works the whole shift", func(t *testing.T) {
		e.exec(`UPDATE clubs SET settings = '{"finances": {"topup_bonus_enabled": true, "topup_bonus_levels": [{"amount": 1000, "percent": 10}]}}'::jsonb WHERE id = $1`, e.club)
		guest, _ := e.user(e.club, "Гость", "client", 0)
		cola := e.scalar("INSERT INTO products (club_id, sku, name, category, price, quantity, created_at, updated_at) VALUES ($1, 'COLA', 'Cola', 'Напитки', 35000, 10, now(), now()) RETURNING id", e.club)

		expect(t, e.do("GET", "/api/staff/cash", cashier, nil), 200, map[string]any{"shift": nil, "transactions": "[]"})
		expect(t, e.do("POST", "/api/staff/cash/top-ups", cashier, map[string]any{"user_id": guest, "amount": 1000, "balance_type": "balance", "payment_method": "cash"}), 422, nil)
		r := e.do("POST", "/api/staff/cash/shifts", cashier, map[string]any{"opening_balance": 5000})
		expect(t, r, 201, map[string]any{"shift.opening_balance": "5000.00"})
		shiftID := int64(r.at("shift.id").(float64))
		expect(t, e.do("POST", "/api/staff/cash/top-ups", cashier, map[string]any{"user_id": guest, "amount": 2000, "balance_type": "balance", "payment_method": "card"}), 200,
			map[string]any{"bonus": "200.00", "balance": "2000.00", "bonus_balance": "200.00"})
		expect(t, e.do("POST", "/api/staff/cash/sales", cashier, map[string]any{"items": []any{map[string]any{"product_id": cola, "quantity": 2}}}), 201,
			map[string]any{"amount": "700.00", "items.0.total": "700.00", "items.0.name": "Cola"})
		expect(t, e.do("POST", "/api/staff/cash/transactions", cashier, map[string]any{"type": "expense", "amount": 100, "description": "Вода"}), 201,
			map[string]any{"amount": "100.00", "type": "expense"})
		expect(t, e.do("POST", "/api/staff/cash/transactions", cashier, map[string]any{"type": "expense", "amount": 99999, "description": "Много"}), 422, nil)
		expect(t, e.do("GET", "/api/staff/me", cashier, nil), 200, map[string]any{"shift.id": shiftID, "shift.balance": "7600.00"})
		r = e.do("GET", "/api/staff/cash", cashier, nil)
		expect(t, r, 200, map[string]any{"totals.income": "2700.00", "totals.expense": "100.00", "totals.balance": "7600.00", "report.items.0.name": "Cola"})
		if n := len(r.at("transactions").([]any)); n != 3 {
			t.Errorf("transactions %d", n)
		}
		expect(t, e.do("POST", fmt.Sprintf("/api/staff/cash/shifts/%d/close", shiftID), cashier, map[string]any{}), 200,
			map[string]any{"shift.closing_balance": "7600.00", "report.staff.0.name": "Кассир"})
		expect(t, e.do("GET", "/api/staff/cash", cashier, nil), 200, map[string]any{"shift": nil})
		// Деньги — через ledger: проводка пополнения клиента есть.
		if n := e.scalar("SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind = 'top_up'", e.club); n != 1 {
			t.Errorf("ledger top-ups %d", n)
		}
		_ = cashierID
	})

	t.Run("GS-278 customers by name or phone", func(t *testing.T) {
		erlan, erlanPhone := e.user(e.club, "Ерлан Сейтов", "client", 0)
		e.exec("UPDATE club_memberships SET discount = 5 WHERE club_id = $1 AND user_id = $2", e.club, erlan)
		r := e.do("GET", "/api/staff/customers?search="+url.QueryEscape("Ерлан"), operator, nil)
		expect(t, r, 200, map[string]any{"data.0.user_id": erlan, "data.0.phone": erlanPhone, "data.0.discount": "5.00",
			"data.0.discount_cumulative": false, "data.0.total_spent": "0.00", "data.0.balance": "0.00", "meta.current_page": 1, "meta.last_page": 1})
		if n := len(r.at("data").([]any)); n != 1 {
			t.Errorf("found %d", n)
		}
		r = e.do("GET", "/api/staff/customers?search="+url.QueryEscape("8 "+erlanPhone[2:5]+" "+erlanPhone[5:8]+" "+erlanPhone[8:]), operator, nil)
		expect(t, r, 200, map[string]any{"data.0.user_id": erlan})
	})

	t.Run("GS-279 goods: everyone receives, managers also write off", func(t *testing.T) {
		chips := e.scalar("INSERT INTO products (club_id, sku, name, category, price, quantity, created_at, updated_at) VALUES ($1, 'CHIPS', 'Chips', 'Еда', 35000, 3, now(), now()) RETURNING id", e.club)
		expect(t, e.do("GET", "/api/staff/products?search=chi", operator, nil), 200, map[string]any{"data.0.quantity": 3, "data.0.price": "350.00"})
		expect(t, e.do("POST", "/api/staff/stock/receive", operator, map[string]any{"product_id": chips, "quantity": 5}), 200, map[string]any{"quantity": 8})
		expect(t, e.do("POST", "/api/staff/stock/movements", operator, map[string]any{"product_id": chips, "type": "write_off", "quantity": 2}), 403, nil)
		expect(t, e.do("POST", "/api/staff/stock/movements", manager, map[string]any{"product_id": chips, "type": "write_off", "quantity": 2}), 200, map[string]any{"quantity": 6})
		expect(t, e.do("POST", "/api/staff/stock/movements", manager, map[string]any{"product_id": chips, "type": "write_off", "quantity": 99}), 422, nil)
	})

	t.Run("GS-280 orders handed over or canceled once", func(t *testing.T) {
		arman, _ := e.user(e.club, "Арман", "client", 0)
		items := `[{"product_id": 999, "name": "Cola", "quantity": 2, "price": "350.00", "total": "700.00"}]`
		o1 := e.scalar(`INSERT INTO shop_orders (club_id, user_id, computer_name, items, amount, status, created_at, updated_at)
			VALUES ($1, $2, 'PC 5', $3::jsonb, 70000, 'new', now(), now()) RETURNING id`, e.club, arman, items)
		o2 := e.scalar(`INSERT INTO shop_orders (club_id, user_id, computer_name, items, amount, status, created_at, updated_at)
			VALUES ($1, $2, 'PC 6', '[]'::jsonb, 10000, 'new', now(), now()) RETURNING id`, e.club, arman)
		r := e.do("GET", "/api/staff/orders", operator, nil)
		expect(t, r, 200, map[string]any{"new_count": 2, "data.1.items.0.name": "Cola", "data.1.items.0.price": "350.00", "data.1.customer.name": "Арман"})
		expect(t, e.do("POST", fmt.Sprintf("/api/staff/orders/%d/done", o1), operator, map[string]any{}), 200,
			map[string]any{"status": "done", "handled_by": "Оператор"})
		r = e.do("POST", fmt.Sprintf("/api/staff/orders/%d/done", o1), operator, map[string]any{})
		if r.status != 422 || r.at("errors.order") == nil {
			t.Fatalf("second done: %d %s", r.status, r.raw)
		}
		expect(t, e.do("POST", fmt.Sprintf("/api/staff/orders/%d/cancel", o2), operator, map[string]any{}), 200, map[string]any{"status": "cancelled"}) //nolint:misspell // статус заказа
		if bal := e.scalar("SELECT balance FROM member_balances WHERE club_id = $1 AND user_id = $2", e.club, arman); bal != 10000 {
			t.Errorf("refund balance %d", bal)
		}
		expect(t, e.do("GET", "/api/staff/orders?status=all", operator, nil), 200, map[string]any{"new_count": 0})
		// Заказ чужого клуба — 404 модели.
		other := e.newClub(true)
		stranger, _ := e.user(other, "Чужой", "client", 0)
		o3 := e.scalar(`INSERT INTO shop_orders (club_id, user_id, items, amount, status, created_at, updated_at)
			VALUES ($1, $2, '[]'::jsonb, 100, 'new', now(), now()) RETURNING id`, other, stranger)
		expect(t, e.do("POST", fmt.Sprintf("/api/staff/orders/%d/done", o3), operator, map[string]any{}), 404, nil)
	})

	t.Run("GS-281 chat, notifications and the day report", func(t *testing.T) {
		guest, _ := e.user(e.club, "Гость чата", "client", 0)
		e.exec("INSERT INTO chat_messages (club_id, customer_id, from_staff, body, created_at, updated_at) VALUES ($1, $2, false, 'Помогите', now(), now())", e.club, guest)
		expect(t, e.do("GET", "/api/staff/chats", manager, nil), 200, map[string]any{"conversations.0.customer.name": "Гость чата",
			"conversations.0.unread": 1, "conversations.0.last.body": "Помогите"})
		expect(t, e.do("POST", fmt.Sprintf("/api/staff/chat/%d", guest), manager, map[string]any{"body": "Иду"}), 403, nil)
		expect(t, e.do("GET", fmt.Sprintf("/api/staff/chat/poll?customer=%d", guest), operator, nil), 200,
			map[string]any{"messages.0.body": "Помогите", "unread": 0})
		expect(t, e.do("POST", fmt.Sprintf("/api/staff/chat/%d", guest), operator, map[string]any{"body": "Иду"}), 201,
			map[string]any{"message.from_staff": true, "message.body": "Иду"})
		r := e.do("GET", "/api/staff/notifications", operator, nil)
		if r.status != 200 || r.body["unread_count"] == nil || r.body["items"] == nil {
			t.Fatalf("notifications: %d %s", r.status, r.raw)
		}
		expect(t, e.do("POST", "/api/staff/notifications/seen", operator, map[string]any{}), 200, map[string]any{"unread_count": 0})

		e.exec("INSERT INTO products (club_id, sku, name, category, price, quantity, created_at, updated_at) VALUES ($1, 'LOW', 'Чипсы', 'Еда', 20000, 0, now(), now())", e.club)
		r = e.do("GET", "/api/staff/reports/today", manager, nil)
		expect(t, r, 200, map[string]any{"low_stock.0.name": "Чипсы", "currency": "KZT"})
		if n := len(r.at("week").([]any)); n != 7 {
			t.Errorf("week %d", n)
		}
	})

	t.Run("GS-276 club map, control and end session rights", func(t *testing.T) {
		zone := e.scalar("INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, 'Hall', 60000, now(), now()) RETURNING id", e.club)
		pc := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 2, now(), now()) RETURNING id", e.club, zone)
		tariff := e.scalar(`INSERT INTO tariffs (club_id, name, duration_minutes, price, is_active, created_at, updated_at)
			VALUES ($1, 'Час', 60, 80000, true, now(), now()) RETURNING id`, e.club)
		r := e.do("GET", "/api/staff/computers", operator, nil)
		expect(t, r, 200, map[string]any{"zones.0.price_per_hour": "600.00", "computers.0.id": pc, "computers.0.connection": "offline",
			"computers.0.session": nil, "computers.0.agent_version": nil, "tariffs.0.id": tariff, "tariffs.0.price": "800.00",
			"tariffs.0.minutes": 60, "guest_mode": true})
		expect(t, e.do("POST", "/api/staff/computers/control", operator, map[string]any{"computer_ids": []int64{pc}, "action": "reserve"}), 200,
			map[string]any{"reserved": true, "computers.0.status": "reserved"})
		expect(t, e.do("POST", fmt.Sprintf("/api/staff/computers/%d/end-session", pc), cashier, map[string]any{"session_id": "s"}), 403, nil)
		r = e.do("POST", fmt.Sprintf("/api/staff/computers/%d/end-session", pc), operator, map[string]any{"session_id": "s"})
		if r.status != 422 || r.at("errors.session_id") == nil {
			t.Fatalf("end session without session: %d %s", r.status, r.raw)
		}
	})
}
