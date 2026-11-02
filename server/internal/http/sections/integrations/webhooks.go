package integrations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	integrationsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/integrations"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/webhook"
)

// webhookData — проверенные url и события.
type webhookData struct {
	url    string
	events []string
}

// validateWebhook — validateWebhook(): url required|string|max:500|url:https,http, events required|array|min:1,
// events.* in; затем WebhookUrlGuard (вне транзакции: DNS).
func (h *handlers) validateWebhook(ctx context.Context, rawURL *string, evs *[]string) (webhookData, error) {
	var d webhookData
	var fe httpx.FormErrors
	if rawURL != nil {
		d.url = strings.TrimSpace(*rawURL)
	}
	switch {
	case d.url == "":
		fe.Add("url", httpx.RuleMessage("url", cash.RuleRequired, ""))
	case utf8.RuneCountInString(d.url) > 500:
		fe.Add("url", httpx.RuleMessage("url", "max_string", "500"))
	case !validURL(d.url):
		// validation.url — в ru.php нет: английский текст валидатора по умолчанию, как в старом.
		fe.Add("url", "The url field must be a valid URL.")
	}
	if evs == nil || len(*evs) == 0 {
		fe.Add("events", httpx.RuleMessage("events", cash.RuleRequired, ""))
	} else {
		for i, e := range *evs {
			if !knownEvent(e) {
				fe.Add("events."+strconv.Itoa(i), httpx.RuleMessage("events."+strconv.Itoa(i), cash.RuleIn, ""))
			} else if !slices.Contains(d.events, e) {
				d.events = append(d.events, e)
			}
		}
	}
	if err := fe.Err(); err != nil {
		return d, err
	}
	if h.d.WebhookURLCheck == nil {
		return d, httpx.ErrNotConfigured
	}
	if reason := h.d.WebhookURLCheck(ctx, d.url); reason != "" {
		msg, ok := guardMessages[reason]
		if !ok {
			msg = "Некорректный адрес вебхука."
		}
		return d, httpx.Validation("url", msg)
	}
	return d, nil
}

// validURL — правило url:https,http (схема http/https и хост).
func validURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http"
}

// newSecret — 'whsec_'.Str::random(40).
func newSecret() (string, error) {
	s, err := randomString(40)
	return "whsec_" + s, err
}

// sealSecret — секрет в базу только зашифрованным (в Laravel — каст 'encrypted', app/Models/WebhookEndpoint.php;
// Q-050): ключи security.data_keys, контекст — клуб. Открытый секрет показывается один раз в ответе.
func (h *handlers) sealSecret(clubID int64, secret string) (string, error) {
	if h.d.Secrets == nil {
		return "", httpx.ErrNotConfigured
	}
	return h.d.Secrets.SealWith(secret, webhook.SecretContext(clubID))
}

// CreateWebhook — storeWebhook(): не больше 20 вебхуков; секрет показывается один раз.
func (h *handlers) CreateWebhook(ctx context.Context, req integrationsapi.CreateWebhookRequestObject) (integrationsapi.CreateWebhookResponseObject, error) {
	var body integrationsapi.WebhookRequest
	if req.Body != nil {
		body = *req.Body
	}
	now := h.d.NowFunc()
	var out integrationsapi.CreatedSecret
	// Доступ — до проверки адреса (DNS): посторонний не должен заставлять сервер резолвить хосты.
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error { _, err := h.allow(ctx, q); return err }); err != nil {
		return nil, err
	}
	d, err := h.validateWebhook(ctx, body.Url, body.Events)
	if err != nil {
		return nil, err
	}
	secret, err := newSecret()
	if err != nil {
		return nil, err
	}
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		n, err := q.IntegrationsCountWebhooks(ctx, v.p.ClubID)
		if err != nil {
			return err
		}
		if n >= maxWebhooks {
			return httpx.Validation("url", msgTooManyWebhooks)
		}
		sealed, err := h.sealSecret(v.p.ClubID, secret)
		if err != nil {
			return err
		}
		id, err := q.IntegrationsInsertWebhook(ctx, store.IntegrationsInsertWebhookParams{
			ClubID: v.p.ClubID, CreatedBy: v.p.UserID, Url: d.url, Secret: sealed, Events: jsonList(d.events), Now: &now,
		})
		if err != nil {
			return err
		}
		out = integrationsapi.CreatedSecret{Type: integrationsapi.Webhook, Name: d.url, Secret: secret}
		return h.record(ctx, q, v.p, "webhook.created", subjectWebhook, id, "Добавлен вебхук: :url",
			map[string]string{"url": d.url}, map[string]any{"events": strings.Join(d.events, ", ")}, now)
	})
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	return integrationsapi.CreateWebhook201JSONResponse(out), nil
}

// UpdateWebhook — updateWebhook(): url и события (если есть url), is_active; включение сбрасывает счётчик ошибок.
func (h *handlers) UpdateWebhook(ctx context.Context, req integrationsapi.UpdateWebhookRequestObject) (integrationsapi.UpdateWebhookResponseObject, error) {
	var body integrationsapi.WebhookUpdateRequest
	if req.Body != nil {
		body = *req.Body
	}
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error { _, err := h.allow(ctx, q); return err }); err != nil {
		return nil, err
	}
	var d *webhookData
	if body.Url != nil {
		v, err := h.validateWebhook(ctx, body.Url, body.Events)
		if err != nil {
			return nil, err
		}
		d = &v
	}
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		w, err := q.IntegrationsLockWebhook(ctx, store.IntegrationsLockWebhookParams{ClubID: v.p.ClubID, ID: req.WebhookId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		newURL, newEvents, active := w.Url, listOf(w.Events), w.IsActive
		if d != nil {
			newURL, newEvents = d.url, d.events
		}
		if body.IsActive != nil {
			active = *body.IsActive
		}
		changes := map[string]any{}
		if newURL != w.Url {
			changes["url"] = phpJSONString(newURL)
		}
		if !slices.Equal(newEvents, listOf(w.Events)) {
			changes["events"] = strings.Join(newEvents, ", ")
		}
		if active != w.IsActive {
			changes["is_active"] = strconv.FormatBool(active) // json_encode(bool)
		}
		if err := q.IntegrationsUpdateWebhook(ctx, store.IntegrationsUpdateWebhookParams{
			Url: newURL, Events: jsonList(newEvents), IsActive: active, ResetFailures: active && !w.IsActive, Now: &now,
			ClubID: v.p.ClubID, ID: w.ID,
		}); err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		return h.record(ctx, q, v.p, "webhook.updated", subjectWebhook, w.ID, "Изменён вебхук: :url",
			map[string]string{"url": newURL}, changes, now)
	})
	if err != nil {
		return nil, err
	}
	return integrationsapi.UpdateWebhook200JSONResponse{Message: h.d.T(ctx, msgWebhookSaved, nil)}, nil
}

// phpJSONString — json_encode($string) PHP без флагов: «/» → «\/», не-ASCII → \uXXXX (детали журнала
// webhook.updated, как в старом).
func phpJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '/':
			b.WriteString(`\/`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r < 0x80:
			b.WriteRune(r)
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// RotateWebhookSecret — rotateWebhookSecret().
func (h *handlers) RotateWebhookSecret(ctx context.Context, req integrationsapi.RotateWebhookSecretRequestObject) (integrationsapi.RotateWebhookSecretResponseObject, error) {
	secret, err := newSecret()
	if err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	var out integrationsapi.CreatedSecret
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		w, err := q.IntegrationsLockWebhook(ctx, store.IntegrationsLockWebhookParams{ClubID: v.p.ClubID, ID: req.WebhookId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		sealed, err := h.sealSecret(v.p.ClubID, secret)
		if err != nil {
			return err
		}
		if err := q.IntegrationsSetWebhookSecret(ctx, store.IntegrationsSetWebhookSecretParams{Secret: sealed, Now: &now, ClubID: v.p.ClubID, ID: w.ID}); err != nil {
			return err
		}
		out = integrationsapi.CreatedSecret{Type: integrationsapi.Webhook, Name: w.Url, Secret: secret}
		return h.record(ctx, q, v.p, "webhook.secret_rotated", subjectWebhook, w.ID, "Перевыпущен секрет вебхука: :url",
			map[string]string{"url": w.Url}, nil, now)
	})
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	return integrationsapi.RotateWebhookSecret200JSONResponse(out), nil
}

// DeleteWebhook — destroyWebhook() (доставки удаляются каскадом).
func (h *handlers) DeleteWebhook(ctx context.Context, req integrationsapi.DeleteWebhookRequestObject) (integrationsapi.DeleteWebhookResponseObject, error) {
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		w, err := q.IntegrationsLockWebhook(ctx, store.IntegrationsLockWebhookParams{ClubID: v.p.ClubID, ID: req.WebhookId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if err := q.IntegrationsDeleteWebhook(ctx, store.IntegrationsDeleteWebhookParams{ClubID: v.p.ClubID, ID: w.ID}); err != nil {
			return err
		}
		return h.record(ctx, q, v.p, "webhook.deleted", subjectWebhook, w.ID, "Удалён вебхук: :url",
			map[string]string{"url": w.Url}, nil, now)
	})
	if err != nil {
		return nil, err
	}
	return integrationsapi.DeleteWebhook200JSONResponse{Message: h.d.T(ctx, msgWebhookDeleted, nil)}, nil
}

// RetryWebhookDelivery — retryDelivery(): только failed/skipped, иначе 404.
func (h *handlers) RetryWebhookDelivery(ctx context.Context, req integrationsapi.RetryWebhookDeliveryRequestObject) (integrationsapi.RetryWebhookDeliveryResponseObject, error) {
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		del, err := q.IntegrationsLockDelivery(ctx, store.IntegrationsLockDeliveryParams{ClubID: v.p.ClubID, ID: req.DeliveryId})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && del.Status != "failed" && del.Status != "skipped") {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if err := q.IntegrationsRetryDelivery(ctx, store.IntegrationsRetryDeliveryParams{Now: &now, ClubID: v.p.ClubID, ID: del.ID}); err != nil {
			return err
		}
		return h.record(ctx, q, v.p, "webhook.retried", subjectDelivery, del.ID, "Повторная отправка вебхука: :event",
			map[string]string{"event": del.Event}, nil, now)
	})
	if err != nil {
		return nil, err
	}
	if h.d.WakeWebhooks != nil {
		h.d.WakeWebhooks() // DeliverWebhook::dispatch()->afterCommit()
	}
	return integrationsapi.RetryWebhookDelivery200JSONResponse{Message: h.d.T(ctx, msgRetryQueued, nil)}, nil
}

// UpdateIntegrationsPermission — updatePermission(): только владелец (403), владельца — 404; required|boolean.
func (h *handlers) UpdateIntegrationsPermission(ctx context.Context, req integrationsapi.UpdateIntegrationsPermissionRequestObject) (integrationsapi.UpdateIntegrationsPermissionResponseObject, error) {
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		v, err := h.allow(ctx, q)
		if err != nil {
			return err
		}
		if !v.owner {
			return httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, "")
		}
		m, err := q.IntegrationsLockMember(ctx, store.IntegrationsLockMemberParams{ClubID: v.p.ClubID, ID: req.MembershipId})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && m.Role == permissions.RoleOwner) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if req.Body == nil || req.Body.CanManageIntegrations == nil {
			return httpx.RuleError("can_manage_integrations", cash.RuleRequired)
		}
		allowed := *req.Body.CanManageIntegrations
		if err := q.IntegrationsSetMemberAccess(ctx, store.IntegrationsSetMemberAccessParams{Allowed: allowed, Now: &now, ClubID: v.p.ClubID, ID: m.ID}); err != nil {
			return err
		}
		name := ""
		if m.Name != nil {
			name = *m.Name
		}
		key := "Снят доступ к интеграциям: :user"
		if allowed {
			key = "Выдан доступ к интеграциям: :user"
		}
		if err := h.record(ctx, q, v.p, "integrations.permission_changed", subjectUser, m.UserID, key,
			map[string]string{"user": name}, map[string]any{"can_manage_integrations": strconv.FormatBool(allowed)}, now); err != nil {
			return err
		}
		if err := audit(ctx, q, v.p.ClubID, "permission", "integrations.permission_changed", `App\Models\ClubMembership`, m.ID,
			map[string]any{"can_manage_integrations": m.CanManageIntegrations}, map[string]any{"can_manage_integrations": allowed}); err != nil {
			return err
		}
		return accessChanged(ctx, q, v.p.ClubID, m.UserID)
	})
	if err != nil {
		return nil, err
	}
	return integrationsapi.UpdateIntegrationsPermission200JSONResponse{Message: h.d.T(ctx, msgRightsUpdated, nil)}, nil
}
