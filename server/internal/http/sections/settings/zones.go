package settings

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // база поясов в бинарнике: проверка timezone:all и смещения не зависят от ОС

	settingsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/settings"
)

// validTimezone — правило timezone:all: идентификатор из DateTimeZone::listIdentifiers() (строго, с учётом
// регистра) и известный базе Go.
func validTimezone(tz string) bool {
	if !slices.Contains(allZones, tz) {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// zoneLabel — "(UTC{offset P}) Зона_с_пробелами" (SettingsController::timezoneOptions(), $label).
func zoneLabel(zone string, now time.Time) string {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return zone
	}
	return fmt.Sprintf("(UTC%s) %s", now.In(loc).Format("-07:00"), strings.ReplaceAll(zone, "_", " "))
}

func zoneOffset(zone string, now time.Time) int {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return 0
	}
	_, off := now.In(loc).Zone()
	return off
}

// timezoneOptions — группы «Казахстан» (порядок списка PHP) и «Все часовые пояса» по текущему смещению:
// sortBy(sprintf('%06d %s', offset + 50000, zone)).
func timezoneOptions(now time.Time) (kz, all []settingsapi.TimezoneOption) {
	for _, z := range kazakhstanZones {
		kz = append(kz, settingsapi.TimezoneOption{Id: z, Label: zoneLabel(z, now)})
	}
	keys := make(map[string]string, len(allZones))
	sorted := slices.Clone(allZones)
	for _, z := range sorted {
		keys[z] = fmt.Sprintf("%06d %s", zoneOffset(z, now)+50000, z)
	}
	sort.SliceStable(sorted, func(i, j int) bool { return keys[sorted[i]] < keys[sorted[j]] })
	for _, z := range sorted {
		all = append(all, settingsapi.TimezoneOption{Id: z, Label: zoneLabel(z, now)})
	}
	return kz, all
}
