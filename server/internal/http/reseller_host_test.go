package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// RestrictResellerHost (GS-561): на хосте реселлера — только кабинет, вход и восстановление пароля.
func TestResellerHostMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := resellerHost("resseler.zafiro.kz")(ok)
	sa := &principal.Principal{UserID: 1, Superadmin: true}
	viewing := &principal.Principal{UserID: 1, Superadmin: true, ReadOnly: true, ClubID: 2}
	staff := &principal.Principal{UserID: 2, ClubID: 2, Role: "owner"}
	cases := []struct {
		host, path string
		p          *principal.Principal
		want       int
	}{
		// Кабинета реселлера нет на других хостах.
		{"zafiro.kz", "/api/reseller/dashboard", staff, http.StatusNotFound},
		{"admin.zafiro.kz", "/api/reseller/dashboard", sa, http.StatusNotFound},
		{"resseler.zafiro.kz:443", "/api/reseller/dashboard", nil, http.StatusTeapot},
		{"RESSELER.zafiro.kz", "/api/reseller/dashboard", nil, http.StatusTeapot},
		// Вход, сессия, второй фактор, восстановление пароля, переводы.
		{"resseler.zafiro.kz", "/api/auth/login", nil, http.StatusTeapot},
		{"resseler.zafiro.kz", "/api/auth/me", staff, http.StatusTeapot},
		{"resseler.zafiro.kz", "/api/auth/logout", staff, http.StatusTeapot},
		{"resseler.zafiro.kz", "/api/auth/second-factor/verify", nil, http.StatusTeapot},
		{"resseler.zafiro.kz", "/api/forgot-password", nil, http.StatusTeapot},
		{"resseler.zafiro.kz", "/api/reset-password", nil, http.StatusTeapot},
		{"resseler.zafiro.kz", "/api/i18n/ru", nil, http.StatusTeapot},
		// Регистрации клуба, панели клуба и кабинета суперадмина здесь нет — никому.
		{"resseler.zafiro.kz", "/api/auth/register", nil, http.StatusForbidden},
		{"resseler.zafiro.kz", "/api/cash/shift", staff, http.StatusForbidden},
		{"resseler.zafiro.kz", "/api/settings/reseller", staff, http.StatusForbidden},
		{"resseler.zafiro.kz", "/api/superadmin/resellers", sa, http.StatusForbidden},
		{"resseler.zafiro.kz", "/api/cash/shift", viewing, http.StatusForbidden},
		{"resseler.zafiro.kz", "/api/ws-ticket", staff, http.StatusForbidden},
		{"resseler.zafiro.kz", "/api/resellers", staff, http.StatusForbidden}, // похожий префикс — не кабинет
		// Страницы SPA и файлы отдаются (в кабинет уводит SPA).
		{"resseler.zafiro.kz", "/dashboard", nil, http.StatusTeapot},
		{"resseler.zafiro.kz", "/assets/app.js", nil, http.StatusTeapot},
		// Остальные хосты не затронуты.
		{"zafiro.kz", "/api/cash/shift", staff, http.StatusTeapot},
		{"zafiro.kz", "/api/auth/register", nil, http.StatusTeapot},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.Host = c.host
		if c.p != nil {
			req = req.WithContext(principal.WithPrincipal(req.Context(), *c.p))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s%s: %d, want %d", c.host, c.path, rec.Code, c.want)
		}
	}
	// Без домена кабинет — по /reseller на любом хосте.
	req := httptest.NewRequest(http.MethodGet, "/api/reseller/dashboard", nil)
	rec := httptest.NewRecorder()
	resellerHost("")(ok).ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("без домена: %d", rec.Code)
	}
}
