package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

func TestSuperadminHostMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := superadminHost("admin.zafiro.kz")(ok)
	cases := []struct {
		host, path string
		p          *principal.Principal
		want       int
	}{
		{"zafiro.kz", "/api/superadmin/clubs", nil, http.StatusNotFound},
		{"admin.zafiro.kz:443", "/api/superadmin/clubs", nil, http.StatusTeapot},
		{"admin.zafiro.kz", "/api/auth/login", nil, http.StatusTeapot},
		{"admin.zafiro.kz", "/api/i18n/ru", nil, http.StatusTeapot},
		{"admin.zafiro.kz", "/clubs", nil, http.StatusTeapot},
		{"admin.zafiro.kz", "/api/cash/shift", &principal.Principal{UserID: 1, Superadmin: true}, http.StatusForbidden},
		{"admin.zafiro.kz", "/api/cash/shift", &principal.Principal{UserID: 1, Superadmin: true, ReadOnly: true, ClubID: 2}, http.StatusTeapot},
		{"zafiro.kz", "/api/cash/shift", nil, http.StatusTeapot},
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
	if superadminHost("")(ok) == nil {
		t.Fatal("nil handler")
	}
}
