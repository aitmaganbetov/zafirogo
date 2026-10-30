//go:build integration

package httpserver

// Этап 3, группа 5: настройки клуба и «API и интеграции» — HTTP → Postgres (RLS).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
)

// g5Hooks — вызовы ConfigChanged и пробуждения вебхуков.
type g5Hooks struct {
	mu      sync.Mutex
	configs []int64
	wakes   int
}

func (g *g5Hooks) configCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.configs)
}

func newG5Env(t *testing.T, now time.Time) (*adminEnv, *g5Hooks) {
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
	lim, err := ratelimit.New(ratelimit.Options{Client: vk})
	if err != nil {
		t.Fatal(err)
	}
	cashSvc := &cashsvc.Service{Notifier: &fakeNotifier{}}
	g := &g5Hooks{}
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim, Passwords: h,
		Pool: d.App, PlatformPool: d.Platform, Billing: billingsvc.New(d.App, nil, cashSvc), Cash: cashSvc,
		Presence: &fakePresence{states: map[int64][]presence.State{}}, Now: func() time.Time { return now },
		ClubConfigChanged: func(_ context.Context, clubID int64) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.configs = append(g.configs, clubID)
		},
		WebhookURLCheck: func(_ context.Context, raw string) string {
			if strings.Contains(raw, "10.0.0.1") {
				return "private_address"
			}
			return ""
		},
		WakeWebhooks: func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.wakes++
		},
	}))
	t.Cleanup(srv.Close)
	e := &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("g5%d", adminSeq.Add(1)))}
	e.exec("UPDATE clubs SET timezone = 'Asia/Almaty' WHERE id = $1", e.club)
	return e, g
}

func (e *adminEnv) upload(method, tok, path, field string, img []byte) resp {
	e.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if img != nil {
		fw, _ := w.CreateFormFile(field, "pic.png")
		_, _ = fw.Write(img)
	}
	_ = w.Close()
	req, _ := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	out := resp{status: res.StatusCode, header: res.Header}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e *adminEnv) clubSettings() map[string]map[string]any {
	e.t.Helper()
	var raw []byte
	if err := e.db.Owner.QueryRow(context.Background(), "SELECT settings FROM clubs WHERE id = $1", e.club).Scan(&raw); err != nil {
		e.t.Fatal(err)
	}
	out := map[string]map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestG5Settings(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e, g := newG5Env(t, now)
	_, owner := e.staff("Владелец", "owner")
	_, manager := e.staff("Менеджер", "manager")
	_, cashier := e.staff("Кассир", "cashier")
	e.settings(`{"club":{"pc_restart_after_session_end":"30 сек"},"finances":{"loyalty_levels":[{"spent":1000,"percent":5}]}}`)

	t.Run("view", func(t *testing.T) {
		r := e.get(manager, "/api/settings")
		expect(t, r, http.StatusOK, "settings")
		s := obj(r.body["settings"])
		club, fin := obj(s["club"]), obj(s["finances"])
		if club["pc_restart_after_session_end"] != 0.5 || club["booking_interval"] != 30.0 || fin["currency"] != "KZT" ||
			obj(s["guests"])["guest_mode_enabled"] != true {
			t.Fatalf("defaults: %v", s)
		}
		levels, _ := fin["loyalty_levels"].([]any)
		if len(levels) != 1 || obj(levels[0])["threshold_tiyn"] != 100000.0 || obj(levels[0])["percent_bp"] != 500.0 {
			t.Fatalf("levels: %v", fin["loyalty_levels"])
		}
		if r.body["club_time"] != "15.10.2026 12:00" || r.body["can_manage_agent_pin"] != false || r.body["agent_pin_set_at"] != nil {
			t.Fatalf("page: %v", r.body)
		}
		kz, _ := r.body["timezones_kazakhstan"].([]any)
		if len(kz) != 7 || obj(kz[0])["label"] != "(UTC+05:00) Asia/Almaty" {
			t.Fatalf("zones: %v", kz)
		}
		if r := e.get(cashier, "/api/settings"); r.status != http.StatusForbidden {
			t.Fatalf("cashier: %d", r.status)
		}
		if r := e.get(owner, "/api/settings"); r.body["can_manage_agent_pin"] != true {
			t.Fatalf("owner pin: %v", r.body["can_manage_agent_pin"])
		}
	})

	t.Run("save club", func(t *testing.T) {
		before := g.configCount()
		r := e.send("PUT", manager, "/api/settings/sections/club", map[string]any{
			"session_idle_timeout": "15", "pc_action_after_session_end": "restart", "pc_restart_after_session_end": "1",
			"session_auto_terminate_when_pc_unavailable": "5", "booking_interval": "45", "max_cashiers_on_shift": "2",
			"max_operators_on_shift": "0", "agent_idle_logout_minutes": "5", "agent_shop_enabled": "0", "agent_auto_update": "1",
		})
		expect(t, r, http.StatusOK, "save")
		if r.body["message"] != "Настройки сохранены" {
			t.Fatalf("message: %v", r.body)
		}
		s := e.clubSettings()
		if s["club"]["booking_interval"] != 45.0 || s["club"]["pc_action_after_session_end"] != "restart" ||
			s["club"]["agent_shop_enabled"] != false || s["finances"]["currency"] != "KZT" {
			t.Fatalf("stored: %v", s)
		}
		d := e.activityDetails("settings.updated")
		if d["section"] != "club" || d["booking_interval"] != "30 → 45" || d["agent_shop_enabled"] != "да → нет" ||
			d["pc_restart_after_session_end"] != "0.5 → 1" {
			t.Fatalf("details: %v", d)
		}
		if g.configCount() != before+1 {
			t.Fatal("ConfigChanged")
		}
		expectField(t, e.send("PUT", manager, "/api/settings/sections/club", map[string]any{"booking_interval": "x"}),
			"session_idle_timeout", "The session idle timeout field is required.")
		if r := e.send("PUT", cashier, "/api/settings/sections/club", map[string]any{}); r.status != http.StatusForbidden {
			t.Fatalf("cashier save: %d", r.status)
		}
	})

	t.Run("save finances", func(t *testing.T) {
		r := e.send("PUT", manager, "/api/settings/sections/finances", map[string]any{
			"currency": "KZT", "loyalty_enabled": "1",
			"loyalty_levels":     []map[string]string{{"spent": "50000", "percent": "10"}, {"spent": "1000", "percent": "5"}},
			"topup_bonus_levels": []map[string]string{{"amount": "", "percent": ""}}, "bonus_max_percent": "80",
		})
		expect(t, r, http.StatusOK, "finances")
		s := e.clubSettings()
		lv, _ := json.Marshal(s["finances"]["loyalty_levels"])
		if string(lv) != `{"levels":[{"percent_bp":500,"threshold_tiyn":100000},{"percent_bp":1000,"threshold_tiyn":5000000}],"v":2}` {
			t.Fatalf("v2: %s", lv)
		}
		if e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND category = 'settings' AND action = 'settings.updated'", e.club) != 1 {
			t.Fatal("audit")
		}
		expectField(t, e.send("PUT", manager, "/api/settings/sections/finances", map[string]any{
			"currency": "KZT", "topup_bonus_levels": []map[string]string{{"amount": "100", "percent": "0"}},
		}), "topup_bonus_levels.0.percent", "Бонус — от 0,01 до 100%.")
		// Решение Q-002: > 2 знаков в пороге — ошибка, а не округление.
		r = e.send("PUT", manager, "/api/settings/sections/finances", map[string]any{
			"currency": "KZT", "loyalty_levels": []map[string]string{{"spent": "100.005", "percent": "5"}},
		})
		if r.status != http.StatusUnprocessableEntity || fieldError(r, "loyalty_levels.0.spent") == "" {
			t.Fatalf("Q-002: %d %v", r.status, r.body)
		}
	})

	t.Run("timezone", func(t *testing.T) {
		expectField(t, e.send("PUT", manager, "/api/settings/timezone", map[string]any{"timezone": "Mars/Base"}), "timezone",
			"The timezone field must be a valid timezone.")
		expect(t, e.send("PUT", manager, "/api/settings/timezone", map[string]any{"timezone": "Asia/Aqtobe"}), http.StatusOK, "tz")
		if d := e.activityDetails("club.timezone_changed"); d["from"] != "Asia/Almaty" || d["to"] != "Asia/Aqtobe" {
			t.Fatalf("tz log: %v", d)
		}
		if me := e.get(manager, "/api/auth/me"); obj(me.body["club"])["timezone"] != "Asia/Aqtobe" {
			t.Fatalf("me: %v", me.body["club"])
		}
	})

	t.Run("language", func(t *testing.T) {
		e.exec(`INSERT INTO languages (code, name, is_enabled, sort_order) VALUES ('ru', 'Русский', true, 0), ('en', 'English', true, 1),
			('xx', 'Off', false, 2) ON CONFLICT (code) DO NOTHING`)
		r := e.get(manager, "/api/settings/languages")
		if r.status != http.StatusOK || r.body["locale"] != "ru" {
			t.Fatalf("languages: %d %v", r.status, r.body)
		}
		expectField(t, e.send("PUT", manager, "/api/settings/language", map[string]any{"locale": "xx"}), "locale", "The selected locale is invalid.")
		expect(t, e.send("PUT", manager, "/api/settings/language", map[string]any{"locale": "en"}), http.StatusOK, "lang")
		if me := e.get(manager, "/api/auth/me"); obj(me.body["club"])["locale"] != "en" {
			t.Fatalf("me locale: %v", me.body["club"])
		}
		e.exec("UPDATE clubs SET locale = 'ru' WHERE id = $1", e.club)
	})

	t.Run("agent pin", func(t *testing.T) {
		if r := e.send("PUT", manager, "/api/settings/agent-pin", map[string]any{"pin": "1234", "pin_confirmation": "1234"}); r.status != http.StatusForbidden {
			t.Fatalf("manager pin: %d", r.status)
		}
		expectField(t, e.send("PUT", owner, "/api/settings/agent-pin", map[string]any{"pin": "12", "pin_confirmation": "12"}), "pin", "PIN-код — от 4 до 8 цифр.")
		r := e.send("PUT", owner, "/api/settings/agent-pin", map[string]any{"pin": "1234", "pin_confirmation": "4321"})
		if r.status != http.StatusUnprocessableEntity {
			t.Fatalf("confirmed: %d", r.status)
		}
		expect(t, e.send("PUT", owner, "/api/settings/agent-pin", map[string]any{"pin": "123456", "pin_confirmation": "123456"}), http.StatusOK, "pin")
		var hash string
		if err := e.db.Owner.QueryRow(context.Background(), "SELECT agent_exit_pin_hash FROM clubs WHERE id = $1", e.club).Scan(&hash); err != nil ||
			!strings.HasPrefix(hash, "$argon2id$") {
			t.Fatalf("hash: %q %v", hash, err)
		}
		if r := e.get(owner, "/api/settings"); r.body["agent_pin_set_at"] == nil {
			t.Fatal("pin set at")
		}
		expect(t, e.send("PUT", owner, "/api/settings/agent-pin", map[string]any{"pin": ""}), http.StatusOK, "pin delete")
		if e.scalar("SELECT count(*) FROM clubs WHERE id = $1 AND agent_exit_pin_hash IS NULL", e.club) != 1 ||
			e.activityCount("club.agent_pin_changed", 0) != 2 {
			t.Fatal("pin deleted")
		}
		expectField(t, e.send("PUT", owner, "/api/settings/agent-staff-time", map[string]any{"minutes": "241"}), "minutes",
			"The minutes field must not be greater than 240.")
		expect(t, e.send("PUT", owner, "/api/settings/agent-staff-time", map[string]any{"minutes": "30"}), http.StatusOK, "staff time")
		if e.scalar("SELECT agent_staff_minutes FROM clubs WHERE id = $1", e.club) != 30 {
			t.Fatal("staff minutes")
		}
	})

	t.Run("images", func(t *testing.T) {
		if r := e.upload("POST", manager, "/api/settings/logo", "logo", pngImage(t, 10, 10)); fieldError(r, "logo") == "" {
			t.Fatalf("logo dimensions: %v", r.body)
		}
		if r := e.upload("POST", manager, "/api/settings/signin-image", "image", pngImage(t, 100, 100)); fieldError(r, "image") != "Картинка — от 640×360 до 4096×4096 пикселей." {
			t.Fatalf("signin dimensions: %v", r.body)
		}
		img := pngImage(t, 64, 64)
		expect(t, e.upload("POST", manager, "/api/settings/logo", "logo", img), http.StatusOK, "logo")
		r := e.get(manager, "/api/settings")
		u, _ := r.body["logo_url"].(string)
		if !strings.HasPrefix(u, fmt.Sprintf("/club-logos/%d/", e.club)) || !strings.HasSuffix(u, ".png") {
			t.Fatalf("logo url: %q", u)
		}
		res, err := http.Get(e.srv.URL + u) //nolint:noctx // тест
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != 200 || !bytes.Equal(body, img) || res.Header.Get("Content-Type") != "image/png" ||
			res.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
			t.Fatalf("public logo: %d %v", res.StatusCode, res.Header)
		}
		if res, _ := http.Get(e.srv.URL + fmt.Sprintf("/club-logos/%d/other.png", e.club)); res.StatusCode != 404 { //nolint:noctx // тест
			t.Fatalf("other name: %d", res.StatusCode)
		}
		expect(t, e.send("DELETE", manager, "/api/settings/logo", nil), http.StatusOK, "logo delete")
		if res, _ := http.Get(e.srv.URL + u); res.StatusCode != 404 { //nolint:noctx // тест
			t.Fatalf("deleted logo: %d", res.StatusCode)
		}
		if e.activityCount("club.logo_changed", 0) != 2 {
			t.Fatal("logo log")
		}
	})
}

func TestG5Integrations(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e, g := newG5Env(t, now)
	_, owner := e.staff("Владелец", "owner")
	adminID, admin := e.staff("Админ", "admin")
	adminMember := e.scalar("SELECT id FROM club_memberships WHERE club_id = $1 AND user_id = $2", e.club, adminID)

	if r := e.get(admin, "/api/settings/integrations"); r.status != http.StatusForbidden {
		t.Fatalf("admin without access: %d", r.status)
	}
	if me := e.get(admin, "/api/auth/me"); obj(me.body["club"])["can_manage_integrations"] != false {
		t.Fatalf("me admin: %v", me.body["club"])
	}

	// Ключ API.
	r := e.post(owner, "/api/settings/integrations/keys", map[string]any{
		"name": "1С склад", "scopes": []string{"products:read", "products:read", "stock:write"}, "allowed_ips": "203.0.113.10\n198.51.100.0/24",
		"rate_limit_per_minute": "200", "expires_at": "2026-12-31",
	})
	expect(t, r, http.StatusCreated, "key")
	secret, _ := r.body["secret"].(string)
	if !strings.HasPrefix(secret, "zfk_") || len(secret) != 4+8+1+40 || r.header.Get("Cache-Control") != "no-store, private" {
		t.Fatalf("secret: %q %v", secret, r.header)
	}
	sum := sha256.Sum256([]byte(secret))
	if e.scalar("SELECT count(*) FROM api_keys WHERE club_id = $1 AND key_hash = $2 AND prefix = $3", e.club, hex.EncodeToString(sum[:]), secret[:12]) != 1 {
		t.Fatal("key hash")
	}
	expectField(t, e.post(owner, "/api/settings/integrations/keys", map[string]any{"name": "x", "scopes": []string{"x"}}), "scopes.0", "The selected scopes.0 is invalid.")
	expectField(t, e.post(owner, "/api/settings/integrations/keys", map[string]any{"name": "x", "scopes": []string{"sales:read"}, "allowed_ips": "1.2.3"}),
		"allowed_ips", "Некорректный IP-адрес или подсеть: 1.2.3")
	expectField(t, e.post(owner, "/api/settings/integrations/keys", map[string]any{"name": "x", "scopes": []string{"sales:read"}, "expires_at": "2026-10-15"}),
		"expires_at", "The expires at field must be a date after today.")

	page := e.get(owner, "/api/settings/integrations")
	keys, _ := page.body["keys"].([]any)
	if len(keys) != 1 || obj(keys[0])["status"] != "active" || obj(keys[0])["rate_limit_per_minute"] != 200.0 || page.body["min_expires_at"] != "2026-10-16" {
		t.Fatalf("page: %v", page.body)
	}
	if sc, _ := obj(keys[0])["scopes"].([]any); len(sc) != 2 {
		t.Fatalf("unique scopes: %v", sc)
	}
	keyID := int64(obj(keys[0])["id"].(float64))
	r = e.post(owner, fmt.Sprintf("/api/settings/integrations/keys/%d/rotate", keyID), nil)
	expect(t, r, http.StatusOK, "rotate")
	if e.scalar("SELECT count(*) FROM api_keys WHERE club_id = $1 AND revoked_at IS NULL AND rotated_from_id = $2", e.club, keyID) != 1 ||
		e.scalar("SELECT count(*) FROM api_keys WHERE id = $1 AND revoked_at IS NOT NULL", keyID) != 1 {
		t.Fatal("rotation")
	}
	if r := e.post(owner, fmt.Sprintf("/api/settings/integrations/keys/%d/rotate", keyID), nil); r.status != http.StatusNotFound {
		t.Fatalf("rotate revoked: %d", r.status)
	}
	if e.activityCount("api_key.created", 0) != 1 || e.activityCount("api_key.rotated", 0) != 1 ||
		e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND category = 'security'", e.club) != 2 {
		t.Fatal("key logs")
	}
	// Секрет не попадает ни в журнал, ни в аудит.
	if e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND details::text LIKE '%'||$2||'%'", e.club, secret[13:]) != 0 ||
		e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND after::text LIKE '%'||$2||'%'", e.club, secret[13:]) != 0 {
		t.Fatal("secret leaked")
	}

	// Доступ сотруднику.
	expect(t, e.send("PATCH", owner, fmt.Sprintf("/api/settings/integrations/members/%d", adminMember), map[string]any{"can_manage_integrations": true}), http.StatusOK, "grant")
	if r := e.get(admin, "/api/settings/integrations"); r.status != http.StatusOK || r.body["is_owner"] != false {
		t.Fatalf("admin with access: %d", r.status)
	}
	if me := e.get(admin, "/api/auth/me"); obj(me.body["club"])["can_manage_integrations"] != true {
		t.Fatalf("me admin: %v", me.body["club"])
	}
	if r := e.send("PATCH", admin, fmt.Sprintf("/api/settings/integrations/members/%d", adminMember), map[string]any{"can_manage_integrations": false}); r.status != http.StatusForbidden {
		t.Fatalf("admin changes access: %d", r.status)
	}
	if e.outboxCount("staff.access_changed") != 1 || e.activityCount("integrations.permission_changed", adminID) != 1 {
		t.Fatal("permission events")
	}

	// Вебхуки.
	expectField(t, e.post(admin, "/api/settings/integrations/webhooks", map[string]any{"url": "https://10.0.0.1/hook", "events": []string{"sale.completed"}}),
		"url", "Адрес вебхука указывает на внутреннюю сеть. Нужен публичный адрес.")
	r = e.post(admin, "/api/settings/integrations/webhooks", map[string]any{"url": "https://example.com/hook", "events": []string{"sale.completed", "stock.changed"}})
	expect(t, r, http.StatusCreated, "webhook")
	if s, _ := r.body["secret"].(string); !strings.HasPrefix(s, "whsec_") || len(s) != 46 {
		t.Fatalf("webhook secret: %v", r.body)
	}
	hook := e.scalar("SELECT id FROM webhook_endpoints WHERE club_id = $1", e.club)
	expect(t, e.send("PATCH", admin, fmt.Sprintf("/api/settings/integrations/webhooks/%d", hook), map[string]any{"is_active": false}), http.StatusOK, "disable")
	if d := e.activityDetails("webhook.updated"); d["is_active"] != "false" {
		t.Fatalf("webhook.updated: %v", d)
	}
	e.exec("UPDATE webhook_endpoints SET failure_count = 7 WHERE id = $1", hook)
	expect(t, e.send("PATCH", admin, fmt.Sprintf("/api/settings/integrations/webhooks/%d", hook), map[string]any{"is_active": true}), http.StatusOK, "enable")
	if e.scalar("SELECT failure_count FROM webhook_endpoints WHERE id = $1", hook) != 0 {
		t.Fatal("failure_count reset")
	}
	expect(t, e.post(admin, fmt.Sprintf("/api/settings/integrations/webhooks/%d/secret", hook), nil), http.StatusOK, "secret")

	// Доставки: keyset и повтор.
	for i := range 22 {
		e.exec(`INSERT INTO webhook_deliveries (club_id, webhook_endpoint_id, event_id, event, payload, status, attempts, created_at, updated_at)
			VALUES ($1, $2, gen_random_uuid(), 'sale.completed', '{}', $3, 1, now(), now())`, e.club, hook, map[bool]string{true: "failed", false: "delivered"}[i == 0])
	}
	page = e.get(admin, "/api/settings/integrations")
	dp := obj(page.body["deliveries"])
	items, _ := dp["items"].([]any)
	cursor, _ := dp["next_cursor"].(string)
	if len(items) != 20 || cursor == "" {
		t.Fatalf("deliveries: %d %v", len(items), dp["next_cursor"])
	}
	next := e.get(admin, "/api/settings/integrations/deliveries?cursor="+cursor)
	items, _ = next.body["items"].([]any)
	if len(items) != 2 || next.body["next_cursor"] != nil {
		t.Fatalf("page 2: %v", next.body)
	}
	failed := e.scalar("SELECT id FROM webhook_deliveries WHERE club_id = $1 AND status = 'failed'", e.club)
	delivered := e.scalar("SELECT max(id) FROM webhook_deliveries WHERE club_id = $1 AND status = 'delivered'", e.club)
	if r := e.post(admin, fmt.Sprintf("/api/settings/integrations/deliveries/%d/retry", delivered), nil); r.status != http.StatusNotFound {
		t.Fatalf("retry delivered: %d", r.status)
	}
	expect(t, e.post(admin, fmt.Sprintf("/api/settings/integrations/deliveries/%d/retry", failed), nil), http.StatusOK, "retry")
	if e.scalar("SELECT count(*) FROM webhook_deliveries WHERE id = $1 AND status = 'pending'", failed) != 1 || g.wakes != 1 {
		t.Fatal("retry state")
	}
	expect(t, e.send("DELETE", admin, fmt.Sprintf("/api/settings/integrations/webhooks/%d", hook), nil), http.StatusOK, "delete")
	if e.scalar("SELECT count(*) FROM webhook_deliveries WHERE club_id = $1", e.club) != 0 {
		t.Fatal("cascade")
	}
	expect(t, e.send("DELETE", owner, fmt.Sprintf("/api/settings/integrations/keys/%d", keyID), nil), http.StatusOK, "revoke revoked")
}
