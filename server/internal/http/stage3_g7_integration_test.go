//go:build integration

package httpserver

// Этап 3, группа 7: QR-вход на ПК (/api/pc-login/{code}), проверка подписки (RequireActiveSubscription), лендинг —
// HTTP → Postgres (RLS).

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
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
)

type g7Approve struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func newG7Env(t *testing.T, now time.Time) (*adminEnv, *g7Approve) {
	t.Helper()
	d, vk := sharedAdmin(t)
	h, _ := password.New(password.Options{})
	sm, err := session.New(session.Options{Client: vk, Keys: []session.Key{{ID: "t", Secret: bytes.Repeat([]byte{7}, 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(auth.Options{Pool: d.App, Hasher: h, Sessions: sm})
	if err != nil {
		t.Fatal(err)
	}
	cashSvc := &cashsvc.Service{Notifier: &fakeNotifier{}}
	ap := &g7Approve{}
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Passwords: h,
		Pool: d.App, PlatformPool: d.Platform, Billing: billingsvc.New(d.App, nil, cashSvc), Cash: cashSvc,
		Presence: &fakePresence{states: map[int64][]presence.State{}}, Now: func() time.Time { return now },
		RequireSubscription: true,
		ApproveQrLogin: func(_ context.Context, clubID int64, code string, userID int64) error {
			ap.mu.Lock()
			defer ap.mu.Unlock()
			ap.calls = append(ap.calls, fmt.Sprintf("%d:%s:%d", clubID, code, userID))
			return ap.err
		},
	}))
	t.Cleanup(srv.Close)
	return &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("g7%d", adminSeq.Add(1)))}, ap
}

func (e *adminEnv) g7Subscription(status string, ends time.Time) {
	e.t.Helper()
	plan := e.scalar(`INSERT INTO saas_plans (slug, name, trial_days, features, created_at, updated_at)
		VALUES ($1, 'План', 30, '{}'::jsonb, now(), now()) RETURNING id`, fmt.Sprintf("g7-%d", adminSeq.Add(1)))
	e.exec(`INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, current_period_ends_at, trial_ends_at)
		VALUES ($1, $2, $3, now(), $4, $4)`, e.club, plan, status, ends)
}

func TestG7Subscription(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)

	t.Run("expired", func(t *testing.T) {
		e, _ := newG7Env(t, now)
		e.g7Subscription("active", now.Add(-time.Hour)) // период кончился, статус ещё active
		_, owner := e.staff("Владелец", "owner")
		r := e.get(owner, "/api/club-computers")
		expect(t, r, http.StatusPaymentRequired, "panel section")
		if r.body["message"] != "Подписка клуба не активна." || r.body["error"] != "subscription_inactive" {
			t.Fatalf("402 body: %v", r.body)
		}
		expect(t, e.get(owner, "/api/auth/me"), http.StatusOK, "me (exempt)")
		expect(t, e.get(owner, "/api/notifications"), http.StatusOK, "notifications (exempt)")
		// Экран «Подписка» — без проверки подписки (сюда ведёт 402).
		r = e.get(owner, "/api/billing")
		expect(t, r, http.StatusOK, "billing (exempt)")
		if sub := obj(r.body["subscription"]); sub["state"] != "expired" || sub["plan_name"] != "План" || sub["status"] != "active" {
			t.Fatalf("billing: %v", r.body)
		}
		_, cashier := e.staff("Кассир", "cashier")
		expect(t, e.get(cashier, "/api/billing"), http.StatusForbidden, "billing.view") // по умолчанию — владелец
	})

	t.Run("active", func(t *testing.T) {
		e, _ := newG7Env(t, now)
		e.g7Subscription("trialing", now.Add(time.Hour))
		_, owner := e.staff("Владелец", "owner")
		expect(t, e.get(owner, "/api/club-computers"), http.StatusOK, "panel section")
	})

	t.Run("no subscription", func(t *testing.T) {
		e, _ := newG7Env(t, now)
		_, owner := e.staff("Владелец", "owner")
		expect(t, e.get(owner, "/api/dashboard"), http.StatusPaymentRequired, "dashboard")
		r := e.get(owner, "/api/billing")
		if r.status != http.StatusOK || r.body["subscription"] != nil {
			t.Fatalf("billing without subscription: %d %v", r.status, r.body)
		}
	})
}

func TestG7PcLogin(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e, ap := newG7Env(t, now)
	e.g7Subscription("active", now.Add(24*time.Hour))
	e.exec("UPDATE clubs SET name = 'Arena' WHERE id = $1", e.club)
	zone := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Зал', now(), now()) RETURNING id", e.club)
	pc := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 7, now(), now()) RETURNING id", e.club, zone)
	code := "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd"
	sum := sha256.Sum256([]byte(code))
	e.exec(`INSERT INTO agent_qr_logins (id, club_id, club_computer_id, code_hash, expires_at, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, now(), now())`, e.club, pc, hex.EncodeToString(sum[:]), now.Add(4*time.Minute))
	stale := "ZZCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd"
	sum2 := sha256.Sum256([]byte(stale))
	e.exec(`INSERT INTO agent_qr_logins (id, club_id, club_computer_id, code_hash, expires_at, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, now(), now())`, e.club, pc, hex.EncodeToString(sum2[:]), now.Add(-time.Minute))

	// Клиент входит на сайт ради QR (pc_login: true).
	uid, phone := e.member("Гость Иванов", "client", "", false)
	r := do(t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": phone, "password": "pw", "pc_login": true}, nil)
	expect(t, r, http.StatusOK, "login")
	tok := r.body["access_token"].(string)

	expect(t, do(t, "GET", e.srv.URL+"/api/pc-login/"+code, nil, nil), http.StatusUnauthorized, "guest")
	expect(t, e.get(tok, "/api/pc-login/short"), http.StatusNotFound, "bad code")

	r = e.get(tok, "/api/pc-login/"+code)
	expect(t, r, http.StatusOK, "show")
	if r.body["open"] != true || r.body["club_name"] != "Arena" || r.body["computer_name"] != "PC-07" || r.body["user_name"] != "Гость Иванов" {
		t.Fatalf("show: %v", r.body)
	}
	r = e.get(tok, "/api/pc-login/"+stale)
	if r.status != http.StatusOK || r.body["open"] != false || r.body["club_name"] != nil {
		t.Fatalf("stale: %d %v", r.status, r.body)
	}

	r = e.post(tok, "/api/pc-login/"+code, nil)
	expect(t, r, http.StatusOK, "approve")
	if r.body["approved"] != true || len(ap.calls) != 1 || ap.calls[0] != fmt.Sprintf("%d:%s:%d", e.club, code, uid) {
		t.Fatalf("approve: %v %v", r.body, ap.calls)
	}
	ap.err = status.Error(codes.InvalidArgument, "Подписка клуба не активна.")
	expectField(t, e.post(tok, "/api/pc-login/"+code, nil), "code", "Подписка клуба не активна.")
	ap.err = status.Error(codes.NotFound, "QR-код устарел.")
	if r = e.post(tok, "/api/pc-login/"+code, nil); r.status != http.StatusOK || r.body["approved"] != false {
		t.Fatalf("race: %d %v", r.status, r.body)
	}
	if r = e.post(tok, "/api/pc-login/"+stale, nil); r.status != http.StatusOK || r.body["approved"] != false || len(ap.calls) != 3 {
		t.Fatalf("stale approve: %d %v %v", r.status, r.body, ap.calls)
	}
}

func TestG7Landing(t *testing.T) {
	e, _ := newG7Env(t, time.Now())
	r := do(t, "GET", e.srv.URL+"/api/landing", nil, nil)
	expect(t, r, http.StatusOK, "landing")
	if r.body["trial_days"].(float64) < 14 || r.body["download_url"] == "" {
		t.Fatalf("landing: %v", r.body)
	}
	res, err := http.Get(e.srv.URL + "/offline")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("offline: %d %v", res.StatusCode, res.Header)
	}
}
