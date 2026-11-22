// Package catalog — каталог клубов для гостевого приложения (тема В, docs/upstream-2026-11.md): список городов,
// оборудование зон и то, что из этого нужно и панели (настройки → «Город и адрес», «Профиль клуба»), и старому
// REST мобильных (server/internal/http/legacy/catalog.go).
//
// Laravel: app/Support/Cities.php, app/Support/ZoneDevices.php, app/Models/ClubPhoto.php.
package catalog

import "sort"

// City — город, в котором может быть клуб: ключ хранится в clubs.city, по координатам приложение находит ближайший
// к телефону город. Name — русское название (ключ перевода, как в старом: __($name)).
type City struct {
	Key  string
	Name string
	Lat  float64
	Lng  float64
}

// cities — список буквально, в порядке старого кода.
// Laravel: app/Support/Cities.php Cities::LIST.
var cities = []City{
	{"astana", "Астана", 51.1694, 71.4491},
	{"almaty", "Алматы", 43.2389, 76.8897},
	{"shymkent", "Шымкент", 42.3417, 69.5901},
	{"aktobe", "Актобе", 50.2839, 57.1670},
	{"karaganda", "Караганда", 49.8047, 73.1094},
	{"taraz", "Тараз", 42.9000, 71.3667},
	{"pavlodar", "Павлодар", 52.2873, 76.9674},
	{"oskemen", "Усть-Каменогорск", 49.9483, 82.6275},
	{"semey", "Семей", 50.4111, 80.2275},
	{"atyrau", "Атырау", 47.1167, 51.8833},
	{"kostanay", "Костанай", 53.2144, 63.6246},
	{"kyzylorda", "Кызылорда", 44.8528, 65.5092},
	{"oral", "Уральск", 51.2333, 51.3667},
	{"petropavl", "Петропавловск", 54.8667, 69.1500},
	{"aktau", "Актау", 43.6500, 51.1600},
	{"temirtau", "Темиртау", 50.0549, 72.9646},
	{"turkistan", "Туркестан", 43.2973, 68.2518},
	{"kokshetau", "Кокшетау", 53.2833, 69.3833},
	{"taldykorgan", "Талдыкорган", 45.0156, 78.3739},
	{"ekibastuz", "Экибастуз", 51.7298, 75.3266},
	{"zhezkazgan", "Жезказган", 47.7833, 67.7667},
	{"konaev", "Конаев", 43.8667, 77.0667},
	{"rudny", "Рудный", 52.9667, 63.1167},
	{"zhanaozen", "Жанаозен", 43.3412, 52.8619},
	{"balkhash", "Балхаш", 46.8481, 74.9950},
}

// CityByKey — город по ключу clubs.city; ok=false — ключа нет в списке (правило Rule::in(Cities::keys())).
func CityByKey(key string) (City, bool) {
	for _, c := range cities {
		if c.Key == key {
			return c, true
		}
	}
	return City{}, false
}

// CityOption — город с названием на языке запроса.
type CityOption struct {
	City
	// Label — __($name).
	Label string
}

// CityOptions — города по названию на языке запроса: asort() PHP сравнивает строки побайтно (не по правилам
// языка), сортировка устойчивая (PHP ≥ 8.0). tr — перевод названия.
// Laravel: app/Support/Cities.php Cities::options().
func CityOptions(tr func(name string) string) []CityOption {
	out := make([]CityOption, len(cities))
	for i, c := range cities {
		out[i] = CityOption{City: c, Label: tr(c.Name)}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}
