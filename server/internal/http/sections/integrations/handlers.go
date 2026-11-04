package integrations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	integrationsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/integrations"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/realtime"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты IntegrationController (ключи перевода).
const (
	msgTooManyKeys     = "Слишком много активных ключей. Отзовите неиспользуемые."
	msgTooManyWebhooks = "Достигнуто максимальное число вебхуков."
	msgBadIP           = "Некорректный IP-адрес или подсеть: :value"
	msgKeyRevoked      = "Ключ отозван. Запросы с ним больше не принимаются."
	msgWebhookSaved    = "Вебхук сохранён."
	msgWebhookDeleted  = "Вебхук удалён."
	msgRetryQueued     = "Отправка поставлена в очередь."
	msgRightsUpdated   = "Права обновлены."

	subjectAPIKey   = `App\Models\ApiKey` // #nosec G101 -- имя класса Laravel, не секрет
	subjectWebhook  = `App\Models\WebhookEndpoint`
	subjectDelivery = `App\Models\WebhookDelivery`
	subjectUser     = `App\Models\User`
)

// guardMessages — WebhookUrlGuard: причина → текст ошибки поля url.
var guardMessages = map[string]string{
	"https_required":    "Адрес вебхука должен начинаться с https://.",
	"private_address":   "Адрес вебхука указывает на внутреннюю сеть. Нужен публичный адрес.",
	"port_not_allowed":  "Разрешены только порты 80, 443, 8080 и 8443.",
	"host_not_resolved": "Не удалось найти хост вебхука.",
}

// viewer — кто открыл раздел.
type viewer struct {
	p     principal.Principal
	owner bool
}

// allow — @can('manage-integrations') + middleware subscription/клуб/режим просмотра (CheckClub): владелец или
// сотрудник с флагом can_manage_integrations; иначе 403.
func (h *handlers) allow(ctx context.Context, q *store.Queries) (viewer, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return viewer{}, httpx.NewAPIError(http.StatusUnauthorized, permissions.CodeUnauthenticated, permissions.MsgUnauthenticated)
	}
	method := http.MethodGet
	if _, r := httpx.HTTPFrom(ctx); r != nil {
		method = r.Method
	}
	if status, code, msg, ok := permissions.CheckClub(p, method); !ok {
		return viewer{}, httpx.NewAPIError(status, code, msg)
	}
	if p.Role == permissions.RoleOwner {
		return viewer{p: p, owner: true}, nil
	}
	m, err := q.IntegrationsAccess(ctx, store.IntegrationsAccessParams{ClubID: p.ClubID, UserID: p.UserID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !m.CanManageIntegrations && m.Role != permissions.RoleOwner) {
		return viewer{}, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, "")
	}
	if err != nil {
		return viewer{}, err
	}
	return viewer{p: p, owner: m.Role == permissions.RoleOwner}, nil
}

// noStore — страница с секретом не должна сохраняться браузером и прокси (page(): Cache-Control).
func noStore(ctx context.Context) {
	if w, _ := httpx.HTTPFrom(ctx); w != nil {
		w.Header().Set("Cache-Control", "no-store, private")
	}
}

func listOf(raw json.RawMessage) []string {
	out := []string{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// GetIntegrations — index() / page().
func (h *handlers) GetIntegrations(ctx context.Context, _ integrationsapi.GetIntegrationsRequestObject) (integrationsapi.GetIntegrationsResponseObject, error) {
	out := integrationsapi.IntegrationsPage{
		Keys: []integrationsapi.ApiKeyItem{}, Webhooks: []integrationsapi.WebhookItem{}, Requests: []integrationsapi.RequestLogItem{},
		Staff: []integrationsapi.StaffAccessItem{}, DefaultRateLimit: h.d.APILimits.WithDefaults().KeyDefault, MaxRateLimit: h.d.APILimits.WithDefaults().KeyMax,
	}
	for _, s := range scopes {
		out.Scopes = append(out.Scopes, integrationsapi.ScopeInfo{Scope: s.name, Label: s.label, Dangerous: s.dangerous})
	}
	for _, e := range events {
		out.Events = append(out.Events, integrationsapi.EventInfo{Event: e.name, Label: e.label})
	}
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		clubID := v.p.ClubID
		out.IsOwner = v.owner
		club, err := q.GetClub(ctx, clubID)
		if err != nil {
			return err
		}
		out.MinExpiresAt = tomorrow(now, club.Timezone)
		keys, err := q.IntegrationsListKeys(ctx, clubID)
		if err != nil {
			return err
		}
		for _, k := range keys {
			item := integrationsapi.ApiKeyItem{
				Id: k.ID, Name: k.Name, CreatorName: k.CreatorName, CreatedAt: k.CreatedAt, Prefix: k.Prefix,
				Scopes: listOf(k.Scopes), AllowedIps: listOf(k.AllowedIps), ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt,
				LastUsedIp: k.LastUsedIp, Status: integrationsapi.Active,
			}
			// min($key->rate_limit_per_minute ?? default, max).
			item.RateLimitPerMinute = h.d.APILimits.WithDefaults().KeyLimit(k.RateLimitPerMinute)
			switch {
			case k.RevokedAt != nil:
				item.Status = integrationsapi.Revoked
			case k.ExpiresAt != nil && !k.ExpiresAt.After(now): // isPast()
				item.Status = integrationsapi.Expired
			}
			out.Keys = append(out.Keys, item)
		}
		hooks, err := q.IntegrationsListWebhooks(ctx, clubID)
		if err != nil {
			return err
		}
		for _, w := range hooks {
			out.Webhooks = append(out.Webhooks, integrationsapi.WebhookItem{
				Id: w.ID, Url: w.Url, Events: listOf(w.Events), IsActive: w.IsActive, FailureCount: w.FailureCount,
				LastSuccessAt: w.LastSuccessAt, LastFailureAt: w.LastFailureAt,
			})
		}
		if out.Deliveries, err = deliveries(ctx, q, clubID, 0); err != nil {
			return err
		}
		reqs, err := q.IntegrationsListRequests(ctx, &clubID)
		if err != nil {
			return err
		}
		for _, r := range reqs {
			out.Requests = append(out.Requests, integrationsapi.RequestLogItem{
				CreatedAt: r.CreatedAt, KeyPrefix: r.KeyPrefix, Method: r.Method, Endpoint: r.Endpoint, Status: r.Status,
				DurationMs: r.DurationMs, Ip: r.Ip,
			})
		}
		if !v.owner {
			return nil
		}
		staff, err := q.IntegrationsListStaff(ctx, clubID)
		for _, m := range staff {
			out.Staff = append(out.Staff, integrationsapi.StaffAccessItem{MembershipId: m.ID, Name: m.Name, Role: m.Role,
				CanManageIntegrations: m.CanManageIntegrations})
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	return integrationsapi.GetIntegrations200JSONResponse(out), nil
}

// tomorrow — now()->addDay()->toDateString() в поясе клуба.
func tomorrow(now time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	return now.In(loc).AddDate(0, 0, 1).Format("2006-01-02")
}

// deliveryCursor — keyset журнала доставок: id последней показанной строки.
type deliveryCursor struct {
	ID int64 `json:"id"`
}

func deliveries(ctx context.Context, q *store.Queries, clubID, beforeID int64) (integrationsapi.DeliveryPage, error) {
	page := integrationsapi.DeliveryPage{Items: []integrationsapi.DeliveryItem{}}
	rows, err := q.IntegrationsListDeliveries(ctx, store.IntegrationsListDeliveriesParams{ClubID: clubID, BeforeID: beforeID, PageSize: deliveriesPage + 1})
	if err != nil {
		return page, err
	}
	if len(rows) > deliveriesPage {
		rows = rows[:deliveriesPage]
		page.NextCursor = httpx.EncodeCursor(deliveryCursor{ID: rows[len(rows)-1].ID})
	}
	for _, d := range rows {
		page.Items = append(page.Items, integrationsapi.DeliveryItem{
			Id: d.ID, CreatedAt: d.CreatedAt, Event: d.Event, EndpointUrl: d.EndpointUrl, Status: d.Status, LastError: d.LastError,
			Attempts: d.Attempts,
		})
	}
	return page, nil
}

// ListWebhookDeliveries — следующая страница журнала доставок.
func (h *handlers) ListWebhookDeliveries(ctx context.Context, req integrationsapi.ListWebhookDeliveriesRequestObject) (integrationsapi.ListWebhookDeliveriesResponseObject, error) {
	var cur deliveryCursor
	if err := httpx.DecodeCursor(req.Params.Cursor, &cur); err != nil {
		return nil, err
	}
	var out integrationsapi.DeliveryPage
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		out, err = deliveries(ctx, q, v.p.ClubID, cur.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return integrationsapi.ListWebhookDeliveries200JSONResponse(out), nil
}

// randomString — Str::random(n): буквы и цифры.
func randomString(n int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[k.Int64()]
	}
	return string(b), nil
}

// newKey — ApiKeyManager::create(): префикс zfk_<8 строчных>, ключ <префикс>_<40>, хранится SHA-256.
func newKey() (prefix, plain, hash string, err error) {
	p, err := randomString(8)
	if err != nil {
		return "", "", "", err
	}
	s, err := randomString(40)
	if err != nil {
		return "", "", "", err
	}
	prefix = keyPrefix + strings.ToLower(p)
	plain = prefix + "_" + s
	sum := sha256.Sum256([]byte(plain))
	return prefix, plain, hex.EncodeToString(sum[:]), nil
}

// keyData — проверенные поля нового ключа.
type keyData struct {
	name      string
	scopes    []string
	ips       []string
	rateLimit *int32
	expiresAt *time.Time
}

var ipSplit = regexp.MustCompile(`[\s,;]+`)

// parseIPs — IntegrationController::parseIps(): IP или подсети через пробелы, запятые, «;»; пусто — nil.
func parseIPs(s string, fe *httpx.FormErrors) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range ipSplit.Split(s, -1) {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		ip, bits, hasBits := strings.Cut(e, "/")
		addr, err := netip.ParseAddr(ip)
		valid := err == nil && addr.Zone() == ""
		if valid && hasBits {
			n, err := strconv.Atoi(bits)
			limit := 32
			if strings.Contains(ip, ":") {
				limit = 128
			}
			valid = err == nil && bits != "" && strings.Trim(bits, "0123456789") == "" && n <= limit
		}
		if !valid {
			fe.AddMsg("allowed_ips", msgBadIP, map[string]string{"value": e})
			return nil
		}
		out = append(out, e)
	}
	return out
}

func validateKey(req *integrationsapi.ApiKeyRequest, now time.Time, tz string, maxRate int) (keyData, error) {
	var d keyData
	var fe httpx.FormErrors
	if req == nil {
		req = &integrationsapi.ApiKeyRequest{}
	}
	if req.Name != nil {
		d.name = strings.TrimSpace(*req.Name)
	}
	switch n := utf8.RuneCountInString(d.name); {
	case n == 0:
		fe.Add("name", httpx.RuleMessage("name", cash.RuleRequired, ""))
	case n > 100:
		fe.Add("name", httpx.RuleMessage("name", "max_string", "100"))
	}
	if req.Scopes == nil || len(*req.Scopes) == 0 {
		fe.Add("scopes", httpx.RuleMessage("scopes", cash.RuleRequired, ""))
	} else {
		seen := map[string]bool{}
		for i, s := range *req.Scopes {
			if !knownScope(s) {
				fe.Add("scopes."+strconv.Itoa(i), httpx.RuleMessage("scopes."+strconv.Itoa(i), cash.RuleIn, ""))
				continue
			}
			if !seen[s] {
				seen[s] = true
				d.scopes = append(d.scopes, s) // array_values(array_unique())
			}
		}
	}
	if req.AllowedIps != nil {
		s := strings.TrimSpace(*req.AllowedIps)
		if utf8.RuneCountInString(s) > 2000 {
			fe.Add("allowed_ips", httpx.RuleMessage("allowed_ips", "max_string", "2000"))
		} else if s != "" {
			d.ips = parseIPs(s, &fe)
		}
	}
	if req.RateLimitPerMinute != nil {
		raw, _ := json.Marshal(req.RateLimitPerMinute)
		var v any
		_ = json.Unmarshal(raw, &v)
		str := strings.TrimSpace(strings.Trim(string(raw), `"`))
		if s, ok := v.(string); ok {
			str = strings.TrimSpace(s)
		}
		if str != "" && str != "null" {
			n, err := strconv.ParseInt(str, 10, 32)
			switch {
			case err != nil:
				fe.Add("rate_limit_per_minute", httpx.RuleMessage("rate_limit_per_minute", cash.RuleInteger, ""))
			case n < 1:
				fe.Add("rate_limit_per_minute", httpx.RuleMessage("rate_limit_per_minute", cash.RuleMin, "1"))
			case n > int64(maxRate):
				fe.Add("rate_limit_per_minute", httpx.RuleMessage("rate_limit_per_minute", cash.RuleMax, strconv.Itoa(maxRate)))
			default:
				v := int32(n)
				d.rateLimit = &v
			}
		}
	}
	if req.ExpiresAt != nil && strings.TrimSpace(*req.ExpiresAt) != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil || tz == "" {
			loc = time.UTC
		}
		t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(*req.ExpiresAt), loc)
		switch {
		case err != nil:
			// validation.date
			fe.Add("expires_at", httpx.RuleMessage("expires_at", "date", ""))
		case t.Format("2006-01-02") <= now.In(loc).Format("2006-01-02"):
			// after:today — в ru.php нет: английский текст валидатора по умолчанию, как в старом.
			fe.Add("expires_at", "The expires at field must be a date after today.")
		default:
			d.expiresAt = &t
		}
	}
	return d, fe.Err()
}

func jsonList(v []string) json.RawMessage {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}

// insertKey — новый ключ клуба (уникальный префикс: повтор при совпадении, как do…while exists()).
func insertKey(ctx context.Context, q *store.Queries, clubID, userID int64, d keyData, rotatedFrom *int64, now time.Time) (id int64, prefix, plain string, err error) {
	for range 5 {
		var hash string
		if prefix, plain, hash, err = newKey(); err != nil {
			return 0, "", "", err
		}
		id, err = q.IntegrationsInsertKey(ctx, store.IntegrationsInsertKeyParams{
			ClubID: clubID, CreatedBy: userID, Name: d.name, Prefix: prefix, KeyHash: hash, Scopes: jsonList(d.scopes),
			AllowedIps: jsonList(d.ips), RateLimitPerMinute: d.rateLimit, ExpiresAt: d.expiresAt, RotatedFromID: rotatedFrom, Now: &now,
		})
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "api_keys_prefix_unique" {
			continue
		}
		return id, prefix, plain, err
	}
	return 0, "", "", err
}

// CreateApiKey — storeKey().
func (h *handlers) CreateApiKey(ctx context.Context, req integrationsapi.CreateApiKeyRequestObject) (integrationsapi.CreateApiKeyResponseObject, error) {
	now := h.d.NowFunc()
	var out integrationsapi.CreatedSecret
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		club, err := q.GetClub(ctx, v.p.ClubID)
		if err != nil {
			return err
		}
		d, err := validateKey(req.Body, now, club.Timezone, h.d.APILimits.WithDefaults().KeyMax)
		if err != nil {
			return err
		}
		active, err := q.IntegrationsCountActiveKeys(ctx, v.p.ClubID)
		if err != nil {
			return err
		}
		if active >= maxActiveKeys {
			return httpx.Validation("name", msgTooManyKeys)
		}
		id, prefix, plain, err := insertKey(ctx, q, v.p.ClubID, v.p.UserID, d, nil, now)
		if err != nil {
			return err
		}
		expires := "—"
		if d.expiresAt != nil {
			expires = d.expiresAt.Format("2006-01-02")
		}
		ips := strings.Join(d.ips, ", ")
		if ips == "" {
			ips = "—"
		}
		if err := h.record(ctx, q, v.p, "api_key.created", subjectAPIKey, id, "Создан API-ключ: :name (:prefix)",
			map[string]string{"name": d.name, "prefix": prefix},
			map[string]any{"scopes": strings.Join(d.scopes, ", "), "allowed_ips": ips, "expires_at": expires}, now); err != nil {
			return err
		}
		if err := audit(ctx, q, v.p.ClubID, "security", "api_key.created", subjectAPIKey, id, nil,
			map[string]any{"name": d.name, "prefix": prefix, "scopes": d.scopes, "allowed_ips": d.ips, "expires_at": d.expiresAt}); err != nil {
			return err
		}
		out = integrationsapi.CreatedSecret{Type: integrationsapi.Key, Name: d.name, Secret: plain}
		return nil
	})
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	return integrationsapi.CreateApiKey201JSONResponse(out), nil
}

// RotateApiKey — rotateKey(): отозванный — 404; новый ключ с теми же правами, старый отзывается.
func (h *handlers) RotateApiKey(ctx context.Context, req integrationsapi.RotateApiKeyRequestObject) (integrationsapi.RotateApiKeyResponseObject, error) {
	now := h.d.NowFunc()
	var out integrationsapi.CreatedSecret
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		old, err := q.IntegrationsLockKey(ctx, store.IntegrationsLockKeyParams{ClubID: v.p.ClubID, ID: req.KeyId})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && old.RevokedAt != nil) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		d := keyData{name: old.Name, scopes: listOf(old.Scopes), rateLimit: old.RateLimitPerMinute, expiresAt: old.ExpiresAt}
		if ips := listOf(old.AllowedIps); len(ips) > 0 {
			d.ips = ips
		}
		id, prefix, plain, err := insertKey(ctx, q, v.p.ClubID, v.p.UserID, d, &old.ID, now)
		if err != nil {
			return err
		}
		if err := q.IntegrationsRevokeKey(ctx, store.IntegrationsRevokeKeyParams{Now: &now, RevokedBy: &v.p.UserID, ClubID: v.p.ClubID, ID: old.ID}); err != nil {
			return err
		}
		if err := h.record(ctx, q, v.p, "api_key.rotated", subjectAPIKey, id, "API-ключ перевыпущен: :name (:old → :new)",
			map[string]string{"name": old.Name, "old": old.Prefix, "new": prefix}, nil, now); err != nil {
			return err
		}
		if err := audit(ctx, q, v.p.ClubID, "security", "api_key.rotated", subjectAPIKey, id,
			map[string]any{"prefix": old.Prefix}, map[string]any{"prefix": prefix}); err != nil {
			return err
		}
		out = integrationsapi.CreatedSecret{Type: integrationsapi.Key, Name: old.Name, Secret: plain}
		return nil
	})
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	return integrationsapi.RotateApiKey200JSONResponse(out), nil
}

// RevokeApiKey — revokeKey(): уже отозванный — без изменений.
func (h *handlers) RevokeApiKey(ctx context.Context, req integrationsapi.RevokeApiKeyRequestObject) (integrationsapi.RevokeApiKeyResponseObject, error) {
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		k, err := q.IntegrationsLockKey(ctx, store.IntegrationsLockKeyParams{ClubID: v.p.ClubID, ID: req.KeyId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil || k.RevokedAt != nil {
			return err
		}
		if err := q.IntegrationsRevokeKey(ctx, store.IntegrationsRevokeKeyParams{Now: &now, RevokedBy: &v.p.UserID, ClubID: v.p.ClubID, ID: k.ID}); err != nil {
			return err
		}
		if err := h.record(ctx, q, v.p, "api_key.revoked", subjectAPIKey, k.ID, "API-ключ отозван: :name (:prefix)",
			map[string]string{"name": k.Name, "prefix": k.Prefix}, nil, now); err != nil {
			return err
		}
		return audit(ctx, q, v.p.ClubID, "security", "api_key.revoked", subjectAPIKey, k.ID, map[string]any{"prefix": k.Prefix}, nil)
	})
	if err != nil {
		return nil, err
	}
	return integrationsapi.RevokeApiKey200JSONResponse{Message: h.d.T(ctx, msgKeyRevoked, nil)}, nil
}

// record — запись журнала действий (ключ + параметры, W-3).
func (h *handlers) record(ctx context.Context, q *store.Queries, p principal.Principal, ev, subjectType string, subjectID int64,
	key string, params map[string]string, details map[string]any, now time.Time) error {
	_, err := activity.Record(ctx, q, activity.Entry{
		ClubID: p.ClubID, Event: ev, UserID: p.UserID, SubjectType: subjectType, SubjectID: subjectID, Details: details, At: now,
	}.Translated(key, params))
	return err
}

// audit — audit_log (только добавление): ключи API — security, доступ к интеграциям — permission. Секретов нет.
func audit(ctx context.Context, q *store.Queries, clubID int64, category, action, subjectType string, subjectID int64, before, after any) error {
	a := httpx.Actor(ctx)
	p := store.InsertAuditLogParams{ClubID: &clubID, ActorKind: a.Kind, Category: category, Action: action,
		SubjectType: &subjectType, SubjectID: &subjectID}
	if a.UserID > 0 {
		p.ActorUserID = &a.UserID
	}
	if a.IP.IsValid() {
		ip := a.IP
		p.Ip = &ip
	}
	if a.RequestID != "" {
		p.RequestID = &a.RequestID
	}
	var err error
	if before != nil {
		if p.Before, err = json.Marshal(before); err != nil {
			return err
		}
	}
	if after != nil {
		if p.After, err = json.Marshal(after); err != nil {
			return err
		}
	}
	return q.InsertAuditLog(ctx, p)
}

// accessChanged — staff.access_changed: меню сотрудника («API и интеграции») обновляется после переподключения.
func accessChanged(ctx context.Context, q *store.Queries, clubID, userID int64) error {
	return billingsvc.AddEvent(ctx, q, clubID, realtime.AccessChangedEvent, map[string]any{"user_id": userID})
}
