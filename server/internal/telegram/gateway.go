package telegram

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// Тексты TelegramGateway::configure().
const (
	MsgGatewayUnreachable = "Не удалось связаться с Telegram Gateway. Попробуйте позже."
	MsgGatewayBadToken    = "Telegram Gateway не принял токен." // #nosec G101 -- текст для пользователя, не секрет
)

// Gateway — Telegram Gateway (gatewayapi.telegram.org): коды подтверждения на номер телефона в Telegram, без бота.
// Laravel: app/Services/TelegramGateway.php.
type Gateway struct {
	API   Caller
	Store *Store
	Log   *slog.Logger
}

func (g *Gateway) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.Default()
}

// Token — TelegramGateway::token(): "" — не настроено.
func (g *Gateway) Token(ctx context.Context) (string, error) {
	s, err := g.Store.Read(ctx, SettingGateway)
	if err != nil || s == nil {
		return "", err
	}
	return s.Token, nil
}

// Configured — TelegramGateway::configured().
func (g *Gateway) Configured(ctx context.Context) (bool, error) {
	t, err := g.Token(ctx)
	return t != "", err
}

// CheckToken — проверка токена перед сохранением (TelegramGateway::configure()): бесплатный запрос статуса.
// С верным токеном неизвестный request_id — REQUEST_ID_INVALID, с неверным — ACCESS_TOKEN_INVALID.
// nil — токен годится; *Error — текст для поля token.
func (g *Gateway) CheckToken(ctx context.Context, token string) error {
	resp, err := g.API.Call(ctx, token, "checkVerificationStatus", map[string]any{"request_id": "0"})
	if err != nil {
		return &Error{Key: MsgGatewayUnreachable}
	}
	if strings.Contains(resp.Error, "ACCESS_TOKEN") {
		return &Error{Key: MsgGatewayBadToken}
	}
	return nil
}

// SendCode — TelegramGateway::sendCode(): наш код (4–8 цифр) на номер в формате E.164; Gateway возвращает деньги,
// если сообщение не доставлено за ttl. false — не настроено или Telegram не принял (текст ошибки — только в лог,
// без номера и токена). Ошибка — только ошибка чтения настроек.
func (g *Gateway) SendCode(ctx context.Context, phone, code string, ttl time.Duration) (bool, error) {
	token, err := g.Token(ctx)
	if err != nil || token == "" {
		return false, err
	}
	resp, err := g.API.Call(ctx, token, "sendVerificationMessage", map[string]any{
		"phone_number": phone, "code": code, "ttl": int64(ttl / time.Second),
	})
	if err != nil {
		g.log().WarnContext(ctx, "Telegram Gateway did not send a code", "error", "no response")
		return false, nil
	}
	if !resp.OK {
		g.log().WarnContext(ctx, "Telegram Gateway did not send a code", "error", resp.Error)
	}
	return resp.OK, nil
}
