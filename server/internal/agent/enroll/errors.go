package enroll

import (
	"errors"
	"time"

	"google.golang.org/grpc/codes"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
)

// Error — ошибка сервиса с кодом контракта агента. Message — русский текст (ключ перевода), как в
// старом коде, где он был; REST-слой панели отображает Kind в HTTP-код.
type Error struct {
	Kind    Kind
	Code    agentv1.ErrorCode
	Message string
	// Field — поле для ошибок валидации.
	Field string
	// RetryAfter — для RATE_LIMITED.
	RetryAfter time.Duration
}

// Kind — класс ошибки (для HTTP/gRPC-кода).
type Kind int

// Классы ошибок.
const (
	KindInvalid Kind = iota + 1
	KindUnauthenticated
	KindForbidden
	KindNotFound
	KindPrecondition
	KindRateLimited
)

func (e *Error) Error() string { return e.Code.String() + ": " + e.Message }

func newErr(kind Kind, code agentv1.ErrorCode, msg string) *Error {
	return &Error{Kind: kind, Code: code, Message: msg}
}

// Ошибки с текстами старого кода (где он был).
var (
	// Laravel: ClubComputerPolicy::register().
	ErrForbidden = newErr(KindForbidden, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED,
		"Недостаточно прав для регистрации компьютера в клубе.")
	ErrCodeInvalid = newErr(KindUnauthenticated, agentv1.ErrorCode_ERROR_CODE_ENROLLMENT_CODE_INVALID,
		"Код подключения ПК недействителен.")
	ErrCodeExpired = newErr(KindUnauthenticated, agentv1.ErrorCode_ERROR_CODE_ENROLLMENT_CODE_EXPIRED,
		"Код подключения ПК истёк или уже использован.")
	ErrCSRInvalid = &Error{Kind: KindInvalid, Code: agentv1.ErrorCode_ERROR_CODE_CSR_INVALID,
		Message: "Запрос сертификата ПК недействителен.", Field: "csr_pem"}
	// Laravel: AgentEnrollController / RequireActiveSubscription (402).
	ErrSubscriptionInactive = newErr(KindPrecondition, agentv1.ErrorCode_ERROR_CODE_SUBSCRIPTION_INACTIVE,
		"Подписка клуба не активна.")
	// Laravel: ClubComputerRegistrar::register() стр. 44–47 (BR-134).
	ErrComputerLimit = newErr(KindPrecondition, agentv1.ErrorCode_ERROR_CODE_COMPUTER_LIMIT_REACHED,
		"Достигнут лимит компьютеров по тарифу клуба.")
	// Laravel: ClubComputerRegistrar::register() стр. 53–55.
	ErrZoneFull = &Error{Kind: KindInvalid, Code: agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED,
		Message: "В зоне достигнут лимит мест.", Field: "zone_id"}
	// Laravel: ClubComputerRegistrar::register() стр. 84 (409).
	ErrDeviceTaken = &Error{Kind: KindInvalid, Code: agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED,
		Message: "Этот device_id уже зарегистрирован.", Field: "device.hardware_uuid"}
	// Laravel: AuthenticateAgentDevice::handle() (device_mismatch).
	ErrDeviceMismatch = newErr(KindForbidden, agentv1.ErrorCode_ERROR_CODE_DEVICE_MISMATCH,
		"Токен устройства принадлежит другому ПК.")
	ErrComputerNotFound = newErr(KindNotFound, agentv1.ErrorCode_ERROR_CODE_COMPUTER_NOT_FOUND,
		"Компьютер не найден.")
	ErrZoneNotFound = &Error{Kind: KindNotFound, Code: agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED,
		Message: "Зона не найдена.", Field: "zone_id"}
	ErrCertificateRevoked = newErr(KindUnauthenticated, agentv1.ErrorCode_ERROR_CODE_CERTIFICATE_REVOKED,
		"Сертификат ПК отозван.")
	ErrNoIdentity = newErr(KindUnauthenticated, agentv1.ErrorCode_ERROR_CODE_CERTIFICATE_EXPIRED,
		"ПК не подключён к клубу.")
	ErrCodeNotFound = newErr(KindNotFound, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED,
		"Код подключения ПК не найден.")
)

func validation(field, msg string) *Error {
	return &Error{Kind: KindInvalid, Code: agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED, Message: msg, Field: field}
}

func rateLimited(retry time.Duration) *Error {
	return &Error{Kind: KindRateLimited, Code: agentv1.ErrorCode_ERROR_CODE_RATE_LIMITED,
		Message: "Слишком много попыток. Повторите позже.", RetryAfter: retry}
}

// ToGRPC переводит ошибку сервиса в статус gRPC (прочие ошибки — INTERNAL без подробностей).
func ToGRPC(err error) error {
	var e *Error
	if !errors.As(err, &e) {
		return grpcserver.Internal()
	}
	switch e.Kind {
	case KindRateLimited:
		return grpcserver.RateLimited(e.RetryAfter, e.Message)
	case KindInvalid:
		if e.Code == agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED {
			return grpcserver.Validation(e.Field, e.Message)
		}
		return grpcserver.Error(codes.InvalidArgument, e.Code, e.Message, fieldMeta(e.Field))
	case KindUnauthenticated:
		return grpcserver.Error(codes.Unauthenticated, e.Code, e.Message, nil)
	case KindForbidden:
		return grpcserver.Error(codes.PermissionDenied, e.Code, e.Message, nil)
	case KindNotFound:
		return grpcserver.Error(codes.NotFound, e.Code, e.Message, fieldMeta(e.Field))
	case KindPrecondition:
		return grpcserver.Error(codes.FailedPrecondition, e.Code, e.Message, fieldMeta(e.Field))
	}
	return grpcserver.Internal()
}

func fieldMeta(field string) map[string]string {
	if field == "" {
		return nil
	}
	return map[string]string{"field": field}
}
