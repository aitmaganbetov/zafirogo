package dashboard

import (
	"reflect"
	"testing"
	"time"

	dashboardapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/dashboard"
)

func TestEveryOperationHasRight(t *testing.T) {
	if bad := access.Uncovered(reflect.TypeFor[dashboardapi.StrictServerInterface]()); len(bad) > 0 {
		t.Fatal(bad)
	}
}

// Дни клуба — локальные: Asia/Almaty (UTC+5), «сейчас» 2026-10-15 01:00 местного = 14.10 20:00 UTC.
func TestDayBounds(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Almaty")
	if err != nil {
		t.Skip(err)
	}
	b := DayBounds(time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC), loc)
	if len(b) != 8 || !b[6].Equal(time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)) || !b[7].Equal(time.Date(2026, 10, 15, 19, 0, 0, 0, time.UTC)) ||
		!b[0].Equal(time.Date(2026, 10, 8, 19, 0, 0, 0, time.UTC)) {
		t.Fatalf("bounds %v", b)
	}
	if got := b[6].In(loc).Format("02.01"); got != "15.10" {
		t.Fatalf("label %s", got)
	}
}
