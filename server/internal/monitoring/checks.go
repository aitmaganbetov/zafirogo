package monitoring

// Условия оповещений старого мониторинга (Q-060, PD-10: по коду старого проекта).
// Laravel: app/Console/Commands/CollectServerMetrics.php (scheduler, disk, errors, queue),
// app/Console/Commands/SnapshotClubLoad.php (club-offline.<id>), app/Services/Monitoring/SchedulerWatchdog.php,
// пороги — config/monitoring.php. Сертификаты (monitoring:certificates) оповещений не слали — PD-5.

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Пороги config/monitoring.php.
const (
	SchedulerSilentMinutes = 5   // scheduler_silent_minutes
	DiskPercent            = 85  // disk_percent
	ErrorRatePercent       = 5.0 // error_rate_percent
	ErrorRateMinErrors     = 10  // error_rate_min_errors
	QueuePending           = 100 // queue_pending
	QueueOldestMinutes     = 10  // queue_oldest_minutes
	// OfflineAlertMinOnline — SnapshotClubLoad::OFFLINE_ALERT_MIN_ONLINE.
	OfflineAlertMinOnline = 3
	// WatchdogStep — проверка «планировщик молчит» (в старом — не чаще раза в минуту на сервер).
	WatchdogStep = time.Minute
	WatchdogJob  = "monitoring:watchdog"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// phpFloat — float в строке, как PHP (20.0 → "20", 12.5 → "12.5").
func phpFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func (w *Workers) update(ctx context.Context, key string, failing bool, problem, recovered Text) {
	if _, err := w.Alerts.Update(ctx, key, failing, problem, recovered); err != nil && ctx.Err() == nil {
		w.log().Warn("monitoring: alert state", "alert", key, "err", err)
	}
}

// checkServer — CollectServerMetrics::handle() после снимка: планировщик жив, диск, доля 5xx, очередь.
func (w *Workers) checkServer(ctx context.Context, now time.Time, s Snapshot) error {
	if w.Alerts == nil {
		return nil
	}
	// Задачу запускает планировщик (лидер) — значит, он снова работает.
	w.update(ctx, "scheduler", false, Text{}, Text{Key: "Планировщик снова работает."})

	// checkDisk(): (int) round(used * 100 / total) > disk_percent.
	if s.DiskTotalMB != nil && *s.DiskTotalMB > 0 && s.DiskUsedMB != nil {
		percent := int64(math.Round(float64(*s.DiskUsedMB) * 100 / float64(*s.DiskTotalMB)))
		p := map[string]string{"percent": itoa(percent)}
		w.update(ctx, "disk", percent > DiskPercent,
			Text{Key: "Диск сервера заполнен на :percent%.", Params: p},
			Text{Key: "Место на диске освободилось: занято :percent%.", Params: p})
	}

	// checkErrors(): запросы за минуты >= $minute - 5.
	var t store.MonitoringErrorTotalsRow
	if err := db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
		var err error
		t, err = q.MonitoringErrorTotals(ctx, now.Unix()/60-5)
		return err
	}); err != nil {
		return err
	}
	percent := 0.0
	if t.Requests > 0 {
		percent = money.PHPRound(float64(t.Errors)*100/float64(t.Requests), 1) // round(…, 1)
	}
	w.update(ctx, "errors", t.Errors >= ErrorRateMinErrors && percent >= ErrorRatePercent,
		Text{Key: "Ошибки 5xx: :percent% запросов за 5 минут (:errors из :requests).",
			Params: map[string]string{"percent": phpFloat(percent), "errors": itoa(t.Errors), "requests": itoa(t.Requests)}},
		Text{Key: "Доля ошибок 5xx вернулась к норме."})

	// checkQueue(): в новом сервере очереди Laravel нет (QueuePending = 0) — условие перенесено как есть.
	var oldest int64
	if s.QueueOldestSeconds != nil {
		oldest = *s.QueueOldestSeconds
	}
	w.update(ctx, "queue", s.QueuePending >= QueuePending || oldest >= QueueOldestMinutes*60,
		Text{Key: "Копится очередь: :count заданий ждут, самое старое — :minutes мин.",
			Params: map[string]string{"count": itoa(s.QueuePending), "minutes": itoa(oldest / 60)}},
		Text{Key: "Очередь разобрана."})
	return nil
}

// checkClubs — SnapshotClubLoad: все ПК клуба (было в сети ≥ 3 за 2–12 минут до снимка) ушли офлайн при активной
// подписке — скорее всего, у клуба пропал интернет; клуб снова в сети — «восстановлено».
func (w *Workers) checkClubs(ctx context.Context, now time.Time, online map[int64]int64) error {
	if w.Alerts == nil || len(online) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(online))
	for id := range online {
		ids = append(ids, id)
	}
	var rows []store.MonitoringOfflineCandidatesRow
	if err := db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
		var err error
		rows, err = q.MonitoringOfflineCandidates(ctx, store.MonitoringOfflineCandidatesParams{
			FromAt: now.Add(-12 * time.Minute), ToAt: now.Add(-2 * time.Minute), Now: &now, ClubIds: ids,
		})
		return err
	}); err != nil {
		return err
	}
	for _, r := range rows {
		cur := online[r.ClubID]
		wentOffline := cur == 0 && r.PreviousOnline >= OfflineAlertMinOnline && r.Subscribed
		if !wentOffline && cur == 0 {
			continue
		}
		w.update(ctx, "club-offline."+itoa(r.ClubID), wentOffline,
			Text{Key: "Клуб :club: все ПК ушли офлайн (было в сети: :count). Возможно, у клуба пропал интернет.",
				Params: map[string]string{"club": r.Name, "count": itoa(r.PreviousOnline)}},
			Text{Key: "Клуб :club снова в сети: ПК онлайн — :count.", Params: map[string]string{"club": r.Name, "count": itoa(cur)}})
	}
	return nil
}

// RunWatchdog — SchedulerWatchdog: каждый инстанс раз в минуту проверяет, что снимки сервера пишутся (задача
// monitoring:collect лидера жива). В старом проверку делали запросы к сайту (cron мог пропасть целиком); здесь
// воркер на каждом инстансе, без лидерства: если лидер завис, оповестит любой живой инстанс.
func (w *Workers) RunWatchdog(ctx context.Context) error {
	every(ctx, WatchdogStep, func(ctx context.Context, now time.Time) {
		if err := w.Watchdog(ctx, now); err != nil && ctx.Err() == nil {
			w.log().Warn("monitoring: watchdog", "job", WatchdogJob, "err", err)
		}
	})
	return nil
}

// Watchdog — SchedulerWatchdog::check(): снимков нет — ничего; последний старше 5 минут — «планировщик молчит».
func (w *Workers) Watchdog(ctx context.Context, now time.Time) error {
	if w.Alerts == nil {
		return nil
	}
	var last int64
	if err := db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
		var err error
		last, err = q.MonitoringLastServerMinute(ctx)
		return err
	}); err != nil {
		return err
	}
	if last == 0 {
		return nil
	}
	if silent := now.Unix()/60 - last; silent > SchedulerSilentMinutes {
		w.update(ctx, "scheduler", true,
			Text{Key: "Планировщик молчит :minutes мин: вебхуки, окончание сессий и подписок не работают. Проверьте crontab.",
				Params: map[string]string{"minutes": itoa(silent)}}, Text{})
	}
	return nil
}
