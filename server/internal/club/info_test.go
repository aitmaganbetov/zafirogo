package club

import (
	"testing"
	"time"
)

// Начало недели клуба — понедельник 00:00 в его поясе (Carbon startOfWeek()); неделя, начатая до перехода на летнее
// время, — по местному календарю, а не «минус 7×24 ч».
func TestStartOfWeek(t *testing.T) {
	almaty, err := time.LoadLocation("Asia/Almaty")
	if err != nil {
		t.Skip(err)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	cases := []struct {
		name string
		loc  *time.Location
		at   time.Time
		want time.Time
	}{
		{"wednesday", almaty, time.Date(2026, 11, 4, 7, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, almaty)},
		{"sunday late UTC is monday local", almaty, time.Date(2026, 11, 1, 19, 30, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, almaty)},
		{"sunday local", almaty, time.Date(2026, 11, 1, 18, 59, 0, 0, time.UTC), time.Date(2026, 10, 26, 0, 0, 0, 0, almaty)},
		{"monday midnight", time.UTC, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)},
		{"week with DST change", berlin, time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC), time.Date(2026, 3, 23, 0, 0, 0, 0, berlin)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Info{Loc: tc.loc}).StartOfWeek(tc.at); !got.Equal(tc.want) {
				t.Errorf("StartOfWeek(%s) = %s, want %s", tc.at, got, tc.want)
			}
		})
	}
}
