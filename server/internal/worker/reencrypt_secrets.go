package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/webhook"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// ReencryptSecretsJob — имя задачи и ключ advisory lock.
const ReencryptSecretsJob = "secrets:reencrypt" // #nosec G101 -- имя задачи, не секрет

// ReencryptSecretsInterval — повторная проверка (после ротации ключей инстансы перезапускаются, первый тик — при
// старте; периодический тик подхватывает строки, записанные инстансами со старой конфигурацией во время раскатки).
const ReencryptSecretsInterval = time.Hour

const reencryptBatch = 200

// ReencryptSecrets — перешифровка секретов в базе текущим ключом данных (security.data_keys, Q-050):
//   - webhook_endpoints.secret: открытый текст (строки до миграции 0025), формат v1 и v2 со старым kid;
//   - platform_settings.value.token (токены Telegram): v1 (superadmin.secret_key Этапа 3) и v2 со старым kid.
//
// Ключа в SQL нет, поэтому это воркер, а не миграция. Один исполнитель — leader.RunOnce (Q-086); запись
// условная (значение не изменилось), поэтому повтор и параллельная ротация секрета пользователем безопасны.
// Значение, которое не расшифровывается ни одним ключом связки, остаётся как есть (ошибка в журнале).
type ReencryptSecrets struct {
	// Platform — пул zafiro_platform (строки всех клубов, advisory lock).
	Platform *pgxpool.Pool
	// Box — связка ключей данных.
	Box *secretbox.Box
	// Interval — период (0 — ReencryptSecretsInterval).
	Interval time.Duration
	// Log — журнал (nil — slog.Default()).
	Log *slog.Logger
}

// ReencryptResult — итог тика.
type ReencryptResult struct {
	Webhooks, Settings int // перешифровано
	Failed             int // не расшифровано ни одним ключом
}

// Run выполняет тики до отмены ctx; первый — сразу.
func (w *ReencryptSecrets) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = ReencryptSecretsInterval
	}
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
				w.log().Error("reencrypt secrets tick failed", "job", ReencryptSecretsJob, "err", err)
			}
			t.Reset(interval)
		}
	}
}

// Tick — один проход: ran = false, если лок держит другой инстанс.
func (w *ReencryptSecrets) Tick(ctx context.Context) (ran bool, res ReencryptResult, err error) {
	if w.Box == nil {
		return false, res, errors.New("reencrypt secrets: no keyring")
	}
	ran, err = leader.RunOnce(ctx, w.Platform, ReencryptSecretsJob, func(ctx context.Context, conn *pgxpool.Conn) error {
		q := store.New(conn)
		if err := w.webhooks(ctx, q, &res); err != nil {
			return err
		}
		return w.settings(ctx, q, &res)
	})
	if res.Webhooks+res.Settings > 0 || res.Failed > 0 {
		w.log().Info("secrets re-encrypted", "job", ReencryptSecretsJob, "key_id", w.Box.ActiveKeyID(),
			"webhooks", res.Webhooks, "settings", res.Settings, "failed", res.Failed)
	}
	return ran, res, err
}

func (w *ReencryptSecrets) webhooks(ctx context.Context, q *store.Queries, res *ReencryptResult) error {
	var after int64
	for {
		rows, err := q.SecretsWebhooksToReseal(ctx, store.SecretsWebhooksToResealParams{
			AfterID: after, CurrentPrefix: w.Box.CurrentPrefix(), Batch: reencryptBatch,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			after = r.ID
			out, changed, err := w.Box.Reseal(r.Secret, webhook.SecretContext(r.ClubID), true)
			if err != nil {
				res.Failed++
				w.log().Error("webhook secret cannot be decrypted with any data key", "club_id", r.ClubID, "endpoint_id", r.ID)
				continue
			}
			if !changed {
				continue
			}
			n, err := q.SecretsResealWebhook(ctx, store.SecretsResealWebhookParams{
				NewSecret: out, ClubID: r.ClubID, ID: r.ID, OldSecret: r.Secret,
			})
			if err != nil {
				return err
			}
			res.Webhooks += int(n)
		}
		if len(rows) < reencryptBatch {
			return nil
		}
	}
}

func (w *ReencryptSecrets) settings(ctx context.Context, q *store.Queries, res *ReencryptResult) error {
	rows, err := q.SecretsPlatformSettingsToReseal(ctx, w.Box.CurrentPrefix())
	if err != nil {
		return err
	}
	for _, r := range rows {
		var v struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(r.Value, &v) != nil {
			continue
		}
		out, changed, err := w.Box.Reseal(v.Token, "", false)
		if err != nil {
			res.Failed++
			w.log().Error("platform setting token cannot be decrypted with any data key", "key", r.Key)
			continue
		}
		if !changed {
			continue
		}
		n, err := q.SecretsResealPlatformSetting(ctx, store.SecretsResealPlatformSettingParams{NewToken: out, Key: r.Key, OldToken: v.Token})
		if err != nil {
			return err
		}
		res.Settings += int(n)
	}
	return nil
}

func (w *ReencryptSecrets) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
