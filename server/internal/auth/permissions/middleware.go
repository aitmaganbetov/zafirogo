package permissions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"syscall"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// Тексты отказов — как в старом коде (русский текст = ключ перевода).
const (
	// MsgUnauthenticated — Laravel: AuthenticationException для JSON ({"message": "Unauthenticated."}).
	MsgUnauthenticated = "Unauthenticated."
	// MsgNoClubs — Laravel: SetCurrentClub::handle() (нет ни одного членства).
	MsgNoClubs = "У аккаунта нет доступа ни к одному клубу."
	// MsgClubForbidden — Laravel: SetCurrentClub::handle() (выбранного клуба нет среди членств).
	MsgClubForbidden = "Нет доступа к выбранному клубу."
	// MsgReadOnly — Laravel: SetCurrentClub::viewAsSuperadmin() (не GET/HEAD в режиме просмотра).
	MsgReadOnly = "Режим просмотра: суперадмин не может ничего менять в клубе."
	// MsgNoRight — Laravel: RequireClubArea::handle() (право маршрута панели).
	MsgNoRight = "Нет права на это действие. Его даёт владелец клуба в «Настройки → Права»."
)

// Коды ошибок в теле ответа.
const (
	CodeUnauthenticated = "unauthenticated"
	CodeForbidden       = "forbidden"
	CodeReadOnly        = "read_only"
)

// Error — тело ошибки {"error": код, "message": текст}.
type Error struct {
	Code    string `json:"error"`
	Message string `json:"message,omitempty"`
}

// WriteError пишет JSON-ошибку.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Code: code, Message: message})
}

// StatusClientClosedRequest — клиент закрыл запрос до ответа (код nginx 499): ответ уже никто не читает. Не 5xx —
// сервер исправен, иначе каждый уход со страницы с незавершённой загрузкой выглядит в журнале и метриках как сбой.
const StatusClientClosedRequest = 499

// ClientGone — ошибка вызвана тем, что клиент ушёл: контекст запроса отменён (context.Canceled при отменённом
// r.Context()) или соединение уже закрыто (EPIPE, ECONNRESET при записи ответа). Дедлайн запроса
// (http.request_timeout, context.DeadlineExceeded) сюда не относится — это медленный сервер, он остаётся 5xx.
func ClientGone(r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) && errors.Is(r.Context().Err(), context.Canceled) {
		return true
	}
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
}

// Forbidden — 403 с текстом.
func Forbidden(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusForbidden, CodeForbidden, message)
}

// Unauthenticated — 401.
func Unauthenticated(w http.ResponseWriter) {
	WriteError(w, http.StatusUnauthorized, CodeUnauthenticated, MsgUnauthenticated)
}

// RequireAuth — запрос с Principal (аутентификация — auth/session.Middleware), иначе 401.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal.FromContext(r.Context()); !ok {
			Unauthenticated(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CheckClub — проверки RequireClub без HTTP: ok или (статус, код, текст) отказа.
// Laravel: SetCurrentClub — клуб выбран; в режиме просмотра суперадмином всё, кроме GET/HEAD, — 403.
func CheckClub(p principal.Principal, method string) (status int, code, message string, ok bool) {
	if p.ClubID <= 0 {
		return http.StatusForbidden, CodeForbidden, MsgNoClubs, false
	}
	if p.ReadOnly && !isSafe(method) {
		return http.StatusForbidden, CodeReadOnly, MsgReadOnly, false
	}
	return 0, "", "", true
}

// isSafe — Laravel Request::isMethodSafe(): GET, HEAD, OPTIONS, TRACE.
func isSafe(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// RequireClub — пользователь в выбранном клубе; в режиме просмотра суперадмином — только чтение.
// Laravel: middleware current-club (SetCurrentClub).
func RequireClub(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			Unauthenticated(w)
			return
		}
		if status, code, msg, ok := CheckClub(p, r.Method); !ok {
			WriteError(w, status, code, msg)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Права разделов панели проверяет httpx.Access (server/internal/http/httpx/strict.go) по таблице RouteRights —
// отдельных middleware «право»/«роль» нет (удалены как недостижимые из cmd/zafiro, аудит BC-11); роль там, где
// Laravel ставил club-role:…, проверяет обработчик раздела.

// RequireSuperadmin — кабинет суперадмина. Laravel: RequireSuperadmin — флаг суперадмина и не заблокирован
// (заблокированный пользователь Principal не получает вовсе — auth/session).
func RequireSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			Unauthenticated(w)
			return
		}
		if !p.Superadmin {
			Forbidden(w, "")
			return
		}
		next.ServeHTTP(w, r)
	})
}
