package presence

import (
	"encoding/json"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

func TestHealthFrom(t *testing.T) {
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if HealthFrom(nil, at) != nil {
		t.Fatal("nil health")
	}
	h := HealthFrom(&agentv1.ComputerHealth{
		CpuTempDecicelsius: 655, GpuLoadBasisPoints: 9000, // без gpu_measured загрузка GPU не берётся
		Disks: []*agentv1.DiskHealth{{Mount: "C:", System: true, TotalMb: 100, FreeMb: 5, Smart: agentv1.DiskSmart_DISK_SMART_FAILING},
			{Mount: "D:", TotalMb: 10, FreeMb: 1, Smart: 77}},
		Peripherals: []*agentv1.Peripheral{{Kind: agentv1.PeripheralKind_PERIPHERAL_KIND_MONITOR, Connected: true, Name: "LG"},
			{Kind: 42, Name: "будущий вид"}},
		LinkSpeedMbps: 1000, UptimeSeconds: 3600, UnexpectedShutdowns_7D: 2, Bugchecks_7D: 1,
	}, at)
	if h.CPUTempDeci != 655 || h.GPULoadBP != 0 || h.LinkMbps != 1000 || h.UptimeSeconds != 3600 || h.Shutdowns7d != 2 ||
		h.Bugchecks7d != 1 || !h.ReportedAt.Equal(at) {
		t.Fatalf("health: %+v", h)
	}
	if len(h.Disks) != 2 || h.Disks[0].Smart != SmartFailing || !h.Disks[0].System || h.Disks[1].Smart != "" {
		t.Fatalf("disks: %+v", h.Disks)
	}
	if len(h.Peripherals) != 1 || h.Peripherals[0].Kind != PeripheralMonitor || !h.Peripherals[0].Connected {
		t.Fatalf("unknown peripheral kinds must be skipped: %+v", h.Peripherals)
	}
	g := HealthFrom(&agentv1.ComputerHealth{GpuMeasured: true, GpuLoadBasisPoints: 9000, GpuMemoryBasisPoints: 100}, at)
	if g.GPULoadBP != 9000 || g.GPUMemoryBP != 100 {
		t.Fatalf("gpu: %+v", g)
	}
}

// Снимок присутствия со здоровьем переживает JSON (Valkey); без здоровья — полей нет (размер снимка старых агентов).
func TestStateHealthJSON(t *testing.T) {
	st := State{ClubID: 1, ComputerID: 2, Online: true, HasLoad: true, Problems: []string{"mouse"}, RTTMillis: 12,
		Health: &Health{CPUTempDeci: 700, Disks: []Disk{{Mount: "C:", TotalMB: 10, FreeMB: 1}}}}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back State
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Health == nil || back.Health.CPUTempDeci != 700 || len(back.Problems) != 1 || back.RTTMillis != 12 || !back.HasLoad {
		t.Fatalf("round trip: %s", b)
	}
	b, _ = json.Marshal(State{ClubID: 1, ComputerID: 2})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"health", "problems", "rtt_ms", "load_ok"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s must be omitted without data: %s", k, b)
		}
	}
}

func TestHealthChanged(t *testing.T) {
	base := func() *Health {
		return &Health{CPUTempDeci: 600, Disks: []Disk{{Mount: "C:", TotalMB: 10, FreeMB: 5, Smart: SmartOK}},
			Peripherals: []Peripheral{{Kind: PeripheralHeadset, Connected: true, Name: "HS"}}}
	}
	cases := []struct {
		name string
		f    func(h *Health) *Health
		want bool
	}{
		{"то же", func(h *Health) *Health { return h }, false},
		{"температура и место — не сразу", func(h *Health) *Health { h.CPUTempDeci, h.Disks[0].FreeMB = 900, 1; return h }, false},
		{"гарнитура отключена", func(h *Health) *Health { h.Peripherals[0].Connected = false; return h }, true},
		{"периферия добавлена", func(h *Health) *Health {
			h.Peripherals = append(h.Peripherals, Peripheral{Kind: PeripheralMouse, Connected: true})
			return h
		}, true},
		{"SMART сменился", func(h *Health) *Health { h.Disks[0].Smart = SmartWarning; return h }, true},
		{"диск пропал", func(h *Health) *Health { h.Disks = nil; return h }, true},
		{"здоровья не стало", func(*Health) *Health { return nil }, true},
	}
	for _, c := range cases {
		if got := HealthChanged(base(), c.f(base())); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if HealthChanged(nil, nil) || !HealthChanged(nil, base()) {
		t.Error("nil cases")
	}
}
