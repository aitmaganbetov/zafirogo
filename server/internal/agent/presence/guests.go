package presence

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Индекс «гость → ПК» (docs/open-questions/stage6-load.md S6-O4-11): уведомлению о балансе гостя (BalanceChanged)
// нужны ПК клуба, где он вошёл, — без чтения и разбора снимков всех ПК клуба (List: HGETALL + JSON на каждое
// изменение баланса; клуб на 500 ПК в пиковую минуту — тысячи разборов в секунду).
//
//	<p>guests:since          — с какого момента (мс Valkey) индекс поддерживается без перерыва; TTL = guestIndexTTL,
//	                           продлевается каждой записью снимка;
//	<p>guest:<club>:<user>   — SET номеров ПК, в чьих снимках был этот гость; TTL = guestIndexTTL.
//
// Поддерживается там же, где пишется снимок ПК (refreshScript, releaseScript — вход и выход гостя приходят в Status,
// смена ПК и переподключение на другой инстанс — новым снимком). Индекс — надмножество: ПК добавляется при
// каждой записи снимка с гостем и не удаляется при выходе, смене ПК, закрытии стрима или уборке Sweeper —
// SignedIn проверяет каждый ПК по его текущему снимку (как List) и в том же скрипте убирает из индекса ПК, чей
// снимок исчез или показывает другого гостя. Пропусков нет, пока индекс поддерживается: каждый живой снимок
// (List не показывает снимки старше staleAfter = 2 × TTL) записан не раньше чем guestIndexTTL назад.
//
// Индекс может быть неполон в первые guestIndexWarmup после появления (выкладка версии с индексом, пока
// работают инстансы без него; потеря данных Valkey; откат на старую версию — метка истекает сама). Тогда
// SignedIn сообщает ready = false, и вызывающий читает клуб целиком (прежний путь List).

// guestIndexLua — добавка к скриптам записи снимка (refreshScript, releaseScript): метка «индекс поддерживается»
// и ПК в индексе гостя. KEYS[4] — метка, KEYS[5] — индекс гостя (нет — в снимке нет гостя); ARGV[3] — ПК,
// ARGV[6] — TTL индекса (мс); nowms — время Valkey (вычислено в скрипте выше).
const guestIndexLua = `if KEYS[4] then redis.call('SET', KEYS[4], string.format('%.0f', nowms), 'NX'); redis.call('PEXPIRE', KEYS[4], ARGV[6]) end
  if KEYS[5] then redis.call('SADD', KEYS[5], ARGV[3]); redis.call('PEXPIRE', KEYS[5], ARGV[6]) end`

// guestIndexWarmup — сколько после появления метки индексу не доверять (выкладка с перекрытием версий
// должна уложиться; см. docs/observability.md). Должно быть ≥ staleAfter.
const guestIndexWarmup = 10 * time.Minute

// guestIndexTTL — срок индекса гостя и метки: вдвое больше staleAfter (запас на расхождение часов инстансов).
func (s *Store) guestIndexTTL() time.Duration { return 2 * s.staleAfter() }

func (s *Store) guestIndexTTLArg() string {
	return strconv.FormatInt(s.guestIndexTTL().Milliseconds(), 10)
}

func (s *Store) guestSinceKey() string { return s.prefix + "guests:since" }

func (s *Store) guestKey(clubID, userID int64) string {
	return s.prefix + "guest:" + strconv.FormatInt(clubID, 10) + ":" + strconv.FormatInt(userID, 10)
}

// guestIndexKeys дополняет ключи скрипта записи снимка: метка и (если в снимке есть гость) его индекс.
func (s *Store) guestIndexKeys(keys []string, clubID, userID int64) []string {
	keys = append(keys, s.guestSinceKey())
	if userID > 0 {
		keys = append(keys, s.guestKey(clubID, userID))
	}
	return keys
}

// signedInScript: ПК гостя по индексу с их снимками; ПК без снимка или со снимком другого гостя/клуба убираются из
// индекса (атомарно с проверкой — запись снимка между чтением и удалением невозможна). Индекс ещё не готов —
// {'-'}. KEYS: метка, индекс гостя, снимки клуба. ARGV: warmup (мс), club, user.
var signedInScript = valkey.NewLuaScript(`
local since = redis.call('GET', KEYS[1])
if not since then return {'-'} end
local t = redis.call('TIME')
local nowms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
if nowms - tonumber(since) < tonumber(ARGV[1]) then return {'-'} end
local out = {}
for _, pc in ipairs(redis.call('SMEMBERS', KEYS[2])) do
  local v = redis.call('HGET', KEYS[3], pc)
  local keep = false
  if v then
    local ok, st = pcall(cjson.decode, v)
    keep = ok and type(st) == 'table' and st['club_id'] == tonumber(ARGV[2]) and st['signed_in_user_id'] == tonumber(ARGV[3])
  end
  if keep then table.insert(out, v) else redis.call('SREM', KEYS[2], pc) end
end
return out
`)

// SignedIn — живые снимки ПК клуба, где сейчас вошёл гость userID (Online не проверяется — как у List).
// ready = false — индекс ещё не готов (см. guestIndexWarmup): ответ пуст, нужен прежний путь через List.
func (s *Store) SignedIn(ctx context.Context, clubID, userID int64) (out []State, ready bool, err error) {
	if clubID <= 0 || userID <= 0 {
		return nil, true, nil
	}
	res, err := signedInScript.Exec(ctx, s.c, []string{s.guestSinceKey(), s.guestKey(clubID, userID), s.clubKey(clubID)},
		[]string{strconv.FormatInt(guestIndexWarmup.Milliseconds(), 10), strconv.FormatInt(clubID, 10),
			strconv.FormatInt(userID, 10)}).AsStrSlice()
	if err != nil {
		return nil, false, fmt.Errorf("presence: signed in: %w", err)
	}
	if len(res) == 1 && res[0] == "-" {
		return nil, false, nil
	}
	for _, v := range res {
		var st State
		if err := json.Unmarshal([]byte(v), &st); err != nil {
			continue
		}
		// Та же отсечка устаревших снимков, что у List.
		if st.ClubID != clubID || st.SignedInUserID != userID || time.Since(st.LastSeenAt) > s.staleAfter() {
			continue
		}
		out = append(out, st)
	}
	return out, true, nil
}

// SignedInOrList — ПК клуба, где вошёл гость: по индексу, а пока индекс не готов — из List (прежний путь).
func (s *Store) SignedInOrList(ctx context.Context, clubID, userID int64) ([]State, error) {
	pcs, ready, err := s.SignedIn(ctx, clubID, userID)
	if err != nil || ready {
		return pcs, err
	}
	all, err := s.List(ctx, clubID)
	if err != nil {
		return nil, err
	}
	for _, st := range all {
		if st.SignedInUserID == userID {
			pcs = append(pcs, st)
		}
	}
	return pcs, nil
}

// ListCalls — сколько раз вызван List (тесты: уведомления о балансе не читают клуб целиком).
func (s *Store) ListCalls() int64 { return s.lists.Load() }
