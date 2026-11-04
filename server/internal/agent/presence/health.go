package presence

import (
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// Здоровье ПК (Этап 7, Status.health): последнее присланное агентом хранится в снимке присутствия (Valkey) вместе
// с Load — в Postgres пишутся только переходы проблем (agent/health, computer_health_events). Status без health
// прежнее здоровье не стирает (agent.proto, Status.health). Ключи JSON короткие: снимок клуба читается картой целиком.

// Значения DiskSmart в снимке и API.
const (
	SmartOK      = "ok"
	SmartWarning = "warning"
	SmartFailing = "failing"
)

// Виды периферии в снимке и API.
const (
	PeripheralMouse    = "mouse"
	PeripheralKeyboard = "keyboard"
	PeripheralHeadset  = "headset"
	PeripheralMonitor  = "monitor"
)

// Health — здоровье ПК (ComputerHealth). Температуры — десятые доли °C (0 — не измерено), доли — сотые доли процента.
type Health struct {
	CPUTempDeci   int32        `json:"cpu_t,omitempty"`
	GPUTempDeci   int32        `json:"gpu_t,omitempty"`
	GPUMeasured   bool         `json:"gpu_m,omitempty"`
	GPULoadBP     uint32       `json:"gpu_bp,omitempty"`
	GPUMemoryBP   uint32       `json:"gpu_mem_bp,omitempty"`
	Disks         []Disk       `json:"disks,omitempty"`
	Peripherals   []Peripheral `json:"periph,omitempty"`
	LinkMbps      uint32       `json:"link,omitempty"`
	UptimeSeconds uint64       `json:"up,omitempty"`
	Shutdowns7d   uint32       `json:"sd7,omitempty"`
	Bugchecks7d   uint32       `json:"bc7,omitempty"`
	// Diskless — бездисковый ПК (сертификат по ключу образа): дисков нет (Disks пуст), проблемы и история по дискам
	// не считаются, скорость сети — строже (agent/health.Evaluate; docs/agent-protocol.md §16.9).
	Diskless bool `json:"dl,omitempty"`
	// ReportedAt — когда сервер получил это здоровье (аптайм — на этот момент).
	ReportedAt time.Time `json:"at"`
}

// Disk — локальный диск.
type Disk struct {
	Mount   string `json:"m"`
	System  bool   `json:"sys,omitempty"`
	TotalMB uint64 `json:"t"`
	FreeMB  uint64 `json:"f"`
	// Smart — ok | warning | failing | "" (не измерено).
	Smart string `json:"s,omitempty"`
}

// Peripheral — периферийное устройство.
type Peripheral struct {
	// Kind — mouse | keyboard | headset | monitor.
	Kind      string `json:"k"`
	Connected bool   `json:"c,omitempty"`
	Name      string `json:"n,omitempty"`
}

func smartName(s agentv1.DiskSmart) string {
	switch s {
	case agentv1.DiskSmart_DISK_SMART_OK:
		return SmartOK
	case agentv1.DiskSmart_DISK_SMART_WARNING:
		return SmartWarning
	case agentv1.DiskSmart_DISK_SMART_FAILING:
		return SmartFailing
	default:
		return ""
	}
}

// PeripheralName — вид периферии протокола в значение снимка; "" — не задан или неизвестен (новее сервера).
func PeripheralName(k agentv1.PeripheralKind) string {
	switch k {
	case agentv1.PeripheralKind_PERIPHERAL_KIND_MOUSE:
		return PeripheralMouse
	case agentv1.PeripheralKind_PERIPHERAL_KIND_KEYBOARD:
		return PeripheralKeyboard
	case agentv1.PeripheralKind_PERIPHERAL_KIND_HEADSET:
		return PeripheralHeadset
	case agentv1.PeripheralKind_PERIPHERAL_KIND_MONITOR:
		return PeripheralMonitor
	default:
		return ""
	}
}

// HealthFrom читает здоровье из Status (уже проверенное stream.validateHealth). Периферия неизвестного вида
// (агент новее сервера) пропускается, неизвестное состояние SMART — «не измерено»; загрузка GPU — только если
// gpu_measured.
func HealthFrom(h *agentv1.ComputerHealth, at time.Time) *Health {
	if h == nil {
		return nil
	}
	out := &Health{
		CPUTempDeci: h.GetCpuTempDecicelsius(), GPUTempDeci: h.GetGpuTempDecicelsius(), GPUMeasured: h.GetGpuMeasured(),
		LinkMbps: h.GetLinkSpeedMbps(), UptimeSeconds: h.GetUptimeSeconds(),
		Shutdowns7d: h.GetUnexpectedShutdowns_7D(), Bugchecks7d: h.GetBugchecks_7D(), ReportedAt: at.UTC(),
	}
	if out.GPUMeasured {
		out.GPULoadBP, out.GPUMemoryBP = h.GetGpuLoadBasisPoints(), h.GetGpuMemoryBasisPoints()
	}
	for _, d := range h.GetDisks() {
		out.Disks = append(out.Disks, Disk{Mount: d.GetMount(), System: d.GetSystem(), TotalMB: d.GetTotalMb(),
			FreeMB: d.GetFreeMb(), Smart: smartName(d.GetSmart())})
	}
	for _, p := range h.GetPeripherals() {
		if k := PeripheralName(p.GetKind()); k != "" {
			out.Peripherals = append(out.Peripherals, Peripheral{Kind: k, Connected: p.GetConnected(), Name: p.GetName()})
		}
	}
	return out
}

// WithoutDisks — здоровье бездискового ПК: диски (тома образа, присланные старым агентом или по ошибке настройки) не
// хранятся и не оцениваются — их место и SMART ничего не говорят о ПК (S7-H-18).
func (h *Health) WithoutDisks() *Health {
	if h != nil {
		h.Disks, h.Diskless = nil, true
	}
	return h
}

// HealthChanged — изменилось то, что агент сообщает сразу (agent.proto Status.health): набор периферии и её
// подключение или SMART дисков; появление или пропажа здоровья. Температура, загрузка и место на диске — нет (их
// частоту ограничивает stream: не чаще MinStatusGap и live_min_gap).
func HealthChanged(prev, cur *Health) bool {
	if prev == nil || cur == nil {
		return (prev == nil) != (cur == nil)
	}
	if len(prev.Peripherals) != len(cur.Peripherals) || len(prev.Disks) != len(cur.Disks) {
		return true
	}
	for i := range cur.Peripherals {
		if prev.Peripherals[i].Kind != cur.Peripherals[i].Kind || prev.Peripherals[i].Connected != cur.Peripherals[i].Connected ||
			prev.Peripherals[i].Name != cur.Peripherals[i].Name {
			return true
		}
	}
	for i := range cur.Disks {
		if prev.Disks[i].Mount != cur.Disks[i].Mount || prev.Disks[i].Smart != cur.Disks[i].Smart {
			return true
		}
	}
	return false
}
