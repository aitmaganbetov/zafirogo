package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/mfa"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/config"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// cmdTOTPReset — подкоманда аварийного сброса второго фактора суперадмина (Этап 7, docs/deploy.md «Аварийный сброс
// двухфакторной аутентификации суперадмина»): для владельца с доступом к серверу, когда ни один суперадмин не может
// войти (потерян телефон у единственного суперадмина или ключ данных security.data_keys).
const cmdTOTPReset = "superadmin-totp-reset"

// totpResetDeps — то, что команде нужно от окружения (в тестах — без Valkey).
type totpResetDeps struct {
	platform db.Beginner
	// revoke завершает сеансы пользователя; nil — не завершаются.
	revoke func(ctx context.Context, userID int64) error
	out    io.Writer
}

// runTOTPReset: zafiro [-config файл] superadmin-totp-reset -phone +7XXXXXXXXXX.
func runTOTPReset(ctx context.Context, cfg *config.Config, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(cmdTOTPReset, flag.ContinueOnError)
	fs.SetOutput(out)
	phone := fs.String("phone", "", "телефон суперадмина: +7XXXXXXXXXX (скобки, пробелы и дефисы допускаются)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.Database.PlatformURL.IsZero() {
		return errors.New("database.platform_url не задан (нужен логин zafiro_platform)")
	}
	pool, err := db.NewPool(ctx, cfg.Database.PlatformURL.Reveal())
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()
	deps := totpResetDeps{platform: pool, out: out}

	vk, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{cfg.Valkey.Addr}, Password: cfg.Valkey.Password.Reveal(), DisableCache: true})
	if err != nil {
		return fmt.Errorf("valkey: %w", err)
	}
	defer vk.Close()
	keys := make([]session.Key, 0, len(cfg.Auth.SigningKeys))
	for _, k := range cfg.Auth.SigningKeys {
		keys = append(keys, session.Key{ID: k.ID, Secret: []byte(k.Secret.Reveal())})
	}
	sessions, err := session.New(session.Options{Client: vk, Keys: keys})
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	deps.revoke = sessions.RevokeUser
	return totpReset(ctx, deps, *phone)
}

// totpReset снимает второй фактор суперадмина по телефону: ключ и коды восстановления, сеансы; запись в журнал
// платформы (от имени платформы, user_id NULL) и audit_log (actor_kind system).
func totpReset(ctx context.Context, d totpResetDeps, rawPhone string) error {
	phone := auth.NormalizePhone(rawPhone)
	if phone == "" {
		return errors.New("-phone обязателен")
	}
	var (
		user store.SuperadminUserByPhoneRow
		had  bool
	)
	err := db.InPlatformTx(ctx, d.platform, func(q *store.Queries) error {
		var err error
		user, err = q.SuperadminUserByPhone(ctx, phone)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("пользователь с телефоном %s не найден", phone)
		}
		if err != nil {
			return err
		}
		if had, err = mfa.Reset(ctx, q, user.ID); err != nil || !had {
			return err
		}
		if _, err := activity.RecordPlatform(ctx, q, activity.PlatformEntry{Event: mfa.EventReset, Key: mfa.MsgReset,
			Params: map[string]string{"user": user.Name}, SubjectType: mfa.SubjectUser, SubjectID: user.ID,
			Details: map[string]any{"via": cmdTOTPReset}}); err != nil {
			return err
		}
		subject := mfa.SubjectUser
		return q.InsertAuditLog(ctx, store.InsertAuditLogParams{ActorKind: "system", Category: "security", Action: "superadmin.totp_reset",
			SubjectType: &subject, SubjectID: &user.ID,
			Before: []byte(`{"second_factor":true}`), After: []byte(`{"second_factor":false,"via":"cli"}`)})
	})
	if err != nil {
		return err
	}
	if !had {
		_, _ = fmt.Fprintf(d.out, "У пользователя %s (id %d) второй фактор не настроен — менять нечего.\n", user.Name, user.ID)
		return nil
	}
	if d.revoke != nil {
		if err := d.revoke(ctx, user.ID); err != nil {
			slog.WarnContext(ctx, "revoke sessions after totp reset", "user_id", user.ID, "err", err)
			_, _ = fmt.Fprintln(d.out, "Внимание: сеансы не завершены (Valkey недоступен) — они закончатся сами.")
		}
	}
	_, _ = fmt.Fprintf(d.out, "Второй фактор пользователя %s (id %d) сброшен. При следующем входе — настройка заново.\n", user.Name, user.ID)
	if !user.IsSuperadmin {
		_, _ = fmt.Fprintln(d.out, "Пользователь не суперадмин: второй фактор у него не запрашивается.")
	}
	return nil
}
