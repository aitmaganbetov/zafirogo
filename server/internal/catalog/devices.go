package catalog

import (
	"regexp"
	"strconv"
	"strings"
)

// PhotoLimit — сколько фото может быть у клуба. Laravel: app/Models/ClubPhoto.php ClubPhoto::LIMIT.
const PhotoLimit = 10

// DeviceField — поле оборудования зоны: ключ club_zones.devices и подпись (ключ перевода).
type DeviceField struct {
	Key   string
	Label string
}

// deviceFields — в порядке старого кода (так же идут поля формы и список оборудования в приложении).
// Laravel: app/Support/ZoneDevices.php ZoneDevices::fields().
var deviceFields = []DeviceField{
	{"cpu", "Процессор"},
	{"gpu", "Видеокарта"},
	{"ram", "Оперативная память"},
	{"monitor", "Монитор"},
	{"keyboard", "Клавиатура"},
	{"mouse", "Мышь"},
	{"headset", "Гарнитура"},
	{"chair", "Кресло"},
}

// DeviceFields — поля оборудования зоны.
func DeviceFields() []DeviceField { return append([]DeviceField(nil), deviceFields...) }

// RAMText — ключ перевода текста памяти («16 ГБ»), подстановка gb.
const RAMText = ":gb ГБ"

// Шаги ZoneDevices::cpu(). Регулярные выражения PHP без флага u: \s, \d, \b и /i — только ASCII (в Go (?i) ещё
// сворачивает «ſ» и «K», поэтому регистр задан классами); \s PCRE включает \v; «$» PCRE стоит и перед последним
// переводом строки.
var (
	cpuSteps = []*regexp.Regexp{
		regexp.MustCompile(`\(([rR]|[tT][mM])\)`),
		regexp.MustCompile(`\bCPU\b`),
		regexp.MustCompile(`@[^\n]*\n?\z`),
		regexp.MustCompile(`^[0-9]+([sS][tT]|[nN][dD]|[rR][dD]|[tT][hH]) [gG][eE][nN][\t\n\v\f\r ]+`),
		regexp.MustCompile(`[\t\n\v\f\r ]+[0-9]+-[cC][oO][rR][eE] [pP][rR][oO][cC][eE][sS][sS][oO][rR]`),
	}
	spaces = regexp.MustCompile(`[\t\n\v\f\r ]+`)
)

// phpTrim — trim() PHP: пробел, \t, \n, \r, \0, \v.
func phpTrim(s string) string { return strings.Trim(s, " \t\n\r\x00\v") }

// CPUName — название процессора для гостя: «11th Gen Intel(R) Core(TM) i5-11400F @ 2.60GHz» → «Intel Core i5-11400F».
// Laravel: app/Support/ZoneDevices.php ZoneDevices::cpu().
func CPUName(model string) string {
	for _, re := range cpuSteps {
		model = re.ReplaceAllString(model, "")
	}
	return phpTrim(spaces.ReplaceAllString(model, " "))
}

// GPUName — trim($gpu). Laravel: ZoneDevices::detected().
func GPUName(model string) string { return phpTrim(model) }

// RAMGigabytes — (int) round($megabytes / 1024): деление на степень двойки точное, округление половины — от нуля
// (16253 МБ → 16, 1536 → 2, 1535 → 1).
// Laravel: app/Support/ZoneDevices.php ZoneDevices::detected().
func RAMGigabytes(megabytes int64) int64 {
	if megabytes < 0 {
		return 0
	}
	return (megabytes + 512) / 1024
}

// Detected — железо зоны по данным агентов: самый частый процессор и видеокарта, наибольшая память.
// Пустая строка — агенты не сообщали (HasCPU и т. п. — значение было, пусть и пустое после чистки).
type Detected struct {
	CPU, GPU       string
	HasCPU, HasGPU bool
	RAMMegabytes   int64
	HasRAM         bool
}

// Values — найденное железо как значения полей cpu, gpu, ram. ram — текст RAMText на языке запроса (tr).
// Laravel: app/Support/ZoneDevices.php ZoneDevices::detected().
func (d Detected) Values(tr func(key string, params map[string]string) string) map[string]string {
	out := map[string]string{}
	if d.HasCPU {
		out["cpu"] = CPUName(d.CPU)
	}
	if d.HasGPU {
		out["gpu"] = GPUName(d.GPU)
	}
	if d.HasRAM {
		out["ram"] = tr(RAMText, map[string]string{"gb": strconv.FormatInt(RAMGigabytes(d.RAMMegabytes), 10)})
	}
	return out
}

// CleanDevices — оборудование зоны из формы: только известные ключи, значения без пробелов по краям, пустые
// убраны (array_filter(array_map(trim…), fn ($v) => $v !== ”)). Пустой результат — nil (в базе NULL).
// Laravel: app/Http/Controllers/ClubProfileController.php ClubProfileController::updateZone().
func CleanDevices(in map[string]string) map[string]string {
	out := map[string]string{}
	for _, f := range deviceFields {
		if v := phpTrim(in[f.Key]); v != "" {
			out[f.Key] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
