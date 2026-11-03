package activity

// Журнал платформы (platform_logs): действия суперадминов и самой платформы (планировщик). Решение PD-15:
// пишется, как в старом. Laravel: app/Services/PlatformLogger.php PlatformLogger::record(), app/Models/PlatformLog.php.
//
// Отличия от журнала клуба: user_id и club_id необязательны (NULL — «Система» / не про клуб), IP без запроса —
// '127.0.0.1' (как request()->ip() ?? '127.0.0.1' в консольной команде), описание обрезается до 500 символов.
// Описание — русский текст с подстановками, ключ и параметры — в details._t (W-3), как у журнала клуба.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// PlatformEntry — одна запись журнала платформы.
type PlatformEntry struct {
	// Event — код события (PlatformLog::EVENT_LABELS).
	Event string
	// Key и Params — текст описания (ключ перевода в формате Laravel и подстановки).
	Key    string
	Params map[string]string
	// UserID — кто сделал (0 — платформа сама: планировщик).
	UserID int64
	// ClubID — клуб, о котором запись (0 — не про клуб).
	ClubID int64
	// SubjectType/SubjectID — предмет (морф-класс Laravel, PD-14).
	SubjectType string
	SubjectID   int64
	// Details — подробности (секреты вычищаются Redact).
	Details map[string]any
	// Meta — данные запроса; nil — из контекста; без запроса — IP 127.0.0.1.
	Meta *RequestMeta
	// At — время (нулевое — time.Now()).
	At time.Time
}

// PlatformEventLabels — PlatformLog::EVENT_LABELS (подписи событий — ключи перевода).
var PlatformEventLabels = []struct{ Event, Label string }{
	{"superadmin.club_viewed", "Просмотр клуба"},
	{"superadmin.subscription_extended", "Продление подписки"},
	{"superadmin.subscription_suspended", "Приостановка подписки"},
	{"superadmin.subscription_plan_changed", "Смена тарифа клуба"},
	{"superadmin.plan_created", "Создание тарифа платформы"},
	{"superadmin.plan_updated", "Изменение тарифа платформы"},
	{"superadmin.telegram_set", "Подключение Telegram-бота"},
	{"superadmin.telegram_cleared", "Отключение Telegram-бота"},
	{"superadmin.telegram_gateway_set", "Подключение Telegram Gateway"},
	{"superadmin.telegram_gateway_cleared", "Отключение Telegram Gateway"},
	{"user.password_code_sent", "Код восстановления пароля"},
	{"user.password_recovered", "Восстановление пароля"},
	{"superadmin.news_created", "Добавление новости Zafiro"},
	{"superadmin.news_updated", "Изменение новости Zafiro"},
	{"superadmin.news_deleted", "Удаление новости Zafiro"},
	{"superadmin.user_blocked", "Блокировка пользователя"},
	{"superadmin.user_unblocked", "Разблокировка пользователя"},
	{"superadmin.granted", "Выдача прав суперадмина"},
	{"superadmin.revoked", "Снятие прав суперадмина"},
	{"superadmin.login", "Вход суперадмина"},
	{"subscription.expired", "Окончание оплаченной подписки"},
	{"superadmin.agent_release_set", "Объявление версии агента"},
	{"superadmin.agent_release_cleared", "Снятие объявления о версии агента"},
	{"language.created", "Добавление языка"},
	{"language.updated", "Изменение языка"},
	{"language.deleted", "Удаление языка"},
	{"translations.updated", "Изменение переводов"},
	{"translations.imported", "Импорт переводов"},
	// Второй фактор суперадмина (Этап 7, новая функция; события пишет server/internal/auth/mfa).
	{"superadmin.totp_enabled", "Включение двухфакторной аутентификации"},
	{"superadmin.totp_failed", "Неверный код двухфакторной аутентификации"},
	{"superadmin.totp_recovery_used", "Вход по коду восстановления"},
	{"superadmin.totp_reset", "Сброс двухфакторной аутентификации"},
}

// PlatformLabel — подпись события журнала платформы (ключ перевода); ok = false — событие без подписи.
func PlatformLabel(event string) (string, bool) {
	for _, l := range PlatformEventLabels {
		if l.Event == event {
			return l.Label, true
		}
	}
	return "", false
}

// RecordPlatform пишет запись в platform_logs в транзакции q (пул zafiro_platform или владелец схемы).
func RecordPlatform(ctx context.Context, q *store.Queries, e PlatformEntry) (int64, error) {
	if e.Event == "" || e.Key == "" || utf8.RuneCountInString(e.Event) > 64 {
		return 0, fmt.Errorf("%w: platform log event and description are required", ErrInvalid)
	}
	meta := RequestMeta{IP: "127.0.0.1"}
	if e.Meta != nil {
		meta = *e.Meta
	} else if m, ok := RequestMetaFromContext(ctx); ok && m.IP != "" {
		meta = m
	}
	if meta.IP == "" {
		meta.IP = "127.0.0.1"
	}
	desc := i18n.Render(i18n.SourceLocale, e.Key, e.Params)
	if utf8.RuneCountInString(desc) > 500 { // mb_substr($description, 0, 500)
		desc = string([]rune(desc)[:500])
	}
	details, removed := Redact(e.Details)
	if len(removed) > 0 {
		slog.WarnContext(ctx, "platform log: secret fields removed from details", "event", e.Event, "fields", removed)
	}
	if details == nil {
		details = map[string]any{}
	}
	details[TextKey] = Text{Key: e.Key, Params: e.Params}
	raw, err := json.Marshal(details)
	if err != nil {
		return 0, fmt.Errorf("platform log: details: %w", err)
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	p := store.SuperadminInsertPlatformLogParams{Event: e.Event, Description: desc, Details: raw, IpAddress: meta.IP, Now: &at}
	if e.UserID > 0 {
		p.UserID = &e.UserID
	}
	if e.ClubID > 0 {
		p.ClubID = &e.ClubID
	}
	if e.SubjectType != "" && e.SubjectID > 0 {
		p.SubjectType, p.SubjectID = &e.SubjectType, &e.SubjectID
	}
	if meta.UserAgent != "" {
		ua := meta.UserAgent
		if utf8.RuneCountInString(ua) > 500 {
			ua = string([]rune(ua)[:500])
		}
		p.UserAgent = &ua
	}
	id, err := q.SuperadminInsertPlatformLog(ctx, p)
	if err != nil {
		return 0, fmt.Errorf("platform log: insert %s: %w", e.Event, err)
	}
	return id, nil
}
