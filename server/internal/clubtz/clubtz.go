// Package clubtz — часовой пояс клуба (clubs.timezone) одной функцией на весь сервер.
//
// Laravel: app/Support/CurrentClub.php CurrentClub::timezone() / localTimezone() — пустая строка →
// config('app.timezone') (UTC).
package clubtz

import (
	"fmt"
	"time"
)

// Location — пояс клуба для показа и границ суток: пустая строка или неизвестный базе Go идентификатор — UTC
// (экраны панели, агент, мониторинг: страница не должна падать из-за пояса).
func Location(tz string) *time.Location {
	loc, err := Strict(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Strict — пояс клуба для денежного пути (покупка времени: периоды продажи и действия тарифа считаются во
// времени клуба): пустая строка — UTC, неизвестный идентификатор — ошибка, а не молчаливый UTC.
func Strict(tz string) (*time.Location, error) {
	if tz == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("club timezone %q: %w", tz, err)
	}
	return loc, nil
}
