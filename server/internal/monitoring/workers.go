package monitoring

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// Имена задач (ключи advisory lock, Q-086) и расписания старого routes/console.php.
const (
	CollectJob  = "monitoring:collect" // everyMinute()->withoutOverlapping(5)
	ClubsJob    = "monitoring:clubs"   // everyFiveMinutes()->withoutOverlapping(5)
	PruneJob    = "monitoring:prune"   // daily()
	CollectStep = time.Minute
	ClubsStep   = 5 * time.Minute
	PruneStep   = 24 * time.Hour
	// RetentionDays — config('monitoring.retention_days').
	RetentionDays = 30
)

// OnlineCounter — ПК на связи по клубам (*presence.Store).
type OnlineCounter interface {
	OnlineByClub(ctx context.Context, now time.Time) (map[int64]int64, error)
}

// Workers — фоновые задачи мониторинга.
type Workers struct {
	// Platform — пул zafiro_platform (по всем клубам; advisory lock).
	Platform *pgxpool.Pool
	Recorder *Recorder
	Probe    Probe
	Online   OnlineCounter
	// Alerts — оповещения в Telegram (Q-060); nil — без оповещений.
	Alerts *Alerts
	Log    *slog.Logger
}

func (w *Workers) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

// every — тик на границе периода (как планировщик Laravel: каждую минуту / каждые 5 минут по часам).
func every(ctx context.Context, step time.Duration, fn func(ctx context.Context, now time.Time)) {
	for {
		now := time.Now()
		next := now.Truncate(step).Add(step)
		t := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case at := <-t.C:
			fn(ctx, at)
		}
	}
}

// RunFlush — каждый инстанс раз в минуту добавляет свои законченные минуты запросов в базу (без лидерства:
// счётчики у каждого инстанса свои, UPSERT складывает).
func (w *Workers) RunFlush(ctx context.Context) error {
	every(ctx, CollectStep, func(ctx context.Context, now time.Time) {
		if err := w.Flush(ctx, now); err != nil && ctx.Err() == nil {
			w.log().Warn("monitoring: flush request metrics", "err", err)
		}
	})
	// Остаток при остановке — до текущей минуты включительно.
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = w.Flush(sctx, time.Now().Add(time.Minute))
	return nil
}

// Flush — CollectServerMetrics::saveRequests() для минут меньше текущей.
func (w *Workers) Flush(ctx context.Context, now time.Time) error {
	d := w.Recorder.Drain(now.Unix() / 60)
	if len(d.Channels) == 0 && len(d.Clubs) == 0 {
		return nil
	}
	err := db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
		for minute, chs := range d.Channels {
			for ch, t := range chs {
				if err := q.MonitoringAddRequests(ctx, store.MonitoringAddRequestsParams{
					Minute: minute, Channel: ch, Requests: t.Requests, Errors: t.Errors, DurationMs: t.DurationMs,
				}); err != nil {
					return err
				}
			}
		}
		for minute, clubs := range d.Clubs {
			for club, t := range clubs {
				if err := q.MonitoringAddClubRequests(ctx, store.MonitoringAddClubRequestsParams{
					ClubID: club, Minute: minute, AgentRequests: t.Agent, ApiRequests: t.API, PanelRequests: t.Panel,
					GuestRequests: t.Guest, Errors: t.Errors, DurationMs: t.DurationMs,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		w.Recorder.Restore(d)
	}
	return err
}

// RunCollect — monitoring:collect: снимок сервера раз в минуту (один исполнитель на кластер). По его строкам
// экран видит, что планировщик жив.
func (w *Workers) RunCollect(ctx context.Context) error {
	every(ctx, CollectStep, func(ctx context.Context, now time.Time) {
		if _, err := w.Collect(ctx, now); err != nil && ctx.Err() == nil {
			w.log().Warn("monitoring: collect", "job", CollectJob, "err", err)
		}
	})
	return nil
}

// Collect — один тик monitoring:collect.
func (w *Workers) Collect(ctx context.Context, now time.Time) (bool, error) {
	return leader.RunOnce(ctx, w.Platform, CollectJob, func(ctx context.Context, _ *pgxpool.Conn) error {
		s := w.Probe.Snapshot(ctx, w.Platform)
		if err := db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
			return q.MonitoringInsertServerMetric(ctx, store.MonitoringInsertServerMetricParams{
				Minute: now.Unix() / 60, Load1: s.Load1, Load5: s.Load5, Load15: s.Load15,
				CpuCount: s.CPUCount, MemoryTotalMb: s.MemoryTotalMB, MemoryUsedMb: s.MemoryUsedMB,
				DiskTotalMb: s.DiskTotalMB, DiskUsedMb: s.DiskUsedMB, UptimeSeconds: s.UptimeSeconds,
				DatabaseMb: s.DatabaseMB, QueuePending: s.QueuePending, QueueFailed: s.QueueFailed,
				QueueOldestSeconds: s.QueueOldestSeconds,
			})
		}); err != nil {
			return err
		}
		return w.checkServer(ctx, now, s)
	})
}

// RunClubs — monitoring:clubs: снимок загрузки клубов раз в 5 минут.
func (w *Workers) RunClubs(ctx context.Context) error {
	every(ctx, ClubsStep, func(ctx context.Context, now time.Time) {
		if _, err := w.SnapshotClubs(ctx, now); err != nil && ctx.Err() == nil {
			w.log().Warn("monitoring: club snapshots", "job", ClubsJob, "err", err)
		}
	})
	return nil
}

// SnapshotClubs — SnapshotClubLoad::handle(): по клубу с ПК — всего, в сети, занято; день недели (1 = пн) и час
// в часовом поясе клуба; оповещение «клуб ушёл офлайн» (Telegram, Q-060) — checkClubs.
func (w *Workers) SnapshotClubs(ctx context.Context, now time.Time) (bool, error) {
	return leader.RunOnce(ctx, w.Platform, ClubsJob, func(ctx context.Context, _ *pgxpool.Conn) error {
		online := map[int64]int64{}
		if w.Online != nil {
			o, err := w.Online.OnlineByClub(ctx, time.Now())
			if err != nil {
				return err
			}
			online = o
		}
		current := map[int64]int64{} // клуб с ПК → в сети сейчас
		if err := db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
			clear(current)
			rows, err := q.MonitoringClubCounts(ctx, &now)
			if err != nil {
				return err
			}
			for _, r := range rows {
				loc, err := time.LoadLocation(r.Timezone)
				if err != nil || r.Timezone == "" {
					loc = time.UTC // config('app.timezone')
				}
				local := now.In(loc)
				wd := int16(local.Weekday()) // #nosec G115 -- 0..6; isoWeekday(): воскресенье — 7
				if wd == 0 {
					wd = 7
				}
				if err := q.MonitoringInsertLoadSnapshot(ctx, store.MonitoringInsertLoadSnapshotParams{
					ClubID: r.ClubID, CapturedAt: now, Weekday: wd, Hour: int16(local.Hour()), // #nosec G115 -- 0..23
					Computers: r.Computers, Online: min(online[r.ClubID], r.Computers), Busy: r.Busy,
				}); err != nil {
					return err
				}
				current[r.ClubID] = min(online[r.ClubID], r.Computers)
			}
			return nil
		}); err != nil {
			return err
		}
		return w.checkClubs(ctx, now, current)
	})
}

// RunPrune — monitoring:prune: раз в сутки удаляет снимки старше 30 дней и запросы клубов старше 2 дней.
func (w *Workers) RunPrune(ctx context.Context) error {
	every(ctx, PruneStep, func(ctx context.Context, now time.Time) {
		if _, err := w.Prune(ctx, now); err != nil && ctx.Err() == nil {
			w.log().Warn("monitoring: prune", "job", PruneJob, "err", err)
		}
	})
	return nil
}

// Prune — PruneMonitoringData::handle().
func (w *Workers) Prune(ctx context.Context, now time.Time) (bool, error) {
	return leader.RunOnce(ctx, w.Platform, PruneJob, func(ctx context.Context, _ *pgxpool.Conn) error {
		before := now.AddDate(0, 0, -RetentionDays)
		minute := before.Unix() / 60
		return db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
			if _, err := q.MonitoringPruneServerMetrics(ctx, minute); err != nil {
				return err
			}
			if _, err := q.MonitoringPruneRequestMetrics(ctx, minute); err != nil {
				return err
			}
			if _, err := q.MonitoringPruneLoadSnapshots(ctx, before); err != nil {
				return err
			}
			_, err := q.MonitoringPruneClubRequests(ctx, now.AddDate(0, 0, -2).Unix()/60)
			return err
		})
	})
}
