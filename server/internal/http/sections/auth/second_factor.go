package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/mfa"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	authapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/auth"
	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// Второй фактор суперадмина (Этап 7, новая функция; docs/open-questions/stage7-totp.md). Тексты — ключи перевода.
const (
	// MsgCodeInvalid — неверный или уже использованный код (TOTP или восстановления).
	MsgCodeInvalid = "Неверный код."
	// MsgCodeRequired — код не введён.
	MsgCodeRequired = "Введите код."
	// MsgChallengeGone — полувход истёк, использован или попытки исчерпаны.
	MsgChallengeGone = "Время на ввод кода истекло. Войдите ещё раз."
	// MsgSecretUnreadable — ключ TOTP не расшифровывается (потерян ключ данных сервера): нужен сброс.
	MsgSecretUnreadable = "Не удалось проверить код: обратитесь к другому суперадмину для сброса двухфакторной аутентификации." // #nosec G101 -- текст ошибки, не секрет
)

const codeChallengeGone = "challenge_gone"

func challengeGone() apicommon.Error { return httpx.ErrorBody(codeChallengeGone, MsgChallengeGone) }

// loginChallenge — 202 входа суперадмина: полувход вместо сессии (cookie не ставятся).
func loginChallenge(ch *mfa.Challenge) authapi.Login202JSONResponse {
	return authapi.Login202JSONResponse{
		Challenge:     ch.Token,
		SetupRequired: ch.SetupRequired,
		ExpiresIn:     int(ch.ExpiresIn / time.Second),
	}
}

// SecondFactorSetup — POST /api/auth/second-factor/setup: ключ TOTP для принудительной настройки.
func (h *handlers) SecondFactorSetup(ctx context.Context, req authapi.SecondFactorSetupRequestObject) (authapi.SecondFactorSetupResponseObject, error) {
	if h.d.Auth == nil {
		return nil, errAuthNotConfigured
	}
	w, r := httpx.HTTPFrom(ctx)
	if r != nil && crossSite(r) {
		return authapi.SecondFactorSetup403JSONResponse{ForbiddenJSONResponse: apicommon.ForbiddenJSONResponse(httpx.ErrorBody(permissions.CodeForbidden, ""))}, nil
	}
	st, err := h.d.Auth.SecondFactorSetup(ctx, req.Body.Challenge)
	switch {
	case errors.Is(err, mfa.ErrChallenge):
		return authapi.SecondFactorSetup410JSONResponse(challengeGone()), nil
	case errors.Is(err, mfa.ErrConfigured):
		return authapi.SecondFactorSetup409JSONResponse(httpx.ErrorBody("already_configured", "")), nil
	case err != nil:
		return nil, err
	}
	noStore(w)
	return authapi.SecondFactorSetup200JSONResponse{Secret: st.Secret, OtpauthUri: st.URI}, nil
}

// SecondFactorVerify — POST /api/auth/second-factor/verify: код TOTP или восстановления → сессия.
func (h *handlers) SecondFactorVerify(ctx context.Context, req authapi.SecondFactorVerifyRequestObject) (authapi.SecondFactorVerifyResponseObject, error) {
	if h.d.Auth == nil {
		return nil, errAuthNotConfigured
	}
	w, r := httpx.HTTPFrom(ctx)
	if r != nil && crossSite(r) {
		return authapi.SecondFactorVerify403JSONResponse{ForbiddenJSONResponse: apicommon.ForbiddenJSONResponse(httpx.ErrorBody(permissions.CodeForbidden, ""))}, nil
	}
	invalid := func(msg string) authapi.SecondFactorVerify422JSONResponse {
		return authapi.SecondFactorVerify422JSONResponse{ValidationFailedJSONResponse: apicommon.ValidationFailedJSONResponse{
			Error: apicommon.ValidationErrorErrorValidationFailed, Message: msg, Errors: map[string][]string{"code": {msg}},
		}}
	}
	if req.Body.Code == "" {
		return invalid(MsgCodeRequired), nil
	}
	res, err := h.d.Auth.CompleteSecondFactor(ctx, req.Body.Challenge, req.Body.Code)
	var lim *mfa.LimitError
	switch {
	case errors.As(err, &lim):
		sec := int(ratelimit.RetryAfterSeconds(lim.RetryAfter))
		return authapi.SecondFactorVerify429JSONResponse{TooManyRequestsJSONResponse: apicommon.TooManyRequestsJSONResponse{
			Body:    httpx.ErrorBody("too_many_requests", ratelimit.TooManyAttemptsMessage),
			Headers: apicommon.TooManyRequestsResponseHeaders{RetryAfter: &sec},
		}}, nil
	case errors.Is(err, mfa.ErrChallenge):
		return authapi.SecondFactorVerify410JSONResponse(challengeGone()), nil
	case errors.Is(err, mfa.ErrInvalidCode):
		return invalid(MsgCodeInvalid), nil
	case errors.Is(err, mfa.ErrSecretUnreadable):
		h.d.Log().ErrorContext(ctx, "superadmin totp secret cannot be decrypted with any data key")
		return invalid(MsgSecretUnreadable), nil
	case err != nil:
		return nil, err
	}
	body, err := h.authSession(ctx, res.Tokens, res.Principal)
	if err != nil {
		return nil, err
	}
	setSessionCookies(w, h.d.Auth.Sessions(), res.Tokens)
	noStore(w)
	codes := res.RecoveryCodes
	if codes == nil {
		codes = []string{}
	}
	return authapi.SecondFactorVerify200JSONResponse{Session: body, RecoveryCodes: codes}, nil
}

// noStore — ключ TOTP, коды восстановления и токены не кэшируются (браузер, прокси).
func noStore(w http.ResponseWriter) {
	if w != nil {
		w.Header().Set("Cache-Control", "no-store, private")
	}
}
