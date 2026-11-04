// Package session — сессии админки: короткий access-токен (HMAC, см. token.go) и непрозрачный
// refresh-токен с ротацией, всё состояние — в Valkey.
//
// Схема ключей (хэш-тег {sid} держит ключи одной сессии в одном слоте кластера):
//
//	auth:s:{<sid>}    hash   uid, club, view, rt, prev, rot, seen, created, exp, ttl, remember, mfa
//	auth:s:{<sid>}:u  set    использованные (заменённые) хэши refresh-токенов семейства
//	auth:u:{<uid>}    zset   сессии пользователя: member = sid, score = последняя активность (мс)
//
// Refresh-токен — "<sid>.<32 случайных байта base64url>"; хранится только sha256 от него.
// Каждое обновление выдаёт новый токен (ротация), старый попадает в «использованные». Повтор
// использованного токена — признак кражи: всё семейство (сессия) отзывается. Исключение — гонка двух
// вкладок: повтор непосредственно предыдущего токена в течение ReuseGrace после ротации отклоняется
// (ErrRefreshRace) без отзыва — новый токен уже получила другая вкладка.
//
// «На смене» (BR-060, StaffShifts::onShift, Q-035): в старом — пользователи с живой web-сессией
// (last_activity за SESSION_LIFETIME = 120 мин). Здесь — ActiveUsers по zset auth:u:{uid}:
// у пользователя есть сессия с активностью не раньше since. Активность обновляется при входе,
// обновлении токена и запросах (не чаще TouchEvery).
package session

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Ошибки.
var (
	// ErrNotFound — сессии нет (вышли, отозвана, истекла).
	ErrNotFound = errors.New("session: not found")
	// ErrInvalidRefresh — refresh-токен неизвестен.
	ErrInvalidRefresh = errors.New("session: invalid refresh token")
	// ErrRefreshReused — повтор использованного refresh-токена: семейство отозвано.
	ErrRefreshReused = errors.New("session: refresh token reused, session revoked")
	// ErrRefreshRace — повтор только что заменённого токена (параллельное обновление); сессия жива.
	ErrRefreshRace = errors.New("session: refresh token already rotated")
)

// Options — настройки Manager.
type Options struct {
	Client valkey.Client
	// Keys — ключи подписи access-токенов; первый подписывает.
	Keys []Key
	// AccessTTL — срок access-токена; по умолчанию 10 мин.
	AccessTTL time.Duration
	// IdleTTL — сессия без активности живёт столько; по умолчанию 120 мин (SESSION_LIFETIME старого).
	IdleTTL time.Duration
	// RememberTTL — то же для входа с «запомнить меня»; по умолчанию 30 дней.
	RememberTTL time.Duration
	// MaxLifetime — абсолютный срок сессии от входа; по умолчанию 30 дней.
	MaxLifetime time.Duration
	// ReuseGrace — окно гонки вкладок при обновлении; по умолчанию 10 с.
	ReuseGrace time.Duration
	// TouchEvery — как часто запросы обновляют активность сессии; по умолчанию 1 мин.
	TouchEvery time.Duration
	// Prefix — префикс ключей; по умолчанию "auth:".
	Prefix string
	// Now — часы (тесты); по умолчанию time.Now.
	Now func() time.Time
}

// Manager выпускает, проверяет и отзывает сессии.
type Manager struct {
	c      valkey.Client
	signer *signer
	o      Options
}

// New создаёт Manager.
func New(o Options) (*Manager, error) {
	if o.Client == nil {
		return nil, errors.New("session: valkey client is required")
	}
	s, err := newSigner(o.Keys)
	if err != nil {
		return nil, err
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.AccessTTL, 10*time.Minute)
	def(&o.IdleTTL, 120*time.Minute)
	def(&o.RememberTTL, 30*24*time.Hour)
	def(&o.MaxLifetime, 30*24*time.Hour)
	def(&o.ReuseGrace, 10*time.Second)
	def(&o.TouchEvery, time.Minute)
	if o.Prefix == "" {
		o.Prefix = "auth:"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Manager{c: o.Client, signer: s, o: o}, nil
}

// IdleTTL — время жизни сессии без активности (окно «на смене»).
func (m *Manager) IdleTTL() time.Duration { return m.o.IdleTTL }

// Tokens — выданные токены.
type Tokens struct {
	SessionID     string
	Access        string
	AccessExpires time.Time
	// Refresh — секрет: только в cookie HttpOnly, не в логах и не в теле ответа.
	Refresh string
	// RefreshMaxAge — срок cookie refresh-токена.
	RefreshMaxAge time.Duration
}

// Session — состояние сессии.
type Session struct {
	ID     string
	UserID int64
	// ClubID — выбранный клуб; 0 — не выбран.
	ClubID int64
	// ViewingClubID — клуб, который суперадмин смотрит только для чтения; 0 — нет.
	ViewingClubID int64
	// SecondFactor — вход подтверждён вторым фактором (TOTP суперадмина, Этап 7). Сессия суперадмина без него
	// не действует (auth.Service.Authenticate): старые сессии и права, выданные уже вошедшему, требуют нового входа.
	SecondFactor bool
}

func (m *Manager) sessKey(sid string) string { return m.o.Prefix + "s:{" + sid + "}" }
func (m *Manager) usedKey(sid string) string { return m.o.Prefix + "s:{" + sid + "}:u" }
func (m *Manager) userKey(uid int64) string {
	return m.o.Prefix + "u:{" + strconv.FormatInt(uid, 10) + "}"
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func ms(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

// splitRefresh — sid из refresh-токена "<sid>.<secret>".
func splitRefresh(token string) (sid string, ok bool) {
	sid, secret, ok := strings.Cut(token, ".")
	if !ok || len(sid) != 22 || len(secret) != 43 {
		return "", false
	}
	return sid, true
}

func (m *Manager) access(sid string, uid int64, now time.Time) (string, time.Time, error) {
	exp := now.Add(m.o.AccessTTL)
	tok, err := m.signer.sign(claims{SID: sid, UID: uid, IAT: now.Unix(), EXP: exp.Unix()})
	return tok, exp, err
}

// Create начинает сессию пользователя в клубе clubID (0 — без клуба).
func (m *Manager) Create(ctx context.Context, userID, clubID int64, remember bool) (Tokens, error) {
	return m.create(ctx, userID, clubID, remember, false)
}

// CreateSecondFactor — Create для входа, подтверждённого вторым фактором (Session.SecondFactor).
func (m *Manager) CreateSecondFactor(ctx context.Context, userID, clubID int64, remember bool) (Tokens, error) {
	return m.create(ctx, userID, clubID, remember, true)
}

func (m *Manager) create(ctx context.Context, userID, clubID int64, remember, secondFactor bool) (Tokens, error) {
	if userID <= 0 {
		return Tokens{}, errors.New("session: user id is required")
	}
	sid, err := randomToken(16)
	if err != nil {
		return Tokens{}, err
	}
	secret, err := randomToken(32)
	if err != nil {
		return Tokens{}, err
	}
	refresh := sid + "." + secret
	now := m.o.Now()
	ttl := m.o.IdleTTL
	if remember {
		ttl = m.o.RememberTTL
	}
	ttl = min(ttl, m.o.MaxLifetime)
	exp := now.Add(m.o.MaxLifetime)
	b := m.c.B()
	rem, mfa := "0", "0"
	if remember {
		rem = "1"
	}
	if secondFactor {
		mfa = "1"
	}
	cmds := valkey.Commands{
		b.Hset().Key(m.sessKey(sid)).FieldValue().
			FieldValue("uid", strconv.FormatInt(userID, 10)).
			FieldValue("club", strconv.FormatInt(clubID, 10)).
			FieldValue("view", "0").
			FieldValue("rt", hashToken(refresh)).
			FieldValue("prev", "").
			FieldValue("rot", "0").
			FieldValue("seen", ms(now)).
			FieldValue("created", ms(now)).
			FieldValue("exp", ms(exp)).
			FieldValue("ttl", strconv.FormatInt(ttl.Milliseconds(), 10)).
			FieldValue("remember", rem).
			FieldValue("mfa", mfa).Build(),
		b.Pexpire().Key(m.sessKey(sid)).Milliseconds(ttl.Milliseconds()).Build(),
		b.Zadd().Key(m.userKey(userID)).ScoreMember().ScoreMember(float64(now.UnixMilli()), sid).Build(),
		b.Pexpire().Key(m.userKey(userID)).Milliseconds(m.o.MaxLifetime.Milliseconds()).Build(),
	}
	for _, r := range m.c.DoMulti(ctx, cmds...) {
		if err := r.Error(); err != nil {
			return Tokens{}, fmt.Errorf("session: create: %w", err)
		}
	}
	access, accessExp, err := m.access(sid, userID, now)
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{SessionID: sid, Access: access, AccessExpires: accessExp, Refresh: refresh, RefreshMaxAge: ttl}, nil
}

// refreshScript: KEYS[1] — сессия, KEYS[2] — использованные; ARGV: хэш предъявленного, новый хэш, now (мс), grace (мс).
var refreshScript = valkey.NewLuaScript(`
local s = redis.call('HMGET', KEYS[1], 'rt', 'prev', 'rot', 'exp', 'ttl', 'uid')
if not s[1] then return {'missing', ''} end
local now = tonumber(ARGV[3])
if ARGV[1] == s[1] then
  local left = tonumber(s[4]) - now
  if left <= 0 then
    redis.call('DEL', KEYS[1], KEYS[2])
    return {'missing', s[6]}
  end
  redis.call('HSET', KEYS[1], 'rt', ARGV[2], 'prev', ARGV[1], 'rot', ARGV[3], 'seen', ARGV[3])
  redis.call('SADD', KEYS[2], ARGV[1])
  local ttl = math.min(tonumber(s[5]), left)
  redis.call('PEXPIRE', KEYS[1], ttl)
  redis.call('PEXPIRE', KEYS[2], ttl)
  return {'ok', s[6], tostring(ttl)}
end
if s[2] ~= '' and ARGV[1] == s[2] and now - tonumber(s[3]) < tonumber(ARGV[4]) then
  return {'race', s[6]}
end
if redis.call('SISMEMBER', KEYS[2], ARGV[1]) == 1 then
  redis.call('DEL', KEYS[1], KEYS[2])
  return {'reused', s[6]}
end
return {'invalid', s[6]}
`)

// Refresh обменивает refresh-токен на новую пару (ротация).
func (m *Manager) Refresh(ctx context.Context, refresh string) (Tokens, error) {
	sid, ok := splitRefresh(refresh)
	if !ok {
		return Tokens{}, ErrInvalidRefresh
	}
	secret, err := randomToken(32)
	if err != nil {
		return Tokens{}, err
	}
	next := sid + "." + secret
	now := m.o.Now()
	res, err := refreshScript.Exec(ctx, m.c, []string{m.sessKey(sid), m.usedKey(sid)},
		[]string{hashToken(refresh), hashToken(next), ms(now), strconv.FormatInt(m.o.ReuseGrace.Milliseconds(), 10)}).AsStrSlice()
	if err != nil {
		return Tokens{}, fmt.Errorf("session: refresh: %w", err)
	}
	if len(res) < 2 {
		return Tokens{}, errors.New("session: refresh: unexpected reply")
	}
	uid, _ := strconv.ParseInt(res[1], 10, 64)
	switch res[0] {
	case "ok":
		ttlMs, _ := strconv.ParseInt(res[2], 10, 64)
		if err := m.touchUser(ctx, uid, sid, now); err != nil {
			return Tokens{}, err
		}
		access, exp, err := m.access(sid, uid, now)
		if err != nil {
			return Tokens{}, err
		}
		return Tokens{SessionID: sid, Access: access, AccessExpires: exp, Refresh: next, RefreshMaxAge: time.Duration(ttlMs) * time.Millisecond}, nil
	case "race":
		return Tokens{}, ErrRefreshRace
	case "reused":
		m.forgetUserSession(ctx, uid, sid)
		return Tokens{}, ErrRefreshReused
	case "missing":
		if uid > 0 {
			m.forgetUserSession(ctx, uid, sid)
		}
		return Tokens{}, ErrInvalidRefresh
	}
	return Tokens{}, ErrInvalidRefresh
}

func (m *Manager) touchUser(ctx context.Context, uid int64, sid string, now time.Time) error {
	b := m.c.B()
	for _, r := range m.c.DoMulti(ctx,
		b.Zadd().Key(m.userKey(uid)).ScoreMember().ScoreMember(float64(now.UnixMilli()), sid).Build(),
		b.Pexpire().Key(m.userKey(uid)).Milliseconds(m.o.MaxLifetime.Milliseconds()).Build(),
	) {
		if err := r.Error(); err != nil {
			return fmt.Errorf("session: touch: %w", err)
		}
	}
	return nil
}

func (m *Manager) forgetUserSession(ctx context.Context, uid int64, sid string) {
	if uid > 0 {
		_ = m.c.Do(ctx, m.c.B().Zrem().Key(m.userKey(uid)).Member(sid).Build()).Error()
	}
}

// touchScript: KEYS[1] — сессия, KEYS[2] — использованные; ARGV: now (мс), touchEvery (мс).
// Ответ: {uid, club, view, touched, mfa} или пусто (сессии до Этапа 7 без поля mfa — '0').
var touchScript = valkey.NewLuaScript(`
local s = redis.call('HMGET', KEYS[1], 'uid', 'club', 'view', 'seen', 'exp', 'ttl', 'mfa')
if not s[1] then return {} end
local now = tonumber(ARGV[1])
local left = tonumber(s[5]) - now
if left <= 0 then
  redis.call('DEL', KEYS[1], KEYS[2])
  return {}
end
local touched = '0'
if now - tonumber(s[4]) >= tonumber(ARGV[2]) then
  redis.call('HSET', KEYS[1], 'seen', ARGV[1])
  redis.call('PEXPIRE', KEYS[1], math.min(tonumber(s[6]), left))
  touched = '1'
end
return {s[1], s[2], s[3], touched, s[7] or '0'}
`)

// Authenticate проверяет access-токен и сессию в Valkey (выход и отзыв действуют сразу).
func (m *Manager) Authenticate(ctx context.Context, access string) (Session, error) {
	now := m.o.Now()
	c, err := m.signer.verify(access, now)
	if err != nil {
		return Session{}, err
	}
	res, err := touchScript.Exec(ctx, m.c, []string{m.sessKey(c.SID), m.usedKey(c.SID)},
		[]string{ms(now), strconv.FormatInt(m.o.TouchEvery.Milliseconds(), 10)}).AsStrSlice()
	if err != nil {
		return Session{}, fmt.Errorf("session: authenticate: %w", err)
	}
	if len(res) != 5 {
		m.forgetUserSession(ctx, c.UID, c.SID)
		return Session{}, ErrNotFound
	}
	uid, _ := strconv.ParseInt(res[0], 10, 64)
	if uid != c.UID {
		return Session{}, ErrInvalidToken
	}
	club, _ := strconv.ParseInt(res[1], 10, 64)
	view, _ := strconv.ParseInt(res[2], 10, 64)
	if res[3] == "1" {
		if err := m.touchUser(ctx, uid, c.SID, now); err != nil {
			return Session{}, err
		}
	}
	return Session{ID: c.SID, UserID: uid, ClubID: club, ViewingClubID: view, SecondFactor: res[4] == "1"}, nil
}

// setScript: KEYS[1] — сессия; ARGV — пары поле/значение. Меняет только существующую сессию.
var setScript = valkey.NewLuaScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
redis.call('HSET', KEYS[1], unpack(ARGV))
return 1
`)

func (m *Manager) set(ctx context.Context, sid string, kv ...string) error {
	n, err := setScript.Exec(ctx, m.c, []string{m.sessKey(sid)}, kv).AsInt64()
	if err != nil {
		return fmt.Errorf("session: update: %w", err)
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

// SetClub выбирает клуб сессии.
func (m *Manager) SetClub(ctx context.Context, sid string, clubID int64) error {
	return m.set(ctx, sid, "club", strconv.FormatInt(clubID, 10))
}

// SetViewing включает (clubID > 0) или выключает (0) режим просмотра клуба суперадмином.
// Laravel: session superadmin_viewing_club_id (Superadmin\ClubController::view/leave).
func (m *Manager) SetViewing(ctx context.Context, sid string, clubID int64) error {
	return m.set(ctx, sid, "view", strconv.FormatInt(clubID, 10))
}

// Revoke завершает сессию (выход). Отсутствующая сессия — не ошибка.
func (m *Manager) Revoke(ctx context.Context, sid string) error {
	uid, err := m.c.Do(ctx, m.c.B().Hget().Key(m.sessKey(sid)).Field("uid").Build()).AsInt64()
	if err != nil && !valkey.IsValkeyNil(err) {
		return fmt.Errorf("session: revoke: %w", err)
	}
	if err := m.c.Do(ctx, m.c.B().Del().Key(m.sessKey(sid), m.usedKey(sid)).Build()).Error(); err != nil {
		return fmt.Errorf("session: revoke: %w", err)
	}
	m.forgetUserSession(ctx, uid, sid)
	return nil
}

// RevokeRefresh — выход по refresh-токену из cookie (когда access-токена нет).
func (m *Manager) RevokeRefresh(ctx context.Context, refresh string) error {
	sid, ok := splitRefresh(refresh)
	if !ok {
		return nil
	}
	cur, err := m.c.Do(ctx, m.c.B().Hget().Key(m.sessKey(sid)).Field("rt").Build()).ToString()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return nil
		}
		return fmt.Errorf("session: revoke: %w", err)
	}
	// Отозвать может только владелец текущего токена (не угадавший sid).
	if subtle.ConstantTimeCompare([]byte(cur), []byte(hashToken(refresh))) != 1 {
		return nil
	}
	return m.Revoke(ctx, sid)
}

// RevokeUser завершает все сессии пользователя (смена пароля, блокировка, «выйти везде»).
func (m *Manager) RevokeUser(ctx context.Context, userID int64) error {
	sids, err := m.c.Do(ctx, m.c.B().Zrange().Key(m.userKey(userID)).Min("0").Max("-1").Build()).AsStrSlice()
	if err != nil && !valkey.IsValkeyNil(err) {
		return fmt.Errorf("session: revoke user: %w", err)
	}
	cmds := make(valkey.Commands, 0, len(sids)+1)
	for _, sid := range sids {
		cmds = append(cmds, m.c.B().Del().Key(m.sessKey(sid), m.usedKey(sid)).Build())
	}
	cmds = append(cmds, m.c.B().Del().Key(m.userKey(userID)).Build())
	for _, r := range m.c.DoMulti(ctx, cmds...) {
		if err := r.Error(); err != nil {
			return fmt.Errorf("session: revoke user: %w", err)
		}
	}
	return nil
}

// ActiveUsers — кто из userIDs активен в панели с момента since (есть сессия с активностью ≥ since).
// Для лимита сотрудников на смене (BR-060): вызывающий передаёт членов клуба с ролью, кроме себя,
// и since = now − IdleTTL (как StaffShifts::onShift: last_activity ≥ now − SESSION_LIFETIME).
func (m *Manager) ActiveUsers(ctx context.Context, userIDs []int64, since time.Time) ([]int64, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	cmds := make(valkey.Commands, 0, len(userIDs))
	for _, id := range userIDs {
		cmds = append(cmds, m.c.B().Zcount().Key(m.userKey(id)).Min(ms(since)).Max("+inf").Build())
	}
	results := m.c.DoMulti(ctx, cmds...)
	if len(results) != len(userIDs) {
		return nil, errors.New("session: active users: unexpected reply")
	}
	var out []int64
	for i, id := range userIDs {
		n, err := results[i].AsInt64()
		if err != nil {
			return nil, fmt.Errorf("session: active users: %w", err)
		}
		if n > 0 {
			out = append(out, id)
		}
	}
	return out, nil
}

// CSRFToken — токен для заголовка X-CSRF-Token, привязанный к сессии: HMAC(ключ, sid).
// Сервер не хранит его: проверка — пересчёт по sid из refresh-cookie (double submit с подписью).
func (m *Manager) CSRFToken(sid string) string {
	k := m.signer.keys[0]
	h := hmac.New(sha256.New, k.Secret)
	h.Write([]byte("zafiro.csrf.v1." + sid))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// CheckCSRF сверяет токен из заголовка с refresh-токеном из cookie (любой из ключей подписи).
func (m *Manager) CheckCSRF(refresh, header string) bool {
	sid, ok := splitRefresh(refresh)
	if !ok || header == "" {
		return false
	}
	for _, k := range m.signer.keys {
		h := hmac.New(sha256.New, k.Secret)
		h.Write([]byte("zafiro.csrf.v1." + sid))
		if hmac.Equal([]byte(header), []byte(base64.RawURLEncoding.EncodeToString(h.Sum(nil)))) {
			return true
		}
	}
	return false
}
