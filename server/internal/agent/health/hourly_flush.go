package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// HourlyJob — имя задачи сброса почасовой истории (ключ advisory lock, Q-086).
const HourlyJob = "agent:health-hourly"

// Умолчания сброса.
const (
	// DefaultHourlyDelay — сколько ждать после конца часа: Status, принятый в последние секунды часа, успевает дописаться
	// с любого инстанса (часы инстансов расходятся на секунды).
	DefaultHourlyDelay = 5 * time.Minute
	// DefaultHourlyInterval — как часто проверять, есть ли что сбрасывать.
	DefaultHourlyInterval = time.Minute
	// hourlyLookback — на сколько часов назад смотреть (агрегат живёт HourlyKeyTTL).
	hourlyLookback = 24
	// hourlyBatch — ПК за один проход (SSCAN COUNT и одна вставка на клуб).
	hourlyBatch = 500
)

// HourlyRow — строка computer_health_hourly (JSON для ComputerHealthHourlyUpsert; null — не измерено).
type HourlyRow struct {
	ClubID                int64     `json:"-"`
	ComputerID            int64     `json:"computer_id"`
	Hour                  time.Time `json:"hour"`
	OnlineMinutes         int       `json:"online_minutes"`
	SessionMinutes        int       `json:"session_minutes"`
	CPULoadMinBP          *int64    `json:"cpu_load_min_bp"`
	CPULoadAvgBP          *int64    `json:"cpu_load_avg_bp"`
	CPULoadMaxBP          *int64    `json:"cpu_load_max_bp"`
	RAMMinBP              *int64    `json:"ram_min_bp"`
	RAMAvgBP              *int64    `json:"ram_avg_bp"`
	RAMMaxBP              *int64    `json:"ram_max_bp"`
	GPULoadMinBP          *int64    `json:"gpu_load_min_bp"`
	GPULoadAvgBP          *int64    `json:"gpu_load_avg_bp"`
	GPULoadMaxBP          *int64    `json:"gpu_load_max_bp"`
	CPUTempMinDeci        *int64    `json:"cpu_temp_min_deci"`
	CPUTempAvgDeci        *int64    `json:"cpu_temp_avg_deci"`
	CPUTempMaxDeci        *int64    `json:"cpu_temp_max_deci"`
	GPUTempMinDeci        *int64    `json:"gpu_temp_min_deci"`
	GPUTempAvgDeci        *int64    `json:"gpu_temp_avg_deci"`
	GPUTempMaxDeci        *int64    `json:"gpu_temp_max_deci"`
	SystemDiskFreeMB      *int64    `json:"system_disk_free_mb"`
	SystemDiskTotalMB     *int64    `json:"system_disk_total_mb"`
	SmartWorst            *string   `json:"smart_worst"`
	PeripheralDisconnects int64     `json:"peripheral_disconnects"`
	UnexpectedShutdowns   int64     `json:"unexpected_shutdowns"`
	Bugchecks             int64     `json:"bugchecks"`
}

// rowFromAgg — строка часа из hash агрегата и числа минут (min/avg/max — целые; среднее — с отбрасыванием дробной части).
func rowFromAgg(clubID, computerID int64, hour time.Time, agg map[string]string, online, session int64) HourlyRow {
	num := func(f string) (int64, bool) {
		v, ok := agg[f]
		if !ok {
			return 0, false
		}
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	}
	ptr := func(f string) *int64 {
		if n, ok := num(f); ok {
			return &n
		}
		return nil
	}
	stat := func(f string) (mn, avg, mx *int64) {
		n, ok := num(f + "_n")
		s, ok2 := num(f + "_s")
		if !ok || !ok2 || n <= 0 {
			return nil, nil, nil
		}
		a := s / n
		return ptr(f + "_mn"), &a, ptr(f + "_mx")
	}
	r := HourlyRow{ClubID: clubID, ComputerID: computerID, Hour: hour.UTC(),
		OnlineMinutes: int(min(online, 60)), SessionMinutes: int(min(session, 60))} // #nosec G115 -- ≤ 60
	r.CPULoadMinBP, r.CPULoadAvgBP, r.CPULoadMaxBP = stat("cpu")
	r.RAMMinBP, r.RAMAvgBP, r.RAMMaxBP = stat("ram")
	r.GPULoadMinBP, r.GPULoadAvgBP, r.GPULoadMaxBP = stat("gl")
	r.CPUTempMinDeci, r.CPUTempAvgDeci, r.CPUTempMaxDeci = stat("ct")
	r.GPUTempMinDeci, r.GPUTempAvgDeci, r.GPUTempMaxDeci = stat("gt")
	if f := ptr("df"); f != nil {
		r.SystemDiskFreeMB, r.SystemDiskTotalMB = f, ptr("dt")
	}
	if n, ok := num("sm"); ok {
		if s := SmartName(int(n)); s != "" {
			r.SmartWorst = &s
		}
	}
	r.PeripheralDisconnects, _ = num("pd")
	r.UnexpectedShutdowns, _ = num("sd")
	r.Bugchecks, _ = num("bc")
	return r
}

// HourlyFlusher — сброс агрегатов прошедших часов из Valkey в computer_health_hourly: одна строка на ПК и час
// (UPSERT по (club_id, computer_id, hour) — повтор после сбоя идемпотентен). Один исполнитель на кластер (advisory
// lock HourlyJob, Q-086); ключи Valkey удаляются только после записи в Postgres.
type HourlyFlusher struct {
	Valkey valkey.Client
	// Prefix — префикс ключей присутствия (как у Hourly).
	Prefix string
	// App — пул zafiro_app (тенантная транзакция клуба, RLS).
	App db.Beginner
	// Platform — пул zafiro_platform для advisory lock; nil — без блокировки (тесты).
	Platform *pgxpool.Pool
	// Delay — сколько ждать после конца часа (0 — DefaultHourlyDelay).
	Delay time.Duration
	// Interval — период проверки (0 — DefaultHourlyInterval).
	Interval time.Duration
	Now      func() time.Time
	Log      *slog.Logger

	rows atomic.Int64
}

// Rows — сколько строк часа записано (замеры, тесты).
func (f *HourlyFlusher) Rows() int64 { return f.rows.Load() }

func (f *HourlyFlusher) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

func (f *HourlyFlusher) keys() *Hourly { return NewHourly(f.Valkey, f.Prefix, 0) }

// Run выполняет тики до отмены ctx (первый — сразу).
func (f *HourlyFlusher) Run(ctx context.Context) error {
	every := f.Interval
	if every <= 0 {
		every = DefaultHourlyInterval
	}
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := f.Tick(ctx); err != nil && ctx.Err() == nil {
				f.log().Error("health hourly flush failed", "job", HourlyJob, "err", err)
			}
			t.Reset(every)
		}
	}
}

// Tick — один проход под блокировкой кластера: сбрасывает все законченные (с запасом Delay) часы за последние сутки.
func (f *HourlyFlusher) Tick(ctx context.Context) (rows int, err error) {
	run := func(ctx context.Context, _ *pgxpool.Conn) error {
		now := time.Now
		if f.Now != nil {
			now = f.Now
		}
		delay := f.Delay
		if delay <= 0 {
			delay = DefaultHourlyDelay
		}
		last := now().UTC().Add(-delay).Truncate(time.Hour).Add(-time.Hour) // последний законченный час
		k := f.keys()
		cmds := make(valkey.Commands, 0, hourlyLookback)
		hours := make([]time.Time, 0, hourlyLookback)
		for i := range hourlyLookback {
			hour := last.Add(-time.Duration(i) * time.Hour)
			hours = append(hours, hour)
			cmds = append(cmds, f.Valkey.B().Exists().Key(k.indexKey(hour)).Build())
		}
		for i, res := range f.Valkey.DoMulti(ctx, cmds...) {
			n, err := res.AsInt64()
			if err != nil {
				return fmt.Errorf("health: hourly index: %w", err)
			}
			if n == 0 {
				continue
			}
			got, err := f.FlushHour(ctx, hours[i])
			rows += got
			if err != nil {
				return err
			}
		}
		return nil
	}
	if f.Platform == nil {
		return rows, run(ctx, nil)
	}
	_, err = leader.RunOnce(ctx, f.Platform, HourlyJob, run)
	return rows, err
}

// FlushHour закрывает час hour (начало часа UTC) для записи и пишет его строки пачками: прочитать агрегаты → UPSERT по
// клубам → удалить ключи и члены индекса. Сбой посреди — оставшиеся ПК сбросятся следующим тиком, записанные повторно перезапишутся теми же
// значениями.
func (f *HourlyFlusher) FlushHour(ctx context.Context, hour time.Time) (rows int, err error) {
	k := f.keys()
	hour = hour.UTC().Truncate(time.Hour)
	idx := k.indexKey(hour)
	// Сначала — «час закрыт»: Record больше не пишет в этот час (атомарно с проверкой в скрипте). Иначе Status,
	// опоздавший после чтения агрегата, терялся бы, а пришедший после удаления ключей создал бы новый частичный агрегат,
	// и следующий тик перезаписал бы им полную строку часа. Маркер живёт, пока в час может прийти запись (TTL агрегата).
	if err := f.Valkey.Do(ctx, f.Valkey.B().Set().Key(k.closedKey(hour)).Value("1").
		Ex(HourlyKeyTTL).Build()).Error(); err != nil {
		return 0, fmt.Errorf("health: hourly close: %w", err)
	}
	var cursor uint64
	for {
		res, err := f.Valkey.Do(ctx, f.Valkey.B().Sscan().Key(idx).Cursor(cursor).Count(hourlyBatch).Build()).AsScanEntry()
		if err != nil {
			return rows, fmt.Errorf("health: hourly scan: %w", err)
		}
		if members := slices.Compact(slices.Sorted(slices.Values(res.Elements))); len(members) > 0 {
			n, err := f.flushBatch(ctx, k, hour, idx, members)
			rows += n
			if err != nil {
				return rows, err
			}
		}
		if cursor = res.Cursor; cursor != 0 {
			continue
		}
		// Проход закончен; члены, добавленные во время прохода (не должно быть: час закончился), — следующим тиком.
		return rows, nil
	}
}

func (f *HourlyFlusher) flushBatch(ctx context.Context, k *Hourly, hour time.Time, idx string, members []string) (int, error) {
	type pc struct{ club, computer int64 }
	pcs := make([]pc, len(members))
	cmds := make(valkey.Commands, 0, 3*len(members))
	for i, m := range members {
		c, p, _ := strings.Cut(m, ":")
		pcs[i].club, _ = strconv.ParseInt(c, 10, 64)
		pcs[i].computer, _ = strconv.ParseInt(p, 10, 64)
		cmds = append(cmds,
			f.Valkey.B().Hgetall().Key(k.aggKey(hour, pcs[i].computer)).Build(),
			f.Valkey.B().Bitcount().Key(k.bitsKey(hour, pcs[i].computer)).Start(0).End(7).Build(),
			f.Valkey.B().Bitcount().Key(k.bitsKey(hour, pcs[i].computer)).Start(8).End(15).Build())
	}
	res := f.Valkey.DoMulti(ctx, cmds...)
	byClub := map[int64][]HourlyRow{}
	for i, p := range pcs {
		agg, err := res[3*i].AsStrMap()
		if err != nil {
			return 0, fmt.Errorf("health: hourly read: %w", err)
		}
		online, err := res[3*i+1].AsInt64()
		if err != nil {
			return 0, fmt.Errorf("health: hourly read: %w", err)
		}
		session, err := res[3*i+2].AsInt64()
		if err != nil {
			return 0, fmt.Errorf("health: hourly read: %w", err)
		}
		if p.club <= 0 || p.computer <= 0 || len(agg) == 0 {
			continue // ключ истёк или испорчен — только убрать из индекса
		}
		byClub[p.club] = append(byClub[p.club], rowFromAgg(p.club, p.computer, hour, agg, online, session))
	}
	written := 0
	clubs := make([]int64, 0, len(byClub))
	for c := range byClub {
		clubs = append(clubs, c)
	}
	slices.Sort(clubs)
	for _, club := range clubs {
		b, err := json.Marshal(byClub[club])
		if err != nil {
			return written, err
		}
		var n int64
		if err := db.InTenantTx(ctx, f.App, club, func(q *store.Queries) error {
			n, err = q.ComputerHealthHourlyUpsert(ctx, store.ComputerHealthHourlyUpsertParams{ClubID: club, Rows: b})
			return err
		}); err != nil {
			return written, fmt.Errorf("health: hourly upsert club %d: %w", club, err)
		}
		written += int(n)
		f.rows.Add(n)
	}
	del := make(valkey.Commands, 0, len(members)+1)
	for _, p := range pcs {
		del = append(del, f.Valkey.B().Del().Key(k.aggKey(hour, p.computer), k.bitsKey(hour, p.computer)).Build())
	}
	del = append(del, f.Valkey.B().Srem().Key(idx).Member(members...).Build())
	for _, r := range f.Valkey.DoMulti(ctx, del...) {
		if err := r.Error(); err != nil {
			return written, fmt.Errorf("health: hourly cleanup: %w", err)
		}
	}
	return written, nil
}
