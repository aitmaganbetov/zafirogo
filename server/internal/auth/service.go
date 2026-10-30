package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты ошибок входа — как в старом (AuthController::login, SetCurrentClub). Русский текст = ключ перевода.
const (
	MsgPhoneRequired      = "Номер телефона обязателен."
	MsgPhoneInvalid       = "Введите корректный номер телефона."
	MsgPasswordRequired   = "Пароль обязателен."       // #nosec G101 -- текст/код ошибки, не секрет
	MsgInvalidCredentials = "Неверные учетные данные." // #nosec G101 -- текст/код ошибки, не секрет
	MsgBlocked            = "Аккаунт заблокирован. Обратитесь в поддержку Zafiro."
	// MsgBlockedAPI — заблокированный пользователь с живой сессией (SetCurrentClub, ветка api/*).
	MsgBlockedAPI  = "Аккаунт заблокирован."
	MsgNoClubs     = permissions.MsgNoClubs
	MsgStaffOnly   = "Панель клуба — только для сотрудников. Клиенты входят на ПК клуба в агенте."
	MsgShiftFull   = "На смене уже максимум сотрудников с вашей ролью. Войти можно, когда коллега выйдет из панели."
	MsgClubMissing = "Нет доступа к выбранному клубу."
)

// Причины неудачного входа для журнала (ActivityLogger::recordFailedLogin('auth.login_failed', phone, reason)).
const (
	ReasonInvalidCredentials = "invalid_credentials" // #nosec G101 -- текст/код ошибки, не секрет
	ReasonBlocked            = "blocked"
	ReasonNoClubAccess       = "no_club_access"
	ReasonShiftFull          = "shift_full"
)

// FieldError — ошибка входа, привязанная к полю формы (Laravel: back()->withErrors([поле => текст])).
type FieldError struct {
	Field   string
	Message string
	// Reason — причина для журнала; пусто — неудача в журнал не пишется (как в старом).
	Reason string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// Ошибки сервиса.
var (
	// ErrUnauthenticated — нет действующей сессии.
	ErrUnauthenticated = errors.New("auth: unauthenticated")
	// ErrBlocked — пользователь заблокирован (сессии отозваны).
	ErrBlocked = errors.New("auth: account blocked")
	// ErrNotFound — клуба или членства нет (Laravel: firstOrFail / findOrFail → 404).
	ErrNotFound = errors.New("auth: not found")
	// ErrReadOnly — режим просмотра суперадмином: изменения запрещены.
	ErrReadOnly = errors.New("auth: read-only club view")
	// ErrForbidden — нет прав.
	ErrForbidden = errors.New("auth: forbidden")
)

// Events — события для журналов (ActivityLogger, PlatformLogger). Реализует пакет журналов (волна A, A4);
// по умолчанию — ничего. Пароли, токены и коды сюда не попадают.
type Events interface {
	// LoginSucceeded — auth.login «Вход в систему: :user» (журнал клуба clubID; 0 — вход суперадмина без клуба,
	// в старом — PlatformLogger superadmin.login).
	LoginSucceeded(ctx context.Context, userID, clubID int64, userName string)
	// LoginFailed — auth.login_failed с нормализованным телефоном и причиной.
	LoginFailed(ctx context.Context, phone, reason string)
	// LoggedOut — auth.logout «Выход из системы: :user».
	LoggedOut(ctx context.Context, p principal.Principal)
	// ClubSwitched — club.switched «Переход в клуб из клуба #:id» (журнал нового клуба).
	ClubSwitched(ctx context.Context, userID, fromClubID, toClubID int64)
	// ClubViewed — superadmin.club_viewed (журнал платформы).
	ClubViewed(ctx context.Context, userID, clubID int64)
}

// NopEvents — Events без действий.
type NopEvents struct{}

// LoginSucceeded ничего не делает.
func (NopEvents) LoginSucceeded(context.Context, int64, int64, string) {}

// LoginFailed ничего не делает.
func (NopEvents) LoginFailed(context.Context, string, string) {}

// LoggedOut ничего не делает.
func (NopEvents) LoggedOut(context.Context, principal.Principal) {}

// ClubSwitched ничего не делает.
func (NopEvents) ClubSwitched(context.Context, int64, int64, int64) {}

// ClubViewed ничего не делает.
func (NopEvents) ClubViewed(context.Context, int64, int64) {}

// Options — зависимости Service.
type Options struct {
	// Pool — пул приложения (роль zafiro_app).
	Pool     *pgxpool.Pool
	Hasher   *password.Hasher
	Sessions *session.Manager
	Events   Events
	Logger   *slog.Logger
	// Now — часы (тесты); по умолчанию time.Now. Должны совпадать с часами session.Manager.
	Now func() time.Time
}

// Service — вход, текущий пользователь, выбор клуба, режим просмотра суперадмина.
type Service struct {
	pool     *pgxpool.Pool
	hasher   *password.Hasher
	sessions *session.Manager
	events   Events
	log      *slog.Logger
	now      func() time.Time
}

// NewService создаёт Service.
func NewService(o Options) (*Service, error) {
	if o.Pool == nil || o.Hasher == nil || o.Sessions == nil {
		return nil, errors.New("auth: pool, hasher and sessions are required")
	}
	if o.Events == nil {
		o.Events = NopEvents{}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{pool: o.Pool, hasher: o.Hasher, sessions: o.Sessions, events: o.Events, log: o.Logger, now: o.Now}, nil
}

// Sessions — менеджер сессий (для HTTP-слоя: cookie, CSRF).
func (s *Service) Sessions() *session.Manager { return s.sessions }

var phoneRe = regexp.MustCompile(`^\+7\d{10}$`)

// NormalizePhone — Laravel: AuthController::normalizePhone(): цифры; 11 цифр с ведущей 7 → без неё; "+7" + цифры.
// Пусто — "".
func NormalizePhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if d == "" {
		return ""
	}
	if len(d) == 11 && d[0] == '7' {
		d = d[1:]
	}
	return "+7" + d
}

// LoginInput — форма входа.
type LoginInput struct {
	Phone    string
	Password string
	Remember bool
	// PCLogin — вход для подтверждения входа на ПК по QR (/pc-login/{code}): клиентам разрешён
	// (в старом — url.intended начинается с /pc-login/).
	PCLogin bool
}

// LoginResult — итог входа.
type LoginResult struct {
	Tokens    session.Tokens
	UserID    int64
	ClubID    int64
	Principal principal.Principal
}

// Login — вход в панель по телефону и паролю. Laravel: AuthController::login().
func (s *Service) Login(ctx context.Context, in LoginInput) (LoginResult, error) {
	phone := NormalizePhone(in.Phone)
	switch {
	case phone == "":
		return LoginResult{}, &FieldError{Field: "phone", Message: MsgPhoneRequired}
	case !phoneRe.MatchString(phone):
		return LoginResult{}, &FieldError{Field: "phone", Message: MsgPhoneInvalid}
	case in.Password == "":
		return LoginResult{}, &FieldError{Field: "password", Message: MsgPasswordRequired}
	}
	fail := func(reason, msg string) (LoginResult, error) {
		if reason != "" {
			s.events.LoginFailed(ctx, phone, reason)
		}
		return LoginResult{}, &FieldError{Field: "phone", Message: msg, Reason: reason}
	}

	q := store.New(s.pool)
	user, err := q.GetUserByPhone(ctx, phone)
	if errors.Is(err, pgx.ErrNoRows) {
		// Та же работа, что и для существующего номера: время ответа не выдаёт, зарегистрирован ли он.
		if err := s.hasher.VerifyDummy(ctx, in.Password); err != nil && !errors.Is(err, password.ErrMismatch) {
			return LoginResult{}, err
		}
		return fail(ReasonInvalidCredentials, MsgInvalidCredentials)
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("auth: find user: %w", err)
	}
	rehash, err := s.hasher.Verify(ctx, in.Password, user.Password)
	if err != nil {
		if !errors.Is(err, password.ErrMismatch) && !errors.Is(err, password.ErrUnknownFormat) && !errors.Is(err, password.ErrMalformed) {
			return LoginResult{}, err
		}
		if !errors.Is(err, password.ErrMismatch) {
			s.log.WarnContext(ctx, "unusable password hash", slog.Int64("user_id", user.ID))
		}
		return fail(ReasonInvalidCredentials, MsgInvalidCredentials)
	}
	// Laravel 11: Auth::attempt перехэширует пароль при успешном входе (hashing.rehash_on_login).
	if rehash {
		s.rehash(ctx, q, user.ID, in.Password, user.Password)
	}
	if user.BlockedAt != nil {
		return fail(ReasonBlocked, MsgBlocked)
	}

	// A staff membership first: a cashier who is also a customer of another club works.
	var memberships []store.ListUserMembershipsRow
	if err := userTx(ctx, s.pool, user.ID, func(q *store.Queries) error {
		var err error
		memberships, err = q.ListUserMemberships(ctx, user.ID)
		return err
	}); err != nil {
		return LoginResult{}, fmt.Errorf("auth: memberships: %w", err)
	}

	var clubID int64
	switch {
	case len(memberships) == 0 && user.IsSuperadmin:
		// Суперадмин без своего клуба работает из кабинета. Отдельный домен кабинета (SuperadminHost) — Q-069.
	case len(memberships) == 0:
		return fail(ReasonNoClubAccess, MsgNoClubs)
	default:
		m := memberships[0]
		// Customers sign in on the site only to confirm a sign-in on a PC (QR code). Без записи в журнал.
		if !permissions.IsStaff(m.Role) && !in.PCLogin {
			return fail("", MsgStaffOnly)
		}
		ok, err := s.hasShiftPlace(ctx, m.ClubID, m.Role, user.ID)
		if err != nil {
			return LoginResult{}, err
		}
		if !ok {
			return fail(ReasonShiftFull, MsgShiftFull)
		}
		clubID = m.ClubID
	}

	tokens, err := s.sessions.Create(ctx, user.ID, clubID, in.Remember)
	if err != nil {
		return LoginResult{}, err
	}
	p, err := s.principalFor(ctx, session.Session{ID: tokens.SessionID, UserID: user.ID, ClubID: clubID}, user.IsSuperadmin)
	if err != nil {
		return LoginResult{}, err
	}
	s.events.LoginSucceeded(ctx, user.ID, clubID, user.Name)
	return LoginResult{Tokens: tokens, UserID: user.ID, ClubID: clubID, Principal: p}, nil
}

// rehash заменяет хэш на Argon2id. Ошибка не мешает входу (пароль уже проверен) — только в лог.
func (s *Service) rehash(ctx context.Context, q *store.Queries, userID int64, pw, old string) {
	h, err := s.hasher.Hash(ctx, pw)
	if err == nil {
		_, err = q.UpdateUserPasswordHash(ctx, store.UpdateUserPasswordHashParams{ID: userID, NewHash: h, OldHash: old})
	}
	if err != nil {
		s.log.WarnContext(ctx, "password rehash failed", slog.Int64("user_id", userID), slog.String("err", err.Error()))
	}
}

// hasShiftPlace — Laravel: StaffShifts::hasPlace() (BR-060): роль из SHIFT, лимит из настроек клуба,
// «на смене» — члены клуба с этой ролью (кроме входящего) с активной сессией за IdleTTL.
func (s *Service) hasShiftPlace(ctx context.Context, clubID int64, role string, userID int64) (bool, error) {
	if !permissions.IsShiftRole(role) {
		return true, nil
	}
	club, err := store.New(s.pool).GetClub(ctx, clubID)
	if err != nil {
		return false, fmt.Errorf("auth: club settings: %w", err)
	}
	limit := cash.ShiftLimit(clubSetting(club.Settings, "club", cash.ShiftLimitSetting(role)))
	if limit == nil {
		return true, nil
	}
	var others []int64
	if err := db.InTenantTx(ctx, s.pool, clubID, func(q *store.Queries) error {
		var err error
		others, err = q.ListClubUserIDsByRole(ctx, store.ListClubUserIDsByRoleParams{ClubID: clubID, Role: role, ExceptUserID: userID})
		return err
	}); err != nil {
		return false, fmt.Errorf("auth: shift members: %w", err)
	}
	active, err := s.sessions.ActiveUsers(ctx, others, s.now().Add(-s.sessions.IdleTTL()))
	if err != nil {
		return false, err
	}
	return cash.HasShiftPlace(role, limit, len(active)), nil
}

// clubSetting — $settings[section][key] ?? nil; числа — json.Number (как есть в JSON).
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

// Authenticate — Principal по access-токену: сессия в Valkey, пользователь и членство в Postgres
// (блокировка и изменения прав действуют сразу, как в старом, где членство читалось на каждом запросе).
func (s *Service) Authenticate(ctx context.Context, accessToken string) (principal.Principal, error) {
	sess, err := s.sessions.Authenticate(ctx, accessToken)
	if err != nil {
		if errors.Is(err, session.ErrInvalidToken) || errors.Is(err, session.ErrNotFound) {
			return principal.Principal{}, ErrUnauthenticated
		}
		return principal.Principal{}, err
	}
	user, err := store.New(s.pool).GetUserAuth(ctx, sess.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = s.sessions.RevokeUser(ctx, sess.UserID)
		return principal.Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return principal.Principal{}, fmt.Errorf("auth: user: %w", err)
	}
	// A blocked account loses access at once, even with a live session (SetCurrentClub).
	if user.BlockedAt != nil {
		if err := s.sessions.RevokeUser(ctx, user.ID); err != nil {
			s.log.WarnContext(ctx, "revoke blocked user sessions", slog.String("err", err.Error()))
		}
		return principal.Principal{}, ErrBlocked
	}
	return s.principalFor(ctx, sess, user.IsSuperadmin)
}

// principalFor собирает Principal сессии.
func (s *Service) principalFor(ctx context.Context, sess session.Session, superadmin bool) (principal.Principal, error) {
	p := principal.Principal{UserID: sess.UserID, Superadmin: superadmin, SessionID: sess.ID, Permissions: []string{}}
	// Superadmin viewing a club: its panel as the owner sees it, but read-only (SetCurrentClub::viewAsSuperadmin).
	if superadmin && sess.ViewingClubID > 0 {
		_, err := store.New(s.pool).GetClub(ctx, sess.ViewingClubID)
		switch {
		case err == nil:
			p.ClubID, p.Role, p.Permissions, p.ReadOnly = sess.ViewingClubID, permissions.RoleOwner, permissions.All(), true
			return p, nil
		case errors.Is(err, pgx.ErrNoRows):
			// Клуба нет — режим просмотра снимается, дальше как обычно.
			if err := s.sessions.SetViewing(ctx, sess.ID, 0); err != nil && !errors.Is(err, session.ErrNotFound) {
				return p, err
			}
		default:
			return p, fmt.Errorf("auth: viewing club: %w", err)
		}
	}
	if sess.ClubID <= 0 {
		return p, nil
	}
	var m store.GetMembershipAuthRow
	err := db.InTenantTxWith(ctx, s.pool, sess.ClubID, db.Options{UserID: sess.UserID}, func(q *store.Queries) error {
		var err error
		m, err = q.GetMembershipAuth(ctx, store.GetMembershipAuthParams{ClubID: sess.ClubID, UserID: sess.UserID})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Членства больше нет: клуб не выбран (RequireClub ответит 403), выбрать можно другой.
		return p, nil
	}
	if err != nil {
		return p, fmt.Errorf("auth: membership: %w", err)
	}
	mem := &permissions.Membership{Role: m.Role, Given: permissions.ParseGiven(m.Permissions)}
	p.ClubID, p.Role, p.Permissions = sess.ClubID, m.Role, permissions.For(mem)
	return p, nil
}

// Membership — клуб пользователя для выбора.
type Membership struct {
	ClubID   int64
	ClubName string
	Role     string
}

// Me — текущий пользователь и его клубы.
type Me struct {
	UserID     int64
	Name       string
	Phone      string
	Superadmin bool
	Principal  principal.Principal
	ClubName   string
	// Clubs — клубы для переключения (availableClubs); в режиме просмотра — пусто.
	Clubs []Membership
}

// Me — Laravel: View::share currentClub/currentMembership/availableClubs (SetCurrentClub).
func (s *Service) Me(ctx context.Context, p principal.Principal) (Me, error) {
	user, err := store.New(s.pool).GetUserAuth(ctx, p.UserID)
	if err != nil {
		return Me{}, fmt.Errorf("auth: me: %w", err)
	}
	me := Me{UserID: user.ID, Name: user.Name, Phone: user.Phone, Superadmin: user.IsSuperadmin, Principal: p, Clubs: []Membership{}}
	if p.ClubID > 0 {
		club, err := store.New(s.pool).GetClub(ctx, p.ClubID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Me{}, fmt.Errorf("auth: me club: %w", err)
		}
		me.ClubName = club.Name
	}
	if p.ReadOnly {
		return me, nil
	}
	if err := userTx(ctx, s.pool, p.UserID, func(q *store.Queries) error {
		rows, err := q.ListUserMemberships(ctx, p.UserID)
		for _, r := range rows {
			me.Clubs = append(me.Clubs, Membership{ClubID: r.ClubID, ClubName: r.ClubName, Role: r.Role})
		}
		return err
	}); err != nil {
		return Me{}, fmt.Errorf("auth: me clubs: %w", err)
	}
	return me, nil
}

// SelectClub — переход в другой клуб пользователя. Laravel: ClubController::switch() — членство в клубе
// (любая роль) или 404; журнал club.switched, если клуб другой. В режиме просмотра — запрещено (не GET).
func (s *Service) SelectClub(ctx context.Context, p principal.Principal, clubID int64) (principal.Principal, error) {
	if p.ReadOnly {
		return p, ErrReadOnly
	}
	var found bool
	if err := userTx(ctx, s.pool, p.UserID, func(q *store.Queries) error {
		rows, err := q.ListUserMemberships(ctx, p.UserID)
		for _, r := range rows {
			found = found || r.ClubID == clubID
		}
		return err
	}); err != nil {
		return p, fmt.Errorf("auth: select club: %w", err)
	}
	if !found {
		return p, ErrNotFound
	}
	if err := s.sessions.SetClub(ctx, p.SessionID, clubID); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return p, ErrUnauthenticated
		}
		return p, err
	}
	np, err := s.principalFor(ctx, session.Session{ID: p.SessionID, UserID: p.UserID, ClubID: clubID}, p.Superadmin)
	if err != nil {
		return p, err
	}
	if clubID != p.ClubID {
		s.events.ClubSwitched(ctx, p.UserID, p.ClubID, clubID)
	}
	return np, nil
}

// ViewClub — суперадмин открывает клуб только для чтения. Laravel: Superadmin\ClubController::view().
func (s *Service) ViewClub(ctx context.Context, p principal.Principal, clubID int64) (principal.Principal, error) {
	if !p.Superadmin {
		return p, ErrForbidden
	}
	if _, err := store.New(s.pool).GetClub(ctx, clubID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, ErrNotFound
		}
		return p, fmt.Errorf("auth: view club: %w", err)
	}
	if err := s.sessions.SetViewing(ctx, p.SessionID, clubID); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return p, ErrUnauthenticated
		}
		return p, err
	}
	s.events.ClubViewed(ctx, p.UserID, clubID)
	return principal.Principal{UserID: p.UserID, ClubID: clubID, Role: permissions.RoleOwner, Permissions: permissions.All(),
		Superadmin: true, ReadOnly: true, SessionID: p.SessionID}, nil
}

// LeaveClub — выход из режима просмотра. Laravel: Superadmin\ClubController::leave(). Возвращает клуб,
// который смотрели (0 — не смотрели).
func (s *Service) LeaveClub(ctx context.Context, p principal.Principal) (int64, error) {
	if !p.Superadmin {
		return 0, ErrForbidden
	}
	was := int64(0)
	if p.ReadOnly {
		was = p.ClubID
	}
	if err := s.sessions.SetViewing(ctx, p.SessionID, 0); err != nil && !errors.Is(err, session.ErrNotFound) {
		return 0, err
	}
	return was, nil
}

// Logout — выход: сессия отзывается (Laravel: AuthController::logout, журнал auth.logout).
func (s *Service) Logout(ctx context.Context, p principal.Principal) error {
	if err := s.sessions.Revoke(ctx, p.SessionID); err != nil {
		return err
	}
	if !p.ReadOnly {
		s.events.LoggedOut(ctx, p)
	}
	return nil
}

// LogoutAll — выход на всех устройствах.
func (s *Service) LogoutAll(ctx context.Context, p principal.Principal) error {
	if err := s.sessions.RevokeUser(ctx, p.UserID); err != nil {
		return err
	}
	if !p.ReadOnly {
		s.events.LoggedOut(ctx, p)
	}
	return nil
}

// userTx — транзакция с app.user_id без клуба: свои членства во всех клубах (политика member_self_read, 0004_rls).
func userTx(ctx context.Context, pool db.Beginner, userID int64, fn func(q *store.Queries) error) error {
	if userID <= 0 {
		return errors.New("auth: user id is required")
	}
	return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', $1, true)", strconv.FormatInt(userID, 10)); err != nil {
			return err
		}
		return fn(store.New(tx))
	})
}
