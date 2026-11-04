package stream

import (
	"strings"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

func TestValidateHealth(t *testing.T) {
	disk := func(mount string, total, free uint64) *agentv1.DiskHealth {
		return &agentv1.DiskHealth{Mount: mount, TotalMb: total, FreeMb: free}
	}
	periph := func(name string) *agentv1.Peripheral {
		return &agentv1.Peripheral{Kind: agentv1.PeripheralKind_PERIPHERAL_KIND_MOUSE, Name: name}
	}
	disks := func(n int) []*agentv1.DiskHealth {
		out := make([]*agentv1.DiskHealth, n)
		for i := range out {
			out[i] = disk("C:", 10, 1)
		}
		return out
	}
	periphs := func(n int) []*agentv1.Peripheral {
		out := make([]*agentv1.Peripheral, n)
		for i := range out {
			out[i] = periph("m")
		}
		return out
	}
	good := []*agentv1.ComputerHealth{
		nil,
		{},
		{CpuTempDecicelsius: -500, GpuTempDecicelsius: 1500, GpuLoadBasisPoints: 10000, GpuMemoryBasisPoints: 10000, GpuMeasured: true},
		{Disks: disks(26), Peripherals: periphs(16)},
		{Disks: []*agentv1.DiskHealth{disk("C:", 1<<30, 1<<30)}},
		{Peripherals: []*agentv1.Peripheral{periph(strings.Repeat("ы", 128)), periph("Logitech G102 «Lightsync»")}},
		// Неизвестный вид периферии (агент новее сервера) — не ошибка, HealthFrom его пропустит.
		{Peripherals: []*agentv1.Peripheral{{Kind: 99, Name: "x"}}},
	}
	for i, h := range good {
		if err := validateHealth(h); err != nil {
			t.Errorf("good[%d]: %v", i, err)
		}
	}
	bad := []*agentv1.ComputerHealth{
		{CpuTempDecicelsius: 1501},
		{CpuTempDecicelsius: -501},
		{GpuTempDecicelsius: 2000},
		{GpuLoadBasisPoints: 10001},
		{GpuMemoryBasisPoints: 10001},
		{Disks: disks(27)},
		{Peripherals: periphs(17)},
		{Disks: []*agentv1.DiskHealth{disk("", 10, 1)}},
		{Disks: []*agentv1.DiskHealth{disk(strings.Repeat("C", 17), 10, 1)}},
		{Disks: []*agentv1.DiskHealth{disk("C:", 10, 11)}},
		{Disks: []*agentv1.DiskHealth{disk("C:", 1<<30+1, 1)}},
		{Disks: []*agentv1.DiskHealth{disk("C:\n", 10, 1)}},
		{Peripherals: []*agentv1.Peripheral{periph(strings.Repeat("a", 129))}},
		{Peripherals: []*agentv1.Peripheral{periph("mouse\x00")}},
		{Peripherals: []*agentv1.Peripheral{periph("mouse\x1b[31m")}},
		{Peripherals: []*agentv1.Peripheral{periph("abc\u202Eexe.gpj")}},
		{Peripherals: []*agentv1.Peripheral{periph("\xff\xfe")}},
	}
	for i, h := range bad {
		if err := validateHealth(h); err == nil {
			t.Errorf("bad[%d]: expected error", i)
		}
	}
}

// Неверное здоровье в Hello не отклоняет подключение (отбрасывается только health, как в Status).
func TestHelloWithInvalidHealthAccepted(t *testing.T) {
	h := &agentv1.Hello{ProtocolVersion: 1, AgentVersion: "2.0.0",
		Status: &agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_IDLE, Health: &agentv1.ComputerHealth{CpuTempDecicelsius: 99999}}}
	if err := validateHello(h); err != nil {
		t.Fatalf("hello with invalid health must be accepted: %v", err)
	}
}

// Status без load/health прежние значения не стирает; с health — заменяет.
func TestApplyStatusKeepsHealth(t *testing.T) {
	c := &conn{}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	c.applyStatus(&agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_IDLE,
		Load:   &agentv1.ComputerLoad{CpuBasisPoints: 4200, RamBasisPoints: 5100},
		Health: &agentv1.ComputerHealth{CpuTempDecicelsius: 655, LinkSpeedMbps: 1000}}, now)
	if !c.st.HasLoad || c.st.Load.CPUBasisPoints != 4200 || c.st.Health == nil || c.st.Health.CPUTempDeci != 655 {
		t.Fatalf("first status: %+v", c.st)
	}
	c.applyStatus(&agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_LOCKED}, now.Add(time.Minute))
	if !c.st.HasLoad || c.st.Load.CPUBasisPoints != 4200 || c.st.Health == nil || c.st.Health.CPUTempDeci != 655 ||
		!c.st.Health.ReportedAt.Equal(now) {
		t.Fatalf("status without load/health must keep them: %+v %+v", c.st, c.st.Health)
	}
	c.applyStatus(&agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_LOCKED,
		Health: &agentv1.ComputerHealth{CpuTempDecicelsius: 700}}, now.Add(2*time.Minute))
	if c.st.Health.CPUTempDeci != 700 || c.st.Health.LinkMbps != 0 {
		t.Fatalf("new health replaces the old one: %+v", c.st.Health)
	}
}

func TestLiveDue(t *testing.T) {
	o := HealthOptions{}
	o.defaults()
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	base := presence.State{HasLoad: true, Load: presence.Load{CPUBasisPoints: 3000, RAMBasisPoints: 4000},
		Health: &presence.Health{CPUTempDeci: 600, GPUTempDeci: 500}}
	prev := markOf(base, t0)
	with := func(f func(*presence.State)) presence.State {
		s := base
		h := *base.Health
		s.Health = &h
		f(&s)
		return s
	}
	cases := []struct {
		name string
		st   presence.State
		at   time.Duration
		want bool
	}{
		{"без изменений", base, time.Minute, false},
		{"CPU +4.99 п.п.", with(func(s *presence.State) { s.Load.CPUBasisPoints = 3499 }), time.Minute, false},
		{"CPU +5 п.п.", with(func(s *presence.State) { s.Load.CPUBasisPoints = 3500 }), time.Minute, true},
		{"ОЗУ −5 п.п.", with(func(s *presence.State) { s.Load.RAMBasisPoints = 3500 }), time.Minute, true},
		{"CPU +5 п.п., но рано", with(func(s *presence.State) { s.Load.CPUBasisPoints = 9000 }), 14 * time.Second, false},
		{"CPU +5 п.п. через 15 с", with(func(s *presence.State) { s.Load.CPUBasisPoints = 9000 }), 15 * time.Second, true},
		{"CPU +1.9 °C", with(func(s *presence.State) { s.Health.CPUTempDeci = 619 }), time.Minute, false},
		{"GPU +2 °C", with(func(s *presence.State) { s.Health.GPUTempDeci = 520 }), time.Minute, true},
		{"проблема появилась — сразу", with(func(s *presence.State) { s.Problems = []string{"mouse"} }), time.Second, true},
		{"периферия отключена — сразу", with(func(s *presence.State) {
			s.Health.Peripherals = []presence.Peripheral{{Kind: presence.PeripheralHeadset}}
		}), time.Second, true},
		{"health пропал", with(func(s *presence.State) { s.Health = nil }), time.Minute, true},
	}
	for _, c := range cases {
		if got := o.liveDue(prev, c.st, t0.Add(c.at)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// Первое обновление (ничего ещё не отправлено) — сразу, если есть данные.
	if !o.liveDue(liveMark{}, base, t0) {
		t.Error("first update must be sent")
	}
}

func TestPingResultRTT(t *testing.T) {
	c := &conn{}
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	c.pingID, c.pingSent = "0199f0a4-7b6e-7c3a-9d2e-1f2a3b4c5d6e", t0
	if c.pingResult(&agentv1.CommandResult{CommandId: "0199f0a4-7b6e-7c3a-9d2e-000000000000"}, t0.Add(time.Second)) {
		t.Fatal("foreign result taken as ping")
	}
	if !c.pingResult(&agentv1.CommandResult{CommandId: c.pingID}, t0.Add(37*time.Millisecond)) || c.st.RTTMillis != 37 {
		t.Fatalf("rtt = %d", c.st.RTTMillis)
	}
	if c.pingResult(&agentv1.CommandResult{CommandId: "0199f0a4-7b6e-7c3a-9d2e-1f2a3b4c5d6e"}, t0.Add(time.Second)) {
		t.Fatal("duplicate ping result accepted")
	}
	if !c.wantsPing(t0.Add(time.Minute), time.Minute) || c.wantsPing(t0.Add(59*time.Second), time.Minute) || c.wantsPing(t0.Add(time.Hour), -1) {
		t.Fatal("wantsPing")
	}
}
