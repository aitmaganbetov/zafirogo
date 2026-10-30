package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// BearerToken — access-токен из заголовка Authorization: Bearer <токен>; "" — нет.
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// Middleware кладёт Principal в контекст, если запрос несёт access-токен. Без токена запрос идёт дальше
// анонимным (права проверяют permissions.Require*). Неверный/отозванный токен — 401, заблокированный
// пользователь — 403 «Аккаунт заблокирован.» (SetCurrentClub, ветка api/*).
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := BearerToken(r)
		if tok == "" {
			next.ServeHTTP(w, r)
			return
		}
		p, err := s.Authenticate(r.Context(), tok)
		switch {
		case err == nil:
			next.ServeHTTP(w, r.WithContext(principal.WithPrincipal(r.Context(), p)))
		case errors.Is(err, ErrUnauthenticated):
			permissions.Unauthenticated(w)
		case errors.Is(err, ErrBlocked):
			permissions.Forbidden(w, MsgBlockedAPI)
		default:
			s.log.ErrorContext(r.Context(), "authenticate", slog.String("err", err.Error()))
			permissions.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "")
		}
	})
}
