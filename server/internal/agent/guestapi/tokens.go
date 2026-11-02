package guestapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
)

// Токен гостя (docs/agent-protocol.md §4): непрозрачная строка "zgt_" + 32 случайных байта (base64url), в Valkey —
// только SHA-256 токена. Привязан к клубу и ПК (computer_id из сертификата) — на другом ПК не принимается.
//
// Ключи Valkey (prefix — Options.KeyPrefix):
//
//	<prefix>tok:<sha256>  HASH {club, pc, user, exp, used}  TTL = срок токена
//	<prefix>pc:<pc>       STRING <sha256> — один токен на ПК: новый вход на ПК отзывает прежний (как
//	                      $user->tokens()->where('name', $deviceName)->delete() в AuthController/QrLogins)
//	<prefix>user:<user>   HASH <pc> → <sha256> — токены пользователя по ПК: правило «один ПК на клиента»
//	                      (AgentSignIns::elsewhere) и отзыв всех токенов при смене пароля.
//
// Valkey без cluster mode (как присутствие ПК, open-questions/stage2-agent-stream.md AS-6): скрипт сам вычисляет
// ключи прежнего токена.
const tokenPrefix = "zgt_"

// AgentSignIns::ACTIVE_MINUTES — клиент считается вошедшим на другом ПК, если его токен там использовался
// в последние 2 минуты (или ПК сейчас в сети и сообщает этого гостя в Status).
const signInActiveFor = 2 * time.Minute

// touchEvery — не чаще этого обновлять «последнее использование» токена (Sanctum писал last_used_at на каждый запрос).
const touchEvery = 20 * time.Second

// Guest — гость запроса (по токену).
type Guest struct {
	UserID    int64
	ClubID    int64
	PC        int64
	ExpiresAt time.Time
	hash      string
}

// Tokens — токены гостя в Valkey.
type Tokens struct {
	c      valkey.Client
	prefix string
	ttl    time.Duration
	now    func() time.Time
}

// NewTokens — хранилище токенов.
func NewTokens(c valkey.Client, prefix string, ttl time.Duration, now func() time.Time) *Tokens {
	if now == nil {
		now = time.Now
	}
	return &Tokens{c: c, prefix: prefix, ttl: ttl, now: now}
}

func (t *Tokens) tokKey(hash string) string { return t.prefix + "tok:" + hash }
func (t *Tokens) pcKey(pc int64) string     { return t.prefix + "pc:" + strconv.FormatInt(pc, 10) }
func (t *Tokens) userKey(user int64) string { return t.prefix + "user:" + strconv.FormatInt(user, 10) }
func hashToken(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}
func validTokenFormat(token string) bool {
	return strings.HasPrefix(token, tokenPrefix) && len(token) == len(tokenPrefix)+43
}
func millis(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }
func unixString(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

// issueScript: отозвать прежний токен ПК, записать новый, привязать к пользователю.
// KEYS: tok(new), pc, user(new). ARGV: club, pc, user, exp, now, ttl_ms, prefix, hash.
var issueScript = valkey.NewLuaScript(`
local old = redis.call('GET', KEYS[2])
if old then
  local oldKey = ARGV[7] .. 'tok:' .. old
  local oldUser = redis.call('HGET', oldKey, 'user')
  redis.call('DEL', oldKey)
  if oldUser then
    local uk = ARGV[7] .. 'user:' .. oldUser
    if redis.call('HGET', uk, ARGV[2]) == old then redis.call('HDEL', uk, ARGV[2]) end
  end
end
redis.call('HSET', KEYS[1], 'club', ARGV[1], 'pc', ARGV[2], 'user', ARGV[3], 'exp', ARGV[4], 'used', ARGV[5])
redis.call('PEXPIRE', KEYS[1], ARGV[6])
redis.call('SET', KEYS[2], ARGV[8], 'PX', ARGV[6])
redis.call('HSET', KEYS[3], ARGV[2], ARGV[8])
redis.call('PEXPIRE', KEYS[3], ARGV[6])
return 1
`)

// revokeScript: удалить токен и его ссылки (ПК, пользователь), если они указывают на него.
// KEYS: tok. ARGV: prefix, hash.
var revokeScript = valkey.NewLuaScript(`
local v = redis.call('HMGET', KEYS[1], 'pc', 'user')
redis.call('DEL', KEYS[1])
if v[1] then
  local pk = ARGV[1] .. 'pc:' .. v[1]
  if redis.call('GET', pk) == ARGV[2] then redis.call('DEL', pk) end
  if v[2] then
    local uk = ARGV[1] .. 'user:' .. v[2]
    if redis.call('HGET', uk, v[1]) == ARGV[2] then redis.call('HDEL', uk, v[1]) end
  end
  return tonumber(v[1])
end
return 0
`)

// Issue выдаёт токен гостю userID на ПК (клуб и ПК — из сертификата); прежний токен этого ПК отзывается.
func (t *Tokens) Issue(ctx context.Context, id identity.Identity, userID int64) (string, time.Time, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, err
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	hash := hashToken(token)
	now := t.now()
	exp := now.Add(t.ttl).Truncate(time.Second)
	err := issueScript.Exec(ctx, t.c,
		[]string{t.tokKey(hash), t.pcKey(id.ComputerID), t.userKey(userID)},
		[]string{strconv.FormatInt(id.ClubID, 10), strconv.FormatInt(id.ComputerID, 10), strconv.FormatInt(userID, 10),
			unixString(exp), unixString(now), millis(t.ttl), t.prefix, hash}).Error()
	if err != nil {
		return "", time.Time{}, err
	}
	return token, exp, nil
}

// errTokenInvalid — токена нет, истёк, отозван или выдан другому ПК (агент выполняет выход гостя).
var errTokenInvalid = rpcError(codes.Unauthenticated, agentv1.ErrorCode_ERROR_CODE_GUEST_TOKEN_INVALID, msgGuestToken, nil, "")

// bearer — токен из метаданных `authorization: Bearer …`; "" — нет.
func bearer(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get("authorization") {
		if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
			return strings.TrimSpace(v[7:])
		}
	}
	return ""
}

// FromContext — гость по токену из метаданных; токен должен принадлежать этому ПК и клубу.
func (t *Tokens) FromContext(ctx context.Context, id identity.Identity) (Guest, error) {
	token := bearer(ctx)
	if token == "" {
		return Guest{}, errTokenInvalid
	}
	g, err := t.Lookup(ctx, token)
	if err != nil {
		return Guest{}, err
	}
	if g.ClubID != id.ClubID || g.PC != id.ComputerID {
		return Guest{}, errTokenInvalid
	}
	return g, nil
}

// Lookup — гость по токену (без проверки ПК). Отсутствующий/истёкший — errTokenInvalid.
func (t *Tokens) Lookup(ctx context.Context, token string) (Guest, error) {
	if !validTokenFormat(token) {
		return Guest{}, errTokenInvalid
	}
	hash := hashToken(token)
	m, err := t.c.Do(ctx, t.c.B().Hgetall().Key(t.tokKey(hash)).Build()).AsStrMap()
	if err != nil {
		return Guest{}, err
	}
	g, used, ok := parseToken(m)
	if !ok || !t.now().Before(g.ExpiresAt) {
		return Guest{}, errTokenInvalid
	}
	g.hash = hash
	if now := t.now(); now.Sub(used) >= touchEvery {
		// last_used_at: «вошёл на другом ПК» смотрит, когда токен использовали (AgentSignIns).
		_ = t.c.Do(ctx, t.c.B().Hset().Key(t.tokKey(hash)).FieldValue().FieldValue("used", unixString(now)).Build()).Error()
	}
	return g, nil
}

func parseToken(m map[string]string) (Guest, time.Time, bool) {
	club, e1 := strconv.ParseInt(m["club"], 10, 64)
	pcID, e2 := strconv.ParseInt(m["pc"], 10, 64)
	user, e3 := strconv.ParseInt(m["user"], 10, 64)
	exp, e4 := strconv.ParseInt(m["exp"], 10, 64)
	used, _ := strconv.ParseInt(m["used"], 10, 64)
	if errors.Join(e1, e2, e3, e4) != nil || club <= 0 || pcID <= 0 || user <= 0 {
		return Guest{}, time.Time{}, false
	}
	return Guest{UserID: user, ClubID: club, PC: pcID, ExpiresAt: time.Unix(exp, 0)}, time.Unix(used, 0), true
}

// Revoke отзывает токен гостя (выход, POST /api/auth/logout: currentAccessToken()->delete()).
func (t *Tokens) Revoke(ctx context.Context, g Guest) error {
	if g.hash == "" {
		return nil
	}
	return revokeScript.Exec(ctx, t.c, []string{t.tokKey(g.hash)}, []string{t.prefix, g.hash}).Error()
}

// RevokeComputer отзывает токен ПК, если он выдан userID (0 — любой). Выход гостя на ПК (событие GuestSignedOut).
func (t *Tokens) RevokeComputer(ctx context.Context, computerID, userID int64) error {
	hash, err := t.c.Do(ctx, t.c.B().Get().Key(t.pcKey(computerID)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if userID > 0 {
		u, err := t.c.Do(ctx, t.c.B().Hget().Key(t.tokKey(hash)).Field("user").Build()).ToString()
		if err != nil && !valkey.IsValkeyNil(err) {
			return err
		}
		if u != strconv.FormatInt(userID, 10) {
			return nil
		}
	}
	return revokeScript.Exec(ctx, t.c, []string{t.tokKey(hash)}, []string{t.prefix, hash}).Error()
}

// RevokeUser отзывает все токены пользователя, кроме except (смена пароля: «остальные токены отзываются, этот
// остаётся», GuestProfileController::password()). Возвращает ПК, где токены отозваны (им — SignOutGuest).
func (t *Tokens) RevokeUser(ctx context.Context, userID int64, except Guest) ([]int64, error) {
	m, err := t.c.Do(ctx, t.c.B().Hgetall().Key(t.userKey(userID)).Build()).AsStrMap()
	if err != nil {
		return nil, err
	}
	var pcs []int64
	for pcField, hash := range m {
		if hash == except.hash {
			continue
		}
		if err := revokeScript.Exec(ctx, t.c, []string{t.tokKey(hash)}, []string{t.prefix, hash}).Error(); err != nil {
			return pcs, err
		}
		if n, err := strconv.ParseInt(pcField, 10, 64); err == nil {
			pcs = append(pcs, n)
		}
	}
	return pcs, nil
}

// SignedInElsewhere — ПК (кроме computerID), где пользователь сейчас вошёл: живой токен, использованный за
// последние 2 минуты, или ПК в сети сообщает этого гостя (присутствие). Laravel: AgentSignIns::elsewhere().
// online(pc) — проверка присутствия (nil — только по токенам). 0 — нигде.
func (t *Tokens) SignedInElsewhere(ctx context.Context, userID, computerID int64, online func(pc int64) bool) (int64, error) {
	m, err := t.c.Do(ctx, t.c.B().Hgetall().Key(t.userKey(userID)).Build()).AsStrMap()
	if err != nil {
		return 0, err
	}
	now := t.now()
	var best int64
	var bestUsed time.Time
	for pcField, hash := range m {
		pcID, err := strconv.ParseInt(pcField, 10, 64)
		if err != nil || pcID == computerID {
			continue
		}
		tm, err := t.c.Do(ctx, t.c.B().Hgetall().Key(t.tokKey(hash)).Build()).AsStrMap()
		if err != nil {
			return 0, err
		}
		g, used, ok := parseToken(tm)
		if !ok || !now.Before(g.ExpiresAt) || g.UserID != userID {
			continue
		}
		active := now.Sub(used) <= signInActiveFor
		if !active && online != nil {
			active = online(pcID)
		}
		if active && (best == 0 || used.After(bestUsed)) { // ->latest('last_used_at')->value('name')
			best, bestUsed = pcID, used
		}
	}
	return best, nil
}

// RevokeEverywhere отзывает все токены пользователя на ПК всех клубов (восстановление пароля по коду из Telegram:
// $user->tokens()->delete(), PasswordRecovery::reset()). Возвращает отозванные токены (клуб и ПК — для SignOutGuest).
func (t *Tokens) RevokeEverywhere(ctx context.Context, userID int64) ([]Guest, error) {
	m, err := t.c.Do(ctx, t.c.B().Hgetall().Key(t.userKey(userID)).Build()).AsStrMap()
	if err != nil {
		return nil, err
	}
	var out []Guest
	for _, hash := range m {
		tm, err := t.c.Do(ctx, t.c.B().Hgetall().Key(t.tokKey(hash)).Build()).AsStrMap()
		if err != nil {
			return out, err
		}
		g, _, ok := parseToken(tm)
		if err := revokeScript.Exec(ctx, t.c, []string{t.tokKey(hash)}, []string{t.prefix, hash}).Error(); err != nil {
			return out, err
		}
		if ok && g.UserID == userID {
			out = append(out, g)
		}
	}
	return out, nil
}
