// Package recovery — «Забыли пароль» на сайте (api/recovery.yaml): код в Telegram на номер аккаунта, затем код и
// новый пароль. Laravel: app/Http/Controllers/PasswordRecoveryController.php, app/Services/PasswordRecovery.php.
// Логика (лимиты, коды, журнал платформы, выход везде) — verifycode.PasswordRecovery, общая с агентом ПК (gRPC).
package recovery

import (
	"context"
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	recoveryapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/recovery"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/verifycode"
)

// access — гостевые маршруты (middleware guest): входа нет.
var access = httpx.Access{Public: []string{"GetPasswordRecovery", "SendPasswordRecoveryCode", "ResetPasswordByCode"}}

// limits — throttle:password-recovery (routes/web.php:161, 163): 10/мин на IP и телефон, 30/мин на IP.
var limits = map[string]string{
	"POST /api/forgot-password": ratelimit.PasswordRecovery,
	"POST /api/reset-password":  ratelimit.PasswordRecovery,
}

type handlers struct{ d *httpx.Deps }

var _ recoveryapi.StrictServerInterface = (*handlers)(nil)

// Section — восстановление пароля.
func Section() httpx.Section { return httpx.Section{Name: "recovery", Mount: mount, Limits: limits} }

func mount(r chi.Router, d *httpx.Deps) {
	si := recoveryapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[recoveryapi.StrictMiddlewareFunc](access),
		recoveryapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	recoveryapi.HandlerWithOptions(si, recoveryapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// GetPasswordRecovery — show(): ['available' => $this->recovery->available()].
func (h *handlers) GetPasswordRecovery(ctx context.Context, _ recoveryapi.GetPasswordRecoveryRequestObject) (recoveryapi.GetPasswordRecoveryResponseObject, error) {
	ok, err := h.d.PasswordRecovery.Available(ctx)
	if err != nil {
		return nil, err
	}
	return recoveryapi.GetPasswordRecovery200JSONResponse{Available: ok}, nil
}

// phone — PasswordRecoveryController::phone(): нормализация и проверка.
func phone(raw *string) (string, error) {
	p := ""
	if raw != nil {
		p = verifycode.NormalizePhone(*raw)
	}
	switch {
	case p == "":
		return "", httpx.ValidationMsg("phone", "Номер телефона обязателен.", nil)
	case !verifycode.PhoneRe.MatchString(p):
		return "", httpx.ValidationMsg("phone", "Введите корректный номер телефона.", nil)
	}
	return p, nil
}

// codeError — ValidationException сервиса → 422 с полем и текстом старого кода.
func codeError(err error) error {
	var ve *verifycode.Error
	if errors.As(err, &ve) {
		return httpx.ValidationMsg(ve.Field, ve.Key, ve.Params)
	}
	return err
}

// SendPasswordRecoveryCode — send(): код в Telegram; ответ одинаковый для любого номера.
func (h *handlers) SendPasswordRecoveryCode(ctx context.Context, req recoveryapi.SendPasswordRecoveryCodeRequestObject) (recoveryapi.SendPasswordRecoveryCodeResponseObject, error) {
	var raw *string
	if req.Body != nil {
		raw = req.Body.Phone
	}
	p, err := phone(raw)
	if err != nil {
		return nil, err
	}
	if h.d.PasswordRecovery == nil {
		return nil, httpx.ValidationMsg("phone", verifycode.MsgRecoveryUnavailable, nil)
	}
	if err := h.d.PasswordRecovery.SendCode(ctx, p); err != nil {
		return nil, codeError(err)
	}
	return recoveryapi.SendPasswordRecoveryCode200JSONResponse{
		Message: h.d.T(ctx, verifycode.MsgCodeSent, map[string]string{"minutes": strconv.Itoa(verifycode.CodeMinutes)}),
		Phone:   p, CodeMinutes: verifycode.CodeMinutes, ResendSeconds: verifycode.ResendSeconds,
	}, nil
}

// ResetPasswordByCode — update(): код и новый пароль; все ошибки формы сразу, как validate() Laravel.
func (h *handlers) ResetPasswordByCode(ctx context.Context, req recoveryapi.ResetPasswordByCodeRequestObject) (recoveryapi.ResetPasswordByCodeResponseObject, error) {
	var b recoveryapi.RecoveryResetRequest
	if req.Body != nil {
		b = *req.Body
	}
	p, err := phone(b.Phone)
	if err != nil {
		return nil, err
	}
	code, pw, confirm := deref(b.Code), deref(b.Password), deref(b.PasswordConfirmation)
	var fe httpx.FormErrors
	switch {
	case code == "":
		fe.AddMsg("code", "Введите код из Telegram.", nil)
	case !verifycode.CodeRe.MatchString(code):
		fe.AddMsg("code", "Код — 6 цифр из сообщения в Telegram.", nil)
	}
	switch {
	case pw == "":
		fe.AddMsg("password", "Пароль обязателен.", nil)
	case utf8.RuneCountInString(pw) < 6:
		fe.AddMsg("password", "Пароль должен содержать минимум 6 символов.", nil)
	case pw != confirm:
		fe.AddMsg("password", "Пароли не совпадают.", nil)
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	if h.d.PasswordRecovery == nil {
		return nil, httpx.ValidationMsg("code", verifycode.MsgCodeWrong, nil)
	}
	if _, err := h.d.PasswordRecovery.Reset(ctx, p, code, pw); err != nil {
		return nil, codeError(err)
	}
	return recoveryapi.ResetPasswordByCode200JSONResponse{Message: h.d.T(ctx, verifycode.MsgPasswordChanged, nil)}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
