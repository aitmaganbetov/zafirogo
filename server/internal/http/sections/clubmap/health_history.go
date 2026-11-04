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
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
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

// weekStartIn — понедельник 00:00 недели момента t в поясе loc (как date_trunc('week', … AT TIME ZONE tz) в SQL).
func weekStartIn(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	d := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

// optInt — -1 (нет измерения в SQL) → null.
func optInt[T int32 | int64](v T) *int64 {
	if v < 0 {
		return nil
	}
	n := int64(v)
	return &n
}

func optPtr(v *int32) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

func weekOf(r store.ComputerHealthWeeklyRow) health.Week {
	return health.Week{
		CPUTempAvgDeci: int64(r.CpuTempAvgDeci), GPUTempAvgDeci: int64(r.GpuTempAvgDeci),
		CPUHotHours: r.CpuHotHours, GPUHotHours: r.GpuHotHours, Shutdowns: r.UnexpectedShutdowns, Bugchecks: r.Bugchecks,
		SystemDiskFreeBP: r.SystemDiskFreeBp, SmartRank: int(r.SmartRank),
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
// запросом, но не показывается, если выходит за weeks).
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
	rules := h.d.HealthVerdict.WithDefaults()
	var (
		rows []store.ComputerHealthWeeklyRow
		loc  = time.UTC
	)
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		ok, err := q.ComputerHealthComputerExists(ctx, store.ComputerHealthComputerExistsParams{ClubID: clubID, ComputerID: req.ComputerId})
		if err != nil {
			return err
		}
		if !ok {
			return clubmap.ErrNotFound
		}
		club, err := q.ClubLocaleTimezone(ctx, clubID)
		if err != nil {
			return err
		}
		if l, err := time.LoadLocation(club.Timezone); err == nil {
			loc = l // неизвестный пояс — UTC (CurrentClub::timezone())
		}
		cur := weekStartIn(now, loc)
		rows, err = q.ComputerHealthWeekly(ctx, store.ComputerHealthWeeklyParams{
			Tz: loc.String(), CpuHotDeci: rules.CPUTempC * 10, GpuHotDeci: rules.GPUTempC * 10,
			ClubID: clubID, ComputerID: req.ComputerId,
			FromAt: cur.AddDate(0, 0, -7*weeks), ToAt: cur.AddDate(0, 0, 7), // +1 неделя раньше — для тренда
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	first := weekStartIn(now, loc).AddDate(0, 0, -7*(weeks-1))
	byDay := make(map[string]health.Week, len(rows))
	for _, r := range rows {
		byDay[r.WeekStart.Format(time.DateOnly)] = weekOf(r)
	}
	out := clubmapapi.HealthWeekPage{Weeks: make([]clubmapapi.HealthWeek, 0, len(rows))}
	for _, r := range rows {
		start := time.Date(r.WeekStart.Year(), r.WeekStart.Month(), r.WeekStart.Day(), 0, 0, 0, 0, loc)
		if start.Before(first) {
			continue
		}
		var prev *health.Week
		if p, ok := byDay[start.AddDate(0, 0, -7).Format(time.DateOnly)]; ok {
			prev = &p
		}
		w := clubmapapi.HealthWeek{
			Start: start, End: start.AddDate(0, 0, 7), OnlineMinutes: r.OnlineMinutes, SessionMinutes: r.SessionMinutes,
			CpuLoadAvgBp: optInt(r.CpuLoadAvgBp), CpuLoadMaxBp: optInt(r.CpuLoadMaxBp),
			RamAvgBp: optInt(r.RamAvgBp), RamMaxBp: optInt(r.RamMaxBp),
			GpuLoadAvgBp: optInt(r.GpuLoadAvgBp), GpuLoadMaxBp: optInt(r.GpuLoadMaxBp),
			CpuTempAvgDeci: optInt(r.CpuTempAvgDeci), CpuTempMaxDeci: optInt(r.CpuTempMaxDeci),
			GpuTempAvgDeci: optInt(r.GpuTempAvgDeci), GpuTempMaxDeci: optInt(r.GpuTempMaxDeci),
			CpuHotHours: r.CpuHotHours, GpuHotHours: r.GpuHotHours, ProblemHours: r.ProblemHours,
			UnexpectedShutdowns: r.UnexpectedShutdowns, Bugchecks: r.Bugchecks, PeripheralDisconnects: r.PeripheralDisconnects,
			SystemDiskFreeMb: optInt(r.SystemDiskFreeMb), SystemDiskFreeBp: optInt(r.SystemDiskFreeBp),
			Verdict: verdictOf(health.Judge(weekOf(r), prev, rules)),
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
	var rows []store.ComputerHealthHourlyPointsRow
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		ok, err := q.ComputerHealthComputerExists(ctx, store.ComputerHealthComputerExistsParams{ClubID: clubID, ComputerID: req.ComputerId})
		if err != nil {
			return err
		}
		if !ok {
			return clubmap.ErrNotFound
		}
		rows, err = q.ComputerHealthHourlyPoints(ctx, store.ComputerHealthHourlyPointsParams{
			ClubID: clubID, ComputerID: req.ComputerId, FromAt: from, ToAt: to})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := clubmapapi.HealthHourPage{Items: make([]clubmapapi.HealthHour, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, clubmapapi.HealthHour{
			Hour: r.Hour, OnlineMinutes: int64(r.OnlineMinutes), SessionMinutes: int64(r.SessionMinutes),
			CpuLoadAvgBp: optPtr(r.CpuLoadAvgBp), CpuLoadMaxBp: optPtr(r.CpuLoadMaxBp),
			RamAvgBp: optPtr(r.RamAvgBp), RamMaxBp: optPtr(r.RamMaxBp),
			GpuLoadAvgBp: optPtr(r.GpuLoadAvgBp), GpuLoadMaxBp: optPtr(r.GpuLoadMaxBp),
			CpuTempAvgDeci: optPtr(r.CpuTempAvgDeci), CpuTempMaxDeci: optPtr(r.CpuTempMaxDeci),
			GpuTempAvgDeci: optPtr(r.GpuTempAvgDeci), GpuTempMaxDeci: optPtr(r.GpuTempMaxDeci),
		})
	}
	return clubmapapi.GetComputerHealthHourly200JSONResponse(out), nil
}
