//go:build integration

package stream

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
)

// fakeClock — общие часы инстансов (Status «раз в минуту» без ожидания минут).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// hourlyStatus — Status минуты m часа: загрузка и температура растут с минутой, мышь отключена с 20-й минуты, с 30-й
// минуты агент насчитал на одно внезапное выключение и один «синий экран» больше, с 40-й SMART предупреждает, минуты
// 10..19 — в сессии.
func hourlyStatus(m int) *agentv1.Status {
	state := agentv1.ComputerState_COMPUTER_STATE_IDLE
	if m >= 10 && m < 20 {
		state = agentv1.ComputerState_COMPUTER_STATE_IN_SESSION
	}
	smart := agentv1.DiskSmart_DISK_SMART_OK
	if m >= 40 {
		smart = agentv1.DiskSmart_DISK_SMART_WARNING
	}
	sd, bc := uint32(1), uint32(0)
	if m >= 30 {
		sd, bc = 2, 1
	}
	return &agentv1.Status{
		State: state, Load: &agentv1.ComputerLoad{CpuBasisPoints: uint32(1000 + 100*m), RamBasisPoints: 5000},
		Health: &agentv1.ComputerHealth{
			CpuTempDecicelsius: int32(500 + 5*m), GpuTempDecicelsius: 600, GpuMeasured: true, GpuLoadBasisPoints: 3000,
			LinkSpeedMbps: 1000, UnexpectedShutdowns_7D: sd, Bugchecks_7D: bc,
			Disks: []*agentv1.DiskHealth{{Mount: "C:", System: true, TotalMb: 512000, FreeMb: uint64(100000 - 100*m), Smart: smart},
				{Mount: "D:", TotalMb: 1000000, FreeMb: 10}},
			Peripherals: []*agentv1.Peripheral{{Kind: agentv1.PeripheralKind_PERIPHERAL_KIND_MOUSE, Connected: m < 20, Name: "G102"}},
		},
	}
}

type hourlyRow struct {
	online, session                  int
	cpuMin, cpuAvg, cpuMax           int
	ctMin, ctAvg, ctMax, gtMax, glMx int
	diskFree, diskTotal              int64
	smart                            string
	pd, sd, bc                       int
}

func readHourly(t *testing.T, pc int64, hour time.Time) (rows int, r hourlyRow) {
	t.Helper()
	ctx := context.Background()
	if err := testEnv.db.Owner.QueryRow(ctx, `SELECT count(*) FROM computer_health_hourly WHERE computer_id = $1`, pc).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		return
	}
	if err := testEnv.db.Owner.QueryRow(ctx, `SELECT online_minutes, session_minutes, cpu_load_min_bp, cpu_load_avg_bp,
		cpu_load_max_bp, cpu_temp_min_deci, cpu_temp_avg_deci, cpu_temp_max_deci, gpu_temp_max_deci, gpu_load_max_bp,
		system_disk_free_mb, system_disk_total_mb, smart_worst, peripheral_disconnects, unexpected_shutdowns, bugchecks
		FROM computer_health_hourly WHERE computer_id = $1 AND hour = $2`, pc, hour).Scan(
		&r.online, &r.session, &r.cpuMin, &r.cpuAvg, &r.cpuMax, &r.ctMin, &r.ctAvg, &r.ctMax, &r.gtMax, &r.glMx,
		&r.diskFree, &r.diskTotal, &r.smart, &r.pd, &r.sd, &r.bc); err != nil {
		t.Fatal(err)
	}
	return
}

// Час Status-ов: сначала — подключение с Hello (минута 0), затем Status раз в минуту (1..59). moveAt > 0 — на этой
// минуте ПК переподключается к инстансу b (перебалансировка посреди часа); Hello там несёт тот же Status.
func runHour(t *testing.T, moveAt int) (pc int64, hour time.Time, flusher *health.HourlyFlusher) {
	t.Helper()
	ctx := context.Background()
	club := testEnv.club(t)
	pc = testEnv.computer(t, club, 1)
	// Прошлый час реального времени (партиции текущего месяца есть; прошлый месяц — EnsurePartitions).
	hour = time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	if _, err := db.EnsurePartitions(ctx, testEnv.db.Platform, hour.AddDate(0, -1, 0), 3); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: hour.Add(5 * time.Second)}
	tune := func(o *Options) {
		o.Now = clock.Now
		o.Health = HealthOptions{PingInterval: -1}
	}
	a := testEnv.instance(t, "a", tune)
	b := testEnv.instance(t, "b", tune)
	prefix := a.svc.Presence().Prefix()
	if prefix != b.svc.Presence().Prefix() {
		t.Fatal("instances must share key prefix")
	}
	writes := func() int64 { return a.svc.HealthHourly().Writes() + b.svc.HealthHourly().Writes() }

	hello := defaultHello()
	hello.Status = hourlyStatus(0)
	cur, _ := a.connect(t, club, pc, hello)
	eventually(t, 3*time.Second, "hello recorded", func() bool { return writes() == 1 })
	for m := 1; m < 60; m++ {
		clock.set(hour.Add(time.Duration(m)*time.Minute + 5*time.Second))
		if m == moveAt {
			h := defaultHello()
			h.Status = hourlyStatus(m)
			cur, _ = b.connect(t, club, pc, h)
		} else {
			cur.send(&agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: hourlyStatus(m)}})
		}
		want := int64(m + 1)
		eventually(t, 3*time.Second, "status recorded", func() bool { return writes() == want })
	}
	if rows, _ := readHourly(t, pc, hour); rows != 0 {
		t.Fatalf("hourly rows before the hour is flushed: %d (must not write on each Status)", rows)
	}
	flusher = &health.HourlyFlusher{Valkey: testEnv.valkey, Prefix: prefix, App: testEnv.db.App,
		Now: func() time.Time { return hour.Add(time.Hour + 6*time.Minute) }}
	return pc, hour, flusher
}

func checkHourRow(t *testing.T, pc int64, hour time.Time) {
	t.Helper()
	rows, r := readHourly(t, pc, hour)
	if rows != 1 {
		t.Fatalf("hourly rows = %d, want 1", rows)
	}
	// CPU 1000 + 100·m (m = 0..59): среднее 3950; t CPU 500 + 5·m: среднее 647 (647.5 → целое деление).
	want := hourlyRow{online: 60, session: 10, cpuMin: 1000, cpuAvg: 3950, cpuMax: 6900, ctMin: 500, ctAvg: 647, ctMax: 795,
		gtMax: 600, glMx: 3000, diskFree: 100000 - 5900, diskTotal: 512000, smart: "warning", pd: 1, sd: 1, bc: 1}
	if r != want {
		t.Fatalf("hour row =\n%+v\nwant\n%+v", r, want)
	}
}

// Status-ы за час → ОДНА строка часа после его конца (не на каждый Status); повторный сброс ничего не меняет.
func TestHealthHourlyOneRowPerHour(t *testing.T) {
	ctx := context.Background()
	pc, hour, f := runHour(t, 0)
	// Час ещё не закончился с запасом Delay — сбрасывать нечего.
	early := &health.HourlyFlusher{Valkey: f.Valkey, Prefix: f.Prefix, App: f.App,
		Now: func() time.Time { return hour.Add(time.Hour + time.Minute) }}
	if n, err := early.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("early tick: %d, %v", n, err)
	}
	if n, err := f.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick: %d rows, %v", n, err)
	}
	checkHourRow(t, pc, hour)
	// Ключи часа удалены; второй тик и повторный сброс того же часа — без изменений.
	if n, err := f.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("second tick: %d, %v", n, err)
	}
	if n, err := f.FlushHour(ctx, hour); err != nil || n != 0 {
		t.Fatalf("second flush: %d, %v", n, err)
	}
	checkHourRow(t, pc, hour)
}

// ПК переехал на другой инстанс посреди часа (перебалансировка): агрегат общий (Valkey) — одна корректная строка, без
// потерь и дублей, прирост внезапных выключений посчитан один раз (Hello на новом инстансе — после «выключения»).
func TestHealthHourlyMoveBetweenInstances(t *testing.T) {
	ctx := context.Background()
	pc, hour, f := runHour(t, 30)
	if n, err := f.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick: %d rows, %v", n, err)
	}
	checkHourRow(t, pc, hour)
}

// Сброс идемпотентен: та же строка, записанная повторно (сбой после записи, до удаления ключей), — те же значения.
func TestHealthHourlyUpsertIdempotent(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 1)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-5 * time.Hour)
	if _, err := db.EnsurePartitions(ctx, testEnv.db.Platform, hour.AddDate(0, -1, 0), 3); err != nil {
		t.Fatal(err)
	}
	h := health.NewHourly(testEnv.valkey, "t:"+t.Name()+":", 0)
	for i := range 3 {
		l := uint32(2000 * (i + 1))
		if err := h.Record(ctx, health.Sample{ClubID: club, ComputerID: pc, At: hour.Add(time.Duration(i) * time.Minute),
			Load: &presence.Load{CPUBasisPoints: l}}); err != nil {
			t.Fatal(err)
		}
	}
	f := &health.HourlyFlusher{Valkey: testEnv.valkey, Prefix: "t:" + t.Name() + ":", App: testEnv.db.App}
	// Первый сброс «упал» после записи: ключи на месте — повторяем их копией.
	if _, err := f.FlushHour(ctx, hour); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := h.Record(ctx, health.Sample{ClubID: club, ComputerID: pc, At: hour.Add(time.Duration(i) * time.Minute),
			Load: &presence.Load{CPUBasisPoints: uint32(2000 * (i + 1))}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.FlushHour(ctx, hour); err != nil {
		t.Fatal(err)
	}
	var rows, online, avg int
	if err := testEnv.db.Owner.QueryRow(ctx, `SELECT count(*), max(online_minutes), max(cpu_load_avg_bp) FROM computer_health_hourly
		WHERE computer_id = $1`, pc).Scan(&rows, &online, &avg); err != nil || rows != 1 || online != 3 || avg != 4000 {
		t.Fatalf("rows %d online %d avg %d (%v)", rows, online, avg, err)
	}
}

// Замер почасовой истории (docs/load-test.md §6.4): ZAFIRO_LOAD_HOURLY=<ПК> — столько ПК клуба по 60 Status за час
// (запись в Valkey 64 потоками), затем сброс часа в Postgres. Печатает Status/с, память Valkey на ПК и время сброса.
func TestHealthHourlyLoad(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("ZAFIRO_LOAD_HOURLY"))
	if n <= 0 {
		t.Skip("ZAFIRO_LOAD_HOURLY=<число ПК> — замер почасовой истории")
	}
	ctx := context.Background()
	club := testEnv.club(t)
	pcs := make([]int64, n)
	for i := range pcs {
		pcs[i] = testEnv.computer(t, club, i+1)
	}
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	if _, err := db.EnsurePartitions(ctx, testEnv.db.Platform, hour.AddDate(0, -1, 0), 3); err != nil {
		t.Fatal(err)
	}
	prefix := "t:" + t.Name() + ":"
	h := health.NewHourly(testEnv.valkey, prefix, 0)
	memBefore := valkeyUsedMemory(t)
	start := time.Now()
	var wg sync.WaitGroup
	work := make(chan int, 1024)
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				pc, m := pcs[i/60], i%60
				st := hourlyStatus(m)
				ld := presence.LoadFrom(st.GetLoad())
				if err := h.Record(ctx, health.Sample{ClubID: club, ComputerID: pc, At: hour.Add(time.Duration(m)*time.Minute + time.Second),
					Since: hour.Add(time.Duration(m-1)*time.Minute + time.Second), Load: &ld,
					Health: presence.HealthFrom(st.GetHealth(), hour)}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := range n * 60 {
		work <- i
	}
	close(work)
	wg.Wait()
	rec := time.Since(start)
	mem := valkeyUsedMemory(t) - memBefore
	f := &health.HourlyFlusher{Valkey: testEnv.valkey, Prefix: prefix, App: testEnv.db.App}
	start = time.Now()
	rows, err := f.FlushHour(ctx, hour)
	if err != nil || rows != n {
		t.Fatalf("flush: %d rows, %v", rows, err)
	}
	flush := time.Since(start)
	var size int64
	part := "computer_health_hourly_" + hour.Format("2006_01")
	if err := testEnv.db.Owner.QueryRow(ctx, "SELECT pg_total_relation_size($1::regclass)", part).Scan(&size); err != nil {
		t.Fatal(err)
	}
	t.Logf("hourly load: %d PC × 60 Status in %s (%.0f Status/s, 64 goroutines); Valkey +%d KB (%.0f B/PC); flush %d rows in %s; "+
		"partition %d KB (≈ %d B/row incl. PK)", n, rec.Round(time.Millisecond), float64(n*60)/rec.Seconds(), mem/1024,
		float64(mem)/float64(n), rows, flush.Round(time.Millisecond), size/1024, size/int64(n))
}

func valkeyUsedMemory(t *testing.T) int64 {
	t.Helper()
	info, err := testEnv.valkey.Do(context.Background(), testEnv.valkey.B().Info().Section("memory").Build()).ToString()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "used_memory:"); ok {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	return 0
}
