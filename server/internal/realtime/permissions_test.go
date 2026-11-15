package realtime

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// Аудит безопасности A-07: событие панели получает только персонал клуба — не клиент (в том числе со старой
// не-сотруднической ролью) и не пользователь без роли.
func TestAllowedOnlyForStaff(t *testing.T) {
	for _, role := range permissions.StaffRoles {
		if !Allowed(principal.Principal{UserID: 1, ClubID: 3, Role: role, Permissions: permissions.All()}, "notifications.changed") {
			t.Errorf("staff role %q must get panel events", role)
		}
	}
	for _, role := range []string{permissions.RoleClient, "Пользователь", ""} {
		if Allowed(principal.Principal{UserID: 1, ClubID: 3, Role: role, Permissions: permissions.All()}, "notifications.changed") {
			t.Errorf("role %q must not get panel events", role)
		}
	}
}

// A-07: /ws клиенту клуба не открывается (403 до рукопожатия WebSocket).
func TestWebSocketRefusesClientRole(t *testing.T) {
	h := &Handler{}
	for _, role := range []string{permissions.RoleClient, "Пользователь", ""} {
		req := httptest.NewRequest(http.MethodGet, "/ws", nil)
		req = req.WithContext(principal.WithPrincipal(req.Context(), principal.Principal{UserID: 6, ClubID: 3, Role: role}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("role %q: /ws → %d, want 403", role, rec.Code)
		}
	}
}
