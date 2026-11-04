package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	authapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/auth"
	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// Cookie сессии. Префиксы __Secure-/__Host- браузер принимает только с Secure (и __Host- — с Path=/ без Domain).
const (
	// RefreshCookie — refresh-токен: HttpOnly, отправляется только на /api/auth (refresh, logout).
	RefreshCookie = "__Secure-zafiro_rt"
	refreshPath   = "/api/auth"
	// CSRFCookie — CSRF-токен для заголовка X-CSRF-Token: его читает SPA (не HttpOnly).
	CSRFCookie = "__Host-zafiro_csrf"
)

var errAuthNotConfigured = errors.New("auth is not configured")

// crossSite — Fetch Metadata: браузер пометил запрос как пришедший с другого сайта. SameSite=Strict и так
// не отправит cookie, это второй рубеж (и защита входа от «login CSRF»). Без заголовка (не браузер) — пропуск.
func crossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return false
	}
	return true
}

func setSessionCookies(w http.ResponseWriter, sm *session.Manager, t session.Tokens) {
	maxAge := int(t.RefreshMaxAge / time.Second)
	http.SetCookie(w, &http.Cookie{Name: RefreshCookie, Value: t.Refresh, Path: refreshPath, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: CSRFCookie, Value: sm.CSRFToken(t.SessionID), Path: "/", MaxAge: maxAge,
		Secure: true, SameSite: http.SameSiteStrictMode}) // #nosec G124 -- CSRF-токен читает SPA (double submit), HttpOnly нельзя
}

func clearSessionCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: RefreshCookie, Value: "", Path: refreshPath, MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: CSRFCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, SameSite: http.SameSiteStrictMode}) // #nosec G124 -- см. setSessionCookies
}

func refreshFromCookie(r *http.Request) string {
	if r == nil {
		return ""
	}
	c, err := r.Cookie(RefreshCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// meResponse — Me с языком и часовым поясом клуба и временем сервера.
func (h *handlers) meResponse(ctx context.Context, me auth.Me) (authapi.Me, error) {
	out := baseMe(me)
	out.ServerTime = h.d.NowFunc().UTC()
	if out.Club != nil {
		if err := h.clubInfo(ctx, out.Club); err != nil {
			return out, err
		}
		if err := h.integrationsAccess(ctx, me.Principal, out.Club); err != nil {
			return out, err
		}
	}
	return out, nil
}

func baseMe(me auth.Me) authapi.Me {
	out := authapi.Me{
		User:        authapi.MeUser{Id: me.UserID, Name: me.Name, Phone: me.Phone, IsSuperadmin: me.Superadmin},
		Permissions: me.Principal.Permissions,
		ReadOnly:    me.Principal.ReadOnly,
		Clubs:       make([]authapi.MembershipItem, 0, len(me.Clubs)),
	}
	if out.Permissions == nil {
		out.Permissions = []string{}
	}
	if me.Principal.ClubID > 0 {
		out.Club = &authapi.MeClub{Id: me.Principal.ClubID, Name: me.ClubName}
		if me.Principal.Role != "" {
			role := me.Principal.Role
			out.Role = &role
		}
	}
	for _, c := range me.Clubs {
		out.Clubs = append(out.Clubs, authapi.MembershipItem{ClubId: c.ClubID, ClubName: c.ClubName, Role: c.Role})
	}
	return out
}

func (h *handlers) authSession(ctx context.Context, t session.Tokens, p principal.Principal) (authapi.AuthSession, error) {
	me, err := h.d.Auth.Me(ctx, p)
	if err != nil {
		return authapi.AuthSession{}, err
	}
	out, err := h.meResponse(ctx, me)
	if err != nil {
		return authapi.AuthSession{}, err
	}
	return authapi.AuthSession{
		AccessToken: t.Access,
		TokenType:   authapi.Bearer,
		ExpiresIn:   int(time.Until(t.AccessExpires).Round(time.Second) / time.Second),
		CsrfToken:   h.d.Auth.Sessions().CSRFToken(t.SessionID),
		Me:          out,
	}, nil
}

// Login — POST /api/auth/login. Laravel: AuthController::login().
func (h *handlers) Login(ctx context.Context, req authapi.LoginRequestObject) (authapi.LoginResponseObject, error) {
	if h.d.Auth == nil {
		return nil, errAuthNotConfigured
	}
	w, r := httpx.HTTPFrom(ctx)
	if r != nil && crossSite(r) {
		return authapi.Login403JSONResponse{ForbiddenJSONResponse: apicommon.ForbiddenJSONResponse(httpx.ErrorBody(permissions.CodeForbidden, ""))}, nil
	}
	in := auth.LoginInput{Phone: req.Body.Phone, Password: req.Body.Password}
	if req.Body.Remember != nil {
		in.Remember = *req.Body.Remember
	}
	if req.Body.PcLogin != nil {
		in.PCLogin = *req.Body.PcLogin
	}
	// Хост кабинета суперадмина (Q-069): здесь входят только суперадмины.
	in.SuperadminOnly = r != nil && onSuperadminHost(r.Host, h.d.Superadmin.Domain)
	res, err := h.d.Auth.Login(ctx, in)
	var fe *auth.FieldError
	if errors.As(err, &fe) {
		return authapi.Login422JSONResponse{ValidationFailedJSONResponse: apicommon.ValidationFailedJSONResponse{
			Error:   apicommon.ValidationErrorErrorValidationFailed,
			Message: fe.Message,
			Errors:  map[string][]string{fe.Field: {fe.Message}},
		}}, nil
	}
	if err != nil {
		return nil, err
	}
	if res.Challenge != nil {
		// Суперадмин: второй фактор (Этап 7) — сессии и cookie до кода нет.
		noStore(w)
		return loginChallenge(res.Challenge), nil
	}
	body, err := h.authSession(ctx, res.Tokens, res.Principal)
	if err != nil {
		return nil, err
	}
	setSessionCookies(w, h.d.Auth.Sessions(), res.Tokens)
	return authapi.Login200JSONResponse(body), nil
}

// RefreshSession — POST /api/auth/refresh: ротация refresh-токена из cookie.
func (h *handlers) RefreshSession(ctx context.Context, req authapi.RefreshSessionRequestObject) (authapi.RefreshSessionResponseObject, error) {
	if h.d.Auth == nil {
		return nil, errAuthNotConfigured
	}
	w, r := httpx.HTTPFrom(ctx)
	sm := h.d.Auth.Sessions()
	refresh := refreshFromCookie(r)
	if refresh == "" {
		return authapi.RefreshSession401JSONResponse{UnauthenticatedJSONResponse: apicommon.UnauthenticatedJSONResponse(
			httpx.ErrorBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	if crossSite(r) || !sm.CheckCSRF(refresh, req.Params.XCSRFToken) {
		return authapi.RefreshSession403JSONResponse{ForbiddenJSONResponse: apicommon.ForbiddenJSONResponse(httpx.ErrorBody("csrf_mismatch", ""))}, nil
	}
	t, err := sm.Refresh(ctx, refresh)
	switch {
	case errors.Is(err, session.ErrRefreshRace):
		return authapi.RefreshSession409JSONResponse(httpx.ErrorBody("refresh_conflict", "")), nil
	case errors.Is(err, session.ErrInvalidRefresh), errors.Is(err, session.ErrRefreshReused):
		clearSessionCookies(w)
		return authapi.RefreshSession401JSONResponse{UnauthenticatedJSONResponse: apicommon.UnauthenticatedJSONResponse(
			httpx.ErrorBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	case err != nil:
		return nil, err
	}
	p, err := h.d.Auth.Authenticate(ctx, t.Access)
	switch {
	case errors.Is(err, auth.ErrBlocked):
		clearSessionCookies(w)
		return authapi.RefreshSession403JSONResponse{ForbiddenJSONResponse: apicommon.ForbiddenJSONResponse(
			httpx.ErrorBody(permissions.CodeForbidden, auth.MsgBlockedAPI))}, nil
	case errors.Is(err, auth.ErrUnauthenticated):
		clearSessionCookies(w)
		return authapi.RefreshSession401JSONResponse{UnauthenticatedJSONResponse: apicommon.UnauthenticatedJSONResponse(
			httpx.ErrorBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	case err != nil:
		return nil, err
	}
	body, err := h.authSession(ctx, t, p)
	if err != nil {
		return nil, err
	}
	setSessionCookies(w, sm, t)
	return authapi.RefreshSession200JSONResponse(body), nil
}

// Logout — POST /api/auth/logout: по access-токену или по refresh-cookie с X-CSRF-Token.
func (h *handlers) Logout(ctx context.Context, req authapi.LogoutRequestObject) (authapi.LogoutResponseObject, error) {
	if h.d.Auth == nil {
		return nil, errAuthNotConfigured
	}
	w, r := httpx.HTTPFrom(ctx)
	if p, ok := principal.FromContext(ctx); ok {
		if err := h.d.Auth.Logout(ctx, p); err != nil {
			return nil, err
		}
	} else if refresh := refreshFromCookie(r); refresh != "" {
		csrf := ""
		if req.Params.XCSRFToken != nil {
			csrf = *req.Params.XCSRFToken
		}
		if crossSite(r) || !h.d.Auth.Sessions().CheckCSRF(refresh, csrf) {
			return authapi.Logout403JSONResponse{ForbiddenJSONResponse: apicommon.ForbiddenJSONResponse(httpx.ErrorBody("csrf_mismatch", ""))}, nil
		}
		if err := h.d.Auth.Sessions().RevokeRefresh(ctx, refresh); err != nil {
			return nil, err
		}
	}
	clearSessionCookies(w)
	return authapi.Logout204Response{}, nil
}

// LogoutAll — POST /api/auth/logout-all.
func (h *handlers) LogoutAll(ctx context.Context, _ authapi.LogoutAllRequestObject) (authapi.LogoutAllResponseObject, error) {
	if h.d.Auth == nil {
		return nil, errAuthNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return authapi.LogoutAll401JSONResponse{UnauthenticatedJSONResponse: apicommon.UnauthenticatedJSONResponse(
			httpx.ErrorBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	if err := h.d.Auth.LogoutAll(ctx, p); err != nil {
		return nil, err
	}
	w, _ := httpx.HTTPFrom(ctx)
	clearSessionCookies(w)
	return authapi.LogoutAll204Response{}, nil
}

// GetMe — GET /api/auth/me.
func (h *handlers) GetMe(ctx context.Context, _ authapi.GetMeRequestObject) (authapi.GetMeResponseObject, error) {
	if h.d.Auth == nil {
		return nil, errAuthNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return authapi.GetMe401JSONResponse{UnauthenticatedJSONResponse: apicommon.UnauthenticatedJSONResponse(
			httpx.ErrorBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	me, err := h.d.Auth.Me(ctx, p)
	if err != nil {
		return nil, err
	}
	out, err := h.meResponse(ctx, me)
	if err != nil {
		return nil, err
	}
	return authapi.GetMe200JSONResponse(out), nil
}

// SelectClub — PUT /api/auth/club. Laravel: ClubController::switch().
func (h *handlers) SelectClub(ctx context.Context, req authapi.SelectClubRequestObject) (authapi.SelectClubResponseObject, error) {
	if h.d.Auth == nil {
		return nil, errAuthNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return authapi.SelectClub401JSONResponse{UnauthenticatedJSONResponse: apicommon.UnauthenticatedJSONResponse(
			httpx.ErrorBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	np, err := h.d.Auth.SelectClub(ctx, p, req.Body.ClubId)
	switch {
	case errors.Is(err, auth.ErrReadOnly):
		return authapi.SelectClub403JSONResponse{ForbiddenJSONResponse: apicommon.ForbiddenJSONResponse(
			httpx.ErrorBody(permissions.CodeReadOnly, permissions.MsgReadOnly))}, nil
	case errors.Is(err, auth.ErrNotFound):
		return authapi.SelectClub404JSONResponse{NotFoundJSONResponse: apicommon.NotFoundJSONResponse(httpx.ErrorBody("not_found", ""))}, nil
	case errors.Is(err, auth.ErrUnauthenticated):
		return authapi.SelectClub401JSONResponse{UnauthenticatedJSONResponse: apicommon.UnauthenticatedJSONResponse(
			httpx.ErrorBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	case err != nil:
		return nil, err
	}
	me, err := h.d.Auth.Me(ctx, np)
	if err != nil {
		return nil, err
	}
	out, err := h.meResponse(ctx, me)
	if err != nil {
		return nil, err
	}
	return authapi.SelectClub200JSONResponse(out), nil
}

// SuperadminViewClub — POST /api/superadmin/clubs/{clubId}/view.
func (h *handlers) SuperadminViewClub(ctx context.Context, req authapi.SuperadminViewClubRequestObject) (authapi.SuperadminViewClubResponseObject, error) {
	if h.d.Auth == nil {
		return nil, errAuthNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return authapi.SuperadminViewClub401JSONResponse{UnauthenticatedJSONResponse: apicommon.UnauthenticatedJSONResponse(
			httpx.ErrorBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	np, err := h.d.Auth.ViewClub(ctx, p, req.ClubId)
	switch {
	case errors.Is(err, auth.ErrForbidden):
		return authapi.SuperadminViewClub403JSONResponse{ForbiddenJSONResponse: apicommon.ForbiddenJSONResponse(httpx.ErrorBody(permissions.CodeForbidden, ""))}, nil
	case errors.Is(err, auth.ErrNotFound):
		return authapi.SuperadminViewClub404JSONResponse{NotFoundJSONResponse: apicommon.NotFoundJSONResponse(httpx.ErrorBody("not_found", ""))}, nil
	case errors.Is(err, auth.ErrUnauthenticated):
		return authapi.SuperadminViewClub401JSONResponse{UnauthenticatedJSONResponse: apicommon.UnauthenticatedJSONResponse(
			httpx.ErrorBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	case err != nil:
		return nil, err
	}
	me, err := h.d.Auth.Me(ctx, np)
	if err != nil {
		return nil, err
	}
	out, err := h.meResponse(ctx, me)
	if err != nil {
		return nil, err
	}
	return authapi.SuperadminViewClub200JSONResponse(out), nil
}

// SuperadminLeaveClub — POST /api/superadmin/leave-club.
func (h *handlers) SuperadminLeaveClub(ctx context.Context, _ authapi.SuperadminLeaveClubRequestObject) (authapi.SuperadminLeaveClubResponseObject, error) {
	if h.d.Auth == nil {
		return nil, errAuthNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return authapi.SuperadminLeaveClub401JSONResponse{UnauthenticatedJSONResponse: apicommon.UnauthenticatedJSONResponse(
			httpx.ErrorBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	if _, err := h.d.Auth.LeaveClub(ctx, p); err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			return authapi.SuperadminLeaveClub403JSONResponse{ForbiddenJSONResponse: apicommon.ForbiddenJSONResponse(httpx.ErrorBody(permissions.CodeForbidden, ""))}, nil
		}
		return nil, err
	}
	_, r := httpx.HTTPFrom(ctx)
	np, err := h.d.Auth.Authenticate(ctx, auth.BearerToken(r))
	if err != nil {
		return nil, err
	}
	me, err := h.d.Auth.Me(ctx, np)
	if err != nil {
		return nil, err
	}
	out, err := h.meResponse(ctx, me)
	if err != nil {
		return nil, err
	}
	return authapi.SuperadminLeaveClub200JSONResponse(out), nil
}

// onSuperadminHost — SuperadminHost::is(): имя хоста запроса без порта совпадает с SUPERADMIN_DOMAIN.
func onSuperadminHost(host, domain string) bool {
	if domain == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.EqualFold(host, domain)
}
