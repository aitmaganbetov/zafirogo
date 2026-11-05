//go:build integration

package httpserver

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// legacyWeeklySQL — ComputerHealthWeekly до переписывания (PR #12, коммит ffd5f20): два коррелированных EXISTS по журналу
// проблем на каждую часовую строку, -1 вместо NULL, колонка hours. Эталон TestComputerHealthWeeklyMatchesLegacy.
const legacyWeeklySQL = `
SELECT width_bucket(h.hour, $1::timestamptz[])::integer AS week_index,
       count(*)::bigint AS hours,
       coalesce(sum(h.online_minutes), 0)::bigint AS online_minutes,
       coalesce(sum(h.session_minutes), 0)::bigint AS session_minutes,
       coalesce(sum(h.cpu_load_avg_bp::bigint * h.online_minutes)
           / nullif(sum(h.online_minutes) FILTER (WHERE h.cpu_load_avg_bp IS NOT NULL), 0), -1)::integer AS cpu_load_avg_bp,
       coalesce(max(h.cpu_load_max_bp), -1)::integer AS cpu_load_max_bp,
       coalesce(sum(h.ram_avg_bp::bigint * h.online_minutes)
           / nullif(sum(h.online_minutes) FILTER (WHERE h.ram_avg_bp IS NOT NULL), 0), -1)::integer AS ram_avg_bp,
       coalesce(max(h.ram_max_bp), -1)::integer AS ram_max_bp,
       coalesce(sum(h.gpu_load_avg_bp::bigint * h.online_minutes)
           / nullif(sum(h.online_minutes) FILTER (WHERE h.gpu_load_avg_bp IS NOT NULL), 0), -1)::integer AS gpu_load_avg_bp,
       coalesce(max(h.gpu_load_max_bp), -1)::integer AS gpu_load_max_bp,
       coalesce(sum(h.cpu_temp_avg_deci::bigint * h.online_minutes)
           / nullif(sum(h.online_minutes) FILTER (WHERE h.cpu_temp_avg_deci IS NOT NULL), 0), -1)::integer AS cpu_temp_avg_deci,
       coalesce(max(h.cpu_temp_max_deci), -1)::integer AS cpu_temp_max_deci,
       coalesce(sum(h.gpu_temp_avg_deci::bigint * h.online_minutes)
           / nullif(sum(h.online_minutes) FILTER (WHERE h.gpu_temp_avg_deci IS NOT NULL), 0), -1)::integer AS gpu_temp_avg_deci,
       coalesce(max(h.gpu_temp_max_deci), -1)::integer AS gpu_temp_max_deci,
       count(*) FILTER (WHERE h.cpu_temp_max_deci >= $2::integer)::bigint AS cpu_hot_hours,
       count(*) FILTER (WHERE h.gpu_temp_max_deci >= $3::integer)::bigint AS gpu_hot_hours,
       count(*) FILTER (WHERE EXISTS (
           SELECT 1 FROM computer_health_events e
           WHERE e.club_id = h.club_id AND e.computer_id = h.computer_id
             AND e.raised_at < h.hour + interval '1 hour' AND (e.cleared_at IS NULL OR e.cleared_at > h.hour)
             AND NOT ($4::boolean AND e.kind IN ('disk_low', 'disk_smart'))))::bigint
           AS problem_hours,
       count(*) FILTER (WHERE EXISTS (
           SELECT 1 FROM computer_health_events e
           WHERE e.club_id = h.club_id AND e.computer_id = h.computer_id AND e.kind = 'network_slow'
             AND e.raised_at < h.hour + interval '1 hour' AND (e.cleared_at IS NULL OR e.cleared_at > h.hour)))::bigint
           AS network_slow_hours,
       coalesce(sum(h.unexpected_shutdowns), 0)::bigint AS unexpected_shutdowns,
       coalesce(sum(h.bugchecks), 0)::bigint AS bugchecks,
       coalesce(sum(h.peripheral_disconnects), 0)::bigint AS peripheral_disconnects,
       coalesce(min(h.system_disk_free_mb), -1)::bigint AS system_disk_free_mb,
       coalesce(min(h.system_disk_free_mb * 10000 / nullif(h.system_disk_total_mb, 0)), -1)::bigint AS system_disk_free_bp,
       coalesce(max(CASE h.smart_worst WHEN 'failing' THEN 3 WHEN 'warning' THEN 2 WHEN 'ok' THEN 1 END), 0)::integer AS smart_rank
FROM computer_health_hourly h
WHERE h.club_id = $5 AND h.computer_id = $6
  AND h.hour >= $7::timestamptz AND h.hour < $8::timestamptz
GROUP BY 1
ORDER BY 1 DESC`

// Переписанный ComputerHealthWeekly (один проход по журналу, NULL вместо -1, нормализация бездискового ПК в SQL) даёт
// то же, что прежний запрос вместе с прежней обработкой в Go (-1 → null; у бездискового — без системного диска и SMART):
// случайная почасовая история с пропусками измерений и случайный журнал проблем (открытые, закрытые, начатые до периода,
// диски и сеть), обычный и бездисковый ПК.
func TestComputerHealthWeeklyMatchesLegacy(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 4, 7, 0, 0, 0, time.UTC)
	e := newWeeklyEnv(t, now)
	zone := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Зал', now(), now()) RETURNING id", e.club)
	pc := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 1, now(), now()) RETURNING id", e.club, zone)
	opt := func(expr string) string { return "CASE WHEN random() < 0.2 THEN NULL ELSE " + expr + " END" }
	e.exec(fmt.Sprintf(`SELECT setseed(0.2026);
		INSERT INTO computer_health_hourly (club_id, computer_id, hour, online_minutes, session_minutes,
			cpu_load_avg_bp, cpu_load_max_bp, ram_avg_bp, ram_max_bp, gpu_load_avg_bp, gpu_load_max_bp,
			cpu_temp_avg_deci, cpu_temp_max_deci, gpu_temp_avg_deci, gpu_temp_max_deci, system_disk_free_mb, system_disk_total_mb,
			smart_worst, peripheral_disconnects, unexpected_shutdowns, bugchecks)
		SELECT %d, %d, g, 1 + floor(random() * 60)::int, floor(random() * 30)::int,
			%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, 512000,
			(ARRAY[NULL, 'ok', 'warning', 'failing'])[1 + floor(random() * 4)::int],
			floor(random() * 2)::int, floor(random() * 1.2)::int, floor(random() * 1.1)::int
		FROM generate_series('2026-09-27T00:00:00Z'::timestamptz, '2026-11-10T00:00:00Z', interval '1 hour') g
		WHERE random() < 0.4`, e.club, pc,
		opt("floor(random() * 10000)::int"), opt("floor(random() * 10000)::int"), opt("floor(random() * 10000)::int"),
		opt("floor(random() * 10000)::int"), opt("floor(random() * 10000)::int"), opt("floor(random() * 10000)::int"),
		opt("300 + floor(random() * 700)::int"), opt("300 + floor(random() * 700)::int"),
		opt("300 + floor(random() * 700)::int"), opt("300 + floor(random() * 700)::int"),
		opt("floor(random() * 512000)::bigint")))
	// Журнал: закрытые проблемы случайных видов и длительности (в том числе начатые до периода) и открытые — по одной
	// на вид (computer_health_events_open_index).
	e.exec(fmt.Sprintf(`SELECT setseed(0.7);
		INSERT INTO computer_health_events (club_id, computer_id, kind, raised_at, cleared_at)
		SELECT %d, %d, k, r, r + (random() * interval '30 hours')
		FROM (SELECT (ARRAY['cpu_hot', 'gpu_hot', 'disk_low', 'disk_smart', 'mouse', 'network_slow'])[1 + floor(random() * 6)::int] AS k,
		             '2026-09-20T00:00:00Z'::timestamptz + random() * interval '50 days' AS r
		      FROM generate_series(1, 120)) x;
		INSERT INTO computer_health_events (club_id, computer_id, kind, raised_at) VALUES
			(%d, %d, 'disk_low', '2026-10-30T05:20:00Z'), (%d, %d, 'network_slow', '2026-11-01T22:10:00Z'),
			(%d, %d, 'mouse', '2026-11-03T03:00:00Z')`, e.club, pc, e.club, pc, e.club, pc, e.club, pc))

	cur := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	bounds := weekBoundsForTest(cur, 5)
	q := store.New(e.db.Owner)
	for _, diskless := range []bool{false, true} {
		type row = [23]*int64
		legacy := map[int64]row{}
		rows, err := e.db.Owner.Query(ctx, legacyWeeklySQL, bounds, int32(900), int32(850), diskless, e.club, pc,
			bounds[0], bounds[len(bounds)-1])
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var v [24]int64
			ptrs := make([]any, len(v))
			for i := range v {
				ptrs[i] = &v[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var r row
			for i, x := range append(v[:1:1], v[2:]...) { // без колонки hours
				r[i] = &x
			}
			// Прежняя обработка в Go: -1 → null (средние, максимумы, диск); бездисковый — без диска и SMART.
			for _, i := range []int{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 20, 21} {
				if *r[i] < 0 || (diskless && i >= 20) {
					r[i] = nil
				}
			}
			if diskless {
				zero := int64(0)
				r[22] = &zero
			}
			legacy[v[0]] = r
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		hidden := []string{}
		if diskless {
			hidden = health.DiskKinds
		}
		got, err := q.ComputerHealthWeekly(ctx, store.ComputerHealthWeeklyParams{Bounds: bounds, CpuHotDeci: 900, GpuHotDeci: 850,
			Diskless: diskless, HiddenKinds: hidden, ClubID: e.club, ComputerID: pc, FromAt: bounds[0], ToAt: bounds[len(bounds)-1]})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(legacy) || len(got) < 5 {
			t.Fatalf("diskless %v: %d weeks, legacy %d", diskless, len(got), len(legacy))
		}
		i64 := func(v int64) *int64 { return &v }
		var problems, network int64
		for _, g := range got {
			problems, network = problems+g.ProblemHours, network+g.NetworkSlowHours
			n := row{i64(int64(g.WeekIndex)), i64(g.OnlineMinutes), i64(g.SessionMinutes),
				opt64(g.CpuLoadAvgBp), opt64(g.CpuLoadMaxBp), opt64(g.RamAvgBp), opt64(g.RamMaxBp), opt64(g.GpuLoadAvgBp),
				opt64(g.GpuLoadMaxBp), opt64(g.CpuTempAvgDeci), opt64(g.CpuTempMaxDeci), opt64(g.GpuTempAvgDeci),
				opt64(g.GpuTempMaxDeci), i64(g.CpuHotHours), i64(g.GpuHotHours), i64(g.ProblemHours), i64(g.NetworkSlowHours),
				i64(g.UnexpectedShutdowns), i64(g.Bugchecks), i64(g.PeripheralDisconnects), g.SystemDiskFreeMb, g.SystemDiskFreeBp,
				i64(int64(g.SmartRank))}
			if want := legacy[int64(g.WeekIndex)]; !reflect.DeepEqual(n, want) {
				t.Errorf("diskless %v week %d:\n got %v\nwant %v", diskless, g.WeekIndex, deref(n[:]), deref(want[:]))
			}
		}
		if problems == 0 || network == 0 {
			t.Fatalf("diskless %v: data must have problem hours (%d) and slow network hours (%d)", diskless, problems, network)
		}
		t.Logf("diskless %v: %d weeks, problem hours %d, slow network hours %d", diskless, len(got), problems, network)
	}
}

func weekBoundsForTest(cur time.Time, weeks int) []time.Time {
	out := make([]time.Time, 0, weeks+2)
	for i := weeks; i >= -1; i-- {
		out = append(out, cur.AddDate(0, 0, -7*i))
	}
	return out
}

func opt64(v *int32) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

func deref(v []*int64) []any {
	out := make([]any, len(v))
	for i, p := range v {
		if p != nil {
			out[i] = *p
		}
	}
	return out
}
