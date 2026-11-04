package health

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// Monitor — проблемы одного ПК на время его стрима: трекер дребезга + журнал. Не потокобезопасен (вызывается из
// горутины чтения стрима). Открытые проблемы читаются из журнала при первом здоровье этого подключения (а не при
// каждом Hello): ПК со старым агентом без health в журнал не обращаются вовсе.
type Monitor struct {
	ClubID, ComputerID int64
	// Journal — nil: переходы фиксируются без записи (тесты).
	Journal *Journal
	Log     *slog.Logger

	tr     *Tracker
	loaded bool
}

// NewMonitor создаёт монитор ПК.
func NewMonitor(th Thresholds, j *Journal, clubID, computerID int64, log *slog.Logger) *Monitor {
	if log == nil {
		log = slog.Default()
	}
	return &Monitor{ClubID: clubID, ComputerID: computerID, Journal: j, Log: log, tr: NewTracker(th)}
}

// Observe — новое здоровье ПК: подтверждённые проблемы после него (по возрастанию) и изменились ли они.
// Ошибка журнала не теряет переход: он повторится на следующем измерении.
func (m *Monitor) Observe(ctx context.Context, h *presence.Health, now time.Time) (problems []string, changed bool) {
	before := m.tr.Active()
	if h == nil {
		return before, false
	}
	if !m.loaded && m.Journal != nil {
		open, err := m.Journal.Open(ctx, m.ClubID, m.ComputerID)
		if err != nil {
			m.Log.Warn("agent health: open problems", "computer_id", m.ComputerID, "err", err)
			return before, false
		}
		m.tr.Restore(open)
	}
	m.loaded = true
	if trs := m.tr.Observe(h, now); len(trs) > 0 {
		if m.Journal != nil {
			if err := m.Journal.Record(ctx, m.ClubID, m.ComputerID, trs, now); err != nil {
				m.Log.Warn("agent health: record transitions", "computer_id", m.ComputerID, "err", err)
				return m.tr.Active(), !slices.Equal(before, m.tr.Active())
			}
		}
		m.tr.Commit(trs)
	}
	after := m.tr.Active()
	return after, !slices.Equal(before, after)
}
