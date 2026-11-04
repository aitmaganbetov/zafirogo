package staff

import (
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
)

func TestManageableAndMayEdit(t *testing.T) {
	owner := principalView{userID: 1, role: permissions.RoleOwner}
	admin := principalView{userID: 2, role: permissions.RoleAdmin}
	cases := []struct {
		viewer       principalView
		role         string
		user         int64
		manage, edit bool
	}{
		{owner, permissions.RoleOwner, 1, false, false}, // владельца не меняют
		{owner, permissions.RoleAdmin, 2, true, true},   // администратора — владелец
		{admin, permissions.RoleAdmin, 2, false, false}, // себя — нельзя
		{admin, permissions.RoleAdmin, 3, false, false}, // другого администратора — только владелец
		{admin, permissions.RoleCashier, 4, true, true}, // кассира — администратор
		{admin, permissions.RoleClient, 5, true, false}, // клиент — не сотрудник (mayEdit)
	}
	for i, c := range cases {
		if got := manageable(c.viewer, c.role, c.user); got != c.manage {
			t.Errorf("%d manageable: %v", i, got)
		}
		if got := mayEdit(c.viewer, c.role, c.user); got != c.edit {
			t.Errorf("%d mayEdit: %v", i, got)
		}
	}
}

func TestAssignableRoles(t *testing.T) {
	if n := len(assignableRoles(permissions.RoleOwner)); n != 4 {
		t.Fatal(n)
	}
	if canAssign(permissions.RoleAdmin, permissions.RoleAdmin) || !canAssign(permissions.RoleAdmin, permissions.RoleOperator) {
		t.Fatal("admin assigns")
	}
}

func TestWorkerPhone(t *testing.T) {
	for in, want := range map[string]string{"700 123 45 67": "+77001234567", "8 700 123 45 67": "+77001234567", "": "+7"} {
		if got := workerPhone(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestGivenJSONOrder(t *testing.T) {
	// Порядок ClubPermissions::all(), пусто — NULL.
	if givenJSON(permissions.All(), nil) != nil {
		t.Fatal("empty must be NULL")
	}
	got := string(givenJSON(permissions.All(), map[string]bool{"workers.view": true, "cash.delete": false}))
	if got != `{"cash.delete":false,"workers.view":true}` {
		t.Fatal(got)
	}
}

func TestInvitationToken(t *testing.T) {
	tok, err := invitationToken()
	if err != nil || len(tok) != 64 {
		t.Fatal(tok, err)
	}
}
