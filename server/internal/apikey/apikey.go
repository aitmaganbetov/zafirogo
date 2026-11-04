// Package apikey — ключи интеграционного API /api/v1: формат, хэш, лимиты и счётчики окон.
//
// Laravel: app/Services/Integrations/ApiKeyManager.php (формат и хэш), app/Http/Middleware/Api/ApiRateLimit.php
// (окна по минуте на ключ и на клуб), AuthenticateApiKey.php (неудачные попытки с IP), config/integrations.php.
//
// Формат ключа прежний: zfk_<8 символов [a-z0-9]>_<40 символов [A-Za-z0-9]>; в api_keys хранится префикс
// (zfk_xxxxxxxx) и SHA-256 полного ключа (hex). Перенесённые ключи старых клиентов работают без перевыпуска:
// хэш тот же (hash('sha256', $plain)), поиск — по тому же префиксу.
package apikey

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Prefix — integrations.key_prefix.
const Prefix = "zfk_"

// tokenRe — ApiKeyManager::TOKEN_PATTERN.
var tokenRe = regexp.MustCompile(`^zfk_([a-z0-9]{8})_([A-Za-z0-9]{40})$`)

// KeyPrefix — публичный префикс ключа (zfk_xxxxxxxx) правильного формата; "" — не ключ.
// Laravel: AuthenticateApiKey::prefixOf(), ApiKeyManager::verify().
func KeyPrefix(token string) string {
	m := tokenRe.FindStringSubmatch(token)
	if m == nil {
		return ""
	}
	return Prefix + m[1]
}

// Hash — hash('sha256', $plain) (hex).
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// HashEqual — hash_equals($stored, hash('sha256', $token)) за постоянное время.
func HashEqual(stored, token string) bool {
	return subtle.ConstantTimeCompare([]byte(stored), []byte(Hash(token))) == 1
}

// Limits — config/integrations.php (ключи конфигурации integrations.*, значения по умолчанию — старые).
type Limits struct {
	// KeyDefault — default_rate_limit_per_minute (API_KEY_RATE_LIMIT, 120).
	KeyDefault int
	// KeyMax — max_rate_limit_per_minute (API_KEY_RATE_LIMIT_MAX, 1000).
	KeyMax int
	// Club — club_rate_limit_per_minute (API_CLUB_RATE_LIMIT, 600).
	Club int
	// FailedAuthPerMinute — failed_auth_per_minute_per_ip (20).
	FailedAuthPerMinute int
	// MaxRequestBytes — max_request_kb × 1024 (256 КБ).
	MaxRequestBytes int64
	// MaxBulkItems — max_bulk_items (50).
	MaxBulkItems int
	// IdempotencyRetention — idempotency_retention_hours (24 ч).
	IdempotencyRetention time.Duration
}

// DefaultLimits — значения config/integrations.php старого проекта.
func DefaultLimits() Limits {
	return Limits{KeyDefault: 120, KeyMax: 1000, Club: 600, FailedAuthPerMinute: 20, MaxRequestBytes: 256 * 1024,
		MaxBulkItems: 50, IdempotencyRetention: 24 * time.Hour}
}

// WithDefaults — нулевые поля заменены значениями по умолчанию.
func (l Limits) WithDefaults() Limits {
	d := DefaultLimits()
	if l.KeyDefault <= 0 {
		l.KeyDefault = d.KeyDefault
	}
	if l.KeyMax <= 0 {
		l.KeyMax = d.KeyMax
	}
	if l.Club <= 0 {
		l.Club = d.Club
	}
	if l.FailedAuthPerMinute <= 0 {
		l.FailedAuthPerMinute = d.FailedAuthPerMinute
	}
	if l.MaxRequestBytes <= 0 {
		l.MaxRequestBytes = d.MaxRequestBytes
	}
	if l.MaxBulkItems <= 0 {
		l.MaxBulkItems = d.MaxBulkItems
	}
	if l.IdempotencyRetention <= 0 {
		l.IdempotencyRetention = d.IdempotencyRetention
	}
	return l
}

// KeyLimit — min($key['rate_limit_per_minute'] ?? default, max).
func (l Limits) KeyLimit(perKey *int32) int {
	n := l.KeyDefault
	if perKey != nil {
		n = int(*perKey)
	}
	return min(n, l.KeyMax)
}

// Counter — атомарные счётчики с временем жизни (Cache::increment / Cache::add старого кода).
type Counter interface {
	// Incr увеличивает счётчик key на 1 и возвращает новое значение; новый счётчик живёт ttl.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	// Get — текущее значение (0 — нет).
	Get(ctx context.Context, key string) (int64, error)
	// TTL — сколько ещё живёт счётчик (0 — нет).
	TTL(ctx context.Context, key string) (time.Duration, error)
	// SetNX ставит флаг key на ttl; false — уже стоял (Cache::add).
	SetNX(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// ---------------------------------------------------------------------------
// Valkey
// ---------------------------------------------------------------------------

// incrScript — INCR и срок жизни при создании (одна атомарная операция на окно, как ApiRateLimit::hit()).
var incrScript = valkey.NewLuaScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return n
`)

// ValkeyCounter — счётчики в Valkey (общие для всех инстансов).
type ValkeyCounter struct {
	Client valkey.Client
	// Prefix ключей (по умолчанию "apiv1:").
	Prefix string
}

func (c ValkeyCounter) key(k string) string {
	if c.Prefix == "" {
		return "apiv1:" + k
	}
	return c.Prefix + k
}

// Incr — Counter.
func (c ValkeyCounter) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return incrScript.Exec(ctx, c.Client, []string{c.key(key)}, []string{strconv.FormatInt(ttl.Milliseconds(), 10)}).AsInt64()
}

// Get — Counter.
func (c ValkeyCounter) Get(ctx context.Context, key string) (int64, error) {
	n, err := c.Client.Do(ctx, c.Client.B().Get().Key(c.key(key)).Build()).AsInt64()
	if valkey.IsValkeyNil(err) {
		return 0, nil
	}
	return n, err
}

// TTL — Counter.
func (c ValkeyCounter) TTL(ctx context.Context, key string) (time.Duration, error) {
	ms, err := c.Client.Do(ctx, c.Client.B().Pttl().Key(c.key(key)).Build()).AsInt64()
	if err != nil || ms < 0 {
		return 0, err
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// SetNX — Counter.
func (c ValkeyCounter) SetNX(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	err := c.Client.Do(ctx, c.Client.B().Set().Key(c.key(key)).Value("1").Nx().Px(ttl).Build()).Error()
	if valkey.IsValkeyNil(err) {
		return false, nil
	}
	return err == nil, err
}

// ---------------------------------------------------------------------------
// В памяти (один инстанс: dev без Valkey, тесты)
// ---------------------------------------------------------------------------

// MemoryCounter — счётчики в памяти процесса.
type MemoryCounter struct {
	// Now — часы (nil — time.Now).
	Now func() time.Time
	mu  sync.Mutex
	m   map[string]memEntry
}

type memEntry struct {
	n       int64
	expires time.Time
}

func (c *MemoryCounter) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *MemoryCounter) live(key string) (memEntry, bool) {
	e, ok := c.m[key]
	if ok && !c.now().Before(e.expires) {
		delete(c.m, key)
		return memEntry{}, false
	}
	return e, ok
}

// Incr — Counter.
func (c *MemoryCounter) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]memEntry{}
	}
	e, ok := c.live(key)
	if !ok {
		e = memEntry{expires: c.now().Add(ttl)}
	}
	e.n++
	c.m[key] = e
	return e.n, nil
}

// Get — Counter.
func (c *MemoryCounter) Get(_ context.Context, key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, _ := c.live(key)
	return e.n, nil
}

// TTL — Counter.
func (c *MemoryCounter) TTL(_ context.Context, key string) (time.Duration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.live(key)
	if !ok {
		return 0, nil
	}
	return e.expires.Sub(c.now()), nil
}

// SetNX — Counter.
func (c *MemoryCounter) SetNX(_ context.Context, key string, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]memEntry{}
	}
	if _, ok := c.live(key); ok {
		return false, nil
	}
	c.m[key] = memEntry{n: 1, expires: c.now().Add(ttl)}
	return true, nil
}
