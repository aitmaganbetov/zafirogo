package guestapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// staffRoles — роли, которых не касается «один ПК на клиента»: ClubComputerSessions::STAFF_ROLES + cashier
// (AgentSignIns::elsewhere()).
var staffRoles = map[string]bool{"owner": true, "admin": true, "manager": true, "operator": true, "cashier": true}

// pcStaffRoles — ClubComputerSessions::STAFF_ROLES (сотрудник на ПК, PIN выхода).
var pcStaffRoles = map[string]bool{"owner": true, "admin": true, "manager": true, "operator": true}

// Тексты регистрации (AgentRegisterController).
const (
	msgPhoneDigits     = "Введите 10 цифр номера после +7."
	msgPasswordMin     = "Пароль — не короче 6 символов."    // #nosec G101 -- текст для пользователя, не секрет
	msgPasswordMax     = "Пароль — не длиннее 255 символов." // #nosec G101 -- текст для пользователя, не секрет
	msgNameLength      = "Имя — от 2 до 255 символов."
	msgAlreadyMember   = "Вы уже клиент этого клуба. Войдите со своим паролем."
	msgJoinWrongPass   = "Этот номер уже есть в Zafiro (вы клиент другого клуба). Введите пароль от своего аккаунта или восстановите его: «Забыли пароль?»." // #nosec G101 -- текст для пользователя, не секрет
	msgQRExpired       = "QR-код устарел. Обновите его на ПК и отсканируйте снова."                                                                          // QrLogins::approve()
	msgQRNotMember     = "Ваш аккаунт не состоит в этом клубе."
	msgPasswordMissing = "Введите пароль." // #nosec G101 -- текст для пользователя, не секрет
	msgPhoneMissing    = "Введите номер телефона."
)

// qrLifetime — AgentQrLogin::LIFETIME_MINUTES.
const qrLifetime = 5 * time.Minute

// maxQRWatchers — не более 2 одновременных ожиданий QR на ПК (guest.proto WatchQrLogin).
const maxQRWatchers = 2

var loginPhoneRe = regexp.MustCompile(`^\+?[1-9]\d{1,14}$`) // LoginRequest: regex:/^\+?[1-9]\d{1,14}$/
var registerPhoneRe = regexp.MustCompile(`^\+7\d{10}$`)

// NormalizePhone — PhoneNumber::normalize() (app/Support/PhoneNumber.php): "+7 700 123-45-67", "8 700…",
// "700…" → "+77001234567"; другое — цифры после +7 (не пройдёт проверку).
func NormalizePhone(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if d == "" {
		return ""
	}
	if len(d) == 11 && (d[0] == '7' || d[0] == '8') {
		d = d[1:]
	}
	return "+7" + d
}

// MaskPhone — Friends::maskPhone(): "+7 701 ••• •• 12"; не казахстанский формат — "•••".
func MaskPhone(phone string) string {
	if !registerPhoneRe.MatchString(phone) {
		if phone == "" {
			return ""
		}
		return "•••"
	}
	return "+7 " + phone[2:5] + " ••• •• " + phone[10:12]
}

type authService struct {
	agentv1.UnimplementedGuestAuthServiceServer
	s *Server
}

// Login — вход по телефону и паролю (POST /api/agent/user/login, AuthController::issueUserLoginResponse()).
func (a *authService) Login(ctx context.Context, req *agentv1.LoginRequest) (*agentv1.LoginResponse, error) {
	s := a.s
	ctx, id, err := s.call(ctx, ratelimit.CustomerLogin, func(sub *ratelimit.Subject) { sub.Phone = req.GetPhone() })
	if err != nil {
		return nil, err
	}
	phone := NormalizePhone(req.GetPhone())
	switch {
	case req.GetPhone() == "":
		return nil, validation("phone", msgPhoneMissing)
	case req.GetPassword() == "":
		return nil, validation("password", msgPasswordMissing)
	case len(req.GetPassword()) > 256 || !loginPhoneRe.MatchString(phone):
		return nil, invalidCredentials()
	}
	var user store.GetUserByPhoneRow
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		var err error
		user, err = q.GetUserByPhone(ctx, phone)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		_ = s.o.Passwords.VerifyDummy(ctx, req.GetPassword()) // время ответа не выдаёт, есть ли номер
		return nil, invalidCredentials()
	}
	if err != nil {
		return nil, s.fail(ctx, "Login", err)
	}
	if err := s.checkPassword(ctx, id, user.ID, user.Password, req.GetPassword()); err != nil {
		return nil, err
	}
	if user.BlockedAt != nil {
		return nil, accountBlocked()
	}
	signIn, err := s.signIn(ctx, id, user.ID, "auth.api_login", "Вход через API: "+user.Name)
	if err != nil {
		return nil, err
	}
	return &agentv1.LoginResponse{SignIn: signIn}, nil
}

func invalidCredentials() error {
	return rpcError(codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_INVALID_CREDENTIALS, msgInvalidCredential, nil, "")
}

func accountBlocked() error {
	return rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_ACCOUNT_BLOCKED, msgAccountBlocked, nil, "")
}

// checkPassword — Hash::check + перехеш bcrypt → Argon2id (A1: password.Hasher). Неверный — INVALID_CREDENTIALS.
func (s *Server) checkPassword(ctx context.Context, id identity.Identity, userID int64, hash, plain string) error {
	needsRehash, err := s.o.Passwords.Verify(ctx, plain, hash)
	if err != nil {
		if errors.Is(err, password.ErrMismatch) || errors.Is(err, password.ErrUnknownFormat) || errors.Is(err, password.ErrMalformed) {
			return invalidCredentials()
		}
		return s.fail(ctx, "checkPassword", err)
	}
	if needsRehash {
		s.rehash(ctx, id, userID, hash, plain)
	}
	return nil
}

func (s *Server) rehash(ctx context.Context, id identity.Identity, userID int64, oldHash, plain string) {
	newHash, err := s.o.Passwords.Hash(ctx, plain)
	if err == nil {
		err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
			_, err := q.UpdateUserPasswordHash(ctx, store.UpdateUserPasswordHashParams{NewHash: newHash, ID: userID, OldHash: oldHash})
			return err
		})
	}
	if err != nil {
		s.o.Log.WarnContext(ctx, "guest password rehash failed", "user_id", userID, "err", err)
	}
}

// signIn — проверки входа в клуб ПК и выдача токена (общие для Login и QR):
// членство (not_club_member) → подписка (402) → «один ПК на клиента» (signed_in_elsewhere) → токен этого ПК
// (прежний токен ПК отзывается) → журнал.
func (s *Server) signIn(ctx context.Context, id identity.Identity, userID int64, event, description string) (*agentv1.GuestSignIn, error) {
	now := s.o.Now()
	var role, computerName string
	err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		var err error
		role, err = q.GuestMembershipRole(ctx, store.GuestMembershipRoleParams{ClubID: id.ClubID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_NOT_CLUB_MEMBER, msgNotClubMember, nil, "")
		}
		if err != nil {
			return err
		}
		if err := checkSubscription(ctx, q, id.ClubID, now); err != nil {
			return err
		}
		pcRow, err := q.GuestComputer(ctx, store.GuestComputerParams{ClubID: id.ClubID, ID: id.ComputerID})
		if err != nil {
			return err
		}
		computerName = displayName(pcRow.DisplayName, pcRow.DeviceName, pcRow.Number)
		return nil
	})
	if err != nil {
		return nil, s.fail(ctx, "signIn", err)
	}
	if !staffRoles[role] {
		other, err := s.tokens.SignedInElsewhere(ctx, userID, id.ComputerID, s.onlineWith(ctx, id.ClubID, userID))
		if err != nil {
			return nil, s.fail(ctx, "signIn", err)
		}
		if other > 0 {
			return nil, s.signedInElsewhere(ctx, id.ClubID, other)
		}
	}
	token, exp, err := s.tokens.Issue(ctx, id, userID)
	if err != nil {
		return nil, s.fail(ctx, "signIn", err)
	}
	out := &agentv1.GuestSignIn{Token: &agentv1.GuestToken{AccessToken: token, ExpiresAt: timestamppb.New(exp)}}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		if err := s.record(ctx, q, activity.Entry{ClubID: id.ClubID, Event: event, Description: description,
			UserID: userID, SubjectType: subjectUser, SubjectID: userID, Details: map[string]any{"device": computerName, "computer": computerName}}); err != nil {
			return err
		}
		var err error
		out.User, out.Balance, out.Session, err = s.profile(ctx, q, id, userID, now)
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "signIn", err)
	}
	return out, nil
}

// onlineWith — ПК в сети и сообщает этого гостя (Status.signed_in_user_id) — признак «сидит за ПК».
func (s *Server) onlineWith(ctx context.Context, clubID, userID int64) func(int64) bool {
	if s.o.Presence == nil {
		return nil
	}
	return func(pcID int64) bool {
		st, ok, err := s.o.Presence.Get(ctx, clubID, pcID)
		return err == nil && ok && st.Online && st.SignedInUserID == userID
	}
}

func (s *Server) signedInElsewhere(ctx context.Context, clubID, computerID int64) error {
	name := "—"
	_ = db.InTenantTx(ctx, s.o.Pool, clubID, func(q *store.Queries) error {
		row, err := q.GuestComputer(ctx, store.GuestComputerParams{ClubID: clubID, ID: computerID})
		if err == nil {
			name = displayName(row.DisplayName, row.DeviceName, row.Number)
		}
		return nil
	})
	return rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SIGNED_IN_ELSEWHERE, msgSignedInElsewhere,
		map[string]string{"computer": name}, "")
}

func checkSubscription(ctx context.Context, q *store.Queries, clubID int64, now time.Time) error {
	sub, err := q.GuestClubSubscription(ctx, clubID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err != nil || !subscriptionActive(sub, now) {
		return rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SUBSCRIPTION_INACTIVE, msgSubscription, nil, "")
	}
	return nil
}

// profile — гость, балансы и его активная сессия на этом ПК (GuestSignIn, GetProfile).
func (s *Server) profile(ctx context.Context, q *store.Queries, id identity.Identity, userID int64, now time.Time) (*agentv1.GuestUser, *agentv1.Balance, *agentv1.ActiveSession, error) {
	u, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: id.ClubID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, errTokenInvalid
	}
	if err != nil {
		return nil, nil, nil, err
	}
	ci, err := loadClub(ctx, q, id)
	if err != nil {
		return nil, nil, nil, err
	}
	bal, err := balanceOf(ctx, q, id.ClubID, userID, ci.settings.currency)
	if err != nil {
		return nil, nil, nil, err
	}
	pcRow, err := q.GuestComputer(ctx, store.GuestComputerParams{ClubID: id.ClubID, ID: id.ComputerID})
	if err != nil {
		return nil, nil, nil, err
	}
	var sess *agentv1.ActiveSession
	if as := activeSession(pcRow, now); as != nil && as.GetUserId() == userID {
		sess = as
	}
	gu := &agentv1.GuestUser{UserId: u.ID, Name: u.Name, MaskedPhone: MaskPhone(u.Phone),
		DiscountBasisPoints: int32(pricing.DiscountBasisPoints(u.Discount))} // #nosec G115 -- 0…10000
	return gu, bal, sess, nil
}

func balanceOf(ctx context.Context, q *store.Queries, clubID, userID int64, currency string) (*agentv1.Balance, error) {
	b, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: clubID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return &agentv1.Balance{Currency: currency}, nil // $balance?->balance ?? 0
	}
	if err != nil {
		return nil, err
	}
	return &agentv1.Balance{MainTiyn: b.Balance, BonusTiyn: b.BonusBalance, Currency: currency}, nil
}

// activeSession — ClubComputer::hasPaidSession() + ClubComputerSessions::sessionPayload().
func activeSession(row store.GuestComputerRow, now time.Time) *agentv1.ActiveSession {
	if row.CurrentSessionID == nil || (row.CurrentSessionUserID == nil && !row.SessionGuest) ||
		row.SessionEndsAt == nil || !row.SessionEndsAt.After(now) {
		return nil
	}
	as := &agentv1.ActiveSession{SessionId: *row.CurrentSessionID, EndsAt: timestamppb.New(*row.SessionEndsAt), Guest: row.SessionGuest}
	if row.CurrentSessionUserID != nil {
		as.UserId = *row.CurrentSessionUserID
	}
	return as
}

// Register — регистрация на ПК или вступление в клуб существующего аккаунта (POST /api/agent/register,
// AgentRegisterController::__invoke()).
func (a *authService) Register(ctx context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	s := a.s
	ctx, id, err := s.call(ctx, ratelimit.AgentRegister, nil)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	phone := NormalizePhone(req.GetPhone())
	switch n := utf8.RuneCountInString(req.GetName()); {
	case n < 2 || n > 255 || utf8.RuneCountInString(name) < 2:
		return nil, validation("name", msgNameLength)
	}
	if !registerPhoneRe.MatchString(phone) {
		return nil, validation("phone", msgPhoneDigits)
	}
	switch n := utf8.RuneCountInString(req.GetPassword()); {
	case n < 6:
		return nil, validation("password", msgPasswordMin)
	case n > 255:
		return nil, validation("password", msgPasswordMax)
	}
	now := s.o.Now()

	var existing *store.GetUserByPhoneRow
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		if err := checkSubscription(ctx, q, id.ClubID, now); err != nil {
			return err
		}
		u, err := q.GetUserByPhone(ctx, phone)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		existing = &u
		if _, err := q.GuestMembershipRole(ctx, store.GuestMembershipRoleParams{ClubID: id.ClubID, UserID: u.ID}); err == nil {
			return rpcError(codes.AlreadyExists, agentv1.ErrorCode_ERROR_CODE_ALREADY_CLUB_MEMBER, msgAlreadyMember, nil, "phone")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if u.BlockedAt != nil {
			return accountBlocked()
		}
		return nil
	})
	if err != nil {
		return nil, s.fail(ctx, "Register", err)
	}
	var hash string
	if existing != nil {
		if err := s.checkPassword(ctx, id, existing.ID, existing.Password, req.GetPassword()); err != nil {
			if status.Code(err) == codes.Unauthenticated {
				return nil, rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_WRONG_PASSWORD, msgJoinWrongPass, nil, "password")
			}
			return nil, err
		}
	} else if hash, err = s.o.Passwords.Hash(ctx, req.GetPassword()); err != nil {
		return nil, s.fail(ctx, "Register", err)
	}

	var userID int64
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		pcRow, err := q.GuestComputer(ctx, store.GuestComputerParams{ClubID: id.ClubID, ID: id.ComputerID})
		if err != nil {
			return err
		}
		computer := displayName(pcRow.DisplayName, pcRow.DeviceName, pcRow.Number)
		event, desc := "user.joined_club", "Клиент другого клуба стал клиентом этого клуба на ПК "+computer+": "
		if existing != nil {
			userID = existing.ID
			desc += existing.Name
		} else {
			userID, err = q.GuestCreateUser(ctx, store.GuestCreateUserParams{Name: name, Phone: phone, PasswordHash: hash,
				Now: &now, ClubID: id.ClubID, ComputerID: id.ComputerID})
			if err != nil {
				return err
			}
			event, desc = "user.registered", "Клиент зарегистрировался на ПК "+computer+": "+name
		}
		if err := q.GuestCreateMembership(ctx, store.GuestCreateMembershipParams{ClubID: id.ClubID, UserID: userID, Now: &now}); err != nil {
			return err
		}
		if err := q.EnsureMemberBalance(ctx, store.EnsureMemberBalanceParams{ClubID: id.ClubID, UserID: userID}); err != nil {
			return err
		}
		return s.record(ctx, q, activity.Entry{ClubID: id.ClubID, Event: event, Description: desc, UserID: userID,
			SubjectType: subjectUser, SubjectID: userID, Details: map[string]any{"computer": computer}})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// Параллельная регистрация того же номера или членства: как «уже клиент».
		return nil, rpcError(codes.AlreadyExists, agentv1.ErrorCode_ERROR_CODE_ALREADY_CLUB_MEMBER, msgAlreadyMember, nil, "phone")
	}
	if err != nil {
		return nil, s.fail(ctx, "Register", err)
	}
	token, exp, err := s.tokens.Issue(ctx, id, userID)
	if err != nil {
		return nil, s.fail(ctx, "Register", err)
	}
	out := &agentv1.GuestSignIn{Token: &agentv1.GuestToken{AccessToken: token, ExpiresAt: timestamppb.New(exp)}}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		var err error
		out.User, out.Balance, out.Session, err = s.profile(ctx, q, id, userID, now)
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "Register", err)
	}
	return &agentv1.RegisterResponse{SignIn: out, AccountCreated: existing == nil}, nil
}

// Logout — выход гостя: токен отзывается (POST /api/auth/logout, AuthController::logout()).
func (a *authService) Logout(ctx context.Context, _ *agentv1.LogoutRequest) (*agentv1.LogoutResponse, error) {
	s := a.s
	ctx, id, g, err := s.guestCall(ctx, "")
	if err != nil {
		return nil, err
	}
	if err := s.tokens.Revoke(ctx, g); err != nil {
		return nil, s.fail(ctx, "Logout", err)
	}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		u, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: id.ClubID, UserID: g.UserID})
		if err != nil {
			return nil //nolint:nilerr // член клуба удалён — журнал не пишется, токен уже отозван
		}
		return s.record(ctx, q, activity.Entry{ClubID: id.ClubID, Event: "auth.api_logout", Description: "Выход через API: " + u.Name,
			UserID: g.UserID, SubjectType: subjectUser, SubjectID: g.UserID})
	})
	if err != nil {
		s.o.Log.WarnContext(ctx, "guest logout activity not recorded", "err", err)
	}
	return &agentv1.LogoutResponse{}, nil
}

// RequestPasswordResetCode — TODO(Этап 2+): коды в Telegram (PasswordRecovery::sendCode, Telegram Gateway).
// Сервиса Telegram в Go ещё нет — Unimplemented (docs/open-questions/stage2-guest-grpc.md GG-5).
func (a *authService) RequestPasswordResetCode(context.Context, *agentv1.RequestPasswordResetCodeRequest) (*agentv1.RequestPasswordResetCodeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "password recovery via Telegram is not implemented yet")
}

// ResetPassword — TODO: см. RequestPasswordResetCode.
func (a *authService) ResetPassword(context.Context, *agentv1.ResetPasswordRequest) (*agentv1.ResetPasswordResponse, error) {
	return nil, status.Error(codes.Unimplemented, "password recovery via Telegram is not implemented yet")
}

// ---------------------------------------------------------------------------
// QR-вход (QrLogins)
// ---------------------------------------------------------------------------

// CreateQrLogin — QR-код входа с телефона (POST /api/agent/qr-login, AgentQrLoginController::store()).
func (a *authService) CreateQrLogin(ctx context.Context, _ *agentv1.CreateQrLoginRequest) (*agentv1.CreateQrLoginResponse, error) {
	s := a.s
	ctx, id, err := s.call(ctx, ratelimit.AgentQrLogin, nil)
	if err != nil {
		return nil, err
	}
	code, err := randomCode(40)
	if err != nil {
		return nil, s.fail(ctx, "CreateQrLogin", err)
	}
	loginID, err := uuid.NewV7()
	if err != nil {
		return nil, s.fail(ctx, "CreateQrLogin", err)
	}
	now := s.o.Now()
	expires := now.Add(qrLifetime)
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		ci, err := loadClub(ctx, q, id)
		if err != nil {
			return err
		}
		if !ci.settings.feature(featureQRLogin) {
			return featureDisabled()
		}
		if err := checkSubscription(ctx, q, id.ClubID, now); err != nil {
			return err
		}
		// Один открытый код на ПК: новый QR заменяет прежний (QrLogins::create()).
		if err := q.GuestDeleteComputerQrLogins(ctx, store.GuestDeleteComputerQrLoginsParams{ClubID: id.ClubID, ComputerID: id.ComputerID}); err != nil {
			return err
		}
		return q.GuestInsertQrLogin(ctx, store.GuestInsertQrLoginParams{ID: pgtype.UUID{Bytes: loginID, Valid: true},
			ClubID: id.ClubID, ComputerID: id.ComputerID, CodeHash: hashQRCode(code), ExpiresAt: expires, Now: &now})
	})
	if err != nil {
		return nil, s.fail(ctx, "CreateQrLogin", err)
	}
	return &agentv1.CreateQrLoginResponse{LoginId: loginID.String(), Url: s.o.QRBaseURL + "/pc-login/" + code,
		ExpiresAt: timestamppb.New(expires)}, nil
}

// hashQRCode — AgentQrLogin::hashCode(): hash('sha256', $code).
func hashQRCode(code string) string {
	s := sha256.Sum256([]byte(code))
	return hex.EncodeToString(s[:])
}

// randomCode — Str::random(n): [A-Za-z0-9].
func randomCode(n int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	buf := make([]byte, 1)
	for i := 0; i < n; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if int(buf[0]) < 248 { // 248 = 4*62: без смещения распределения
			b[i] = alphabet[int(buf[0])%len(alphabet)]
			i++
		}
	}
	return string(b), nil
}

// WatchQrLogin — ожидание подтверждения QR-входа: текущий статус сразу, затем при изменении; после APPROVED
// (с токеном) или EXPIRED стрим закрывается. Заменяет опрос POST /api/agent/qr-login/poll каждые 3 с
// (QrLogins::poll()).
func (a *authService) WatchQrLogin(req *agentv1.WatchQrLoginRequest, st agentv1.GuestAuthService_WatchQrLoginServer) error {
	s := a.s
	ctx, id, err := s.call(st.Context(), ratelimit.AgentQrPoll, nil)
	if err != nil {
		return err
	}
	loginID, err := uuid.Parse(req.GetLoginId())
	if err != nil {
		return validation("login_id", "Неверный QR-вход.")
	}
	release, err := s.qrWatchSlot(ctx, id.ComputerID)
	if err != nil {
		return err
	}
	defer release()

	wake := s.qrWaiters.add(loginID)
	defer s.qrWaiters.remove(loginID, wake)
	last := agentv1.QrLoginStatus_QR_LOGIN_STATUS_UNSPECIFIED
	t := time.NewTicker(s.o.QRPollEvery)
	defer t.Stop()
	for {
		resp, err := s.pollQr(ctx, id, loginID)
		if err != nil {
			return s.fail(ctx, "WatchQrLogin", err)
		}
		if resp.GetStatus() != last {
			if err := st.Send(resp); err != nil {
				return err
			}
			last = resp.GetStatus()
		}
		if last != agentv1.QrLoginStatus_QR_LOGIN_STATUS_PENDING {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-t.C:
		}
	}
}

// pollQr — QrLogins::poll(): вход этого ПК FOR UPDATE; нет или не открыт → expired; не подтверждён → pending;
// подтверждён → погасить, проверить аккаунт (заблокирован или не член клуба → expired), выдать токен.
func (s *Server) pollQr(ctx context.Context, id identity.Identity, loginID uuid.UUID) (*agentv1.WatchQrLoginResponse, error) {
	now := s.o.Now()
	expired := &agentv1.WatchQrLoginResponse{Status: agentv1.QrLoginStatus_QR_LOGIN_STATUS_EXPIRED}
	pending := &agentv1.WatchQrLoginResponse{Status: agentv1.QrLoginStatus_QR_LOGIN_STATUS_PENDING}
	pgID := pgtype.UUID{Bytes: loginID, Valid: true}
	// Сначала без блокировки: ожидание (pending) не держит строку.
	var approved int64
	var name string
	err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		approved = 0
		row, err := q.GuestGetQrLogin(ctx, store.GuestGetQrLoginParams{ClubID: id.ClubID, ID: pgID, ComputerID: id.ComputerID})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (row.ConsumedAt != nil || !row.ExpiresAt.After(now))) {
			return errQRExpired
		}
		if err != nil {
			return err
		}
		if row.ApprovedUserID == nil {
			return errQRPending
		}
		row2, err := q.GuestLockQrLogin(ctx, store.GuestLockQrLoginParams{ClubID: id.ClubID, ID: pgID, ComputerID: id.ComputerID})
		if errors.Is(err, pgx.ErrNoRows) {
			return errQRExpired
		}
		if err != nil {
			return err
		}
		if row2.ConsumedAt != nil || !row2.ExpiresAt.After(now) || row2.ApprovedUserID == nil {
			return errQRExpired
		}
		if err := q.GuestConsumeQrLogin(ctx, store.GuestConsumeQrLoginParams{Now: &now, ClubID: id.ClubID, ID: pgID}); err != nil {
			return err
		}
		u, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: id.ClubID, UserID: *row2.ApprovedUserID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // не член клуба — погашен, expired
		}
		if err != nil {
			return err
		}
		if u.BlockedAt == nil {
			approved, name = u.ID, u.Name
		}
		return nil
	})
	switch {
	case errors.Is(err, errQRExpired):
		return expired, nil
	case errors.Is(err, errQRPending):
		return pending, nil
	case err != nil:
		return nil, err
	case approved == 0:
		return expired, nil
	}
	// Как при входе по паролю: один токен на ПК, прежний перестаёт работать (QrLogins::poll() стр. 63).
	signIn, err := s.signIn(ctx, id, approved, "auth.qr_login", "Вход по QR-коду на ПК: "+name)
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() != codes.Internal && st.Code() != codes.Unavailable {
			return expired, nil
		}
		return nil, err
	}
	return &agentv1.WatchQrLoginResponse{Status: agentv1.QrLoginStatus_QR_LOGIN_STATUS_APPROVED, SignIn: signIn}, nil
}

var (
	errQRExpired = errors.New("qr login expired")
	errQRPending = errors.New("qr login pending")
)

// qrWatchSlot — не более maxQRWatchers ожиданий QR на ПК одновременно (счётчик в Valkey с TTL).
func (s *Server) qrWatchSlot(ctx context.Context, computerID int64) (func(), error) {
	key := s.o.KeyPrefix + "qrw:" + strconv.FormatInt(computerID, 10)
	c := s.o.Valkey
	n, err := c.Do(ctx, c.B().Incr().Key(key).Build()).AsInt64()
	if err != nil {
		return nil, s.fail(ctx, "WatchQrLogin", err)
	}
	_ = c.Do(ctx, c.B().Expire().Key(key).Seconds(int64(qrLifetime/time.Second)+60).Build()).Error()
	release := func() { _ = c.Do(context.WithoutCancel(ctx), c.B().Decr().Key(key).Build()).Error() }
	if n > maxQRWatchers {
		release()
		return nil, grpcRateLimited(time.Second)
	}
	return release, nil
}

// ApproveQrLogin — подтверждение QR-входа с телефона клиентом userID (QrLogins::approve()); серверная часть
// для веб-страницы /pc-login/{code} и мобильного приложения (их маршруты — следующие этапы). clubID — клуб
// страницы/токена клиента. После успеха ПК получает подсказку QrLoginApproved, ожидание WatchQrLogin
// на этом инстансе просыпается сразу (на других — по таймеру).
func (s *Server) ApproveQrLogin(ctx context.Context, clubID int64, code string, userID int64) error {
	now := s.o.Now()
	var loginID uuid.UUID
	var computerID int64
	err := db.InTenantTx(ctx, s.o.Pool, clubID, func(q *store.Queries) error {
		login, err := q.GuestFindOpenQrLogin(ctx, store.GuestFindOpenQrLoginParams{ClubID: clubID, CodeHash: hashQRCode(code), Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_QR_LOGIN_EXPIRED, msgQRExpired, nil, "code")
		}
		if err != nil {
			return err
		}
		loginID, computerID = login.ID.Bytes, login.ClubComputerID
		u, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: clubID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return validation("code", msgQRNotMember)
		}
		if err != nil {
			return err
		}
		if u.BlockedAt != nil {
			return validation("code", msgAccountBlocked)
		}
		if !staffRoles[u.Role] {
			other, err := s.tokens.SignedInElsewhere(ctx, userID, computerID, s.onlineWith(ctx, clubID, userID))
			if err != nil {
				return err
			}
			if other > 0 {
				st, _ := status.FromError(s.signedInElsewhere(ctx, clubID, other))
				return validation("code", st.Message())
			}
		}
		if err := checkSubscription(ctx, q, clubID, now); err != nil {
			return validation("code", msgSubscription)
		}
		uid := userID
		n, err := q.GuestApproveQrLogin(ctx, store.GuestApproveQrLoginParams{UserID: &uid, Now: &now, ClubID: clubID, ID: login.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return validation("code", msgQRExpired)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.qrWaiters.wake(loginID)
	if s.o.Commands != nil {
		if _, err := s.o.Commands.Send(ctx, clubID, computerID, &agentv1.Command{
			Kind: &agentv1.Command_QrLoginApproved{QrLoginApproved: &agentv1.QrLoginApproved{LoginId: loginID.String()}},
		}); err != nil {
			s.o.Log.WarnContext(ctx, "qr login approved hint not sent", "computer_id", computerID, "err", err)
		}
	}
	return nil
}
