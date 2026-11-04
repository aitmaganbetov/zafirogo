package app

import (
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/stream"
	"github.com/aitmaganbetov/zafirogo/server/internal/config"
)

// healthOptions — agent.health.* → stream.HealthOptions (Этап 7, здоровье ПК; docs/open-questions/stage7-health.md).
func healthOptions(h config.AgentHealth) stream.HealthOptions {
	ping := h.PingInterval
	if ping == 0 {
		ping = -1 // 0 в конфигурации — «не мерить»; 0 в stream — «по умолчанию»
	}
	return stream.HealthOptions{
		Disabled: !h.Enabled,
		Thresholds: health.Thresholds{
			CPUTempC: h.CPUTempC, GPUTempC: h.GPUTempC, TempHysteresisC: h.TempHysteresisC,
			SystemDiskFreePercent: h.SystemDiskFreePercent, SystemDiskFreeGB: h.SystemDiskFreeGB,
			OtherDiskFreePercent: h.OtherDiskFreePercent, LinkFullMbps: h.LinkFullMbps, LinkMinMbps: h.LinkMinMbps,
			UnstableShutdowns: h.UnstableShutdowns, UnstableBugchecks: h.UnstableBugchecks,
			RaiseAfter: h.RaiseAfter, ClearAfter: h.ClearAfter,
		},
		LiveMinGap:       h.LiveMinGap,
		LiveLoadStepBP:   h.LiveLoadStepPercent * 100,
		LiveTempStepDeci: h.LiveTempStepC * 10,
		PingInterval:     ping,
	}
}

// verdictRules — agent.health.* → пороги вердикта исправности ПК за неделю (health.VerdictRules, S7-H-15).
func verdictRules(h config.AgentHealth) health.VerdictRules {
	return health.VerdictRules{
		CPUTempC: h.CPUTempC, GPUTempC: h.GPUTempC, HotHours: h.VerdictHotHours, HotHoursRepair: h.VerdictHotHoursRepair,
		Shutdowns: h.VerdictShutdowns, Bugchecks: h.VerdictBugchecks,
		SystemDiskFreePercent: int64(h.SystemDiskFreePercent), // #nosec G115 -- ≤ 90 (config.validateHealth)
		TempRiseC:             h.VerdictTempRiseC,
		NetworkSlowHours:      h.VerdictNetworkSlowHours,
		LinkFullMbps:          int64(h.LinkFullMbps),
	}
}
