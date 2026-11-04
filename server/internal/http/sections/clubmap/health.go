package clubmap

// Состояние ПК (Этап 7, новое — в Laravel не было; docs/open-questions/stage7-health.md): загрузка и здоровье из
// присутствия агента (Valkey), история проблем из computer_health_events. Право — как у карты клуба (club_map.view).

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	clubmapapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// presenceGetter — снимок одного ПК (presence.Store.Get) без чтения всего клуба.
type presenceGetter interface {
	Get(ctx context.Context, clubID, computerID int64) (presence.State, bool, error)
}

// onePresence — присутствие ПК клуба; ok = false — ПК не в сети (или Valkey недоступен: карточка без данных агента).
func (h *handlers) onePresence(ctx context.Context, clubID, computerID int64) (presence.State, bool) {
	if h.d.Presence == nil {
		return presence.State{}, false
	}
	if g, ok := h.d.Presence.(presenceGetter); ok {
		st, found, err := g.Get(ctx, clubID, computerID)
		if err != nil {
			h.d.Log().WarnContext(ctx, "computer health: presence", "err", err)
			return presence.State{}, false
		}
		return st, found && st.Online
	}
	st, ok := h.presenceByID(ctx, clubID)[computerID]
	return st, ok
}

func problemsOf(kinds []string) []clubmapapi.HealthProblem {
	out := make([]clubmapapi.HealthProblem, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, clubmapapi.HealthProblem(k))
	}
	return out
}

func loadOf(st presence.State) *clubmapapi.ComputerLoad {
	d := clubmap.LoadDataOf(st)
	if d.Load == nil {
		return nil
	}
	return &clubmapapi.ComputerLoad{CpuBp: int64(d.Load.CPUBasisPoints), RamBp: int64(d.Load.RAMBasisPoints), IdleSeconds: int64(d.Load.IdleSeconds)}
}

func nonZero[T int32 | uint32 | uint64](v T) *int64 {
	if v == 0 {
		return nil
	}
	n := int64(v) // #nosec G115 -- значения проверены stream.validateHealth (≤ 1 ПиБ, ≤ 150 °C)
	return &n
}

func snapshotOf(h *presence.Health) *clubmapapi.HealthSnapshot {
	if h == nil {
		return nil
	}
	out := &clubmapapi.HealthSnapshot{
		ReportedAt: h.ReportedAt, CpuTempDeci: nonZero(h.CPUTempDeci), GpuTempDeci: nonZero(h.GPUTempDeci),
		LinkMbps: nonZero(h.LinkMbps), UptimeSeconds: nonZero(h.UptimeSeconds),
		UnexpectedShutdowns7d: int64(h.Shutdowns7d), Bugchecks7d: int64(h.Bugchecks7d),
		Disks: make([]clubmapapi.DiskView, 0, len(h.Disks)), Peripherals: make([]clubmapapi.PeripheralView, 0, len(h.Peripherals)),
	}
	if h.GPUMeasured {
		l, m := int64(h.GPULoadBP), int64(h.GPUMemoryBP)
		out.GpuLoadBp, out.GpuMemoryBp = &l, &m
	}
	for _, d := range h.Disks {
		v := clubmapapi.DiskView{Mount: d.Mount, System: d.System, TotalMb: int64(d.TotalMB), FreeMb: int64(d.FreeMB)} // #nosec G115 -- ≤ 1 ПиБ в МБ
		if d.Smart != "" {
			s := clubmapapi.DiskViewSmart(d.Smart)
			v.Smart = &s
		}
		out.Disks = append(out.Disks, v)
	}
	for _, p := range h.Peripherals {
		out.Peripherals = append(out.Peripherals, clubmapapi.PeripheralView{Kind: clubmapapi.PeripheralViewKind(p.Kind), Connected: p.Connected, Name: p.Name})
	}
	return out
}

// healthComputer — ПК принадлежит клубу из аутентификации (иначе 404); diskless — бездисковый (действующий, иначе
// последний выпущенный сертификат — по ключу образа; то же правило, что у стрима, S7-H-18).
func healthComputer(ctx context.Context, q *store.Queries, clubID, computerID int64) (diskless bool, err error) {
	diskless, err = q.ComputerHealthComputer(ctx, store.ComputerHealthComputerParams{ClubID: clubID, ComputerID: computerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, clubmap.ErrNotFound
	}
	return diskless, err
}

// computerExists — healthComputer в своей транзакции.
func (h *handlers) computerExists(ctx context.Context, clubID, computerID int64) (diskless bool, err error) {
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		diskless, err = healthComputer(ctx, q, clubID, computerID)
		return err
	})
	return diskless, err
}

// GetComputerHealth — состояние ПК: загрузка, здоровье, проблемы, время ответа агента.
func (h *handlers) GetComputerHealth(ctx context.Context, req clubmapapi.GetComputerHealthRequestObject) (clubmapapi.GetComputerHealthResponseObject, error) {
	clubID := httpx.ClubID(ctx)
	diskless, err := h.computerExists(ctx, clubID, req.ComputerId)
	if err != nil {
		return nil, err
	}
	out := clubmapapi.ComputerHealthView{ComputerId: req.ComputerId, Connection: clubmapapi.ComputerHealthViewConnectionOffline,
		Diskless: diskless, Problems: []clubmapapi.HealthProblem{}}
	if st, ok := h.onePresence(ctx, clubID, req.ComputerId); ok {
		out.Connection = clubmapapi.ComputerHealthViewConnectionOnline
		if st.AgentVersion != "" {
			v := st.AgentVersion
			out.AgentVersion = &v
		}
		out.RttMs = nonZero(st.RTTMillis)
		out.Load = loadOf(st)
		if diskless && st.Health != nil && len(st.Health.Disks) > 0 {
			// Снимок записан до того, как ПК стал бездисковым (подключение по прежнему сертификату): тома не показываем.
			hh := *st.Health
			out.Health = snapshotOf(hh.WithoutDisks())
		} else {
			out.Health = snapshotOf(st.Health)
		}
		out.Problems = problemsOf(st.Problems)
	}
	return clubmapapi.GetComputerHealth200JSONResponse(out), nil
}

// healthCursor — keyset истории (raised_at, id) последней строки страницы.
type healthCursor struct {
	At time.Time `json:"at"`
	ID int64     `json:"id"`
}

// ListComputerHealthEvents — история проблем ПК, новые сверху.
func (h *handlers) ListComputerHealthEvents(ctx context.Context, req clubmapapi.ListComputerHealthEventsRequestObject) (clubmapapi.ListComputerHealthEventsResponseObject, error) {
	clubID := httpx.ClubID(ctx)
	var cur healthCursor
	if err := httpx.DecodeCursor(req.Params.Cursor, &cur); err != nil {
		return nil, err
	}
	size := httpx.PageSize(req.Params.Limit)
	var rows []store.ComputerHealthHistoryRow
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		diskless, err := healthComputer(ctx, q, clubID, req.ComputerId)
		if err != nil {
			return err
		}
		p := store.ComputerHealthHistoryParams{ClubID: clubID, ComputerID: req.ComputerId, Diskless: diskless, PageSize: size + 1}
		if cur.ID > 0 {
			p.BeforeAt, p.BeforeID = &cur.At, &cur.ID
		}
		rows, err = q.ComputerHealthHistory(ctx, p)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := clubmapapi.HealthEventPage{Items: make([]clubmapapi.HealthEvent, 0, min(len(rows), int(size)))}
	for i, r := range rows {
		if i == int(size) {
			last := rows[i-1]
			out.NextCursor = httpx.EncodeCursor(healthCursor{At: last.RaisedAt, ID: last.ID})
			break
		}
		ev := clubmapapi.HealthEvent{Id: r.ID, Kind: clubmapapi.HealthProblem(r.Kind), RaisedAt: r.RaisedAt, ClearedAt: r.ClearedAt,
			Details: map[string]any{}}
		_ = json.Unmarshal(r.Details, &ev.Details)
		out.Items = append(out.Items, ev)
	}
	return clubmapapi.ListComputerHealthEvents200JSONResponse(out), nil
}
