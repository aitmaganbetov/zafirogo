// Package pclogin — вход на ПК по QR-коду с телефона (api/pclogin.yaml). Laravel: QrLoginController, QrLogins.
//
// ПК получает код из gRPC GuestAuthService.CreateQrLogin и показывает QR со ссылкой /pc-login/{code}. Гость открывает
// её на телефоне, входит на сайте и подтверждает; подтверждение делает guestapi.Server.ApproveQrLogin (та же логика,
// что у агента: членство, блокировка, «вошёл на другом ПК», подписка, атомарное одобрение) и будит WatchQrLogin ПК.
// Клуб — клуб кода (страница знает только код), не клуб сессии: ищем открытый вход пулом zafiro_platform.
package pclogin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	pcloginapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/pclogin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// access — middleware auth без current-club (routes/web.php:149–153): вход проверяет обработчик, клуб — клуб кода.
var access = httpx.Access{Public: []string{"GetPcLogin", "ApprovePcLogin"}}

// limits — throttle:10,1,pc-login.approve (правило в auth/ratelimit).
var limits = map[string]string{"POST /api/pc-login/{code}": "pc-login.approve"}

// codeRe — where('code', '[A-Za-z0-9]{40}'): другой код — 404, как у маршрута Laravel.
var codeRe = regexp.MustCompile(`^[A-Za-z0-9]{40}$`)

// ApproveFunc — guestapi.Server.ApproveQrLogin: ошибки — статусы gRPC (NotFound — вход уже не открыт, остальные —
// текст для поля code).
type ApproveFunc func(ctx context.Context, clubID int64, code string, userID int64) error

type handlers struct{ d *httpx.Deps }

var _ pcloginapi.StrictServerInterface = (*handlers)(nil)

// Section — вход на ПК по QR.
func Section() httpx.Section { return httpx.Section{Name: "pclogin", Mount: mount, Limits: limits} }

func mount(r chi.Router, d *httpx.Deps) {
	si := pcloginapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[pcloginapi.StrictMiddlewareFunc](access),
		pcloginapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	pcloginapi.HandlerWithOptions(si, pcloginapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// hashCode — AgentQrLogin::hashCode(): hash('sha256', $code).
func hashCode(code string) string {
	s := sha256.Sum256([]byte(code))
	return hex.EncodeToString(s[:])
}

// displayName — ClubComputer::displayName(): display_name ?: device_name ?: sprintf('PC-%02d', number).
func displayName(display, device *string, number *int64) *string {
	switch {
	case display != nil && *display != "":
		return display
	case device != nil && *device != "":
		return device
	case number != nil:
		s := fmt.Sprintf("PC-%02d", *number)
		return &s
	}
	return nil
}

// user — вошедший пользователь (middleware auth): 401 без входа; заблокированного не пропускает уже Auth.Middleware.
func (h *handlers) user(ctx context.Context) (int64, error) {
	p := httpx.Principal(ctx)
	if p.UserID == 0 {
		return 0, httpx.NewAPIError(http.StatusUnauthorized, permissions.CodeUnauthenticated, permissions.MsgUnauthenticated)
	}
	if h.d.PlatformPool == nil {
		return 0, httpx.ErrNotConfigured
	}
	return p.UserID, nil
}

// findOpen — QrLogins::findOpen(): открытый вход в любом клубе; нет — nil.
func (h *handlers) findOpen(ctx context.Context, code string) (*store.PcLoginFindOpenRow, error) {
	if !codeRe.MatchString(code) {
		return nil, nil
	}
	var row *store.PcLoginFindOpenRow
	err := db.InPlatformTx(ctx, h.d.PlatformPool, func(q *store.Queries) error {
		r, err := q.PcLoginFindOpen(ctx, store.PcLoginFindOpenParams{CodeHash: hashCode(code), Now: h.d.NowFunc()})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		row = &r
		return nil
	})
	return row, err
}

// GetPcLogin — QrLoginController::show().
func (h *handlers) GetPcLogin(ctx context.Context, req pcloginapi.GetPcLoginRequestObject) (pcloginapi.GetPcLoginResponseObject, error) {
	userID, err := h.user(ctx)
	if err != nil {
		return nil, err
	}
	if !codeRe.MatchString(req.Code) {
		return nil, httpx.NotFound()
	}
	out := pcloginapi.PcLogin{}
	err = db.InPlatformTx(ctx, h.d.PlatformPool, func(q *store.Queries) error {
		u, err := q.GetUserAuth(ctx, userID)
		if err != nil {
			return err
		}
		out.UserName = u.Name
		return nil
	})
	if err != nil {
		return nil, err
	}
	row, err := h.findOpen(ctx, req.Code)
	if err != nil {
		return nil, err
	}
	if row != nil {
		out.Open = true
		out.ClubName = &row.ClubName
		out.ComputerName = displayName(row.DisplayName, row.DeviceName, row.Number)
	}
	return pcloginapi.GetPcLogin200JSONResponse(out), nil
}

// ApprovePcLogin — QrLoginController::approve(): вход не открыт — approved: false (в старом — назад на страницу кода,
// где «QR-код устарел»); иначе QrLogins::approve() в клубе кода.
func (h *handlers) ApprovePcLogin(ctx context.Context, req pcloginapi.ApprovePcLoginRequestObject) (pcloginapi.ApprovePcLoginResponseObject, error) {
	userID, err := h.user(ctx)
	if err != nil {
		return nil, err
	}
	if !codeRe.MatchString(req.Code) {
		return nil, httpx.NotFound()
	}
	if h.d.ApproveQrLogin == nil {
		return nil, httpx.ErrNotConfigured
	}
	row, err := h.findOpen(ctx, req.Code)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return pcloginapi.ApprovePcLogin200JSONResponse{Approved: false}, nil
	}
	if err := h.d.ApproveQrLogin(ctx, row.ClubID, req.Code, userID); err != nil {
		st, ok := status.FromError(err)
		switch {
		case !ok || st.Code() == codes.Internal || st.Code() == codes.Unavailable || st.Code() == codes.Unknown:
			return nil, err
		case st.Code() == codes.NotFound:
			return pcloginapi.ApprovePcLogin200JSONResponse{Approved: false}, nil
		default:
			// ValidationException::withMessages(['code' => …]) — текст старого кода (подстановки уже сделаны).
			return nil, httpx.Validation("code", st.Message())
		}
	}
	return pcloginapi.ApprovePcLogin200JSONResponse{Approved: true}, nil
}
