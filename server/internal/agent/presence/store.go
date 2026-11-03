package presence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Ключи Valkey (Prefix по умолчанию "zafiro:agent:"):
//
//	<p>gen              — счётчик поколений соединений (монотонный, не меньше текущего времени в мкс);
//	<p>pc:<computer>    — владелец стрима "<instance>|<gen>", TTL = Options.TTL (90 с), продлевается инстансом;
//	<p>club:<club>      — hash computer_id → State (JSON) для карты клуба;
//	<p>online           — zset "<club>:<computer>" → крайний срок (мс Valkey): после него Sweeper проверяет ПК;
//	<p>guest:<club>:<user>, <p>guests:since — индекс «гость → ПК» для уведомлений о балансе (guests.go).
//
// Скрипты обращаются к ключам, вычисленным внутри (pc:, club:) — рассчитано на Valkey без кластера
// (один primary + реплика; 50 000 ПК ≈ 2 000 операций/с). См. docs/open-questions/stage2-agent-stream.md AS-6.

// DefaultPrefix — префикс ключей по умолчанию.
const DefaultPrefix = "zafiro:agent:"

// ErrNotOwner — соединение больше не владеет ПК (его вытеснило более новое).
var ErrNotOwner = errors.New("presence: connection is not the owner of the computer")

// Store — присутствие ПК в Valkey.
type Store struct {
	c      valkey.Client
	prefix string
	ttl    time.Duration
	// lists — вызовы List (ListCalls; тесты пути «гость → ПК» без чтения всего клуба).
	lists atomic.Int64
}

// NewStore создаёт хранилище. ttl — сколько ПК считается в сети без продления (≥ 3 периодов продления).
func NewStore(c valkey.Client, prefix string, ttl time.Duration) *Store {
	if prefix == "" {
		prefix = DefaultPrefix
	}
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	return &Store{c: c, prefix: prefix, ttl: ttl}
}

// TTL — срок владения без продления.
func (s *Store) TTL() time.Duration { return s.ttl }

// Client — клиент Valkey (для соседних пакетов агента: команды, дедупликация событий).
func (s *Store) Client() valkey.Client { return s.c }

// Prefix — префикс ключей.
func (s *Store) Prefix() string { return s.prefix }

func (s *Store) pcKey(computerID int64) string {
	return s.prefix + "pc:" + strconv.FormatInt(computerID, 10)
}
func (s *Store) clubKey(clubID int64) string {
	return s.prefix + "club:" + strconv.FormatInt(clubID, 10)
}
func (s *Store) onlineKey() string { return s.prefix + "online" }
func member(clubID, computerID int64) string {
	return strconv.FormatInt(clubID, 10) + ":" + strconv.FormatInt(computerID, 10)
}

// Owner — кто держит стрим: "<instance>|<gen>".
type Owner struct {
	Instance string
	Gen      int64
}

func (o Owner) String() string { return o.Instance + "|" + strconv.FormatInt(o.Gen, 10) }

// ParseOwner разбирает "<instance>|<gen>"; ok = false — пусто или формат не тот.
func ParseOwner(v string) (Owner, bool) {
	i := strings.LastIndexByte(v, '|')
	if i <= 0 {
		return Owner{}, false
	}
	g, err := strconv.ParseInt(v[i+1:], 10, 64)
	if err != nil {
		return Owner{}, false
	}
	return Owner{Instance: v[:i], Gen: g}, true
}

var acquireScript = valkey.NewLuaScript(`
local t = redis.call('TIME')
local nowus = tonumber(t[1]) * 1000000 + tonumber(t[2])
local g = redis.call('INCR', KEYS[1])
if g < nowus then
  g = nowus
  redis.call('SET', KEYS[1], string.format('%.0f', g))
end
local gs = string.format('%.0f', g)
local prev = redis.call('GET', KEYS[2])
redis.call('SET', KEYS[2], ARGV[1] .. '|' .. gs, 'PX', ARGV[2])
local nowms = math.floor(nowus / 1000)
redis.call('ZADD', KEYS[3], string.format('%.0f', nowms + tonumber(ARGV[2])), ARGV[3])
if not prev then prev = '' end
return {gs, prev}
`)

// Acquire делает инстанс владельцем стрима ПК с новым поколением. prev — прежний владелец (ok = false — не было).
// Новое соединение всегда побеждает: прежнее закрывается (один стрим на ПК, agent.proto п. 9).
func (s *Store) Acquire(ctx context.Context, clubID, computerID int64, instance string) (own Owner, prev Owner, hadPrev bool, err error) {
	res, err := acquireScript.Exec(ctx, s.c,
		[]string{s.prefix + "gen", s.pcKey(computerID), s.onlineKey()},
		[]string{instance, strconv.FormatInt(s.ttl.Milliseconds(), 10), member(clubID, computerID)}).AsStrSlice()
	if err != nil {
		return Owner{}, Owner{}, false, fmt.Errorf("presence: acquire: %w", err)
	}
	if len(res) != 2 {
		return Owner{}, Owner{}, false, fmt.Errorf("presence: acquire: unexpected reply %v", res)
	}
	g, err := strconv.ParseInt(res[0], 10, 64)
	if err != nil {
		return Owner{}, Owner{}, false, fmt.Errorf("presence: acquire: gen %q: %w", res[0], err)
	}
	prev, hadPrev = ParseOwner(res[1])
	return Owner{Instance: instance, Gen: g}, prev, hadPrev, nil
}

var refreshScript = valkey.NewLuaScript(`
local cur = redis.call('GET', KEYS[1])
if cur and cur ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
local t = redis.call('TIME')
local nowms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZADD', KEYS[3], string.format('%.0f', nowms + tonumber(ARGV[2])), ARGV[5])
if ARGV[4] ~= '' then
  redis.call('HSET', KEYS[2], ARGV[3], ARGV[4])
  ` + guestIndexLua + `
end
return 1
`)

// RefreshItem — продление владения (и, если State не nil, запись снимка).
type RefreshItem struct {
	Owner Owner
	State *State
	// ClubID, ComputerID — обязательны (State может быть nil).
	ClubID, ComputerID int64
}

func (s *Store) refreshExec(it RefreshItem) (valkey.LuaExec, error) {
	js := ""
	if it.State != nil {
		b, err := json.Marshal(it.State)
		if err != nil {
			return valkey.LuaExec{}, err
		}
		js = string(b)
	}
	var signedIn int64
	if it.State != nil {
		signedIn = it.State.SignedInUserID
	}
	return valkey.LuaExec{
		Keys: s.guestIndexKeys([]string{s.pcKey(it.ComputerID), s.clubKey(it.ClubID), s.onlineKey()}, it.ClubID, signedIn),
		Args: []string{it.Owner.String(), strconv.FormatInt(s.ttl.Milliseconds(), 10),
			strconv.FormatInt(it.ComputerID, 10), js, member(it.ClubID, it.ComputerID), s.guestIndexTTLArg()},
	}, nil
}

// Refresh продлевает владение и пишет снимок. ErrNotOwner — ПК уже держит другое соединение.
func (s *Store) Refresh(ctx context.Context, it RefreshItem) error {
	ex, err := s.refreshExec(it)
	if err != nil {
		return err
	}
	ok, err := refreshScript.Exec(ctx, s.c, ex.Keys, ex.Args).AsInt64()
	if err != nil {
		return fmt.Errorf("presence: refresh: %w", err)
	}
	if ok == 0 {
		return ErrNotOwner
	}
	return nil
}

// RefreshMany — то же пачкой (один конвейер на тик инстанса). Возвращает ошибку по каждому элементу.
func (s *Store) RefreshMany(ctx context.Context, items []RefreshItem) []error {
	errs := make([]error, len(items))
	if len(items) == 0 {
		return errs
	}
	execs := make([]valkey.LuaExec, len(items))
	for i, it := range items {
		ex, err := s.refreshExec(it)
		if err != nil {
			errs[i] = err
		}
		execs[i] = ex
	}
	for i, r := range refreshScript.ExecMulti(ctx, s.c, execs...) {
		if errs[i] != nil {
			continue
		}
		ok, err := r.AsInt64()
		switch {
		case err != nil:
			errs[i] = fmt.Errorf("presence: refresh: %w", err)
		case ok == 0:
			errs[i] = ErrNotOwner
		}
	}
	return errs
}

var releaseScript = valkey.NewLuaScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1])
local t = redis.call('TIME')
local nowms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZADD', KEYS[3], string.format('%.0f', nowms + tonumber(ARGV[2])), ARGV[5])
if ARGV[4] ~= '' then
  redis.call('HSET', KEYS[2], ARGV[3], ARGV[4])
  ` + guestIndexLua + `
end
return 1
`)

// Release — стрим закрыт. Если соединение ещё владелец, ПК помечается «не в сети» (Online = false), а офлайн
// в Postgres запишет Sweeper через grace, если ПК не переподключится (дебаунс коротких разрывов).
// released = false — ПК уже держит другое соединение (вытеснение): ничего не меняется.
func (s *Store) Release(ctx context.Context, own Owner, st State, grace time.Duration) (released bool, err error) {
	st.Online = false
	b, err := json.Marshal(st)
	if err != nil {
		return false, err
	}
	n, err := releaseScript.Exec(ctx, s.c,
		s.guestIndexKeys([]string{s.pcKey(st.ComputerID), s.clubKey(st.ClubID), s.onlineKey()}, st.ClubID, st.SignedInUserID),
		[]string{own.String(), strconv.FormatInt(grace.Milliseconds(), 10), strconv.FormatInt(st.ComputerID, 10),
			string(b), member(st.ClubID, st.ComputerID), s.guestIndexTTLArg()}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("presence: release: %w", err)
	}
	return n == 1, nil
}

// OwnerOf — кто держит стрим ПК; ok = false — ПК не в сети.
func (s *Store) OwnerOf(ctx context.Context, computerID int64) (Owner, bool, error) {
	v, err := s.c.Do(ctx, s.c.B().Get().Key(s.pcKey(computerID)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return Owner{}, false, nil
	}
	if err != nil {
		return Owner{}, false, fmt.Errorf("presence: owner: %w", err)
	}
	o, ok := ParseOwner(v)
	return o, ok, nil
}

// staleAfter — снимок старше этого не показывается, даже если Sweeper ещё не убрал его.
func (s *Store) staleAfter() time.Duration { return 2 * s.ttl }

// Get — состояние ПК клуба; ok = false — ПК не в сети (и не в ожидании переподключения).
func (s *Store) Get(ctx context.Context, clubID, computerID int64) (State, bool, error) {
	v, err := s.c.Do(ctx, s.c.B().Hget().Key(s.clubKey(clubID)).Field(strconv.FormatInt(computerID, 10)).Build()).AsBytes()
	if valkey.IsValkeyNil(err) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("presence: get: %w", err)
	}
	var st State
	if err := json.Unmarshal(v, &st); err != nil {
		return State{}, false, fmt.Errorf("presence: decode: %w", err)
	}
	if st.ClubID != clubID || time.Since(st.LastSeenAt) > s.staleAfter() {
		return State{}, false, nil
	}
	return st, true, nil
}

// List — все ПК клуба, о которых есть живой снимок (в сети или в ожидании переподключения).
// Размер — число ПК клуба (сотни), поэтому без пагинации.
func (s *Store) List(ctx context.Context, clubID int64) ([]State, error) {
	s.lists.Add(1)
	m, err := s.c.Do(ctx, s.c.B().Hgetall().Key(s.clubKey(clubID)).Build()).AsStrMap()
	if err != nil {
		return nil, fmt.Errorf("presence: list: %w", err)
	}
	out := make([]State, 0, len(m))
	for _, v := range m {
		var st State
		if err := json.Unmarshal([]byte(v), &st); err != nil {
			continue
		}
		if st.ClubID != clubID || time.Since(st.LastSeenAt) > s.staleAfter() {
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

var sweepScript = valkey.NewLuaScript(`
local t = redis.call('TIME')
local nowms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local ms = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', string.format('%.0f', nowms), 'LIMIT', 0, tonumber(ARGV[2]))
local out = {}
for _, m in ipairs(ms) do
  local sep = string.find(m, ':', 1, true)
  local club = string.sub(m, 1, sep - 1)
  local pc = string.sub(m, sep + 1)
  local ttl = redis.call('PTTL', ARGV[1] .. 'pc:' .. pc)
  if ttl == -1 then ttl = tonumber(ARGV[3]) end
  if ttl > 0 then
    redis.call('ZADD', KEYS[1], string.format('%.0f', nowms + ttl), m)
  else
    redis.call('ZREM', KEYS[1], m)
    local hk = ARGV[1] .. 'club:' .. club
    local v = redis.call('HGET', hk, pc)
    redis.call('HDEL', hk, pc)
    table.insert(out, m)
    table.insert(out, v or '')
  end
end
return out
`)

// Gone — ПК, у которого истёк срок (стрим закрыт и не открылся за grace, или инстанс упал).
type Gone struct {
	ClubID, ComputerID int64
	// Last — последний снимок (может быть пустым).
	Last State
}

// ClaimGone забирает до limit ПК с истёкшим сроком: удаляет их из zset и снимка клуба. Атомарно — два
// исполнителя не получат один ПК. ПК, у которого есть живой владелец, получает новый срок по его TTL.
func (s *Store) ClaimGone(ctx context.Context, limit int) ([]Gone, error) {
	res, err := sweepScript.Exec(ctx, s.c, []string{s.onlineKey()},
		[]string{s.prefix, strconv.Itoa(limit), strconv.FormatInt(s.ttl.Milliseconds(), 10)}).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("presence: claim gone: %w", err)
	}
	out := make([]Gone, 0, len(res)/2)
	for i := 0; i+1 < len(res); i += 2 {
		club, pc, ok := parseMember(res[i])
		if !ok {
			continue
		}
		g := Gone{ClubID: club, ComputerID: pc}
		if res[i+1] != "" {
			_ = json.Unmarshal([]byte(res[i+1]), &g.Last)
		}
		out = append(out, g)
	}
	return out, nil
}

func parseMember(m string) (club, pc int64, ok bool) {
	a, b, found := strings.Cut(m, ":")
	if !found {
		return 0, 0, false
	}
	c, err1 := strconv.ParseInt(a, 10, 64)
	p, err2 := strconv.ParseInt(b, 10, 64)
	return c, p, err1 == nil && err2 == nil
}

// Known сообщает по каждому ПК, знает ли о нём Valkey: есть владелец или ПК ждёт проверки в zset.
// Для сверки «status = 'online' в Postgres, но в Valkey ничего» (падение инстанса без TTL, потеря Valkey).
func (s *Store) Known(ctx context.Context, pcs [][2]int64) ([]bool, error) {
	cmds := make(valkey.Commands, 0, 2*len(pcs))
	for _, p := range pcs {
		cmds = append(cmds,
			s.c.B().Exists().Key(s.pcKey(p[1])).Build(),
			s.c.B().Zscore().Key(s.onlineKey()).Member(member(p[0], p[1])).Build())
	}
	res := s.c.DoMulti(ctx, cmds...)
	out := make([]bool, len(pcs))
	for i := range pcs {
		n, err := res[2*i].AsInt64()
		if err != nil {
			return nil, fmt.Errorf("presence: exists: %w", err)
		}
		_, zerr := res[2*i+1].AsFloat64()
		if zerr != nil && !valkey.IsValkeyNil(zerr) {
			return nil, fmt.Errorf("presence: zscore: %w", zerr)
		}
		out[i] = n > 0 || zerr == nil
	}
	return out, nil
}

var unlockScript = valkey.NewLuaScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  if ARGV[2] == '0' then return redis.call('DEL', KEYS[1]) end
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

// TryLock берёт (или продлевает своим token) лок name на ttl — «один исполнитель» фоновой задачи.
func (s *Store) TryLock(ctx context.Context, name, token string, ttl time.Duration) (bool, error) {
	key := s.prefix + "lock:" + name
	err := s.c.Do(ctx, s.c.B().Set().Key(key).Value(token).Nx().Px(ttl).Build()).Error()
	if err == nil {
		return true, nil
	}
	if !valkey.IsValkeyNil(err) {
		return false, fmt.Errorf("presence: lock: %w", err)
	}
	n, err := unlockScript.Exec(ctx, s.c, []string{key}, []string{token, strconv.FormatInt(ttl.Milliseconds(), 10)}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("presence: lock: %w", err)
	}
	return n == 1, nil
}

// Unlock снимает лок, если он наш.
func (s *Store) Unlock(ctx context.Context, name, token string) error {
	return unlockScript.Exec(ctx, s.c, []string{s.prefix + "lock:" + name}, []string{token, "0"}).Error()
}
