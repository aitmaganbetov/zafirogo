package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"

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

// integrationsAccess — @can('manage-integrations') (ClubMembership::canManageIntegrations()): владелец (и режим
// просмотра суперадмина — как владелец) или флаг can_manage_integrations членства. Меню настроек (G5).
func (h *handlers) integrationsAccess(ctx context.Context, p principal.Principal, c *authapi.MeClub) error {
	allowed := p.Role == permissions.RoleOwner
	if !allowed && p.Role != "" && h.d.Pool != nil {
		err := db.InTenantTxWith(ctx, h.d.Pool, p.ClubID, db.Options{UserID: p.UserID}, func(q *store.Queries) error {
			m, err := q.IntegrationsAccess(ctx, store.IntegrationsAccessParams{ClubID: p.ClubID, UserID: p.UserID})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			allowed = err == nil && m.CanManageIntegrations
			return err
		})
		if err != nil {
			return err
		}
	}
	c.CanManageIntegrations = &allowed
	return nil
}
