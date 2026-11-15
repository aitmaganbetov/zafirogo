//go:build integration

package legacy_test

// Каталог клубов гостевого приложения и вступление в клуб из каталога (тема В, docs/upstream-2026-11.md):
// HTTP → Postgres (пул платформы и RLS клуба) + Valkey (присутствие ПК, лимиты). Сценарии
// tests/Feature/ClubCatalogTest.php старого проекта — GS-567, GS-568, GS-569, GS-571; пробелы GAP-056, GAP-057.
// Формат ответов сверен с ответом стенда старого проекта (testdata/catalog-*.json) и с моделями fromJson
// приложения (mobileclient/lib/api/models.dart: CatalogCity, ClubCard, ClubPage, ClubZoneInfo).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/legacy"
)

// withPresence — присутствие агентов в Valkey (свой префикс на тест).
func withPresence(e *env) option {
	e.presence = presence.NewStore(tvk, fmt.Sprintf("t:catalog%d:", seq.Add(1)), 0)
	return func(*legacy.Options) {}
}

// online — агент ПК на связи (владелец стрима в Valkey).
func (e *env) online(club, pc int64) {
	e.t.Helper()
	if _, _, _, err := e.presence.(*presence.Store).Acquire(context.Background(), club, pc, "i1"); err != nil {
		e.t.Fatal(err)
	}
}

// cityClub — клуб без подписки с городом (city "" — без города, клуба нет в каталоге).
func (e *env) cityClub(name, city, address string) int64 {
	e.t.Helper()
	id := tdb.Club(e.t, fmt.Sprintf("cat%d", seq.Add(1)))
	e.exec("UPDATE clubs SET name = $2, city = NULLIF($3, ''), address = NULLIF($4, '') WHERE id = $1", id, name, city, address)
	return id
}

func (e *env) zone(club int64, name string, price any, order int, devices string) int64 {
	return e.scalar(`INSERT INTO club_zones (club_id, name, price_per_hour, sort_order, devices, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, '')::jsonb, now(), now()) RETURNING id`, club, name, price, order, devices)
}

func (e *env) pc(club, zone int64, number int, set string) int64 {
	id := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, $3, now(), now()) RETURNING id", club, zone, number)
	if set != "" {
		e.exec("UPDATE club_computers SET "+set+" WHERE id = $1", id)
	}
	return id
}

func cityOf(r resp, key string) map[string]any {
	list, _ := r.body["cities"].([]any)
	for _, c := range list {
		if m, _ := c.(map[string]any); m["key"] == key {
			return m
		}
	}
	return nil
}

func TestCatalogCities(t *testing.T) {
	e := newEnv(t)
	e.cityClub("Бета", "aktobe", "ул. Маресьева, 5")
	alpha := e.cityClub("Альфа", "aktobe", "")
	e.cityClub("Алматинский", "almaty", "")
	e.cityClub("Без города", "", "")

	t.Run("GS-567 anyone sees the clubs of a city, with the cities and their coordinates", func(t *testing.T) {
		r := e.do("GET", "/api/catalog/clubs?city=aktobe", "", nil, "Accept-Language", "ru")
		expect(t, r, 200, map[string]any{"city": "aktobe", "clubs.0.name": "Альфа", "clubs.0.id": alpha, "clubs.0.address": nil,
			"clubs.1.name": "Бета", "clubs.1.address": "ул. Маресьева, 5", "clubs.0.pcs": 0, "clubs.0.free_pcs": 0,
			"clubs.0.price_from": nil, "clubs.0.logo_url": nil, "clubs.0.photo_url": nil, "clubs.0.hours": nil})
		if clubs, _ := r.body["clubs"].([]any); len(clubs) != 2 || strings.Contains(r.raw, "Без города") {
			t.Fatalf("clubs: %s", r.raw)
		}
		cities, _ := r.body["cities"].([]any)
		if len(cities) != 25 {
			t.Fatalf("cities %d", len(cities))
		}
		// Города — по названию; у каждого координаты и число клубов.
		if first := cities[0].(map[string]any); first["key"] != "aktau" || first["name"] != "Актау" {
			t.Errorf("first city %v", first)
		}
		aktobe := cityOf(r, "aktobe")
		if aktobe["name"] != "Актобе" || aktobe["clubs"] != float64(2) || aktobe["lat"] != 50.2839 || aktobe["lng"] != 57.167 {
			t.Errorf("aktobe %v", aktobe)
		}
		if cityOf(r, "almaty")["clubs"] != float64(1) || cityOf(r, "astana")["clubs"] != float64(0) {
			t.Errorf("counts: %s", r.raw)
		}
		// Порядок ключей и числа — как в ответе Laravel (json_encode: 57.167, а не 57.1670).
		if !strings.HasPrefix(r.raw, `{"cities":[{"key":"aktau","name":"Актау","lat":43.65,"lng":51.16,"clubs":0},{"key":"aktobe","name":"Актобе","lat":50.2839,"lng":57.167,"clubs":2},`) {
			t.Errorf("raw: %.200s", r.raw)
		}
		if r.header.Get("X-RateLimit-Limit") != "30" || !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
			t.Errorf("headers %v", r.header)
		}

		// Без города — города есть, клубов нет.
		r = e.do("GET", "/api/catalog/clubs", "", nil)
		expect(t, r, 200, map[string]any{"city": nil})
		if clubs, ok := r.body["clubs"].([]any); !ok || len(clubs) != 0 || !strings.Contains(r.raw, `"city":null,"clubs":[]`) {
			t.Fatalf("no city: %s", r.raw)
		}
		expect(t, e.do("GET", "/api/catalog/clubs?city=", "", nil), 200, map[string]any{"city": nil})

		// Неизвестный город — 422 в формате ValidationException.
		r = e.do("GET", "/api/catalog/clubs?city=atlantis", "", nil)
		expect(t, r, 422, map[string]any{"message": "The selected city is invalid.", "errors.city.0": "The selected city is invalid."})
		expect(t, e.do("GET", "/api/catalog/clubs?city[]=aktobe", "", nil), 422, map[string]any{"errors.city.0": "The city field must be a string."})
	})

	t.Run("the answer is cached for a short time", func(t *testing.T) {
		// Первый ответ по городу уже в кэше инстанса: новый клуб появится в списке не сразу (UQ-20).
		e.cityClub("Гамма", "aktobe", "")
		r := e.do("GET", "/api/catalog/clubs?city=aktobe", "", nil, "Accept-Language", "ru")
		if clubs, _ := r.body["clubs"].([]any); len(clubs) != 2 {
			t.Fatalf("cached list expected: %s", r.raw)
		}
		// Другой инстанс (свой кэш) видит клуб сразу.
		e2 := newEnv(t)
		r = e2.do("GET", "/api/catalog/clubs?city=aktobe", "", nil, "Accept-Language", "ru")
		if clubs, _ := r.body["clubs"].([]any); len(clubs) != 3 || cityOf(r, "aktobe")["clubs"] != float64(3) {
			t.Fatalf("fresh list: %s", r.raw)
		}
	})

	t.Run("public routes are throttled like in Laravel", func(t *testing.T) {
		e3 := newEnv(t)
		for i := 0; i < 30; i++ {
			if r := e3.do("GET", "/api/catalog/clubs", "", nil); r.status != 200 {
				t.Fatalf("request %d: %d", i, r.status)
			}
		}
		r := e3.do("GET", "/api/catalog/clubs", "", nil)
		expect(t, r, 429, map[string]any{"message": "Too Many Attempts."})
		// Страница клуба — свой лимит (60 в минуту).
		if r := e3.do("GET", fmt.Sprintf("/api/catalog/clubs/%d", alpha), "", nil); r.status != 200 || r.header.Get("X-RateLimit-Limit") != "60" {
			t.Fatalf("club page: %d %v", r.status, r.header)
		}
	})
}

func TestCatalogCardsAndClubPage(t *testing.T) {
	e := newEnv(t, withPresence)
	club := e.cityClub("Zafiro Semey", "semey", "ул. Есет батыра, 138А")
	e.exec(`UPDATE clubs SET profile = '{"description": "Лучший клуб", "hours": "Круглосуточно"}' WHERE id = $1`, club)
	vip := e.zone(club, "VIP", 80000, 1, `{"monitor": "ASUS 240 Гц"}`)
	main := e.zone(club, "Main", 50000, 2, "")
	pc1 := e.pc(club, vip, 1, "gpu_model = 'NVIDIA GeForce RTX 3060'")
	pc2 := e.pc(club, vip, 2, "is_reserved = true")
	e.pc(club, main, 3, "last_seen_at = now()") // не в сети: в Valkey его нет (last_seen_at в базе не учитывается)
	e.online(club, pc1)
	e.online(club, pc2)
	photoPath := fmt.Sprintf("club-photos/%d/%s.jpg", club, strings.Repeat("b", 40))
	e.exec("INSERT INTO club_photos (club_id, path, sort_order, created_at, updated_at) VALUES ($1, $2, 1, now(), now())", club, photoPath)
	photoURL := fmt.Sprintf("https://zafiro.test/club-photos/%d/%s.jpg", club, strings.Repeat("b", 40))
	hidden := e.cityClub("Без города", "", "")

	t.Run("GS-571 the app shows the club cards of a city and the club page", func(t *testing.T) {
		r := e.do("GET", "/api/catalog/clubs?city=semey", "", nil, "Accept-Language", "ru")
		expect(t, r, 200, map[string]any{"clubs.0.id": club, "clubs.0.pcs": 3, "clubs.0.free_pcs": 1, "clubs.0.price_from": 500,
			"clubs.0.hours": "Круглосуточно", "clubs.0.photo_url": photoURL, "clubs.0.address": "ул. Есет батыра, 138А", "clubs.0.logo_url": nil})
		// price_from — (float) тенге, как json_encode: 500, а не 500.0 и не тиыны.
		if !strings.Contains(r.raw, `"pcs":3,"free_pcs":1,"price_from":500}`) {
			t.Errorf("card: %s", r.raw)
		}

		r = e.do("GET", fmt.Sprintf("/api/catalog/clubs/%d", club), "", nil, "Accept-Language", "ru")
		expect(t, r, 200, map[string]any{"id": club, "name": "Zafiro Semey", "city": "Семей", "address": "ул. Есет батыра, 138А",
			"description": "Лучший клуб", "hours": "Круглосуточно", "phone": nil, "instagram": nil, "logo_url": nil,
			"photos.0":   photoURL,
			"zones.0.id": vip, "zones.0.name": "VIP", "zones.0.pcs": 2, "zones.0.free_pcs": 1, "zones.0.price_per_hour": 800,
			"zones.0.description":   nil,
			"zones.0.devices.0.key": "gpu", "zones.0.devices.0.label": "Видеокарта", "zones.0.devices.0.value": "NVIDIA GeForce RTX 3060",
			"zones.0.devices.1.key": "monitor", "zones.0.devices.1.label": "Монитор", "zones.0.devices.1.value": "ASUS 240 Гц",
			"zones.1.id": main, "zones.1.name": "Main", "zones.1.pcs": 1, "zones.1.free_pcs": 0, "zones.1.price_per_hour": 500})
		if d, _ := r.at("zones.0.devices").([]any); len(d) != 2 {
			t.Errorf("devices: %v", d)
		}
		if d, ok := r.at("zones.1.devices").([]any); !ok || len(d) != 0 {
			t.Errorf("empty devices must be []: %s", r.raw)
		}
		// Порядок ключей страницы клуба — как в контроллере.
		want := fmt.Sprintf(`{"id":%d,"name":"Zafiro Semey","city":"Семей","address":"ул. Есет батыра, 138А","logo_url":null,"description":"Лучший клуб","phone":null,"hours":"Круглосуточно","instagram":null,"photos":["%s"],"zones":[{"id":%d,"name":"VIP","description":null,"price_per_hour":800,"pcs":2,"free_pcs":1,"devices":[{"key":"gpu","label":"Видеокарта","value":"NVIDIA GeForce RTX 3060"},{"key":"monitor","label":"Монитор","value":"ASUS 240 Гц"}]},{"id":%d,"name":"Main","description":null,"price_per_hour":500,"pcs":1,"free_pcs":0,"devices":[]}]}`,
			club, photoURL, vip, main)
		if strings.TrimSpace(r.raw) != want {
			t.Errorf("club page:\n got %s\nwant %s", r.raw, want)
		}

		// Клуб без города и несуществующий — 404 как ModelNotFoundException.
		r = e.do("GET", fmt.Sprintf("/api/catalog/clubs/%d", hidden), "", nil)
		expect(t, r, 404, map[string]any{"message": fmt.Sprintf(`No query results for model [App\Models\Club] %d`, hidden)})
		expect(t, e.do("GET", "/api/catalog/clubs/999999999", "", nil), 404, map[string]any{"message": `No query results for model [App\Models\Club] 999999999`})
		// whereNumber('club'): не число — маршрута нет.
		expect(t, e.do("GET", "/api/catalog/clubs/abc", "", nil), 404, map[string]any{"message": "The route api/catalog/clubs/abc could not be found."})
	})

	t.Run("the club page does not leak more than Laravel gives", func(t *testing.T) {
		e.exec(`UPDATE clubs SET settings = '{"finances": {"currency": "KZT"}, "club": {"agent_shop_enabled": true}}' WHERE id = $1`, club)
		e2 := newEnv(t, withPresence)
		r := e2.do("GET", fmt.Sprintf("/api/catalog/clubs/%d", club), "", nil)
		for k := range r.body {
			switch k {
			case "id", "name", "city", "address", "logo_url", "description", "phone", "hours", "instagram", "photos", "zones":
			default:
				t.Errorf("unexpected key %q", k)
			}
		}
		if len(r.body) != 11 || strings.Contains(r.raw, "settings") || strings.Contains(r.raw, "slug") {
			t.Errorf("club page keys: %s", r.raw)
		}
	})
}

// GAP-056: «свободный ПК» каталога — агент на связи, ПК не занят вручную, нет идущей сессии и сотрудника; брони не
// учитываются (ПК с бронью на сейчас — свободен), ПК с сотрудником — нет.
func TestCatalogFreePCs(t *testing.T) {
	e := newEnv(t, withPresence)
	club := e.cityClub("Свободные ПК", "taraz", "")
	zone := e.zone(club, "Зал", 60000, 1, "")
	free := e.pc(club, zone, 1, "")
	booked := e.pc(club, zone, 2, "")
	staff := e.pc(club, zone, 3, "staff_until = now() + interval '10 minutes'")
	staffOver := e.pc(club, zone, 4, "staff_until = now() - interval '1 minute'")
	inSession := e.pc(club, zone, 5, "current_session_id = 's1', session_ends_at = now() + interval '30 minutes'")
	sessionOver := e.pc(club, zone, 6, "current_session_id = 's2', session_ends_at = now() - interval '1 minute'")
	openSession := e.pc(club, zone, 7, "current_session_id = 's3'") // сессия без срока — занят
	reserved := e.pc(club, zone, 8, "is_reserved = true")
	e.pc(club, zone, 9, "status = 'online', last_seen_at = now()") // по базе «в сети», в Valkey нет — не свободен
	for _, pc := range []int64{free, booked, staff, staffOver, inSession, sessionOver, openSession, reserved} {
		e.online(club, pc)
	}
	uid, _ := e.user(club, "Гость", "client", 0)
	e.exec(`INSERT INTO computer_reservations (club_id, club_computer_id, user_id, starts_at, ends_at, created_at, updated_at)
		VALUES ($1, $2, $3, now() - interval '5 minutes', now() + interval '1 hour', now(), now())`, club, booked, uid)

	r := e.do("GET", "/api/catalog/clubs?city=taraz", "", nil)
	// Свободны: free, booked (бронь не учитывается), staffOver, sessionOver.
	expect(t, r, 200, map[string]any{"clubs.0.id": club, "clubs.0.pcs": 9, "clubs.0.free_pcs": 4})
	expect(t, e.do("GET", fmt.Sprintf("/api/catalog/clubs/%d", club), "", nil), 200, map[string]any{"zones.0.pcs": 9, "zones.0.free_pcs": 4})

	// Без присутствия (Valkey не подключён) свободных нет — как у ПК не в сети.
	e2 := newEnv(t)
	expect(t, e2.do("GET", "/api/catalog/clubs?city=taraz", "", nil), 200, map[string]any{"clubs.0.pcs": 9, "clubs.0.free_pcs": 0})
}

// Оборудование зоны: значение клуба, иначе найденное агентами (самый частый процессор и видеокарта, наибольшая
// память); цена зоны с тиынами; города на языке запроса.
func TestCatalogDetectedDevices(t *testing.T) {
	e := newEnv(t, withPresence)
	club := e.cityClub("Железо", "pavlodar", "")
	e.exec(`UPDATE clubs SET profile = '{"phone": "+7 700 000 00 00", "instagram": "asia.game"}', logo_path = $2 WHERE id = $1`,
		club, fmt.Sprintf("club-logos/%d/%s.png", club, strings.Repeat("L", 40)))
	zone := e.zone(club, "Bootcamp", 123450, 1, `{"cpu": "Свой процессор", "chair": "DXRacer"}`)
	noPrice := e.zone(club, "Без цены", nil, 2, "")
	for i, cpu := range []string{"11th Gen Intel(R) Core(TM) i5-11400F @ 2.60GHz", "11th Gen Intel(R) Core(TM) i5-11400F @ 2.60GHz", "AMD Ryzen 5 5600X 6-Core Processor"} {
		e.pc(club, noPrice, 10+i, fmt.Sprintf("cpu_model = '%s', gpu_model = ' NVIDIA GeForce RTX 4060 ', ram_total_mb = %d", cpu, 16253+int64(i)*16000))
	}
	e.pc(club, zone, 1, "cpu_model = 'Intel(R) Core(TM) i7-9700K CPU @ 3.60GHz', ram_total_mb = 32621")

	r := e.do("GET", fmt.Sprintf("/api/catalog/clubs/%d", club), "", nil, "Accept-Language", "ru")
	expect(t, r, 200, map[string]any{"phone": "+7 700 000 00 00", "instagram": "asia.game", "description": nil,
		"logo_url": fmt.Sprintf("https://zafiro.test/club-logos/%d/%s.png", club, strings.Repeat("L", 40)),
		// Значение клуба важнее найденного; память — из данных агентов; порядок — как у полей формы.
		"zones.0.price_per_hour": 1234.5,
		"zones.0.devices.0.key":  "cpu", "zones.0.devices.0.value": "Свой процессор",
		"zones.0.devices.1.key": "ram", "zones.0.devices.1.label": "Оперативная память", "zones.0.devices.1.value": "32 ГБ",
		"zones.0.devices.2.key": "chair", "zones.0.devices.2.value": "DXRacer",
		// Зона без цены: (float) null = 0. Самый частый процессор, MAX памяти: 48253 МБ → 47 ГБ.
		"zones.1.price_per_hour":  0,
		"zones.1.devices.0.value": "Intel Core i5-11400F", "zones.1.devices.1.value": "NVIDIA GeForce RTX 4060",
		"zones.1.devices.2.value": "47 ГБ"})
	if !strings.Contains(r.raw, `"price_per_hour":1234.5,`) || !strings.Contains(r.raw, `"price_per_hour":0,`) {
		t.Errorf("prices: %s", r.raw)
	}
	// Карточка: цена «от» — наименьшая среди зон с ценой.
	expect(t, e.do("GET", "/api/catalog/clubs?city=pavlodar", "", nil), 200, map[string]any{"clubs.0.price_from": 1234.5, "clubs.0.pcs": 4})
}

// Поклубный переход (UQ-2): каталог объединяет клубы этого сервера и старого; страница клуба, которого здесь нет, —
// со старого сервера; вход в клуб каталога, которого здесь нет, — туда же.
func TestCatalogUpstream(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []*http.Request
		fail     bool
		// dupID — клуб, который есть и здесь, и в ответе старого сервера.
		dupID atomic.Int64
	)
	withCatalogUpstream := func(e *env) option {
		return func(o *legacy.Options) {
			e.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.Clone(context.Background()))
				bad := fail
				mu.Unlock()
				e.upHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case bad:
					w.WriteHeader(http.StatusBadGateway)
				case r.URL.Path == "/api/catalog/clubs":
					// Ответ Laravel: два клуба города (один уже переехал сюда и на старом сервере не очищен).
					_, _ = fmt.Fprintf(w, `{"cities":[{"key":"oral","name":"Уральск","lat":51.2333,"lng":51.3667,"clubs":2},{"key":"balkhash","name":"Балхаш","lat":46.8481,"lng":74.995,"clubs":7}],"city":%q,"clubs":[`+
						`{"id":%s,"name":"Дубль","address":null,"logo_url":null,"photo_url":null,"hours":null,"pcs":1,"free_pcs":0,"price_from":null},`+
						`{"id":900001,"name":"Бобёр","address":"ул. Старая, 1","logo_url":"https:\/\/old.test\/club-logos\/900001\/x.png","photo_url":null,"hours":"24\/7","pcs":20,"free_pcs":5,"price_from":700.5}]}`,
						r.URL.Query().Get("city"), fmt.Sprint(dupID.Load()))
				default:
					_, _ = w.Write([]byte(`{"upstream":true,"path":"` + r.URL.Path + `"}`))
				}
			}))
			e.t.Cleanup(e.upstream.Close)
			o.Upstream, _ = url.Parse(e.upstream.URL)
			o.UpstreamHost = "zafiro.kz"
		}
	}
	e := newEnv(t, withCatalogUpstream)
	e.cityClub("Арена", "oral", "")
	dup := e.cityClub("Дубль", "oral", "ул. Новая, 2")
	e.cityClub("Вега", "oral", "")
	dupID.Store(dup)

	t.Run("the list merges the clubs of both servers", func(t *testing.T) {
		r := e.do("GET", "/api/catalog/clubs?city=oral", "", nil, "Accept-Language", "kk", "X-Forwarded-For", "203.0.113.7")
		// По названию: Арена (здесь), Бобёр (старый), Вега (здесь), Дубль (здесь — наш, не старый).
		expect(t, r, 200, map[string]any{"city": "oral", "clubs.0.name": "Арена", "clubs.1.name": "Бобёр", "clubs.1.id": 900001,
			"clubs.1.price_from": 700.5, "clubs.1.hours": "24/7", "clubs.1.logo_url": "https://old.test/club-logos/900001/x.png",
			"clubs.2.name": "Вега", "clubs.3.name": "Дубль", "clubs.3.address": "ул. Новая, 2"})
		if clubs, _ := r.body["clubs"].([]any); len(clubs) != 4 {
			t.Fatalf("merged clubs: %s", r.raw)
		}
		// Число клубов города: 3 здесь + 2 там − 1 дубль; других городов — сумма.
		if cityOf(r, "oral")["clubs"] != float64(4) || cityOf(r, "balkhash")["clubs"] != float64(7) {
			t.Errorf("counts: oral %v balkhash %v", cityOf(r, "oral")["clubs"], cityOf(r, "balkhash")["clubs"])
		}
		mu.Lock()
		last := requests[len(requests)-1]
		mu.Unlock()
		if last.URL.Query().Get("city") != "oral" || last.Header.Get("Accept-Language") != "kk" || last.Host != "zafiro.kz" ||
			!strings.HasPrefix(last.Header.Get("X-Forwarded-For"), "203.0.113.7") {
			t.Errorf("upstream request: %s host %s headers %v", last.URL, last.Host, last.Header)
		}
		// Второй запрос — из кэша: к старому серверу не ходим.
		hits := e.upHits.Load()
		e.do("GET", "/api/catalog/clubs?city=oral", "", nil, "Accept-Language", "kk")
		if e.upHits.Load() != hits {
			t.Error("the merged list must be cached")
		}
	})

	t.Run("the old server is down: own clubs only, not cached", func(t *testing.T) {
		mu.Lock()
		fail = true
		mu.Unlock()
		// Другой язык — другой ключ кэша (ответ на kk уже в кэше).
		r := e.do("GET", "/api/catalog/clubs?city=oral", "", nil, "Accept-Language", "en")
		expect(t, r, 200, map[string]any{"clubs.0.name": "Арена", "clubs.1.name": "Вега", "clubs.2.name": "Дубль"})
		if clubs, _ := r.body["clubs"].([]any); len(clubs) != 3 || cityOf(r, "oral")["clubs"] != float64(3) {
			t.Errorf("own clubs only: %s", r.raw)
		}
		mu.Lock()
		fail = false
		mu.Unlock()
		r = e.do("GET", "/api/catalog/clubs?city=oral", "", nil, "Accept-Language", "en")
		if clubs, _ := r.body["clubs"].([]any); len(clubs) != 4 {
			t.Errorf("a failed merge must not be cached: %s", r.raw)
		}
	})

	t.Run("a club page and a sign-in of a club that is not here go to the old server", func(t *testing.T) {
		hits := e.upHits.Load()
		r := e.do("GET", "/api/catalog/clubs/900001", "", nil)
		expect(t, r, 200, map[string]any{"upstream": true, "path": "/api/catalog/clubs/900001"})
		// Свой клуб — здесь.
		expect(t, e.do("GET", fmt.Sprintf("/api/catalog/clubs/%d", dup), "", nil), 200, map[string]any{"name": "Дубль"})
		if e.upHits.Load() != hits+1 {
			t.Errorf("upstream hits %d, want %d", e.upHits.Load(), hits+1)
		}

		// Аккаунт этого сервера входит в клуб каталога, который ещё на старом сервере, — запрос уходит туда.
		_, phone := e.user(e.club, "Гость", "client", 0)
		r = e.do("POST", "/api/auth/login", "", map[string]any{"phone": phone, "password": "pw", "device_name": "mobileclient", "club_id": 900001})
		expect(t, r, 200, map[string]any{"upstream": true, "path": "/api/auth/login"})
		// В клуб этого сервера — здесь (и вступает в него).
		r = e.do("POST", "/api/auth/login", "", map[string]any{"phone": phone, "password": "pw", "device_name": "mobileclient", "club_id": dup})
		expect(t, r, 200, map[string]any{"club_id": dup, "user.role": "client"})
		// Незнакомый телефон — как раньше, на старый сервер.
		r = e.do("POST", "/api/auth/login", "", map[string]any{"phone": "+77770000000", "password": "pw", "device_name": "mobileclient", "club_id": dup})
		expect(t, r, 200, map[string]any{"upstream": true})
	})
}

// GS-568, GS-569, GAP-057: вход в клуб из списка города делает аккаунт клиентом клуба; гость не зависит от подписки
// клуба, сотрудник — зависит.
func TestCatalogJoin(t *testing.T) {
	e := newEnv(t)
	club := e.cityClub("Новый клуб", "kostanay", "") // с городом, подписки нет вовсе
	hidden := e.cityClub("Без города", "", "")
	uid, phone := e.user(e.club, "Арман", "client", 0)
	login := func(phone string, clubID int64) resp {
		return e.do("POST", "/api/auth/login", "", map[string]any{"phone": phone, "password": "pw", "device_name": "mobileclient", "club_id": clubID})
	}

	t.Run("GS-568 a guest signs in to a club of the list, joins it and does not depend on its subscription", func(t *testing.T) {
		// Клуб без города — в каталоге его нет: 403, как раньше.
		r := login(phone, hidden)
		expect(t, r, 403, map[string]any{"message": "У аккаунта нет доступа к выбранному клубу.", "code": "not_club_member"})
		if e.scalar("SELECT count(*) FROM club_memberships WHERE club_id = $1", hidden) != 0 {
			t.Fatal("joined a club without a city")
		}
		expect(t, login(phone, 999999999), 403, map[string]any{"code": "not_club_member"})

		r = login(phone, club)
		expect(t, r, 200, map[string]any{"club_id": club, "club_name": "Новый клуб", "user.id": uid, "user.club_id": club,
			"user.role": "client", "user.balance": 0, "user.bonus_balance": 0, "user.discount": 0})
		if !strings.Contains(r.raw, `"balance":0,`) {
			t.Errorf("balance must be json_encode((float) 0) = 0: %s", r.raw)
		}
		tok := r.body["access_token"].(string)
		if e.scalar("SELECT count(*) FROM club_memberships WHERE club_id = $1 AND user_id = $2 AND role = 'client' AND discount = 0", club, uid) != 1 ||
			e.scalar("SELECT count(*) FROM member_balances WHERE club_id = $1 AND user_id = $2 AND balance = 0 AND bonus_balance = 0", club, uid) != 1 ||
			e.scalar("SELECT count(*) FROM club_subscriptions WHERE club_id = $1", club) != 0 {
			t.Fatal("membership, balance row or subscription")
		}
		if e.scalar(`SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'user.joined_club' AND user_id = $2
			AND description = 'Клиент стал клиентом клуба в мобильном приложении: Арман'`, club, uid) != 1 {
			t.Fatal("user.joined_club log")
		}
		// Список клубов аккаунта: новый — текущий.
		found := false
		for _, c := range r.body["clubs"].([]any) {
			if m := c.(map[string]any); m["id"] == float64(club) {
				found = m["current"] == true && m["role"] == "client"
			}
		}
		if !found {
			t.Errorf("clubs: %v", r.body["clubs"])
		}

		// Гость клуба без подписки: профиль, новости, журнал, друзья — открыты; вход на ПК по QR — 402.
		for _, p := range []string{"/api/auth/me", "/api/guest/news", "/api/guest/history", "/api/guest/friends"} {
			expect(t, e.do("GET", p, tok, nil), 200, nil)
		}
		expect(t, e.do("GET", "/api/guest/qr-login/"+strings.Repeat("A", 40), tok, nil), 402, map[string]any{"message": "Подписка клуба не активна."})

		// Повторный вход членство не дублирует и в журнал вступления не пишет.
		expect(t, login(phone, club), 200, map[string]any{"club_id": club})
		if e.scalar("SELECT count(*) FROM club_memberships WHERE club_id = $1 AND user_id = $2", club, uid) != 1 ||
			e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'user.joined_club'", club) != 1 {
			t.Fatal("joined twice")
		}
	})

	t.Run("GS-569 a signed-in guest moves to a club of the list and joins it; staff still need the subscription", func(t *testing.T) {
		gid, p2 := e.user(e.club, "Данияр", "client", 0)
		tok := e.token(p2, "mobileclient")
		r := e.do("POST", "/api/auth/switch-club", tok, map[string]any{"club_id": club})
		expect(t, r, 200, map[string]any{"club_id": club, "club_name": "Новый клуб", "user.role": "client"})
		found := false
		for _, c := range r.body["clubs"].([]any) {
			if m := c.(map[string]any); m["id"] == float64(club) {
				found = m["current"] == true && m["role"] == "client"
			}
		}
		if !found || e.scalar("SELECT count(*) FROM club_memberships WHERE club_id = $1 AND user_id = $2 AND role = 'client'", club, gid) != 1 ||
			e.scalar("SELECT count(*) FROM member_balances WHERE club_id = $1 AND user_id = $2", club, gid) != 1 ||
			e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'user.joined_club' AND user_id = $2", club, gid) != 1 {
			t.Fatalf("switch join: %s", r.raw)
		}
		// Прежний токен отозван, новый — токен нового клуба.
		expect(t, e.do("GET", "/api/auth/me", tok, nil), 401, nil)
		expect(t, e.do("GET", "/api/auth/me", r.body["access_token"].(string), nil), 200, map[string]any{"user.club_id": club})
		// Клуб без города — 403.
		tok2 := e.token(p2, "mobileclient")
		expect(t, e.do("POST", "/api/auth/switch-club", tok2, map[string]any{"club_id": hidden}), 403,
			map[string]any{"message": "У аккаунта нет доступа к выбранному клубу."})

		// Сотрудник клуба без подписки войти в него не может.
		_, staffPhone := e.user(club, "Админ", "admin", 0)
		expect(t, e.do("POST", "/api/auth/login", "", map[string]any{"phone": staffPhone, "password": "pw", "device_name": "Zafiro Staff", "club_id": club}),
			402, map[string]any{"message": "Подписка клуба не активна."})
	})

	t.Run("GAP-057 a blocked account does not join", func(t *testing.T) {
		bid, p3 := e.user(e.club, "Блок", "client", 0)
		e.exec("UPDATE users SET blocked_at = now() WHERE id = $1", bid)
		expect(t, login(p3, club), 403, map[string]any{"code": "account_blocked"})
		if e.scalar("SELECT count(*) FROM club_memberships WHERE club_id = $1 AND user_id = $2", club, bid) != 0 {
			t.Fatal("blocked account joined")
		}
		// Неверный пароль — тоже не вступает.
		_, p4 := e.user(e.club, "Чужой", "client", 0)
		r := e.do("POST", "/api/auth/login", "", map[string]any{"phone": p4, "password": "bad", "device_name": "mobileclient", "club_id": club})
		expect(t, r, 401, nil)
		if e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'user.joined_club'", club) != 2 {
			t.Fatal("joined with a wrong password")
		}
	})

	t.Run("GAP-057 parallel sign-ins join once", func(t *testing.T) {
		pid, p5 := e.user(e.club, "Параллельный", "client", 0)
		target := e.cityClub("Параллельный клуб", "kostanay", "")
		var wg sync.WaitGroup
		statuses := make([]int, 4) // лимит customer-login — 5 в минуту на телефон
		for i := range statuses {
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses[i] = login(p5, target).status
			}()
		}
		wg.Wait()
		for i, s := range statuses {
			if s != 200 {
				t.Errorf("login %d: %d", i, s)
			}
		}
		if e.scalar("SELECT count(*) FROM club_memberships WHERE club_id = $1 AND user_id = $2", target, pid) != 1 ||
			e.scalar("SELECT count(*) FROM member_balances WHERE club_id = $1 AND user_id = $2", target, pid) != 1 ||
			e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'user.joined_club'", target) != 1 {
			t.Fatal("parallel join: membership, balance or log duplicated")
		}
	})
}

// keyOrder — ключи объектов JSON в порядке появления (порядок ключей ответа должен совпадать с Laravel).
func keyOrder(t *testing.T, raw []byte) []string {
	t.Helper()
	var keys []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Стек: true — внутри объекта и ждём ключ.
	type frame struct{ object, key bool }
	var stack []frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return keys
		}
		if err != nil {
			t.Fatalf("json: %v", err)
		}
		top := len(stack) - 1
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, frame{object: true, key: true})
				continue
			case '[':
				stack = append(stack, frame{})
				continue
			default:
				stack = stack[:top]
				top--
			}
		case string:
			if top >= 0 && stack[top].object && stack[top].key {
				keys = append(keys, v)
				stack[top].key = false
				continue
			}
		}
		if top >= 0 && stack[top].object {
			stack[top].key = true
		}
	}
}

// Сверка с ответом стенда старого проекта (Laravel a680bab, PHP 8.4.27): те же данные — тот же JSON (значения,
// типы чисел, null и порядок ключей). Эталоны — testdata/catalog-index.laravel.json и catalog-club.laravel.json:
// ответы `GET /api/catalog/clubs?city=zhezkazgan` и `GET /api/catalog/clubs/3` стенда tools/visual старого проекта
// с данными testdata/catalog-seed.php (Accept-Language: ru). Отличия, которые тест приводит к общему виду: id
// (в эталоне — 3, 4, 6, 7, 8), адрес сайта (http://localhost:8011) и число клубов других городов (база тестов общая).
func TestCatalogMatchesLaravel(t *testing.T) {
	e := newEnv(t, withPresence)
	club := e.cityClub("Zafiro Arena Test", "zhezkazgan", "ул. Есет батыра, 138А")
	e.exec(`UPDATE clubs SET profile = '{"description": "Лучший клуб города", "phone": "+7 700 000 00 00", "hours": "Круглосуточно", "instagram": "asia.game"}',
		logo_path = $2 WHERE id = $1`, club, fmt.Sprintf("club-logos/%d/%s.png", club, strings.Repeat("L", 40)))
	vip := e.zone(club, "VIP", 80000, 1, `{"monitor": "ASUS 240 Гц", "mouse": "Logitech G102"}`)
	e.exec("UPDATE club_zones SET description = 'Тихая комната' WHERE id = $1", vip)
	main := e.zone(club, "Main", 50050, 2, "")
	bare := e.zone(club, "Без цены", nil, 3, "")
	pc1 := e.pc(club, vip, 1, "cpu_model = '11th Gen Intel(R) Core(TM) i5-11400F @ 2.60GHz', gpu_model = 'NVIDIA GeForce RTX 3060', ram_total_mb = 16253")
	pc2 := e.pc(club, vip, 2, "is_reserved = true")
	e.pc(club, main, 3, "")
	e.online(club, pc1) // в старом — last_seen_at = сейчас
	e.online(club, pc2)
	for i, name := range []string{strings.Repeat("b", 40) + ".jpg", strings.Repeat("a", 40) + ".png"} {
		e.exec("INSERT INTO club_photos (club_id, path, sort_order, created_at, updated_at) VALUES ($1, $2, $3, now(), now())",
			club, fmt.Sprintf("club-photos/%d/%s", club, name), i+1)
	}
	second := e.cityClub("Второй клуб", "zhezkazgan", "")

	// Ответ нового сервера → вид эталона: id и адрес сайта стенда.
	normalize := func(raw string) []byte {
		raw = strings.ReplaceAll(raw, "https://zafiro.test", "http://localhost:8011")
		for id, golden := range map[int64]int{club: 3, second: 4, vip: 6, main: 7, bare: 8} {
			raw = strings.ReplaceAll(raw, fmt.Sprintf(`"id":%d,`, id), fmt.Sprintf(`"id":%d,`, golden))
			raw = strings.ReplaceAll(raw, fmt.Sprintf(`/%d/`, id), fmt.Sprintf(`/%d/`, golden))
		}
		return []byte(raw)
	}
	compare := func(name string, got []byte, adjust func(v any)) {
		t.Helper()
		want, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var g, w any
		if err := json.Unmarshal(got, &g); err != nil {
			t.Fatalf("%s: %v: %s", name, err, got)
		}
		if err := json.Unmarshal(want, &w); err != nil {
			t.Fatal(err)
		}
		if adjust != nil {
			adjust(g)
			adjust(w)
		}
		if !reflect.DeepEqual(g, w) {
			gj, _ := json.Marshal(g)
			wj, _ := json.Marshal(w)
			t.Errorf("%s: values differ\n got %s\nwant %s", name, gj, wj)
		}
		if gk, wk := strings.Join(keyOrder(t, got), ","), strings.Join(keyOrder(t, want), ","); gk != wk {
			t.Errorf("%s: key order differs\n got %s\nwant %s", name, gk, wk)
		}
	}

	r := e.do("GET", "/api/catalog/clubs?city=zhezkazgan", "", nil, "Accept-Language", "ru")
	if r.status != 200 {
		t.Fatalf("index: %d %s", r.status, r.raw)
	}
	compare("catalog-index.laravel.json", normalize(r.raw), func(v any) {
		for _, c := range v.(map[string]any)["cities"].([]any) {
			if m := c.(map[string]any); m["key"] != "zhezkazgan" {
				m["clubs"] = float64(0)
			}
		}
	})
	// Числа — как json_encode PHP: 500.5 и 47.7833 без лишних нулей, целые float — без «.0».
	for _, part := range []string{`"price_from":500.5}`, `"price_from":null}`, `"lat":47.7833,"lng":67.7667,"clubs":2}`, `"lat":43.65,"lng":51.16,`} {
		if !strings.Contains(r.raw, part) {
			t.Errorf("index: no %s in %s", part, r.raw)
		}
	}

	r = e.do("GET", fmt.Sprintf("/api/catalog/clubs/%d", club), "", nil, "Accept-Language", "ru")
	if r.status != 200 {
		t.Fatalf("club: %d %s", r.status, r.raw)
	}
	compare("catalog-club.laravel.json", normalize(r.raw), nil)
	for _, part := range []string{`"price_per_hour":800,`, `"price_per_hour":500.5,`, `"price_per_hour":0,`} {
		if !strings.Contains(r.raw, part) {
			t.Errorf("club: no %s in %s", part, r.raw)
		}
	}
}
