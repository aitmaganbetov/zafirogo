package grpcserver

import (
	"strconv"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// ErrorDomain — ErrorInfo.domain всех ошибок контракта агента (common.proto).
const ErrorDomain = "agent.zafiro.kz"

// MessageLocale — язык текста ошибки в LocalizedMessage. Текст — русский ключ перевода, как в старом
// проекте; перевод на язык ПК (заголовок zafiro-locale) — Этап 3 (общий словарь переводов).
const MessageLocale = "ru"

// Error собирает ошибку gRPC: status + ErrorInfo{reason: ErrorCode, domain, metadata} + LocalizedMessage.
func Error(code codes.Code, reason agentv1.ErrorCode, message string, metadata map[string]string) error {
	st := status.New(code, message)
	details := []protoadapt.MessageV1{&errdetails.ErrorInfo{Reason: reason.String(), Domain: ErrorDomain, Metadata: metadata}}
	if message != "" {
		details = append(details, &errdetails.LocalizedMessage{Locale: MessageLocale, Message: message})
	}
	return withDetails(st, details...)
}

func withDetails(st *status.Status, details ...protoadapt.MessageV1) error {
	withD, err := st.WithDetails(details...)
	if err != nil {
		return st.Err()
	}
	return withD.Err()
}

// RateLimited — RESOURCE_EXHAUSTED + ERROR_CODE_RATE_LIMITED + RetryInfo.
func RateLimited(retryAfter time.Duration, message string) error {
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	st := status.New(codes.ResourceExhausted, message)
	secs := int64((retryAfter + time.Second - 1) / time.Second)
	return withDetails(st,
		&errdetails.ErrorInfo{Reason: agentv1.ErrorCode_ERROR_CODE_RATE_LIMITED.String(), Domain: ErrorDomain,
			Metadata: map[string]string{"retry_after_seconds": strconv.FormatInt(secs, 10)}},
		&errdetails.RetryInfo{RetryDelay: durationpb.New(time.Duration(secs) * time.Second)},
		&errdetails.LocalizedMessage{Locale: MessageLocale, Message: message},
	)
}

// Validation — INVALID_ARGUMENT + ERROR_CODE_VALIDATION_FAILED + BadRequest с нарушением поля.
func Validation(field, message string) error {
	st := status.New(codes.InvalidArgument, message)
	return withDetails(st,
		&errdetails.ErrorInfo{Reason: agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED.String(), Domain: ErrorDomain,
			Metadata: map[string]string{"field": field}},
		&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: field, Description: message}}},
		&errdetails.LocalizedMessage{Locale: MessageLocale, Message: message},
	)
}

// Internal — INTERNAL + ERROR_CODE_INTERNAL без подробностей (подробности — только в лог сервера).
func Internal() error {
	return Error(codes.Internal, agentv1.ErrorCode_ERROR_CODE_INTERNAL, "Внутренняя ошибка сервера.", nil)
}

// ReasonOf достаёт ErrorInfo.reason из ошибки gRPC (тесты и клиенты на Go).
func ReasonOf(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return ""
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}
