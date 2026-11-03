// Package health — проблемы здоровья ПК (Этап 7; в старом Laravel этого не было — новая функция по поручению
// владельца, docs/open-questions/stage7-health.md).
//
// Агент присылает измерения (Status.health, agent.proto ComputerHealth); проблемы считает СЕРВЕР по порогам
// (Thresholds, ключи конфигурации agent.health.*), чтобы пороги менялись без выпуска агента и были одинаковы для
// всех ПК. Путь:
//
//	Status.health → presence.Health (снимок в Valkey) → Evaluate (сырые проблемы по порогам, гистерезис температуры)
//	→ Tracker (дребезг: проблема держится RaiseAfter, прежде чем считаться появившейся; отсутствует ClearAfter,
//	прежде чем считаться ушедшей) → переходы → Journal (computer_health_events, только переходы).
//
// В Postgres не пишется каждый Status (CLAUDE.md «Производительность»): строка — когда подтверждённая проблема
// появилась (raised_at) и когда ушла (cleared_at).
package health

import (
	"slices"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// Виды проблем (computer_health_events.kind, API карты клуба).
const (
	KindCPUHot      = "cpu_hot"      // CPU горячее порога
	KindGPUHot      = "gpu_hot"      // GPU горячее порога
	KindDiskLow     = "disk_low"     // мало места на диске
	KindDiskSmart   = "disk_smart"   // SMART: предупреждение или предсказан отказ
	KindMouse       = "mouse"        // мышь не подключена
	KindKeyboard    = "keyboard"     // клавиатура не подключена
	KindMonitor     = "monitor"      // монитор не подключён
	KindNetworkSlow = "network_slow" // скорость сети упала
	KindUnstable    = "unstable"     // внезапные выключения / «синие экраны» за 7 суток
)

// Kinds — все виды проблем (проверка ввода, переводы).
var Kinds = []string{KindCPUHot, KindGPUHot, KindDiskLow, KindDiskSmart, KindMouse, KindKeyboard, KindMonitor,
	KindNetworkSlow, KindUnstable}

// Thresholds — пороги проблем (agent.health.*). Нули — значения по умолчанию (Defaults).
type Thresholds struct {
	// CPUTempC, GPUTempC — перегрев, °C (≥).
	CPUTempC, GPUTempC int32
	// TempHysteresisC — проблема перегрева уходит, когда температура ниже порога на столько °C.
	TempHysteresisC int32
	// SystemDiskFreePercent, SystemDiskFreeGB — системный диск: свободно меньше процента ИЛИ меньше ГБ.
	SystemDiskFreePercent, SystemDiskFreeGB uint64
	// OtherDiskFreePercent — остальные локальные диски: свободно меньше процента.
	OtherDiskFreePercent uint64
	// LinkFullMbps — «полная» скорость: если на этом подключении была ≥ неё, а стала ниже — проблема.
	LinkFullMbps uint32
	// LinkMinMbps — ниже этой скорости проблема всегда.
	LinkMinMbps uint32
	// UnstableShutdowns, UnstableBugchecks — нестабилен: внезапных выключений ≥ или «синих экранов» ≥ за 7 суток.
	UnstableShutdowns, UnstableBugchecks uint32
	// RaiseAfter — сколько проблема должна держаться, прежде чем считаться появившейся (запись в журнал).
	RaiseAfter time.Duration
	// ClearAfter — сколько проблемы не должно быть, прежде чем считать её ушедшей.
	ClearAfter time.Duration
}

// Defaults — пороги по умолчанию (поручение владельца; обоснование — docs/open-questions/stage7-health.md).
func Defaults() Thresholds {
	return Thresholds{
		CPUTempC: 90, GPUTempC: 85, TempHysteresisC: 5,
		SystemDiskFreePercent: 10, SystemDiskFreeGB: 10, OtherDiskFreePercent: 5,
		LinkFullMbps: 1000, LinkMinMbps: 100,
		UnstableShutdowns: 3, UnstableBugchecks: 1,
		RaiseAfter: 2 * time.Minute, ClearAfter: 5 * time.Minute,
	}
}

// Problems — сырые проблемы одного измерения: вид → подробности (computer_health_events.details).
type Problems map[string]map[string]any

// Sorted — виды проблем по возрастанию.
func (p Problems) Sorted() []string {
	out := make([]string, 0, len(p))
	for k := range p {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Evaluate — проблемы по порогам. active — проблемы, уже подтверждённые (для гистерезиса температуры: перегрев
// держится, пока температура не опустится ниже порога на TempHysteresisC). linkMax — наибольшая скорость сети,
// замеченная на этом подключении (0 — неизвестна). Не измеренное (0, «не задано») проблемой не считается.
func Evaluate(h *presence.Health, th Thresholds, active map[string]bool, linkMax uint32) Problems {
	out := Problems{}
	if h == nil {
		return out
	}
	hot := func(kind string, deci, limitC int32) {
		if deci <= 0 {
			return
		}
		limit := limitC * 10
		if active[kind] {
			limit -= th.TempHysteresisC * 10
		}
		if deci >= limit {
			out[kind] = map[string]any{"temp_deci": deci, "threshold_c": limitC}
		}
	}
	hot(KindCPUHot, h.CPUTempDeci, th.CPUTempC)
	hot(KindGPUHot, h.GPUTempDeci, th.GPUTempC)

	var low, smart []map[string]any
	for _, d := range h.Disks {
		if d.TotalMB > 0 && diskLow(d, th) {
			low = append(low, map[string]any{"mount": d.Mount, "system": d.System, "free_mb": d.FreeMB, "total_mb": d.TotalMB})
		}
		if d.Smart == presence.SmartWarning || d.Smart == presence.SmartFailing {
			smart = append(smart, map[string]any{"mount": d.Mount, "smart": d.Smart})
		}
	}
	if len(low) > 0 {
		out[KindDiskLow] = map[string]any{"disks": low}
	}
	if len(smart) > 0 {
		out[KindDiskSmart] = map[string]any{"disks": smart}
	}

	for _, pk := range []struct{ periph, kind string }{
		{presence.PeripheralMouse, KindMouse}, {presence.PeripheralKeyboard, KindKeyboard}, {presence.PeripheralMonitor, KindMonitor},
	} {
		seen, connected := false, false
		var names []string
		for _, p := range h.Peripherals {
			if p.Kind != pk.periph {
				continue
			}
			seen = true
			if p.Connected {
				connected = true
			} else if p.Name != "" {
				names = append(names, p.Name)
			}
		}
		// Вида нет в списке — агент его не проверяет (неизвестно), это не проблема.
		if seen && !connected {
			d := map[string]any{}
			if len(names) > 0 {
				d["devices"] = names
			}
			out[pk.kind] = d
		}
	}

	if l := h.LinkMbps; l > 0 && (l < th.LinkMinMbps || (linkMax >= th.LinkFullMbps && l < th.LinkFullMbps)) {
		d := map[string]any{"mbps": l}
		if linkMax > l {
			d["previous_mbps"] = linkMax
		}
		out[KindNetworkSlow] = d
	}

	if (th.UnstableShutdowns > 0 && h.Shutdowns7d >= th.UnstableShutdowns) || (th.UnstableBugchecks > 0 && h.Bugchecks7d >= th.UnstableBugchecks) {
		out[KindUnstable] = map[string]any{"shutdowns_7d": h.Shutdowns7d, "bugchecks_7d": h.Bugchecks7d}
	}
	return out
}

// diskLow — системный диск: свободно < SystemDiskFreePercent % или < SystemDiskFreeGB ГБ; другой — < OtherDiskFreePercent %.
// Сравнение в целых: free·100 < total·percent (без float).
func diskLow(d presence.Disk, th Thresholds) bool {
	if d.System {
		return d.FreeMB*100 < d.TotalMB*th.SystemDiskFreePercent || d.FreeMB < th.SystemDiskFreeGB*1024
	}
	return d.FreeMB*100 < d.TotalMB*th.OtherDiskFreePercent
}
