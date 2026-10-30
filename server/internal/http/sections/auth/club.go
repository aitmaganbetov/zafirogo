package auth

import (
	"context"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	authapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// defaultClubLocale — LocaleResolver::DEFAULT_CLUB_LOCALE.
const defaultClubLocale = "ru"

// clubInfo — язык и часовой пояс клуба (clubs — платформенная таблица без RLS).
func (h *handlers) clubInfo(ctx context.Context, c *authapi.MeClub) error {
	c.Locale, c.Timezone = defaultClubLocale, "Asia/Almaty"
	if h.d.Pool == nil {
		return nil
	}
	return db.InPlatformTx(ctx, h.d.Pool, func(q *store.Queries) error {
		row, err := q.ClubLocaleTimezone(ctx, c.Id)
		if err != nil {
			return err
		}
		if row.Locale != nil && *row.Locale != "" {
			c.Locale = *row.Locale
		}
		c.Timezone = row.Timezone
		return nil
	})
}
