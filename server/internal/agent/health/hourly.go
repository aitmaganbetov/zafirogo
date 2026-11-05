package health

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// Почасовая история здоровья ПК (Этап 7, docs/agent-protocol.md §16.6, S7-H-13): агрегат часа по каждому ПК копится в
// Valkey из присылаемых Status (avg/max загрузки и температур, минимум свободного места системного диска, худший
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
//	<p>hhl:<computer>         — последние счётчики сбоев агента перед загрузкой с её boot_id (boot, sdb, bcb), TTL 8 суток.

// HourlyKeyTTL — сколько живёт агрегат часа в Valkey, если его так и не сбросили (сброс оглядывается на сутки назад).
const HourlyKeyTTL = 26 * time.Hour

// lastCountersTTL — последние счётчики сбоев ПК перед загрузкой: пока они есть, сбой перед следующей загрузкой
// засчитывается и после недели выключения; без них первое значение ПК — 0 (старые сбои не засчитываются).
const lastCountersTTL = 8 * 24 * time.Hour

// Smart-ранги агрегата: чем больше, тем хуже (0 — не измерено). Те же числа считает SQL недель
// (db/queries/computer_health_hourly.sql ComputerHealthWeekly smart_rank) — меняются вместе.
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

// hourlyKeys — ключи почасовой истории от префикса присутствия (общие для Hourly и HourlyFlusher). Час и ПК — уже
// строками (hourPart, strconv.FormatInt): Record считает их один раз на измерение.
type hourlyKeys struct{ prefix string }

func newHourlyKeys(prefix string) hourlyKeys {
	if prefix == "" {
		prefix = presence.DefaultPrefix
	}
	return hourlyKeys{prefix: prefix}
}

func hourPart(hour time.Time) string { return strconv.FormatInt(hour.Unix(), 10) }

func (k hourlyKeys) agg(hour, pc string) string  { return k.prefix + "hh:" + hour + ":" + pc }
func (k hourlyKeys) bits(hour, pc string) string { return k.prefix + "hhb:" + hour + ":" + pc }
func (k hourlyKeys) index(hour string) string    { return k.prefix + "hhi:" + hour }
func (k hourlyKeys) closed(hour string) string   { return k.prefix + "hhc:" + hour }
func (k hourlyKeys) last(pc string) string       { return k.prefix + "hhl:" + pc }

// Hourly — запись измерений в агрегат часа (Valkey). Потокобезопасен.
type Hourly struct {
	c    valkey.Client
	keys hourlyKeys
	// maxGap — наибольший разрыв между Status одного подключения, при котором минуты между ними считаются онлайн.
	maxGap time.Duration
	writes atomic.Int64
	late   atomic.Int64
}

// NewHourly создаёт запись агрегатов. prefix — префикс ключей присутствия (presence.Store.Prefix()).
func NewHourly(c valkey.Client, prefix string, maxGap time.Duration) *Hourly {
	if maxGap <= 0 {
		maxGap = 150 * time.Second
	}
	return &Hourly{c: c, keys: newHourlyKeys(prefix), maxGap: maxGap}
}

// Writes — сколько измерений записано (тесты, замеры).
func (h *Hourly) Writes() int64 { return h.writes.Load() }

// Late — сколько измерений пришло в уже закрытый (сброшенный) час и не записано.
func (h *Hourly) Late() int64 { return h.late.Load() }

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
	if prev == nil || cur == nil || slices.Equal(prev.Peripherals, cur.Peripherals) {
		return 0 // нечего сравнивать или периферия не изменилась (почти каждый Status) — без map
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
//	      12 внезапных выключений и 13 «синих экранов» перед этой загрузкой Windows (-1 — не измерено:
//	      crashes_boot_measured = false), 14 boot_id, 15 TTL последних счётчиков (с), 16–19 — число минут: онлайн и в
//	      сессии этого часа, онлайн и в сессии прошлого часа; дальше — сами минуты.
//
// Немного команд на Status (их 5 000–50 000 в минуту): агрегат читается одним HMGET, avg (сумма и число) и max считает
// Lua, запись — одним HSET со всеми полями, минуты — одним BITFIELD; последние счётчики сбоев читаются и пишутся, только
// если сбои перед загрузкой измерены; TTL ставится, когда ключ создан (агрегат — по полю v, член индекса — по ответу
// SADD), а не на каждый Status. Минимумы не считаются (S7-H-26: для решения о ремонте нужны максимумы и средние).
// Поля и значения агрегата — те же, что давали HINCRBY / HSET по полю (hourly_script_integration_test.go сверяет
// побитно). Суммы — целые много меньше 10^14 (tostring без экспоненты; ≤ 1 800 Status в час × 10 000).
//
// Прирост сбоев за час (поля sd, bc агрегата, S7-H-20, S7-H-27) — только по счётчикам «перед загрузкой» и boot_id: та же
// загрузка — прирост от прошлого значения, новая — всё значение, первое значение ПК — 0 (старые сбои не засчитываются).
// Не измерено (crashes_boot_measured = false) — сбои в почасовую историю не добавляются.
//
// Возврат: 1 — записано, 0 — час уже закрыт (опоздавшее измерение не пишется: иначе после сброса появился бы частичный
// агрегат и перезаписал бы полную строку часа в Postgres). Счётчики сбоев тогда тоже не трогаются — прирост попадёт в
// час следующего измерения.
var hourlyScript = valkey.NewLuaScript(`
if redis.call('EXISTS', KEYS[5]) == 1 then return 0 end
local h, ttl, member = KEYS[1], ARGV[1], ARGV[2]
-- Поля агрегата: 1 v, 2 df, 3 sm, 4 pd, 5 sd, 6 bc, затем по 3 на показатель (_n, _s, _mx) в порядке ARGV 3..7.
local cur = redis.call('HMGET', h, 'v', 'df', 'sm', 'pd', 'sd', 'bc',
  'cpu_n', 'cpu_s', 'cpu_mx', 'ram_n', 'ram_s', 'ram_mx', 'gl_n', 'gl_s', 'gl_mx',
  'ct_n', 'ct_s', 'ct_mx', 'gt_n', 'gt_s', 'gt_mx')
local stats = {'cpu', 'ram', 'gl', 'ct', 'gt'}
local set, ns = {'v', '1'}, 2
for i = 1, 5 do
  local s = ARGV[2 + i]
  local v = tonumber(s)
  if v >= 0 then
    local b, f = 6 + 3 * (i - 1), stats[i]
    local mx = cur[b + 3]
    set[ns + 1] = f .. '_n'; set[ns + 2] = tostring((tonumber(cur[b + 1]) or 0) + 1)
    set[ns + 3] = f .. '_s'; set[ns + 4] = tostring((tonumber(cur[b + 2]) or 0) + v)
    ns = ns + 4
    if not mx or v > tonumber(mx) then set[ns + 1] = f .. '_mx'; set[ns + 2] = s; ns = ns + 2 end
  end
end
local free = tonumber(ARGV[8])
if free >= 0 and (not cur[2] or free < tonumber(cur[2])) then
  set[ns + 1] = 'df'; set[ns + 2] = ARGV[8]; set[ns + 3] = 'dt'; set[ns + 4] = ARGV[9]; ns = ns + 4
end
if tonumber(ARGV[10]) > (tonumber(cur[3]) or 0) then set[ns + 1] = 'sm'; set[ns + 2] = ARGV[10]; ns = ns + 2 end
local pd = tonumber(ARGV[11])
if pd > 0 then set[ns + 1] = 'pd'; set[ns + 2] = tostring((tonumber(cur[4]) or 0) + pd); ns = ns + 2 end
-- Прирост сбоев: только по счётчикам «перед загрузкой» с boot_id (события сбоя пишет загрузка после него — каждое
-- попадает ровно в одну загрузку и из окна «с загрузки» не выпадает).
local sdb, boot = tonumber(ARGV[12]), ARGV[14]
if sdb >= 0 and boot ~= '' then
  local L = KEYS[3]
  local last = redis.call('HMGET', L, 'boot', 'sdb', 'bcb')
  if last[1] then
    local dsd, dbc = sdb, tonumber(ARGV[13])
    if last[1] == boot then dsd, dbc = dsd - (tonumber(last[2]) or 0), dbc - (tonumber(last[3]) or 0) end
    if dsd > 0 then set[ns + 1] = 'sd'; set[ns + 2] = tostring((tonumber(cur[5]) or 0) + dsd); ns = ns + 2 end
    if dbc > 0 then set[ns + 1] = 'bc'; set[ns + 2] = tostring((tonumber(cur[6]) or 0) + dbc); ns = ns + 2 end
  end
  if last[1] ~= boot or last[2] ~= ARGV[12] or last[3] ~= ARGV[13] then
    redis.call('HSET', L, 'boot', boot, 'sdb', ARGV[12], 'bcb', ARGV[13])
  end
  redis.call('EXPIRE', L, ARGV[15])
end
redis.call('HSET', h, unpack(set))
-- Минуты часа (биты 0..59 онлайн, 64..123 в сессии) одним BITFIELD; TTL агрегата и минут — при создании агрегата,
-- TTL индекса — когда ПК впервые попал в индекс часа.
local function touch(agg, bits, idx, created, from, n, nses)
  local ops, k = {}, 0
  for i = from, from + n + nses - 1 do
    local off = tonumber(ARGV[i])
    if i >= from + n then off = off + 64 end
    ops[k + 1] = 'SET'; ops[k + 2] = 'u1'; ops[k + 3] = off; ops[k + 4] = 1
    k = k + 4
  end
  if k > 0 then redis.call('BITFIELD', bits, unpack(ops)) end
  if created then
    redis.call('EXPIRE', agg, ttl)
    redis.call('EXPIRE', bits, ttl)
  end
  if redis.call('SADD', idx, member) == 1 then redis.call('EXPIRE', idx, ttl) end
  return from + n + nses
end
local n, nses, np, nps = tonumber(ARGV[16]), tonumber(ARGV[17]), tonumber(ARGV[18]), tonumber(ARGV[19])
local i = touch(h, KEYS[2], KEYS[4], not cur[1], 20, n, nses)
if np > 0 and redis.call('EXISTS', KEYS[9]) == 0 then
  touch(KEYS[6], KEYS[7], KEYS[8], redis.call('HSETNX', KEYS[6], 'v', '1') == 1, i, np, nps)
end
return 1
`)

func i64(v int64) string { return strconv.FormatInt(v, 10) }

// measurement — измерения одного Status для скрипта: -1 — не измерено (smart: 0).
type measurement struct {
	cpu, ram, gpuLoad, cpuTemp, gpuTemp int64
	diskFree, diskTotal                 int64
	smart                               int
	shutdownsBoot, bugchecksBoot        int64
}

func measurementOf(s Sample) measurement {
	m := measurement{cpu: -1, ram: -1, gpuLoad: -1, cpuTemp: -1, gpuTemp: -1, diskFree: -1,
		shutdownsBoot: -1, bugchecksBoot: -1}
	if s.Load != nil {
		m.cpu, m.ram = int64(s.Load.CPUBasisPoints), int64(s.Load.RAMBasisPoints)
	}
	hh := s.Health
	if hh == nil {
		return m
	}
	if hh.GPUMeasured {
		m.gpuLoad = int64(hh.GPULoadBP)
	}
	if hh.CPUTempDeci > 0 {
		m.cpuTemp = int64(hh.CPUTempDeci)
	}
	if hh.GPUTempDeci > 0 {
		m.gpuTemp = int64(hh.GPUTempDeci)
	}
	m.diskFree, m.diskTotal, m.smart = diskFacts(hh)
	if hh.CrashesBootMeasured {
		m.shutdownsBoot, m.bugchecksBoot = int64(hh.ShutdownsBoot), int64(hh.BugchecksBoot)
	}
	return m
}

// diskFacts — свободное место и объём системного диска (-1 — не измерено) и худший SMART измерения. У бездискового ПК
// дисков в здоровье нет (stream applyStatus: presence.Health.MarkDiskless) — «не измерено».
func diskFacts(hh *presence.Health) (free, total int64, smart int) {
	free = -1
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

// call — KEYS и ARGV скрипта для измерения s (час — s.At в UTC).
func (h *Hourly) call(s Sample) (keys, args []string) {
	at := s.At.UTC()
	hour := at.Truncate(time.Hour)
	m := measurementOf(s)
	prevOnline, online := OnlineMinutes(s.Since, at, h.maxGap)
	var prevSession, session []int
	if s.InSession {
		prevSession, session = SessionMinutes(prevOnline, online, s.Since, at)
	}
	const fixed = 19
	args = make([]string, 0, fixed+len(online)+len(session)+len(prevOnline)+len(prevSession))
	pc := strconv.FormatInt(s.ComputerID, 10)
	args = append(args,
		i64(int64(HourlyKeyTTL/time.Second)), strconv.FormatInt(s.ClubID, 10)+":"+pc,
		i64(m.cpu), i64(m.ram), i64(m.gpuLoad), i64(m.cpuTemp), i64(m.gpuTemp), i64(m.diskFree), i64(m.diskTotal),
		strconv.Itoa(m.smart), strconv.Itoa(max(s.PeripheralDisconnects, 0)),
		i64(m.shutdownsBoot), i64(m.bugchecksBoot), s.BootID,
		i64(int64(lastCountersTTL/time.Second)),
		strconv.Itoa(len(online)), strconv.Itoa(len(session)), strconv.Itoa(len(prevOnline)), strconv.Itoa(len(prevSession)))
	for _, list := range [][]int{online, session, prevOnline, prevSession} {
		for _, m := range list {
			args = append(args, strconv.Itoa(m))
		}
	}
	cur, prev := hourPart(hour), hourPart(hour.Add(-time.Hour))
	k := h.keys
	keys = []string{k.agg(cur, pc), k.bits(cur, pc), k.last(pc), k.index(cur), k.closed(cur),
		k.agg(prev, pc), k.bits(prev, pc), k.index(prev), k.closed(prev)}
	return keys, args
}

// Record добавляет измерение в агрегат часа s.At (один скрипт Valkey). Час уже закрыт сбросом (опоздавшее
// измерение: часы инстанса отстали больше DefaultHourlyDelay или обработка задержалась) — измерение не пишется
// (Late()), ошибки нет.
func (h *Hourly) Record(ctx context.Context, s Sample) error {
	if h == nil || s.ClubID <= 0 || s.ComputerID <= 0 {
		return nil
	}
	keys, args := h.call(s)
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
