package stream

import (
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// ErrorDomain — google.rpc.ErrorInfo.domain (docs/agent-protocol.md §7).
const ErrorDomain = "agent.zafiro.kz"

// Поддерживаемые версии протокола (Hello.protocol_version, Welcome.min/max_protocol_version).
const (
	MinProtocolVersion uint32 = 1
	MaxProtocolVersion uint32 = 1
)

// statusErr — gRPC-статус с ErrorInfo{reason = имя ErrorCode}.
func statusErr(c codes.Code, ec agentv1.ErrorCode, msg string) error {
	st := status.New(c, msg)
	if ec != agentv1.ErrorCode_ERROR_CODE_UNSPECIFIED {
		if d, err := st.WithDetails(&errdetails.ErrorInfo{Reason: ec.String(), Domain: ErrorDomain}); err == nil {
			st = d
		}
	}
	return st.Err()
}

// validationErr — INVALID_ARGUMENT + VALIDATION_FAILED + BadRequest по полю.
func validationErr(field, desc string) error {
	st := status.New(codes.InvalidArgument, fmt.Sprintf("%s: %s", field, desc))
	if d, err := st.WithDetails(
		&errdetails.ErrorInfo{Reason: agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED.String(), Domain: ErrorDomain},
		&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: field, Description: desc}}},
	); err == nil {
		st = d
	}
	return st.Err()
}

func tooLong(s string, n int) bool { return utf8.RuneCountInString(s) > n || !utf8.ValidString(s) }

// Пределы длины — по колонкам club_computers (0001_init.sql): agent_version varchar(50), local_ip varchar(45),
// cpu_model/gpu_model/os_version/device_name varchar(255), agent_boot_id varchar(64) (0010).
func validateHello(h *agentv1.Hello) error {
	if h.GetProtocolVersion() < MinProtocolVersion || h.GetProtocolVersion() > MaxProtocolVersion {
		return statusErr(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_PROTOCOL_VERSION_UNSUPPORTED,
			fmt.Sprintf("protocol version %d is not supported (supported %d..%d)", h.GetProtocolVersion(), MinProtocolVersion, MaxProtocolVersion))
	}
	checks := []struct {
		field string
		v     string
		max   int
	}{
		{"hello.agent_version", h.GetAgentVersion(), 50},
		{"hello.boot_id", h.GetBootId(), 64},
		{"hello.locale", h.GetLocale(), 16},
		{"hello.device.hardware_uuid", h.GetDevice().GetHardwareUuid(), 255},
		{"hello.device.mac_address", h.GetDevice().GetMacAddress(), 17},
		{"hello.device.hostname", h.GetDevice().GetHostname(), 255},
		{"hello.device.agent_version", h.GetDevice().GetAgentVersion(), 50},
		{"hello.device.os_version", h.GetDevice().GetOsVersion(), 255},
		{"hello.device.cpu_model", h.GetDevice().GetCpuModel(), 255},
		{"hello.device.gpu_model", h.GetDevice().GetGpuModel(), 255},
		{"hello.device.local_ip", h.GetDevice().GetLocalIp(), 45},
	}
	for _, c := range checks {
		if tooLong(c.v, c.max) {
			return validationErr(c.field, fmt.Sprintf("must be valid UTF-8 up to %d characters", c.max))
		}
	}
	if h.GetAgentVersion() == "" {
		return validationErr("hello.agent_version", "is required")
	}
	if d := h.GetDevice(); d != nil {
		if d.GetCpuCores() < 0 || d.GetCpuCores() > 1024 {
			return validationErr("hello.device.cpu_cores", "must be 0..1024")
		}
		if d.GetRamTotalMb() < 0 {
			return validationErr("hello.device.ram_total_mb", "must not be negative")
		}
	}
	if h.GetStatus() != nil {
		if err := validateStatus(h.GetStatus()); err != nil {
			return validationErr("hello.status", err.Error())
		}
	}
	return nil
}

func validateStatus(s *agentv1.Status) error {
	if _, ok := agentv1.ComputerState_name[int32(s.GetState())]; !ok || s.GetState() == agentv1.ComputerState_COMPUTER_STATE_UNSPECIFIED {
		return fmt.Errorf("state is required")
	}
	if tooLong(s.GetSessionId(), 255) {
		return fmt.Errorf("session_id is too long")
	}
	if s.GetSignedInUserId() < 0 {
		return fmt.Errorf("signed_in_user_id must not be negative")
	}
	if l := s.GetLoad(); l != nil && (l.GetCpuBasisPoints() > 10000 || l.GetRamBasisPoints() > 10000) {
		return fmt.Errorf("load basis points must be 0..10000")
	}
	return nil
}

func validUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil && len(s) == 36
}

// validateEvent — проверка события агента. Ошибка — событие отбрасывается (с подтверждением, чтобы
// испорченное событие не блокировало журнал агента).
func validateEvent(e *agentv1.AgentEvent) error {
	if !validUUID(e.GetEventId()) {
		return fmt.Errorf("event_id must be a UUID")
	}
	if e.GetOccurredAt() == nil || !e.GetOccurredAt().IsValid() {
		return fmt.Errorf("occurred_at is required")
	}
	switch k := e.GetKind().(type) {
	case *agentv1.AgentEvent_SessionEnded:
		if k.SessionEnded.GetSessionId() == "" || tooLong(k.SessionEnded.GetSessionId(), 255) {
			return fmt.Errorf("session_ended.session_id is required (up to 255 characters)")
		}
	case *agentv1.AgentEvent_GuestSignedIn:
		if k.GuestSignedIn.GetUserId() < 0 {
			return fmt.Errorf("guest_signed_in.user_id must not be negative")
		}
	case *agentv1.AgentEvent_GuestSignedOut:
		if k.GuestSignedOut.GetUserId() < 0 {
			return fmt.Errorf("guest_signed_out.user_id must not be negative")
		}
	case *agentv1.AgentEvent_Error:
		if tooLong(k.Error.GetComponent(), 64) || tooLong(k.Error.GetMessage(), 500) {
			return fmt.Errorf("error.component ≤ 64 and error.message ≤ 500 characters")
		}
	case *agentv1.AgentEvent_UpdateResult:
		if k.UpdateResult.GetVersion() == "" || tooLong(k.UpdateResult.GetVersion(), 50) {
			return fmt.Errorf("update_result.version is required (up to 50 characters)")
		}
	case *agentv1.AgentEvent_ServiceModeChanged:
	default:
		return fmt.Errorf("kind is required")
	}
	return nil
}
