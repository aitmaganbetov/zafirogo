package telegram_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/telegram"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram/telegramtest"
)

const (
	gatewayToken = "AAGexampleGatewayToken_1234567890"
	botToken     = "123456:ABCdefGhIJKlmNoPQRsTUVwxyZ0123456789"
)

func TestGatewayClient_BearerAndJSON(t *testing.T) {
	tg := telegramtest.New(t)
	c := &telegram.GatewayClient{URL: tg.GatewayURL()}
	resp, err := c.Call(context.Background(), gatewayToken, "sendVerificationMessage", map[string]any{"phone_number": "+77001112233", "code": "123456", "ttl": 300})
	if err != nil || !resp.OK {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	r := tg.Requests()[0]
	if r.Auth != "Bearer "+gatewayToken || r.Method != "sendVerificationMessage" || r.Body["phone_number"] != "+77001112233" || r.Body["ttl"] != float64(300) {
		t.Fatalf("request = %+v", r)
	}
}

func TestBotClient_TokenInPath(t *testing.T) {
	tg := telegramtest.New(t)
	c := &telegram.BotClient{URL: tg.BotURL()}
	if _, err := c.Call(context.Background(), botToken, "getMe", nil); err != nil {
		t.Fatal(err)
	}
	if r := tg.Requests()[0]; r.Token != botToken || r.Method != "getMe" || r.Auth != "" {
		t.Fatalf("request = %+v", r)
	}
}

func TestClient_UnreachableAndNotJSON_TokenNotLogged(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	tg := telegramtest.New(t)
	tg.OnBot(func(telegramtest.Request) telegramtest.Reply { return telegramtest.Reply{Status: 502} })
	c := &telegram.BotClient{URL: tg.BotURL(), Log: log}
	if _, err := c.Call(context.Background(), botToken, "getMe", nil); !errors.Is(err, telegram.ErrUnreachable) {
		t.Fatalf("not json: err = %v", err)
	}
	// Закрытый порт: ошибка связи, в логе нет токена (url.Error содержит адрес с токеном бота).
	tg.Close()
	c = &telegram.BotClient{URL: tg.BotURL(), Log: log, HTTP: &http.Client{Timeout: time.Second}}
	if _, err := c.Call(context.Background(), botToken, "getMe", nil); !errors.Is(err, telegram.ErrUnreachable) {
		t.Fatalf("closed: err = %v", err)
	}
	if strings.Contains(logs.String(), botToken) || !strings.Contains(logs.String(), "Telegram request failed") {
		t.Fatalf("log = %s", logs.String())
	}
}

// GS-241 (часть без базы): ACCESS_TOKEN_INVALID — ошибка, REQUEST_ID_INVALID — токен верный.
func TestGateway_CheckToken(t *testing.T) {
	tg := telegramtest.New(t)
	g := &telegram.Gateway{API: &telegram.GatewayClient{URL: tg.GatewayURL()}}
	if err := g.CheckToken(context.Background(), gatewayToken); err != nil {
		t.Fatalf("valid token: %v", err)
	}
	if r := tg.Requests()[0]; r.Method != "checkVerificationStatus" || r.Body["request_id"] != "0" {
		t.Fatalf("request = %+v", r)
	}
	tg.OnGateway(func(telegramtest.Request) telegramtest.Reply {
		return telegramtest.Reply{Status: 401, Body: map[string]any{"ok": false, "error": "ACCESS_TOKEN_INVALID"}}
	})
	var te *telegram.Error
	if err := g.CheckToken(context.Background(), gatewayToken); !errors.As(err, &te) || te.Key != telegram.MsgGatewayBadToken {
		t.Fatalf("bad token: %v", err)
	}
	tg.OnGateway(func(telegramtest.Request) telegramtest.Reply { return telegramtest.Reply{} })
	if err := g.CheckToken(context.Background(), gatewayToken); !errors.As(err, &te) || te.Key != telegram.MsgGatewayUnreachable {
		t.Fatalf("unreachable: %v", err)
	}
}

// GS-398, GS-399 (часть без базы): чат — последний, кто писал боту; никто не писал — просьба /start.
func TestAlerts_Configure(t *testing.T) {
	tg := telegramtest.New(t)
	a := &telegram.Alerts{API: &telegram.BotClient{URL: tg.BotURL()}}
	ctx := context.Background()
	var te *telegram.Error
	if _, err := a.Configure(ctx, botToken, "", "test"); !errors.As(err, &te) || te.Key != telegram.MsgBotNoChat {
		t.Fatalf("no chat: %v", err)
	}
	tg.OnBot(func(r telegramtest.Request) telegramtest.Reply {
		if r.Method == "getUpdates" {
			return telegramtest.Reply{Body: map[string]any{"ok": true, "result": []any{
				map[string]any{"update_id": 1, "message": map[string]any{"chat": map[string]any{"id": 42}}},
				map[string]any{"update_id": 2, "my_chat_member": map[string]any{"chat": map[string]any{"id": -1001234567890}}},
			}}}
		}
		return telegramtest.Reply{Body: map[string]any{"ok": true}}
	})
	chat, err := a.Configure(ctx, botToken, "", "Оповещения подключены")
	if err != nil || chat != "-1001234567890" {
		t.Fatalf("chat = %q, %v", chat, err)
	}
	if m := tg.Messages(); len(m) != 1 || m[0] != "Оповещения подключены" {
		t.Fatalf("messages = %v", m)
	}
	last := tg.Requests()[len(tg.Requests())-1]
	if last.Body["chat_id"] != "-1001234567890" || last.Body["disable_web_page_preview"] != true {
		t.Fatalf("sendMessage = %+v", last.Body)
	}

	tg.OnBot(func(r telegramtest.Request) telegramtest.Reply {
		if r.Method == "sendMessage" {
			return telegramtest.Reply{Status: 400, Body: map[string]any{"ok": false, "description": "Bad Request: chat not found"}}
		}
		return telegramtest.Reply{Body: map[string]any{"ok": true}}
	})
	if _, err := a.Configure(ctx, botToken, "@zafiro_alerts", "x"); !errors.As(err, &te) || te.Key != telegram.MsgBotChatFailed || te.Params["chat"] != "@zafiro_alerts" {
		t.Fatalf("chat failed: %v", err)
	}
	tg.OnBot(func(telegramtest.Request) telegramtest.Reply {
		return telegramtest.Reply{Status: 401, Body: map[string]any{"ok": false, "description": "Unauthorized"}}
	})
	if _, err := a.Configure(ctx, botToken, "42", "x"); !errors.As(err, &te) || te.Key != telegram.MsgBotBadToken {
		t.Fatalf("bad token: %v", err)
	}
	tg.OnBot(func(telegramtest.Request) telegramtest.Reply { return telegramtest.Reply{} })
	if _, err := a.Configure(ctx, botToken, "42", "x"); !errors.As(err, &te) || te.Key != telegram.MsgBotUnreachable {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestError_Text(t *testing.T) {
	e := &telegram.Error{Key: telegram.MsgBotChatFailed, Params: map[string]string{"chat": "42"}}
	if e.Error() != "Не удалось отправить сообщение в чат 42. Проверьте, что бот добавлен в чат." {
		t.Fatal(e.Error())
	}
}
