package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/apikey"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// keyInfo — проверенные метаданные ключа (ApiKeyManager::load(), без секрета).
type keyInfo struct {
	ID        int64
	ClubID    int64
	CreatedBy int64
	Prefix    string
	Scopes    []string
	Allowed   []string
	RateLimit *int32
	ExpiresAt *time.Time
	ClubName  string
	ClubSlug  string
}

// call — один запрос к v1.
type call struct {
	ctx   context.Context
	r     *http.Request
	rt    route
	body  []byte
	ip    string
	key   *keyInfo
	token string
	// input — $request->all(): query + тело (тело важнее), после TrimStrings и ConvertEmptyStringsToNull.
	input map[string]any
}

func (c *call) clubID() int64 { return c.key.ClubID }

func (c *call) param(name string) string { return chi.URLParam(c.r, name) }

// chain — обработка маршрута rt (порядок — в комментарии пакета).
func (h *handlers) chain(rt route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &call{ctx: r.Context(), r: r, rt: rt, ip: httpx.HostOnly(r.RemoteAddr)}
		h.logRequest(c, func() response {
			return h.limitSize(c, func() response {
				return h.authenticate(c, func() response {
					return h.rateLimit(c, func() response {
						return h.requireScope(c, func() response {
							return h.idempotent(c, func() response { return h.run(c) })
						})
					})
				})
			})
		}).write(w)
	})
}

// run — обработчик маршрута.
func (h *handlers) run(c *call) response { return c.rt.fn(h, c) }

// fail — ошибка обработчика → конверт: *apiError как есть, ошибки форм/домена (httpx.MapError) — 422/404,
// остальное — 500 server_error без подробностей.
func (h *handlers) fail(c *call, err error) response {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.response()
	}
	if he := httpx.MapError(err); he != nil {
		he = h.d.Localized(c.ctx, he)
		if fields, ok := validationFields(he); ok {
			e := newError("validation_failed", http.StatusUnprocessableEntity, "")
			e.env.Fields = fields
			return e.response()
		}
		switch he.Status {
		case http.StatusNotFound:
			return errorResponse("not_found", http.StatusNotFound)
		case http.StatusForbidden:
			return errorResponse("forbidden", http.StatusForbidden)
		case http.StatusConflict:
			return errorResponse("conflict", http.StatusConflict)
		case http.StatusBadRequest:
			return errorResponse("bad_request", http.StatusBadRequest)
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return errorResponse("not_found", http.StatusNotFound)
	}
	h.d.Log().ErrorContext(c.ctx, "api v1: handler error", slog.String("route", c.rt.name), slog.String("err", err.Error()))
	return errorResponse("server_error", http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// api.log — LogApiRequest
// ---------------------------------------------------------------------------

// logRequest — журнал каждого вызова, включая отклонённые: время, IP, префикс ключа, метод, путь, статус,
// длительность. Query, заголовки и тело не пишутся. Ошибка записи не портит ответ.
func (h *handlers) logRequest(c *call, next func() response) response {
	started := time.Now()
	res := next()
	p := store.ApiV1InsertRequestLogParams{
		Method:     truncate(c.r.Method, 8),
		Endpoint:   truncate(pathOf(c.r), 191),
		Status:     int32(res.status), // #nosec G115 -- статус HTTP
		DurationMs: int64(math.Round(float64(time.Since(started).Microseconds()) / 1000)),
		Ip:         truncate(c.ip, 45),
		CreatedAt:  h.d.NowFunc(),
	}
	if c.key != nil {
		p.ClubID, p.ApiKeyID, p.KeyPrefix = &c.key.ClubID, &c.key.ID, &c.key.Prefix
	} else if prefix := apikey.KeyPrefix(c.token); prefix != "" {
		p.KeyPrefix = &prefix
	}
	if err := h.writeLog(context.WithoutCancel(c.ctx), p); err != nil {
		h.d.Log().ErrorContext(c.ctx, "API request log write failed", slog.String("err", err.Error()))
	}
	return res
}

func (h *handlers) writeLog(ctx context.Context, p store.ApiV1InsertRequestLogParams) error {
	if h.d.Pool == nil {
		return nil
	}
	if p.ClubID != nil {
		return db.InTenantTx(ctx, h.d.Pool, *p.ClubID, func(q *store.Queries) error { return q.ApiV1InsertRequestLog(ctx, p) })
	}
	// Без клуба: политика tenant_isolation пропускает club_id IS NULL (0004, 0022).
	return pgx.BeginTxFunc(ctx, h.d.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return store.New(tx).ApiV1InsertRequestLog(ctx, p)
	})
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ---------------------------------------------------------------------------
// api.size — LimitApiRequestSize
// ---------------------------------------------------------------------------

func (h *handlers) limitSize(c *call, next func() response) response {
	maxBytes := h.d.APILimits.MaxRequestBytes
	if c.r.ContentLength > maxBytes {
		return errorResponse("payload_too_large", http.StatusRequestEntityTooLarge)
	}
	if c.r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(c.r.Body, maxBytes+1))
		if err != nil {
			return errorResponse("bad_request", http.StatusBadRequest)
		}
		if int64(len(b)) > maxBytes {
			return errorResponse("payload_too_large", http.StatusRequestEntityTooLarge)
		}
		c.body = b
	}
	return next()
}

// ---------------------------------------------------------------------------
// api.key — AuthenticateApiKey
// ---------------------------------------------------------------------------

// presentedToken — $request->bearerToken() ?? X-Api-Key; пусто или длиннее 128 — нет ключа.
func presentedToken(r *http.Request) string {
	tok := ""
	if a := r.Header.Get("Authorization"); a != "" {
		if i := strings.Index(a, "Bearer "); i >= 0 {
			tok = strings.TrimSpace(a[i+len("Bearer "):])
			if j := strings.IndexByte(tok, ','); j >= 0 {
				tok = tok[:j]
			}
		}
	}
	if tok == "" {
		tok = r.Header.Get("X-Api-Key")
	}
	if tok == "" || len(tok) > 128 {
		return ""
	}
	return tok
}

const failedAuthWindow = 60 * time.Second

func (h *handlers) authenticate(c *call, next func() response) response {
	ctx := c.ctx
	counter := h.d.APICounter
	failKey := "auth-failed:" + c.ip
	n, err := counter.Get(ctx, failKey)
	if err != nil {
		return h.fail(c, fmt.Errorf("failed auth counter: %w", err))
	}
	if n >= int64(h.d.APILimits.FailedAuthPerMinute) {
		ttl, _ := counter.TTL(ctx, failKey)
		res := errorResponse("too_many_failed_attempts", http.StatusTooManyRequests)
		res.setHeader("Retry-After", strconv.FormatInt(max(int64(math.Ceil(ttl.Seconds())), 0), 10))
		return res
	}
	c.token = presentedToken(c.r)
	key, reason, err := h.verify(ctx, c.token)
	if err != nil {
		return h.fail(c, err)
	}
	if reason != "" {
		// Только подбор считается в блокировку IP: отозванный или истёкший ключ — настоящий ключ.
		if reason == "invalid_api_key" {
			if _, err := counter.Incr(ctx, failKey, failedAuthWindow); err != nil {
				h.d.Log().WarnContext(ctx, "api v1: failed auth counter", slog.String("err", err.Error()))
			}
		}
		status := http.StatusUnauthorized
		if reason == "subscription_inactive" {
			status = http.StatusPaymentRequired
		}
		res := errorResponse(reason, status)
		res.setHeader("WWW-Authenticate", "Bearer")
		return res
	}
	c.key = key
	if len(key.Allowed) > 0 && !ipAllowed(c.ip, key.Allowed) {
		return errorResponse("ip_not_allowed", http.StatusForbidden)
	}
	c.parseInput()
	if c.overridesTenant() {
		return errorResponse("tenant_override_forbidden", http.StatusForbidden)
	}
	// Клуб ключа — клуб запроса: Principal создателя ключа (actorId()) и префикс ключа в журнале действий.
	c.ctx = principal.WithPrincipal(c.ctx, principal.Principal{UserID: key.CreatedBy, ClubID: key.ClubID})
	meta, _ := activity.RequestMetaFromContext(c.ctx)
	if meta.IP == "" {
		meta.IP = c.ip
	}
	meta.APIKeyPrefix = key.Prefix
	c.ctx = activity.WithRequestMeta(c.ctx, meta)
	h.touch(c)
	return next()
}

// verify — ApiKeyManager::verify(): причина отказа или метаданные ключа. Поиск — по префиксу (политика
// api_key_lookup), сравнение хэша — за постоянное время; затем членство создателя и подписка — в клубе ключа.
func (h *handlers) verify(ctx context.Context, token string) (*keyInfo, string, error) {
	prefix := apikey.KeyPrefix(token)
	if prefix == "" {
		return nil, "invalid_api_key", nil
	}
	if h.d.Pool == nil {
		return nil, "", httpx.ErrNotConfigured
	}
	var (
		row       store.ApiV1FindKeyRow
		found     bool
		ownerOK   bool
		sub       store.GuestClubSubscriptionRow
		hasSub    bool
		notFound  = errors.New("no key")
		clubIDStr string
	)
	err := pgx.BeginTxFunc(ctx, h.d.Pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.api_key_prefix', $1, true)", prefix); err != nil {
			return err
		}
		q := store.New(tx)
		var err error
		row, err = q.ApiV1FindKey(ctx, prefix)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound
		}
		if err != nil {
			return err
		}
		found = true
		if !apikey.HashEqual(row.KeyHash, token) {
			return nil
		}
		clubIDStr = strconv.FormatInt(row.ClubID, 10)
		if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", clubIDStr); err != nil {
			return err
		}
		ownerOK, err = q.ApiV1OwnerAccess(ctx, store.ApiV1OwnerAccessParams{ClubID: row.ClubID, UserID: row.CreatedBy})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		sub, err = q.GuestClubSubscription(ctx, row.ClubID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		default:
			hasSub = true
		}
		return nil
	})
	if errors.Is(err, notFound) {
		return nil, "invalid_api_key", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !found || !apikey.HashEqual(row.KeyHash, token) {
		return nil, "invalid_api_key", nil
	}
	now := h.d.NowFunc()
	switch {
	case row.RevokedAt != nil:
		return nil, "api_key_revoked", nil
	case row.ExpiresAt != nil && !row.ExpiresAt.After(now):
		return nil, "api_key_expired", nil
	case !ownerOK:
		return nil, "api_key_owner_lost_access", nil
	case !hasSub || !subscriptionActive(sub, now):
		return nil, "subscription_inactive", nil
	}
	k := &keyInfo{
		ID: row.ID, ClubID: row.ClubID, CreatedBy: row.CreatedBy, Prefix: row.Prefix, RateLimit: row.RateLimitPerMinute,
		ExpiresAt: row.ExpiresAt, ClubName: row.ClubName, ClubSlug: row.ClubSlug, Scopes: []string{}, Allowed: []string{},
	}
	_ = json.Unmarshal(row.Scopes, &k.Scopes)
	if len(row.AllowedIps) > 0 {
		_ = json.Unmarshal(row.AllowedIps, &k.Allowed)
	}
	return k, "", nil
}

// subscriptionActive — ClubSubscription::isActive() (BR-130).
func subscriptionActive(s store.GuestClubSubscriptionRow, now time.Time) bool {
	switch s.Status {
	case "active":
		return s.CurrentPeriodEndsAt == nil || s.CurrentPeriodEndsAt.After(now)
	case "trialing":
		return s.TrialEndsAt != nil && s.TrialEndsAt.After(now)
	}
	return false
}

// ipAllowed — IpUtils::checkIp($ip, $allowed): адрес или подсеть CIDR.
func ipAllowed(ip string, allowed []string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if strings.Contains(a, "/") {
			if p, err := netip.ParsePrefix(a); err == nil && p.Masked().Contains(addr) {
				return true
			}
			continue
		}
		if b, err := netip.ParseAddr(a); err == nil && b.Unmap() == addr {
			return true
		}
	}
	return false
}

// overridesTenant — X-Club-Id, ?club_id= или club_id в теле, если называет не клуб ключа.
func (c *call) overridesTenant() bool {
	club := strconv.FormatInt(c.key.ClubID, 10)
	claimed := []any{}
	if v := strings.TrimSpace(c.r.Header.Get("X-Club-Id")); v != "" {
		claimed = append(claimed, v)
	}
	if v, ok := c.query()["club_id"]; ok && v != nil {
		claimed = append(claimed, v)
	}
	if v, ok := c.bodyInput()["club_id"]; ok && v != nil {
		claimed = append(claimed, v)
	}
	for _, v := range claimed {
		s, ok := scalarString(v)
		if !ok || s != club {
			return true
		}
	}
	return false
}

// touch — last_used_at не чаще раза в минуту на ключ (Cache::add("api_keys.touched.{id}", true, 60)).
func (h *handlers) touch(c *call) {
	ok, err := h.d.APICounter.SetNX(c.ctx, "touched:"+strconv.FormatInt(c.key.ID, 10), time.Minute)
	if err != nil || !ok || h.d.Pool == nil {
		return
	}
	now := h.d.NowFunc()
	ip := truncate(c.ip, 45)
	err = db.InTenantTx(c.ctx, h.d.Pool, c.key.ClubID, func(q *store.Queries) error {
		return q.ApiV1TouchKey(c.ctx, store.ApiV1TouchKeyParams{Now: &now, Ip: &ip, ClubID: c.key.ClubID, ID: c.key.ID})
	})
	if err != nil {
		h.d.Log().WarnContext(c.ctx, "api v1: touch key", slog.String("err", err.Error()))
	}
}

// ---------------------------------------------------------------------------
// api.limit — ApiRateLimit
// ---------------------------------------------------------------------------

// rateLimit — фиксированные окна по минуте на ключ и на клуб: сначала атомарное увеличение, потом сравнение.
func (h *handlers) rateLimit(c *call, next func() response) response {
	now := h.d.NowFunc().Unix()
	window := strconv.FormatInt(now/60, 10)
	retryAfter := 60 - now%60
	lim := h.d.APILimits
	keyLimit := lim.KeyLimit(c.key.RateLimit)
	keyHits, err := h.d.APICounter.Incr(c.ctx, "rl:key:"+strconv.FormatInt(c.key.ID, 10)+":"+window, 2*time.Minute)
	if err != nil {
		return h.fail(c, fmt.Errorf("rate limit: %w", err))
	}
	clubHits, err := h.d.APICounter.Incr(c.ctx, "rl:club:"+strconv.FormatInt(c.key.ClubID, 10)+":"+window, 2*time.Minute)
	if err != nil {
		return h.fail(c, fmt.Errorf("rate limit: %w", err))
	}
	limit := strconv.Itoa(keyLimit)
	if keyHits > int64(keyLimit) || clubHits > int64(lim.Club) {
		res := errorResponse("rate_limited", http.StatusTooManyRequests)
		res.setHeader("X-RateLimit-Limit", limit)
		res.setHeader("X-RateLimit-Remaining", "0")
		res.setHeader("Retry-After", strconv.FormatInt(retryAfter, 10))
		return res
	}
	res := next()
	res.setHeader("X-RateLimit-Limit", limit)
	res.setHeader("X-RateLimit-Remaining", strconv.FormatInt(max(0, int64(keyLimit)-keyHits), 10))
	return res
}

// ---------------------------------------------------------------------------
// api.scope — RequireApiScope
// ---------------------------------------------------------------------------

func (h *handlers) requireScope(c *call, next func() response) response {
	if c.rt.scope != scopeAny && !slices.Contains(c.key.Scopes, c.rt.scope) {
		e := newError("insufficient_scope", http.StatusForbidden, "")
		e.env.RequiredScopes = []string{c.rt.scope}
		return e.response()
	}
	return next()
}

// ---------------------------------------------------------------------------
// api.idempotent — ApiIdempotency
// ---------------------------------------------------------------------------

var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9_:\-]{1,100}$`)

// idempotent — первый запрос с ключом выполняется, его ответ (статус < 500) хранится; повтор с тем же телом
// получает сохранённый ответ (Idempotent-Replayed: true), с другим — 422, параллельный — 409.
func (h *handlers) idempotent(c *call, next func() response) response {
	if c.rt.idem == idemNone {
		return next()
	}
	header := c.r.Header.Get("Idempotency-Key")
	if header == "" {
		if c.rt.idem == idemRequired {
			return errorResponse("idempotency_key_required", http.StatusPreconditionRequired)
		}
		return next()
	}
	if !idempotencyKeyRe.MatchString(header) {
		return errorResponse("idempotency_key_invalid", http.StatusUnprocessableEntity)
	}
	if h.d.Pool == nil {
		return h.fail(c, httpx.ErrNotConfigured)
	}
	sum := sha256.Sum256([]byte(c.r.Method + " " + strings.TrimLeft(pathOf(c.r), "/") + "\n" + string(c.body)))
	hash := hex.EncodeToString(sum[:])
	clubID, keyID := c.key.ClubID, c.key.ID
	now := h.d.NowFunc()
	var (
		stored store.ApiV1IdempotencyGetRow
		exists bool
		id     int64
		busy   bool
	)
	err := db.InTenantTx(c.ctx, h.d.Pool, clubID, func(q *store.Queries) error {
		exists, busy = false, false
		row, err := q.ApiV1IdempotencyGet(c.ctx, store.ApiV1IdempotencyGetParams{ClubID: clubID, ApiKeyID: keyID, IdempotencyKey: header})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case row.CreatedAt.Before(now.Add(-h.d.APILimits.IdempotencyRetention)):
			// Срок хранения вышел (integrations:prune удалял такие строки раз в час) — ключ снова свободен.
			if err := q.ApiV1IdempotencyDelete(c.ctx, store.ApiV1IdempotencyDeleteParams{ClubID: clubID, ID: row.ID}); err != nil {
				return err
			}
		default:
			stored, exists = row, true
			return nil
		}
		id, err = q.ApiV1IdempotencyInsert(c.ctx, store.ApiV1IdempotencyInsertParams{
			ClubID: clubID, ApiKeyID: keyID, IdempotencyKey: header, RequestHash: hash, Now: now,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			busy = true
			return nil
		}
		return err
	})
	if err != nil {
		return h.fail(c, err)
	}
	if busy {
		return errorResponse("idempotency_in_progress", http.StatusConflict)
	}
	if exists {
		switch {
		case stored.RequestHash != hash:
			return errorResponse("idempotency_key_reused", http.StatusUnprocessableEntity)
		case stored.Status == nil:
			return errorResponse("idempotency_in_progress", http.StatusConflict)
		}
		res := response{status: int(*stored.Status)}
		if stored.Response != nil {
			res.body = []byte(*stored.Response)
		}
		res.setHeader("Content-Type", "application/json")
		res.setHeader("Idempotent-Replayed", "true")
		return res
	}
	return h.runIdempotent(c, id, next)
}

func (h *handlers) runIdempotent(c *call, id int64, next func() response) response {
	clubID := c.key.ClubID
	ctx := context.WithoutCancel(c.ctx)
	release := func() {
		if err := db.InTenantTx(ctx, h.d.Pool, clubID, func(q *store.Queries) error {
			return q.ApiV1IdempotencyDelete(ctx, store.ApiV1IdempotencyDeleteParams{ClubID: clubID, ID: id})
		}); err != nil {
			h.d.Log().ErrorContext(ctx, "api v1: release idempotency key", slog.String("err", err.Error()))
		}
	}
	done := false
	defer func() {
		if !done {
			release() // паника обработчика: ключ свободен для повтора
		}
	}()
	res := next()
	done = true
	if res.status >= http.StatusInternalServerError {
		release() // клиент может повторить
		return res
	}
	status := int32(res.status) // #nosec G115 -- статус HTTP
	body := string(res.body)
	if err := db.InTenantTx(ctx, h.d.Pool, clubID, func(q *store.Queries) error {
		return q.ApiV1IdempotencyFinish(ctx, store.ApiV1IdempotencyFinishParams{Status: &status, Response: &body, ClubID: clubID, ID: id})
	}); err != nil {
		h.d.Log().ErrorContext(ctx, "api v1: store idempotent response", slog.String("err", err.Error()))
	}
	return res
}
