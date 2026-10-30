package presence

import (
	"testing"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

func ptr[T any](v T) *T { return &v }

func TestStateName(t *testing.T) {
	cases := map[agentv1.ComputerState]string{
		agentv1.ComputerState_COMPUTER_STATE_UNSPECIFIED:   "",
		agentv1.ComputerState_COMPUTER_STATE_IDLE:          "idle",
		agentv1.ComputerState_COMPUTER_STATE_LOCKED:        "locked",
		agentv1.ComputerState_COMPUTER_STATE_IN_SESSION:    "in_session",
		agentv1.ComputerState_COMPUTER_STATE_SHUTTING_DOWN: "shutting_down",
		agentv1.ComputerState_COMPUTER_STATE_SERVICE:       "service",
	}
	for in, want := range cases {
		if got := StateName(in); got != want {
			t.Errorf("StateName(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestParseOwner(t *testing.T) {
	o, ok := ParseOwner("api-1|1761830000000123")
	if !ok || o.Instance != "api-1" || o.Gen != 1761830000000123 || o.String() != "api-1|1761830000000123" {
		t.Errorf("ParseOwner = %+v %v", o, ok)
	}
	for _, bad := range []string{"", "x", "|1", "x|y"} {
		if _, ok := ParseOwner(bad); ok {
			t.Errorf("ParseOwner(%q) must fail", bad)
		}
	}
}

func onlineRow() store.GetAgentComputerRow {
	return store.GetAgentComputerRow{
		ID: 7, ClubID: 3, Status: "online", AgentVersion: ptr("2.0.0"), AgentBootID: ptr("boot-1"),
		RemoteIp: ptr("10.0.0.1"), LocalIp: ptr("192.168.1.7"), CpuModel: ptr("Ryzen 5"), CpuCores: ptr(int32(12)),
		RamTotalMb: ptr(int64(16384)), GpuModel: ptr("RTX 4060"), OsVersion: ptr("Windows 11"), DeviceName: ptr("PC-07"),
	}
}

func sameFacts() HelloFacts {
	return HelloFacts{AgentVersion: "2.0.0", BootID: "boot-1", RemoteIP: "10.0.0.1", Device: &agentv1.DeviceInfo{
		LocalIp: "192.168.1.7", CpuModel: "Ryzen 5", CpuCores: 12, RamTotalMb: 16384, GpuModel: "RTX 4060",
		OsVersion: "Windows 11", Hostname: "PC-07",
	}}
}

func TestDiffHelloNoChanges(t *testing.T) {
	// Переподключение с теми же данными к ПК, который уже online, — ни одной записи в Postgres.
	if u := DiffHello(onlineRow(), sameFacts()); !u.Empty() || u.FreshBoot {
		t.Fatalf("expected no write, got %+v", u)
	}
}

func TestDiffHelloChanges(t *testing.T) {
	row := onlineRow()
	row.Status = "offline"
	if u := DiffHello(row, sameFacts()); u.Empty() || !u.Online || u.HardwareChanged {
		t.Errorf("offline → online must be written without hardware: %+v", u)
	}

	f := sameFacts()
	f.AgentVersion = "2.0.1"
	f.BootID = "boot-2"
	u := DiffHello(onlineRow(), f)
	if u.Empty() || !u.FreshBoot || u.Params.AgentVersion == nil || *u.Params.AgentVersion != "2.0.1" || u.HardwareChanged {
		t.Errorf("version/boot change: %+v", u)
	}

	f = sameFacts()
	f.Device.RamTotalMb = 32768
	u = DiffHello(onlineRow(), f)
	if !u.HardwareChanged || u.Params.RamTotalMb == nil || u.Params.CpuModel != nil {
		t.Errorf("hardware change must write only changed fields: %+v", u)
	}

	// Пустые значения не стирают сохранённые.
	u = DiffHello(onlineRow(), HelloFacts{AgentVersion: "2.0.0"})
	if !u.Empty() {
		t.Errorf("empty facts must not write: %+v", u)
	}
}

func TestSignificant(t *testing.T) {
	a := State{AgentState: "idle", Load: Load{CPUBasisPoints: 100}}
	b := a
	b.Load.CPUBasisPoints = 9000
	if b.Significant(a) {
		t.Error("load change is not significant")
	}
	b.AgentState = "locked"
	if !b.Significant(a) {
		t.Error("state change is significant")
	}
}
