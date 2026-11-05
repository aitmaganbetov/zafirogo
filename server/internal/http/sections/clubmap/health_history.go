package clubmap

// Здоровье ПК по неделям и по часам (Этап 7, новое — в Laravel не было; docs/agent-protocol.md §16.6–16.7,
// docs/open-questions/stage7-health.md S7-H-13…S7-H-16): агрегаты почасовой истории computer_health_hourly SQL-ом
// (диапазоном по hour, недели — в поясе клуба) и вердикт исправности за неделю (health.Judge). Право — как у состояния
// ПК (club_map.view); клуб — из аутентификации, чужой ПК — 404.

import (
	"context"
	"strconv"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	clubmapapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Лимиты параметров.
const (
	defaultHealthWeeks = 8
	maxHealthWeeks     = 12
	// maxHourlySpan — график по часам: неделя плюс запас на переход часов (DST) и границы недели в чужом поясе.
	maxHourlySpan = 8 * 24 * time.Hour
)

// weekBounds — границы weeks+1 недель по возрастанию: неделя до показываемых (тренд), показываемые и конец текущей
// (cur — начало текущей недели в поясе клуба; AddDate в поясе — переход на летнее время учтён).
func weekBounds(cur time.Time, weeks int) []time.Time {
	out := make([]time.Time, 0, weeks+2)
	for i := weeks; i >= -1; i-- {
		out = append(out, cur.AddDate(0, 0, -7*i))
	}
	return out
}

// opt — нет измерения (NULL в SQL) → null в API.
func opt[T int32 | int64](v *T) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

// orUnmeasured — значение для health.Week: нет измерения — -1.
func orUnmeasured[T int32 | int64](v *T) int64 {
	if v == nil {
		return -1
	}
	return int64(*v)
}

func weekOf(r store.ComputerHealthWeeklyRow, diskless bool) health.Week {
	return health.Week{
		CPUTempAvgDeci: orUnmeasured(r.CpuTempAvgDeci), GPUTempAvgDeci: orUnmeasured(r.GpuTempAvgDeci),
		CPUHotHours: r.CpuHotHours, GPUHotHours: r.GpuHotHours, Shutdowns: r.UnexpectedShutdowns, Bugchecks: r.Bugchecks,
		SystemDiskFreeBP: orUnmeasured(r.SystemDiskFreeBp), SmartRank: int(r.SmartRank), NetworkSlowHours: r.NetworkSlowHours,
		Diskless: diskless,
	}
}

func verdictOf(v health.Verdict) clubmapapi.HealthVerdict {
	out := clubmapapi.HealthVerdict{Level: clubmapapi.HealthVerdictLevel(v.Level), Reasons: make([]clubmapapi.HealthVerdictReason, 0, len(v.Reasons))}
	for _, r := range v.Reasons {
		out.Reasons = append(out.Reasons, clubmapapi.HealthVerdictReason{Code: clubmapapi.HealthVerdictReasonCode(r.Code),
			Level: clubmapapi.HealthVerdictReasonLevel(r.Level), Value: r.Value, Threshold: r.Threshold})
	}
	return out
}

// GetComputerHealthWeekly — недели ПК (новые сверху) с вердиктом; тренд — к предыдущей неделе (она читается тем же
// запросом, но не показывается, если выходит за weeks). Данные дисков бездискового ПК отбрасывает запрос (diskless).
func (h *handlers) GetComputerHealthWeekly(ctx context.Context, req clubmapapi.GetComputerHealthWeeklyRequestObject) (clubmapapi.GetComputerHealthWeeklyResponseObject, error) {
	weeks := defaultHealthWeeks
	if w := req.Params.Weeks; w != nil {
		switch {
		case *w < 1:
			return nil, httpx.RuleErrorWith("weeks", cash.RuleMin, "1")
		case *w > maxHealthWeeks:
			return nil, httpx.RuleErrorWith("weeks", cash.RuleMax, strconv.Itoa(maxHealthWeeks))
		}
		weeks = int(*w)
	}
	clubID, now := httpx.ClubID(ctx), h.d.NowFunc()
	var (
		rows     []store.ComputerHealthWeeklyRow
		bounds   []time.Time
		diskless bool
	)
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if diskless, err = healthComputer(ctx, q, clubID, req.ComputerId); err != nil {
			return err
		}
		info, err := club.Load(ctx, q, clubID) // неизвестный пояс — UTC (CurrentClub::timezone())
		if err != nil {
			return err
		}
		bounds = weekBounds(info.StartOfWeek(now), weeks)
		rows, err = q.ComputerHealthWeekly(ctx, store.ComputerHealthWeeklyParams{
			Bounds: bounds, CpuHotDeci: h.verdict.CPUTempC * 10, GpuHotDeci: h.verdict.GPUTempC * 10, Diskless: diskless,
			HiddenKinds: hiddenKinds(diskless), ClubID: clubID, ComputerID: req.ComputerId,
			FromAt: bounds[0], ToAt: bounds[len(bounds)-1],
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	byIndex := make(map[int32]health.Week, len(rows))
	for _, r := range rows {
		byIndex[r.WeekIndex] = weekOf(r, diskless)
	}
	out := clubmapapi.HealthWeekPage{Diskless: diskless, Weeks: make([]clubmapapi.HealthWeek, 0, len(rows))}
	for _, r := range rows {
		// Интервал 1 — неделя до показываемых (только для тренда); вне границ (не бывает: выборка — тем же диапазоном) —
		// пропуск.
		if r.WeekIndex < 2 || int(r.WeekIndex) >= len(bounds) {
			continue
		}
		var prev *health.Week
		if p, ok := byIndex[r.WeekIndex-1]; ok {
			prev = &p
		}
		w := clubmapapi.HealthWeek{
			Start: bounds[r.WeekIndex-1], End: bounds[r.WeekIndex], OnlineMinutes: r.OnlineMinutes, SessionMinutes: r.SessionMinutes,
			CpuLoadAvgBp: opt(r.CpuLoadAvgBp), CpuLoadMaxBp: opt(r.CpuLoadMaxBp),
			RamAvgBp: opt(r.RamAvgBp), RamMaxBp: opt(r.RamMaxBp),
			GpuLoadAvgBp: opt(r.GpuLoadAvgBp), GpuLoadMaxBp: opt(r.GpuLoadMaxBp),
			CpuTempAvgDeci: opt(r.CpuTempAvgDeci), CpuTempMaxDeci: opt(r.CpuTempMaxDeci),
			GpuTempAvgDeci: opt(r.GpuTempAvgDeci), GpuTempMaxDeci: opt(r.GpuTempMaxDeci),
			CpuHotHours: r.CpuHotHours, GpuHotHours: r.GpuHotHours, ProblemHours: r.ProblemHours, NetworkSlowHours: r.NetworkSlowHours,
			UnexpectedShutdowns: r.UnexpectedShutdowns, Bugchecks: r.Bugchecks, PeripheralDisconnects: r.PeripheralDisconnects,
			SystemDiskFreeMb: opt(r.SystemDiskFreeMb), SystemDiskFreeBp: opt(r.SystemDiskFreeBp),
			Verdict: verdictOf(health.Judge(byIndex[r.WeekIndex], prev, h.verdict)),
		}
		if s := health.SmartName(int(r.SmartRank)); s != "" {
			v := clubmapapi.HealthWeekSmart(s)
			w.Smart = &v
		}
		out.Weeks = append(out.Weeks, w)
	}
	return clubmapapi.GetComputerHealthWeekly200JSONResponse(out), nil
}

// GetComputerHealthHourly — точки по часам за [from, to) (≤ 8 суток), по возрастанию времени.
func (h *handlers) GetComputerHealthHourly(ctx context.Context, req clubmapapi.GetComputerHealthHourlyRequestObject) (clubmapapi.GetComputerHealthHourlyResponseObject, error) {
	from, to := req.Params.From.UTC(), req.Params.To.UTC()
	if !to.After(from) {
		return nil, httpx.RuleErrorWith("to", cash.RuleGt, "from")
	}
	if to.Sub(from) > maxHourlySpan {
		return nil, httpx.RuleErrorWith("to", "before_or_equal", "from + 8 days")
	}
	clubID := httpx.ClubID(ctx)
	var (
		rows     []store.ComputerHealthHourlyPointsRow
		diskless bool
	)
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if diskless, err = healthComputer(ctx, q, clubID, req.ComputerId); err != nil {
			return err
		}
		rows, err = q.ComputerHealthHourlyPoints(ctx, store.ComputerHealthHourlyPointsParams{
			ClubID: clubID, ComputerID: req.ComputerId, FromAt: from, ToAt: to})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := clubmapapi.HealthHourPage{Diskless: diskless, Items: make([]clubmapapi.HealthHour, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, clubmapapi.HealthHour{
			Hour: r.Hour, OnlineMinutes: int64(r.OnlineMinutes), SessionMinutes: int64(r.SessionMinutes),
			CpuLoadAvgBp: opt(r.CpuLoadAvgBp), CpuLoadMaxBp: opt(r.CpuLoadMaxBp),
			RamAvgBp: opt(r.RamAvgBp), RamMaxBp: opt(r.RamMaxBp),
			GpuLoadAvgBp: opt(r.GpuLoadAvgBp), GpuLoadMaxBp: opt(r.GpuLoadMaxBp),
			CpuTempAvgDeci: opt(r.CpuTempAvgDeci), CpuTempMaxDeci: opt(r.CpuTempMaxDeci),
			GpuTempAvgDeci: opt(r.GpuTempAvgDeci), GpuTempMaxDeci: opt(r.GpuTempMaxDeci),
		})
	}
	return clubmapapi.GetComputerHealthHourly200JSONResponse(out), nil
}
