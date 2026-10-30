package presence

import (
	"context"
	"log/slog"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// SweeperJob — имя фоновой задачи и лока «один исполнитель» в Valkey.
const SweeperJob = "agent-presence-sweeper"

// Sweeper — фоновая проверка присутствия (один исполнитель на кластер, лок в Valkey):
//
//   - каждые Interval: ПК, у которых истёк срок в Valkey (стрим закрыт и не открылся за OfflineGrace,
//     или инстанс упал и перестал продлевать TTL), получают status = 'offline' в Postgres;
//   - каждые ReconcileEvery: ПК со status = 'online' в Postgres, о которых Valkey ничего не знает два прохода
//     подряд (потеря данных Valkey, сбой между записями), тоже получают 'offline';
//   - там же — истечение невыполненных команд агента (expires_at) и удаление старых строк agent_commands.
type Sweeper struct {
	Store *Store
	// Pool — пул логина zafiro_platform (задача обходит все клубы).
	Pool db.Beginner
	// Token — значение лока (id инстанса).
	Token string
	// Interval — период проверки (0 — 5 с).
	Interval time.Duration
	// ReconcileEvery — период сверки с Postgres и очистки команд (0 — 5 мин).
	ReconcileEvery time.Duration
	// CommandRetention — сколько хранить строки agent_commands (0 — 7 дней).
	CommandRetention time.Duration
	// Batch — размер пачки (0 — 500).
	Batch int
	Now   func() time.Time
	Log   *slog.Logger
	// OnOffline — ПК записан офлайн (WebSocket админки и т.п.). Необязательно.
	OnOffline func(ctx context.Context, clubID, computerID int64)
	// OnCommandsExpired — команды истекли невыполненными. Необязательно.
	OnCommandsExpired func(ctx context.Context, rows []store.ExpireAgentCommandsRow)

	suspects  map[[2]int64]bool
	lastRecon time.Time
}

func (w *Sweeper) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Sweeper) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

func (w *Sweeper) interval() time.Duration {
	if w.Interval > 0 {
		return w.Interval
	}
	return 5 * time.Second
}

func (w *Sweeper) batch() int {
	if w.Batch > 0 {
		return w.Batch
	}
	return 500
}

// Run выполняет проверки до отмены ctx.
func (w *Sweeper) Run(ctx context.Context) error {
	t := time.NewTicker(w.interval())
	defer t.Stop()
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = w.Store.Unlock(unlockCtx, SweeperJob, w.Token)
	}()
	for {
		ok, err := w.Store.TryLock(ctx, SweeperJob, w.Token, 3*w.interval())
		if err != nil && ctx.Err() == nil {
			w.log().Warn("presence sweeper lock failed", "err", err)
		}
		if ok {
			if _, err := w.Tick(ctx); err != nil && ctx.Err() == nil {
				w.log().Error("presence sweeper tick failed", "err", err)
			}
			every := w.ReconcileEvery
			if every <= 0 {
				every = 5 * time.Minute
			}
			if w.now().Sub(w.lastRecon) >= every {
				w.lastRecon = w.now()
				if _, err := w.Reconcile(ctx); err != nil && ctx.Err() == nil {
					w.log().Error("presence reconcile failed", "err", err)
				}
				if _, _, err := w.CleanupCommands(ctx); err != nil && ctx.Err() == nil {
					w.log().Error("agent commands cleanup failed", "err", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick записывает офлайн ПК с истёкшим сроком. Возвращает, сколько строк Postgres изменено.
func (w *Sweeper) Tick(ctx context.Context) (int, error) {
	total := 0
	for {
		gone, err := w.Store.ClaimGone(ctx, w.batch())
		if err != nil {
			return total, err
		}
		if len(gone) == 0 {
			return total, nil
		}
		n, err := w.markOffline(ctx, gone)
		total += n
		if err != nil {
			return total, err
		}
		if len(gone) < w.batch() {
			return total, nil
		}
	}
}

func (w *Sweeper) markOffline(ctx context.Context, gone []Gone) (int, error) {
	now := w.now().UTC()
	var changed []Gone
	err := db.InPlatformTx(ctx, w.Pool, func(q *store.Queries) error {
		changed = changed[:0]
		for _, g := range gone {
			seen := g.Last.LastSeenAt.UTC()
			if seen.IsZero() || seen.After(now) {
				seen = now
			}
			n, err := q.MarkAgentOffline(ctx, store.MarkAgentOfflineParams{LastSeenAt: &seen, Now: &now, ClubID: g.ClubID, ComputerID: g.ComputerID})
			if err != nil {
				return err
			}
			if n > 0 {
				changed = append(changed, g)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	// Гонка: ПК переподключился между проверкой в Valkey и записью — вернуть 'online'. Новое соединение берёт
	// владение в Valkey раньше, чем читает карточку из Postgres, поэтому оно либо видно здесь, либо прочитает
	// уже 'offline' и само запишет 'online'.
	pcs := make([][2]int64, len(gone))
	for i, g := range gone {
		pcs[i] = [2]int64{g.ClubID, g.ComputerID}
	}
	known, err := w.Store.Known(ctx, pcs)
	if err != nil {
		return len(changed), err
	}
	repaired := map[[2]int64]bool{}
	if err := db.InPlatformTx(ctx, w.Pool, func(q *store.Queries) error {
		for i, k := range known {
			if !k {
				continue
			}
			if _, err := q.MarkAgentOnline(ctx, store.MarkAgentOnlineParams{Now: &now, ClubID: pcs[i][0], ComputerID: pcs[i][1]}); err != nil {
				return err
			}
			repaired[pcs[i]] = true
		}
		return nil
	}); err != nil {
		return len(changed), err
	}
	if w.OnOffline != nil {
		for _, g := range changed {
			if !repaired[[2]int64{g.ClubID, g.ComputerID}] {
				w.OnOffline(ctx, g.ClubID, g.ComputerID)
			}
		}
	}
	return len(changed), nil
}

// Reconcile сверяет status = 'online' в Postgres с Valkey. ПК без следа в Valkey два прохода подряд
// получает 'offline' (одного прохода мало: после перезапуска Valkey инстансы восстанавливают ключи за TTL/3).
func (w *Sweeper) Reconcile(ctx context.Context) (int, error) {
	prev := w.suspects
	next := map[[2]int64]bool{}
	var offline []Gone
	var afterClub, afterID int64
	for {
		var rows []store.ListOnlineComputersRow
		err := db.InPlatformTx(ctx, w.Pool, func(q *store.Queries) error {
			var err error
			rows, err = q.ListOnlineComputers(ctx, store.ListOnlineComputersParams{AfterClubID: afterClub, AfterID: afterID, RowLimit: int32(w.batch())}) // #nosec G115 -- batch ≤ разумного размера
			return err
		})
		if err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			break
		}
		pcs := make([][2]int64, len(rows))
		for i, r := range rows {
			pcs[i] = [2]int64{r.ClubID, r.ID}
		}
		known, err := w.Store.Known(ctx, pcs)
		if err != nil {
			return 0, err
		}
		for i, k := range known {
			if k {
				continue
			}
			if prev[pcs[i]] {
				offline = append(offline, Gone{ClubID: pcs[i][0], ComputerID: pcs[i][1]})
			} else {
				next[pcs[i]] = true
			}
		}
		last := rows[len(rows)-1]
		afterClub, afterID = last.ClubID, last.ID
		if len(rows) < w.batch() {
			break
		}
	}
	w.suspects = next
	if len(offline) == 0 {
		return 0, nil
	}
	return w.markOffline(ctx, offline)
}

// CleanupCommands помечает истёкшие невыполненные команды и удаляет строки старше CommandRetention.
func (w *Sweeper) CleanupCommands(ctx context.Context) (expired, deleted int, err error) {
	now := w.now().UTC()
	limit := int32(w.batch()) // #nosec G115 -- batch ≤ разумного размера
	for {
		var rows []store.ExpireAgentCommandsRow
		err = db.InPlatformTx(ctx, w.Pool, func(q *store.Queries) error {
			var err error
			rows, err = q.ExpireAgentCommands(ctx, store.ExpireAgentCommandsParams{Now: &now, RowLimit: limit})
			return err
		})
		if err != nil {
			return expired, deleted, err
		}
		expired += len(rows)
		if len(rows) > 0 && w.OnCommandsExpired != nil {
			w.OnCommandsExpired(ctx, rows)
		}
		if len(rows) < int(limit) {
			break
		}
	}
	retention := w.CommandRetention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	before := now.Add(-retention)
	for {
		var n int64
		err = db.InPlatformTx(ctx, w.Pool, func(q *store.Queries) error {
			var err error
			n, err = q.DeleteOldAgentCommands(ctx, store.DeleteOldAgentCommandsParams{Before: before, RowLimit: limit})
			return err
		})
		if err != nil {
			return expired, deleted, err
		}
		deleted += int(n)
		if n < int64(limit) {
			return expired, deleted, nil
		}
	}
}
