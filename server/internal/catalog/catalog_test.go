package catalog

import (
	"reflect"
	"strings"
	"testing"
)

// Cities::LIST — 25 городов; порядок и координаты — буквально из старого кода.
func TestCities(t *testing.T) {
	all := cities
	if len(all) != 25 {
		t.Fatalf("cities %d, want 25", len(all))
	}
	if all[0].Key != "astana" || all[24].Key != "balkhash" {
		t.Errorf("order: %s … %s", all[0].Key, all[24].Key)
	}
	c, ok := CityByKey("aktobe")
	if !ok || c.Name != "Актобе" || c.Lat != 50.2839 || c.Lng != 57.1670 {
		t.Errorf("aktobe: %+v %v", c, ok)
	}
	if _, ok := CityByKey("atlantis"); ok {
		t.Error("atlantis is not a city")
	}
	if _, ok := CityByKey(""); ok {
		t.Error("empty key")
	}
	seen := map[string]bool{}
	for _, c := range all {
		if seen[c.Key] || len(c.Key) > 32 {
			t.Errorf("key %q", c.Key)
		}
		seen[c.Key] = true
	}
}

// Cities::options(): asort() по названию на языке запроса, побайтно. Эталон — ответ старого стенда
// (GET /api/catalog/clubs, Accept-Language: ru).
func TestCityOptionsOrder(t *testing.T) {
	var keys []string
	for _, o := range CityOptions(func(s string) string { return s }) {
		keys = append(keys, o.Key)
	}
	want := "aktau aktobe almaty astana atyrau balkhash zhanaozen zhezkazgan karaganda kokshetau konaev kostanay kyzylorda " +
		"pavlodar petropavl rudny semey taldykorgan taraz temirtau turkistan oral oskemen shymkent ekibastuz"
	if got := strings.Join(keys, " "); got != want {
		t.Errorf("order:\n got %s\nwant %s", got, want)
	}
	// Другой язык — другой порядок (по переводу), сравнение — побайтно: латиница раньше кириллицы.
	en := CityOptions(func(s string) string {
		if s == "Шымкент" {
			return "Shymkent"
		}
		return s
	})
	if en[0].Key != "shymkent" || en[0].Label != "Shymkent" {
		t.Errorf("first %+v", en[0])
	}
}

// ZoneDevices::cpu() — значения сверены с PHP 8.4 (preg_replace теми же выражениями).
func TestCPUName(t *testing.T) {
	for in, want := range map[string]string{
		"11th Gen Intel(R) Core(TM) i5-11400F @ 2.60GHz":  "Intel Core i5-11400F",
		"AMD Ryzen 5 5600X 6-Core Processor":              "AMD Ryzen 5 5600X",
		"Intel(R) Core(TM) i7-9700K CPU @ 3.60GHz":        "Intel Core i7-9700K",
		"AMD Ryzen 7 7800X3D 8-Core Processor           ": "AMD Ryzen 7 7800X3D",
		"Intel(R) Xeon(R) CPU E5-2670 v3 @ 2.30GHz":       "Intel Xeon E5-2670 v3",
		"  ":                                   "",
		"12th Gen Intel(R) Core(TM) i5-12400F": "Intel Core i5-12400F",
		"CPU":                                  "",
		"Intel(r) core(tm) CPU0 cpu @x\n":      "Intel core CPU0 cpu",
		"13th gen Intel(R) Core(TM) i9-13900KF\n": "Intel Core i9-13900KF",
		"AMD Ryzen 9 5900X 12-core processor":     "AMD Ryzen 9 5900X",
		"A\tB\vC  D":                              "A B C D",
	} {
		if got := CPUName(in); got != want {
			t.Errorf("CPUName(%q) = %q, want %q", in, got, want)
		}
	}
}

// (int) round($mb / 1024) — значения PHP 8.4.
func TestRAMGigabytes(t *testing.T) {
	for mb, want := range map[int64]int64{16253: 16, 1535: 1, 1536: 2, 512: 1, 511: 0, 0: 0, 32768: 32, 8100: 8} {
		if got := RAMGigabytes(mb); got != want {
			t.Errorf("RAMGigabytes(%d) = %d, want %d", mb, got, want)
		}
	}
}

func TestDetectedValues(t *testing.T) {
	tr := func(key string, p map[string]string) string { return strings.ReplaceAll(key, ":gb", p["gb"]) }
	d := Detected{CPU: "11th Gen Intel(R) Core(TM) i5-11400F @ 2.60GHz", HasCPU: true, RAMMegabytes: 16253, HasRAM: true}
	if got := d.Values(tr); !reflect.DeepEqual(got, map[string]string{"cpu": "Intel Core i5-11400F", "ram": "16 ГБ"}) {
		t.Errorf("%v", got)
	}
	if got := (Detected{GPU: " NVIDIA GeForce RTX 3060 ", HasGPU: true}).Values(tr); got["gpu"] != "NVIDIA GeForce RTX 3060" || len(got) != 1 {
		t.Errorf("%v", got)
	}
}

// GS-570: devices {monitor: "ASUS 240 Гц", gpu: "", mouse: "Logitech G102"} → {monitor, mouse}; пустое — NULL.
func TestCleanDevices(t *testing.T) {
	got := CleanDevices(map[string]string{"monitor": " ASUS 240 Гц ", "gpu": "", "mouse": "Logitech G102", "unknown": "x"})
	if !reflect.DeepEqual(got, map[string]string{"monitor": "ASUS 240 Гц", "mouse": "Logitech G102"}) {
		t.Errorf("%v", got)
	}
	if CleanDevices(map[string]string{"cpu": "  "}) != nil || CleanDevices(nil) != nil {
		t.Error("empty devices must be nil")
	}
	if len(DeviceFields()) != 8 || DeviceFields()[0].Key != "cpu" || DeviceFields()[7].Key != "chair" {
		t.Errorf("fields %v", DeviceFields())
	}
}

// GS-570: профиль; «@» у Instagram убирается; телефон «<script>» — ошибка.
func TestNewProfile(t *testing.T) {
	p, errs := NewProfile("Лучший клуб", "+7 700 000 00 00", "Круглосуточно", "@asia.game")
	if errs != nil || p != (Profile{Description: "Лучший клуб", Phone: "+7 700 000 00 00", Hours: "Круглосуточно", Instagram: "asia.game"}) {
		t.Fatalf("%+v %v", p, errs)
	}
	if _, errs := NewProfile("", "<script>", "", ""); len(errs) != 1 || errs[0].Field != "phone" || errs[0].Message != MsgPhoneFormat {
		t.Errorf("%v", errs)
	}
	if p, errs := NewProfile("", "", "", ""); errs != nil || !p.Empty() {
		t.Errorf("%+v %v", p, errs)
	}
	for _, c := range []struct {
		desc, phone, hours, inst string
		field, rule              string
	}{
		{strings.Repeat("я", 2001), "", "", "", "description", "max"},
		{"", strings.Repeat("1", 33), "", "", "phone", "max"},
		{"", "8 (700) 000-00-00", "", "", "", ""},
		{"", "+7 700 abc", "", "", "phone", "regex"},
		{"", "", strings.Repeat("ч", 101), "", "hours", "max"},
		{"", "", "", "@@club", "instagram", "regex"},
		{"", "", "", "club name", "instagram", "regex"},
		{"", "", "", "клуб", "instagram", "regex"},
		{"", "", "", strings.Repeat("a", 65), "instagram", "max"},
		{strings.Repeat("я", 2000), strings.Repeat("1", 32), strings.Repeat("ч", 100), strings.Repeat("a", 64), "", ""},
	} {
		_, errs := NewProfile(c.desc, c.phone, c.hours, c.inst)
		switch {
		case c.field == "" && errs != nil:
			t.Errorf("%q/%q: unexpected %v", c.phone, c.inst, errs)
		case c.field != "" && (len(errs) != 1 || errs[0].Field != c.field || errs[0].Rule != c.rule):
			t.Errorf("%s %s: %v", c.field, c.rule, errs)
		}
	}
}

func TestTrim(t *testing.T) {
	if got := Trim("\xef\xbb\xbf \xc2\xa0ул. Абая, 10\xe2\x80\x8b\n"); got != "ул. Абая, 10" {
		t.Errorf("%q", got)
	}
}
