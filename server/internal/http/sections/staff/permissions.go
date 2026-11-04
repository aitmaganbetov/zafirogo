package staff

// Настройки → Права. Laravel: app/Http/Controllers/PermissionController.php (club-role:owner,admin).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	staffapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/staff"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты PermissionController (ключи перевода).
const (
	msgOwnerSetsAdmins   = "Права администратора задаёт только владелец клуба."
	msgRightsSaved       = "Права сохранены."
	msgRightsUpdatedLog  = "Изменены права сотрудника: :user"
	msgRightsByRole      = "Права сотрудника снова по роли."
	msgRightsByRoleLog   = "Права сотрудника по роли: :user"
	msgYes               = "да"
	msgNo                = "нет"
	permissionsUpdatedEv = "permissions.updated"
)

// ownerOrAdmin — club-role:owner,admin (RequireClubRole): вход (401), выбранный клуб и запрет записи в режиме
// просмотра (RequireClub), роль владельца или администратора (403 без текста, abort(403)).
func ownerOrAdmin(ctx context.Context) (principalView, error) {
	p, ok := principal.FromContext(ctx)
	if !ok {
		return principalView{}, httpx.NewAPIError(http.StatusUnauthorized, permissions.CodeUnauthenticated, permissions.MsgUnauthenticated)
	}
	method := http.MethodGet
	if _, r := httpx.HTTPFrom(ctx); r != nil {
		method = r.Method
	}
	if status, code, msg, ok := permissions.CheckClub(p, method); !ok {
		return principalView{}, httpx.NewAPIError(status, code, msg)
	}
	if p.Role != permissions.RoleOwner && p.Role != permissions.RoleAdmin {
		return principalView{}, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, "")
	}
	return principalView{userID: p.UserID, role: p.Role}, nil
}

// mayEdit — PermissionController::mayEdit(): сотрудник (не владелец); владелец правит всех, администратор —
// не администраторов и не себя.
func mayEdit(viewer principalView, role string, userID int64) bool {
	if !permissions.IsStaff(role) || role == permissions.RoleOwner {
		return false
	}
	return viewer.role == permissions.RoleOwner ||
		(viewer.role == permissions.RoleAdmin && role != permissions.RoleAdmin && userID != viewer.userID)
}

// customized — !empty($member->permissions): NULL, {} и [] — пусто.
func customized(raw []byte) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && !bytes.Equal(t, []byte("null")) && !bytes.Equal(t, []byte("{}")) && !bytes.Equal(t, []byte("[]"))
}

// ListPermissionStaff — PermissionController::index().
func (h *handlers) ListPermissionStaff(ctx context.Context, _ staffapi.ListPermissionStaffRequestObject) (staffapi.ListPermissionStaffResponseObject, error) {
	viewer, err := ownerOrAdmin(ctx)
	if err != nil {
		return nil, err
	}
	out := staffapi.PermissionStaffList{Items: []staffapi.PermissionStaffMember{}}
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := q.AdminListPermissionStaff(ctx, httpx.ClubID(ctx))
		if err != nil {
			return err
		}
		for _, r := range rows {
			out.Items = append(out.Items, staffapi.PermissionStaffMember{
				MembershipId: r.ID, Name: r.Name, Phone: r.Phone, Role: r.Role, RoleLabel: permissions.RoleLabel(r.Role),
				Customized: r.Customized, Editable: mayEdit(viewer, r.Role, r.UserID),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return staffapi.ListPermissionStaff200JSONResponse(out), nil
}

// editableMember — PermissionController::authorizeFor(): сотрудник клуба, не владелец (404); mayEdit (403).
func editableMember(ctx context.Context, q *store.Queries, viewer principalView, id int64, lock bool) (store.AdminGetMembershipRow, error) {
	var (
		m   store.AdminGetMembershipRow
		err error
	)
	clubID := httpx.ClubID(ctx)
	if lock {
		var r store.AdminGetMembershipForUpdateRow
		r, err = q.AdminGetMembershipForUpdate(ctx, store.AdminGetMembershipForUpdateParams{ClubID: clubID, ID: id})
		m = store.AdminGetMembershipRow(r)
	} else {
		m, err = q.AdminGetMembership(ctx, store.AdminGetMembershipParams{ClubID: clubID, ID: id})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return m, httpx.NotFound()
	}
	if err != nil {
		return m, err
	}
	if !permissions.IsStaff(m.Role) || m.Role == permissions.RoleOwner {
		return m, httpx.NotFound()
	}
	if !mayEdit(viewer, m.Role, m.UserID) {
		return m, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, msgOwnerSetsAdmins)
	}
	return m, nil
}

// matrix — ClubPermissions::sections() и actionLabels() с состоянием сотрудника (hasPermission, given).
func matrix(m store.AdminGetMembershipRow) ([]staffapi.PermissionAction, []staffapi.PermissionSection) {
	mem := &permissions.Membership{Role: m.Role, Given: permissions.ParseGiven(m.Permissions)}
	actions := make([]staffapi.PermissionAction, 0, len(permissions.Actions))
	for _, a := range permissions.Actions {
		actions = append(actions, staffapi.PermissionAction{Action: a, Label: permissions.ActionLabels[a]})
	}
	sections := make([]staffapi.PermissionSection, 0, len(permissions.Sections))
	for _, s := range permissions.Sections {
		labels := permissions.SectionLabels[s.Name]
		sec := staffapi.PermissionSection{Section: s.Name, Label: labels.Label, Cells: []staffapi.PermissionCell{}}
		for _, a := range permissions.Actions {
			if _, ok := s.Actions[a]; !ok {
				continue
			}
			right := s.Name + "." + a
			_, given := permissions.Given(mem, right)
			sec.Cells = append(sec.Cells, staffapi.PermissionCell{
				Action: a, Description: labels.Actions[a], Allowed: permissions.Allows(mem, right), Given: given,
			})
		}
		sections = append(sections, sec)
	}
	return actions, sections
}

func memberView(viewer principalView, m store.AdminGetMembershipRow) staffapi.PermissionStaffMember {
	return staffapi.PermissionStaffMember{
		MembershipId: m.ID, Name: m.Name, Phone: m.Phone, Role: m.Role, RoleLabel: permissions.RoleLabel(m.Role),
		Customized: customized(m.Permissions), Editable: mayEdit(viewer, m.Role, m.UserID),
	}
}

// GetStaffPermissions — PermissionController::edit().
func (h *handlers) GetStaffPermissions(ctx context.Context, req staffapi.GetStaffPermissionsRequestObject) (staffapi.GetStaffPermissionsResponseObject, error) {
	viewer, err := ownerOrAdmin(ctx)
	if err != nil {
		return nil, err
	}
	var out staffapi.StaffPermissions
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		m, err := editableMember(ctx, q, viewer, req.MembershipId, false)
		if err != nil {
			return err
		}
		out.Member = memberView(viewer, m)
		out.Actions, out.Sections = matrix(m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return staffapi.GetStaffPermissions200JSONResponse(out), nil
}

// givenJSON — club_memberships.permissions: {"section.action": bool} в порядке ClubPermissions::all() (как массив
// PHP), пусто — NULL ($given === [] ? null : $given).
func givenJSON(rights []string, given map[string]bool) json.RawMessage {
	if len(given) == 0 {
		return nil
	}
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	for _, r := range rights {
		v, ok := given[r]
		if !ok {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		k, _ := json.Marshal(r)
		b.Write(k)
		b.WriteByte(':')
		b.WriteString(strconv.FormatBool(v))
	}
	b.WriteByte('}')
	return b.Bytes()
}

// UpdateStaffPermissions — PermissionController::update(): хранится только отличие от прав роли.
func (h *handlers) UpdateStaffPermissions(ctx context.Context, req staffapi.UpdateStaffPermissionsRequestObject) (staffapi.UpdateStaffPermissionsResponseObject, error) {
	viewer, err := ownerOrAdmin(ctx)
	if err != nil {
		return nil, err
	}
	sent := req.Body.Rights
	clubID, now := httpx.ClubID(ctx), h.d.NowFunc()
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		m, err := editableMember(ctx, q, viewer, req.MembershipId, true)
		if err != nil {
			return err
		}
		mem := &permissions.Membership{Role: m.Role, Given: permissions.ParseGiven(m.Permissions)}
		all := permissions.All()
		given := map[string]bool{}
		changes := map[string]any{}
		for _, right := range all {
			section, action := splitRight(right)
			after := sent[section][action] // нет в запросе — false (hidden-поле «0» формы)
			if after != permissions.ByRole(m.Role, right) {
				given[right] = after
			}
			if after != permissions.Allows(mem, right) {
				label := h.d.T(ctx, permissions.SectionLabels[section].Label, nil) + ": " + h.d.T(ctx, permissions.ActionLabels[action], nil)
				changes[label] = h.d.T(ctx, map[bool]string{true: msgYes, false: msgNo}[after], nil)
			}
		}
		if err := q.AdminSetMembershipPermissions(ctx, store.AdminSetMembershipPermissionsParams{
			Permissions: givenJSON(all, given), Now: &now, ClubID: clubID, ID: m.ID,
		}); err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		if err := h.record(ctx, q, clubID, m.UserID, permissionsUpdatedEv, msgRightsUpdatedLog, map[string]string{"user": m.Name}, changes); err != nil {
			return err
		}
		if err := audit(ctx, q, clubID, permissionsUpdatedEv, subjectMembership, m.ID,
			map[string]any{"permissions": mem.Given}, map[string]any{"permissions": given}); err != nil {
			return err
		}
		return accessChanged(ctx, q, clubID, m.UserID)
	})
	if err != nil {
		return nil, err
	}
	return staffapi.UpdateStaffPermissions200JSONResponse{Message: h.d.T(ctx, msgRightsSaved, nil)}, nil
}

// ResetStaffPermissions — PermissionController::reset(): права снова по роли.
func (h *handlers) ResetStaffPermissions(ctx context.Context, req staffapi.ResetStaffPermissionsRequestObject) (staffapi.ResetStaffPermissionsResponseObject, error) {
	viewer, err := ownerOrAdmin(ctx)
	if err != nil {
		return nil, err
	}
	clubID, now := httpx.ClubID(ctx), h.d.NowFunc()
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		m, err := editableMember(ctx, q, viewer, req.MembershipId, true)
		if err != nil {
			return err
		}
		if err := q.AdminSetMembershipPermissions(ctx, store.AdminSetMembershipPermissionsParams{Now: &now, ClubID: clubID, ID: m.ID}); err != nil {
			return err
		}
		if err := h.record(ctx, q, clubID, m.UserID, permissionsUpdatedEv, msgRightsByRoleLog, map[string]string{"user": m.Name},
			map[string]any{"reset": true}); err != nil {
			return err
		}
		if err := audit(ctx, q, clubID, permissionsUpdatedEv, subjectMembership, m.ID,
			map[string]any{"permissions": permissions.ParseGiven(m.Permissions)}, map[string]any{"permissions": nil, "reset": true}); err != nil {
			return err
		}
		return accessChanged(ctx, q, clubID, m.UserID)
	})
	if err != nil {
		return nil, err
	}
	return staffapi.ResetStaffPermissions200JSONResponse{Message: h.d.T(ctx, msgRightsByRole, nil)}, nil
}

func splitRight(right string) (string, string) {
	for i := range len(right) {
		if right[i] == '.' {
			return right[:i], right[i+1:]
		}
	}
	return right, ""
}
