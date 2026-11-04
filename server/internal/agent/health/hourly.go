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
//	<p>hhc:<hour>             — «час закрыт»: HourlyFlusher начал его сбрасывать, измерения в него больше не пишутся;
//	<p>hhl:<computer>         — последние счётчики сбоев агента: за 7 суток (sd, bc) и перед загрузкой (boot, sdb, bcb),
//	                            TTL 8 суток.

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
	// BootID — Hello.boot_id подключения: прирост сбоев по счётчикам «перед этой загрузкой» (Health.CrashesBootMeasured).
	BootID string
}

// Hourly — запись измерений в агрегат часа (Valkey). Потокобезопасен.
type Hourly struct {
	c      valkey.Client
	prefix string
	// MaxGap — наибольший разрыв между Status одного подключения, при котором минуты между ними считаются онлайн.
	MaxGap time.Duration
	writes atomic.Int64
	late   atomic.Int64
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

// Late — сколько измерений пришло в уже закрытый (сброшенный) час и не записано.
func (h *Hourly) Late() int64 { return h.late.Load() }

func hourKeyPart(hour time.Time) string { return strconv.FormatInt(hour.Unix(), 10) }

func (h *Hourly) aggKey(hour time.Time, computerID int64) string {
	return h.prefix + "hh:" + hourKeyPart(hour) + ":" + strconv.FormatInt(computerID, 10)
}
func (h *Hourly) bitsKey(hour time.Time, computerID int64) string {
	return h.prefix + "hhb:" + hourKeyPart(hour) + ":" + strconv.FormatInt(computerID, 10)
}
func (h *Hourly) indexKey(hour time.Time) string  { return h.prefix + "hhi:" + hourKeyPart(hour) }
func (h *Hourly) closedKey(hour time.Time) string { return h.prefix + "hhc:" + hourKeyPart(hour) }
func (h *Hourly) lastKey(computerID int64) string {
	return h.prefix + "hhl:" + strconv.FormatInt(computerID, 10)
}

// OnlineMinutes — минуты онлайн, которые отмечает Status в момент at: cur — минуты часа at (0..59): минута at и, если
// предыдущий Status был не раньше maxGap назад, все минуты с него; prev — минуты ПРЕДЫДУЩЕГО часа с предыдущего Status
// (Status раз в минуту «перешагнул» границу часа: 09:58:50 → 10:00:10 — минута 59 часа 09 тоже онлайн). Раньше
// предыдущего часа минуты не отмечаются (разрыв больше часа — не онлайн при любом разумном maxGap).
func OnlineMinutes(since, at time.Time, maxGap time.Duration) (prev, cur []int) {
	at = at.UTC()
	hour := at.Truncate(time.Hour)
	now := int(at.Sub(hour) / time.Minute)
	from := now
	if !since.IsZero() && !since.After(at) && at.Sub(since) <= maxGap {
		since = since.UTC()
		if since.Before(hour) {
			from = 0
			if p := hour.Add(-time.Hour); !since.Before(p) {
				prev = minuteRange(int(since.Sub(p)/time.Minute), 59)
			} else {
				prev = minuteRange(0, 59)
			}
		} else {
			from = int(since.Sub(hour) / time.Minute)
		}
	}
	return prev, minuteRange(from, now)
}

func minuteRange(from, to int) []int {
	out := make([]int, 0, to-from+1)
	for m := from; m <= to; m++ {
		out = append(out, m)
	}
	return out
}

// SessionMinutes — минуты в сессии, которые отмечает Status «в сессии»: те же, что онлайн (prev, cur — OnlineMinutes),
// кроме минуты предыдущего Status (тогда ПК мог быть ещё не в сессии — её отметил предыдущий Status, если был в сессии).
func SessionMinutes(prev, cur []int, since, at time.Time) (sPrev, sCur []int) {
	if since.IsZero() {
		return prev, cur
	}
	since, hour := since.UTC(), at.UTC().Truncate(time.Hour)
	switch {
	case len(prev) > 0 && since.Truncate(time.Hour).Equal(hour.Add(-time.Hour)) &&
		prev[0] == int(since.Sub(hour.Add(-time.Hour))/time.Minute):
		return prev[1:], cur
	case len(cur) > 1 && since.Truncate(time.Hour).Equal(hour) && cur[0] == int(since.Sub(hour)/time.Minute):
		return prev, cur[1:]
	}
	return prev, cur
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
//	KEYS: 1 агрегат (hash), 2 минуты (bitmap), 3 последние счётчики ПК (hash), 4 индекс часа (set), 5 «час закрыт»
//	      (HourlyFlusher уже сбрасывает или сбросил час), 6–9 — то же для предыдущего часа (минуты разрыва через границу).
//	ARGV: 1 TTL агрегата (с), 2 член индекса, 3 CPU, 4 ОЗУ, 5 GPU (сотые доли %), 6 t CPU, 7 t GPU (десятые °C) — -1 нет;
//	      8 свободно на системном диске (МБ, -1 нет), 9 объём (МБ), 10 ранг SMART (0 нет), 11 отключений периферии,
//	      12 внезапных выключений за 7 суток, 13 «синих экранов» за 7 суток (-1 нет), 14 внезапных выключений и
//	      15 «синих экранов» перед этой загрузкой Windows (-1 нет: старый агент), 16 boot_id, 17 TTL последних счётчиков (с),
//	      18–21 — число минут: онлайн и в сессии этого часа, онлайн и в сессии прошлого часа; дальше — сами минуты.
//
// Возврат: 1 — записано, 0 — час уже закрыт (опоздавшее измерение не пишется: иначе после сброса появился бы частичный
// агрегат и перезаписал бы полную строку часа в Postgres). Счётчики сбоев тогда тоже не трогаются — прирост попадёт в
// час следующего измерения.
var hourlyScript = valkey.NewLuaScript(`
if redis.call('EXISTS', KEYS[5]) == 1 then return 0 end
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
local L = KEYS[3]
local function add(f, n) if n > 0 then redis.call('HINCRBY', h, f, n) end end
local function delta(f, s)
  local v = tonumber(s)
  if v < 0 then return end
  local last = redis.call('HGET', L, f)
  if last then add(f, v - tonumber(last)) end
end
local sdb, bcb, boot = tonumber(ARGV[14]), tonumber(ARGV[15]), ARGV[16]
local lastBoot = redis.call('HGET', L, 'boot')
if sdb >= 0 and boot ~= '' and lastBoot then
  -- Сбои перед загрузкой: события пишутся при загрузке после сбоя и из окна «с загрузки» не выпадают — прирост точный.
  if lastBoot == boot then
    add('sd', sdb - tonumber(redis.call('HGET', L, 'sdb') or '0'))
    add('bc', bcb - tonumber(redis.call('HGET', L, 'bcb') or '0'))
  else
    add('sd', sdb)
    add('bc', bcb)
  end
else
  delta('sd', ARGV[12])
  delta('bc', ARGV[13])
end
if tonumber(ARGV[12]) >= 0 then redis.call('HSET', L, 'sd', ARGV[12], 'bc', ARGV[13]) end
if sdb >= 0 and boot ~= '' then redis.call('HSET', L, 'boot', boot, 'sdb', ARGV[14], 'bcb', ARGV[15]) end
redis.call('EXPIRE', L, ARGV[17])
local function minutes(bits, from, n, off)
  for i = from, from + n - 1 do redis.call('SETBIT', bits, tonumber(ARGV[i]) + off, 1) end
  return from + n
end
local i = minutes(KEYS[2], 22, tonumber(ARGV[18]), 0)
i = minutes(KEYS[2], i, tonumber(ARGV[19]), 64)
redis.call('HSET', h, 'v', '1')
redis.call('EXPIRE', h, ARGV[1])
redis.call('EXPIRE', KEYS[2], ARGV[1])
redis.call('SADD', KEYS[4], ARGV[2])
redis.call('EXPIRE', KEYS[4], ARGV[1])
local np, nps = tonumber(ARGV[20]), tonumber(ARGV[21])
if np > 0 and redis.call('EXISTS', KEYS[9]) == 0 then
  i = minutes(KEYS[7], i, np, 0)
  minutes(KEYS[7], i, nps, 64)
  redis.call('HSET', KEYS[6], 'v', '1')
  redis.call('EXPIRE', KEYS[6], ARGV[1])
  redis.call('EXPIRE', KEYS[7], ARGV[1])
  redis.call('SADD', KEYS[8], ARGV[2])
  redis.call('EXPIRE', KEYS[8], ARGV[1])
end
return 1
`)

func i64(v int64) string { return strconv.FormatInt(v, 10) }

// diskFacts — свободное место и объём системного диска (-1 — не измерено) и худший SMART измерения. Бездисковый ПК
// (S7-H-18) — «не измерено»: тома образа к ПК не относятся.
func diskFacts(hh *presence.Health) (free, total int64, smart int) {
	free = -1
	if hh.Diskless {
		return free, 0, 0
	}
	for _, d := range hh.Disks {
		if r := smartRank(d.Smart); r > smart {
			smart = r
		}
		if d.System && d.TotalMB > 0 && free < 0 {
			free, total = int64(d.FreeMB), int64(d.TotalMB) // #nosec G115 -- ≤ 1 ПиБ в МБ (stream.validateHealth)
		}
	}
	return free, total, smart
}

// Record добавляет измерение в агрегат часа s.At (одна команда Valkey). Час уже закрыт сбросом (опоздавшее
// измерение: часы инстанса отстали больше DefaultHourlyDelay или обработка задержалась) — измерение не пишется
// (Late()), ошибки нет.
func (h *Hourly) Record(ctx context.Context, s Sample) error {
	if h == nil || s.ClubID <= 0 || s.ComputerID <= 0 {
		return nil
	}
	at := s.At.UTC()
	hour := at.Truncate(time.Hour)
	cpu, ram, gl, ct, gt := int64(-1), int64(-1), int64(-1), int64(-1), int64(-1)
	free, total, smart, sd, bc, sdb, bcb := int64(-1), int64(0), 0, int64(-1), int64(-1), int64(-1), int64(-1)
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
		free, total, smart = diskFacts(hh)
		sd, bc = int64(hh.Shutdowns7d), int64(hh.Bugchecks7d)
		if hh.CrashesBootMeasured {
			sdb, bcb = int64(hh.ShutdownsBoot), int64(hh.BugchecksBoot)
		}
	}
	prevOnline, online := OnlineMinutes(s.Since, at, h.MaxGap)
	var prevSession, session []int
	if s.InSession {
		prevSession, session = SessionMinutes(prevOnline, online, s.Since, at)
	}
	args := []string{
		i64(int64(HourlyKeyTTL / time.Second)), strconv.FormatInt(s.ClubID, 10) + ":" + strconv.FormatInt(s.ComputerID, 10),
		i64(cpu), i64(ram), i64(gl), i64(ct), i64(gt), i64(free), i64(total), strconv.Itoa(smart),
		strconv.Itoa(max(s.PeripheralDisconnects, 0)), i64(sd), i64(bc), i64(sdb), i64(bcb), s.BootID,
		i64(int64(lastCountersTTL / time.Second)),
		strconv.Itoa(len(online)), strconv.Itoa(len(session)), strconv.Itoa(len(prevOnline)), strconv.Itoa(len(prevSession)),
	}
	for _, list := range [][]int{online, session, prevOnline, prevSession} {
		for _, m := range list {
			args = append(args, strconv.Itoa(m))
		}
	}
	prev := hour.Add(-time.Hour)
	keys := []string{h.aggKey(hour, s.ComputerID), h.bitsKey(hour, s.ComputerID), h.lastKey(s.ComputerID), h.indexKey(hour),
		h.closedKey(hour), h.aggKey(prev, s.ComputerID), h.bitsKey(prev, s.ComputerID), h.indexKey(prev), h.closedKey(prev)}
	n, err := hourlyScript.Exec(ctx, h.c, keys, args).AsInt64()
	if err != nil {
		return fmt.Errorf("health: hourly record: %w", err)
	}
	if n == 0 {
		h.late.Add(1)
		return nil
	}
	h.writes.Add(1)
	return nil
}
