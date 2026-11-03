package health

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

const gb = 1024 // МБ в ГБ

func TestEvaluateThresholds(t *testing.T) {
	th := Defaults()
	cases := []struct {
		name    string
		h       presence.Health
		active  []string
		linkMax uint32
		want    []string
	}{
		{"ничего не измерено", presence.Health{}, nil, 0, nil},
		{"CPU 89.9 °C — норма", presence.Health{CPUTempDeci: 899}, nil, 0, nil},
		{"CPU 90 °C — перегрев", presence.Health{CPUTempDeci: 900}, nil, 0, []string{KindCPUHot}},
		{"GPU 85 °C — перегрев", presence.Health{GPUTempDeci: 850}, nil, 0, []string{KindGPUHot}},
		{"GPU 84.9 °C — норма", presence.Health{GPUTempDeci: 849}, nil, 0, nil},
		{"гистерезис: CPU 86 °C при открытой проблеме держится", presence.Health{CPUTempDeci: 860}, []string{KindCPUHot}, 0, []string{KindCPUHot}},
		{"гистерезис: CPU 84.9 °C — уходит", presence.Health{CPUTempDeci: 849}, []string{KindCPUHot}, 0, nil},
		{"гистерезис не держит без открытой проблемы", presence.Health{CPUTempDeci: 860}, nil, 0, nil},
		{"системный диск: 9 % из 1 ТБ", presence.Health{Disks: []presence.Disk{{Mount: "C:", System: true, TotalMB: 1000 * gb, FreeMB: 90 * gb}}}, nil, 0, []string{KindDiskLow}},
		{"системный диск: 10 % ровно — норма", presence.Health{Disks: []presence.Disk{{Mount: "C:", System: true, TotalMB: 1000 * gb, FreeMB: 100 * gb}}}, nil, 0, nil},
		{"системный диск: 50 % из 16 ГБ, но < 10 ГБ", presence.Health{Disks: []presence.Disk{{Mount: "C:", System: true, TotalMB: 16 * gb, FreeMB: 8 * gb}}}, nil, 0, []string{KindDiskLow}},
		{"другой диск: 6 % — норма", presence.Health{Disks: []presence.Disk{{Mount: "D:", TotalMB: 1000 * gb, FreeMB: 60 * gb}}}, nil, 0, nil},
		{"другой диск: 4 % — мало", presence.Health{Disks: []presence.Disk{{Mount: "D:", TotalMB: 1000 * gb, FreeMB: 40 * gb}}}, nil, 0, []string{KindDiskLow}},
		{"диск без объёма не проверяется", presence.Health{Disks: []presence.Disk{{Mount: "E:"}}}, nil, 0, nil},
		{"SMART warning", presence.Health{Disks: []presence.Disk{{Mount: "C:", TotalMB: 100, FreeMB: 100, Smart: presence.SmartWarning}}}, nil, 0, []string{KindDiskSmart}},
		{"SMART failing", presence.Health{Disks: []presence.Disk{{Mount: "C:", TotalMB: 100, FreeMB: 100, Smart: presence.SmartFailing}}}, nil, 0, []string{KindDiskSmart}},
		{"SMART ok", presence.Health{Disks: []presence.Disk{{Mount: "C:", TotalMB: 100, FreeMB: 100, Smart: presence.SmartOK}}}, nil, 0, nil},
		{"мышь отключена", presence.Health{Peripherals: []presence.Peripheral{{Kind: presence.PeripheralMouse, Name: "G102"}}}, nil, 0, []string{KindMouse}},
		{"одна из двух мышей подключена", presence.Health{Peripherals: []presence.Peripheral{{Kind: presence.PeripheralMouse}, {Kind: presence.PeripheralMouse, Connected: true}}}, nil, 0, nil},
		{"клавиатура и монитор отключены", presence.Health{Peripherals: []presence.Peripheral{{Kind: presence.PeripheralKeyboard}, {Kind: presence.PeripheralMonitor}}}, nil, 0, []string{KindKeyboard, KindMonitor}},
		{"гарнитура — не проблема", presence.Health{Peripherals: []presence.Peripheral{{Kind: presence.PeripheralHeadset}}}, nil, 0, nil},
		{"вида нет в списке — не проблема", presence.Health{Peripherals: []presence.Peripheral{{Kind: presence.PeripheralMouse, Connected: true}}}, nil, 0, nil},
		{"сеть 100 Мбит/с без истории — норма", presence.Health{LinkMbps: 100}, nil, 0, nil},
		{"сеть 10 Мбит/с — всегда проблема", presence.Health{LinkMbps: 10}, nil, 0, []string{KindNetworkSlow}},
		{"сеть упала с 1000 до 100", presence.Health{LinkMbps: 100}, nil, 1000, []string{KindNetworkSlow}},
		{"сеть 1000 — норма", presence.Health{LinkMbps: 1000}, nil, 1000, nil},
		{"сеть 2500 → 1000 — норма", presence.Health{LinkMbps: 1000}, nil, 2500, nil},
		{"3 внезапных выключения", presence.Health{Shutdowns7d: 3}, nil, 0, []string{KindUnstable}},
		{"2 внезапных выключения — норма", presence.Health{Shutdowns7d: 2}, nil, 0, nil},
		{"один «синий экран»", presence.Health{Bugchecks7d: 1}, nil, 0, []string{KindUnstable}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			active := map[string]bool{}
			for _, k := range c.active {
				active[k] = true
			}
			got := Evaluate(&c.h, th, active, c.linkMax).Sorted()
			want := c.want
			if want == nil {
				want = []string{}
			}
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestEvaluateDetails(t *testing.T) {
	p := Evaluate(&presence.Health{CPUTempDeci: 955, LinkMbps: 100,
		Disks: []presence.Disk{{Mount: "C:", System: true, TotalMB: 100 * gb, FreeMB: 5 * gb}}}, Defaults(), nil, 1000)
	if !reflect.DeepEqual(p[KindCPUHot], map[string]any{"temp_deci": int32(955), "threshold_c": int32(90)}) {
		t.Errorf("cpu details: %v", p[KindCPUHot])
	}
	if !reflect.DeepEqual(p[KindNetworkSlow], map[string]any{"mbps": uint32(100), "previous_mbps": uint32(1000)}) {
		t.Errorf("network details: %v", p[KindNetworkSlow])
	}
	if d, ok := p[KindDiskLow]["disks"].([]map[string]any); !ok || len(d) != 1 || d[0]["mount"] != "C:" {
		t.Errorf("disk details: %v", p[KindDiskLow])
	}
}

// Дребезг: проблема записывается, только продержавшись RaiseAfter, и снимается, отсутствуя ClearAfter.
func TestTrackerDebounce(t *testing.T) {
	th := Defaults() // RaiseAfter 2 мин, ClearAfter 5 мин
	tr := NewTracker(th)
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	hot := &presence.Health{CPUTempDeci: 950}
	cool := &presence.Health{CPUTempDeci: 600}

	if trs := tr.Observe(hot, t0); len(trs) != 0 {
		t.Fatalf("raised immediately: %v", trs)
	}
	// Мигание: проблема пропала до RaiseAfter — отсчёт начинается заново.
	if trs := tr.Observe(cool, t0.Add(time.Minute)); len(trs) != 0 {
		t.Fatalf("cool: %v", trs)
	}
	if trs := tr.Observe(hot, t0.Add(90*time.Second)); len(trs) != 0 {
		t.Fatalf("restart: %v", trs)
	}
	if trs := tr.Observe(hot, t0.Add(3*time.Minute)); len(trs) != 0 {
		t.Fatalf("raised before RaiseAfter from the restart: %v", trs)
	}
	trs := tr.Observe(hot, t0.Add(3*time.Minute+30*time.Second))
	if len(trs) != 1 || !trs[0].Raised || trs[0].Kind != KindCPUHot {
		t.Fatalf("want raise, got %v", trs)
	}
	// Не записалось (нет Commit) — повторится.
	if trs := tr.Observe(hot, t0.Add(4*time.Minute)); len(trs) != 1 {
		t.Fatalf("uncommitted raise must repeat: %v", trs)
	}
	tr.Commit(trs)
	if got := tr.Active(); !slices.Equal(got, []string{KindCPUHot}) {
		t.Fatalf("active = %v", got)
	}
	// То же состояние — без переходов.
	for i := range 10 {
		if trs := tr.Observe(hot, t0.Add(5*time.Minute+time.Duration(i)*time.Minute)); len(trs) != 0 {
			t.Fatalf("same state produced transitions: %v", trs)
		}
	}
	// Остывает: снимается только через ClearAfter; возврат перегрева сбрасывает отсчёт.
	t1 := t0.Add(20 * time.Minute)
	if trs := tr.Observe(cool, t1); len(trs) != 0 {
		t.Fatalf("cleared immediately: %v", trs)
	}
	if trs := tr.Observe(hot, t1.Add(4*time.Minute)); len(trs) != 0 {
		t.Fatalf("hot again: %v", trs)
	}
	if trs := tr.Observe(cool, t1.Add(5*time.Minute)); len(trs) != 0 {
		t.Fatalf("clear before ClearAfter from the restart: %v", trs)
	}
	trs = tr.Observe(cool, t1.Add(10*time.Minute))
	if len(trs) != 1 || trs[0].Raised || trs[0].Kind != KindCPUHot {
		t.Fatalf("want clear, got %v", trs)
	}
	tr.Commit(trs)
	if len(tr.Active()) != 0 {
		t.Fatalf("active after clear: %v", tr.Active())
	}
}

func TestTrackerRestoreNoDuplicateRaise(t *testing.T) {
	th := Defaults()
	th.RaiseAfter = 0
	tr := NewTracker(th)
	tr.Restore([]Open{{Kind: KindMouse}, {Kind: KindNetworkSlow, Details: map[string]any{"mbps": float64(100), "previous_mbps": float64(1000)}}})
	h := &presence.Health{LinkMbps: 100, Peripherals: []presence.Peripheral{{Kind: presence.PeripheralMouse}}}
	if trs := tr.Observe(h, time.Now()); len(trs) != 0 {
		t.Fatalf("restored problems must not be raised again: %v", trs)
	}
	// Прежняя «полная» скорость восстановлена из журнала: 100 Мбит/с — всё ещё проблема, не снимается.
	th2 := tr.th
	th2.ClearAfter = 0
	tr.th = th2
	if trs := tr.Observe(h, time.Now()); len(trs) != 0 {
		t.Fatalf("network_slow must stay with previous_mbps restored: %v", trs)
	}
}

func TestTrackerNoHealthNoTransitions(t *testing.T) {
	tr := NewTracker(Defaults())
	if trs := tr.Observe(nil, time.Now()); trs != nil {
		t.Fatal(trs)
	}
}
