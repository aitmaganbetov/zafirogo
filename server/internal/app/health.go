package app

import (
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/stream"
	"github.com/aitmaganbetov/zafirogo/server/internal/config"
)

// thresholds — agent.health.* → пороги проблем (общие для стрима и вердикта недели).
func thresholds(h config.AgentHealth) health.Thresholds {
	return health.Thresholds{
		CPUTempC: h.CPUTempC, GPUTempC: h.GPUTempC, TempHysteresisC: h.TempHysteresisC,
		SystemDiskFreePercent: h.SystemDiskFreePercent, SystemDiskFreeGB: h.SystemDiskFreeGB,
		OtherDiskFreePercent: h.OtherDiskFreePercent, LinkFullMbps: h.LinkFullMbps, LinkMinMbps: h.LinkMinMbps,
		UnstableShutdowns: h.UnstableShutdowns, UnstableBugchecks: h.UnstableBugchecks,
		RaiseAfter: h.RaiseAfter, ClearAfter: h.ClearAfter,
	}
}

// healthOptions — agent.health.* → stream.HealthOptions (Этап 7, здоровье ПК; docs/open-questions/stage7-health.md).
func healthOptions(h config.AgentHealth) stream.HealthOptions {
	ping := h.PingInterval
	if ping == 0 {
		ping = -1 // 0 в конфигурации — «не мерить»; 0 в stream — «по умолчанию»
	}
	return stream.HealthOptions{
		Disabled:         !h.Enabled,
		Thresholds:       thresholds(h),
		LiveMinGap:       h.LiveMinGap,
		LiveLoadStepBP:   h.LiveLoadStepPercent * 100,
		LiveTempStepDeci: h.LiveTempStepC * 10,
		PingInterval:     ping,
	}
}

// verdictRules — agent.health.* → пороги вердикта исправности ПК за неделю (health.VerdictRules, S7-H-15).
func verdictRules(h config.AgentHealth) health.VerdictRules {
	return health.VerdictRules{
		Thresholds: thresholds(h), HotHours: h.VerdictHotHours, HotHoursRepair: h.VerdictHotHoursRepair,
		Shutdowns: h.VerdictShutdowns, Bugchecks: h.VerdictBugchecks, TempRiseC: h.VerdictTempRiseC,
		NetworkSlowHours: h.VerdictNetworkSlowHours,
	}
}
