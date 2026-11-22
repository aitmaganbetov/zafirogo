package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
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

// Тексты TelegramAlerts::clubRegistered().
const (
	MsgNewClub      = "Новый клуб: :club"
	MsgNewClubOwner = "Владелец: :owner, :phone"
	MsgNewClubTrial = "Пробный период до :date"
)

// NewClub — клуб, только что зарегистрированный на сайте.
type NewClub struct {
	Name       string
	OwnerName  string
	OwnerPhone string
	// TrialEndsAt — конец пробного периода (нулевое время — «—»).
	TrialEndsAt time.Time
	// URL — страница клуба в кабинете суперадмина (route('superadmin.clubs.show', $club)); пусто — без ссылки.
	URL string
}

// ClubRegistered сообщает в чат оповещений платформы о клубе, зарегистрированном на сайте, на языке оповещений:
//
//	🎉 Новый клуб: :club
//	Владелец: :owner, :phone
//	Пробный период до :date        // trial_ends_at d.m.Y или «—»
//	<ссылка на клуб в кабинете>
//
// Бот не настроен — ничего не отправляется (false). Дата — по UTC: в старом коде подписка читалась в
// CurrentClub::timezone() только что созданного клуба, у модели которого часового пояса ещё нет → config('app.timezone').
// Laravel: app/Services/Monitoring/TelegramAlerts.php clubRegistered() (Zafiro e872af0).
func (a *Alerts) ClubRegistered(ctx context.Context, c NewClub) (bool, error) {
	s, err := a.Settings(ctx)
	if err != nil || s == nil {
		return false, err
	}
	locale := s.Locale
	if locale == "" {
		locale = i18n.SourceLocale
	}
	t := func(key string, params map[string]string) string {
		if a.I18n != nil {
			return a.I18n.Translate(ctx, locale, key, params)
		}
		return i18n.Render(i18n.SourceLocale, key, params)
	}
	date := "—"
	if !c.TrialEndsAt.IsZero() {
		date = c.TrialEndsAt.UTC().Format("02.01.2006")
	}
	text := withLink(strings.Join([]string{
		"🎉 " + t(MsgNewClub, map[string]string{"club": c.Name}),
		t(MsgNewClubOwner, map[string]string{"owner": c.OwnerName, "phone": c.OwnerPhone}),
		t(MsgNewClubTrial, map[string]string{"date": date}),
	}, "\n"), c.URL)
	return a.deliver(ctx, s.Token, s.ChatID, text), nil
}

// Тексты TelegramAlerts::invoiceOpened().
const (
	MsgInvoice       = "Счёт :number: :club"
	MsgInvoiceTerms  = ":plan, :months мес., :computers ПК"
	MsgInvoiceAmount = "К оплате :amount ₸"
)

// OpenedInvoice — счёт за подписку, который клуб только что выставил и собирается оплатить через Kaspi.
type OpenedInvoice struct {
	// Number — номер счёта («ZF-00042»).
	Number    string
	Club      string
	Plan      string
	Months    int
	Computers int64
	// AmountTenge — сумма счёта в целых тенге (сумма счёта всегда целые тенге, BR-180).
	AmountTenge int64
	// URL — страница «Оплаты» кабинета суперадмина (route('superadmin.invoices.index')); пусто — без ссылки.
	URL string
}

// InvoiceOpened сообщает в чат оповещений платформы о выставленном счёте, на языке оповещений:
//
//	💳 Счёт :number: :club
//	:plan, :months мес., :computers ПК
//	К оплате :amount ₸               // number_format($amount, 0, '.', ' ')
//	<ссылка на «Оплаты» в кабинете>
//
// Бот не настроен — ничего не отправляется (false).
// Laravel: app/Services/Monitoring/TelegramAlerts.php invoiceOpened() (Zafiro 77ae201).
func (a *Alerts) InvoiceOpened(ctx context.Context, inv OpenedInvoice) (bool, error) {
	s, err := a.Settings(ctx)
	if err != nil || s == nil {
		return false, err
	}
	locale := s.Locale
	if locale == "" {
		locale = i18n.SourceLocale
	}
	t := func(key string, params map[string]string) string {
		if a.I18n != nil {
			return a.I18n.Translate(ctx, locale, key, params)
		}
		return i18n.Render(i18n.SourceLocale, key, params)
	}
	text := withLink(strings.Join([]string{
		"💳 " + t(MsgInvoice, map[string]string{"number": inv.Number, "club": inv.Club}),
		t(MsgInvoiceTerms, map[string]string{"plan": inv.Plan, "months": strconv.Itoa(inv.Months), "computers": strconv.FormatInt(inv.Computers, 10)}),
		t(MsgInvoiceAmount, map[string]string{"amount": money.GroupThousands(inv.AmountTenge, " ")}),
	}, "\n"), inv.URL)
	return a.deliver(ctx, s.Token, s.ChatID, text), nil
}

// withLink — текст оповещения со ссылкой последней строкой; адрес кабинета не настроен (url пуст) — без неё.
func withLink(text, url string) string {
	if url == "" {
		return text
	}
	return text + "\n" + url
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
