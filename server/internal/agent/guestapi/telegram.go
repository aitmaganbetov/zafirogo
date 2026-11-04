package guestapi

// Коды Telegram (Этап 4, H2): восстановление пароля на ПК (POST /api/auth/password/code и /reset,
// Api\PasswordRecoveryController) и код подтверждения перевода (POST /api/guest/friends/{friendship}/transfer/code,
// GuestFriendController::transferCode()). Логика и лимиты — verifycode (PasswordRecovery, FriendTransferCodes).

import (
	"context"
	"errors"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/verifycode"
)

// Тексты Api\PasswordRecoveryController (validate()).
const (
	msgRecoveryPhoneRequired = "Номер телефона обязателен."
	msgRecoveryPhoneInvalid  = "Введите корректный номер телефона."
	msgRecoveryCodeRequired  = "Введите код из Telegram."
	msgRecoveryPasswordShort = "Пароль должен содержать минимум 6 символов." // #nosec G101 -- текст для пользователя, не секрет
	msgRecoveryPasswordLong  = "Пароль — не длиннее 255 символов."           // #nosec G101 -- текст для пользователя, не секрет
)

// codeStatus — ошибка verifycode → статус gRPC; остальные — fail (billing.BalanceError и т. п. через toStatus).
func (s *Server) codeStatus(ctx context.Context, method string, err error) error {
	var ve *verifycode.Error
	if !errors.As(err, &ve) {
		return s.fail(ctx, method, err)
	}
	msg := substitute(ve.Key, ve.Params)
	switch ve.Kind {
	case verifycode.KindUnavailable:
		return rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_FEATURE_DISABLED, ve.Key, ve.Params, ve.Field)
	case verifycode.KindWait, verifycode.KindHourly:
		return grpcserver.ErrorWithRetry(codes.ResourceExhausted, agentv1.ErrorCode_ERROR_CODE_RATE_LIMITED, msg, ve.RetryAfter)
	case verifycode.KindCodeWrong:
		return rpcError(codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_CONFIRMATION_CODE_INVALID, ve.Key, ve.Params, ve.Field)
	case verifycode.KindCodeExpired:
		return rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_CONFIRMATION_CODE_EXPIRED, ve.Key, ve.Params, ve.Field)
	default: // KindNotSent
		return rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED, ve.Key, ve.Params, ve.Field)
	}
}

// recoveryPhone — PasswordRecoveryController::phone(): PhoneNumber::normalize + regex:/^\+7\d{10}$/.
func recoveryPhone(raw string) (string, error) {
	p := verifycode.NormalizePhone(raw)
	switch {
	case p == "":
		return "", validation("phone", msgRecoveryPhoneRequired)
	case !verifycode.PhoneRe.MatchString(p):
		return "", validation("phone", msgRecoveryPhoneInvalid)
	}
	return p, nil
}

// RequestPasswordResetCode — POST /api/auth/password/code (throttle:password-recovery): код в Telegram; ответ
// одинаковый, есть ли номер в Zafiro.
func (a *authService) RequestPasswordResetCode(ctx context.Context, req *agentv1.RequestPasswordResetCodeRequest) (*agentv1.RequestPasswordResetCodeResponse, error) {
	s := a.s
	ctx, _, err := s.call(ctx, ratelimit.PasswordRecovery, func(sub *ratelimit.Subject) { sub.Phone = req.GetPhone() })
	if err != nil {
		return nil, err
	}
	phone, err := recoveryPhone(req.GetPhone())
	if err != nil {
		return nil, err
	}
	if s.o.Recovery == nil {
		return nil, s.codeStatus(ctx, "RequestPasswordResetCode", &verifycode.Error{Kind: verifycode.KindUnavailable, Field: "phone", Key: verifycode.MsgRecoveryUnavailable})
	}
	if err := s.o.Recovery.SendCode(ctx, phone); err != nil {
		return nil, s.codeStatus(ctx, "RequestPasswordResetCode", err)
	}
	return &agentv1.RequestPasswordResetCodeResponse{
		CodeValidFor: durationpb.New(verifycode.CodeTTL), ResendAfter: durationpb.New(verifycode.ResendAfter),
	}, nil
}

// ResetPassword — POST /api/auth/password/reset (throttle:password-recovery): новый пароль по коду; все сеансы и
// токены аккаунта отзываются (verifycode.PasswordRecovery.SignOut). Подтверждение пароля проверяет агент.
func (a *authService) ResetPassword(ctx context.Context, req *agentv1.ResetPasswordRequest) (*agentv1.ResetPasswordResponse, error) {
	s := a.s
	ctx, _, err := s.call(ctx, ratelimit.PasswordRecovery, func(sub *ratelimit.Subject) { sub.Phone = req.GetPhone() })
	if err != nil {
		return nil, err
	}
	phone, err := recoveryPhone(req.GetPhone())
	if err != nil {
		return nil, err
	}
	switch code := req.GetCode(); {
	case code == "":
		return nil, rpcError(codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_CONFIRMATION_CODE_INVALID, msgRecoveryCodeRequired, nil, "code")
	case !verifycode.CodeRe.MatchString(code):
		return nil, rpcError(codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_CONFIRMATION_CODE_INVALID, msgCodeFormat, nil, "code")
	}
	switch n := utf8.RuneCountInString(req.GetNewPassword()); {
	case n < 6:
		return nil, validation("password", msgRecoveryPasswordShort)
	case n > 255: // нового: тело HTTP в старом ограничивал сервер; Argon2id длинный пароль не нужен
		return nil, validation("password", msgRecoveryPasswordLong)
	}
	if s.o.Recovery == nil {
		return nil, s.codeStatus(ctx, "ResetPassword", &verifycode.Error{Kind: verifycode.KindCodeExpired, Field: "code", Key: verifycode.MsgCodeWrong})
	}
	if _, err := s.o.Recovery.Reset(ctx, phone, req.GetCode(), req.GetNewPassword()); err != nil {
		return nil, s.codeStatus(ctx, "ResetPassword", err)
	}
	return &agentv1.ResetPasswordResponse{}, nil
}

// RequestTransferCode — GuestFriendController::transferCode() (throttle:5,1): функция «переводы» клуба, затем
// FriendTransferCodes::send() — проверки перевода, лимиты на аккаунт, код в Telegram.
func (ss *socialService) RequestTransferCode(ctx context.Context, req *agentv1.RequestTransferCodeRequest) (*agentv1.RequestTransferCodeResponse, error) {
	s := ss.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.friends.transfer.code")
	if err != nil {
		return nil, err
	}
	if err := s.requireFeature(ctx, id, featureBalanceTransfer); err != nil {
		return nil, err
	}
	if s.o.TransferCodes == nil {
		return nil, s.codeStatus(ctx, "RequestTransferCode", &verifycode.Error{Kind: verifycode.KindUnavailable, Field: "code", Key: verifycode.MsgTransferUnavailable})
	}
	if err := s.o.TransferCodes.Send(ctx, id.ClubID, g.UserID, req.GetFriendshipId(), money.Tiyn(req.GetAmountTiyn())); err != nil {
		return nil, s.codeStatus(ctx, "RequestTransferCode", err)
	}
	return &agentv1.RequestTransferCodeResponse{
		CodeValidFor: durationpb.New(verifycode.CodeTTL), ResendAfter: durationpb.New(verifycode.ResendAfter),
	}, nil
}
