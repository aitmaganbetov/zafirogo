package health

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// Почасовая история здоровья ПК (Этап 7, docs/agent-protocol.md §16.6, S7-H-13): агрегат часа по каждому ПК копится в
// Valkey из присылаемых Status (min/avg/max загрузки и температур, минимум свободного места системного диска, худший
// SMART, минуты онлайн и в сессии, отключения периферии, прирост внезапных выключений и «синих экранов»). В Postgres —
// одна строка на ПК и час после конца часа (HourlyFlusher), а не каждый Status (CLAUDE.md «Производительность»).
//
// Агрегат — в Valkey, а не в памяти инстанса: ПК может переехать на другой инстанс посреди часа (перебалансировка,
// переподключение), и оба пишут в один ключ «ПК + час» — ничего не теряется и не дублируется. Минуты онлайн — битовая
// карта (повтор той же минуты с любого инстанса идемпотентен), прирост счётчиков сбоев — от последнего значения ПК в
// Valkey (а не в памяти подключения: внезапное выключение обнаруживается как раз после переподключения).
//
// Ключи (префикс — presence.Store.Prefix(), «zafiro:agent:»; Valkey без кластера, как presence.Store):
//
//	<p>hh:<hour>:<computer>   — hash агрегата часа (hour — Unix-секунды начала часа UTC), TTL HourlyKeyTTL;
//	<p>hhb:<hour>:<computer>  — битовая карта минут: биты 0..59 — онлайн, 64..123 — в сессии;
//	<p>hhi:<hour>             — set «club:computer» ПК, у которых есть агрегат этого часа (что сбрасывать);
//	<p>hhl:<computer>         — последние счётчики сбоев агента за 7 суток (sd, bc), TTL 8 суток.

// HourlyKeyTTL — сколько живёт агрегат часа в Valkey, если его так и не сбросили (сброс оглядывается на сутки назад).
const HourlyKeyTTL = 26 * time.Hour

// lastCountersTTL — последние счётчики сбоев ПК: дольше окна агента (7 суток), чтобы ПК, выключенный на неделю, после
// включения не посчитал старые сбои заново.
const lastCountersTTL = 8 * 24 * time.Hour

// Smart-ранги агрегата: чем больше, тем хуже (0 — не измерено).
const (
	smartRankOK      = 1
	smartRankWarning = 2
	smartRankFailing = 3
)

func smartRank(s string) int {
	switch s {
	case presence.SmartOK:
		return smartRankOK
	case presence.SmartWarning:
		return smartRankWarning
	case presence.SmartFailing:
		return smartRankFailing
	default:
		return 0
	}
}

// SmartName — ранг в значение SMART ("" — не измерено).
func SmartName(rank int) string {
	switch rank {
	case smartRankOK:
		return presence.SmartOK
	case smartRankWarning:
		return presence.SmartWarning
	case smartRankFailing:
		return presence.SmartFailing
	default:
		return ""
	}
}

// Sample — один принятый Status для почасовой истории.
type Sample struct {
	ClubID, ComputerID int64
	// At — когда сервер принял Status (часы сервера, не агента).
	At time.Time
	// Since — предыдущий учтённый Status этого подключения (нуль — первый): минуты между ними тоже онлайн, если разрыв
	// не больше MaxGap (Status приходит раз в минуту и может «перешагнуть» минуту).
	Since time.Time
	// InSession — ПК в сессии (agent_state in_session или есть session_id).
	InSession bool
	// Load — загрузка из ЭТОГО Status (nil — в Status её нет; прежняя не учитывается повторно).
	Load *presence.Load
	// Health — здоровье из ЭТОГО Status (nil — нет).
	Health *presence.Health
	// PeripheralDisconnects — сколько устройств отключилось с прошлого Status (PeripheralDisconnects).
	PeripheralDisconnects int
}

// Hourly — запись измерений в агрегат часа (Valkey). Потокобезопасен.
type Hourly struct {
	c      valkey.Client
	prefix string
	// MaxGap — наибольший разрыв между Status одного подключения, при котором минуты между ними считаются онлайн.
	MaxGap time.Duration
	writes atomic.Int64
}

// NewHourly создаёт запись агрегатов. prefix — префикс ключей присутствия (presence.Store.Prefix()).
func NewHourly(c valkey.Client, prefix string, maxGap time.Duration) *Hourly {
	if prefix == "" {
		prefix = presence.DefaultPrefix
	}
	if maxGap <= 0 {
		maxGap = 150 * time.Second
	}
	return &Hourly{c: c, prefix: prefix, MaxGap: maxGap}
}

// Writes — сколько измерений записано (тесты, замеры).
func (h *Hourly) Writes() int64 { return h.writes.Load() }

func hourKeyPart(hour time.Time) string { return strconv.FormatInt(hour.Unix(), 10) }

func (h *Hourly) aggKey(hour time.Time, computerID int64) string {
	return h.prefix + "hh:" + hourKeyPart(hour) + ":" + strconv.FormatInt(computerID, 10)
}
func (h *Hourly) bitsKey(hour time.Time, computerID int64) string {
	return h.prefix + "hhb:" + hourKeyPart(hour) + ":" + strconv.FormatInt(computerID, 10)
}
func (h *Hourly) indexKey(hour time.Time) string { return h.prefix + "hhi:" + hourKeyPart(hour) }
func (h *Hourly) lastKey(computerID int64) string {
	return h.prefix + "hhl:" + strconv.FormatInt(computerID, 10)
}

// OnlineMinutes — минуты часа at (0..59), отмечаемые этим Status: минута at и, если предыдущий Status был не раньше
// maxGap назад, все минуты с него (только внутри текущего часа: прошлый час мог быть уже сброшен в Postgres).
func OnlineMinutes(since, at time.Time, maxGap time.Duration) []int {
	at = at.UTC()
	hour := at.Truncate(time.Hour)
	cur := int(at.Sub(hour) / time.Minute)
	from := cur
	if !since.IsZero() && !since.After(at) && at.Sub(since) <= maxGap {
		if since.Before(hour) {
			from = 0
		} else {
			from = int(since.Sub(hour) / time.Minute)
		}
	}
	out := make([]int, 0, cur-from+1)
	for m := from; m <= cur; m++ {
		out = append(out, m)
	}
	return out
}

// SessionMinutes — минуты в сессии, которые отмечает Status «в сессии»: те же, что онлайн, кроме минуты предыдущего
// Status (тогда ПК мог быть ещё не в сессии — её отметил предыдущий Status, если был в сессии).
func SessionMinutes(online []int, since, at time.Time) []int {
	hour := at.UTC().Truncate(time.Hour)
	if len(online) > 1 && !since.IsZero() && since.UTC().Truncate(time.Hour).Equal(hour) &&
		online[0] == int(since.UTC().Sub(hour)/time.Minute) {
		return online[1:]
	}
	return online
}

// PeripheralDisconnects — сколько устройств мыши, клавиатуры и монитора было подключено в prev и не подключено (или
// пропало из списка) в cur. Гарнитура не считается (её снимают и уносят — это не неисправность, S7-H-1). Нет одного из
// снимков — 0 (не с чем сравнить: первое здоровье подключения).
func PeripheralDisconnects(prev, cur *presence.Health) int {
	if prev == nil || cur == nil {
		return 0
	}
	type dev struct{ kind, name string }
	was := map[dev]int{}
	for _, p := range prev.Peripherals {
		if p.Connected && p.Kind != presence.PeripheralHeadset {
			was[dev{p.Kind, p.Name}]++
		}
	}
	for _, p := range cur.Peripherals {
		if p.Connected {
			was[dev{p.Kind, p.Name}]--
		}
	}
	n := 0
	for _, c := range was {
		if c > 0 {
			n += c
		}
	}
	return n
}

// hourlyScript — атомарное обновление агрегата часа.
//
//	KEYS: 1 агрегат (hash), 2 минуты (bitmap), 3 последние счётчики ПК (hash), 4 индекс часа (set)
//	ARGV: 1 TTL агрегата (с), 2 член индекса, 3 CPU, 4 ОЗУ, 5 GPU (сотые доли %), 6 t CPU, 7 t GPU (десятые °C) — -1 нет;
//	      8 свободно на системном диске (МБ, -1 нет), 9 объём (МБ), 10 ранг SMART (0 нет), 11 отключений периферии,
//	      12 внезапных выключений за 7 суток, 13 «синих экранов» за 7 суток (-1 нет), 14 не используется,
//	      15 TTL последних счётчиков (с), 16 N — число минут онлайн, 17…16+N минуты онлайн, дальше — минуты в сессии.
var hourlyScript = valkey.NewLuaScript(`
local h = KEYS[1]
local function acc(f, s)
  local v = tonumber(s)
  if v < 0 then return end
  redis.call('HINCRBY', h, f .. '_n', 1)
  redis.call('HINCRBY', h, f .. '_s', s)
  local mn = redis.call('HGET', h, f .. '_mn')
  if not mn or v < tonumber(mn) then redis.call('HSET', h, f .. '_mn', s) end
  local mx = redis.call('HGET', h, f .. '_mx')
  if not mx or v > tonumber(mx) then redis.call('HSET', h, f .. '_mx', s) end
end
acc('cpu', ARGV[3])
acc('ram', ARGV[4])
acc('gl', ARGV[5])
acc('ct', ARGV[6])
acc('gt', ARGV[7])
if tonumber(ARGV[8]) >= 0 then
  local cur = redis.call('HGET', h, 'df')
  if not cur or tonumber(ARGV[8]) < tonumber(cur) then redis.call('HSET', h, 'df', ARGV[8], 'dt', ARGV[9]) end
end
if tonumber(ARGV[10]) > 0 then
  local cur = tonumber(redis.call('HGET', h, 'sm') or '0')
  if tonumber(ARGV[10]) > cur then redis.call('HSET', h, 'sm', ARGV[10]) end
end
if tonumber(ARGV[11]) > 0 then redis.call('HINCRBY', h, 'pd', ARGV[11]) end
local function delta(f, s)
  local v = tonumber(s)
  if v < 0 then return end
  local last = redis.call('HGET', KEYS[3], f)
  if last and v > tonumber(last) then redis.call('HINCRBY', h, f, v - tonumber(last)) end
  redis.call('HSET', KEYS[3], f, s)
end
delta('sd', ARGV[12])
delta('bc', ARGV[13])
redis.call('EXPIRE', KEYS[3], ARGV[15])
local n = tonumber(ARGV[16])
for i = 17, #ARGV do
  local m = tonumber(ARGV[i])
  if i <= 16 + n then redis.call('SETBIT', KEYS[2], m, 1) else redis.call('SETBIT', KEYS[2], m + 64, 1) end
end
redis.call('HSET', h, 'v', '1')
redis.call('EXPIRE', h, ARGV[1])
redis.call('EXPIRE', KEYS[2], ARGV[1])
redis.call('SADD', KEYS[4], ARGV[2])
redis.call('EXPIRE', KEYS[4], ARGV[1])
return 1
`)

func i64(v int64) string { return strconv.FormatInt(v, 10) }

// Record добавляет измерение в агрегат часа s.At (одна команда Valkey).
func (h *Hourly) Record(ctx context.Context, s Sample) error {
	if h == nil || s.ClubID <= 0 || s.ComputerID <= 0 {
		return nil
	}
	at := s.At.UTC()
	hour := at.Truncate(time.Hour)
	cpu, ram, gl, ct, gt := int64(-1), int64(-1), int64(-1), int64(-1), int64(-1)
	free, total, smart, sd, bc := int64(-1), int64(0), 0, int64(-1), int64(-1)
	if s.Load != nil {
		cpu, ram = int64(s.Load.CPUBasisPoints), int64(s.Load.RAMBasisPoints)
	}
	if hh := s.Health; hh != nil {
		if hh.GPUMeasured {
			gl = int64(hh.GPULoadBP)
		}
		if hh.CPUTempDeci > 0 {
			ct = int64(hh.CPUTempDeci)
		}
		if hh.GPUTempDeci > 0 {
			gt = int64(hh.GPUTempDeci)
		}
		for _, d := range hh.Disks {
			if r := smartRank(d.Smart); r > smart {
				smart = r
			}
			if d.System && d.TotalMB > 0 && free < 0 {
				free, total = int64(d.FreeMB), int64(d.TotalMB) // #nosec G115 -- ≤ 1 ПиБ в МБ (stream.validateHealth)
			}
		}
		sd, bc = int64(hh.Shutdowns7d), int64(hh.Bugchecks7d)
	}
	online := OnlineMinutes(s.Since, at, h.MaxGap)
	var session []int
	if s.InSession {
		session = SessionMinutes(online, s.Since, at)
	}
	args := []string{
		i64(int64(HourlyKeyTTL / time.Second)), strconv.FormatInt(s.ClubID, 10) + ":" + strconv.FormatInt(s.ComputerID, 10),
		i64(cpu), i64(ram), i64(gl), i64(ct), i64(gt), i64(free), i64(total), strconv.Itoa(smart),
		strconv.Itoa(max(s.PeripheralDisconnects, 0)), i64(sd), i64(bc), "0", i64(int64(lastCountersTTL / time.Second)),
		strconv.Itoa(len(online)),
	}
	for _, m := range append(online, session...) {
		args = append(args, strconv.Itoa(m))
	}
	keys := []string{h.aggKey(hour, s.ComputerID), h.bitsKey(hour, s.ComputerID), h.lastKey(s.ComputerID), h.indexKey(hour)}
	if err := hourlyScript.Exec(ctx, h.c, keys, args).Error(); err != nil {
		return fmt.Errorf("health: hourly record: %w", err)
	}
	h.writes.Add(1)
	return nil
}
