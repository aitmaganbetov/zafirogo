package pricing

import (
	"encoding/json"
	"fmt"
	"time"
)

// WeeklyPeriod — строка периода тарифа (tariffs.validity_periods / sale_periods):
// дни mon…sun, время "HH:MM". Период может идти через конец недели (сб 22:00 – пн 06:00).
type WeeklyPeriod struct {
	StartDay  string `json:"start_day"`
	StartTime string `json:"start_time"`
	EndDay    string `json:"end_day"`
	EndTime   string `json:"end_time"`
}

// WeeklyPeriods — список периодов; nil или пустой — «всегда».
type WeeklyPeriods []WeeklyPeriod

// ParseWeeklyPeriods разбирает jsonb колонки периодов. NULL/пусто → nil («всегда»).
// Значения полей, которые не строки (в старом коде форма пишет только строки), приводятся к "" —
// для дня это «неизвестный день» (период пропускается), для времени — 00:00, как ?? ” в PHP.
func ParseWeeklyPeriods(raw []byte) (WeeklyPeriods, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("pricing: periods: %w", err)
	}
	out := make(WeeklyPeriods, 0, len(rows))
	for _, r := range rows {
		str := func(k string) string { s, _ := r[k].(string); return s }
		out = append(out, WeeklyPeriod{StartDay: str("start_day"), StartTime: str("start_time"), EndDay: str("end_day"), EndTime: str("end_time")})
	}
	return out, nil
}

// weekDays — WeeklyPeriods::DAYS.
var weekDays = map[string]int{"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4, "sat": 5, "sun": 6}

// Contains — момент localNow (уже в часовом поясе клуба) внутри одного из периодов.
//
//	нет периодов → true («всегда»)
//	m = (isoWeekday-1)*1440 + hour*60 + minute          // секунды отброшены, пн = 0
//	start = DAYS[start_day]*1440 + minutes(start_time); end = DAYS[end_day]*1440 + minutes(end_time)
//	неизвестный день → период пропускается
//	start <= end: start <= m <= end; иначе (через конец недели): m >= start || m <= end
//
// Конец включительно до минуты: «до пн 06:00» действует весь пн 06:00:00–06:00:59.
// Laravel: app/Support/WeeklyPeriods.php contains() (стр. 17–39) (BR-017, GAP-017).
func (p WeeklyPeriods) Contains(localNow time.Time) bool {
	if len(p) == 0 {
		return true
	}
	isoDay := (int(localNow.Weekday())+6)%7 + 1 // Carbon dayOfWeekIso: пн = 1 … вс = 7
	minuteOfWeek := (isoDay-1)*1440 + localNow.Hour()*60 + localNow.Minute()
	for _, period := range p {
		startDay, ok1 := weekDays[period.StartDay]
		endDay, ok2 := weekDays[period.EndDay]
		if !ok1 || !ok2 {
			continue
		}
		start := startDay*1440 + periodMinutes(period.StartTime)
		end := endDay*1440 + periodMinutes(period.EndTime)
		var hit bool
		if start <= end {
			hit = minuteOfWeek >= start && minuteOfWeek <= end
		} else {
			hit = minuteOfWeek >= start || minuteOfWeek <= end
		}
		if hit {
			return true
		}
	}
	return false
}

// ContainsAt — Contains для момента at в часовом поясе клуба loc (Tariff::onSale()/inValidityPeriod():
// Carbon::now(CurrentClub::localTimezone())). loc == nil — UTC (config('app.timezone') старого проекта).
func (p WeeklyPeriods) ContainsAt(at time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.UTC
	}
	return p.Contains(at.In(loc))
}

// IsAlways — периоды не ограничивают: их нет или есть период пн 00:00 – вс 23:59.
// Laravel: app/Support/WeeklyPeriods.php isAlways() (стр. 42–46).
func (p WeeklyPeriods) IsAlways() bool {
	if len(p) == 0 {
		return true
	}
	for _, period := range p {
		if period.StartDay == "mon" && period.StartTime == "00:00" && period.EndDay == "sun" && period.EndTime == "23:59" {
			return true
		}
	}
	return false
}

// periodMinutes — WeeklyPeriods::minutes(): preg_match('/^(\d{2}):(\d{2})$/') ? HH*60+MM : 0.
// Часы и минуты не проверяются на диапазон ("99:99" → 6039). PCRE `$` без модификатора D совпадает
// и перед завершающим "\n", поэтому "12:30\n" тоже 750 — как в PHP.
func periodMinutes(t string) int {
	if len(t) == 6 && t[5] == '\n' {
		t = t[:5]
	}
	if len(t) != 5 || t[2] != ':' || !isASCIIDigit(t[0]) || !isASCIIDigit(t[1]) || !isASCIIDigit(t[3]) || !isASCIIDigit(t[4]) {
		return 0
	}
	return (int(t[0]-'0')*10+int(t[1]-'0'))*60 + int(t[3]-'0')*10 + int(t[4]-'0')
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }
