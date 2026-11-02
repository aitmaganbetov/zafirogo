package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

// Тексты TelegramAlerts::configure() и TelegramController::testAlerts().
const (
	MsgBotUnreachable = "Не удалось связаться с Telegram. Попробуйте позже."
	MsgBotBadToken    = "Telegram не принял токен бота." // #nosec G101 -- текст для пользователя, не секрет
	MsgBotNoChat      = "Бот пока никому не писал: отправьте боту /start (или добавьте его в группу) и сохраните ещё раз."
	MsgBotChatFailed  = "Не удалось отправить сообщение в чат :chat. Проверьте, что бот добавлен в чат."
	MsgBotConnected   = "Оповещения Zafiro подключены: сюда будут приходить сообщения о сбоях сервера."
	MsgBotTest        = "Проверка: оповещения Zafiro работают."
)

// Translator — перевод текста на язык (i18n.Catalog.Translate).
type Translator interface {
	Translate(ctx context.Context, locale, key string, params map[string]string) string
}

// Alerts — оповещения о сбоях в чат Telegram через бота (Bot API): Gateway умеет только коды, поэтому у
// оповещений свой бот. Laravel: app/Services/Monitoring/TelegramAlerts.php.
type Alerts struct {
	API   Caller
	Store *Store
	// I18n — перевод оповещений на язык суперадмина, подключившего бота (nil — русский текст).
	I18n Translator
	Log  *slog.Logger
}

func (a *Alerts) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// Settings — TelegramAlerts::settings(): nil — не настроено.
func (a *Alerts) Settings(ctx context.Context) (*Secret, error) {
	s, err := a.Store.Read(ctx, SettingAlerts)
	if err != nil || s == nil || s.ChatID == "" {
		return nil, err
	}
	return s, nil
}

// Configure — TelegramAlerts::configure(): проверка токена (getMe), чат — последний, кто писал боту (getUpdates),
// если не задан, проверочное сообщение testText. Возвращает чат для сохранения; *Error — текст для поля bot_token.
// Сохраняет настройку вызывающий (кабинет суперадмина).
func (a *Alerts) Configure(ctx context.Context, token, chatID, testText string) (string, error) {
	me, err := a.API.Call(ctx, token, "getMe", nil)
	if err != nil {
		return "", &Error{Key: MsgBotUnreachable}
	}
	if !me.OK {
		return "", &Error{Key: MsgBotBadToken}
	}
	if chatID == "" {
		chatID = lastChat(a.updates(ctx, token))
		if chatID == "" {
			return "", &Error{Key: MsgBotNoChat}
		}
	}
	if !a.deliver(ctx, token, chatID, testText) {
		return "", &Error{Key: MsgBotChatFailed, Params: map[string]string{"chat": chatID}}
	}
	return chatID, nil
}

func (a *Alerts) updates(ctx context.Context, token string) json.RawMessage {
	resp, err := a.API.Call(ctx, token, "getUpdates", nil)
	if err != nil {
		return nil
	}
	return resp.Result
}

// lastChat — collect($updates['result'])->map(message.chat.id ?? my_chat_member.chat.id)->filter()->last().
func lastChat(result json.RawMessage) string {
	type chat struct {
		Chat struct {
			ID json.Number `json:"id"`
		} `json:"chat"`
	}
	var updates []struct {
		Message      *chat `json:"message"`
		MyChatMember *chat `json:"my_chat_member"`
	}
	d := json.NewDecoder(bytes.NewReader(result))
	d.UseNumber()
	if len(result) == 0 || d.Decode(&updates) != nil {
		return ""
	}
	last := ""
	for _, u := range updates {
		id := ""
		if u.Message != nil {
			id = u.Message.Chat.ID.String()
		}
		if id == "" && u.MyChatMember != nil {
			id = u.MyChatMember.Chat.ID.String()
		}
		if id != "" && id != "0" {
			last = id
		}
	}
	return last
}

// Send — TelegramAlerts::send(): текст в настроенный чат; false — не настроено или не отправлено.
func (a *Alerts) Send(ctx context.Context, text string) (bool, error) {
	s, err := a.Settings(ctx)
	if err != nil || s == nil {
		return false, err
	}
	return a.deliver(ctx, s.Token, s.ChatID, text), nil
}

// SendKey — оповещение на языке оповещений (Alerts::update(): app()->setLocale($telegram->locale())): prefix + перевод.
func (a *Alerts) SendKey(ctx context.Context, prefix, key string, params map[string]string) (bool, error) {
	s, err := a.Settings(ctx)
	if err != nil || s == nil {
		return false, err
	}
	locale := s.Locale
	if locale == "" {
		locale = i18n.SourceLocale // config('app.locale')
	}
	var text string
	if a.I18n != nil {
		text = a.I18n.Translate(ctx, locale, key, params)
	} else {
		text = i18n.Render(i18n.SourceLocale, key, params)
	}
	return a.deliver(ctx, s.Token, s.ChatID, prefix+text), nil
}

// deliver — sendMessage без превью ссылок; описание ошибки Telegram — в лог (без токена).
func (a *Alerts) deliver(ctx context.Context, token, chatID, text string) bool {
	resp, err := a.API.Call(ctx, token, "sendMessage", map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true})
	if err != nil {
		a.log().WarnContext(ctx, "Telegram alert was not sent", "error", "no response")
		return false
	}
	if !resp.OK {
		a.log().WarnContext(ctx, "Telegram alert was not sent", "error", resp.Description)
	}
	return resp.OK
}
