package superadmin

// Telegram Gateway (коды восстановления пароля) и бот оповещений о сбоях — только настройки. Laravel:
// app/Http/Controllers/Superadmin/TelegramController.php, app/Services/TelegramGateway.php,
// app/Services/Monitoring/TelegramAlerts.php. Сама отправка через Telegram (проверка токена, поиск чата,
// проверочное сообщение, алерты) — Этап 4 (DECISIONS «Telegram», Q-060): docs/open-questions/stage3-superadmin.md SA-5.
// Токены хранятся зашифрованными (secretbox, как Crypt::encryptString), в ответ и журнал не попадают.

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	settingGateway = "telegram_gateway" // TelegramGateway::SETTING
	settingAlerts  = "telegram_alerts"  // TelegramAlerts::SETTING
)

var (
	gatewayTokenRe = regexp.MustCompile(`^[A-Za-z0-9_\-:.]{20,255}$`)
	botTokenRe     = regexp.MustCompile(`^\d{5,15}:[A-Za-z0-9_\-]{30,100}$`)
	chatIDRe       = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z0-9_]{5,32})$`)
)

type secretSetting struct {
	Token  string `json:"token"`
	ChatID string `json:"chat_id,omitempty"`
	Locale string `json:"locale,omitempty"`
}

// readSecret — настройка с токеном; токен, который не расшифровывается (другой ключ), — «не настроено».
func (h *handlers) readSecret(ctx context.Context, q *store.Queries, key string) (*secretSetting, error) {
	raw, err := q.SuperadminSetting(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) || len(raw) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s secretSetting
	if json.Unmarshal(raw, &s) != nil || s.Token == "" || h.d.Superadmin.Secrets == nil {
		return nil, nil
	}
	if _, err := h.d.Superadmin.Secrets.Open(s.Token); err != nil {
		return nil, nil
	}
	return &s, nil
}

func (h *handlers) writeSecret(ctx context.Context, q *store.Queries, key, token string, extra secretSetting) error {
	if h.d.Superadmin.Secrets == nil {
		return httpx.ErrNotConfigured
	}
	sealed, err := h.d.Superadmin.Secrets.Seal(token)
	if err != nil {
		return err
	}
	extra.Token = sealed
	raw, err := json.Marshal(extra)
	if err != nil {
		return err
	}
	now := h.d.NowFunc()
	uid := httpx.Principal(ctx).UserID
	return q.SuperadminPutSetting(ctx, store.SuperadminPutSettingParams{Key: key, Value: raw, UpdatedBy: &uid, Now: &now})
}

// SaTelegram — show(): только чат, никогда токен.
func (h *handlers) SaTelegram(ctx context.Context, _ superadminapi.SaTelegramRequestObject) (superadminapi.SaTelegramResponseObject, error) {
	var out superadminapi.TelegramState
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		gw, err := h.readSecret(ctx, q, settingGateway)
		if err != nil {
			return err
		}
		out.Configured = gw != nil
		al, err := h.readSecret(ctx, q, settingAlerts)
		if err != nil {
			return err
		}
		if al != nil {
			out.AlertsChatId = ptr(al.ChatID)
		}
		since := now.Add(-30 * 24 * time.Hour)
		out.SentLastMonth, err = q.SuperadminCountPlatformEventSince(ctx, store.SuperadminCountPlatformEventSinceParams{Event: "user.password_code_sent", Since: &since})
		return err
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaTelegram200JSONResponse(out), nil
}

// SaSetTelegramGateway — update().
func (h *handlers) SaSetTelegramGateway(ctx context.Context, req superadminapi.SaSetTelegramGatewayRequestObject) (superadminapi.SaSetTelegramGatewayResponseObject, error) {
	token := ""
	if req.Body != nil {
		token = strings.TrimSpace(req.Body.Token)
	}
	switch {
	case token == "":
		return nil, httpx.RuleError("token", "required")
	case len(token) > 255:
		return nil, httpx.RuleErrorWith("token", "max_string", "255")
	case !gatewayTokenRe.MatchString(token):
		return nil, httpx.ValidationMsg("token", "Это не похоже на токен Telegram Gateway: скопируйте его из настроек аккаунта gateway.telegram.org.", nil)
	}
	err := h.tx(ctx, func(q *store.Queries) error {
		if err := h.writeSecret(ctx, q, settingGateway, token, secretSetting{}); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.telegram_gateway_set", Key: "Подключён Telegram Gateway"}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "security", "telegram_gateway.set", "", 0, nil, map[string]any{"configured": true})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaSetTelegramGateway200JSONResponse(msg(h.d.T(ctx, "Telegram Gateway подключён.", nil))), nil
}

// SaClearTelegramGateway — destroy().
func (h *handlers) SaClearTelegramGateway(ctx context.Context, _ superadminapi.SaClearTelegramGatewayRequestObject) (superadminapi.SaClearTelegramGatewayResponseObject, error) {
	err := h.tx(ctx, func(q *store.Queries) error {
		if err := q.SuperadminDeleteSetting(ctx, settingGateway); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.telegram_gateway_cleared", Key: "Telegram Gateway отключён"}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "security", "telegram_gateway.cleared", "", 0, nil, map[string]any{"configured": false})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaClearTelegramGateway200JSONResponse(msg(h.d.T(ctx, "Telegram Gateway отключён.", nil))), nil
}

// SaSetTelegramAlerts — updateAlerts(): токен бота и чат (язык оповещений — язык суперадмина).
func (h *handlers) SaSetTelegramAlerts(ctx context.Context, req superadminapi.SaSetTelegramAlertsRequestObject) (superadminapi.SaSetTelegramAlertsResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("bot_token", "required")
	}
	var fe httpx.FormErrors
	token := strings.TrimSpace(req.Body.BotToken)
	switch {
	case token == "":
		fe.Add("bot_token", httpx.RuleMessage("bot_token", "required", ""))
	case len(token) > 255:
		fe.Add("bot_token", httpx.RuleMessage("bot_token", "max_string", "255"))
	case !botTokenRe.MatchString(token):
		fe.AddMsg("bot_token", "Это не похоже на токен бота: скопируйте его из сообщения @BotFather.", nil)
	}
	chat := ""
	if req.Body.ChatId != nil {
		chat = strings.TrimSpace(*req.Body.ChatId)
	}
	switch {
	case chat == "":
	case len(chat) > 64:
		fe.Add("chat_id", httpx.RuleMessage("chat_id", "max_string", "64"))
	case !chatIDRe.MatchString(chat):
		fe.AddMsg("chat_id", "ID чата — число (у групп со знаком минус) или @имя канала.", nil)
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	locale := h.d.Locale(ctx)
	err := h.tx(ctx, func(q *store.Queries) error {
		if err := h.writeSecret(ctx, q, settingAlerts, token, secretSetting{ChatID: chat, Locale: locale}); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.telegram_alerts_set", Key: "Подключён бот оповещений о сбоях"}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "security", "telegram_alerts.set", "", 0, nil, map[string]any{"chat_id": chat})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaSetTelegramAlerts200JSONResponse(msg(h.d.T(ctx, "Бот подключён: проверочное сообщение отправлено в чат.", nil))), nil
}

// SaTestTelegramAlerts — testAlerts(): отправка через Telegram — Этап 4; пока — ошибка отправки старого кода.
func (h *handlers) SaTestTelegramAlerts(ctx context.Context, _ superadminapi.SaTestTelegramAlertsRequestObject) (superadminapi.SaTestTelegramAlertsResponseObject, error) {
	return nil, httpx.ValidationMsg("bot_token", "Не удалось отправить сообщение: проверьте бота и чат.", nil)
}

// SaClearTelegramAlerts — destroyAlerts().
func (h *handlers) SaClearTelegramAlerts(ctx context.Context, _ superadminapi.SaClearTelegramAlertsRequestObject) (superadminapi.SaClearTelegramAlertsResponseObject, error) {
	err := h.tx(ctx, func(q *store.Queries) error {
		if err := q.SuperadminDeleteSetting(ctx, settingAlerts); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.telegram_alerts_cleared", Key: "Бот оповещений о сбоях отключён"}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "security", "telegram_alerts.cleared", "", 0, nil, map[string]any{"configured": false})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaClearTelegramAlerts200JSONResponse(msg(h.d.T(ctx, "Оповещения отключены.", nil))), nil
}
