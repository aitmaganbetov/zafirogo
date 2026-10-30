// Package ratelimit — именованные лимиты запросов в Valkey, как RateLimiter::for / throttle: в Laravel.
//
// Алгоритм — скользящее окно (журнал меток времени в sorted set), атомарно одним Lua-скриптом:
// «не больше N запросов за любые M минут». Выбор вместо GCRA: GCRA с всплеском N пропускает до ~2N
// запросов за окно, фиксированное окно Laravel — до 2N на стыке окон; для подбора пароля и кодов нужна
// строгая граница. Время — TIME сервера Valkey (одинаково для всех инстансов, без рассинхрона часов).
// Отклонённые запросы не считаются (как ThrottleRequests: tooManyAttempts → исключение без hit).
//
// Ключи в Valkey: rl:<правило>:<номер лимита>:<sha256(ключ)> — без телефонов и IP в открытом виде.
//
// При недоступном Valkey запрос отклоняется (503), как в старом при упавшем кэше (исключение → 500);
// Options.FailOpen пропускает такие запросы.
package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// TooManyAttemptsMessage — текст 429. Laravel: ThrottleRequests::buildException() → ThrottleRequestsException('Too Many Attempts.').
const TooManyAttemptsMessage = "Too Many Attempts."

// slidingWindow: KEYS[1] — счётчик; ARGV: окно (мс), максимум, уникальная метка.
// Ответ: {разрешено 1|0, осталось, через сколько мс можно повторить}.
var slidingWindow = valkey.NewLuaScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local window = tonumber(ARGV[1])
local max = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - window)
local count = redis.call('ZCARD', KEYS[1])
if count < max then
  redis.call('ZADD', KEYS[1], now, now .. ':' .. ARGV[3])
  redis.call('PEXPIRE', KEYS[1], window)
  return {1, max - count - 1, 0}
end
local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
local retry = window
if oldest[2] then retry = tonumber(oldest[2]) + window - now end
if retry < 1 then retry = 1 end
return {0, 0, retry}
`)

// Result — итог проверки правила.
type Result struct {
	Allowed bool
	// Limit и Remaining — для заголовков X-RateLimit-* (последний проверенный лимит, как в Laravel).
	Limit     int
	Remaining int
	// RetryAfter — когда можно повторить (только при Allowed = false).
	RetryAfter time.Duration
}

// ErrUnknownRule — правила с таким именем нет.
var ErrUnknownRule = errors.New("ratelimit: unknown rule")

// Options — настройки Limiter.
type Options struct {
	Client valkey.Client
	// Rules — правила; nil — DefaultRules().
	Rules []Rule
	// Enable — включить правила, выключенные по умолчанию (web-login, web-register).
	Enable []string
	// Disable — выключить правила (например, в тестах нагрузки).
	Disable []string
	// FailOpen — пропускать запросы, когда Valkey недоступен (по умолчанию — отклонять).
	FailOpen bool
	// Prefix — префикс ключей; по умолчанию "rl:".
	Prefix string
	Logger *slog.Logger
}

// Limiter проверяет именованные лимиты.
type Limiter struct {
	client   valkey.Client
	rules    map[string]Rule
	failOpen bool
	prefix   string
	log      *slog.Logger
}

// New создаёт Limiter.
func New(o Options) (*Limiter, error) {
	if o.Client == nil {
		return nil, errors.New("ratelimit: valkey client is required")
	}
	rules := o.Rules
	if rules == nil {
		rules = DefaultRules()
	}
	l := &Limiter{client: o.Client, rules: make(map[string]Rule, len(rules)), failOpen: o.FailOpen, prefix: o.Prefix, log: o.Logger}
	if l.prefix == "" {
		l.prefix = "rl:"
	}
	if l.log == nil {
		l.log = slog.Default()
	}
	for _, r := range rules {
		if _, dup := l.rules[r.Name]; dup {
			return nil, fmt.Errorf("ratelimit: duplicate rule %q", r.Name)
		}
		for _, lim := range r.Limits {
			if lim.Max < 1 || lim.Window < time.Millisecond || lim.Key == nil {
				return nil, fmt.Errorf("ratelimit: invalid limit in rule %q", r.Name)
			}
		}
		l.rules[r.Name] = r
	}
	for _, name := range o.Enable {
		r, ok := l.rules[name]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownRule, name)
		}
		r.Disabled = false
		l.rules[name] = r
	}
	for _, name := range o.Disable {
		r, ok := l.rules[name]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownRule, name)
		}
		r.Disabled = true
		l.rules[name] = r
	}
	return l, nil
}

// Has — есть ли правило (включённое или нет).
func (l *Limiter) Has(name string) bool { _, ok := l.rules[name]; return ok }

// Enabled — правило есть и включено.
func (l *Limiter) Enabled(name string) bool { r, ok := l.rules[name]; return ok && !r.Disabled }

// Allow засчитывает запрос subject по правилу name. Лимиты правила проверяются по порядку,
// каждый засчитывается, пока не встретится превышенный (ThrottleRequests::handleRequest()).
// Выключенное правило всегда разрешает. Ошибка — только при недоступном Valkey и FailOpen = false.
func (l *Limiter) Allow(ctx context.Context, name string, s Subject) (Result, error) {
	r, ok := l.rules[name]
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownRule, name)
	}
	res := Result{Allowed: true}
	if r.Disabled {
		return res, nil
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Result{}, err
	}
	for i, lim := range r.Limits {
		key := lim.Key(s)
		if key == "" {
			continue
		}
		vk := l.prefix + name + ":" + strconv.Itoa(i) + ":" + sha256hex(key)
		vals, err := slidingWindow.Exec(ctx, l.client, []string{vk},
			[]string{strconv.FormatInt(lim.Window.Milliseconds(), 10), strconv.Itoa(lim.Max), hex.EncodeToString(nonce[:])}).AsIntSlice()
		if err == nil && len(vals) != 3 {
			err = errors.New("unexpected script reply")
		}
		if err != nil {
			if l.failOpen {
				l.log.WarnContext(ctx, "rate limit unavailable, allowing", slog.String("rule", name), slog.String("err", err.Error()))
				return Result{Allowed: true}, nil
			}
			return Result{}, fmt.Errorf("ratelimit: %s: %w", name, err)
		}
		res.Limit, res.Remaining = lim.Max, int(vals[1])
		if vals[0] != 1 {
			res.Allowed = false
			res.RetryAfter = time.Duration(vals[2]) * time.Millisecond
			return res, nil
		}
	}
	return res, nil
}

// Reset удаляет счётчики правила для subject (например, после успешного входа — RateLimiter::clear).
func (l *Limiter) Reset(ctx context.Context, name string, s Subject) error {
	r, ok := l.rules[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRule, name)
	}
	cmds := make(valkey.Commands, 0, len(r.Limits))
	for i, lim := range r.Limits {
		if key := lim.Key(s); key != "" {
			cmds = append(cmds, l.client.B().Del().Key(l.prefix+name+":"+strconv.Itoa(i)+":"+sha256hex(key)).Build())
		}
	}
	for _, resp := range l.client.DoMulti(ctx, cmds...) {
		if err := resp.Error(); err != nil {
			return err
		}
	}
	return nil
}
