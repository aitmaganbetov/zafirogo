//go:build integration

package health

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/authtest"
)

// legacyHourlyScript — скрипт агрегата часа до сокращения команд (PR #12, коммит ffd5f20): по команде Valkey на каждое
// поле (HINCRBY / HGET / HSET, SETBIT на минуту, EXPIRE на каждый Status). Эталон для TestHourlyScriptMatchesLegacy:
// тот же вход (KEYS/ARGV — Hourly.call) должен дать побитно те же ключи.
var legacyHourlyScript = valkey.NewLuaScript(`
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

var testValkey valkey.Client

func TestMain(m *testing.M) {
	ctx := context.Background()
	c, cleanup, err := authtest.StartValkey(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testValkey = c
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// dumpHour — всё, что скрипт пишет для ПК: агрегат текущего и прошлого часа, минуты (байты), последние счётчики,
// членство в индексах. TTL — отдельно (у нового скрипта ставится при создании ключа, а не на каждый Status).
func dumpHour(ctx context.Context, t *testing.T, keys []string, member string) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, i := range []int{0, 2, 5} {
		m, err := testValkey.Do(ctx, testValkey.B().Hgetall().Key(keys[i]).Build()).AsStrMap()
		if err != nil {
			t.Fatal(err)
		}
		out[fmt.Sprint("hash", i)] = m
	}
	for _, i := range []int{1, 6} {
		b, err := testValkey.Do(ctx, testValkey.B().Get().Key(keys[i]).Build()).ToString()
		if err != nil && !valkey.IsValkeyNil(err) {
			t.Fatal(err)
		}
		out[fmt.Sprint("bits", i)] = b
	}
	for _, i := range []int{3, 7} {
		n, err := testValkey.Do(ctx, testValkey.B().Sismember().Key(keys[i]).Member(member).Build()).AsInt64()
		if err != nil {
			t.Fatal(err)
		}
		out[fmt.Sprint("index", i)] = n
	}
	return out
}

// Сокращение команд скрипта агрегата не меняет результат: случайные последовательности Status (загрузка, температуры,
// диски, SMART, периферия, сбои за 7 суток и перед загрузкой с переподключениями, сессии, разрывы и переход часа) через
// прежний и новый скрипт дают побитно одинаковые агрегаты, минуты, последние счётчики и индексы; у созданных ключей есть TTL.
func TestHourlyScriptMatchesLegacy(t *testing.T) {
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(7, 2026))
	hour := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for run := range 40 {
		oldH := NewHourly(testValkey, fmt.Sprintf("t:legacy:%d:", run), 0)
		newH := NewHourly(testValkey, fmt.Sprintf("t:new:%d:", run), 0)
		boot := "boot-a"
		var since time.Time
		sd7, bc7, sdb, bcb := uint32(rng.IntN(3)), uint32(rng.IntN(2)), uint32(0), uint32(0)
		at := hour.Add(-time.Duration(rng.IntN(5)) * time.Minute)
		for step := range 90 {
			at = at.Add(time.Duration(20+rng.IntN(130)) * time.Second)
			s := Sample{ClubID: 3, ComputerID: int64(10 + run), At: at, Since: since, InSession: rng.IntN(3) == 0,
				PeripheralDisconnects: rng.IntN(4) - 1, BootID: boot}
			if rng.IntN(5) > 0 {
				s.Load = &presence.Load{CPUBasisPoints: uint32(rng.IntN(10001)), RAMBasisPoints: uint32(rng.IntN(10001))}
			}
			if rng.IntN(4) > 0 {
				if rng.IntN(10) == 0 { // перезагрузка: новый boot_id, сбои перед ней
					boot, sdb, bcb = fmt.Sprintf("boot-%d", step), uint32(rng.IntN(3)), uint32(rng.IntN(2))
					sd7, bc7 = sd7+sdb, bc7+bcb
					s.BootID = boot
				}
				if rng.IntN(8) == 0 && sd7 > 0 { // старый сбой выпал из окна 7 суток
					sd7--
				}
				hh := &presence.Health{CPUTempDeci: int32(rng.IntN(3) * (300 + rng.IntN(700))), GPUTempDeci: int32(rng.IntN(900)),
					GPUMeasured: rng.IntN(3) > 0, GPULoadBP: uint32(rng.IntN(10001)), Shutdowns7d: sd7, Bugchecks7d: bc7,
					CrashesBootMeasured: run%3 != 0, ShutdownsBoot: sdb, BugchecksBoot: bcb}
				if rng.IntN(4) > 0 {
					hh.Disks = []presence.Disk{{Mount: "D:", TotalMB: 1000, FreeMB: uint64(rng.IntN(1000)), Smart: []string{"", presence.SmartOK}[rng.IntN(2)]},
						{Mount: "C:", System: true, TotalMB: 512000, FreeMB: uint64(rng.IntN(512000)),
							Smart: []string{"", presence.SmartOK, presence.SmartWarning, presence.SmartFailing}[rng.IntN(4)]}}
				}
				s.Health = hh
			}
			if rng.IntN(15) == 0 { // переподключение: первый Status подключения
				s.Since = time.Time{}
			}
			ok, ak := oldH.call(s)
			nk, na := newH.call(s)
			if len(ak) != len(na) {
				t.Fatalf("args differ: %v / %v", ak, na)
			}
			// Сброс закрыл прошлый час (минуты разрыва туда не пишутся), позже — текущий (опоздавшие Status: 0).
			for _, c := range []struct{ step, key int }{{40, 8}, {70, 4}} {
				if step == c.step {
					for _, k := range []string{ok[c.key], nk[c.key]} {
						if err := testValkey.Do(ctx, testValkey.B().Set().Key(k).Value("1").Build()).Error(); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			want, err := legacyHourlyScript.Exec(ctx, testValkey, ok, ak).AsInt64()
			if err != nil {
				t.Fatal(err)
			}
			got, err := hourlyScript.Exec(ctx, testValkey, nk, na).AsInt64()
			if err != nil {
				t.Fatal(err)
			}
			member := na[1]
			if w, g := dumpHour(ctx, t, ok, member), dumpHour(ctx, t, nk, member); got != want || !reflect.DeepEqual(w, g) {
				t.Fatalf("run %d step %d (%+v): result %d / %d\nlegacy %v\nnew    %v", run, step, s, want, got, w, g)
			}
			for _, i := range []int{0, 1, 2, 3, 5, 6, 7} { // -2 — ключа нет (нет сбоев, нет прошлого часа); -1 — без TTL
				if ttl, err := testValkey.Do(ctx, testValkey.B().Ttl().Key(nk[i]).Build()).AsInt64(); err != nil || ttl == -1 {
					t.Fatalf("run %d step %d: key %s ttl %d (%v)", run, step, nk[i], ttl, err)
				}
			}
			since = at
		}
	}
}
