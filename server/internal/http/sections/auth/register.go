package auth

// Регистрация владельца с клубом и приглашения в клуб. Laravel: AuthController::register(),
// ClubInvitationController::show()/accept(). Билет WebSocket (решение EV-3).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	authapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/auth"
	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты AuthController::register() (ключи перевода) и ClubInvitationController.
const (
	msgNameRequired     = "Имя обязательно."
	msgClubNameRequired = "Название клуба обязательно."
	msgPhoneTaken       = "Этот номер телефона уже зарегистрирован."
	msgPasswordMin      = "Пароль должен содержать минимум 6 символов." // #nosec G101 -- текст ошибки, не секрет
	msgPasswordMismatch = "Пароли не совпадают."                        // #nosec G101 -- текст ошибки, не секрет
	msgInviteOtherEmail = "Это приглашение предназначено для другого email."
	msgAlreadyInClub    = "Аккаунт уже состоит в этом клубе."
	// Записи журнала (ActivityLogger::record): готовый текст на языке клуба (новый клуб — ru).
	msgRegistered = "Регистрация пользователя: :user"
	msgAccepted   = "Принято приглашение: :user, роль :role"

	maxString      = 255 // 'max:255'
	minPassword    = 6   // 'min:6'
	trialDays      = 14  // $plan->trial_days ?: 14
	subjectUser    = `App\Models\User`
	subjectInvite  = `App\Models\ClubInvitation`
	roleOwner      = "owner"
	slugSuffixLen  = 8
	slugFallback   = "club"
	slugAlphabet   = "abcdefghijklmnopqrstuvwxyz0123456789"
	pgUniqueViolat = "23505"
)

var phoneRe = regexp.MustCompile(`^\+7\d{10}$`)

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// passwords — хешер паролей (Argon2id); без него регистрация недоступна.
func (h *handlers) platform() (db.Beginner, error) {
	if h.d.Auth == nil || h.d.PlatformPool == nil || h.d.Passwords == nil {
		return nil, httpx.ErrNotConfigured
	}
	return h.d.PlatformPool, nil
}

// Register — POST /api/auth/register.
func (h *handlers) Register(ctx context.Context, req authapi.RegisterRequestObject) (authapi.RegisterResponseObject, error) {
	pool, err := h.platform()
	if err != nil {
		return nil, err
	}
	w, r := httpx.HTTPFrom(ctx)
	if r != nil && crossSite(r) {
		return authapi.Register403JSONResponse{ForbiddenJSONResponse: apicommon.ForbiddenJSONResponse(httpx.ErrorBody(permissions.CodeForbidden, ""))}, nil
	}
	var body authapi.RegisterRequest
	if req.Body != nil {
		body = *req.Body
	}
	name, clubName, pw := str(body.Name), str(body.ClubName), str(body.Password)
	phone := auth.NormalizePhone(str(body.Phone))

	// Порядок полей и тексты — как $request->validate() в register().
	var fe httpx.FormErrors
	switch {
	case strings.TrimSpace(name) == "":
		fe.Add("name", msgNameRequired)
	case utf8.RuneCountInString(name) > maxString:
		fe.Add("name", httpx.RuleMessage("name", "max_string", "255"))
	}
	switch {
	case strings.TrimSpace(clubName) == "":
		fe.Add("club_name", msgClubNameRequired)
	case utf8.RuneCountInString(clubName) > maxString:
		fe.Add("club_name", httpx.RuleMessage("club_name", "max_string", "255"))
	}
	h.validatePhone(ctx, pool, &fe, phone, auth.MsgPhoneRequired, auth.MsgPhoneInvalid, msgPhoneTaken)
	validatePassword(&fe, pw, str(body.PasswordConfirmation), auth.MsgPasswordRequired, msgPasswordMin, msgPasswordMismatch)
	if err := fe.Err(); err != nil {
		return nil, err
	}

	hash, err := h.d.Passwords.Hash(ctx, pw)
	if err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	var userID, clubID int64
	err = db.InPlatformTx(ctx, pool, func(q *store.Queries) error {
		var err error
		if userID, err = q.RegisterCreateUser(ctx, store.RegisterCreateUserParams{Name: name, Phone: phone, Password: hash, Now: &now}); err != nil {
			return err
		}
		if clubID, err = q.RegisterCreateClub(ctx, store.RegisterCreateClubParams{Name: clubName, Slug: clubSlug(clubName), Now: &now}); err != nil {
			return err
		}
		if err := q.RegisterCreateMembership(ctx, store.RegisterCreateMembershipParams{ClubID: clubID, UserID: userID, Role: roleOwner, Now: &now}); err != nil {
			return err
		}
		if err := q.EnsureMemberBalance(ctx, store.EnsureMemberBalanceParams{ClubID: clubID, UserID: userID}); err != nil {
			return err
		}
		plan, err := q.RegisterTrialPlan(ctx, &now)
		if err != nil {
			return err
		}
		days := int(plan.TrialDays)
		if days == 0 {
			days = trialDays
		}
		ends := now.AddDate(0, 0, days)
		if err := q.RegisterCreateSubscription(ctx, store.RegisterCreateSubscriptionParams{
			ClubID: clubID, SaasPlanID: plan.ID, Now: now, TrialEndsAt: &ends,
		}); err != nil {
			return err
		}
		_, err = activity.Record(ctx, q, activity.Entry{
			ClubID: clubID, Event: "auth.registered", Description: httpx.Substitute(msgRegistered, map[string]string{"user": name}),
			UserID: userID, SubjectType: subjectUser, SubjectID: userID, At: now,
		})
		return err
	})
	if isUnique(err) { // гонка двух регистраций одного номера: unique:users
		return nil, httpx.Validation("phone", msgPhoneTaken)
	}
	if err != nil {
		return nil, err
	}
	body2, err := h.startSession(ctx, w, userID, clubID)
	if err != nil {
		return nil, err
	}
	return authapi.Register201JSONResponse(body2), nil
}

// validatePhone — 'phone' => ['required', 'regex:/^\+7\d{10}$/', 'unique:users'].
func (h *handlers) validatePhone(ctx context.Context, pool db.Beginner, fe *httpx.FormErrors, phone, required, invalid, taken string) {
	switch {
	case phone == "":
		fe.Add("phone", required)
	case !phoneRe.MatchString(phone):
		fe.Add("phone", invalid)
	default:
		var exists bool
		err := db.InPlatformTx(ctx, pool, func(q *store.Queries) error {
			var err error
			exists, err = q.RegisterPhoneTaken(ctx, phone)
			return err
		})
		if err == nil && exists {
			fe.Add("phone", taken)
		}
	}
}

// validatePassword — 'password' => ['required', 'string', 'min:6', 'confirmed'].
func validatePassword(fe *httpx.FormErrors, pw, confirm, required, minMsg, mismatch string) {
	switch {
	case pw == "":
		fe.Add("password", required)
		return
	case utf8.RuneCountInString(pw) < minPassword:
		fe.Add("password", minMsg)
	}
	if pw != confirm {
		fe.Add("password", mismatch)
	}
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == pgUniqueViolat
}

// startSession — вход нового пользователя (Auth::login + session current_club_id): cookie и AuthSession.
func (h *handlers) startSession(ctx context.Context, w http.ResponseWriter, userID, clubID int64) (authapi.AuthSession, error) {
	t, err := h.d.Auth.Sessions().Create(ctx, userID, clubID, false)
	if err != nil {
		return authapi.AuthSession{}, err
	}
	p, err := h.d.Auth.Authenticate(ctx, t.Access)
	if err != nil {
		return authapi.AuthSession{}, err
	}
	out, err := h.authSession(ctx, t, p)
	if err != nil {
		return out, err
	}
	setSessionCookies(w, h.d.Auth.Sessions(), t)
	return out, nil
}

// clubSlug — Str::slug($club_name) ?: 'club', плюс '-' и 8 случайных символов (Str::lower(Str::random(8))).
// Транслитерация кириллицы — упрощённая (slug внутренний и уникален за счёт случайного хвоста).
func clubSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if t, ok := translit[r]; ok {
			b.WriteString(t)
			dash = false
			continue
		}
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = slugFallback
	}
	suffix := make([]byte, slugSuffixLen)
	raw := make([]byte, slugSuffixLen)
	_, _ = rand.Read(raw)
	for i, c := range raw {
		suffix[i] = slugAlphabet[int(c)%len(slugAlphabet)]
	}
	return slug + "-" + string(suffix)
}

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "zh", 'з': "z", 'и': "i", 'й': "y",
	'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f",
	'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'ә': "a", 'ғ': "g", 'қ': "q", 'ң': "n", 'ө': "o", 'ұ': "u", 'ү': "u", 'һ': "h", 'і': "i",
}

// ---------------------------------------------------------------------------
// Приглашения
// ---------------------------------------------------------------------------

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (h *handlers) findInvitation(ctx context.Context, pool db.Beginner, token string) (store.InvitationByTokenHashRow, error) {
	var inv store.InvitationByTokenHashRow
	err := db.InPlatformTx(ctx, pool, func(q *store.Queries) error {
		var err error
		inv, err = q.InvitationByTokenHash(ctx, store.InvitationByTokenHashParams{TokenHash: tokenHash(token), Now: h.d.NowFunc()})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return inv, httpx.NotFound()
	}
	return inv, err
}

func memberOf(ctx context.Context, q *store.Queries, clubID, userID int64) (bool, error) {
	return q.InvitationMembershipExists(ctx, store.InvitationMembershipExistsParams{ClubID: clubID, UserID: userID})
}

// GetInvitation — GET /api/invitations/{token}.
func (h *handlers) GetInvitation(ctx context.Context, req authapi.GetInvitationRequestObject) (authapi.GetInvitationResponseObject, error) {
	pool, err := h.platform()
	if err != nil {
		return nil, err
	}
	inv, err := h.findInvitation(ctx, pool, req.Token)
	if err != nil {
		return nil, err
	}
	out := authapi.Invitation{ClubName: inv.ClubName, Role: inv.Role, Email: inv.Email}
	if p, ok := principal.FromContext(ctx); ok {
		err := db.InPlatformTx(ctx, pool, func(q *store.Queries) error {
			var err error
			out.AlreadyMember, err = memberOf(ctx, q, inv.ClubID, p.UserID)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return authapi.GetInvitation200JSONResponse(out), nil
}

// AcceptInvitation — POST /api/invitations/{token}/accept.
func (h *handlers) AcceptInvitation(ctx context.Context, req authapi.AcceptInvitationRequestObject) (authapi.AcceptInvitationResponseObject, error) {
	pool, err := h.platform()
	if err != nil {
		return nil, err
	}
	w, r := httpx.HTTPFrom(ctx)
	if r != nil && crossSite(r) {
		return nil, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, "")
	}
	inv, err := h.findInvitation(ctx, pool, req.Token)
	if err != nil {
		return nil, err
	}
	p, authed := principal.FromContext(ctx)
	var body authapi.AcceptInvitationRequest
	if req.Body != nil {
		body = *req.Body
	}
	var userName, hash, phone string
	if authed {
		var email *string
		err := db.InPlatformTx(ctx, pool, func(q *store.Queries) error {
			var err error
			email, err = q.InvitationUserEmail(ctx, p.UserID)
			return err
		})
		if err != nil {
			return nil, err
		}
		if inv.Email != nil && (email == nil || *email != *inv.Email) {
			return nil, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, msgInviteOtherEmail)
		}
		me, err := h.d.Auth.Me(ctx, p)
		if err != nil {
			return nil, err
		}
		userName = me.Name
	} else {
		// Без своих текстов — стандартные сообщения валидатора (accept() не задаёт messages).
		userName, phone = str(body.Name), auth.NormalizePhone(str(body.Phone))
		if phone == "" {
			phone = "+7" // $request->merge(['phone' => '+7'.$digits]) — пустой номер даёт ошибку regex
		}
		pw := str(body.Password)
		var fe httpx.FormErrors
		switch {
		case strings.TrimSpace(userName) == "":
			fe.Add("name", httpx.RuleMessage("name", "required", ""))
		case utf8.RuneCountInString(userName) > maxString:
			fe.Add("name", httpx.RuleMessage("name", "max_string", "255"))
		}
		h.validatePhone(ctx, pool, &fe, phone, httpx.RuleMessage("phone", "required", ""),
			httpx.RuleMessage("phone", "regex", ""), httpx.RuleMessage("phone", "unique", ""))
		validatePassword(&fe, pw, str(body.PasswordConfirmation), httpx.RuleMessage("password", "required", ""),
			httpx.RuleMessage("password", "min_string", "6"), httpx.RuleMessage("password", "confirmed", ""))
		if err := fe.Err(); err != nil {
			return nil, err
		}
		if hash, err = h.d.Passwords.Hash(ctx, pw); err != nil {
			return nil, err
		}
	}

	now := h.d.NowFunc()
	userID := p.UserID
	err = db.InPlatformTx(ctx, pool, func(q *store.Queries) error {
		lock, err := q.LockInvitation(ctx, store.LockInvitationParams{ID: inv.ID, ClubID: inv.ClubID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if lock.AcceptedAt != nil || !lock.ExpiresAt.After(now) {
			return httpx.NewAPIError(http.StatusGone, "gone", "")
		}
		if !authed {
			if userID, err = q.RegisterCreateUser(ctx, store.RegisterCreateUserParams{
				Name: userName, Phone: phone, Email: inv.Email, Password: hash, Now: &now,
			}); err != nil {
				return err
			}
		}
		exists, err := memberOf(ctx, q, inv.ClubID, userID)
		if err != nil {
			return err
		}
		if exists {
			return httpx.NewAPIError(http.StatusConflict, "already_member", msgAlreadyInClub)
		}
		if err := q.RegisterCreateMembership(ctx, store.RegisterCreateMembershipParams{ClubID: inv.ClubID, UserID: userID, Role: inv.Role, Now: &now}); err != nil {
			return err
		}
		if err := q.EnsureMemberBalance(ctx, store.EnsureMemberBalanceParams{ClubID: inv.ClubID, UserID: userID}); err != nil {
			return err
		}
		if err := q.InvitationMarkAccepted(ctx, store.InvitationMarkAcceptedParams{ID: inv.ID, ClubID: inv.ClubID, Now: &now}); err != nil {
			return err
		}
		_, err = activity.Record(ctx, q, activity.Entry{
			ClubID: inv.ClubID, Event: "invitation.accepted",
			Description: httpx.Substitute(msgAccepted, map[string]string{"user": userName, "role": inv.Role}),
			UserID:      userID, SubjectType: subjectInvite, SubjectID: inv.ID, At: now,
			Details: map[string]any{"role": inv.Role, "new_account": !authed},
		})
		return err
	})
	if isUnique(err) && !authed {
		return nil, httpx.Validation("phone", httpx.RuleMessage("phone", "unique", ""))
	}
	if err != nil {
		return nil, err
	}
	if !authed {
		out, err := h.startSession(ctx, w, userID, inv.ClubID)
		if err != nil {
			return nil, err
		}
		return authapi.AcceptInvitation201JSONResponse(out), nil
	}
	// $request->session()->put('current_club_id', $invitation->club_id).
	np, err := h.d.Auth.SelectClub(ctx, p, inv.ClubID)
	if err != nil {
		return nil, err
	}
	me, err := h.d.Auth.Me(ctx, np)
	if err != nil {
		return nil, err
	}
	out, err := h.meResponse(ctx, me)
	if err != nil {
		return nil, err
	}
	return authapi.AcceptInvitation200JSONResponse(out), nil
}

// ---------------------------------------------------------------------------
// WebSocket
// ---------------------------------------------------------------------------

// IssueWsTicket — POST /api/ws-ticket: одноразовый билет для /ws (EV-3).
func (h *handlers) IssueWsTicket(ctx context.Context, _ authapi.IssueWsTicketRequestObject) (authapi.IssueWsTicketResponseObject, error) {
	if h.d.WSTickets == nil {
		return nil, httpx.ErrNotConfigured
	}
	p, ok := principal.FromContext(ctx)
	if !ok {
		return nil, httpx.NewAPIError(http.StatusUnauthorized, permissions.CodeUnauthenticated, permissions.MsgUnauthenticated)
	}
	if status, code, msg, ok := permissions.CheckClub(p, http.MethodGet); !ok {
		return nil, httpx.NewAPIError(status, code, msg)
	}
	_, r := httpx.HTTPFrom(ctx)
	ticket, err := h.d.WSTickets.Issue(ctx, auth.BearerToken(r))
	if err != nil {
		return nil, err
	}
	return authapi.IssueWsTicket200JSONResponse{Ticket: ticket, ExpiresIn: int(h.d.WSTickets.TTL() / time.Second)}, nil
}
