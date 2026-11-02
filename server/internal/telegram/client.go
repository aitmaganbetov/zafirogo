// Package telegram — исходящие вызовы Telegram: Telegram Gateway (коды подтверждения на номер телефона, без бота)
// и Bot API (оповещения платформы о сбоях). Сервер только подключается наружу.
//
// Laravel: app/Services/TelegramGateway.php (Gateway), app/Services/Monitoring/TelegramAlerts.php (бот).
// Токены задаются в кабинете суперадмина, хранятся зашифрованными (secretbox, как Crypt::encryptString), не
// кэшируются и не попадают в лог: при ошибке связи в лог пишется только метод и класс ошибки.
//
// Внешние вызовы — только через интерфейс Caller (тесты подставляют httptest-сервер: telegramtest).
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Адреса API (как в старом коде).
const (
	DefaultGatewayURL = "https://gatewayapi.telegram.org/" // TelegramGateway::URL
	DefaultBotURL     = "https://api.telegram.org/"        // 'https://api.telegram.org/bot'.$token.'/'.$method
	// Timeout — Http::timeout(10) старого кода.
	Timeout = 10 * time.Second
	// maxBody — ответ Telegram больше не бывает; лишнее не читаем.
	maxBody = 1 << 20
)

// ErrUnreachable — Telegram не ответил JSON-объектом (сеть, таймаут, не JSON): в старом коде call() возвращал [].
var ErrUnreachable = errors.New("telegram: no response")

// Response — ответ Gateway или Bot API: {ok, result, error (Gateway), description (Bot API)}.
type Response struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	Description string          `json:"description,omitempty"`
}

// Caller — вызов метода API с токеном. Реализации: GatewayClient, BotClient (и подмены в тестах).
type Caller interface {
	Call(ctx context.Context, token, method string, payload any) (*Response, error)
}

// GatewayClient — Telegram Gateway: POST <URL><method>, токен в Authorization: Bearer.
type GatewayClient struct {
	// URL — адрес API (пусто — DefaultGatewayURL).
	URL string
	// HTTP — клиент (nil — с таймаутом Timeout).
	HTTP *http.Client
	Log  *slog.Logger
}

// Call — TelegramGateway::call(): Http::withToken($token)->timeout(10)->asJson()->post(URL.$method, $payload).
func (c *GatewayClient) Call(ctx context.Context, token, method string, payload any) (*Response, error) {
	base := c.URL
	if base == "" {
		base = DefaultGatewayURL
	}
	return call(ctx, c.HTTP, c.Log, "Telegram Gateway request failed", withSlash(base)+method, token, method, payload)
}

// BotClient — Bot API: POST <URL>bot<token>/<method> (токен — часть адреса).
type BotClient struct {
	// URL — адрес API (пусто — DefaultBotURL).
	URL  string
	HTTP *http.Client
	Log  *slog.Logger
}

// Call — TelegramAlerts::call(): Http::timeout(10)->asJson()->post('https://api.telegram.org/bot'.$token.'/'.$method).
func (c *BotClient) Call(ctx context.Context, token, method string, payload any) (*Response, error) {
	base := c.URL
	if base == "" {
		base = DefaultBotURL
	}
	return call(ctx, c.HTTP, c.Log, "Telegram request failed", withSlash(base)+"bot"+token+"/"+method, "", method, payload)
}

func withSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return s
	}
	return s + "/"
}

func call(ctx context.Context, hc *http.Client, log *slog.Logger, failMsg, url, bearer, method string, payload any) (*Response, error) {
	if hc == nil {
		hc = &http.Client{Timeout: Timeout}
	}
	if log == nil {
		log = slog.Default()
	}
	if payload == nil {
		payload = map[string]any{}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		// Адрес бота содержит токен: в лог — только метод.
		log.WarnContext(ctx, failMsg, "method", method, "error", "request")
		return nil, ErrUnreachable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := hc.Do(req) // #nosec G704 -- адрес — константа API Telegram (или адрес теста), не ввод пользователя
	if err != nil {
		// Ни токен, ни номер телефона в лог не попадают (url.Error содержит адрес с токеном бота).
		log.WarnContext(ctx, failMsg, "method", method, "error", errClass(err))
		return nil, ErrUnreachable
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		log.WarnContext(ctx, failMsg, "method", method, "error", "read")
		return nil, ErrUnreachable
	}
	// is_array($response->json()) ? $response->json() : [] — ответ с кодом 4xx тоже разбирается (ok: false).
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return nil, ErrUnreachable
	}
	return &out, nil
}

// errClass — class_basename($exception): вид ошибки без адреса.
func errClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "connection"
	}
}
