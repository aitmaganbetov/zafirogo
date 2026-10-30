package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Middleware `subscription` = RequireActiveSubscription (bootstrap/app.php): разделы панели клуба — только при
// активной подписке выбранного клуба; иначе JSON 402 «Подписка клуба не активна.» (страницы SPA по 402 уводят на
// /billing, как redirect()->route('billing.show')). В старом его нет у billing.show, auth.logout, clubs.switch,
// notifications.* (routes/web.php:169–176) и у маршрутов вне группы клуба (вход, приглашения, QR-вход, публичные).

// MsgSubscriptionInactive — RequireActiveSubscription: __('Подписка клуба не активна.').
const MsgSubscriptionInactive = "Подписка клуба не активна."

// CodeSubscriptionInactive — код ошибки 402.
const CodeSubscriptionInactive = "subscription_inactive"

// subscriptionExempt — пути /api без middleware subscription (префикс целым сегментом).
var subscriptionExempt = []string{
	"/api/auth",          // вход, выход (auth.logout), сессия, смена клуба (clubs.switch), регистрация
	"/api/superadmin",    // кабинет и вход/выход из просмотра клуба
	"/api/invitations",   // приглашения (вне группы клуба)
	"/api/ws-ticket",     // билет WebSocket (события, не данные раздела)
	"/api/i18n",          // переводы
	"/api/layout",        // бейджи меню: каркас есть и на странице подписки
	"/api/notifications", // notifications.index/seen — без subscription
	"/api/billing",       // billing.show — сюда ведёт неактивная подписка
	"/api/pc-login",      // QR-вход на ПК (свои проверки подписки клуба кода)
	"/api/landing",       // лендинг
}

func subscriptionExemptPath(path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return true
	}
	for _, p := range subscriptionExempt {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// subscriptionActive — ClubSubscription::isActive() (BR-130): active без конца периода или с концом в будущем,
// trialing с концом пробного периода в будущем. Нет подписки — не активна (currentClub->subscription === null).
func subscriptionActive(row store.GuestClubSubscriptionRow, now time.Time) bool {
	switch row.Status {
	case "active":
		return row.CurrentPeriodEndsAt == nil || row.CurrentPeriodEndsAt.After(now)
	case "trialing":
		return row.TrialEndsAt != nil && row.TrialEndsAt.After(now)
	}
	return false
}

// subscriptionLookup — подписка клуба (ok = false — подписки нет).
type subscriptionLookup func(ctx context.Context, clubID int64) (row store.GuestClubSubscriptionRow, ok bool, err error)

// subscriptionCacheTTL — сколько держится прочитанная подписка клуба (строка, не вердикт: срок сверяется с часами
// на каждом запросе). Продление суперадмином видно не позже чем через TTL.
const subscriptionCacheTTL = 10 * time.Second

type subscriptionCache struct {
	lookup subscriptionLookup
	now    func() time.Time
	mu     sync.Mutex
	rows   map[int64]cachedSubscription
}

type cachedSubscription struct {
	row     store.GuestClubSubscriptionRow
	ok      bool
	fetched time.Time
}

func (c *subscriptionCache) get(ctx context.Context, clubID int64) (store.GuestClubSubscriptionRow, bool, error) {
	mono := time.Now()
	c.mu.Lock()
	e, hit := c.rows[clubID]
	c.mu.Unlock()
	if hit && mono.Sub(e.fetched) < subscriptionCacheTTL {
		return e.row, e.ok, nil
	}
	row, ok, err := c.lookup(ctx, clubID)
	if err != nil {
		return row, ok, err
	}
	c.mu.Lock()
	c.rows[clubID] = cachedSubscription{row: row, ok: ok, fetched: mono}
	c.mu.Unlock()
	return row, ok, nil
}

// poolSubscriptionLookup — подписка из club_subscriptions в тенантной транзакции клуба.
func poolSubscriptionLookup(pool db.Beginner) subscriptionLookup {
	return func(ctx context.Context, clubID int64) (store.GuestClubSubscriptionRow, bool, error) {
		var (
			row store.GuestClubSubscriptionRow
			ok  bool
		)
		err := db.InTenantTx(ctx, pool, clubID, func(q *store.Queries) error {
			r, err := q.GuestClubSubscription(ctx, clubID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			row, ok = r, true
			return nil
		})
		return row, ok, err
	}
}

// requireSubscription — middleware subscription для /api панели клуба. Без Principal или без выбранного клуба —
// дальше (ответ 401/403 даст сам раздел).
func requireSubscription(lookup subscriptionLookup, now func() time.Time, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if lookup == nil {
			return next
		}
		if now == nil {
			now = time.Now
		}
		cache := &subscriptionCache{lookup: lookup, now: now, rows: map[int64]cachedSubscription{}}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if subscriptionExemptPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			p, ok := principal.FromContext(r.Context())
			if !ok || p.ClubID <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			row, found, err := cache.get(r.Context(), p.ClubID)
			if err != nil {
				log.ErrorContext(r.Context(), "subscription check", slog.Int64("club_id", p.ClubID), slog.String("err", err.Error()))
				permissions.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "")
				return
			}
			if !found || !subscriptionActive(row, now()) {
				permissions.WriteError(w, http.StatusPaymentRequired, CodeSubscriptionInactive, MsgSubscriptionInactive)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
