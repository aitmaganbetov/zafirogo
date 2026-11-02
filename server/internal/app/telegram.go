package app

import (
	"context"
	"log/slog"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/guestapi"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/legacy"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/monitoring"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram"
	"github.com/aitmaganbetov/zafirogo/server/internal/verifycode"
)

// telegramSet — Telegram (Этап 4, H2): Gateway (коды), бот оповещений, восстановление пароля, коды перевода,
// состояние оповещений мониторинга. Ключи шифрования (store.Box) подставляются, когда собрана связка ключей.
type telegramSet struct {
	store     *telegram.Store
	gateway   *telegram.Gateway
	alerts    *telegram.Alerts
	recovery  *verifycode.PasswordRecovery
	transfers *verifycode.TransferCodes
	monitor   *monitoring.Alerts
}

func newTelegram(a *App, hasher *password.Hasher, guestTokenTTL time.Duration, now func() time.Time, log *slog.Logger) *telegramSet {
	st := &telegram.Store{Pool: a.PlatformPool}
	t := &telegramSet{store: st}
	t.gateway = &telegram.Gateway{API: &telegram.GatewayClient{Log: log}, Store: st, Log: log}
	t.alerts = &telegram.Alerts{API: &telegram.BotClient{Log: log}, Store: st, I18n: i18n.NewCatalog(a.AppPool), Log: log}
	t.recovery = &verifycode.PasswordRecovery{Platform: a.PlatformPool, Gateway: t.gateway, Hasher: hasher, Now: now, Log: log,
		SignOut: passwordResetSignOut(a, guestTokenTTL, log)}
	t.transfers = &verifycode.TransferCodes{App: a.AppPool, Platform: a.PlatformPool, Gateway: t.gateway, Hasher: hasher, Now: now, Log: log}
	t.monitor = &monitoring.Alerts{Valkey: a.Valkey, Bot: t.alerts, Log: log}
	return t
}

// passwordResetSignOut — PasswordRecovery::reset(): выход везде — сеансы панели (sessions), токены мобильных
// приложений (http/legacy), токены гостя на ПК всех клубов (+ SignOutGuest на эти ПК). Ошибки — в журнал: пароль уже сменён.
func passwordResetSignOut(a *App, ttl time.Duration, log *slog.Logger) func(ctx context.Context, userID int64) {
	return func(ctx context.Context, userID int64) {
		if a.Auth != nil {
			if err := a.Auth.Sessions().RevokeUser(ctx, userID); err != nil {
				log.WarnContext(ctx, "password reset: sessions not revoked", "user_id", userID, "err", err)
			}
		}
		// Токены мобильных приложений (Sanctum, mobile_api_tokens) — во всех клубах.
		if a.AppPool != nil {
			if err := legacy.RevokeUserTokens(ctx, a.AppPool, userID, time.Now()); err != nil {
				log.WarnContext(ctx, "password reset: mobile tokens not revoked", "user_id", userID, "err", err)
			}
		}
		tokens := guestapi.NewTokens(a.Valkey, guestapi.DefaultKeyPrefix, ttl, time.Now)
		revoked, err := tokens.RevokeEverywhere(ctx, userID)
		if err != nil {
			log.WarnContext(ctx, "password reset: guest tokens not revoked", "user_id", userID, "err", err)
		}
		if a.Stream == nil {
			return
		}
		for _, g := range revoked {
			if _, err := a.Stream.Commands().Send(ctx, g.ClubID, g.PC, &agentv1.Command{
				Kind: &agentv1.Command_SignOutGuest{SignOutGuest: &agentv1.SignOutGuest{UserId: userID}},
			}); err != nil {
				log.WarnContext(ctx, "password reset: sign out command not sent", "computer_id", g.PC, "err", err)
			}
		}
	}
}
