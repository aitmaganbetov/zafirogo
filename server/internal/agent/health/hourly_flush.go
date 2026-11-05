package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
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
	// hourlyBatch — ПК за одно чтение агрегатов, SSCAN COUNT и одну вставку (пачки — внутри транзакции клуба).
	hourlyBatch = 500
)

// HourlyRow — строка computer_health_hourly (JSON для ComputerHealthHourlyUpsert; null — не измерено).
type HourlyRow struct {
	ClubID                int64     `json:"-"`
	ComputerID            int64     `json:"computer_id"`
	Hour                  time.Time `json:"hour"`
	OnlineMinutes         int       `json:"online_minutes"`
	SessionMinutes        int       `json:"session_minutes"`
	CPULoadAvgBP          *int64    `json:"cpu_load_avg_bp"`
	CPULoadMaxBP          *int64    `json:"cpu_load_max_bp"`
	RAMAvgBP              *int64    `json:"ram_avg_bp"`
	RAMMaxBP              *int64    `json:"ram_max_bp"`
	GPULoadAvgBP          *int64    `json:"gpu_load_avg_bp"`
	GPULoadMaxBP          *int64    `json:"gpu_load_max_bp"`
	CPUTempAvgDeci        *int64    `json:"cpu_temp_avg_deci"`
	CPUTempMaxDeci        *int64    `json:"cpu_temp_max_deci"`
	GPUTempAvgDeci        *int64    `json:"gpu_temp_avg_deci"`
	GPUTempMaxDeci        *int64    `json:"gpu_temp_max_deci"`
	SystemDiskFreeMB      *int64    `json:"system_disk_free_mb"`
	SystemDiskTotalMB     *int64    `json:"system_disk_total_mb"`
	SmartWorst            *string   `json:"smart_worst"`
	PeripheralDisconnects int64     `json:"peripheral_disconnects"`
	UnexpectedShutdowns   int64     `json:"unexpected_shutdowns"`
	Bugchecks             int64     `json:"bugchecks"`
}

// rowFromAgg — строка часа из hash агрегата и числа минут (avg/max — целые; среднее — с отбрасыванием дробной части).
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
	stat := func(f string) (avg, mx *int64) {
		n, ok := num(f + "_n")
		s, ok2 := num(f + "_s")
		if !ok || !ok2 || n <= 0 {
			return nil, nil
		}
		a := s / n
		return &a, ptr(f + "_mx")
	}
	r := HourlyRow{ClubID: clubID, ComputerID: computerID, Hour: hour.UTC(),
		OnlineMinutes: int(min(online, 60)), SessionMinutes: int(min(session, 60))} // #nosec G115 -- ≤ 60
	r.CPULoadAvgBP, r.CPULoadMaxBP = stat("cpu")
	r.RAMAvgBP, r.RAMMaxBP = stat("ram")
	r.GPULoadAvgBP, r.GPULoadMaxBP = stat("gl")
	r.CPUTempAvgDeci, r.CPUTempMaxDeci = stat("ct")
	r.GPUTempAvgDeci, r.GPUTempMaxDeci = stat("gt")
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

func (f *HourlyFlusher) keys() hourlyKeys { return newHourlyKeys(f.Prefix) }

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
			cmds = append(cmds, f.Valkey.B().Exists().Key(k.index(hourPart(hour))).Build())
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

// FlushHour закрывает час hour (начало часа UTC) для записи и пишет его строки по клубам: члены индекса часа
// группируются по клубу, и на клуб — ОДНА тенантная транзакция за час (агрегаты читаются пачками по hourlyBatch ПК,
// UPSERT — теми же пачками внутри транзакции), затем ключи ПК клуба и их члены индекса удаляются. Сбой посреди —
// оставшиеся клубы сбросятся следующим тиком (транзакция клуба откатывается целиком), записанные повторно перезапишутся
// теми же значениями.
func (f *HourlyFlusher) FlushHour(ctx context.Context, hour time.Time) (rows int, err error) {
	k := f.keys()
	hour = hour.UTC().Truncate(time.Hour)
	hp := hourPart(hour)
	idx := k.index(hp)
	// Сначала — «час закрыт»: Record больше не пишет в этот час (атомарно с проверкой в скрипте). Иначе Status,
	// опоздавший после чтения агрегата, терялся бы, а пришедший после удаления ключей создал бы новый частичный агрегат,
	// и следующий тик перезаписал бы им полную строку часа. Маркер живёт, пока в час может прийти запись (TTL агрегата).
	if err := f.Valkey.Do(ctx, f.Valkey.B().Set().Key(k.closed(hp)).Value("1").
		Ex(HourlyKeyTTL).Build()).Error(); err != nil {
		return 0, fmt.Errorf("health: hourly close: %w", err)
	}
	// Члены индекса — весь час сразу (≤ число ПК в сети за час: 50 000 строк «club:computer» — единицы МБ); члены,
	// добавленные во время прохода (не должно быть: час закончился), — следующим тиком.
	byClub := map[int64][]hourlyMember{}
	var cursor uint64
	for {
		res, err := f.Valkey.Do(ctx, f.Valkey.B().Sscan().Key(idx).Cursor(cursor).Count(hourlyBatch).Build()).AsScanEntry()
		if err != nil {
			return rows, fmt.Errorf("health: hourly scan: %w", err)
		}
		for _, m := range res.Elements {
			hm := parseHourlyMember(m)
			byClub[hm.club] = append(byClub[hm.club], hm)
		}
		if cursor = res.Cursor; cursor == 0 {
			break
		}
	}
	for _, club := range slices.Sorted(maps.Keys(byClub)) {
		// SSCAN может вернуть член повторно — по одному разу.
		members := slices.CompactFunc(slices.SortedFunc(slices.Values(byClub[club]), compareHourlyMembers),
			func(a, b hourlyMember) bool { return a.raw == b.raw })
		n, err := f.flushClub(ctx, k, hour, idx, club, members)
		rows += n
		if err != nil {
			return rows, err
		}
	}
	return rows, nil
}

// hourlyMember — член индекса часа «club:computer» (club, computer ≤ 0 — испорчен: только убрать из индекса).
type hourlyMember struct {
	raw            string
	club, computer int64
}

func parseHourlyMember(m string) hourlyMember {
	c, p, _ := strings.Cut(m, ":")
	out := hourlyMember{raw: m}
	out.club, _ = strconv.ParseInt(c, 10, 64)
	out.computer, _ = strconv.ParseInt(p, 10, 64)
	if out.club <= 0 || out.computer <= 0 {
		out.club = 0
	}
	return out
}

func compareHourlyMembers(a, b hourlyMember) int { return strings.Compare(a.raw, b.raw) }

// flushClub — строки часа ПК одного клуба (club = 0 — испорченные члены индекса: только удалить).
func (f *HourlyFlusher) flushClub(ctx context.Context, k hourlyKeys, hour time.Time, idx string, club int64, members []hourlyMember) (int, error) {
	hp := hourPart(hour)
	rows := make([]HourlyRow, 0, len(members))
	for batch := range slices.Chunk(members, hourlyBatch) {
		cmds := make(valkey.Commands, 0, 3*len(batch))
		for _, m := range batch {
			pc := strconv.FormatInt(m.computer, 10)
			cmds = append(cmds,
				f.Valkey.B().Hgetall().Key(k.agg(hp, pc)).Build(),
				f.Valkey.B().Bitcount().Key(k.bits(hp, pc)).Start(0).End(7).Build(),
				f.Valkey.B().Bitcount().Key(k.bits(hp, pc)).Start(8).End(15).Build())
		}
		res := f.Valkey.DoMulti(ctx, cmds...)
		for i, m := range batch {
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
			if club <= 0 || len(agg) == 0 {
				continue // ключ истёк или член испорчен — только убрать из индекса
			}
			rows = append(rows, rowFromAgg(club, m.computer, hour, agg, online, session))
		}
	}
	var written int64
	if len(rows) > 0 {
		if err := db.InTenantTx(ctx, f.App, club, func(q *store.Queries) error {
			written = 0
			for batch := range slices.Chunk(rows, hourlyBatch) {
				b, err := json.Marshal(batch)
				if err != nil {
					return err
				}
				n, err := q.ComputerHealthHourlyUpsert(ctx, store.ComputerHealthHourlyUpsertParams{ClubID: club, Rows: b})
				if err != nil {
					return err
				}
				written += n
			}
			return nil
		}); err != nil {
			return 0, fmt.Errorf("health: hourly upsert club %d: %w", club, err)
		}
		f.rows.Add(written)
	}
	del := make(valkey.Commands, 0, len(members)+len(members)/hourlyBatch+1)
	for _, m := range members {
		if club > 0 { // ключи испорченного члена не трогаем (истекут по TTL): номер ПК в нём ненадёжен
			pc := strconv.FormatInt(m.computer, 10)
			del = append(del, f.Valkey.B().Del().Key(k.agg(hp, pc), k.bits(hp, pc)).Build())
		}
	}
	for batch := range slices.Chunk(members, hourlyBatch) {
		raw := make([]string, len(batch))
		for i, m := range batch {
			raw[i] = m.raw
		}
		del = append(del, f.Valkey.B().Srem().Key(idx).Member(raw...).Build())
	}
	for _, r := range f.Valkey.DoMulti(ctx, del...) {
		if err := r.Error(); err != nil {
			return int(written), fmt.Errorf("health: hourly cleanup: %w", err)
		}
	}
	return int(written), nil
}
