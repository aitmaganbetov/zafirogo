// Package password — хэши паролей: Argon2id для новых, проверка старых bcrypt-хэшей Laravel ($2y$)
// и прозрачная перехеш-миграция при успешном входе (Q-051, docs/open-questions/database.md В-6).
//
// Формат Argon2id — стандартная PHC-строка, как у PHP password_hash(PASSWORD_ARGON2ID) и libsodium:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<соль base64 без паддинга>$<хэш base64 без паддинга>
//
// Параметры по умолчанию — минимум OWASP Password Storage Cheat Sheet (2025): m = 19 MiB, t = 2, p = 1.
// Параметры записаны в самой строке: при их увеличении старые хэши проверяются по своим параметрам,
// а NeedsRehash сообщает, что пора перехэшировать.
//
// Защита от перебора на уровне этого пакета:
//   - сравнение в константное время (crypto/subtle);
//   - VerifyDummy — та же работа для несуществующего пользователя (время ответа не выдаёт, есть ли номер);
//   - ограничение числа одновременных вычислений Argon2id (память m на каждое) — поток неверных паролей
//     не съедает память сервера; лишние запросы ждут очереди (или отмены контекста).
//
// Число попыток ограничивает rate limit (auth/ratelimit), не этот пакет.
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// Params — параметры Argon2id.
type Params struct {
	// MemoryKiB — память в КиБ (m).
	MemoryKiB uint32
	// Iterations — число проходов (t).
	Iterations uint32
	// Parallelism — число потоков (p).
	Parallelism uint8
	// SaltLen и KeyLen — длины соли и хэша в байтах.
	SaltLen uint32
	KeyLen  uint32
}

// DefaultParams — OWASP (2025): Argon2id, m = 19 MiB, t = 2, p = 1; соль 16 байт, хэш 32 байта.
var DefaultParams = Params{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLen: 16, KeyLen: 32}

// Ограничения на параметры из чужой строки хэша: не дать испорченной записи в БД занять гигабайты.
const (
	maxMemoryKiB  = 1 << 20 // 1 GiB
	maxIterations = 64
	maxKeyLen     = 128
	minSaltLen    = 8
	// bcryptMaxBytes — bcrypt учитывает только первые 72 байта пароля. PHP password_verify молча
	// отбрасывает остальное (проверено: php -r password_verify(36×«я»."zz", hash(36×«я»)) → true),
	// а Go bcrypt возвращает ErrPasswordTooLong, поэтому старые хэши проверяем по первым 72 байтам.
	bcryptMaxBytes = 72
)

// Ошибки.
var (
	// ErrMismatch — пароль не подходит.
	ErrMismatch = errors.New("password: mismatch")
	// ErrUnknownFormat — строка не похожа ни на argon2id, ни на bcrypt.
	ErrUnknownFormat = errors.New("password: unknown hash format")
	// ErrMalformed — строка argon2id испорчена или параметры вне разумных пределов.
	ErrMalformed = errors.New("password: malformed hash")
)

// Hasher считает и проверяет хэши. Нулевое значение не годится — используйте New.
type Hasher struct {
	params Params
	sem    chan struct{}
	dummy  string
}

// Options — настройки Hasher.
type Options struct {
	// Params — параметры новых хэшей; нулевое значение — DefaultParams.
	Params Params
	// MaxConcurrent — сколько хэшей Argon2id считается одновременно; 0 — 2 × GOMAXPROCS.
	MaxConcurrent int
}

// New создаёт Hasher.
func New(o Options) (*Hasher, error) {
	p := o.Params
	if p == (Params{}) {
		p = DefaultParams
	}
	if p.MemoryKiB < 8*uint32(p.Parallelism) || p.MemoryKiB > maxMemoryKiB || p.Iterations < 1 ||
		p.Iterations > maxIterations || p.Parallelism < 1 || p.SaltLen < minSaltLen || p.KeyLen < 16 || p.KeyLen > maxKeyLen {
		return nil, fmt.Errorf("password: invalid params %+v", p)
	}
	n := o.MaxConcurrent
	if n <= 0 {
		n = 2 * runtime.GOMAXPROCS(0)
	}
	h := &Hasher{params: p, sem: make(chan struct{}, n)}
	// Хэш случайного пароля той же стоимости — для VerifyDummy.
	var junk [24]byte
	if _, err := rand.Read(junk[:]); err != nil {
		return nil, err
	}
	d, err := h.hash(context.Background(), base64.RawStdEncoding.EncodeToString(junk[:]))
	if err != nil {
		return nil, err
	}
	h.dummy = d
	return h, nil
}

// Params возвращает параметры новых хэшей.
func (h *Hasher) Params() Params { return h.params }

// Hash возвращает строку Argon2id для пароля.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	return h.hash(ctx, password)
}

func (h *Hasher) hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, h.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: salt: %w", err)
	}
	key, err := h.argon(ctx, []byte(password), salt, h.params)
	if err != nil {
		return "", err
	}
	return encode(h.params, salt, key), nil
}

// Verify проверяет пароль по строке хэша (argon2id или bcrypt $2y$/$2a$/$2b$).
// Возвращает nil при совпадении, ErrMismatch — при несовпадении, иные ошибки — испорченный хэш.
// needsRehash = true, если хэш надо заменить хэшем Hash (bcrypt или устаревшие параметры argon2id);
// имеет смысл только при err == nil.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (needsRehash bool, err error) {
	switch {
	case strings.HasPrefix(encoded, "$argon2id$"):
		p, salt, key, err := decode(encoded)
		if err != nil {
			return false, err
		}
		got, err := h.argon(ctx, []byte(password), salt, p)
		if err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare(got, key) != 1 {
			return false, ErrMismatch
		}
		return p != h.params, nil
	case isBcrypt(encoded):
		// Laravel: Hash::check() → password_verify() (bcrypt, cost из config/hashing.php, по умолчанию 12).
		pw := []byte(password)
		if len(pw) > bcryptMaxBytes {
			pw = pw[:bcryptMaxBytes]
		}
		if err := h.acquire(ctx); err != nil {
			return false, err
		}
		defer h.release()
		// bcrypt.CompareHashAndPassword сравнивает в константное время (subtle.ConstantTimeCompare).
		switch err := bcrypt.CompareHashAndPassword([]byte(encoded), pw); {
		case err == nil:
			return true, nil
		case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
			return false, ErrMismatch
		default:
			return false, fmt.Errorf("%w: %w", ErrMalformed, err)
		}
	default:
		return false, ErrUnknownFormat
	}
}

// VerifyDummy тратит столько же времени, сколько проверка настоящего хэша, и всегда возвращает ErrMismatch.
// Вызывается, когда пользователя с таким логином нет: по времени ответа нельзя узнать, есть ли номер.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) error {
	if _, err := h.Verify(ctx, password, h.dummy); err != nil && !errors.Is(err, ErrMismatch) {
		return err
	}
	return ErrMismatch
}

// NeedsRehash — нужно ли заменить хэш (не argon2id или параметры отличаются от текущих).
func (h *Hasher) NeedsRehash(encoded string) bool {
	if !strings.HasPrefix(encoded, "$argon2id$") {
		return true
	}
	p, _, _, err := decode(encoded)
	return err != nil || p != h.params
}

// IsLegacy — хэш старого формата (bcrypt из Laravel).
func IsLegacy(encoded string) bool { return isBcrypt(encoded) }

func isBcrypt(s string) bool {
	return len(s) == 60 && (strings.HasPrefix(s, "$2y$") || strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$"))
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.sem }

func (h *Hasher) argon(ctx context.Context, password, salt []byte, p Params) ([]byte, error) {
	if err := h.acquire(ctx); err != nil {
		return nil, err
	}
	defer h.release()
	return argon2.IDKey(password, salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLen), nil
}

func encode(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// decode разбирает $argon2id$v=19$m=…,t=…,p=…$salt$hash.
func decode(s string) (Params, []byte, []byte, error) {
	parts := strings.Split(s, "$")
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrMalformed
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, fmt.Errorf("%w: version %q", ErrMalformed, parts[2])
	}
	var p Params
	kv := strings.Split(parts[3], ",")
	if len(kv) != 3 {
		return Params{}, nil, nil, ErrMalformed
	}
	for i, name := range []string{"m", "t", "p"} {
		k, v, ok := strings.Cut(kv[i], "=")
		if !ok || k != name {
			return Params{}, nil, nil, ErrMalformed
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return Params{}, nil, nil, ErrMalformed
		}
		switch name {
		case "m":
			p.MemoryKiB = uint32(n)
		case "t":
			p.Iterations = uint32(n)
		case "p":
			if n < 1 || n > 255 {
				return Params{}, nil, nil, ErrMalformed
			}
			p.Parallelism = uint8(n)
		}
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, ErrMalformed
	}
	p.SaltLen, p.KeyLen = uint32(len(salt)), uint32(len(key)) // #nosec G115 -- длины ограничены ниже
	if p.MemoryKiB < 8*uint32(p.Parallelism) || p.MemoryKiB > maxMemoryKiB || p.Iterations < 1 || p.Iterations > maxIterations ||
		len(salt) < minSaltLen || len(salt) > 64 || len(key) < 16 || len(key) > maxKeyLen {
		return Params{}, nil, nil, ErrMalformed
	}
	return p, salt, key, nil
}
