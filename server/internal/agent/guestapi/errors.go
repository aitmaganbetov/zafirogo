package guestapi

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
)

const unavailable = codes.Unavailable

// Тексты ошибок (русский текст = ключ перевода, как в старом коде; ссылки — у мест использования).
const (
	msgInternal          = "Внутренняя ошибка сервера."
	msgInvalidCredential = "Неверный номер телефона или пароль."                  // #nosec G101 -- текст для пользователя, не секрет
	msgAccountBlocked    = "Аккаунт заблокирован. Обратитесь в поддержку Zafiro." // там же, AgentRegisterController
	msgNotClubMember     = "Вы ещё не клиент этого клуба. Нажмите «Зарегистрироваться» и введите номер и пароль от своего аккаунта Zafiro."
	msgSubscription      = "Подписка клуба не активна."
	msgSignedInElsewhere = "Вы уже вошли на другом ПК (:computer). Выйдите там, чтобы войти здесь." // AgentSignIns::message()
	msgGuestToken        = "Войдите в аккаунт заново."                                              // #nosec G101 -- текст для пользователя, не секрет
	msgFeatureDisabled   = "Эта возможность отключена в клубе."                                     // GuestFeatures::ensure()
	msgIdemRequired      = "Нужен ключ идемпотентности (UUID)."
	msgIdemReused        = "Ключ идемпотентности уже использован другим запросом."
	msgIdemInProgress    = "Запрос с этим ключом ещё выполняется."
	msgNotFound          = "Не найдено."
)

// errNoCertificate — вызов без идентичности ПК (перехватчик mTLS не пропустил бы; на всякий случай).
var errNoCertificate = grpcserver.Error(codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_CERTIFICATE_REQUIRED,
	"ПК не подключён к клубу.", nil)

// rpcError — уже готовый ответ (статус gRPC), не требует сопоставления.
func rpcError(code codes.Code, reason agentv1.ErrorCode, message string, params map[string]string, field string) error {
	msg := substitute(message, params)
	var md map[string]string
	if field != "" {
		md = map[string]string{"field": field}
	}
	return withI18nKey(grpcserver.Error(code, reason, msg, md), message, params)
}

func substitute(msg string, params map[string]string) string {
	for k, v := range params {
		msg = strings.ReplaceAll(msg, ":"+k, v)
	}
	return msg
}

func validation(field, message string) error { return grpcserver.Validation(field, message) }

func featureDisabled() error {
	return rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_FEATURE_DISABLED, msgFeatureDisabled, nil, "")
}

// toStatus переводит ошибку сервиса в статус gRPC с ErrorInfo (ErrorCode) и текстом старого кода.
// known = false — неожиданная ошибка (INTERNAL, подробности только в лог).
func toStatus(err error) (known bool, st error) {
	if err == nil {
		return true, nil
	}
	if _, ok := status.FromError(err); ok {
		return true, err
	}
	if errors.Is(err, context.Canceled) {
		return true, status.Error(codes.Canceled, "canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true, status.Error(codes.DeadlineExceeded, "deadline exceeded")
	}

	var pe *pricing.Error
	if errors.As(err, &pe) {
		return true, pricingStatus(pe)
	}
	var se *session.Error
	if errors.As(err, &se) {
		return true, sessionStatus(se)
	}
	var be *billingsvc.Error
	if errors.As(err, &be) {
		switch be.Code {
		case billingsvc.ErrBookedFrom.Code:
			return true, rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_COMPUTER_RESERVED, be.Message, be.Params, be.Field)
		case billingsvc.ErrWholeBalanceDisabled.Code:
			return true, rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_FEATURE_DISABLED, be.Message, be.Params, "")
		case billingsvc.ErrNoSuchSession.Code:
			return true, rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SESSION_MISMATCH, be.Message, be.Params, be.Field)
		default:
			return true, rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED, be.Message, be.Params, be.Field)
		}
	}
	var fe *billingsvc.ForbiddenError
	if errors.As(err, &fe) {
		return true, rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_SESSION_MISMATCH, fe.Message, nil, "")
	}
	var bal *billing.BalanceError
	if errors.As(err, &bal) {
		return true, balanceStatus(bal)
	}
	var mismatch *billing.TransferCodeMismatchError
	if errors.As(err, &mismatch) {
		e := mismatch.AsBalanceError()
		return true, rpcError(codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_CONFIRMATION_CODE_INVALID, e.Message, nil, e.Field)
	}
	var she *shop.Error
	if errors.As(err, &she) {
		switch she.Code {
		case shop.ErrNotEnoughMain.Code:
			return true, rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_INSUFFICIENT_FUNDS, she.Message, she.Params, she.Field)
		default:
			return true, rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_OUT_OF_STOCK, she.Message, she.Params, she.Field)
		}
	}
	switch {
	case errors.Is(err, billingsvc.ErrIdempotencyConflict), errors.Is(err, shop.ErrIdempotencyConflict),
		errors.Is(err, ledger.ErrIdempotencyConflict):
		return true, rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_IDEMPOTENCY_KEY_REUSED, msgIdemReused, nil, "idempotency_key")
	case errors.Is(err, billing.ErrFriendshipNotFound):
		return true, rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_FRIENDSHIP_NOT_FOUND, "Заявка не найдена.", nil, "friendship_id")
	case errors.Is(err, ledger.ErrInsufficientFunds):
		return true, rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_INSUFFICIENT_FUNDS, "Недостаточно средств на основном счёте.", nil, "balance")
	case errors.Is(err, billingsvc.ErrNotFound):
		return true, notFoundStatus(err)
	case errors.Is(err, shop.ErrComputerNotFound):
		return true, rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_COMPUTER_NOT_FOUND, msgNotFound, nil, "")
	}
	return false, grpcserver.Internal()
}

// notFoundStatus — findOrFail → 404 старого кода; по сущности выбирается код протокола.
func notFoundStatus(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "time package"):
		return rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_PACKAGE_UNAVAILABLE, msgNotFound, nil, "package_id")
	case strings.Contains(msg, "tariff"):
		return rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_TARIFF_UNAVAILABLE, "Тариф недоступен.", nil, "tariff_id")
	case strings.Contains(msg, "membership"):
		return rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_NOT_CLUB_MEMBER, msgNotClubMember, nil, "")
	case strings.Contains(msg, "member balance"):
		// MemberBalance::firstOrFail() в sell(): денег нет — для агента это «недостаточно средств».
		return rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_INSUFFICIENT_FUNDS, "Недостаточно средств для покупки тарифа.", nil, "balance")
	case strings.Contains(msg, "computer"):
		return rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_COMPUTER_NOT_FOUND, msgNotFound, nil, "")
	}
	return rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED, msgNotFound, nil, "")
}

func pricingStatus(e *pricing.Error) error {
	code, reason := codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED
	switch e.Code {
	case pricing.ErrTariffUnavailable.Code, pricing.ErrTariffNotForComputer.Code, pricing.ErrNoHourlyPrice.Code:
		reason = agentv1.ErrorCode_ERROR_CODE_TARIFF_UNAVAILABLE
	case pricing.ErrComputerReserved.Code:
		reason = agentv1.ErrorCode_ERROR_CODE_COMPUTER_RESERVED
	case pricing.ErrComputerBusy.Code:
		reason = agentv1.ErrorCode_ERROR_CODE_SEAT_UNAVAILABLE
	case pricing.ErrInsufficientFunds.Code, pricing.ErrBonusNotEnough.Code:
		reason = agentv1.ErrorCode_ERROR_CODE_INSUFFICIENT_FUNDS
	case pricing.ErrPackageExpired.Code, pricing.ErrPackageNotForPC.Code, pricing.ErrPackageOutOfPeriod.Code:
		reason = agentv1.ErrorCode_ERROR_CODE_PACKAGE_UNAVAILABLE
	case pricing.ErrMainBalanceOnly.Code, pricing.ErrBonusNotAllowed.Code, pricing.ErrBonusPercentLimit.Code:
		code = codes.InvalidArgument
	}
	return rpcError(code, reason, e.Message, e.Params, e.Field)
}

func sessionStatus(e *session.Error) error {
	reason := agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED
	switch e {
	case session.ErrComputerReserved:
		reason = agentv1.ErrorCode_ERROR_CODE_COMPUTER_RESERVED
	case session.ErrComputerBusy, session.ErrTransferSameOrMissing, session.ErrTransferUnavailable:
		reason = agentv1.ErrorCode_ERROR_CODE_SEAT_UNAVAILABLE
	case session.ErrTransferNotOwnSession:
		reason = agentv1.ErrorCode_ERROR_CODE_NO_ACTIVE_SESSION
	case session.ErrPackageExpired, session.ErrPackageWrongZone, session.ErrPackageOutsidePeriod:
		reason = agentv1.ErrorCode_ERROR_CODE_PACKAGE_UNAVAILABLE
	}
	return rpcError(codes.FailedPrecondition, reason, e.Message, nil, e.Field)
}

func balanceStatus(e *billing.BalanceError) error {
	switch {
	case e.Field == "amount" && strings.Contains(e.Message, "Недостаточно"):
		return rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_INSUFFICIENT_FUNDS, e.Message, e.Params, e.Field)
	case e.Field == "amount":
		return rpcError(codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_TRANSFER_LIMIT, e.Message, e.Params, e.Field)
	case e.Field == "friendship":
		return rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_FRIENDSHIP_NOT_FOUND, e.Message, e.Params, e.Field)
	case e.Field == "code":
		return rpcError(codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_CONFIRMATION_CODE_INVALID, e.Message, e.Params, e.Field)
	}
	return rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED, e.Message, e.Params, e.Field)
}

// withRequestMeta — данные запроса для журнала действий (IP ПК). Журнал требует IP (ActivityLogger).
func withRequestMeta(ctx context.Context) context.Context {
	if _, ok := activity.RequestMetaFromContext(ctx); ok {
		return ctx
	}
	ip := peerIP(ctx)
	if ip == "" {
		return ctx
	}
	return activity.WithRequestMeta(ctx, activity.RequestMeta{IP: ip, UserAgent: agentUserAgent(ctx)})
}

// agentUserAgent — "ZafiroAgent/<версия>" из метаданных zafiro-agent-version (agent.proto п. 2); пусто — NULL.
func agentUserAgent(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("zafiro-agent-version"); len(v) > 0 && v[0] != "" && len(v[0]) <= 50 {
		return "ZafiroAgent/" + v[0]
	}
	return ""
}

// peerIP — адрес соединения (grpc/peer; за L4-балансировщиком без PROXY protocol — адрес балансировщика).
func peerIP(ctx context.Context) string {
	if ip := grpcserver.PeerIP(ctx); ip != nil {
		return ip.String()
	}
	return ""
}
