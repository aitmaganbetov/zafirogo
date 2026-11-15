package clubtz

import (
	"testing"
	"time"
)

// BC-05: два поведения одного правила — «для показа» (UTC при неизвестном поясе) и «для денег» (ошибка).
func TestLocation(t *testing.T) {
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		tz         string
		wantName   string
		wantOffset int // секунд от UTC в январе
		strictErr  bool
	}{
		{"", "UTC", 0, false},
		{"UTC", "UTC", 0, false},
		{"Asia/Almaty", "Asia/Almaty", 5 * 3600, false},
		{"Europe/Moscow", "Europe/Moscow", 3 * 3600, false},
		{"America/New_York", "America/New_York", -5 * 3600, false},
		{"Mars/Olympus", "UTC", 0, true},
		{"asia/almaty ", "UTC", 0, true},
		{"+05:00", "UTC", 0, true},
		{"../etc/passwd", "UTC", 0, true},
	}
	for _, c := range cases {
		t.Run(c.tz, func(t *testing.T) {
			loc := Location(c.tz)
			if _, off := at.In(loc).Zone(); loc.String() != c.wantName || off != c.wantOffset {
				t.Fatalf("Location(%q) = %s (%d с), want %s (%d с)", c.tz, loc, off, c.wantName, c.wantOffset)
			}
			strict, err := Strict(c.tz)
			if (err != nil) != c.strictErr {
				t.Fatalf("Strict(%q) err = %v, want error: %v", c.tz, err, c.strictErr)
			}
			if err == nil && strict.String() != loc.String() {
				t.Fatalf("Strict(%q) = %s, Location = %s", c.tz, strict, loc)
			}
			if err != nil && strict != nil {
				t.Fatalf("Strict(%q) при ошибке вернул пояс %s", c.tz, strict)
			}
		})
	}
	if Location("") != time.UTC {
		t.Fatal("пустой пояс — time.UTC")
	}
}
