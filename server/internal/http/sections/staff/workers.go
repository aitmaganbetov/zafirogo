package staff

// Работники и приглашения. Laravel: app/Http/Controllers/WorkerController.php, ClubInvitationController::store().

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	staffapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/staff"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/realtime"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты WorkerController и ClubInvitationController (ключи перевода).
const (
	msgPhoneDigits        = "Введите 10 цифр номера после +7."
	msgNotRegistered      = "Пользователь с таким номером не зарегистрирован в Zafiro. Отправьте ему ссылку-приглашение."
	msgAlreadyWorker      = "Этот пользователь уже работает в клубе."
	msgWorkerAdded        = "Работник добавлен."
	msgWorkerAddedLog     = "Добавлен работник :user: :role"
	msgRoleChanged        = "Роль изменена."
	msgRoleChangedLog     = "Роль работника :user: :from → :to"
	msgWorkerRemoved      = "Работник убран из сотрудников. Аккаунт остался клиентом клуба."
	msgWorkerRemovedLog   = "Работник :user больше не сотрудник клуба"
	msgOwnRole            = "Свою роль изменить нельзя."
	msgAdminsByOwner      = "Администраторов меняет только владелец."
	msgInvitationCreated  = "Ссылка-приглашение создана. Она действует 7 дней."
	msgInvitationLog      = "Создано приглашение в клуб: роль :role"
	invitationTTL         = 7 * 24 * time.Hour
	invitationTokenLength = 64
	// workersPerPage — WorkerController::index() paginate(50).
	workersPerPage = 50
	maxString      = 255
)

// Морф-классы Laravel (PD-14).
const (
	subjectUser       = billingsvc.SubjectUser
	subjectMembership = `App\Models\ClubMembership`
	subjectInvitation = `App\Models\ClubInvitation`
)

var phoneRe = regexp.MustCompile(`^\+7\d{10}$`)

// invitationRoles — 'role' => 'in:admin,manager,cashier,operator' (ClubInvitationController::store()).
var invitationRoles = []string{permissions.RoleAdmin, permissions.RoleManager, permissions.RoleCashier, permissions.RoleOperator}

// assignableRoles — WorkerController::assignableRoles(): владелец назначает любую роль ClubRoles::assignable(),
// остальные — кроме администратора.
func assignableRoles(myRole string) []staffapi.RoleOption {
	out := make([]staffapi.RoleOption, 0, len(permissions.AssignableRoles))
	for _, r := range permissions.AssignableRoles {
		if r.Role == permissions.RoleAdmin && myRole != permissions.RoleOwner {
			continue
		}
		out = append(out, staffapi.RoleOption{Role: r.Role, Label: r.Label})
	}
	return out
}

func canAssign(myRole, role string) bool {
	return slices.ContainsFunc(assignableRoles(myRole), func(o staffapi.RoleOption) bool { return o.Role == role })
}

// manageable — шаблон workers/index ($manageable) и ensureManageable(): не владелец, не сам, администратора — только владелец.
func manageable(p principalView, role string, userID int64) bool {
	return role != permissions.RoleOwner && userID != p.userID && (role != permissions.RoleAdmin || p.role == permissions.RoleOwner)
}

// principalView — кто смотрит (роль в клубе и пользователь).
type principalView struct {
	userID int64
	role   string
}

func me(ctx context.Context) principalView {
	p := httpx.Principal(ctx)
	return principalView{userID: p.UserID, role: p.Role}
}

// workerCursor — keyset персонала: (ранг роли, id членства).
type workerCursor struct {
	Rank int32 `json:"r"`
	ID   int64 `json:"i"`
}

// clubSetting — $settings[section][key] ?? null (числа — json.Number, как StaffShifts::limit()).
func clubSetting(raw []byte, section, key string) any {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var all map[string]any
	if dec.Decode(&all) != nil {
		return nil
	}
	sec, _ := all[section].(map[string]any)
	return sec[key]
}

// onShift — StaffShifts::onShift() для ролей ClubRoles::SHIFT: члены клуба с ролью и живой сессией панели
// (активность за IdleTTL, решение Q-035). Без сессий (тесты) — никого.
func (h *handlers) onShift(ctx context.Context, q *store.Queries, clubID int64) (map[string][]int64, error) {
	out := map[string][]int64{}
	if h.d.Auth == nil {
		return out, nil
	}
	sessions := h.d.Auth.Sessions()
	since := h.d.NowFunc().Add(-sessions.IdleTTL())
	for _, role := range permissions.ShiftRoles {
		ids, err := q.ListClubUserIDsByRole(ctx, store.ListClubUserIDsByRoleParams{ClubID: clubID, Role: role, ExceptUserID: 0})
		if err != nil {
			return nil, err
		}
		active, err := sessions.ActiveUsers(ctx, ids, since)
		if err != nil {
			return nil, err
		}
		out[role] = active
	}
	return out, nil
}

// ListWorkers — WorkerController::index().
func (h *handlers) ListWorkers(ctx context.Context, req staffapi.ListWorkersRequestObject) (staffapi.ListWorkersResponseObject, error) {
	var next, prev workerCursor
	backward := req.Params.Before != nil && *req.Params.Before != ""
	if err := httpx.DecodeCursor(req.Params.Cursor, &next); err != nil {
		return nil, err
	}
	if err := httpx.DecodeCursor(req.Params.Before, &prev); err != nil {
		return nil, err
	}
	if backward && req.Params.Cursor != nil && *req.Params.Cursor != "" {
		return nil, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
	}
	size := int32(workersPerPage)
	if req.Params.Limit != nil {
		size = httpx.PageSize(req.Params.Limit)
	}
	if next.Rank == 0 && next.ID == 0 {
		next.Rank = -1
	}
	viewer := me(ctx)
	out := staffapi.WorkerPage{Items: []staffapi.Worker{}, PageSize: int(size), AssignableRoles: assignableRoles(viewer.role)}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		var rows []store.AdminListWorkersAfterRow
		if backward {
			list, err := q.AdminListWorkersBefore(ctx, store.AdminListWorkersBeforeParams{
				ClubID: clubID, BeforeRank: prev.Rank, BeforeID: prev.ID, PageSize: size + 1,
			})
			if err != nil {
				return err
			}
			more := len(list) > int(size)
			if more {
				list = list[:size]
			}
			for i := len(list) - 1; i >= 0; i-- {
				rows = append(rows, store.AdminListWorkersAfterRow(list[i]))
			}
			if more {
				out.PrevCursor = httpx.EncodeCursor(workerCursor{rows[0].RoleRank, rows[0].ID})
			}
			if len(rows) > 0 {
				last := rows[len(rows)-1]
				out.NextCursor = httpx.EncodeCursor(workerCursor{last.RoleRank, last.ID})
			}
		} else {
			list, err := q.AdminListWorkersAfter(ctx, store.AdminListWorkersAfterParams{
				ClubID: clubID, AfterRank: next.Rank, AfterID: next.ID, PageSize: size + 1,
			})
			if err != nil {
				return err
			}
			more := len(list) > int(size)
			if more {
				list = list[:size]
			}
			rows = list
			if more {
				last := rows[len(rows)-1]
				out.NextCursor = httpx.EncodeCursor(workerCursor{last.RoleRank, last.ID})
			}
			if next.Rank >= 0 && len(rows) > 0 {
				out.PrevCursor = httpx.EncodeCursor(workerCursor{rows[0].RoleRank, rows[0].ID})
			}
		}
		total, err := q.AdminCountWorkers(ctx, clubID)
		if err != nil {
			return err
		}
		out.Total = total
		shift, err := h.onShift(ctx, q, clubID)
		if err != nil {
			return err
		}
		club, err := q.GetClub(ctx, clubID)
		if err != nil {
			return err
		}
		var onShift []int64
		for _, role := range permissions.ShiftRoles {
			onShift = append(onShift, shift[role]...)
			sl := staffapi.ShiftLimit{Role: role, Count: len(shift[role])}
			if l := cash.ShiftLimit(clubSetting(club.Settings, "club", cash.ShiftLimitSetting(role))); l != nil {
				sl.Limit = httpx.Ptr(int(*l))
			}
			out.ShiftLimits = append(out.ShiftLimits, sl)
		}
		for _, r := range rows {
			out.Items = append(out.Items, staffapi.Worker{
				MembershipId: r.ID, UserId: r.UserID, Name: r.Name, Phone: r.Phone, Role: r.Role,
				RoleLabel: permissions.RoleLabel(r.Role), OnShift: slices.Contains(onShift, r.UserID),
				Manageable: manageable(viewer, r.Role, r.UserID),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return staffapi.ListWorkers200JSONResponse(out), nil
}

// audit — запись audit_log (категория permission): роли, права, приглашения.
func audit(ctx context.Context, q *store.Queries, clubID int64, action, subjectType string, subjectID int64, before, after any) error {
	a := httpx.Actor(ctx)
	p := store.InsertAuditLogParams{ClubID: &clubID, ActorKind: a.Kind, Category: "permission", Action: action,
		SubjectType: &subjectType, SubjectID: &subjectID}
	if a.UserID > 0 {
		p.ActorUserID = &a.UserID
	}
	if a.IP.IsValid() {
		ip := a.IP
		p.Ip = &ip
	}
	if a.RequestID != "" {
		p.RequestID = &a.RequestID
	}
	var err error
	if before != nil {
		if p.Before, err = json.Marshal(before); err != nil {
			return err
		}
	}
	if after != nil {
		if p.After, err = json.Marshal(after); err != nil {
			return err
		}
	}
	return q.InsertAuditLog(ctx, p)
}

// accessChanged — событие staff.access_changed: WebSocket сотрудника переподключается с новыми правами
// (docs/realtime.md); права REST и так читаются из БД на каждом запросе.
func accessChanged(ctx context.Context, q *store.Queries, clubID, userID int64) error {
	return billingsvc.AddEvent(ctx, q, clubID, realtime.AccessChangedEvent, map[string]any{"user_id": userID})
}

func (h *handlers) record(ctx context.Context, q *store.Queries, clubID, subjectID int64, event, key string, params map[string]string, details map[string]any) error {
	return billingsvc.RecordActivity(ctx, q, activity.Entry{
		ClubID: clubID, UserID: httpx.Principal(ctx).UserID, SubjectType: subjectUser, SubjectID: subjectID,
		Event: event, Description: h.d.T(ctx, key, params), Details: details,
	})
}

// workerPhone — WorkerController::store(): цифры; 11 цифр с ведущей 7 или 8 → без неё; '+7' + цифры.
func workerPhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) == 11 && (d[0] == '7' || d[0] == '8') {
		d = d[1:]
	}
	return "+7" + d
}

// AddWorker — WorkerController::store().
func (h *handlers) AddWorker(ctx context.Context, req staffapi.AddWorkerRequestObject) (staffapi.AddWorkerResponseObject, error) {
	viewer := me(ctx)
	phone := workerPhone(req.Body.Phone)
	role := strings.TrimSpace(req.Body.Role)
	var form httpx.FormErrors
	if !phoneRe.MatchString(phone) {
		form.Add("phone", msgPhoneDigits)
	}
	switch {
	case role == "":
		form.AddErr(httpx.RuleError("role", cash.RuleRequired))
	case !canAssign(viewer.role, role):
		form.AddErr(httpx.RuleError("role", cash.RuleIn))
	}
	if err := form.Err(); err != nil {
		return nil, err
	}
	clubID := httpx.ClubID(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		user, err := q.GetUserByPhone(ctx, phone)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.Validation("phone", msgNotRegistered)
		}
		if err != nil {
			return err
		}
		m, err := q.AdminMembershipByUserForUpdate(ctx, store.AdminMembershipByUserForUpdateParams{ClubID: clubID, UserID: user.ID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if err := q.AdminInsertStaffMembership(ctx, store.AdminInsertStaffMembershipParams{ClubID: clubID, UserID: user.ID, Role: role, Now: &now}); err != nil {
				return err
			}
			if err := q.EnsureMemberBalance(ctx, store.EnsureMemberBalanceParams{ClubID: clubID, UserID: user.ID}); err != nil {
				return err
			}
		case err != nil:
			return err
		case permissions.IsStaff(m.Role):
			return httpx.Validation("phone", msgAlreadyWorker)
		default:
			if err := q.AdminSetMembershipRole(ctx, store.AdminSetMembershipRoleParams{Role: role, Now: &now, ClubID: clubID, ID: m.ID}); err != nil {
				return err
			}
		}
		if err := h.record(ctx, q, clubID, user.ID, "worker.added", msgWorkerAddedLog,
			map[string]string{"user": user.Name, "role": h.d.T(ctx, permissions.RoleLabel(role), nil)},
			map[string]any{"role": role}); err != nil {
			return err
		}
		if err := audit(ctx, q, clubID, "worker.added", subjectUser, user.ID, nil, map[string]any{"role": role}); err != nil {
			return err
		}
		return accessChanged(ctx, q, clubID, user.ID)
	})
	if err != nil {
		return nil, err
	}
	return staffapi.AddWorker200JSONResponse{Message: h.d.T(ctx, msgWorkerAdded, nil)}, nil
}

// manageableMember — WorkerController::ensureManageable(): сотрудник клуба, не владелец (иначе 404); не сам (403);
// администратора — только владелец (403). Строка членства блокируется.
func manageableMember(ctx context.Context, q *store.Queries, clubID, id int64) (store.AdminGetMembershipForUpdateRow, error) {
	m, err := q.AdminGetMembershipForUpdate(ctx, store.AdminGetMembershipForUpdateParams{ClubID: clubID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return m, httpx.NotFound()
	}
	if err != nil {
		return m, err
	}
	viewer := me(ctx)
	switch {
	case !permissions.IsStaff(m.Role) || m.Role == permissions.RoleOwner:
		return m, httpx.NotFound()
	case m.UserID == viewer.userID:
		return m, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, msgOwnRole)
	case m.Role == permissions.RoleAdmin && viewer.role != permissions.RoleOwner:
		return m, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, msgAdminsByOwner)
	}
	return m, nil
}

// ChangeWorkerRole — WorkerController::update().
func (h *handlers) ChangeWorkerRole(ctx context.Context, req staffapi.ChangeWorkerRoleRequestObject) (staffapi.ChangeWorkerRoleResponseObject, error) {
	clubID, viewer := httpx.ClubID(ctx), me(ctx)
	role := strings.TrimSpace(req.Body.Role)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		m, err := manageableMember(ctx, q, clubID, req.MembershipId)
		if err != nil {
			return err
		}
		switch {
		case role == "":
			return httpx.RuleError("role", cash.RuleRequired)
		case !canAssign(viewer.role, role):
			return httpx.RuleError("role", cash.RuleIn)
		case m.Role == role:
			return nil
		}
		if err := q.AdminSetMembershipRole(ctx, store.AdminSetMembershipRoleParams{Role: role, Now: &now, ClubID: clubID, ID: m.ID}); err != nil {
			return err
		}
		if err := h.record(ctx, q, clubID, m.UserID, "worker.role_changed", msgRoleChangedLog, map[string]string{
			"user": m.Name, "from": h.d.T(ctx, permissions.RoleLabel(m.Role), nil), "to": h.d.T(ctx, permissions.RoleLabel(role), nil),
		}, map[string]any{"from": m.Role, "to": role}); err != nil {
			return err
		}
		if err := audit(ctx, q, clubID, "worker.role_changed", subjectMembership, m.ID,
			map[string]any{"role": m.Role}, map[string]any{"role": role}); err != nil {
			return err
		}
		return accessChanged(ctx, q, clubID, m.UserID)
	})
	if err != nil {
		return nil, err
	}
	return staffapi.ChangeWorkerRole200JSONResponse{Message: h.d.T(ctx, msgRoleChanged, nil)}, nil
}

// RemoveWorker — WorkerController::destroy(): роль client (баланс остаётся); сессии панели сотрудника отзываются.
func (h *handlers) RemoveWorker(ctx context.Context, req staffapi.RemoveWorkerRequestObject) (staffapi.RemoveWorkerResponseObject, error) {
	clubID := httpx.ClubID(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		m, err := manageableMember(ctx, q, clubID, req.MembershipId)
		if err != nil {
			return err
		}
		if err := q.AdminSetMembershipRole(ctx, store.AdminSetMembershipRoleParams{Role: permissions.RoleClient, Now: &now, ClubID: clubID, ID: m.ID}); err != nil {
			return err
		}
		if err := h.record(ctx, q, clubID, m.UserID, "worker.removed", msgWorkerRemovedLog,
			map[string]string{"user": m.Name}, map[string]any{"from": m.Role}); err != nil {
			return err
		}
		if err := audit(ctx, q, clubID, "worker.removed", subjectMembership, m.ID,
			map[string]any{"role": m.Role}, map[string]any{"role": permissions.RoleClient}); err != nil {
			return err
		}
		if err := accessChanged(ctx, q, clubID, m.UserID); err != nil {
			return err
		}
		// Принцип 4 (DECISIONS): бывший сотрудник выходит из панели сразу (в старом сессия жила до истечения,
		// но страницы отвечали 403 по роли).
		if h.d.Auth != nil {
			sessions, userID, log := h.d.Auth.Sessions(), m.UserID, h.d.Log()
			billingsvc.AfterCommit(ctx, func(ctx context.Context) {
				if err := sessions.RevokeUser(ctx, userID); err != nil {
					log.WarnContext(ctx, "revoke removed worker sessions", "user_id", userID, "err", err)
				}
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return staffapi.RemoveWorker200JSONResponse{Message: h.d.T(ctx, msgWorkerRemoved, nil)}, nil
}

// invitationToken — Str::random(64): буквы и цифры.
func invitationToken() (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, invitationTokenLength)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		b[i] = chars[n.Int64()]
	}
	return string(b), nil
}

// CreateInvitation — ClubInvitationController::store().
func (h *handlers) CreateInvitation(ctx context.Context, req staffapi.CreateInvitationRequestObject) (staffapi.CreateInvitationResponseObject, error) {
	var email *string
	if req.Body.Email != nil {
		if e := strings.TrimSpace(*req.Body.Email); e != "" {
			email = &e
		}
	}
	role := strings.TrimSpace(req.Body.Role)
	var form httpx.FormErrors
	if email != nil {
		a, err := mail.ParseAddress(*email)
		switch {
		case err != nil || a.Address != *email || a.Name != "":
			form.AddErr(httpx.RuleError("email", "email"))
		case utf8.RuneCountInString(*email) > maxString:
			form.AddErr(httpx.RuleErrorWith("email", "max_string", strconv.Itoa(maxString)))
		}
	}
	switch {
	case role == "":
		form.AddErr(httpx.RuleError("role", cash.RuleRequired))
	case !slices.Contains(invitationRoles, role):
		form.AddErr(httpx.RuleError("role", cash.RuleIn))
	}
	if err := form.Err(); err != nil {
		return nil, err
	}
	// abort_if($inviterRole === 'manager' && role === 'admin', 403)
	if me(ctx).role == permissions.RoleManager && role == permissions.RoleAdmin {
		return nil, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, "")
	}
	token, err := invitationToken()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(token))
	clubID, now := httpx.ClubID(ctx), h.d.NowFunc()
	expires := now.Add(invitationTTL)
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		inviter := httpx.Principal(ctx).UserID
		id, err := q.AdminInsertInvitation(ctx, store.AdminInsertInvitationParams{
			ClubID: clubID, Email: email, Role: role, TokenHash: hex.EncodeToString(sum[:]), InvitedBy: &inviter,
			ExpiresAt: expires, Now: &now,
		})
		if err != nil {
			return err
		}
		emailText := "—"
		if email != nil {
			emailText = *email
		}
		details := map[string]any{
			"role": role, "email": emailText,
			// $invitation->expires_at->toDateTimeString() (config app.timezone = UTC).
			"expires_at": expires.UTC().Format(time.DateTime),
		}
		if err := billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, UserID: inviter, SubjectType: subjectInvitation, SubjectID: id, Event: "invitation.created",
			Description: h.d.T(ctx, msgInvitationLog, map[string]string{"role": role}), Details: details,
		}); err != nil {
			return err
		}
		// Аудит — без email и токена (только роль и срок).
		return audit(ctx, q, clubID, "invitation.created", subjectInvitation, id, nil, map[string]any{
			"role": role, "expires_at": expires.UTC(),
		})
	})
	if err != nil {
		return nil, err
	}
	return staffapi.CreateInvitation201JSONResponse{
		Body:    staffapi.InvitationCreated{Path: "/invitations/" + token, Message: h.d.T(ctx, msgInvitationCreated, nil)},
		Headers: staffapi.CreateInvitation201ResponseHeaders{CacheControl: httpx.Ptr("no-store, private")},
	}, nil
}
