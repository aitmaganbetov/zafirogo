package health

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Journal — история проблем в Postgres (computer_health_events): только переходы. Пул — zafiro_app (тенантные
// транзакции клуба ПК).
type Journal struct {
	pool   db.Beginner
	writes atomic.Int64
}

// NewJournal создаёт журнал.
func NewJournal(pool db.Beginner) *Journal { return &Journal{pool: pool} }

// Writes — сколько строк вставлено или закрыто (метрика, тесты «нет записей без перехода»).
func (j *Journal) Writes() int64 { return j.writes.Load() }

// Open — открытые проблемы ПК.
func (j *Journal) Open(ctx context.Context, clubID, computerID int64) ([]Open, error) {
	var out []Open
	err := db.InTenantTx(ctx, j.pool, clubID, func(q *store.Queries) error {
		rows, err := q.ComputerHealthOpen(ctx, store.ComputerHealthOpenParams{ClubID: clubID, ComputerID: computerID})
		if err != nil {
			return err
		}
		out = make([]Open, 0, len(rows))
		for _, r := range rows {
			o := Open{Kind: r.Kind}
			_ = json.Unmarshal(r.Details, &o.Details)
			out = append(out, o)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("health: open problems: %w", err)
	}
	return out, nil
}

// Record пишет переходы одной транзакцией (всё или ничего: не записалось — трекер повторит). Если строка журнала
// действительно вставлена или закрыта — в той же транзакции событие notifications.changed (колокольчик панели:
// «Проблемы с ПК», S7-H-11): не чаще реальных переходов, которые уже защищены от дребезга трекером.
func (j *Journal) Record(ctx context.Context, clubID, computerID int64, trs []Transition, at time.Time) error {
	if len(trs) == 0 {
		return nil
	}
	var n int64
	err := db.InTenantTx(ctx, j.pool, clubID, func(q *store.Queries) error {
		n = 0
		for _, tr := range trs {
			var rows int64
			var err error
			if tr.Raised {
				details := tr.Details
				if details == nil {
					details = map[string]any{}
				}
				b, merr := json.Marshal(details)
				if merr != nil {
					return merr
				}
				rows, err = q.ComputerHealthRaise(ctx, store.ComputerHealthRaiseParams{
					ClubID: clubID, ComputerID: computerID, Kind: tr.Kind, RaisedAt: at.UTC(), Details: b})
			} else {
				rows, err = q.ComputerHealthClear(ctx, store.ComputerHealthClearParams{
					ClubID: clubID, ComputerID: computerID, Kind: tr.Kind, ClearedAt: at.UTC()})
			}
			if err != nil {
				return err
			}
			n += rows
		}
		if n == 0 {
			return nil
		}
		_, err := outbox.Add(ctx, q, outbox.Event{ClubID: clubID, Type: "notifications.changed", Data: map[string]any{}})
		return err
	})
	if err != nil {
		return fmt.Errorf("health: record: %w", err)
	}
	j.writes.Add(n)
	return nil
}
