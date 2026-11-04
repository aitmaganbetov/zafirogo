package health

import (
	"reflect"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// Минуты онлайн, которые отмечает Status: своя минута и минуты с предыдущего Status (разрыв ≤ maxGap), только в
// текущем часе.
func TestOnlineMinutes(t *testing.T) {
	h := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	gap := 150 * time.Second
	cases := []struct {
		name      string
		since, at time.Time
		want      []int
	}{
		{"first status", time.Time{}, h.Add(30 * time.Second), []int{0}},
		{"status every 60 s skipping a minute", h.Add(59 * time.Second), h.Add(2 * time.Minute), []int{0, 1, 2}},
		{"same minute", h.Add(10 * time.Second), h.Add(40 * time.Second), []int{0}},
		{"across the hour — only this hour", h.Add(-30 * time.Second), h.Add(40 * time.Second), []int{0}},
		{"previous status from last hour, later minute", h.Add(-20 * time.Second), h.Add(70 * time.Second), []int{0, 1}},
		{"gap too long — only this minute", h.Add(10 * time.Minute), h.Add(20 * time.Minute), []int{20}},
		{"since after at (clock skew)", h.Add(5 * time.Minute), h.Add(4 * time.Minute), []int{4}},
		{"last minute of hour", h.Add(58*time.Minute + 30*time.Second), h.Add(59*time.Minute + 59*time.Second), []int{58, 59}},
		{"local time zone input", time.Time{}, h.Add(15 * time.Minute).In(time.FixedZone("x", 5*3600+1800)), []int{15}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OnlineMinutes(tc.since, tc.at, gap); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("OnlineMinutes = %v, want %v", got, tc.want)
			}
		})
	}
}

// Минуты в сессии: без минуты предыдущего Status (тогда ПК мог быть ещё не в сессии).
func TestSessionMinutes(t *testing.T) {
	h := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		since, at time.Time
		want      []int
	}{
		{"first status", time.Time{}, h.Add(10*time.Minute + 5*time.Second), []int{10}},
		{"next minute", h.Add(9*time.Minute + 5*time.Second), h.Add(10*time.Minute + 5*time.Second), []int{10}},
		{"skipped minute", h.Add(8*time.Minute + 59*time.Second), h.Add(10 * time.Minute), []int{9, 10}},
		{"same minute", h.Add(10 * time.Minute), h.Add(10*time.Minute + 30*time.Second), []int{10}},
		{"previous status in last hour", h.Add(-20 * time.Second), h.Add(70 * time.Second), []int{0, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SessionMinutes(OnlineMinutes(tc.since, tc.at, 150*time.Second), tc.since, tc.at); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SessionMinutes = %v, want %v", got, tc.want)
			}
		})
	}
}

// Отключения периферии: устройство мыши/клавиатуры/монитора было подключено и перестало (или пропало); гарнитура — нет.
func TestPeripheralDisconnects(t *testing.T) {
	p := func(kind, name string, c bool) presence.Peripheral {
		return presence.Peripheral{Kind: kind, Name: name, Connected: c}
	}
	hh := func(ps ...presence.Peripheral) *presence.Health { return &presence.Health{Peripherals: ps} }
	all := hh(p("mouse", "G102", true), p("keyboard", "K120", true), p("monitor", "VG248", true), p("headset", "HS", true))
	cases := []struct {
		name      string
		prev, cur *presence.Health
		want      int
	}{
		{"no previous", nil, all, 0},
		{"no current", all, nil, 0},
		{"same", all, all, 0},
		{"mouse unplugged", all, hh(p("mouse", "G102", false), p("keyboard", "K120", true), p("monitor", "VG248", true), p("headset", "HS", true)), 1},
		{"mouse vanished, monitor off", all, hh(p("keyboard", "K120", true), p("monitor", "VG248", false)), 2},
		{"headset removed — not counted", all, hh(p("mouse", "G102", true), p("keyboard", "K120", true), p("monitor", "VG248", true)), 0},
		{"mouse replaced by another", all, hh(p("mouse", "M100", true), p("keyboard", "K120", true), p("monitor", "VG248", true), p("headset", "HS", true)), 1},
		{"was already off", hh(p("mouse", "G102", false)), hh(p("mouse", "G102", false)), 0},
		{"plugged back — not a disconnect", hh(p("mouse", "G102", false)), hh(p("mouse", "G102", true)), 0},
		{"two same monitors, one off", hh(p("monitor", "VG248", true), p("monitor", "VG248", true)),
			hh(p("monitor", "VG248", true), p("monitor", "VG248", false)), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PeripheralDisconnects(tc.prev, tc.cur); got != tc.want {
				t.Errorf("PeripheralDisconnects = %d, want %d", got, tc.want)
			}
		})
	}
}

// Строка часа из агрегата: среднее — целым делением, нет измерения — null, SMART — по рангу, минуты ≤ 60.
func TestRowFromAgg(t *testing.T) {
	hour := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	agg := map[string]string{"v": "1",
		"cpu_n": "3", "cpu_s": "10000", "cpu_mn": "1000", "cpu_mx": "6000",
		"ct_n": "2", "ct_s": "1301", "ct_mn": "600", "ct_mx": "701",
		"df": "40960", "dt": "512000", "sm": "2", "pd": "1", "sd": "2", "bc": "1"}
	r := rowFromAgg(5, 7, hour, agg, 58, 70)
	i := func(v int64) *int64 { return &v }
	warn := presence.SmartWarning
	want := HourlyRow{ClubID: 5, ComputerID: 7, Hour: hour, OnlineMinutes: 58, SessionMinutes: 60,
		CPULoadMinBP: i(1000), CPULoadAvgBP: i(3333), CPULoadMaxBP: i(6000),
		CPUTempMinDeci: i(600), CPUTempAvgDeci: i(650), CPUTempMaxDeci: i(701),
		SystemDiskFreeMB: i(40960), SystemDiskTotalMB: i(512000), SmartWorst: &warn,
		PeripheralDisconnects: 1, UnexpectedShutdowns: 2, Bugchecks: 1}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("rowFromAgg =\n%+v\nwant\n%+v", r, want)
	}
	if e := rowFromAgg(5, 7, hour, map[string]string{"v": "1"}, 1, 0); e.CPULoadAvgBP != nil || e.SmartWorst != nil ||
		e.SystemDiskFreeMB != nil || e.OnlineMinutes != 1 {
		t.Errorf("empty aggregate: %+v", e)
	}
}
