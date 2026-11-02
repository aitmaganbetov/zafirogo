//go:build integration

package v1_test

// Контрактные тесты интеграционного API /api/v1: HTTP → роутер → Postgres (RLS, роль zafiro_app). Сценарии —
// старые Pest-тесты tests/Feature/IntegrationApiAuthTest.php и IntegrationApiResourcesTest.php
// (docs/golden-scenarios.md GS-282…GS-345, ID — в именах подтестов). Каждый ответ сверяется со схемой api/v1.yaml и
// старой resources/api/openapi-v1.yaml.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/apikey"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	httpserver "github.com/aitmaganbetov/zafirogo/server/internal/http"
)

var (
	shared     *dbtest.DB
	sharedSeq  atomic.Int64
	specNew    *openAPI
	specLegacy *openAPI
)

func TestMain(m *testing.M) {
	d, cleanup, err := dbtest.Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	shared = d
	code := m.Run()
	cleanup()
	os.Exit(code)
}

type fakePresence struct{}

func (fakePresence) List(context.Context, int64) ([]presence.State, error) { return nil, nil }

// clock — часы сервера теста (окна лимитов и сроки ключей без гонок на границе минуты).
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t     *testing.T
	srv   *httptest.Server
	clock *clock
	ip    string
	// owner — владелец клуба «mine».
	club, owner int64
}

type seeded struct {
	category, product, movement, zone, computer, tariff, sale int64
}

func newEnv(t *testing.T, limits apikey.Limits) *env {
	t.Helper()
	if specNew == nil {
		specNew, specLegacy = loadSpec(t, "api/v1.yaml", specPath), loadSpec(t, "legacy openapi-v1.yaml", legacyPath)
	}
	ck := &clock{t: time.Now().UTC().Truncate(time.Minute).Add(5 * time.Second)}
	srv := httptest.NewServer(httpserver.NewRouter(httpserver.Options{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		Pool:           shared.App, PlatformPool: shared.Platform, Presence: fakePresence{}, Now: ck.now,
		APILimits: limits, APICounter: &apikey.MemoryCounter{Now: ck.now},
	}))
	t.Cleanup(srv.Close)
	n := sharedSeq.Add(1)
	e := &env{t: t, srv: srv, clock: ck, ip: fmt.Sprintf("10.%d.%d.%d", 100+n/65536%100, n/256%256, n%256)}
	e.club, e.owner = e.newClub("mine")
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := shared.Owner.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) scalar(sql string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := shared.Owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// newClub — integrationClub(): клуб с активной подпиской (план trial) и владельцем.
func (e *env) newClub(tag string) (club, owner int64) {
	e.t.Helper()
	club = shared.Club(e.t, fmt.Sprintf("%s-%d", tag, sharedSeq.Add(1)))
	plan := e.scalar(`INSERT INTO saas_plans (slug, name, trial_days, is_active) VALUES ('trial', 'Trial', 14, true)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name RETURNING id`)
	e.exec("INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at) VALUES ($1, $2, 'active', now())", club, plan)
	owner = e.user(club, "Owner "+tag, "owner")
	return club, owner
}

func (e *env) user(club int64, name, role string) int64 {
	id := e.scalar("INSERT INTO users (name, phone, email, password, created_at, updated_at) VALUES ($1, $2, $3, 'x', now(), now()) RETURNING id",
		name, fmt.Sprintf("+7701%07d", sharedSeq.Add(1)), fmt.Sprintf("u%d@example.com", sharedSeq.Add(1)))
	e.exec("INSERT INTO club_memberships (club_id, user_id, role, created_at, updated_at) VALUES ($1, $2, $3, now(), now())", club, id, role)
	e.exec("INSERT INTO member_balances (club_id, user_id, created_at, updated_at) VALUES ($1, $2, now(), now())", club, id)
	return id
}

// seedData — seedIntegrationData(): по одной записи всего, с меткой tag в именах.
func (e *env) seedData(club, owner int64, tag string) seeded {
	var s seeded
	s.category = e.scalar("INSERT INTO product_categories (club_id, name, created_at, updated_at) VALUES ($1, $2, now(), now()) RETURNING id", club, tag+" drinks")
	s.product = e.scalar(`INSERT INTO products (club_id, sku, name, category, product_category_id, price, quantity, created_at, updated_at)
		VALUES ($1, 'SKU-SAME', $2, $3, $4, 50000, 10, now(), now()) RETURNING id`, club, tag+" cola", tag+" drinks", s.category)
	s.movement = e.scalar(`INSERT INTO stock_movements (club_id, product_id, user_id, type, quantity, balance_after, created_at, updated_at)
		VALUES ($1, $2, $3, 'receipt', 10, 10, now(), now()) RETURNING id`, club, s.product, owner)
	s.zone = e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, $2, now(), now()) RETURNING id", club, tag+" hall")
	s.computer = e.scalar(`INSERT INTO club_computers (club_id, club_zone_id, number, device_name, created_at, updated_at)
		VALUES ($1, $2, 1, $3, now(), now()) RETURNING id`, club, s.zone, tag+"-PC")
	s.tariff = e.scalar(`INSERT INTO tariffs (club_id, name, duration_minutes, price, is_active, created_at, updated_at)
		VALUES ($1, $2, 60, 100000, true, now(), now()) RETURNING id`, club, tag+" hour")
	e.exec("INSERT INTO club_zone_tariff (club_zone_id, tariff_id, club_id) VALUES ($1, $2, $3)", s.zone, s.tariff, club)
	shift := e.scalar(`INSERT INTO cash_shifts (club_id, opened_by, status, open_marker, opening_balance, opened_at, created_at, updated_at)
		VALUES ($1, $2, 'open', true, 0, now(), now(), now()) RETURNING id`, club, owner)
	receipt := fmt.Sprintf(`{"items": [{"name": %q, "quantity": 1, "price": 500, "total": 500}]}`, tag+" cola")
	s.sale = e.scalar(`INSERT INTO cash_transactions (club_id, cash_shift_id, user_id, type, amount, description, receipt_data, created_at, updated_at)
		VALUES ($1, $2, $3, 'income', 50000, 'Продажа POS', $4::jsonb, now(), now()) RETURNING id`, club, shift, owner, receipt)
	e.exec(`INSERT INTO cash_transactions (club_id, cash_shift_id, user_id, type, amount, description, created_at, updated_at)
		VALUES ($1, $2, $3, 'income', 10000, 'Cash in', now(), now())`, club, shift, owner)
	return s
}

const alnum = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func randomString(n, alphabet int) string {
	var b strings.Builder
	for range n {
		i, _ := rand.Int(rand.Reader, big.NewInt(int64(alphabet)))
		b.WriteByte(alnum[i.Int64()])
	}
	return b.String()
}

type keyOpts struct {
	scopes    []string
	allowed   []string
	rateLimit *int
	expiresAt *time.Time
}

var allScopes = []string{"products:read", "products:write", "stock:write", "sales:read", "computers:read", "bookings:write",
	"computers:control", "tariffs:read", "members:read"}

// issueKey — ApiKeyManager::create(): zfk_<8>_<40>, в БД — префикс и SHA-256. Возвращает id и ключ.
func (e *env) issueKey(club, creator int64, o keyOpts) (int64, string) {
	e.t.Helper()
	prefix := "zfk_" + randomString(8, 36)
	plain := prefix + "_" + randomString(40, len(alnum))
	if o.scopes == nil {
		o.scopes = []string{"products:read"}
	}
	scopes, _ := json.Marshal(o.scopes)
	var allowed any
	if o.allowed != nil {
		b, _ := json.Marshal(o.allowed)
		allowed = string(b)
	}
	id := e.scalar(`INSERT INTO api_keys (club_id, created_by, name, prefix, key_hash, scopes, allowed_ips, rate_limit_per_minute, expires_at, created_at, updated_at)
		VALUES ($1, $2, 'Test key', $3, $4, $5::jsonb, $6::jsonb, $7, $8, now(), now()) RETURNING id`,
		club, creator, prefix, apikey.Hash(plain), string(scopes), allowed, o.rateLimit, o.expiresAt)
	return id, plain
}

type result struct {
	status  int
	header  http.Header
	raw     []byte
	body    map[string]any
	method  string
	pattern string
}

func (r result) code() string         { s, _ := m(r.body["error"])["code"].(string); return s }
func (r result) data() map[string]any { return m(r.body["data"]) }
func (r result) list() []any          { l, _ := r.body["data"].([]any); return l }

// call — запрос с ключом token; pattern — путь спецификации операции (для сверки схемы), "" — не сверять.
func (e *env) call(method, path, pattern, token string, headers map[string]string, body any) result {
	e.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rdr = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rdr = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rdr)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Forwarded-For", e.ip)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := result{status: res.StatusCode, header: res.Header, raw: raw, method: method, pattern: pattern}
	v, err := decodeJSON(raw)
	if err != nil {
		e.t.Fatalf("%s %s: not JSON: %q", method, path, raw)
	}
	out.body = m(v)
	if pattern != "" {
		specNew.checkContract(e.t, method, pattern, res.StatusCode, raw)
		specLegacy.checkContract(e.t, method, pattern, res.StatusCode, raw)
	}
	return out
}

func (e *env) get(path, pattern, token string) result {
	return e.call("GET", path, pattern, token, nil, nil)
}

func expect(t *testing.T, r result, status int, code string) {
	t.Helper()
	if r.status != status || r.code() != code {
		t.Fatalf("%s %s: want %d %q, got %d %s", r.method, r.pattern, status, code, r.status, r.raw)
	}
}

func num(v any) int64 {
	n, _ := v.(json.Number).Int64()
	return n
}

// ---------------------------------------------------------------------------
// IntegrationApiAuthTest.php
// ---------------------------------------------------------------------------

func TestV1Auth(t *testing.T) {
	t.Run("GS-282 TestV1Auth_ValidKeyRateHeaders", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		_, key := e.issueKey(e.club, e.owner, keyOpts{})
		r := e.get("/api/v1/me", "/me", key)
		expect(t, r, 200, "")
		k := m(r.data()["key"])
		if num(m(r.data()["club"])["id"]) != e.club || fmt.Sprint(k["scopes"]) != "[products:read]" ||
			r.header.Get("X-RateLimit-Limit") != "120" || r.header.Get("X-RateLimit-Remaining") != "119" ||
			num(k["rate_limit_per_minute"]) != 120 || k["prefix"] != key[:12] {
			t.Fatalf("%s %v", r.raw, r.header)
		}
		r = e.call("GET", "/api/v1/me", "/me", "", map[string]string{"X-Api-Key": key}, nil)
		expect(t, r, 200, "")
	})

	t.Run("GS-283…GS-287 TestV1Auth_InvalidKey", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		_, key := e.issueKey(e.club, e.owner, keyOpts{})
		last := key[len(key)-1:]
		other := "a"
		if last == "a" {
			other = "b"
		}
		for name, h := range map[string]map[string]string{
			"GS-283 no header": {}, "GS-284 not a key": {"Authorization": "Bearer hello"},
			"GS-285 wrong secret":              {"Authorization": "Bearer " + key + "x"},
			"GS-286 sanctum style token":       {"Authorization": "Bearer 1|abcdefghijklmnopqrstuvwxyz"},
			"GS-287 wrong secret valid prefix": {"Authorization": "Bearer " + key[:len(key)-1] + other},
		} {
			r := e.call("GET", "/api/v1/me", "/me", "", h, nil)
			expect(t, r, 401, "invalid_api_key")
			if r.header.Get("WWW-Authenticate") != "Bearer" || m(r.body["error"])["message"] != "The API key is missing or invalid." {
				t.Fatalf("%s: %v %s", name, r.header, r.raw)
			}
		}
	})

	t.Run("GS-288 TestV1Auth_RevokedImmediately", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		id, key := e.issueKey(e.club, e.owner, keyOpts{})
		expect(t, e.get("/api/v1/me", "/me", key), 200, "")
		e.exec("UPDATE api_keys SET revoked_at = now(), revoked_by = created_by WHERE id = $1", id)
		expect(t, e.get("/api/v1/me", "/me", key), 401, "api_key_revoked")
	})

	t.Run("GS-289 TestV1Auth_Expired", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		exp := e.clock.now().Add(time.Hour)
		_, key := e.issueKey(e.club, e.owner, keyOpts{expiresAt: &exp})
		r := e.get("/api/v1/me", "/me", key)
		expect(t, r, 200, "")
		if m(r.data()["key"])["expires_at"] != exp.UTC().Format("2006-01-02T15:04:05-07:00") {
			t.Fatalf("expires_at %s", r.raw)
		}
		e.clock.add(61 * time.Minute)
		expect(t, e.get("/api/v1/me", "/me", key), 401, "api_key_expired")
	})

	t.Run("GS-291 TestV1Auth_OwnerLostAccess", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		admin := e.user(e.club, "Admin", "admin")
		e.exec("UPDATE club_memberships SET can_manage_integrations = true WHERE club_id = $1 AND user_id = $2", e.club, admin)
		_, key := e.issueKey(e.club, admin, keyOpts{})
		expect(t, e.get("/api/v1/me", "/me", key), 200, "")
		e.exec("UPDATE club_memberships SET can_manage_integrations = false WHERE club_id = $1 AND user_id = $2", e.club, admin)
		expect(t, e.get("/api/v1/me", "/me", key), 401, "api_key_owner_lost_access")
		e.exec("DELETE FROM member_balances WHERE club_id = $1 AND user_id = $2", e.club, admin)
		e.exec("DELETE FROM club_memberships WHERE club_id = $1 AND user_id = $2", e.club, admin)
		expect(t, e.get("/api/v1/me", "/me", key), 401, "api_key_owner_lost_access")
	})

	t.Run("GS-292 TestV1Auth_SubscriptionInactive", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		_, key := e.issueKey(e.club, e.owner, keyOpts{})
		expect(t, e.get("/api/v1/me", "/me", key), 200, "")
		e.exec("UPDATE club_subscriptions SET status = 'suspended' WHERE club_id = $1", e.club)
		expect(t, e.get("/api/v1/me", "/me", key), 402, "subscription_inactive")
	})

	t.Run("GS-293 TestV1Auth_IPAllowlist", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		_, key := e.issueKey(e.club, e.owner, keyOpts{allowed: []string{"10.0.0.0/24", "192.168.1.5"}})
		for ip, want := range map[string]int{"10.0.0.77": 200, "192.168.1.5": 200, "10.0.1.1": 403} {
			e.ip = ip
			r := e.get("/api/v1/me", "/me", key)
			if r.status != want || (want == 403 && r.code() != "ip_not_allowed") {
				t.Fatalf("%s: %d %s", ip, r.status, r.raw)
			}
		}
	})

	t.Run("GS-294…GS-296 TestV1Auth_TenantOverride", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		victim, _ := e.newClub("victim")
		s := e.seedData(e.club, e.owner, "Mine")
		_, key := e.issueKey(e.club, e.owner, keyOpts{scopes: allScopes})
		for _, club := range []int64{victim, e.club} {
			want, code := 403, "tenant_override_forbidden"
			if club == e.club {
				want, code = 200, ""
			}
			cs := fmt.Sprint(club)
			expect(t, e.call("GET", "/api/v1/me", "/me", key, map[string]string{"X-Club-Id": cs}, nil), want, code)
			expect(t, e.get("/api/v1/me?club_id="+cs, "/me", key), want, code)
			expect(t, e.call("PUT", fmt.Sprintf("/api/v1/computers/%d/reservation", s.computer), "/computers/{computer}/reservation", key, nil,
				map[string]any{"reserved": false, "club_id": club}), want, code)
		}
	})

	t.Run("GS-297 TestV1Auth_ScopesRequired", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		_, key := e.issueKey(e.club, e.owner, keyOpts{scopes: []string{"products:read"}})
		r := e.call("POST", "/api/v1/products", "/products", key, nil, map[string]any{})
		expect(t, r, 403, "insufficient_scope")
		if fmt.Sprint(m(r.body["error"])["required_scopes"]) != "[products:write]" {
			t.Fatal(string(r.raw))
		}
		_, full := e.issueKey(e.club, e.owner, keyOpts{scopes: []string{"products:read", "products:write"}})
		expect(t, e.call("POST", "/api/v1/products", "/products", full, nil, map[string]any{}), 422, "validation_failed")
	})

	t.Run("GS-299 TestV1Auth_FailedAuthIPBlock", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{FailedAuthPerMinute: 5})
		_, key := e.issueKey(e.club, e.owner, keyOpts{})
		for range 5 {
			expect(t, e.get("/api/v1/me", "/me", "zfk_aaaaaaaa_"+strings.Repeat("b", 40)), 401, "invalid_api_key")
		}
		r := e.get("/api/v1/me", "/me", key)
		expect(t, r, 429, "too_many_failed_attempts")
		if r.header.Get("Retry-After") == "" {
			t.Fatal("Retry-After")
		}
		e.ip = "10.9.9.9"
		expect(t, e.get("/api/v1/me", "/me", key), 200, "")
	})

	t.Run("GS-300 TestV1Auth_RevokedKeysDoNotBlockIP", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{FailedAuthPerMinute: 3})
		id, revoked := e.issueKey(e.club, e.owner, keyOpts{})
		_, key := e.issueKey(e.club, e.owner, keyOpts{})
		e.exec("UPDATE api_keys SET revoked_at = now() WHERE id = $1", id)
		for range 6 {
			expect(t, e.get("/api/v1/me", "/me", revoked), 401, "api_key_revoked")
		}
		expect(t, e.get("/api/v1/me", "/me", key), 200, "")
	})

	t.Run("GS-301 TestV1RateLimit_PerKey", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		three := 3
		_, slow := e.issueKey(e.club, e.owner, keyOpts{rateLimit: &three})
		_, other := e.issueKey(e.club, e.owner, keyOpts{})
		for range 3 {
			expect(t, e.get("/api/v1/me", "/me", slow), 200, "")
		}
		r := e.get("/api/v1/me", "/me", slow)
		expect(t, r, 429, "rate_limited")
		if r.header.Get("Retry-After") != "55" || r.header.Get("X-RateLimit-Remaining") != "0" {
			t.Fatalf("%v", r.header)
		}
		expect(t, e.get("/api/v1/me", "/me", other), 200, "")
		e.clock.add(time.Minute) // новое окно
		expect(t, e.get("/api/v1/me", "/me", slow), 200, "")
	})

	t.Run("GS-302 TestV1RateLimit_PerClub", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{Club: 4})
		other, otherOwner := e.newClub("second")
		_, first := e.issueKey(e.club, e.owner, keyOpts{})
		_, second := e.issueKey(e.club, e.owner, keyOpts{})
		_, foreign := e.issueKey(other, otherOwner, keyOpts{})
		for range 2 {
			expect(t, e.get("/api/v1/me", "/me", first), 200, "")
			expect(t, e.get("/api/v1/me", "/me", second), 200, "")
		}
		expect(t, e.get("/api/v1/me", "/me", second), 429, "rate_limited")
		expect(t, e.get("/api/v1/me", "/me", foreign), 200, "")
	})

	t.Run("GS-303 TestV1_PayloadTooLarge", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		_, key := e.issueKey(e.club, e.owner, keyOpts{scopes: allScopes})
		big := map[string]any{"blob": strings.Repeat("x", 300*1024)}
		expect(t, e.call("POST", "/api/v1/products", "/products", key, nil, big), 413, "payload_too_large")
		expect(t, e.call("POST", "/api/v1/products", "/products", key, nil, map[string]any{"blob": "small"}), 422, "validation_failed")
	})

	t.Run("GS-304…GS-306 TestV1Idempotency", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		s := e.seedData(e.club, e.owner, "Mine")
		keyID, key := e.issueKey(e.club, e.owner, keyOpts{scopes: allScopes})
		_, key2 := e.issueKey(e.club, e.owner, keyOpts{scopes: allScopes})
		body := map[string]any{"product_id": s.product, "type": "receipt", "quantity": 1}
		const pat = "/stock-movements"
		expect(t, e.call("POST", "/api/v1/stock-movements", pat, key, nil, body), 428, "idempotency_key_required")
		h := map[string]string{"Idempotency-Key": "order-42"}
		first := e.call("POST", "/api/v1/stock-movements", pat, key, h, body)
		expect(t, first, 201, "")
		replay := e.call("POST", "/api/v1/stock-movements", pat, key, h, body)
		expect(t, replay, 201, "")
		if !bytes.Equal(first.raw, replay.raw) || replay.header.Get("Idempotent-Replayed") != "true" {
			t.Fatalf("replay %s %v", replay.raw, replay.header)
		}
		expect(t, e.call("POST", "/api/v1/stock-movements", pat, key, h, map[string]any{"product_id": s.product, "type": "receipt", "quantity": 999}),
			422, "idempotency_key_reused")
		expect(t, e.call("POST", "/api/v1/stock-movements", pat, key, map[string]string{"Idempotency-Key": "bad key!"}, body), 422, "idempotency_key_invalid")
		// GS-305: тот же ключ идемпотентности другого API-ключа — отдельная операция.
		expect(t, e.call("POST", "/api/v1/stock-movements", pat, key2, h, body), 201, "")
		if q := e.scalar("SELECT quantity FROM products WHERE id = $1", s.product); q != 12 {
			t.Fatalf("quantity %d, want 12 (two receipts)", q)
		}
		// GS-306: ключ ещё выполняется (строка без ответа) — 409, операция не выполняется.
		raw, _ := json.Marshal(body)
		e.exec(`INSERT INTO api_idempotency_keys (club_id, api_key_id, idempotency_key, request_hash, created_at)
			VALUES ($1, $2, 'busy', encode(sha256(convert_to($3, 'UTF8')), 'hex'), now())`, e.club, keyID, "POST api/v1/stock-movements\n"+string(raw))
		r := e.call("POST", "/api/v1/stock-movements", pat, key, map[string]string{"Idempotency-Key": "busy"}, raw)
		expect(t, r, 409, "idempotency_in_progress")
		if q := e.scalar("SELECT quantity FROM products WHERE id = $1", s.product); q != 12 {
			t.Fatalf("quantity %d after busy key", q)
		}
		// Срок хранения (24 ч) вышел — ключ снова свободен (integrations:prune).
		e.clock.add(25 * time.Hour)
		expect(t, e.call("POST", "/api/v1/stock-movements", pat, key, h, body), 201, "")
	})

	t.Run("GS-307 TestV1Audit_NoSecretsOrQuery", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		id, key := e.issueKey(e.club, e.owner, keyOpts{})
		expect(t, e.get("/api/v1/me?token=secret-value", "/me", key), 200, "")
		expect(t, e.get("/api/v1/me", "/me", key[:len(key)-2]+"zz"), 401, "invalid_api_key")
		rows, err := shared.Owner.Query(context.Background(), `SELECT coalesce(club_id, 0), coalesce(api_key_id, 0), coalesce(key_prefix, ''),
			method, endpoint, status, ip FROM api_request_logs WHERE key_prefix = $1 ORDER BY id`, key[:12])
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var club, kid int64
			var prefix, method, endpoint, ip string
			var status int
			if err := rows.Scan(&club, &kid, &prefix, &method, &endpoint, &status, &ip); err != nil {
				t.Fatal(err)
			}
			got = append(got, fmt.Sprintf("%d %d %s %s %s %d %s", club, kid, prefix, method, endpoint, status, ip))
		}
		want := []string{
			fmt.Sprintf("%d %d %s GET /api/v1/me 200 %s", e.club, id, key[:12], e.ip),
			fmt.Sprintf("0 0 %s GET /api/v1/me 401 %s", key[:12], e.ip),
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("logs\n%v\nwant\n%v", got, want)
		}
		if n := e.scalar("SELECT count(*) FROM api_request_logs WHERE endpoint LIKE '%secret%'"); n != 0 {
			t.Fatal("query in log")
		}
		// last_used_at обновлён (не чаще раза в минуту), в api_keys — только хэш (GS-308).
		if n := e.scalar("SELECT count(*) FROM api_keys WHERE id = $1 AND last_used_at IS NOT NULL AND key_hash = $2 AND last_used_ip = $3",
			id, apikey.Hash(key), e.ip); n != 1 {
			t.Fatal("last_used_at / key_hash")
		}
	})

	t.Run("GS-311 TestV1_NotFoundEnvelope", func(t *testing.T) {
		e := newEnv(t, apikey.Limits{})
		r := e.get("/api/v1/nothing-here", "", "")
		expect(t, r, 404, "not_found")
		expect(t, e.call("DELETE", "/api/v1/me", "", "", nil, nil), 405, "method_not_allowed")
	})
}

// ---------------------------------------------------------------------------
// IntegrationApiResourcesTest.php
// ---------------------------------------------------------------------------

// twoClubs — twoClubsWithData(): клубы mine и theirs с данными и ключ клуба mine со всеми областями.
func twoClubs(t *testing.T) (*env, seeded, seeded, string) {
	e := newEnv(t, apikey.Limits{})
	theirs, theirOwner := e.newClub("theirs")
	mine := e.seedData(e.club, e.owner, "Mine")
	their := e.seedData(theirs, theirOwner, "Theirs")
	_, key := e.issueKey(e.club, e.owner, keyOpts{scopes: allScopes})
	return e, mine, their, key
}

func TestV1Resources(t *testing.T) {
	e, mine, theirs, key := twoClubs(t)

	t.Run("GS-313…GS-320 TestV1List_OnlyOwnClub", func(t *testing.T) {
		for _, p := range []string{"/products", "/product-categories", "/stock-movements", "/sales", "/zones", "/computers", "/tariffs", "/members"} {
			r := e.get("/api/v1"+p, p, key)
			expect(t, r, 200, "")
			if len(r.list()) == 0 || bytes.Contains(r.raw, []byte("Theirs")) {
				t.Fatalf("%s: %s", p, r.raw)
			}
		}
	})

	t.Run("GS-321 TestV1List_OwnRecords", func(t *testing.T) {
		r := e.get("/api/v1/products", "/products", key)
		if len(r.list()) != 1 || num(m(r.list()[0])["id"]) != mine.product || m(r.list()[0])["price"] != "500.00" {
			t.Fatal(string(r.raw))
		}
		r = e.get("/api/v1/sales", "/sales", key)
		item := m(r.list()[0])
		if len(r.list()) != 1 || num(item["id"]) != mine.sale || item["amount"] != "500.00" ||
			fmt.Sprint(item["items"]) != "[map[name:Mine cola price:500.00 quantity:1 total:500.00]]" {
			t.Fatal(string(r.raw))
		}
		r = e.get("/api/v1/tariffs", "/tariffs", key)
		if fmt.Sprint(m(r.list()[0])["zone_ids"]) != fmt.Sprintf("[%d]", mine.zone) || m(r.list()[0])["price"] != "1000.00" {
			t.Fatal(string(r.raw))
		}
		r = e.get("/api/v1/zones", "/zones", key)
		if num(m(r.list()[0])["computers_count"]) != 1 {
			t.Fatal(string(r.raw))
		}
		r = e.get(fmt.Sprintf("/api/v1/sales/%d", mine.sale), "/sales/{sale}", key)
		expect(t, r, 200, "")
		r = e.get(fmt.Sprintf("/api/v1/products/%d", mine.product), "/products/{product}", key)
		expect(t, r, 200, "")
		r = e.get(fmt.Sprintf("/api/v1/computers/%d", mine.computer), "/computers/{computer}", key)
		if r.data()["name"] != "Mine-PC" || r.data()["status"] != "offline" || r.data()["connection"] != "offline" {
			t.Fatal(string(r.raw))
		}
		r = e.get("/api/v1/members", "/members", key)
		mem := m(r.list()[0])
		if mem["balance"] != "0.00" || mem["bonus_balance"] != "0.00" || mem["role"] != "owner" {
			t.Fatal(string(r.raw))
		}
	})

	t.Run("GS-322 TestV1_ForeignRecordsByIdNotFound", func(t *testing.T) {
		idem := map[string]string{"Idempotency-Key": "k1"}
		expect(t, e.get(fmt.Sprintf("/api/v1/products/%d", theirs.product), "/products/{product}", key), 404, "not_found")
		expect(t, e.call("PATCH", fmt.Sprintf("/api/v1/products/%d", theirs.product), "/products/{product}", key, nil, map[string]any{"price": 1}), 404, "not_found")
		expect(t, e.get(fmt.Sprintf("/api/v1/sales/%d", theirs.sale), "/sales/{sale}", key), 404, "not_found")
		expect(t, e.get(fmt.Sprintf("/api/v1/computers/%d", theirs.computer), "/computers/{computer}", key), 404, "not_found")
		expect(t, e.call("PUT", fmt.Sprintf("/api/v1/computers/%d/reservation", theirs.computer), "/computers/{computer}/reservation", key, nil,
			map[string]any{"reserved": true}), 404, "not_found")
		expect(t, e.call("POST", "/api/v1/computers/control", "/computers/control", key, idem,
			map[string]any{"computer_ids": []int64{theirs.computer}, "action": "power_off"}), 422, "validation_failed")
		expect(t, e.call("POST", "/api/v1/stock-movements", "/stock-movements", key, map[string]string{"Idempotency-Key": "k2"},
			map[string]any{"product_id": theirs.product, "type": "write_off", "quantity": 1}), 422, "validation_failed")
		r := e.call("POST", "/api/v1/products", "/products", key, nil,
			map[string]any{"sku": "NEW", "name": "X", "product_category_id": theirs.category, "price": 1, "quantity": 0})
		expect(t, r, 422, "validation_failed")
		if _, ok := m(m(r.body["error"])["fields"])["product_category_id"]; !ok {
			t.Fatal(string(r.raw))
		}
		if n := len(e.get(fmt.Sprintf("/api/v1/stock-movements?product_id=%d", theirs.product), "/stock-movements", key).list()); n != 0 {
			t.Fatal("foreign movements")
		}
		if n := len(e.get(fmt.Sprintf("/api/v1/computers?zone_id=%d", theirs.zone), "/computers", key).list()); n != 0 {
			t.Fatal("foreign computers")
		}
		if e.scalar("SELECT price FROM products WHERE id = $1", theirs.product) != 50000 || e.scalar("SELECT quantity FROM products WHERE id = $1", theirs.product) != 10 ||
			e.scalar("SELECT count(*) FROM club_computers WHERE id = $1 AND (is_reserved OR status = 'offline')", theirs.computer) != 0 {
			t.Fatal("foreign records changed")
		}
	})

	t.Run("GS-323…GS-338 TestV1Scope", func(t *testing.T) {
		for _, c := range []struct{ method, path, pattern, scope string }{
			{"GET", "/products", "/products", "products:read"},
			{"GET", fmt.Sprintf("/products/%d", mine.product), "/products/{product}", "products:read"},
			{"GET", "/product-categories", "/product-categories", "products:read"},
			{"GET", "/stock-movements", "/stock-movements", "products:read"},
			{"POST", "/products", "/products", "products:write"},
			{"PATCH", fmt.Sprintf("/products/%d", mine.product), "/products/{product}", "products:write"},
			{"POST", "/stock-movements", "/stock-movements", "stock:write"},
			{"GET", "/sales", "/sales", "sales:read"},
			{"GET", fmt.Sprintf("/sales/%d", mine.sale), "/sales/{sale}", "sales:read"},
			{"GET", "/zones", "/zones", "computers:read"},
			{"GET", "/computers", "/computers", "computers:read"},
			{"GET", fmt.Sprintf("/computers/%d", mine.computer), "/computers/{computer}", "computers:read"},
			{"PUT", fmt.Sprintf("/computers/%d/reservation", mine.computer), "/computers/{computer}/reservation", "bookings:write"},
			{"POST", "/computers/control", "/computers/control", "computers:control"},
			{"GET", "/tariffs", "/tariffs", "tariffs:read"},
			{"GET", "/members", "/members", "members:read"},
		} {
			var scopes []string
			for _, s := range allScopes {
				if s != c.scope {
					scopes = append(scopes, s)
				}
			}
			_, k := e.issueKey(e.club, e.owner, keyOpts{scopes: scopes})
			var body any
			if c.method != "GET" {
				body = map[string]any{}
			}
			r := e.call(c.method, "/api/v1"+c.path, c.pattern, k, map[string]string{"Idempotency-Key": "x"}, body)
			expect(t, r, 403, "insufficient_scope")
			if fmt.Sprint(m(r.body["error"])["required_scopes"]) != "["+c.scope+"]" {
				t.Fatalf("%s %s: %s", c.method, c.path, r.raw)
			}
		}
	})

	t.Run("GS-339 TestV1Products_CreateUpdateBusinessRules", func(t *testing.T) {
		r := e.call("POST", "/api/v1/products", "/products", key, nil,
			map[string]any{"sku": "API-1", "name": "Api tea", "product_category_id": mine.category, "price": "250.50", "quantity": 4})
		expect(t, r, 201, "")
		id := num(r.data()["id"])
		if num(r.data()["quantity"]) != 4 || r.data()["category"] != "Mine drinks" || r.data()["price"] != "250.50" {
			t.Fatal(string(r.raw))
		}
		r = e.call("POST", "/api/v1/products", "/products", key, nil,
			map[string]any{"sku": "API-1", "name": "Dup", "product_category_id": mine.category, "price": 1, "quantity": 0})
		expect(t, r, 422, "validation_failed")
		if _, ok := m(m(r.body["error"])["fields"])["sku"]; !ok {
			t.Fatal(string(r.raw))
		}
		r = e.call("PATCH", fmt.Sprintf("/api/v1/products/%d", id), "/products/{product}", key, nil, map[string]any{"price": 300, "name": "Api green tea"})
		expect(t, r, 200, "")
		if r.data()["price"] != "300.00" || r.data()["name"] != "Api green tea" {
			t.Fatal(string(r.raw))
		}
		r = e.call("PATCH", fmt.Sprintf("/api/v1/products/%d", id), "/products/{product}", key, nil, map[string]any{"quantity": 999})
		expect(t, r, 422, "validation_failed")
		if fmt.Sprint(m(m(r.body["error"])["fields"])["quantity"]) != "[Use POST /api/v1/stock-movements to change stock.]" {
			t.Fatal(string(r.raw))
		}
		r = e.call("PATCH", fmt.Sprintf("/api/v1/products/%d", id), "/products/{product}", key, nil, map[string]any{"price": "1.005"})
		expect(t, r, 422, "validation_failed") // решение Q-002: > 2 знаков после точки
		if e.scalar("SELECT club_id FROM products WHERE id = $1", id) != e.club || e.scalar("SELECT quantity FROM products WHERE id = $1", id) != 4 {
			t.Fatal("product")
		}
		if n := e.scalar(`SELECT count(*) FROM activity_logs WHERE club_id = $1 AND subject_id = $2
			AND event IN ('product.created', 'product.updated', 'stock.received') AND details->>'api_key' = $3 AND ip_address = $4`,
			e.club, id, key[:12], e.ip); n != 3 {
			t.Fatalf("activity with api_key: %d", n)
		}
		if n := e.scalar(`SELECT count(*) FROM outbox_events WHERE club_id = $1 AND type IN ('product.created', 'product.updated', 'stock.changed')
			AND (payload->>'product_id')::bigint = $2`, e.club, id); n != 3 {
			t.Fatalf("webhook events: %d", n)
		}
	})

	t.Run("GS-340 TestV1Products_SameSkuOtherClub", func(t *testing.T) {
		r := e.call("POST", "/api/v1/products", "/products", key, nil,
			map[string]any{"sku": "SKU-SAME-2", "name": "A", "product_category_id": mine.category, "price": 1, "quantity": 0})
		expect(t, r, 201, "")
		if n := e.scalar("SELECT count(*) FROM products WHERE sku = 'SKU-SAME'"); n < 2 {
			t.Fatal(n)
		}
	})

	t.Run("GS-341 TestV1StockMovement_IdempotentAndRules", func(t *testing.T) {
		body := map[string]any{"product_id": mine.product, "type": "write_off", "quantity": 3}
		expect(t, e.call("POST", "/api/v1/stock-movements", "/stock-movements", key, nil, body), 428, "idempotency_key_required")
		first := e.call("POST", "/api/v1/stock-movements", "/stock-movements", key, map[string]string{"Idempotency-Key": "wo-1"}, body)
		expect(t, first, 201, "")
		replay := e.call("POST", "/api/v1/stock-movements", "/stock-movements", key, map[string]string{"Idempotency-Key": "wo-1"}, body)
		expect(t, replay, 201, "")
		r := e.call("POST", "/api/v1/stock-movements", "/stock-movements", key, map[string]string{"Idempotency-Key": "wo-2"},
			map[string]any{"product_id": mine.product, "type": "write_off", "quantity": 100})
		expect(t, r, 422, "validation_failed")
		if _, ok := m(m(r.body["error"])["fields"])["quantity"]; !ok {
			t.Fatal(string(r.raw))
		}
		if !bytes.Equal(first.raw, replay.raw) || num(first.data()["balance_after"]) != 7 ||
			e.scalar("SELECT quantity FROM products WHERE id = $1", mine.product) != 7 {
			t.Fatalf("%s %s", first.raw, replay.raw)
		}
	})

	t.Run("GS-342 TestV1Computers_ReservationIdempotent", func(t *testing.T) {
		path := fmt.Sprintf("/api/v1/computers/%d/reservation", mine.computer)
		const pat = "/computers/{computer}/reservation"
		before := e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'computer.controlled'", e.club)
		r := e.call("PUT", path, pat, key, nil, map[string]any{"reserved": true})
		if r.data()["is_reserved"] != true || r.data()["status"] != "reserved" {
			t.Fatal(string(r.raw))
		}
		expect(t, e.call("PUT", path, pat, key, nil, map[string]any{"reserved": true}), 200, "")
		r = e.call("PUT", path, pat, key, nil, map[string]any{"reserved": false})
		if r.data()["is_reserved"] != false {
			t.Fatal(string(r.raw))
		}
		if n := e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'computer.controlled'", e.club) - before; n != 2 {
			t.Fatalf("computer.controlled: %d", n)
		}
		if n := e.scalar(`SELECT count(*) FROM outbox_events WHERE club_id = $1 AND type = 'computer.reservation_changed'
			AND payload->'computer_ids' = to_jsonb(ARRAY[$2::bigint])`, e.club, mine.computer); n != 2 {
			t.Fatalf("webhook computer.reservation_changed: %d", n)
		}
	})

	t.Run("GS-343 TestV1Control_RefuseInSessionBatchLimit", func(t *testing.T) {
		e.exec("UPDATE club_computers SET current_session_id = 'session-1', status = 'online' WHERE id = $1", mine.computer)
		body := map[string]any{"computer_ids": []int64{mine.computer}, "action": "power_off"}
		const pat = "/computers/control"
		expect(t, e.call("POST", "/api/v1/computers/control", pat, key, nil, body), 428, "idempotency_key_required")
		r := e.call("POST", "/api/v1/computers/control", pat, key, map[string]string{"Idempotency-Key": "c1"}, body)
		expect(t, r, 409, "conflict")
		if fmt.Sprint(m(r.body["error"])["computer_ids"]) != fmt.Sprintf("[%d]", mine.computer) {
			t.Fatal(string(r.raw))
		}
		if e.scalar("SELECT count(*) FROM club_computers WHERE id = $1 AND status = 'online'", mine.computer) != 1 {
			t.Fatal("status changed")
		}
		ids := make([]int64, 51)
		for i := range ids {
			ids[i] = int64(i + 1)
		}
		expect(t, e.call("POST", "/api/v1/computers/control", pat, key, map[string]string{"Idempotency-Key": "c2"},
			map[string]any{"computer_ids": ids, "action": "restart"}), 422, "validation_failed")
		e.exec("UPDATE club_computers SET current_session_id = NULL WHERE id = $1", mine.computer)
		r = e.call("POST", "/api/v1/computers/control", pat, key, map[string]string{"Idempotency-Key": "c3"}, body)
		expect(t, r, 200, "")
		if num(r.data()["updated"]) != 1 || r.data()["action"] != "power_off" ||
			e.scalar("SELECT count(*) FROM club_computers WHERE id = $1 AND status = 'offline'", mine.computer) != 1 {
			t.Fatal(string(r.raw))
		}
	})

	t.Run("GS-344 TestV1Members_MaskedPhonesNoEmail", func(t *testing.T) {
		r := e.get("/api/v1/members", "/members", key)
		mem := m(r.list()[0])
		masked, _ := mem["phone_masked"].(string)
		if len(masked) != len("+7 *** *** 45 67") || !strings.HasPrefix(masked, "+7 *** *** ") || bytes.Contains(r.raw, []byte("+7701")) ||
			bytes.Contains(r.raw, []byte("@example.com")) {
			t.Fatal(string(r.raw))
		}
	})

	t.Run("GS-345 TestV1_CursorPagination", func(t *testing.T) {
		e2, s, _, k := twoClubs(t)
		for i := 1; i <= 4; i++ {
			e2.exec(`INSERT INTO products (club_id, sku, name, category, product_category_id, price, quantity, created_at, updated_at)
				VALUES ($1, $2, $3, 'x', $4, 100, 1, now(), now())`, e2.club, fmt.Sprintf("P-%d", i), fmt.Sprintf("Page %d", i), s.category)
		}
		var sizes []int
		cursor := ""
		for {
			r := e2.get("/api/v1/products?limit=2"+cursor, "/products", k)
			sizes = append(sizes, len(r.list()))
			next, _ := m(r.body["meta"])["next_cursor"].(string)
			if num(m(r.body["meta"])["per_page"]) != 2 {
				t.Fatal(string(r.raw))
			}
			if next == "" {
				break
			}
			cursor = "&cursor=" + next
		}
		if fmt.Sprint(sizes) != "[2 2 1]" {
			t.Fatal(sizes)
		}
		r := e2.get("/api/v1/products?limit=500", "/products", k)
		expect(t, r, 422, "validation_failed")
		if fmt.Sprint(m(m(r.body["error"])["fields"])["limit"]) != "[The limit field must not be greater than 100.]" {
			t.Fatal(string(r.raw))
		}
	})
}
