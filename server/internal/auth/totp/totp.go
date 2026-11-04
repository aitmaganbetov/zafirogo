// Package totp — одноразовые коды по времени (RFC 6238 поверх HOTP RFC 4226) и коды восстановления для второго
// фактора суперадмина (Этап 7, docs/open-questions/stage7-totp.md). Только stdlib: HMAC-SHA1, 6 цифр, шаг 30 с,
// окно ±1 шаг. Повтор кода отсекает вызывающий: Verify принимает только шаги больше последнего принятого.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6238/4226: HMAC-SHA1 — алгоритм приложений-аутентификаторов, не хэш паролей
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Параметры TOTP (значения по умолчанию всех приложений-аутентификаторов: Google Authenticator, Aegis, 1Password…).
const (
	Digits = 6
	// Period — шаг времени, секунды.
	Period = 30
	// Skew — сколько шагов до и после текущего принимается (рассинхронизация часов телефона).
	Skew = 1
	// SecretBytes — длина ключа (160 бит, RFC 4226 §4 рекомендует ≥ 128, SHA-1 — 160).
	SecretBytes = 20
)

// ErrSecret — ключ не base32 или пустой.
var ErrSecret = errors.New("totp: invalid secret")

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret — случайный ключ в base32 без '=' (как показывают приложения: 32 символа A–Z2–7).
func NewSecret() (string, error) {
	b := make([]byte, SecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// DecodeSecret — ключ из base32 (регистр, пробелы и '=' не важны).
func DecodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '=' {
			return -1
		}
		return r
	}, secret))
	key, err := b32.DecodeString(s)
	if err != nil || len(key) == 0 {
		return nil, ErrSecret
	}
	return key, nil
}

// Step — номер шага для момента t (RFC 6238: T = (unix − T0) / X, T0 = 0, X = 30).
func Step(t time.Time) int64 { return t.Unix() / Period }

// HOTP — код RFC 4226 §5.3 из digits цифр (динамическое усечение HMAC-SHA1 счётчика).
func HOTP(key []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	s := strconv.FormatUint(uint64(bin%mod), 10)
	return strings.Repeat("0", digits-len(s)) + s
}

// Code — код TOTP шага step (6 цифр).
func Code(key []byte, step int64) string {
	if step < 0 {
		return ""
	}
	return HOTP(key, uint64(step), Digits)
}

// NormalizeCode — цифры кода без пробелов и дефисов («123 456» → «123456»).
func NormalizeCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, code)
}

// IsCode — похоже на код TOTP: ровно Digits цифр (иначе это код восстановления).
func IsCode(code string) bool {
	if len(code) != Digits {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Verify проверяет код на шагах now−Skew…now+Skew, которые больше lastStep (повтор принятого шага — отказ).
// Возвращает совпавший шаг. Сравнение — за постоянное время, все шаги окна проверяются всегда.
func Verify(key []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	code = NormalizeCode(code)
	if !IsCode(code) {
		return 0, false
	}
	cur := Step(now)
	var matched int64
	ok := false
	for s := cur - Skew; s <= cur+Skew; s++ {
		eq := subtle.ConstantTimeCompare([]byte(Code(key, s)), []byte(code)) == 1
		if eq && s > lastStep && !ok {
			matched, ok = s, true
		}
	}
	return matched, ok
}

// URI — ссылка otpauth:// для приложения-аутентификатора (формат Key Uri Format Google Authenticator):
// otpauth://totp/<issuer>:<account>?secret=…&issuer=…&algorithm=SHA1&digits=6&period=30.
func URI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(Digits))
	q.Set("period", strconv.Itoa(Period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// ---------------------------------------------------------------------------
// Коды восстановления
// ---------------------------------------------------------------------------

// RecoveryCount — сколько кодов восстановления выдаётся.
const RecoveryCount = 10

// recoveryAlphabet — base32 Крокфорда в нижнем регистре: без i, l, o, u (не путаются с 1, 0 и v).
const recoveryAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// recoveryLen — символов в коде: 16 × 5 бит = 80 бит случайности (перебор по sha256 из базы невозможен).
const recoveryLen = 16

// NewRecoveryCodes — n кодов вида xxxx-xxxx-xxxx-xxxx.
func NewRecoveryCodes(n int) ([]string, error) {
	out := make([]string, 0, n)
	buf := make([]byte, recoveryLen)
	for range n {
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		var b strings.Builder
		for i, c := range buf {
			if i > 0 && i%4 == 0 {
				b.WriteByte('-')
			}
			b.WriteByte(recoveryAlphabet[c&31])
		}
		out = append(out, b.String())
	}
	return out, nil
}

// NormalizeRecovery — код восстановления без регистра, пробелов и дефисов; o → 0, i и l → 1 (Крокфорд).
func NormalizeRecovery(code string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(code) {
		switch {
		case r == 'o':
			b.WriteByte('0')
		case r == 'i' || r == 'l':
			b.WriteByte('1')
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// IsRecovery — после нормализации — ровно 16 символов алфавита.
func IsRecovery(code string) bool {
	n := NormalizeRecovery(code)
	if len(n) != recoveryLen {
		return false
	}
	for _, r := range n {
		if !strings.ContainsRune(recoveryAlphabet, r) {
			return false
		}
	}
	return true
}

// HashRecovery — sha256 кода, привязанный к пользователю (одинаковый код у двух суперадминов — разные хэши).
func HashRecovery(userID int64, code string) string {
	h := sha256.Sum256([]byte("zafiro.recovery.v1:" + strconv.FormatInt(userID, 10) + ":" + NormalizeRecovery(code)))
	return hex.EncodeToString(h[:])
}
