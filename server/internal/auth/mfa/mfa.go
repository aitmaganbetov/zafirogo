// Package mfa — второй фактор входа суперадмина (Этап 7, docs/open-questions/stage7-totp.md; в старом Laravel его
// нет — новая функция по поручению владельца, снимает SEC-010).
//
// Вход суперадмина идёт в два шага. После пароля сервис входа не выдаёт сессию, а начинает «полувход» (Begin):
// одноразовый токен на ChallengeTTL (≈5 мин) в Valkey, привязанный к пользователю (там же клуб и «запомнить меня» для
// будущей сессии). Сессия и refresh-cookie выдаются только после верного кода (Verify). Если TOTP ещё не настроен —
// принудительная настройка: Setup выдаёт новый ключ, первый верный код включает TOTP и выдаёт 10 кодов
// восстановления (показываются один раз, хранятся sha256).
//
// Ключ TOTP хранится только зашифрованным связкой security.data_keys (secretbox v2, контекст привязан к
// пользователю) — не в открытом виде, не в логах, не в журнале и не в Valkey (полувход хранит только пользователя).
// Повтор кода: принимается только шаг больше последнего принятого (superadmin_totp.last_step). Перебор: правило
// ratelimit superadmin-totp (аккаунт и IP) и не больше MaxAttempts попыток на один полувход. Неудачи, коды
// восстановления и включение — в журнал платформы (platform_logs).
//
// Таблицы — только пул zafiro_platform (0029_superadmin_totp.sql).
package mfa

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/totp"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Ошибки.
var (
	// ErrChallenge — полувхода нет: истёк, уже использован, исчерпаны попытки или второй фактор сброшен.
	// Нужно войти заново с паролем.
	ErrChallenge = errors.New("mfa: sign-in challenge expired or used")
	// ErrInvalidCode — код неверный (или уже использован).
	ErrInvalidCode = errors.New("mfa: invalid code")
	// ErrConfigured — настройка не нужна: TOTP уже включён.
	ErrConfigured = errors.New("mfa: second factor is already configured")
	// ErrSecretUnreadable — ключ TOTP не расшифровывается ни одним ключом связки (ключ данных потерян): нужен сброс.
	ErrSecretUnreadable = errors.New("mfa: totp secret cannot be decrypted")
)

// LimitError — превышен лимит попыток (ratelimit superadmin-totp).
type LimitError struct{ RetryAfter time.Duration }

func (e *LimitError) Error() string { return "mfa: too many attempts" }

// Issuer — имя в приложении-аутентификаторе.
const Issuer = "Zafiro"

// Тексты журнала платформы (ключи перевода, подстановки — :user).
const (
	EventEnabled      = "superadmin.totp_enabled"
	EventFailed       = "superadmin.totp_failed"
	EventRecoveryUsed = "superadmin.totp_recovery_used"
	EventReset        = "superadmin.totp_reset"

	msgEnabled      = "Включена двухфакторная аутентификация: :user"
	msgFailed       = "Неверный код двухфакторной аутентификации: :user"
	msgRecoveryUsed = "Вход по коду восстановления: :user"
	// MsgReset — сброс другим суперадмином или командой сервера.
	MsgReset = "Сброшена двухфакторная аутентификация: :user"
)

// SecretContext — контекст шифрования ключа TOTP: шифротекст нельзя перенести другому пользователю.
func SecretContext(userID int64) string {
	return "superadmin_totp.secret:user=" + strconv.FormatInt(userID, 10)
}

// Options — зависимости Manager.
type Options struct {
	// Platform — пул zafiro_platform.
	Platform db.Beginner
	Valkey   valkey.Client
	// Box — связка ключей данных (security.data_keys).
	Box *secretbox.Box
	// Limiter — лимиты; правило ratelimit.SuperadminTOTP.
	Limiter *ratelimit.Limiter
	// ChallengeTTL — срок полувхода; по умолчанию 5 мин.
	ChallengeTTL time.Duration
	// MaxAttempts — попыток кода на один полувход; по умолчанию 5.
	MaxAttempts int
	// AllowReplay — принимать повтор шага TOTP (только dev/test: стенд визуального сравнения входит суперадмином на
	// каждый снимок; config dev.totp_allow_replay, в prod запрещено). Окно ±1 шаг и сам код проверяются всегда.
	AllowReplay bool
	// Prefix — префикс ключей Valkey; по умолчанию "auth:mfa:".
	Prefix string
	// Now — часы; по умолчанию time.Now (TOTP — по настоящим часам, не по dev.fixed_now).
	Now    func() time.Time
	Logger *slog.Logger
}

// Manager — полувход, настройка и проверка второго фактора.
type Manager struct{ o Options }

// New создаёт Manager.
func New(o Options) (*Manager, error) {
	if o.Platform == nil || o.Valkey == nil || o.Box == nil || o.Limiter == nil {
		return nil, errors.New("mfa: platform pool, valkey, keyring and limiter are required")
	}
	if !o.Limiter.Has(ratelimit.SuperadminTOTP) {
		return nil, errors.New("mfa: rate limit rule " + ratelimit.SuperadminTOTP + " is missing")
	}
	if o.ChallengeTTL <= 0 {
		o.ChallengeTTL = 5 * time.Minute
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if o.Prefix == "" {
		o.Prefix = "auth:mfa:"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Manager{o: o}, nil
}

// Pending — вход, ожидающий второго фактора: из него сервис входа создаёт сессию после верного кода.
type Pending struct {
	UserID   int64
	ClubID   int64
	Remember bool
	// Name и Phone — для журнала и подписи в приложении-аутентификаторе.
	Name  string
	Phone string
}

// Challenge — начатый полувход. Token — секрет (только в ответе входа, не в логах; в Valkey — sha256).
type Challenge struct {
	Token         string
	SetupRequired bool
	ExpiresIn     time.Duration
}

func (m *Manager) key(token string) string {
	h := sha256.Sum256([]byte(token))
	return m.o.Prefix + "c:" + hex.EncodeToString(h[:])
}

// Begin начинает полувход после верного пароля.
func (m *Manager) Begin(ctx context.Context, p Pending) (Challenge, error) {
	if p.UserID <= 0 {
		return Challenge{}, errors.New("mfa: user id is required")
	}
	var confirmed bool
	if err := db.InPlatformTx(ctx, m.o.Platform, func(q *store.Queries) error {
		var err error
		confirmed, err = q.SuperadminTotpConfirmed(ctx, p.UserID)
		return err
	}); err != nil {
		return Challenge{}, fmt.Errorf("mfa: state: %w", err)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Challenge{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	rem, setup := "0", "0"
	if p.Remember {
		rem = "1"
	}
	if !confirmed {
		setup = "1"
	}
	k := m.key(token)
	c := m.o.Valkey
	for _, r := range c.DoMulti(ctx,
		c.B().Hset().Key(k).FieldValue().
			FieldValue("uid", strconv.FormatInt(p.UserID, 10)).
			FieldValue("club", strconv.FormatInt(p.ClubID, 10)).
			FieldValue("remember", rem).
			FieldValue("setup", setup).
			FieldValue("name", p.Name).
			FieldValue("phone", p.Phone).
			FieldValue("attempts", "0").Build(),
		c.B().Pexpire().Key(k).Milliseconds(m.o.ChallengeTTL.Milliseconds()).Build(),
	) {
		if err := r.Error(); err != nil {
			return Challenge{}, fmt.Errorf("mfa: challenge: %w", err)
		}
	}
	return Challenge{Token: token, SetupRequired: !confirmed, ExpiresIn: m.o.ChallengeTTL}, nil
}

type challenge struct {
	Pending
	token string
	setup bool
}

func (m *Manager) load(ctx context.Context, token string) (challenge, error) {
	if token == "" || len(token) > 128 {
		return challenge{}, ErrChallenge
	}
	h, err := m.o.Valkey.Do(ctx, m.o.Valkey.B().Hgetall().Key(m.key(token)).Build()).AsStrMap()
	if err != nil {
		return challenge{}, fmt.Errorf("mfa: challenge: %w", err)
	}
	uid, _ := strconv.ParseInt(h["uid"], 10, 64)
	if uid <= 0 {
		return challenge{}, ErrChallenge
	}
	club, _ := strconv.ParseInt(h["club"], 10, 64)
	return challenge{
		Pending: Pending{UserID: uid, ClubID: club, Remember: h["remember"] == "1", Name: h["name"], Phone: h["phone"]},
		token:   token,
		setup:   h["setup"] == "1",
	}, nil
}

// Setup — новый ключ TOTP для принудительной настройки (только пока TOTP не включён). Повторный вызов заменяет
// ключ (например, первый не отсканировали). Возвращает ключ base32 и ссылку otpauth:// — показать один раз.
type Setup struct {
	Secret string
	URI    string
}

// Setup выдаёт ключ для настройки по полувходу.
func (m *Manager) Setup(ctx context.Context, token string) (Setup, error) {
	ch, err := m.load(ctx, token)
	if err != nil {
		return Setup{}, err
	}
	if !ch.setup {
		return Setup{}, ErrConfigured
	}
	secret, err := totp.NewSecret()
	if err != nil {
		return Setup{}, err
	}
	sealed, err := m.o.Box.SealWith(secret, SecretContext(ch.UserID))
	if err != nil {
		return Setup{}, fmt.Errorf("mfa: seal: %w", err)
	}
	now := m.o.Now()
	var n int64
	if err := db.InPlatformTx(ctx, m.o.Platform, func(q *store.Queries) error {
		n, err = q.SuperadminTotpBegin(ctx, store.SuperadminTotpBeginParams{UserID: ch.UserID, Secret: sealed, Now: now})
		return err
	}); err != nil {
		return Setup{}, fmt.Errorf("mfa: setup: %w", err)
	}
	if n == 0 {
		// Тем временем TOTP включили (другая вкладка): настраивать нечего.
		return Setup{}, ErrConfigured
	}
	account := ch.Phone
	if account == "" {
		account = strconv.FormatInt(ch.UserID, 10)
	}
	return Setup{Secret: secret, URI: totp.URI(Issuer, account, secret)}, nil
}

// Result — итог верного кода.
type Result struct {
	Pending
	// RecoveryCodes — коды восстановления, выданные при включении TOTP (показать один раз); иначе пусто.
	RecoveryCodes []string
	// RecoveryUsed — вход по коду восстановления; RecoveryLeft — сколько неиспользованных осталось.
	RecoveryUsed bool
	RecoveryLeft int64
}

// attemptScript: KEYS[1] — полувход; ARGV[1] — максимум попыток. Ответ: номер попытки или 0 (полувхода нет;
// при превышении он удаляется).
var attemptScript = valkey.NewLuaScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
local n = redis.call('HINCRBY', KEYS[1], 'attempts', 1)
if n > tonumber(ARGV[1]) then
  redis.call('DEL', KEYS[1])
  return 0
end
return n
`)

// Verify проверяет код TOTP или код восстановления. Верный код завершает полувход (одноразово): дальше сервис
// входа создаёт сессию. Неверный — ErrInvalidCode (полувход жив до MaxAttempts попыток).
func (m *Manager) Verify(ctx context.Context, token, code string) (Result, error) {
	ch, err := m.load(ctx, token)
	if err != nil {
		return Result{}, err
	}
	ip := ""
	if meta, ok := activity.RequestMetaFromContext(ctx); ok {
		ip = meta.IP
	}
	lim, err := m.o.Limiter.Allow(ctx, ratelimit.SuperadminTOTP, ratelimit.Subject{IP: ip, UserID: ch.UserID})
	if err != nil {
		return Result{}, err
	}
	if !lim.Allowed {
		return Result{}, &LimitError{RetryAfter: lim.RetryAfter}
	}
	n, err := attemptScript.Exec(ctx, m.o.Valkey, []string{m.key(token)}, []string{strconv.Itoa(m.o.MaxAttempts)}).AsInt64()
	if err != nil {
		return Result{}, fmt.Errorf("mfa: attempt: %w", err)
	}
	if n == 0 {
		return Result{}, ErrChallenge
	}

	now := m.o.Now()
	res := Result{Pending: ch.Pending}
	var verr error // итог проверки кода; ошибка транзакции — отдельно
	err = db.InPlatformTx(ctx, m.o.Platform, func(q *store.Queries) error {
		res.RecoveryCodes, res.RecoveryUsed, res.RecoveryLeft, verr = nil, false, 0, nil
		row, err := q.SuperadminTotpLock(ctx, ch.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			if ch.setup {
				verr = ErrInvalidCode // ключ ещё не выдан (Setup не вызывали)
				return nil
			}
			verr = ErrChallenge // второй фактор сбросили после пароля
			return nil
		}
		if err != nil {
			return err
		}
		if ch.setup != (row.ConfirmedAt == nil) {
			// Настройку завершили в другой вкладке или второй фактор сбросили: начать вход заново.
			verr = ErrChallenge
			return nil
		}
		if ch.setup {
			return m.confirm(ctx, q, ch, row, code, now, &res, &verr)
		}
		return m.check(ctx, q, ch, row, code, now, &res, &verr)
	})
	if err != nil {
		if errors.Is(err, ErrChallenge) {
			return Result{}, ErrChallenge
		}
		return Result{}, err
	}
	if verr != nil {
		return Result{}, verr
	}
	return res, nil
}

// consume — полувход одноразовый: удаляется в транзакции проверки до коммита; если его уже нет (параллельный
// верный код), транзакция откатывается.
func (m *Manager) consume(ctx context.Context, ch challenge) error {
	n, err := m.o.Valkey.Do(ctx, m.o.Valkey.B().Del().Key(m.key(ch.token)).Build()).AsInt64()
	if err != nil {
		return fmt.Errorf("mfa: consume: %w", err)
	}
	if n == 0 {
		return ErrChallenge
	}
	return nil
}

func (m *Manager) open(row store.SuperadminTotpLockRow) ([]byte, error) {
	secret, err := m.o.Box.OpenWith(row.Secret, SecretContext(row.UserID))
	if err != nil {
		return nil, ErrSecretUnreadable
	}
	return totp.DecodeSecret(secret)
}

// confirm — первый код после Setup: TOTP включается, выдаются коды восстановления.
func (m *Manager) confirm(ctx context.Context, q *store.Queries, ch challenge, row store.SuperadminTotpLockRow, code string,
	now time.Time, res *Result, verr *error) error {
	key, err := m.open(row)
	if err != nil {
		return err
	}
	step, ok := totp.Verify(key, code, now, row.LastStep)
	if !ok {
		*verr = ErrInvalidCode
		return m.logFailed(ctx, q, ch, now)
	}
	codes, err := totp.NewRecoveryCodes(totp.RecoveryCount)
	if err != nil {
		return err
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = totp.HashRecovery(ch.UserID, c)
	}
	if err := q.SuperadminTotpConfirm(ctx, store.SuperadminTotpConfirmParams{UserID: ch.UserID, Step: step, Now: &now}); err != nil {
		return err
	}
	if err := q.SuperadminRecoveryDeleteAll(ctx, ch.UserID); err != nil {
		return err
	}
	if err := q.SuperadminRecoveryInsert(ctx, store.SuperadminRecoveryInsertParams{UserID: ch.UserID, CodeHashes: hashes, Now: now}); err != nil {
		return err
	}
	if err := m.log(ctx, q, ch, EventEnabled, msgEnabled, nil, now); err != nil {
		return err
	}
	if err := m.consume(ctx, ch); err != nil {
		return err
	}
	res.RecoveryCodes, res.RecoveryLeft = codes, int64(len(codes))
	return nil
}

// check — вход с настроенным TOTP: код приложения (6 цифр) или код восстановления.
func (m *Manager) check(ctx context.Context, q *store.Queries, ch challenge, row store.SuperadminTotpLockRow, code string,
	now time.Time, res *Result, verr *error) error {
	code = totp.NormalizeCode(code)
	if totp.IsCode(code) {
		key, err := m.open(row)
		if err != nil {
			return err
		}
		last := row.LastStep
		if m.o.AllowReplay {
			last = -1
		}
		step, ok := totp.Verify(key, code, now, last)
		if ok && step > row.LastStep {
			var n int64
			if n, err = q.SuperadminTotpAccept(ctx, store.SuperadminTotpAcceptParams{UserID: ch.UserID, Step: step, Now: now}); err != nil {
				return err
			}
			ok = n == 1
		}
		if !ok {
			*verr = ErrInvalidCode
			return m.logFailed(ctx, q, ch, now)
		}
		return m.consume(ctx, ch)
	}
	if !totp.IsRecovery(code) {
		*verr = ErrInvalidCode
		return m.logFailed(ctx, q, ch, now)
	}
	n, err := q.SuperadminRecoveryUse(ctx, store.SuperadminRecoveryUseParams{UserID: ch.UserID, CodeHash: totp.HashRecovery(ch.UserID, code), Now: &now})
	if err != nil {
		return err
	}
	if n != 1 {
		*verr = ErrInvalidCode
		return m.logFailed(ctx, q, ch, now)
	}
	left, err := q.SuperadminRecoveryLeft(ctx, ch.UserID)
	if err != nil {
		return err
	}
	if err := m.log(ctx, q, ch, EventRecoveryUsed, msgRecoveryUsed, map[string]any{"recovery_codes_left": left}, now); err != nil {
		return err
	}
	if err := m.consume(ctx, ch); err != nil {
		return err
	}
	res.RecoveryUsed, res.RecoveryLeft = true, left
	return nil
}

func (m *Manager) logFailed(ctx context.Context, q *store.Queries, ch challenge, now time.Time) error {
	return m.log(ctx, q, ch, EventFailed, msgFailed, nil, now)
}

// log — запись журнала платформы о самом суперадмине (код и ключ туда не попадают).
func (m *Manager) log(ctx context.Context, q *store.Queries, ch challenge, event, key string, details map[string]any, now time.Time) error {
	_, err := activity.RecordPlatform(ctx, q, activity.PlatformEntry{
		Event: event, Key: key, Params: map[string]string{"user": ch.Name},
		UserID: ch.UserID, SubjectType: SubjectUser, SubjectID: ch.UserID, Details: details, At: now,
	})
	return err
}

// SubjectUser — морф-класс пользователя в журнале платформы (как в кабинете суперадмина).
const SubjectUser = `App\Models\User`

// Reset снимает второй фактор пользователя (ключ и коды восстановления) в транзакции q пула zafiro_platform.
// Следующий вход — снова принудительная настройка. had — был ли ключ (настроенный или начатый).
func Reset(ctx context.Context, q *store.Queries, userID int64) (had bool, err error) {
	n, err := q.SuperadminTotpDelete(ctx, userID)
	if err != nil {
		return false, err
	}
	if err := q.SuperadminRecoveryDeleteAll(ctx, userID); err != nil {
		return false, err
	}
	return n > 0, nil
}
