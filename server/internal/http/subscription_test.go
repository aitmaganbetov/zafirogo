package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// RequireActiveSubscription: какие пути проверяются, какой ответ и ClubSubscription::isActive().
func TestRequireSubscription(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	subs := map[int64]store.GuestClubSubscriptionRow{
		1: {Status: "active"},                               // без конца периода
		2: {Status: "active", CurrentPeriodEndsAt: &future}, // период идёт
		3: {Status: "active", CurrentPeriodEndsAt: &past},   // период кончился, subscriptions:expire ещё не прошёл
		4: {Status: "trialing", TrialEndsAt: &future},
		5: {Status: "trialing", TrialEndsAt: &past},
		6: {Status: "trialing"},
		7: {Status: "expired"},
		8: {Status: "suspended", CurrentPeriodEndsAt: &future},
		// 9 — подписки нет
	}
	var lookups atomic.Int64
	lookup := func(_ context.Context, clubID int64) (store.GuestClubSubscriptionRow, bool, error) {
		lookups.Add(1)
		row, ok := subs[clubID]
		return row, ok, nil
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := requireSubscription(lookup, func() time.Time { return now }, slog.New(slog.DiscardHandler))(ok)

	do := func(path string, clubID int64) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if clubID >= 0 {
			r = r.WithContext(principal.WithPrincipal(r.Context(), principal.Principal{UserID: 10, ClubID: clubID, Role: "owner"}))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	for clubID, want := range map[int64]int{1: 204, 2: 204, 3: 402, 4: 204, 5: 402, 6: 402, 7: 402, 8: 402, 9: 402} {
		if got := do("/api/cash/shift", clubID).Code; got != want {
			t.Errorf("club %d: %d, want %d", clubID, got, want)
		}
	}

	w := do("/api/club-map", 7)
	var body struct{ Error, Message string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusPaymentRequired ||
		body.Error != CodeSubscriptionInactive || body.Message != "Подписка клуба не активна." {
		t.Fatalf("402 body: %d %s", w.Code, w.Body)
	}

	// Без middleware в старом: billing.show, logout, clubs.switch, notifications, вход, QR-вход, публичные, SPA.
	for _, p := range []string{"/api/auth/logout", "/api/auth/club", "/api/auth/me", "/api/notifications", "/api/notifications/seen",
		"/api/billing", "/api/layout/badges", "/api/i18n/ru", "/api/pc-login/x", "/api/superadmin/leave-club", "/api/ws-ticket",
		"/api/invitations/t", "/api/landing", "/dashboard", "/offline"} {
		if got := do(p, 7).Code; got != 204 {
			t.Errorf("%s: %d, want 204 (exempt)", p, got)
		}
	}
	// Похожий префикс — не исключение.
	if got := do("/api/billingx", 7).Code; got != 402 {
		t.Errorf("/api/billingx: %d", got)
	}
	// Без входа и без клуба — дальше (401/403 даст раздел).
	if got := do("/api/cash/shift", -1).Code; got != 204 {
		t.Errorf("no principal: %d", got)
	}
	if got := do("/api/cash/shift", 0).Code; got != 204 {
		t.Errorf("no club: %d", got)
	}

	// Строка подписки кэшируется (не запрос на каждый вызов).
	before := lookups.Load()
	for range 5 {
		do("/api/cash/shift", 2)
	}
	if n := lookups.Load() - before; n != 0 {
		t.Fatalf("lookups: %d", n)
	}
}
