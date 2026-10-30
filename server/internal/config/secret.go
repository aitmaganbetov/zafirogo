package config

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

// Secret — строка-секрет (пароль, DSN с паролем, ключ). Не печатается ни через fmt (%v, %s, %+v, %#v),
// ни через slog, ни в JSON: везде "***". Значение — только явным Reveal().
type Secret string

const masked = "***"

// Reveal возвращает значение секрета. Вызывать только там, где секрет передаётся потребителю
// (подключение к БД, ключ подписи), — никогда для логов.
func (s Secret) Reveal() string { return string(s) }

// IsZero — секрет не задан.
func (s Secret) IsZero() bool { return s == "" }

// String реализует fmt.Stringer.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return masked
}

// GoString реализует fmt.GoStringer (%#v).
func (s Secret) GoString() string { return `config.Secret("` + s.String() + `")` }

// LogValue реализует slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalText — на случай сериализации конфигурации (JSON/YAML): секрет не раскрывается.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// randomSecret — случайный секрет для dev/test (n байт в hex).
func randomSecret(n int) Secret {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand не возвращает ошибок в поддерживаемых ОС
	}
	return Secret(hex.EncodeToString(b))
}
