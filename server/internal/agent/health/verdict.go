package health

// Вердикт исправности ПК за неделю (Этап 7, docs/agent-protocol.md §16.7, S7-H-15): «Исправен» / «Наблюдать» /
// «Требует ремонта» по прозрачным правилам над недельными агрегатами почасовой истории. Считает сервер (пороги —
// agent.health.*, одинаковы для всех клубов); панель показывает уровень и причины (код причины + числа для подстановки
// в перевод).

// Уровни вердикта (по возрастанию тяжести).
const (
	VerdictOK     = "ok"
	VerdictWatch  = "watch"
	VerdictRepair = "repair"
)

// Коды причин вердикта (API: HealthVerdictReason.code).
const (
	ReasonSmartFailing = "smart_failing" // SMART предсказывает отказ диска → ремонт
	ReasonBugchecks    = "bugchecks"     // «синие экраны» за неделю ≥ порога → ремонт
	ReasonCPUHot       = "cpu_hot"       // часов с CPU ≥ порога перегрева больше HotHours (больше HotHoursRepair — ремонт)
	ReasonGPUHot       = "gpu_hot"       // то же для GPU
	ReasonSmartWarning = "smart_warning" // SMART предупреждает → наблюдать
	ReasonShutdowns    = "shutdowns"     // внезапных выключений за неделю ≥ порога → наблюдать
	ReasonDiskLow      = "disk_low"      // на системном диске было свободно меньше порога → наблюдать
	ReasonCPUTempRise  = "cpu_temp_rise" // средняя температура CPU выросла к прошлой неделе на ≥ порога → наблюдать
	ReasonGPUTempRise  = "gpu_temp_rise" // то же для GPU
	ReasonNetworkSlow  = "network_slow"  // бездисковый ПК: часов с медленной сетью больше NetworkSlowHours → наблюдать
)

// VerdictRules — пороги вердикта (agent.health.*). Нули — DefaultVerdictRules.
type VerdictRules struct {
	// Thresholds — общие с проблемами пороги: CPUTempC, GPUTempC (перегрев, те же, что у cpu_hot/gpu_hot),
	// SystemDiskFreePercent («наблюдать», если на системном диске было свободно меньше процента) и LinkFullMbps (порог
	// медленной сети бездискового ПК, тот же, что у network_slow, — для текста причины). Остальные поля не используются.
	Thresholds
	// HotHours — «наблюдать», если часов с максимумом температуры ≥ порога перегрева больше этого.
	HotHours int64
	// HotHoursRepair — «ремонт», если таких часов больше этого (хронический перегрев: чистка, термопаста, охлаждение).
	HotHoursRepair int64
	// Shutdowns — «наблюдать» при стольких внезапных выключениях за неделю (≥).
	Shutdowns int64
	// Bugchecks — «ремонт» при стольких «синих экранах» за неделю (≥).
	Bugchecks int64
	// TempRiseC — «наблюдать», если средняя температура CPU/GPU выросла к прошлой неделе на столько °C (≥).
	TempRiseC int32
	// NetworkSlowHours — бездисковый ПК: «наблюдать», если часов с открытой проблемой network_slow больше этого.
	NetworkSlowHours int64
}

// DefaultVerdictRules — пороги по умолчанию (обоснование — docs/open-questions/stage7-health.md S7-H-15); общие с
// проблемами — Defaults().
func DefaultVerdictRules() VerdictRules {
	return VerdictRules{Thresholds: Defaults(), HotHours: 2, HotHoursRepair: 24, Shutdowns: 3, Bugchecks: 1, TempRiseC: 5,
		NetworkSlowHours: 2}
}

// orDefault — v, а если он не задан (≤ 0), — d.
func orDefault[T int32 | int64 | uint32 | uint64](v, d T) T {
	if v <= 0 {
		return d
	}
	return v
}

// WithDefaults — нулевые пороги заменены значениями по умолчанию.
func (r VerdictRules) WithDefaults() VerdictRules {
	d := DefaultVerdictRules()
	r.CPUTempC, r.GPUTempC = orDefault(r.CPUTempC, d.CPUTempC), orDefault(r.GPUTempC, d.GPUTempC)
	r.SystemDiskFreePercent = orDefault(r.SystemDiskFreePercent, d.SystemDiskFreePercent)
	r.LinkFullMbps = orDefault(r.LinkFullMbps, d.LinkFullMbps)
	r.HotHours, r.HotHoursRepair = orDefault(r.HotHours, d.HotHours), orDefault(r.HotHoursRepair, d.HotHoursRepair)
	r.Shutdowns, r.Bugchecks = orDefault(r.Shutdowns, d.Shutdowns), orDefault(r.Bugchecks, d.Bugchecks)
	r.TempRiseC = orDefault(r.TempRiseC, d.TempRiseC)
	r.NetworkSlowHours = orDefault(r.NetworkSlowHours, d.NetworkSlowHours)
	return r
}

// Week — недельные агрегаты ПК (ComputerHealthWeekly). -1 — не измерено.
type Week struct {
	CPUTempAvgDeci, GPUTempAvgDeci int64
	CPUHotHours, GPUHotHours       int64
	Shutdowns, Bugchecks           int64
	// SystemDiskFreeBP — наименьшая доля свободного места на системном диске, сотые доли процента (-1 — нет данных).
	SystemDiskFreeBP int64
	// SmartRank — худший SMART: 0 не измерено, 1 ok, 2 warning, 3 failing.
	SmartRank int
	// NetworkSlowHours — часов в сети с открытой проблемой network_slow.
	NetworkSlowHours int64
	// Diskless — бездисковый ПК: сеть — причина network_slow (S7-H-18). Данных дисков у него нет (SmartRank 0,
	// SystemDiskFreeBP -1): их отбрасывает выборка недель (ComputerHealthWeekly diskless) — причин по дискам нет.
	Diskless bool
}

// Reason — причина вердикта: код, уровень и числа для текста («Перегрев CPU (≥ 90 °C): 5 ч»).
type Reason struct {
	Code  string
	Level string
	// Value — значение (часы, штуки, °C, проценты); Threshold — порог правила.
	Value, Threshold int64
}

// Verdict — итог недели.
type Verdict struct {
	Level   string
	Reasons []Reason
}

// Judge — вердикт недели w; prev — предыдущая неделя (nil — нет данных) для тренда температуры. Причины — в порядке
// правил (сначала «ремонт»); уровень — наихудший из причин.
func Judge(w Week, prev *Week, r VerdictRules) Verdict {
	r = r.WithDefaults()
	v := Verdict{Level: VerdictOK, Reasons: []Reason{}}
	add := func(code, level string, value, threshold int64) {
		v.Reasons = append(v.Reasons, Reason{Code: code, Level: level, Value: value, Threshold: threshold})
		if level == VerdictRepair || (level == VerdictWatch && v.Level == VerdictOK) {
			v.Level = level
		}
	}
	if w.SmartRank >= smartRankFailing {
		add(ReasonSmartFailing, VerdictRepair, 0, 0)
	}
	if w.Bugchecks >= r.Bugchecks {
		add(ReasonBugchecks, VerdictRepair, w.Bugchecks, r.Bugchecks)
	}
	hot := func(code string, hours int64, limitC int32) {
		switch {
		case hours > r.HotHoursRepair:
			add(code, VerdictRepair, hours, int64(limitC))
		case hours > r.HotHours:
			add(code, VerdictWatch, hours, int64(limitC))
		}
	}
	hot(ReasonCPUHot, w.CPUHotHours, r.CPUTempC)
	hot(ReasonGPUHot, w.GPUHotHours, r.GPUTempC)
	if w.SmartRank == smartRankWarning {
		add(ReasonSmartWarning, VerdictWatch, 0, 0)
	}
	if w.Shutdowns >= r.Shutdowns {
		add(ReasonShutdowns, VerdictWatch, w.Shutdowns, r.Shutdowns)
	}
	// Свободно < порога: free·100 < total·percent, доля уже в сотых процента (bp < percent·100) — без float.
	diskPercent := int64(min(r.SystemDiskFreePercent, 100)) // #nosec G115 -- ≤ 100
	if w.SystemDiskFreeBP >= 0 && w.SystemDiskFreeBP < diskPercent*100 {
		add(ReasonDiskLow, VerdictWatch, w.SystemDiskFreeBP/100, diskPercent)
	}
	// Медленная сеть — «наблюдать», а не «ремонт»: частая причина — кабель или порт коммутатора, а не ПК.
	if w.Diskless && w.NetworkSlowHours > r.NetworkSlowHours {
		add(ReasonNetworkSlow, VerdictWatch, w.NetworkSlowHours, int64(r.LinkFullMbps))
	}
	if prev != nil {
		rise := func(code string, cur, before int64) {
			if cur > 0 && before > 0 && cur-before >= int64(r.TempRiseC)*10 {
				add(code, VerdictWatch, (cur-before)/10, int64(r.TempRiseC))
			}
		}
		rise(ReasonCPUTempRise, w.CPUTempAvgDeci, prev.CPUTempAvgDeci)
		rise(ReasonGPUTempRise, w.GPUTempAvgDeci, prev.GPUTempAvgDeci)
	}
	return v
}
