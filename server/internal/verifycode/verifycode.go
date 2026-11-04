// Package verifycode — коды подтверждения в Telegram (Telegram Gateway): восстановление пароля (сайт, агент ПК)
// и подтверждение перевода баланса другу (мобильное приложение / SocialService.RequestTransferCode).
// Коды стоят денег, поэтому ограничены на аккаунт: новый — не раньше чем через 60 с, не больше 5 в час; код
// действует 5 минут, 5 попыток ввода. Хранится только хэш кода (Argon2id; перенесённые bcrypt тоже проверяются).
//
// Laravel: app/Services/PasswordRecovery.php, app/Services/FriendTransferCodes.php (BR-092, BR-103).
//
// Отличие от старого (принцип 4 DECISIONS.md): отправки кода одного аккаунта идут по очереди (блокировка строки
// users), а запись кода резервируется до отправки и снимается, если Telegram код не принял, — два параллельных
// запроса не обходят лимит «1 в минуту / 5 в час».
package verifycode

import (
	"context"
	"crypto/rand"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

// Константы PasswordRecovery / FriendTransferCodes (одинаковые).
const (
	CodeMinutes   = 5  // CODE_MINUTES
	ResendSeconds = 60 // RESEND_SECONDS
	CodesPerHour  = 5  // CODES_PER_HOUR
	MaxAttempts   = 5  // MAX_ATTEMPTS

	CodeTTL     = CodeMinutes * time.Minute
	ResendAfter = ResendSeconds * time.Second
)

// Тексты старого кода (ключи перевода).
const (
	MsgRecoveryUnavailable = "Восстановление пароля через Telegram пока не подключено. Обратитесь к администратору клуба."
	MsgTransferUnavailable = "Подтверждение кодом из Telegram пока не подключено."
	MsgWait                = "Новый код можно запросить через :seconds с."
	MsgHourly              = "Слишком много запросов кода. Попробуйте через час."
	MsgNotSent             = "Не удалось отправить код в Telegram. Проверьте, что на этот номер зарегистрирован Telegram, или обратитесь к администратору клуба."
	MsgCodeWrong           = "Код неверный или устарел. Запросите новый код."
	// MsgCodeSent — PasswordRecoveryController: ответ на запрос кода (одинаковый для любого номера).
	MsgCodeSent = "Если номер зарегистрирован в Zafiro, код придёт в Telegram на этот номер. Код действует :minutes мин."
	// MsgTransferCodeSent — GuestFriendController::transferCode().
	MsgTransferCodeSent = "Код для подтверждения перевода отправлен в Telegram. Код действует :minutes мин."
	// MsgPasswordChanged — ответ после смены пароля по коду.
	MsgPasswordChanged = "Пароль изменён. Войдите с новым паролем." // #nosec G101 -- текст для пользователя, не секрет
)

// Kind — вид ошибки (для статуса gRPC; в REST все — 422 с полем).
type Kind int

// Виды ошибок.
const (
	KindUnavailable Kind = iota + 1 // Gateway не настроен
	KindWait                        // новый код раньше чем через 60 с
	KindHourly                      // 5 кодов за час
	KindNotSent                     // Telegram не принял номер
	KindCodeWrong                   // неверный код (попытка засчитана)
	KindCodeExpired                 // кода нет, истёк или попытки кончились
)

// Error — ошибка для пользователя (ValidationException старого кода): поле, ключ перевода, подстановки.
type Error struct {
	Kind   Kind
	Field  string
	Key    string
	Params map[string]string
	// RetryAfter — через сколько можно повторить (KindWait, KindHourly).
	RetryAfter time.Duration
}

func (e *Error) Error() string { return i18n.Render(i18n.SourceLocale, e.Key, e.Params) }

// Sender — отправка кода (*telegram.Gateway).
type Sender interface {
	Configured(ctx context.Context) (bool, error)
	SendCode(ctx context.Context, phone, code string, ttl time.Duration) (bool, error)
}

// Hasher — хэш кода и пароля (*password.Hasher).
type Hasher interface {
	Hash(ctx context.Context, plain string) (string, error)
	Verify(ctx context.Context, plain, encoded string) (bool, error)
}

// newCode — random_int(100000, 999999).
func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(n.Int64()+100000, 10), nil
}

// limits — общая проверка лимитов по последнему коду и кодам за час (send() обоих сервисов).
func limits(field string, now, lastAt time.Time, inHour int64, firstInHour time.Time) *Error {
	if lastAt.After(now.Add(-ResendAfter)) {
		// RESEND_SECONDS - (int) $last->created_at->diffInSeconds(now()), не меньше 1.
		seconds := ResendSeconds - int64(now.Sub(lastAt)/time.Second)
		seconds = max(1, seconds)
		return &Error{Kind: KindWait, Field: field, Key: MsgWait, Params: map[string]string{"seconds": strconv.FormatInt(seconds, 10)},
			RetryAfter: time.Duration(seconds) * time.Second}
	}
	if inHour >= CodesPerHour {
		retry := firstInHour.Add(time.Hour).Sub(now)
		if retry < time.Second {
			retry = time.Second
		}
		return &Error{Kind: KindHourly, Field: field, Key: MsgHourly, RetryAfter: retry}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// NormalizePhone — PhoneNumber::normalize() (app/Support/PhoneNumber.php): "+7 700 123-45-67", "8 700…", "700…" →
// "+77001234567"; иначе — цифры после +7 (не пройдут проверку PhoneRe). Пусто — "".
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
	if len(d) == 11 && (d[0] == '7' || d[0] == '8') {
		d = d[1:]
	}
	return "+7" + d
}

// PhoneRe — regex:/^\+7\d{10}$/ контроллеров восстановления.
var PhoneRe = regexp.MustCompile(`^\+7\d{10}$`)

// CodeRe — regex:/^\d{6}$/.
var CodeRe = regexp.MustCompile(`^\d{6}$`)
