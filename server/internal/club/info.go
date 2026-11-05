package club

// Данные клуба, которые экраны панели берут из CurrentClub старого кода: часовой пояс, валюта, интервал броней.

import (
	"context"
	"fmt"
	"time"

	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Info — клуб запроса.
type Info struct {
	ID       int64
	Name     string
	Locale   string
	Settings []byte
	// Loc — CurrentClub::timezone() (пусто — UTC, как config('app.timezone')).
	Loc *time.Location
}

// Load читает клуб в тенантной транзакции.
func Load(ctx context.Context, q *store.Queries, clubID int64) (Info, error) {
	c, err := q.GetClub(ctx, clubID)
	if err != nil {
		return Info{}, fmt.Errorf("club: %w", err)
	}
	loc := time.UTC
	if c.Timezone != "" {
		if l, err := time.LoadLocation(c.Timezone); err == nil {
			loc = l
		}
	}
	info := Info{ID: c.ID, Name: c.Name, Settings: c.Settings, Loc: loc}
	if c.Locale != nil {
		info.Locale = *c.Locale
	}
	return info, nil
}

// Currency — data_get(settings, 'finances.currency', 'KZT').
func (i Info) Currency() string { return billingsvc.ClubCurrency(i.Settings) }

// BookingGapMinutes — ComputerReservations::gapMinutes().
func (i Info) BookingGapMinutes() int64 { return billingsvc.BookingGapMinutes(i.Settings) }

// StartOfDay — начало локального дня клуба, в котором лежит момент t (Carbon::now($tz)->startOfDay()).
func (i Info) StartOfDay(t time.Time) time.Time {
	l := t.In(i.Loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, i.Loc)
}

// StartOfWeek — понедельник 00:00 недели момента t в поясе клуба (Carbon startOfWeek(): неделя с понедельника).
func (i Info) StartOfWeek(t time.Time) time.Time {
	d := i.StartOfDay(t)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

// ParseDay — день "Y-m-d" в поясе клуба (Carbon::createFromFormat('Y-m-d', $date, $tz)->startOfDay()).
func (i Info) ParseDay(s string) (time.Time, bool) {
	d, err := time.ParseInLocation("2006-01-02", s, i.Loc)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

// ComputerName — ClubComputer::displayName(): display_name ?: device_name ?: sprintf('PC-%02d', number).
func ComputerName(displayName, deviceName *string, number int64) string {
	if displayName != nil && *displayName != "" {
		return *displayName
	}
	if deviceName != nil && *deviceName != "" {
		return *deviceName
	}
	return fmt.Sprintf("PC-%02d", number)
}
