package notifications

import (
	"strconv"
	"time"
)

// diffForHumans — Carbon::diffForHumans() (одна единица, относительно now) на языке клуба: «5 минут назад»,
// «через 2 часа», «3 days ago». Единицы — как в Carbon: секунды, минуты, часы, дни, недели, месяцы, годы
// (вперёд — «через …»). Формы — переводы Carbon (resources/lang Carbon: ru, kk, en).
func diffForHumans(at, now time.Time, locale string) string {
	d := now.Sub(at)
	future := d < 0
	if future {
		d = -d
	}
	unit, n := humanUnit(at, now, d, future)
	words := unitWords[locale]
	if words == nil {
		words = unitWords["ru"]
	}
	text := plural(words[unit], n)
	tpl := agoTpl
	if future {
		tpl = fromNowTpl
	}
	t, ok := tpl[locale]
	if !ok {
		t = tpl["ru"]
	}
	return t[0] + text + t[1]
}

func humanUnit(at, now time.Time, d time.Duration, future bool) (string, int64) {
	secs := int64(d / time.Second)
	switch {
	case secs < 60:
		return "second", secs
	case secs < 3600:
		return "minute", secs / 60
	case secs < 86400:
		return "hour", secs / 3600
	}
	days := secs / 86400
	from, to := at, now
	if future {
		from, to = now, at
	}
	months := int64(to.Year()-from.Year())*12 + int64(to.Month()-from.Month())
	if to.Day() < from.Day() {
		months--
	}
	switch {
	case months >= 12:
		return "year", months / 12
	case months >= 1:
		return "month", months
	case days >= 7:
		return "week", days / 7
	}
	return "day", days
}

// agoTpl / fromNowTpl — 'ago' => ':time назад', 'from_now' => 'через :time'.
var (
	agoTpl     = map[string][2]string{"ru": {"", " назад"}, "kk": {"", " бұрын"}, "en": {"", " ago"}}
	fromNowTpl = map[string][2]string{"ru": {"через ", ""}, "kk": {"", " ішінде"}, "en": {"in ", ""}}
)

// unitWords — формы единиц Carbon: ru — 3 формы (1, 2–4, 5+), kk — одна, en — 2.
var unitWords = map[string]map[string][]string{
	"ru": {
		"second": {"секунду", "секунды", "секунд"}, "minute": {"минуту", "минуты", "минут"},
		"hour": {"час", "часа", "часов"}, "day": {"день", "дня", "дней"}, "week": {"неделю", "недели", "недель"},
		"month": {"месяц", "месяца", "месяцев"}, "year": {"год", "года", "лет"},
	},
	"kk": {
		"second": {"секунд"}, "minute": {"минут"}, "hour": {"сағат"}, "day": {"күн"}, "week": {"апта"},
		"month": {"ай"}, "year": {"жыл"},
	},
	"en": {
		"second": {"second", "seconds"}, "minute": {"minute", "minutes"}, "hour": {"hour", "hours"},
		"day": {"day", "days"}, "week": {"week", "weeks"}, "month": {"month", "months"}, "year": {"year", "years"},
	},
}

func plural(forms []string, n int64) string {
	idx := 0
	switch {
	case len(forms) == 3: // ru
		m10, m100 := n%10, n%100
		switch {
		case m10 == 1 && m100 != 11:
			idx = 0
		case m10 >= 2 && m10 <= 4 && (m100 < 10 || m100 >= 20):
			idx = 1
		default:
			idx = 2
		}
	case len(forms) == 2 && n != 1:
		idx = 1
	}
	return strconv.FormatInt(n, 10) + " " + forms[idx]
}
