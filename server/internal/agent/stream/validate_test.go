package stream

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

func ptr[T any](v T) *T { return &v }

func reasonOf(t *testing.T, err error) (codes.Code, string) {
	t.Helper()
	st := status.Convert(err)
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			if ei.GetDomain() != ErrorDomain {
				t.Errorf("domain = %q", ei.GetDomain())
			}
			return st.Code(), ei.GetReason()
		}
	}
	return st.Code(), ""
}

func TestValidateHello(t *testing.T) {
	good := &agentv1.Hello{ProtocolVersion: 1, AgentVersion: "2.0.0", BootId: "b",
		Status: &agentv1.Status{State: agentv1.ComputerState_COMPUTER_STATE_IDLE}}
	if err := validateHello(good); err != nil {
		t.Fatalf("good hello: %v", err)
	}
	for _, v := range []uint32{0, 2, 99} {
		h := &agentv1.Hello{ProtocolVersion: v, AgentVersion: "2.0.0"}
		code, reason := reasonOf(t, validateHello(h))
		if code != codes.FailedPrecondition || reason != "ERROR_CODE_PROTOCOL_VERSION_UNSUPPORTED" {
			t.Errorf("protocol %d: %v %q", v, code, reason)
		}
	}
	bad := []*agentv1.Hello{
		{ProtocolVersion: 1},
		{ProtocolVersion: 1, AgentVersion: strings.Repeat("1", 51)},
		{ProtocolVersion: 1, AgentVersion: "2", BootId: strings.Repeat("b", 65)},
		{ProtocolVersion: 1, AgentVersion: "2", Device: &agentv1.DeviceInfo{CpuCores: 5000}},
		{ProtocolVersion: 1, AgentVersion: "2", Status: &agentv1.Status{}},
		{ProtocolVersion: 1, AgentVersion: "2", Status: &agentv1.Status{State: 1, Load: &agentv1.ComputerLoad{CpuBasisPoints: 10001}}},
	}
	for i, h := range bad {
		code, reason := reasonOf(t, validateHello(h))
		if code != codes.InvalidArgument || reason != "ERROR_CODE_VALIDATION_FAILED" {
			t.Errorf("bad[%d]: %v %q", i, code, reason)
		}
	}
}

func TestValidateEvent(t *testing.T) {
	id := "0199f0a4-7b6e-7c3a-9d2e-1f2a3b4c5d6e"
	now := timestamppb.Now()
	good := []*agentv1.AgentEvent{
		{EventId: id, OccurredAt: now, Kind: &agentv1.AgentEvent_SessionEnded{SessionEnded: &agentv1.SessionEnded{SessionId: "s1"}}},
		{EventId: id, OccurredAt: now, Kind: &agentv1.AgentEvent_GuestSignedIn{GuestSignedIn: &agentv1.GuestSignedIn{UserId: 5}}},
		{EventId: id, OccurredAt: now, Kind: &agentv1.AgentEvent_Error{Error: &agentv1.AgentError{Component: "webview", Message: "x"}}},
	}
	for i, e := range good {
		if err := validateEvent(e); err != nil {
			t.Errorf("good[%d]: %v", i, err)
		}
	}
	bad := []*agentv1.AgentEvent{
		{EventId: "nope", OccurredAt: now, Kind: &agentv1.AgentEvent_GuestSignedIn{GuestSignedIn: &agentv1.GuestSignedIn{}}},
		{EventId: id, Kind: &agentv1.AgentEvent_GuestSignedIn{GuestSignedIn: &agentv1.GuestSignedIn{}}},
		{EventId: id, OccurredAt: now},
		{EventId: id, OccurredAt: now, Kind: &agentv1.AgentEvent_SessionEnded{SessionEnded: &agentv1.SessionEnded{}}},
		{EventId: id, OccurredAt: now, Kind: &agentv1.AgentEvent_Error{Error: &agentv1.AgentError{Message: strings.Repeat("x", 501)}}},
	}
	for i, e := range bad {
		if err := validateEvent(e); err == nil {
			t.Errorf("bad[%d]: expected error", i)
		}
	}
}

func TestDisplayName(t *testing.T) {
	// Laravel: ClubComputer::displayName() — display_name ?: device_name ?: sprintf('PC-%02d', number).
	cases := []struct {
		row  store.GetAgentComputerRow
		want string
	}{
		{store.GetAgentComputerRow{Number: 3}, "PC-03"},
		{store.GetAgentComputerRow{Number: 123}, "PC-123"},
		{store.GetAgentComputerRow{Number: 3, DeviceName: ptr("DESKTOP-1")}, "DESKTOP-1"},
		{store.GetAgentComputerRow{Number: 3, DeviceName: ptr("D"), DisplayName: ptr("VIP-2")}, "VIP-2"},
		{store.GetAgentComputerRow{Number: 3, DisplayName: ptr("")}, "PC-03"},
	}
	for _, c := range cases {
		if got := displayName(c.row); got != c.want {
			t.Errorf("displayName = %q, want %q", got, c.want)
		}
	}
}

func TestActiveSession(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Second)
	cases := []struct {
		name string
		row  store.GetAgentComputerRow
		want bool
	}{
		{"нет сессии", store.GetAgentComputerRow{}, false},
		{"клиент", store.GetAgentComputerRow{CurrentSessionID: ptr("s"), CurrentSessionUserID: ptr(int64(9)), SessionEndsAt: &later}, true},
		{"гость с кассы", store.GetAgentComputerRow{CurrentSessionID: ptr("s"), SessionGuest: true, SessionEndsAt: &later}, true},
		{"сессия агента без оплаты", store.GetAgentComputerRow{CurrentSessionID: ptr("s"), SessionEndsAt: &later}, false},
		{"время вышло", store.GetAgentComputerRow{CurrentSessionID: ptr("s"), CurrentSessionUserID: ptr(int64(9)), SessionEndsAt: &earlier}, false},
	}
	for _, c := range cases {
		got := activeSession(c.row, now)
		if (got != nil) != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}
