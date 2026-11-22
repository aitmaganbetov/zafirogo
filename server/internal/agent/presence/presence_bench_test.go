package presence

import (
	"encoding/json"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// Микробенчмарки обработки Status (аудит PERF-5): разбор загрузки и здоровья, сравнение со снимком и JSON снимка
// присутствия, который пишется в Valkey на каждый Status. Запуск: go test -run '^$' -bench . -benchmem ./internal/agent/presence/.

var (
	sinkState State
	sinkBool  bool
	sinkBytes []byte
)

func benchStatus() *agentv1.ComputerHealth {
	return &agentv1.ComputerHealth{
		CpuTempDecicelsius: 612, GpuTempDecicelsius: 705, GpuMeasured: true, GpuLoadBasisPoints: 4300, GpuMemoryBasisPoints: 3100,
		Disks: []*agentv1.DiskHealth{
			{Mount: "C:", System: true, TotalMb: 476000, FreeMb: 120000, Smart: agentv1.DiskSmart_DISK_SMART_OK},
			{Mount: "D:", TotalMb: 1900000, FreeMb: 300000, Smart: agentv1.DiskSmart_DISK_SMART_OK},
		},
		Peripherals: []*agentv1.Peripheral{
			{Kind: agentv1.PeripheralKind_PERIPHERAL_KIND_MOUSE, Connected: true, Name: "Logitech G Pro"},
			{Kind: agentv1.PeripheralKind_PERIPHERAL_KIND_KEYBOARD, Connected: true, Name: "HyperX Alloy"},
		},
		LinkSpeedMbps: 1000, UptimeSeconds: 86400,
	}
}

func benchState() State {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	return State{ClubID: 17, ComputerID: 1234, Online: true, Instance: "app-1", Gen: 7, ProtocolVersion: 1,
		AgentVersion: "1.4.2", AgentState: "session", SessionID: "01HZX0000000000000000000", SignedInUserID: 99,
		Load: LoadFrom(&agentv1.ComputerLoad{CpuBasisPoints: 2300, RamBasisPoints: 6100, IdleSeconds: 3}), HasLoad: true,
		Health: HealthFrom(benchStatus(), now), RTTMillis: 12, RemoteIP: "10.0.0.15", ConnectedAt: now, LastSeenAt: now}
}

// Разбор Status: загрузка + здоровье + сравнение со снимком.
func BenchmarkApplyStatus(b *testing.B) {
	h, prev := benchStatus(), benchState()
	load := &agentv1.ComputerLoad{CpuBasisPoints: 2500, RamBasisPoints: 6150, IdleSeconds: 4}
	now := time.Date(2026, 11, 22, 12, 1, 0, 0, time.UTC)
	for b.Loop() {
		st := prev
		st.Load, st.Health = LoadFrom(load), HealthFrom(h, now)
		sinkBool = st.Significant(prev) || HealthChanged(prev.Health, st.Health)
		sinkState = st
	}
}

// Снимок присутствия → JSON (Store.Refresh) и обратно (Store.List для карты клуба).
func BenchmarkStateJSON(b *testing.B) {
	st := benchState()
	b.Run("marshal", func(b *testing.B) {
		for b.Loop() {
			out, err := json.Marshal(st)
			if err != nil {
				b.Fatal(err)
			}
			sinkBytes = out
		}
	})
	raw, _ := json.Marshal(st)
	b.Run("unmarshal", func(b *testing.B) {
		for b.Loop() {
			var s State
			if err := json.Unmarshal(raw, &s); err != nil {
				b.Fatal(err)
			}
			sinkState = s
		}
	})
}
