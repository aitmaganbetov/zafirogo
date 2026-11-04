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

// weekBounds — границы weeks+1 недель по возрастанию: неделя до показываемых (тренд), показываемые и конец текущей
// (cur — начало текущей недели в поясе клуба; AddDate в поясе — переход на летнее время учтён).
func weekBounds(cur time.Time, weeks int) []time.Time {
	out := make([]time.Time, 0, weeks+2)
	for i := weeks; i >= -1; i-- {
		out = append(out, cur.AddDate(0, 0, -7*i))
	}
	return out
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

func weekOf(r store.ComputerHealthWeeklyRow, diskless bool) health.Week {
	return health.Week{
		CPUTempAvgDeci: int64(r.CpuTempAvgDeci), GPUTempAvgDeci: int64(r.GpuTempAvgDeci),
		CPUHotHours: r.CpuHotHours, GPUHotHours: r.GpuHotHours, Shutdowns: r.UnexpectedShutdowns, Bugchecks: r.Bugchecks,
		SystemDiskFreeBP: r.SystemDiskFreeBp, SmartRank: int(r.SmartRank), NetworkSlowHours: r.NetworkSlowHours,
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
		rows     []store.ComputerHealthWeeklyRow
		loc      = time.UTC
		bounds   []time.Time
		diskless bool
	)
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if diskless, err = healthComputer(ctx, q, clubID, req.ComputerId); err != nil {
			return err
		}
		club, err := q.ClubLocaleTimezone(ctx, clubID)
		if err != nil {
			return err
		}
		if l, err := time.LoadLocation(club.Timezone); err == nil {
			loc = l // неизвестный пояс — UTC (CurrentClub::timezone())
		}
		bounds = weekBounds(weekStartIn(now, loc), weeks)
		rows, err = q.ComputerHealthWeekly(ctx, store.ComputerHealthWeeklyParams{
			Bounds: bounds, CpuHotDeci: rules.CPUTempC * 10, GpuHotDeci: rules.GPUTempC * 10, Diskless: diskless,
			ClubID: clubID, ComputerID: req.ComputerId, FromAt: bounds[0], ToAt: bounds[len(bounds)-1],
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
		start := bounds[r.WeekIndex-1]
		var prev *health.Week
		if p, ok := byIndex[r.WeekIndex-1]; ok {
			prev = &p
		}
		w := clubmapapi.HealthWeek{
			Start: start, End: bounds[r.WeekIndex], OnlineMinutes: r.OnlineMinutes, SessionMinutes: r.SessionMinutes,
			CpuLoadAvgBp: optInt(r.CpuLoadAvgBp), CpuLoadMaxBp: optInt(r.CpuLoadMaxBp),
			RamAvgBp: optInt(r.RamAvgBp), RamMaxBp: optInt(r.RamMaxBp),
			GpuLoadAvgBp: optInt(r.GpuLoadAvgBp), GpuLoadMaxBp: optInt(r.GpuLoadMaxBp),
			CpuTempAvgDeci: optInt(r.CpuTempAvgDeci), CpuTempMaxDeci: optInt(r.CpuTempMaxDeci),
			GpuTempAvgDeci: optInt(r.GpuTempAvgDeci), GpuTempMaxDeci: optInt(r.GpuTempMaxDeci),
			CpuHotHours: r.CpuHotHours, GpuHotHours: r.GpuHotHours, ProblemHours: r.ProblemHours, NetworkSlowHours: r.NetworkSlowHours,
			UnexpectedShutdowns: r.UnexpectedShutdowns, Bugchecks: r.Bugchecks, PeripheralDisconnects: r.PeripheralDisconnects,
			Verdict: verdictOf(health.Judge(weekOf(r, diskless), prev, rules)),
		}
		// Бездисковый ПК: дисков нет — поля системного диска и SMART не отдаются (старые строки истории — до перевода
		// ПК на загрузку по сети).
		if !diskless {
			w.SystemDiskFreeMb, w.SystemDiskFreeBp = optInt(r.SystemDiskFreeMb), optInt(r.SystemDiskFreeBp)
		}
		if s := health.SmartName(int(r.SmartRank)); s != "" && !diskless {
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
			CpuLoadAvgBp: optPtr(r.CpuLoadAvgBp), CpuLoadMaxBp: optPtr(r.CpuLoadMaxBp),
			RamAvgBp: optPtr(r.RamAvgBp), RamMaxBp: optPtr(r.RamMaxBp),
			GpuLoadAvgBp: optPtr(r.GpuLoadAvgBp), GpuLoadMaxBp: optPtr(r.GpuLoadMaxBp),
			CpuTempAvgDeci: optPtr(r.CpuTempAvgDeci), CpuTempMaxDeci: optPtr(r.CpuTempMaxDeci),
			GpuTempAvgDeci: optPtr(r.GpuTempAvgDeci), GpuTempMaxDeci: optPtr(r.GpuTempMaxDeci),
		})
	}
	return clubmapapi.GetComputerHealthHourly200JSONResponse(out), nil
}
