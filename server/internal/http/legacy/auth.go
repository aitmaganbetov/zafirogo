package legacy

// Аутентификация Sanctum, клуб токена, подписка, роли и права группы маршрутов; вход, профиль, клубы, смена клуба,
// выход и восстановление пароля (Laravel: app/Http/Controllers/Api/AuthController.php,
// PasswordRecoveryController.php; middleware auth:sanctum, current-club, subscription, club-role, club-can).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты (русский текст = ключ перевода, как в старом коде).
const (
	msgBadCredentials   = "Неверный номер телефона или пароль." // #nosec G101 -- текст для пользователя, не секрет
	msgAccountBlocked   = "Аккаунт заблокирован. Обратитесь в поддержку Zafiro."
	msgBlockedShort     = "Аккаунт заблокирован."
	msgNoClubAccess     = "У аккаунта нет доступа к выбранному клубу."
	msgSubscription     = "Подписка клуба не активна."
	msgBadClubKey       = "Ключ установки недействителен."
	msgTokenOtherClub   = "API-токен привязан к другому клубу." // #nosec G101 -- текст для пользователя, не секрет
	msgNoSelectedClub   = "Нет доступа к выбранному клубу."
	msgNoRight          = "Нет права на этот раздел. Его даёт владелец клуба в «Настройки → Права»."
	msgLoggedOut        = "Сессия завершена."
	msgPhoneRequired    = "Номер телефона обязателен."
	msgPhoneInvalid     = "Введите корректный номер телефона."
	msgCodeRequired     = "Введите код из Telegram."
	msgCodeFormat       = "Код — 6 цифр из сообщения в Telegram."
	msgPasswordRequired = "Пароль обязателен."                          // #nosec G101 -- текст для пользователя, не секрет
	msgPasswordMin      = "Пароль должен содержать минимум 6 символов." // #nosec G101 -- текст для пользователя, не секрет
	msgPasswordMismatch = "Пароли не совпадают."                        // #nosec G101 -- текст для пользователя, не секрет
	msgPasswordChanged  = "Пароль изменён. Войдите с новым паролем."    // #nosec G101 -- текст для пользователя, не секрет
	msgCodeSent         = "Если номер зарегистрирован в Zafiro, код придёт в Telegram на этот номер. Код действует :minutes мин."
	msgRecoveryOff      = "Восстановление пароля через Telegram пока не подключено. Обратитесь к администратору клуба." // #nosec G101 -- текст для пользователя, не секрет
	morphUser           = `App\Models\User`
)

// PasswordRecovery::CODE_MINUTES, RESEND_SECONDS.
const (
	recoveryCodeMinutes   = 5
	recoveryResendSeconds = 60
)

var (
	loginPhoneRe = regexp.MustCompile(`^\+?[1-9]\d{1,14}$`) // LoginRequest
	kzPhoneRe    = regexp.MustCompile(`^\+7\d{10}$`)
	codeRe       = regexp.MustCompile(`^\d{6}$`)
)

// normalizePhone — App\Support\PhoneNumber::normalize().
func normalizePhone(phone string) string {
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

// ---------------------------------------------------------------------------
// Токены Sanctum
// ---------------------------------------------------------------------------

// token — токен запроса (currentAccessToken()).
type token struct {
	ID     int64
	ClubID int64
	UserID int64
	Name   string
}

type tokenKey struct{}

func tokenFrom(ctx context.Context) token { t, _ := ctx.Value(tokenKey{}).(token); return t }

func hashToken(plain string) string {
	s := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(s[:])
}

const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// newPlainToken — Sanctum 4 generateTokenString(): Str::random(40) + crc32b hex (префикса нет — config/sanctum.php
// в старом проекте отсутствует, token_prefix = ”).
func newPlainToken() (string, error) {
	b := make([]byte, 40)
	limit := big.NewInt(int64(len(alnum)))
	for i := range b {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		b[i] = alnum[n.Int64()]
	}
	return fmt.Sprintf("%s%08x", b, crc32.ChecksumIEEE(b)), nil
}

// issueToken — $user->createToken($name, ['club:'.$clubId], $expiresAt): «<id>|<токен>».
func (a *API) issueToken(ctx context.Context, clubID, userID int64, name string, now, expires time.Time) (string, error) {
	plain, err := newPlainToken()
	if err != nil {
		return "", err
	}
	var id int64
	err = db.InTenantTx(ctx, a.d.Pool, clubID, func(q *store.Queries) error {
		id, err = q.MobileTokenInsert(ctx, store.MobileTokenInsertParams{ClubID: clubID, UserID: userID, Name: name,
			TokenHash: hashToken(plain), Now: now, ExpiresAt: &expires})
		return err
	})
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10) + "|" + plain, nil
}

// lookupToken — строка токена по id и хешу (транзакция с app.mobile_token_hash). Нет — found=false.
func (a *API) lookupToken(ctx context.Context, id int64, plain string) (row store.MobileTokenLookupRow, found bool, err error) {
	h := hashToken(plain)
	err = pgx.BeginTxFunc(ctx, a.d.Pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.mobile_token_hash', $1, true)", h); err != nil {
			return err
		}
		row, err = store.New(tx).MobileTokenLookup(ctx, store.MobileTokenLookupParams{ID: id, TokenHash: h})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, false, nil
	}
	return row, err == nil, err
}

// userTx — транзакция с app.user_id без клуба: свои членства (member_self_read) и свои токены.
func (a *API) userTx(ctx context.Context, userID int64, fn func(q *store.Queries) error) error {
	return pgx.BeginTxFunc(ctx, a.d.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', $1, true)", strconv.FormatInt(userID, 10)); err != nil {
			return err
		}
		return fn(store.New(tx))
	})
}

// touched — last_used_at не чаще раза в минуту на токен (Sanctum пишет на каждый запрос; CLAUDE.md:
// «не писать в Postgres на каждый пинг»).
var touched sync.Map

const touchEvery = time.Minute

func (a *API) touch(ctx context.Context, t token, now time.Time) {
	if prev, ok := touched.Load(t.ID); ok {
		if at, _ := prev.(time.Time); now.Sub(at) < touchEvery {
			return
		}
	}
	touched.Store(t.ID, now)
	if err := db.InTenantTx(ctx, a.d.Pool, t.ClubID, func(q *store.Queries) error {
		return q.MobileTokenTouch(ctx, store.MobileTokenTouchParams{Now: &now, ClubID: t.ClubID, ID: t.ID})
	}); err != nil {
		a.o.Log.WarnContext(ctx, "mobile token touch", slog.String("err", err.Error()))
	}
}

// ---------------------------------------------------------------------------
// Middleware группы маршрутов
// ---------------------------------------------------------------------------

// forward — запрос не этого сервера: на старый Laravel (true), если он задан.
func (a *API) forward(w http.ResponseWriter, r *http.Request) bool {
	if a.proxy == nil {
		return false
	}
	a.proxy.ServeHTTP(w, r)
	return true
}

// authenticate — auth:sanctum + current-club: токен, блокировка, клуб токена (X-Club-Id — только он же),
// членство; Principal (клуб и права) в контексте.
func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, plain, ok := bearer(r)
		if !ok {
			writeMessage(w, http.StatusUnauthorized, msgUnauthenticated)
			return
		}
		row, found, err := a.lookupToken(ctx, id, plain)
		if err != nil {
			a.fail(w, r, "token lookup", err)
			return
		}
		if !found {
			// Токен другого сервера (клуб ещё на Laravel) — туда; иначе он недействителен.
			if !a.forward(w, r) {
				writeMessage(w, http.StatusUnauthorized, msgUnauthenticated)
			}
			return
		}
		now := a.d.NowFunc()
		if row.RevokedAt != nil || (row.ExpiresAt != nil && !row.ExpiresAt.After(now)) {
			writeMessage(w, http.StatusUnauthorized, msgUnauthenticated)
			return
		}
		t := token{ID: row.ID, ClubID: row.ClubID, UserID: row.UserID, Name: row.Name}
		ctx = context.WithValue(ctx, tokenKey{}, t)
		if row.BlockedAt != nil {
			writeMessage(w, http.StatusForbidden, a.tc(ctx, row.ClubID, msgBlockedShort, nil))
			return
		}
		if h := strings.TrimSpace(r.Header.Get("X-Club-Id")); h != "" {
			if n, err := strconv.ParseInt(h, 10, 64); err != nil || n != t.ClubID {
				writeMessage(w, http.StatusForbidden, a.tc(ctx, row.ClubID, msgTokenOtherClub, nil))
				return
			}
		}
		var m store.GetMembershipAuthRow
		err = db.InTenantTxWith(ctx, a.d.Pool, t.ClubID, db.Options{UserID: t.UserID}, func(q *store.Queries) error {
			m, err = q.GetMembershipAuth(ctx, store.GetMembershipAuthParams{ClubID: t.ClubID, UserID: t.UserID})
			return err
		})
		if errors.Is(err, pgx.ErrNoRows) {
			writeMessage(w, http.StatusForbidden, a.tc(ctx, row.ClubID, msgNoSelectedClub, nil))
			return
		}
		if err != nil {
			a.fail(w, r, "membership", err)
			return
		}
		a.touch(ctx, t, now)
		mem := &permissions.Membership{Role: m.Role, Given: permissions.ParseGiven(m.Permissions)}
		ctx = principal.WithPrincipal(ctx, principal.Principal{UserID: t.UserID, ClubID: t.ClubID, Role: m.Role,
			Permissions: permissions.For(mem)})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// subscriptionActive — ClubSubscription::isActive() (BR-130).
func subscriptionActive(row store.GuestClubSubscriptionRow, now time.Time) bool {
	switch row.Status {
	case "active":
		return row.CurrentPeriodEndsAt == nil || row.CurrentPeriodEndsAt.After(now)
	case "trialing":
		return row.TrialEndsAt != nil && row.TrialEndsAt.After(now)
	}
	return false
}

func (a *API) clubActive(ctx context.Context, clubID int64) (bool, error) {
	var active bool
	err := db.InTenantTx(ctx, a.d.Pool, clubID, func(q *store.Queries) error {
		row, err := q.GuestClubSubscription(ctx, clubID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		active = err == nil && subscriptionActive(row, a.d.NowFunc())
		return err
	})
	return active, err
}

// subscription — RequireActiveSubscription: 402 {"message": "Подписка клуба не активна."}.
func (a *API) subscription(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, err := a.clubActive(r.Context(), httpx.ClubID(r.Context()))
		if err != nil {
			a.fail(w, r, "subscription", err)
			return
		}
		if !ok {
			writeMessage(w, http.StatusPaymentRequired, a.t(r, msgSubscription, nil))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// roles — club-role:<роли> (владелец — всегда): abort(403) без текста.
func roles(allowed []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := httpx.Principal(r.Context()).Role
			if role != permissions.RoleOwner && !slices.Contains(allowed, role) {
				writeMessage(w, http.StatusForbidden, "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// can — club-can:<право> (RequireClubPermission).
func (a *API) can(right string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !httpx.Principal(r.Context()).Can(right) {
				writeMessage(w, http.StatusForbidden, a.t(r, msgNoRight, nil))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// throttle — throttle:<правило> (ThrottleRequests): ключ — пользователь токена, без входа — IP; для
// customer-login и password-recovery — ещё телефон из тела. 429 {"message": "Too Many Attempts."}.
func (a *API) throttle(rule string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		l := a.o.Limiter
		if l == nil {
			return next
		}
		if !l.Has(rule) {
			panic("legacy: unknown rate limit " + rule)
		}
		fields := ratelimit.FieldsFor(rule)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Enabled(rule) {
				next.ServeHTTP(w, r)
				return
			}
			s := ratelimit.SubjectFromRequest(r, fields)
			res, err := l.Allow(r.Context(), rule, s)
			if err != nil {
				a.fail(w, r, "rate limit", err)
				return
			}
			ratelimit.WriteHeaders(w, res)
			if !res.Allowed {
				writeMessage(w, http.StatusTooManyRequests, ratelimit.TooManyAttemptsMessage)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// fail — неожиданная ошибка: 500 {"message": "Server Error"}, подробности — только в лог.
func (a *API) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.o.Log.ErrorContext(r.Context(), "legacy "+what, slog.String("err", err.Error()))
	writeMessage(w, http.StatusInternalServerError, "Server Error")
}

// ---------------------------------------------------------------------------
// Переводы
// ---------------------------------------------------------------------------

// locale — язык ответа: клуб запроса (LocaleResolver::forAgentRequest(): X-Agent-Locale или язык клуба); до входа —
// Accept-Language (forGuest), иначе ru.
func (a *API) locale(r *http.Request) string {
	if clubID := httpx.ClubID(r.Context()); clubID > 0 {
		return a.clubLocale(r.Context(), clubID)
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		l := strings.ToLower(strings.TrimSpace(strings.SplitN(strings.SplitN(part, ";", 2)[0], "-", 2)[0]))
		if l == "ru" || l == "kk" || l == "en" {
			return l
		}
	}
	return i18n.SourceLocale
}

func (a *API) clubLocale(ctx context.Context, clubID int64) string {
	if a.d.I18n == nil {
		return i18n.SourceLocale
	}
	return a.d.I18n.ClubLocale(ctx, clubID)
}

// t — __($key, $params) на языке запроса.
func (a *API) t(r *http.Request, key string, params map[string]string) string {
	return a.tl(r.Context(), a.locale(r), key, params)
}

// tc — на языке клуба clubID.
func (a *API) tc(ctx context.Context, clubID int64, key string, params map[string]string) string {
	return a.tl(ctx, a.clubLocale(ctx, clubID), key, params)
}

func (a *API) tl(ctx context.Context, locale, key string, params map[string]string) string {
	if a.d.I18n == nil {
		return i18n.Render(i18n.SourceLocale, key, params)
	}
	return a.d.I18n.Translate(ctx, locale, key, params)
}

// ---------------------------------------------------------------------------
// Вход и профиль
// ---------------------------------------------------------------------------

// userData — AuthController::userData(): деньги — (float) тенге, как в старом ответе.
type userData struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Phone        string   `json:"phone"`
	ClubID       *int64   `json:"club_id"`
	Role         *string  `json:"role"`
	Discount     phpFloat `json:"discount"`
	Balance      phpFloat `json:"balance"`
	BonusBalance phpFloat `json:"bonus_balance"`
	Currency     string   `json:"currency"`

	clubName string
	settings clubSetting
}

type clubInfo struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Current bool   `json:"current"`
	Role    string `json:"role"`
}

// loginPayload — ответ входа и смены клуба (порядок ключей — как в PHP-массиве).
type loginPayload struct {
	TokenType   string     `json:"token_type"`
	ExpiresAt   string     `json:"expires_at"`
	User        userData   `json:"user"`
	ClubID      int64      `json:"club_id"`
	ClubName    string     `json:"club_name"`
	Token       string     `json:"token"`
	AccessToken string     `json:"access_token"`
	Clubs       []clubInfo `json:"clubs"`
}

func (a *API) profile(ctx context.Context, clubID, userID int64) (userData, error) {
	var u userData
	err := db.InTenantTxWith(ctx, a.d.Pool, clubID, db.Options{UserID: userID}, func(q *store.Queries) error {
		p, err := q.MobileMemberProfile(ctx, store.MobileMemberProfileParams{ClubID: clubID, UserID: userID})
		if err != nil {
			return err
		}
		cid, role := clubID, p.Role
		cs := parseClubSetting(p.Settings)
		u = userData{ID: userID, Name: p.UserName, Phone: p.UserPhone, ClubID: &cid, Role: &role,
			Discount: phpFloatOf(p.Discount), Balance: tenge(p.Balance), BonusBalance: tenge(p.BonusBalance),
			Currency: cs.currency(), clubName: p.ClubName, settings: cs}
		return nil
	})
	return u, err
}

func (a *API) clubList(ctx context.Context, userID, current int64) ([]clubInfo, []store.MobileUserClubsRow, error) {
	var rows []store.MobileUserClubsRow
	err := a.userTx(ctx, userID, func(q *store.Queries) error {
		var err error
		rows, err = q.MobileUserClubs(ctx, userID)
		return err
	})
	out := make([]clubInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, clubInfo{ID: r.ClubID, Name: r.ClubName, Current: r.ClubID == current, Role: r.Role})
	}
	return out, rows, err
}

// localUser — аккаунт с этим телефоном и его клубы на этом сервере (маршрутизация входа).
func (a *API) localUser(ctx context.Context, phone string) (store.GetUserByPhoneRow, []store.MobileUserClubsRow, bool, error) {
	var u store.GetUserByPhoneRow
	err := pgx.BeginTxFunc(ctx, a.d.Pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		u, err = store.New(tx).GetUserByPhone(ctx, phone)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return u, nil, false, nil
	}
	if err != nil {
		return u, nil, false, err
	}
	_, rows, err := a.clubList(ctx, u.ID, 0)
	return u, rows, true, err
}

// recordLoginFailed — ActivityLogger::recordFailedLogin('auth.api_login_failed', …): во всех клубах аккаунта.
func (a *API) recordLoginFailed(ctx context.Context, user store.GetUserByPhoneRow, clubs []store.MobileUserClubsRow, reason, device string) {
	if a.d.PlatformPool == nil || len(clubs) == 0 {
		return
	}
	ids := make([]int64, 0, len(clubs))
	for _, c := range clubs {
		ids = append(ids, c.ClubID)
	}
	err := db.InPlatformTx(ctx, a.d.PlatformPool, func(q *store.Queries) error {
		return activity.RecordInClubs(ctx, q, ids, activity.Entry{Event: "auth.api_login_failed",
			Description: i18n.Render(i18n.SourceLocale, "Неудачный вход через API: :user", map[string]string{"user": user.Name}),
			UserID:      user.ID, SubjectType: morphUser, SubjectID: user.ID,
			Details: map[string]any{"reason": reason, "device": device}})
	})
	if err != nil {
		a.o.Log.WarnContext(ctx, "legacy login failed record", slog.String("err", err.Error()))
	}
}

func (a *API) record(ctx context.Context, clubID int64, e activity.Entry) {
	e.ClubID = clubID
	err := db.InTenantTx(ctx, a.d.Pool, clubID, func(q *store.Queries) error {
		_, err := activity.Record(ctx, q, e)
		return err
	})
	if err != nil {
		a.o.Log.WarnContext(ctx, "legacy activity", slog.String("event", e.Event), slog.String("err", err.Error()))
	}
}

// login — POST /api/auth/login (AuthController::login() → issueUserLoginResponse(..., false)).
func (a *API) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw := peekBody(r)
	b := readBody(r)
	restore(r, raw)
	phone, _ := b.str("phone")
	// Маршрутизация: телефон без клубов на этом сервере — на старый сервер.
	user, clubs, found, err := a.localUser(ctx, phone)
	if err != nil {
		a.fail(w, r, "login lookup", err)
		return
	}
	if len(clubs) == 0 && a.forward(w, r) {
		return
	}
	a.throttle(ratelimit.CustomerLogin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.doLogin(w, r, b, user, clubs, found)
	})).ServeHTTP(w, r)
}

func (a *API) doLogin(w http.ResponseWriter, r *http.Request, b body, user store.GetUserByPhoneRow, clubs []store.MobileUserClubsRow, found bool) {
	ctx := r.Context()
	// LoginRequest.
	form := &httpx.FormErrors{}
	phone, has := b.str("phone")
	switch {
	case !b.filled("phone"):
		form.AddErr(httpx.RuleError("phone", "required"))
	case !b.isString("phone"):
		form.AddErr(httpx.RuleError("phone", "string"))
	case !loginPhoneRe.MatchString(phone):
		form.AddErr(httpx.RuleError("phone", "regex"))
	}
	_ = has
	pass, _ := b.str("password")
	switch {
	case !b.filled("password"):
		form.AddErr(httpx.RuleError("password", "required"))
	case !b.isString("password"):
		form.AddErr(httpx.RuleError("password", "string"))
	}
	device, _ := b.str("device_name")
	if b.filled("device_name") && len([]rune(device)) > 255 {
		form.AddErr(httpx.RuleErrorWith("device_name", "max_string", "255"))
	}
	var requested int64
	if b.filled("club_id") {
		n, ok := b.integer("club_id")
		if !ok {
			form.AddErr(httpx.RuleError("club_id", "integer"))
		}
		requested = n
	}
	if err := form.Err(); err != nil {
		a.writeError(w, r, err)
		return
	}
	if strings.TrimSpace(device) == "" {
		device = "mobile"
	}
	failed := func(reason string) { a.recordLoginFailed(ctx, user, clubs, reason, device) }

	if !found {
		_ = a.o.Passwords.VerifyDummy(ctx, pass)
		writeMessage(w, http.StatusUnauthorized, a.t(r, msgBadCredentials, nil))
		return
	}
	if _, err := a.o.Passwords.Verify(ctx, pass, user.Password); err != nil {
		if !errors.Is(err, password.ErrMismatch) && !errors.Is(err, password.ErrUnknownFormat) && !errors.Is(err, password.ErrMalformed) {
			a.fail(w, r, "password", err)
			return
		}
		failed("invalid_credentials")
		writeMessage(w, http.StatusUnauthorized, a.t(r, msgBadCredentials, nil))
		return
	}
	if user.BlockedAt != nil {
		failed("blocked")
		writeCode(w, http.StatusForbidden, a.t(r, msgAccountBlocked, nil), "account_blocked")
		return
	}
	if b.filled("club_key") {
		// Ключи установки клуба (ClubInstallationKey) заменены кодами регистрации ПК (Q-037): ключ не найден.
		failed("invalid_club_key")
		writeMessage(w, http.StatusUnauthorized, a.t(r, msgBadClubKey, nil))
		return
	}
	// ClubMembership::where('user_id')->when($clubId)->first(): первое членство (по id) или выбранного клуба.
	var membership *store.MobileUserClubsRow
	for i := range clubs {
		c := clubs[i]
		if (requested == 0 || c.ClubID == requested) && (membership == nil || c.MembershipID < membership.MembershipID) {
			membership = &c
		}
	}
	if membership == nil {
		failed("no_club_access")
		writeCode(w, http.StatusForbidden, a.t(r, msgNoClubAccess, nil), "not_club_member")
		return
	}
	active, err := a.clubActive(ctx, membership.ClubID)
	if err != nil {
		a.fail(w, r, "subscription", err)
		return
	}
	if !active {
		failed("subscription_inactive")
		writeMessage(w, http.StatusPaymentRequired, a.tc(ctx, membership.ClubID, msgSubscription, nil))
		return
	}
	a.respondToken(w, r, membership.ClubID, user.ID, device, "auth.api_login", "Вход через API: :user", 0)
}

// respondToken — новый токен клуба и ответ входа/смены клуба. revoke — текущий токен (смена клуба), 0 — нет.
func (a *API) respondToken(w http.ResponseWriter, r *http.Request, clubID, userID int64, device, event, descKey string, revoke int64) {
	ctx := r.Context()
	now := a.d.NowFunc()
	expires := now.Add(a.o.TokenTTL)
	plain, err := a.issueToken(ctx, clubID, userID, device, now, expires)
	if err != nil {
		a.fail(w, r, "issue token", err)
		return
	}
	if revoke > 0 {
		if err := db.InTenantTx(ctx, a.d.Pool, httpx.ClubID(ctx), func(q *store.Queries) error {
			return q.MobileTokenRevoke(ctx, store.MobileTokenRevokeParams{Now: &now, ClubID: httpx.ClubID(ctx), ID: revoke})
		}); err != nil {
			a.fail(w, r, "revoke token", err)
			return
		}
	}
	u, err := a.profile(ctx, clubID, userID)
	if err != nil {
		a.fail(w, r, "profile", err)
		return
	}
	details := map[string]any(nil)
	if event == "auth.api_login" {
		details = map[string]any{"device": device}
	}
	a.record(ctx, clubID, activity.Entry{Event: event, Description: a.tc(ctx, clubID, descKey, map[string]string{"user": u.Name}),
		UserID: userID, SubjectType: morphUser, SubjectID: userID, Details: details})
	clubs, _, err := a.clubList(ctx, userID, clubID)
	if err != nil {
		a.fail(w, r, "clubs", err)
		return
	}
	writeJSON(w, http.StatusOK, loginPayload{TokenType: "Bearer", ExpiresAt: billingsvc.ISO8601(expires), User: u,
		ClubID: clubID, ClubName: u.clubName, Token: plain, AccessToken: plain, Clubs: clubs})
}

func restore(r *http.Request, raw []byte) { r.Body = nopBody(raw) }

// me — GET /api/auth/me: {"user": userData}.
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	t := tokenFrom(r.Context())
	u, err := a.profile(r.Context(), t.ClubID, t.UserID)
	if err != nil {
		a.fail(w, r, "me", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

// clubs — GET /api/auth/clubs: {"clubs": [...]}.
func (a *API) clubs(w http.ResponseWriter, r *http.Request) {
	t := tokenFrom(r.Context())
	list, _, err := a.clubList(r.Context(), t.UserID, t.ClubID)
	if err != nil {
		a.fail(w, r, "clubs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clubs": list})
}

// switchClub — POST /api/auth/switch-club: токен другого клуба аккаунта вместо текущего. Клуб, который ещё на
// старом сервере, здесь не найден — 403 «нет доступа» (docs/mobile-compat.md §3.4).
func (a *API) switchClub(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t := tokenFrom(ctx)
	b := readBody(r)
	if !b.filled("club_id") {
		a.writeError(w, r, httpx.RuleError("club_id", "required"))
		return
	}
	clubID, ok := b.integer("club_id")
	if !ok {
		a.writeError(w, r, httpx.RuleError("club_id", "integer"))
		return
	}
	_, rows, err := a.clubList(ctx, t.UserID, t.ClubID)
	if err != nil {
		a.fail(w, r, "clubs", err)
		return
	}
	if !slices.ContainsFunc(rows, func(c store.MobileUserClubsRow) bool { return c.ClubID == clubID }) {
		writeMessage(w, http.StatusForbidden, a.t(r, msgNoClubAccess, nil))
		return
	}
	active, err := a.clubActive(ctx, clubID)
	if err != nil {
		a.fail(w, r, "subscription", err)
		return
	}
	if !active {
		writeMessage(w, http.StatusPaymentRequired, a.tc(ctx, clubID, msgSubscription, nil))
		return
	}
	name := t.Name
	if name == "" {
		name = "mobile"
	}
	a.respondToken(w, r, clubID, t.UserID, name, "auth.api_club_switched", "Смена клуба в приложении: :user", t.ID)
}

// logout — POST /api/auth/logout: журнал auth.api_logout и отзыв текущего токена.
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t := tokenFrom(ctx)
	now := a.d.NowFunc()
	var name string
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		u, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: t.ClubID, UserID: t.UserID})
		if err != nil {
			return err
		}
		name = u.Name
		if _, err := activity.Record(ctx, q, activity.Entry{ClubID: t.ClubID, Event: "auth.api_logout",
			Description: a.tc(ctx, t.ClubID, "Выход через API: :user", map[string]string{"user": name}),
			UserID:      t.UserID, SubjectType: morphUser, SubjectID: t.UserID}); err != nil {
			return err
		}
		return q.MobileTokenRevoke(ctx, store.MobileTokenRevokeParams{Now: &now, ClubID: t.ClubID, ID: t.ID})
	})
	if err != nil {
		a.fail(w, r, "logout", err)
		return
	}
	writeMessage(w, http.StatusOK, a.t(r, msgLoggedOut, nil))
}

// ---------------------------------------------------------------------------
// Восстановление пароля (PasswordRecoveryController)
// ---------------------------------------------------------------------------

// recoveryPhone — PasswordRecoveryController::phone(): нормализация и проверка; ok=false — ответ уже записан.
func (a *API) recoveryPhone(w http.ResponseWriter, r *http.Request, b body) (string, bool) {
	raw, _ := b.str("phone")
	phone := normalizePhone(raw)
	switch {
	case phone == "":
		writeValidation(w, "phone", a.t(r, msgPhoneRequired, nil))
		return "", false
	case !kzPhoneRe.MatchString(phone):
		writeValidation(w, "phone", a.t(r, msgPhoneInvalid, nil))
		return "", false
	}
	return phone, true
}

// passwordRoute — маршрутизация по телефону (как вход) и лимит password-recovery.
func (a *API) passwordRoute(h func(w http.ResponseWriter, r *http.Request, b body)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := peekBody(r)
		b := readBody(r)
		restore(r, raw)
		s, _ := b.str("phone")
		_, clubs, _, err := a.localUser(r.Context(), normalizePhone(s))
		if err != nil {
			a.fail(w, r, "recovery lookup", err)
			return
		}
		if len(clubs) == 0 && a.forward(w, r) {
			return
		}
		a.throttle(ratelimit.PasswordRecovery)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h(w, r, b)
		})).ServeHTTP(w, r)
	}
}

// passwordCode — POST /api/auth/password/code.
func (a *API) passwordCode(w http.ResponseWriter, r *http.Request, b body) {
	phone, ok := a.recoveryPhone(w, r, b)
	if !ok {
		return
	}
	if a.o.Recovery == nil {
		writeValidation(w, "phone", a.t(r, msgRecoveryOff, nil))
		return
	}
	if err := a.o.Recovery.SendCode(r.Context(), phone); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Message       string `json:"message"`
		CodeMinutes   int    `json:"code_minutes"`
		ResendSeconds int    `json:"resend_seconds"`
	}{a.t(r, msgCodeSent, map[string]string{"minutes": strconv.Itoa(recoveryCodeMinutes)}), recoveryCodeMinutes, recoveryResendSeconds})
}

// passwordReset — POST /api/auth/password/reset: новый пароль по коду; все токены аккаунта отзываются.
func (a *API) passwordReset(w http.ResponseWriter, r *http.Request, b body) {
	phone, ok := a.recoveryPhone(w, r, b)
	if !ok {
		return
	}
	form := &httpx.FormErrors{}
	code, _ := b.str("code")
	switch {
	case !b.filled("code"):
		form.Add("code", a.t(r, msgCodeRequired, nil))
	case !b.isString("code") || !codeRe.MatchString(code):
		form.Add("code", a.t(r, msgCodeFormat, nil))
	}
	pw, _ := b.str("password")
	confirm, _ := b.str("password_confirmation")
	switch {
	case !b.filled("password"):
		form.Add("password", a.t(r, msgPasswordRequired, nil))
	case len([]rune(pw)) < 6:
		form.Add("password", a.t(r, msgPasswordMin, nil))
	case pw != confirm:
		form.Add("password", a.t(r, msgPasswordMismatch, nil))
	}
	if err := form.Err(); err != nil {
		a.writeError(w, r, err)
		return
	}
	if a.o.Recovery == nil {
		writeValidation(w, "phone", a.t(r, msgRecoveryOff, nil))
		return
	}
	userID, err := a.o.Recovery.Reset(r.Context(), phone, code, pw)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.RevokeUser(r.Context(), userID); err != nil {
		a.fail(w, r, "revoke tokens", err)
		return
	}
	writeMessage(w, http.StatusOK, a.t(r, msgPasswordChanged, nil))
}

// RevokeUser — $user->tokens()->delete(): токены приложений аккаунта во всех клубах (сброс пароля).
func (a *API) RevokeUser(ctx context.Context, userID int64) error {
	return RevokeUserTokens(ctx, a.d.Pool, userID, a.d.NowFunc())
}

// RevokeUserTokens — отзыв всех токенов мобильных приложений аккаунта (транзакция с app.user_id, политики
// mobile_token_owner_*). Для выхода везде после сброса пароля (app.passwordResetSignOut).
func RevokeUserTokens(ctx context.Context, pool db.Beginner, userID int64, now time.Time) error {
	if userID <= 0 {
		return nil
	}
	return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.user_id', $1, true)", strconv.FormatInt(userID, 10)); err != nil {
			return err
		}
		_, err := store.New(tx).MobileTokensRevokeUser(ctx, store.MobileTokensRevokeUserParams{Now: &now, UserID: userID})
		return err
	})
}
