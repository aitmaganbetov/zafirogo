package worker

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// ExpireSubscriptionsJob — имя задачи и ключ advisory lock.
const ExpireSubscriptionsJob = "subscriptions:expire"

// ExpireSubscriptionsInterval — Schedule::command('subscriptions:expire')->everyFiveMinutes() (routes/console.php).
const ExpireSubscriptionsInterval = 5 * time.Minute

// ExpireSubscriptions — оплаченные подписки клубов с прошедшим сроком получают статус expired и запись журнала
// платформы subscription.expired. Laravel: app/Console/Commands/ExpireSubscriptions.php. Доступ закрывается и без
// неё (проверка активности по дате); статус — для кабинета, страницы оплаты и уведомлений. Только active: истёкший
// пробный период остаётся trialing (решение Q-036 — как в старом). Один исполнитель на кластер (Q-086).
type ExpireSubscriptions struct {
	Platform *pgxpool.Pool
	Interval time.Duration
	Now      func() time.Time
	Log      *slog.Logger
}

// Run выполняет тики до отмены ctx (первый — сразу).
func (w *ExpireSubscriptions) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = ExpireSubscriptionsInterval
	}
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
				w.log().Error("expire subscriptions tick failed", "job", ExpireSubscriptionsJob, "err", err)
			}
			t.Reset(interval)
		}
	}
}

// Tick — один запуск: n — сколько подписок истекло.
func (w *ExpireSubscriptions) Tick(ctx context.Context) (ran bool, n int, err error) {
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	ran, err = leader.RunOnce(ctx, w.Platform, ExpireSubscriptionsJob, func(ctx context.Context, _ *pgxpool.Conn) error {
		// Одна транзакция: статус и записи журнала вместе (в старом — транзакция на подписку, итог тот же).
		return db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
			rows, err := q.SuperadminExpireDueSubscriptions(ctx, &now)
			if err != nil {
				return err
			}
			n = len(rows)
			for _, r := range rows {
				club := r.ClubName
				if club == "" {
					club = "#" + strconv.FormatInt(r.ClubID, 10)
				}
				date := ""
				if r.CurrentPeriodEndsAt != nil {
					date = r.CurrentPeriodEndsAt.UTC().Format("02.01.2006") // ->format('d.m.Y'), app.timezone UTC
				}
				if _, err := activity.RecordPlatform(ctx, q, activity.PlatformEntry{
					Event: "subscription.expired", Key: "Оплаченная подписка клуба :club закончилась :date",
					Params: map[string]string{"club": club, "date": date}, ClubID: r.ClubID,
					SubjectType: `App\Models\ClubSubscription`, SubjectID: r.ID, At: now,
				}); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if n > 0 {
		w.log().Info("club subscriptions expired", "job", ExpireSubscriptionsJob, "count", n)
	}
	return ran, n, err
}

func (w *ExpireSubscriptions) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
