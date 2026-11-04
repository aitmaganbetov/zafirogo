package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Ключи platform_settings.
const (
	SettingGateway = "telegram_gateway" // TelegramGateway::SETTING
	SettingAlerts  = "telegram_alerts"  // TelegramAlerts::SETTING
)

// Error — ошибка для пользователя: ключ перевода (русский текст старого кода) и подстановки.
type Error struct {
	Key    string
	Params map[string]string
}

func (e *Error) Error() string { return i18n.Render(i18n.SourceLocale, e.Key, e.Params) }

// Secret — настройка с расшифрованным токеном (никогда не отдаётся наружу и не пишется в лог).
type Secret struct {
	Token  string
	ChatID string
	Locale string
}

// Store читает настройки Telegram из platform_settings прямо из таблицы (без кэша, как в старом коде).
type Store struct {
	// Pool — пул zafiro_platform.
	Pool db.Beginner
	// Box — ключи шифрования секретов; nil — токены не расшифровать («не настроено»).
	Box *secretbox.Box
}

// Read — настройка key; nil — нет, без токена или токен не расшифровывается (другой ключ) — «не настроено»,
// как catch (Throwable) → null в TelegramGateway::token().
func (s *Store) Read(ctx context.Context, key string) (*Secret, error) {
	if s == nil || s.Pool == nil {
		return nil, nil
	}
	var raw []byte
	err := db.InPlatformTx(ctx, s.Pool, func(q *store.Queries) error {
		var err error
		raw, err = q.TelegramSetting(ctx, key)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v struct {
		Token  string `json:"token"`
		ChatID any    `json:"chat_id"`
		Locale string `json:"locale"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if len(raw) == 0 || dec.Decode(&v) != nil || v.Token == "" || s.Box == nil {
		return nil, nil
	}
	token, err := s.Box.Open(v.Token)
	if err != nil {
		return nil, nil //nolint:nilerr // не расшифровывается — «не настроено», как в старом коде
	}
	out := &Secret{Token: token, Locale: v.Locale}
	switch c := v.ChatID.(type) {
	case string:
		out.ChatID = c
	case json.Number:
		out.ChatID = c.String()
	}
	return out, nil
}
