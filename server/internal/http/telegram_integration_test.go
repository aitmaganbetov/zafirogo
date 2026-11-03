//go:build integration

package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram/telegramtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/verifycode"
)

const (
	tgGatewayToken = "AAGexampleGatewayToken_1234567890"
	tgBotToken     = "123456:ABCdefGhIJKlmNoPQRsTUVwxyZ0123456789"
)

// newTelegramEnv — сервер с кабинетом суперадмина, восстановлением пароля и поддельным Telegram.
func newTelegramEnv(t *testing.T) (*adminEnv, *telegramtest.Server, *[]int64) {
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
	lim, err := ratelimit.New(ratelimit.Options{Client: vk, Prefix: fmt.Sprintf("tg%d:", adminSeq.Add(1))})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetSecondFactor(testSecondFactor(t, d.Platform, vk, lim))
	box, _ := secretbox.New(strings.Repeat("s", 32))
	tg := telegramtest.New(t)
	st := &telegram.Store{Pool: d.Platform, Box: box}
	gw := &telegram.Gateway{API: &telegram.GatewayClient{URL: tg.GatewayURL()}, Store: st}
	signedOut := &[]int64{}
	rec := &verifycode.PasswordRecovery{Platform: d.Platform, Gateway: gw, Hasher: h,
		SignOut: func(ctx context.Context, id int64) { *signedOut = append(*signedOut, id); _ = sm.RevokeUser(ctx, id) }}
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim,
		Pool: d.App, PlatformPool: d.Platform, Presence: &fakePresence{states: map[int64][]presence.State{}},
		PasswordRecovery: rec,
		Superadmin: httpx.SuperadminDeps{Secrets: box, TelegramGateway: gw,
			TelegramAlerts: &telegram.Alerts{API: &telegram.BotClient{URL: tg.BotURL()}, Store: st}},
	}))
	t.Cleanup(srv.Close)
	for _, k := range []string{telegram.SettingGateway, telegram.SettingAlerts} {
		if _, err := d.Owner.Exec(context.Background(), "DELETE FROM platform_settings WHERE key = $1", k); err != nil {
			t.Fatal(err)
		}
	}
	return &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("tg%d", adminSeq.Add(1)))}, tg, signedOut
}

func (e *adminEnv) rawSetting(key string) string {
	e.t.Helper()
	var v string
	if err := e.db.Owner.QueryRow(context.Background(), "SELECT value::text FROM platform_settings WHERE key = $1", key).Scan(&v); err != nil {
		return ""
	}
	return v
}

func wantField(t *testing.T, r resp, status int, field, text string) {
	t.Helper()
	errs, _ := r.body["errors"].(map[string]any)
	got, _ := errs[field].([]any)
	if r.status != status || len(got) == 0 || (text != "" && got[0] != text) {
		t.Fatalf("status %d, errors %v; want %d %s %q", r.status, r.body, status, field, text)
	}
}

// GS-237, GS-240, GS-241: суперадмин подключает Gateway (токен проверяется и хранится зашифрованным); сайт: номер,
// затем код и новый пароль, затем вход; выход везде.
func TestTelegram_WebPasswordRecoveryAndGateway(t *testing.T) {
	e, tg, signedOut := newTelegramEnv(t)
	_, saPhone := e.member("Супер Админ", "", "", true)
	sa := e.login(saPhone)
	uid, phone := e.member("Оператор", "operator", "", false)
	local := phone[2:]

	if r := do(t, "GET", e.srv.URL+"/api/forgot-password", nil, nil); r.status != 200 || r.body["available"] != false {
		t.Fatalf("available: %d %v", r.status, r.body)
	}
	wantField(t, do(t, "POST", e.srv.URL+"/api/forgot-password", map[string]any{"phone": local}, nil), 422, "phone",
		verifycode.MsgRecoveryUnavailable)

	// Gateway: ACCESS_TOKEN_INVALID — ошибка token, не сохранено; REQUEST_ID_INVALID — сохранено зашифрованным.
	tg.OnGateway(func(telegramtest.Request) telegramtest.Reply {
		return telegramtest.Reply{Status: 401, Body: map[string]any{"ok": false, "error": "ACCESS_TOKEN_INVALID"}}
	})
	wantField(t, e.send6("POST", sa, "/api/superadmin/telegram", map[string]any{"token": tgGatewayToken}), 422, "token", telegram.MsgGatewayBadToken)
	if e.rawSetting(telegram.SettingGateway) != "" {
		t.Fatal("bad token saved")
	}
	tg.OnGateway(nil)
	if r := e.send6("POST", sa, "/api/superadmin/telegram", map[string]any{"token": tgGatewayToken}); r.status != 200 {
		t.Fatalf("set gateway: %d %v", r.status, r.body)
	}
	if v := e.rawSetting(telegram.SettingGateway); v == "" || strings.Contains(v, tgGatewayToken) {
		t.Fatalf("stored = %s", v)
	}
	if r := e.send6("GET", sa, "/api/superadmin/telegram", nil); r.body["configured"] != true {
		t.Fatalf("telegram: %v", r.body)
	}

	// Сайт: номер → код в Telegram.
	if r := do(t, "GET", e.srv.URL+"/api/forgot-password", nil, nil); r.body["available"] != true {
		t.Fatalf("available: %v", r.body)
	}
	wantField(t, do(t, "POST", e.srv.URL+"/api/forgot-password", map[string]any{"phone": "12"}, nil), 422, "phone", "Введите корректный номер телефона.")
	r := do(t, "POST", e.srv.URL+"/api/forgot-password", map[string]any{"phone": local[:3] + " " + local[3:6] + "-" + local[6:8] + "-" + local[8:]}, nil)
	if r.status != 200 || r.body["phone"] != phone || r.body["code_minutes"] != float64(5) ||
		r.body["message"] != "Если номер зарегистрирован в Zafiro, код придёт в Telegram на этот номер. Код действует 5 мин." {
		t.Fatalf("send: %d %v", r.status, r.body)
	}
	code := tg.Codes()[len(tg.Codes())-1]
	reset := func(code, pw, confirm string) resp {
		return do(t, "POST", e.srv.URL+"/api/reset-password", map[string]any{"phone": phone, "code": code, "password": pw, "password_confirmation": confirm}, nil)
	}
	wantField(t, reset("12", "new-secret", "new-secret"), 422, "code", "Код — 6 цифр из сообщения в Telegram.")
	wantField(t, reset(code, "new-secret", "other"), 422, "password", "Пароли не совпадают.")
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	wantField(t, reset(wrong, "new-secret", "new-secret"), 422, "code", verifycode.MsgCodeWrong)
	before := e.login(phone) // сеанс до смены пароля
	if r := reset(code, "new-secret", "new-secret"); r.status != 200 || r.body["message"] != verifycode.MsgPasswordChanged {
		t.Fatalf("reset: %d %v", r.status, r.body)
	}
	if len(*signedOut) != 1 || (*signedOut)[0] != uid {
		t.Fatalf("signed out = %v", *signedOut)
	}
	if r := do(t, "GET", e.srv.URL+"/api/auth/me", nil, bearer(before)); r.status != http.StatusUnauthorized {
		t.Fatalf("old session: %d", r.status)
	}
	if r := do(t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": phone, "password": "new-secret"}, nil); r.status != 200 {
		t.Fatalf("login with new password: %d %v", r.status, r.body)
	}
	if r := do(t, "POST", e.srv.URL+"/api/auth/login", map[string]any{"phone": phone, "password": "pw"}, nil); r.status == 200 {
		t.Fatal("old password works")
	}
	var logs int64
	_ = e.db.Owner.QueryRow(context.Background(), "SELECT count(*) FROM platform_logs WHERE user_id = $1 AND event IN ('user.password_code_sent', 'user.password_recovered')", uid).Scan(&logs)
	if logs != 2 {
		t.Fatalf("platform logs = %d", logs)
	}

	if r := e.send6("DELETE", sa, "/api/superadmin/telegram", nil); r.status != 200 {
		t.Fatalf("clear gateway: %d", r.status)
	}
	if r := do(t, "GET", e.srv.URL+"/api/forgot-password", nil, nil); r.body["available"] != false {
		t.Fatalf("available after clear: %v", r.body)
	}
}

// GS-398, GS-399: бот оповещений — без /start ошибка; чат из getUpdates, проверочное сообщение, токен зашифрован;
// «Отправить проверочное сообщение» — реальная отправка; после отключения — ошибка.
func TestTelegram_AlertsBot(t *testing.T) {
	e, tg, _ := newTelegramEnv(t)
	_, saPhone := e.member("Супер Админ", "", "", true)
	sa := e.login(saPhone)

	wantField(t, e.send6("POST", sa, "/api/superadmin/telegram/alerts", map[string]any{"bot_token": tgBotToken}), 422, "bot_token", telegram.MsgBotNoChat)
	if e.rawSetting(telegram.SettingAlerts) != "" {
		t.Fatal("saved without chat")
	}
	tg.OnBot(func(r telegramtest.Request) telegramtest.Reply {
		if r.Method == "getUpdates" {
			return telegramtest.Reply{Body: map[string]any{"ok": true, "result": []any{
				map[string]any{"update_id": 1, "message": map[string]any{"chat": map[string]any{"id": -100500}}}}}}
		}
		return telegramtest.Reply{Body: map[string]any{"ok": true}}
	})
	if r := e.send6("POST", sa, "/api/superadmin/telegram/alerts", map[string]any{"bot_token": tgBotToken}); r.status != 200 {
		t.Fatalf("set alerts: %d %v", r.status, r.body)
	}
	if v := e.rawSetting(telegram.SettingAlerts); !strings.Contains(v, "-100500") || strings.Contains(v, tgBotToken) {
		t.Fatalf("stored = %s", v)
	}
	if r := e.send6("GET", sa, "/api/superadmin/telegram", nil); r.body["alerts_chat_id"] != "-100500" || strings.Contains(fmt.Sprint(r.body), tgBotToken) {
		t.Fatalf("telegram: %v", r.body)
	}
	if r := e.send6("POST", sa, "/api/superadmin/telegram/alerts/test", nil); r.status != 200 || r.body["message"] != "Сообщение отправлено." {
		t.Fatalf("test: %d %v", r.status, r.body)
	}
	msgs := tg.Messages()
	if len(msgs) != 2 || msgs[0] != telegram.MsgBotConnected || msgs[1] != telegram.MsgBotTest {
		t.Fatalf("messages = %q", msgs)
	}
	if r := e.send6("DELETE", sa, "/api/superadmin/telegram/alerts", nil); r.status != 200 {
		t.Fatalf("clear: %d", r.status)
	}
	wantField(t, e.send6("POST", sa, "/api/superadmin/telegram/alerts/test", nil), 422, "bot_token", "Не удалось отправить сообщение: проверьте бота и чат.")
}
