// Package logging создаёт slog.Logger по конфигурации.
//
// Правило проекта: в логи не пишутся тела запросов, пароли, токены, DSN с паролями.
// Для структур с секретами реализуйте slog.LogValuer (см. config.Config).
package logging

import (
	"io"
	"log/slog"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/config"
)

// New возвращает логгер, пишущий в w в формате cfg.Format (json|text)
// с уровнем cfg.Level.
func New(cfg config.Log, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: ParseLevel(cfg.Level)}
	var h slog.Handler
	if strings.EqualFold(cfg.Format, "text") {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

// ParseLevel переводит строку в slog.Level; неизвестное значение — info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
