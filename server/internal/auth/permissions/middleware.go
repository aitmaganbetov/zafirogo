package permissions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

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
	// MsgNoSectionRight — Laravel: RequireClubPermission::handle() (club-can:… в API сотрудника).
	MsgNoSectionRight = "Нет права на этот раздел. Его даёт владелец клуба в «Настройки → Права»."
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

// RequirePermission — право «section.action» в выбранном клубе (RequireClub + ClubPermissions::allows).
// Laravel: RequireClubArea (маршруты панели) — текст MsgNoRight. Несуществующее право — паника при
// сборке роутера: в старом такой маршрут отклонялся всегда (GS-254), здесь ошибка видна сразу.
func RequirePermission(right string) func(http.Handler) http.Handler {
	return requirePermission(right, MsgNoRight)
}

// RequireSectionPermission — то же с текстом RequireClubPermission (club-can:… — API сотрудника).
func RequireSectionPermission(right string) func(http.Handler) http.Handler {
	return requirePermission(right, MsgNoSectionRight)
}

func requirePermission(right, message string) func(http.Handler) http.Handler {
	if !Exists(right) {
		panic(fmt.Sprintf("permissions: unknown right %q", right))
	}
	return func(next http.Handler) http.Handler {
		return RequireClub(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := principal.FromContext(r.Context())
			if !p.Can(right) {
				Forbidden(w, message)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// RequireRole — роль в клубе: владелец всегда, иначе одна из roles. Laravel: RequireClubRole
// (club-role:owner,admin,… ; отказ — abort(403) без текста).
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return RequireClub(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := principal.FromContext(r.Context())
			if p.Role == "" || (p.Role != RoleOwner && !slices.Contains(roles, p.Role)) {
				Forbidden(w, "")
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// RequireStaff — любая роль сотрудника (club-role:owner,admin,manager,cashier,operator).
func RequireStaff(next http.Handler) http.Handler { return RequireRole(StaffRoles...)(next) }

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
