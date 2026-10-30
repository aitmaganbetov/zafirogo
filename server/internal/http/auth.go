package httpserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/api"
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

// httpCtxKey — *http.Request и http.ResponseWriter для strict-обработчиков (cookie, заголовки).
type httpCtxKey struct{}

type httpPair struct {
	w http.ResponseWriter
	r *http.Request
}

// withHTTP — strict-middleware: кладёт w и r в контекст обработчика.
func withHTTP(f api.StrictHandlerFunc, _ string) api.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
		return f(context.WithValue(ctx, httpCtxKey{}, httpPair{w: w, r: r}), w, r, req)
	}
}

func httpFrom(ctx context.Context) (http.ResponseWriter, *http.Request) {
	p, _ := ctx.Value(httpCtxKey{}).(httpPair)
	return p.w, p.r
}

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

func errBody(code, message string) api.Error {
	e := api.Error{Error: code}
	if message != "" {
		e.Message = &message
	}
	return e
}

func meResponse(me auth.Me) api.Me {
	out := api.Me{
		User:        api.MeUser{Id: me.UserID, Name: me.Name, Phone: me.Phone, IsSuperadmin: me.Superadmin},
		Permissions: me.Principal.Permissions,
		ReadOnly:    me.Principal.ReadOnly,
		Clubs:       make([]api.MembershipItem, 0, len(me.Clubs)),
	}
	if out.Permissions == nil {
		out.Permissions = []string{}
	}
	if me.Principal.ClubID > 0 {
		out.Club = &api.MeClub{Id: me.Principal.ClubID, Name: me.ClubName}
		if me.Principal.Role != "" {
			role := me.Principal.Role
			out.Role = &role
		}
	}
	for _, c := range me.Clubs {
		out.Clubs = append(out.Clubs, api.MembershipItem{ClubId: c.ClubID, ClubName: c.ClubName, Role: c.Role})
	}
	return out
}

func (h *handlers) authSession(ctx context.Context, t session.Tokens, p principal.Principal) (api.AuthSession, error) {
	me, err := h.opts.Auth.Me(ctx, p)
	if err != nil {
		return api.AuthSession{}, err
	}
	return api.AuthSession{
		AccessToken: t.Access,
		TokenType:   api.Bearer,
		ExpiresIn:   int(time.Until(t.AccessExpires).Round(time.Second) / time.Second),
		CsrfToken:   h.opts.Auth.Sessions().CSRFToken(t.SessionID),
		Me:          meResponse(me),
	}, nil
}

// Login — POST /api/auth/login. Laravel: AuthController::login().
func (h *handlers) Login(ctx context.Context, req api.LoginRequestObject) (api.LoginResponseObject, error) {
	if h.opts.Auth == nil {
		return nil, errAuthNotConfigured
	}
	w, r := httpFrom(ctx)
	if r != nil && crossSite(r) {
		return api.Login403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(errBody(permissions.CodeForbidden, ""))}, nil
	}
	in := auth.LoginInput{Phone: req.Body.Phone, Password: req.Body.Password}
	if req.Body.Remember != nil {
		in.Remember = *req.Body.Remember
	}
	if req.Body.PcLogin != nil {
		in.PCLogin = *req.Body.PcLogin
	}
	res, err := h.opts.Auth.Login(ctx, in)
	var fe *auth.FieldError
	if errors.As(err, &fe) {
		return api.Login422JSONResponse{ValidationFailedJSONResponse: api.ValidationFailedJSONResponse{
			Error:   api.ValidationErrorErrorValidationFailed,
			Message: fe.Message,
			Errors:  map[string][]string{fe.Field: {fe.Message}},
		}}, nil
	}
	if err != nil {
		return nil, err
	}
	body, err := h.authSession(ctx, res.Tokens, res.Principal)
	if err != nil {
		return nil, err
	}
	setSessionCookies(w, h.opts.Auth.Sessions(), res.Tokens)
	return api.Login200JSONResponse(body), nil
}

// RefreshSession — POST /api/auth/refresh: ротация refresh-токена из cookie.
func (h *handlers) RefreshSession(ctx context.Context, req api.RefreshSessionRequestObject) (api.RefreshSessionResponseObject, error) {
	if h.opts.Auth == nil {
		return nil, errAuthNotConfigured
	}
	w, r := httpFrom(ctx)
	sm := h.opts.Auth.Sessions()
	refresh := refreshFromCookie(r)
	if refresh == "" {
		return api.RefreshSession401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(
			errBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	if crossSite(r) || !sm.CheckCSRF(refresh, req.Params.XCSRFToken) {
		return api.RefreshSession403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(errBody("csrf_mismatch", ""))}, nil
	}
	t, err := sm.Refresh(ctx, refresh)
	switch {
	case errors.Is(err, session.ErrRefreshRace):
		return api.RefreshSession409JSONResponse(errBody("refresh_conflict", "")), nil
	case errors.Is(err, session.ErrInvalidRefresh), errors.Is(err, session.ErrRefreshReused):
		clearSessionCookies(w)
		return api.RefreshSession401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(
			errBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	case err != nil:
		return nil, err
	}
	p, err := h.opts.Auth.Authenticate(ctx, t.Access)
	switch {
	case errors.Is(err, auth.ErrBlocked):
		clearSessionCookies(w)
		return api.RefreshSession403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(
			errBody(permissions.CodeForbidden, auth.MsgBlockedAPI))}, nil
	case errors.Is(err, auth.ErrUnauthenticated):
		clearSessionCookies(w)
		return api.RefreshSession401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(
			errBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	case err != nil:
		return nil, err
	}
	body, err := h.authSession(ctx, t, p)
	if err != nil {
		return nil, err
	}
	setSessionCookies(w, sm, t)
	return api.RefreshSession200JSONResponse(body), nil
}

// Logout — POST /api/auth/logout: по access-токену или по refresh-cookie с X-CSRF-Token.
func (h *handlers) Logout(ctx context.Context, req api.LogoutRequestObject) (api.LogoutResponseObject, error) {
	if h.opts.Auth == nil {
		return nil, errAuthNotConfigured
	}
	w, r := httpFrom(ctx)
	if p, ok := principal.FromContext(ctx); ok {
		if err := h.opts.Auth.Logout(ctx, p); err != nil {
			return nil, err
		}
	} else if refresh := refreshFromCookie(r); refresh != "" {
		csrf := ""
		if req.Params.XCSRFToken != nil {
			csrf = *req.Params.XCSRFToken
		}
		if crossSite(r) || !h.opts.Auth.Sessions().CheckCSRF(refresh, csrf) {
			return api.Logout403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(errBody("csrf_mismatch", ""))}, nil
		}
		if err := h.opts.Auth.Sessions().RevokeRefresh(ctx, refresh); err != nil {
			return nil, err
		}
	}
	clearSessionCookies(w)
	return api.Logout204Response{}, nil
}

// LogoutAll — POST /api/auth/logout-all.
func (h *handlers) LogoutAll(ctx context.Context, _ api.LogoutAllRequestObject) (api.LogoutAllResponseObject, error) {
	if h.opts.Auth == nil {
		return nil, errAuthNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return api.LogoutAll401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(
			errBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	if err := h.opts.Auth.LogoutAll(ctx, p); err != nil {
		return nil, err
	}
	w, _ := httpFrom(ctx)
	clearSessionCookies(w)
	return api.LogoutAll204Response{}, nil
}

// GetMe — GET /api/auth/me.
func (h *handlers) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	if h.opts.Auth == nil {
		return nil, errAuthNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return api.GetMe401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(
			errBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	me, err := h.opts.Auth.Me(ctx, p)
	if err != nil {
		return nil, err
	}
	return api.GetMe200JSONResponse(meResponse(me)), nil
}

// SelectClub — PUT /api/auth/club. Laravel: ClubController::switch().
func (h *handlers) SelectClub(ctx context.Context, req api.SelectClubRequestObject) (api.SelectClubResponseObject, error) {
	if h.opts.Auth == nil {
		return nil, errAuthNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return api.SelectClub401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(
			errBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	np, err := h.opts.Auth.SelectClub(ctx, p, req.Body.ClubId)
	switch {
	case errors.Is(err, auth.ErrReadOnly):
		return api.SelectClub403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(
			errBody(permissions.CodeReadOnly, permissions.MsgReadOnly))}, nil
	case errors.Is(err, auth.ErrNotFound):
		return api.SelectClub404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(errBody("not_found", ""))}, nil
	case errors.Is(err, auth.ErrUnauthenticated):
		return api.SelectClub401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(
			errBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	case err != nil:
		return nil, err
	}
	me, err := h.opts.Auth.Me(ctx, np)
	if err != nil {
		return nil, err
	}
	return api.SelectClub200JSONResponse(meResponse(me)), nil
}

// SuperadminViewClub — POST /api/superadmin/clubs/{clubId}/view.
func (h *handlers) SuperadminViewClub(ctx context.Context, req api.SuperadminViewClubRequestObject) (api.SuperadminViewClubResponseObject, error) {
	if h.opts.Auth == nil {
		return nil, errAuthNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return api.SuperadminViewClub401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(
			errBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	np, err := h.opts.Auth.ViewClub(ctx, p, req.ClubId)
	switch {
	case errors.Is(err, auth.ErrForbidden):
		return api.SuperadminViewClub403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(errBody(permissions.CodeForbidden, ""))}, nil
	case errors.Is(err, auth.ErrNotFound):
		return api.SuperadminViewClub404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse(errBody("not_found", ""))}, nil
	case errors.Is(err, auth.ErrUnauthenticated):
		return api.SuperadminViewClub401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(
			errBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	case err != nil:
		return nil, err
	}
	me, err := h.opts.Auth.Me(ctx, np)
	if err != nil {
		return nil, err
	}
	return api.SuperadminViewClub200JSONResponse(meResponse(me)), nil
}

// SuperadminLeaveClub — POST /api/superadmin/leave-club.
func (h *handlers) SuperadminLeaveClub(ctx context.Context, _ api.SuperadminLeaveClubRequestObject) (api.SuperadminLeaveClubResponseObject, error) {
	if h.opts.Auth == nil {
		return nil, errAuthNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return api.SuperadminLeaveClub401JSONResponse{UnauthenticatedJSONResponse: api.UnauthenticatedJSONResponse(
			errBody(permissions.CodeUnauthenticated, permissions.MsgUnauthenticated))}, nil
	}
	if _, err := h.opts.Auth.LeaveClub(ctx, p); err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			return api.SuperadminLeaveClub403JSONResponse{ForbiddenJSONResponse: api.ForbiddenJSONResponse(errBody(permissions.CodeForbidden, ""))}, nil
		}
		return nil, err
	}
	_, r := httpFrom(ctx)
	np, err := h.opts.Auth.Authenticate(ctx, auth.BearerToken(r))
	if err != nil {
		return nil, err
	}
	me, err := h.opts.Auth.Me(ctx, np)
	if err != nil {
		return nil, err
	}
	return api.SuperadminLeaveClub200JSONResponse(meResponse(me)), nil
}
