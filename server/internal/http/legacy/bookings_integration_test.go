//go:build integration

package legacy_test

// Брони из гостевого приложения в старом REST (тема Г, docs/upstream-2026-11.md): сверка ответов с Laravel
// (эталон снят со стенда старого проекта) и маршруты, лимиты, подписка, история. Деньги, гонки и сценарии
// GS-538…GS-551 по шагам — server/internal/reservations (app_integration_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/legacy"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// withNow — фиксированное «сейчас» сервера.
func withNow(at time.Time) func(e *env) option {
	return func(e *env) option {
		e.now = func() time.Time { return at }
		return func(*legacy.Options) {}
	}
}

// balance — начальные балансы гостя (только через ledger).
func (e *env) balance(club, user int64, main, bonus money.Tiyn) {
	e.t.Helper()
	if err := db.InTenantTx(context.Background(), tdb.App, club, func(q *store.Queries) error {
		_, err := billing.SetInitialBalance(context.Background(), q, billing.InitialBalanceParams{ClubID: club, UserID: user, Main: main, Bonus: bonus})
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) tariff(club int64, name string, minutes, price int64, extra string) int64 {
	e.t.Helper()
	id := e.scalar(`INSERT INTO tariffs (club_id, name, duration_minutes, price, payment_method, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'main_or_bonus', true, now(), now()) RETURNING id`, club, name, minutes, price)
	if extra != "" {
		e.exec("UPDATE tariffs SET "+extra+" WHERE id = $1", id)
	}
	return id
}

// ruleTexts — тексты правил валидатора на стенде (русский перевод каталога) → ключ перевода (lang/en/validation.php).
var ruleTexts = map[string]string{
	"Поле «starts at» обязательно.":                                    "The starts at field is required.",
	"Поле «starts at» должно быть корректной датой.":                   "The starts at field must be a valid date.",
	"Поле «duration minutes» должно быть не меньше 30.":                "The duration minutes field must be at least 30.",
	"Поле «club computer ids» не может содержать больше 10 элементов.": "The club computer ids field must not have more than 10 items.",
}

type bookingStep struct {
	Name     string          `json:"name"`
	Method   string          `json:"method"`
	Path     string          `json:"path"`
	User     string          `json:"user"`
	Body     map[string]any  `json:"body"`
	Status   int             `json:"status"`
	Response json.RawMessage `json:"response"`
}

// TestBookingsMatchLaravel — те же данные и та же последовательность запросов, что на стенде старого проекта
// (Laravel a680bab, PHP 8.4.27; данные — testdata/bookings-seed.php, «сейчас» — понедельник 16.11.2026 10:00 Алматы):
// код HTTP и JSON каждого ответа совпадают — значения, типы, null, порядок ключей, тексты ошибок на русском с
// временем клуба. Эталон — testdata/bookings.laravel.json (как снять — docs/open-questions/upstream-bookings.md).
// К общему виду приводятся только id (зон, ПК, тарифов, броней, записей истории) и случайный код брони.
func TestBookingsMatchLaravel(t *testing.T) {
	raw, err := os.ReadFile("testdata/bookings.laravel.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Now   time.Time        `json:"now"`
		IDs   map[string]int64 `json:"ids"`
		Steps []bookingStep    `json:"steps"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, withNow(golden.Now))
	club := e.club
	e.exec(`UPDATE clubs SET timezone = 'Asia/Almaty', locale = 'ru', settings = '{"club": {"booking_interval": 10, "app_booking_enabled": true},
		"finances": {"currency": "KZT", "allow_mixed_payment": true, "bonus_max_percent": 50}}' WHERE id = $1`, club)
	ids := map[string]int64{}
	zone := func(key, name string, price any, order int) {
		ids[key] = e.scalar("INSERT INTO club_zones (club_id, name, price_per_hour, sort_order, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now()) RETURNING id",
			club, name, price, order)
	}
	zone("vip", "VIP", 60000, 1)
	zone("common", "Общий", 33333, 2)
	zone("bare", "Без цены", nil, 3)
	ids["pc1"] = e.pc(club, ids["vip"], 1, "")
	ids["pc2"] = e.pc(club, ids["vip"], 2, "")
	ids["pc3"] = e.pc(club, ids["common"], 3, "")
	ids["pc4"] = e.pc(club, ids["common"], 4, "is_reserved = true")
	ids["pc5"] = e.pc(club, ids["bare"], 5, "display_name = 'Стрим'")
	ids["three"] = e.tariff(club, "3 часа", 180, 120000, "quantity_limit_enabled = true, quantity_limit = 3")
	e.exec("INSERT INTO club_zone_tariff (club_id, club_zone_id, tariff_id) VALUES ($1, $2, $3)", club, ids["vip"], ids["three"])
	ids["night"] = e.tariff(club, "Ночь", 600, 200000,
		`description = 'Ночной пакет', validity_periods = '[{"start_day": "mon", "start_time": "22:00", "end_day": "tue", "end_time": "08:00"}]'`)
	ids["hour"] = e.tariff(club, "Час", 60, 30000, "discount_enabled = true")
	guest, guestPhone := e.user(club, "Гость Тест", "client", 0)
	e.exec("UPDATE club_memberships SET discount = 10 WHERE club_id = $1 AND user_id = $2", club, guest)
	e.balance(club, guest, 5000_00, 1000_00)
	poor, poorPhone := e.user(club, "Гость Без Денег", "client", 0)
	e.balance(club, poor, 100_00, 0)
	tokens := map[string]string{"guest": e.token(guestPhone, "mobile"), "poor": e.token(poorPhone, "mobile")}

	// id эталона → id этой базы (и обратно): зоны, ПК, тарифы — по ключам сидов; брони — по мере создания.
	toLocal := map[string]map[int64]int64{"zone": {}, "pc": {}, "tariff": {}, "booking": {}}
	toGolden := map[string]map[int64]int64{"zone": {}, "pc": {}, "tariff": {}, "booking": {}}
	link := func(kind string, goldenID, localID int64) {
		toLocal[kind][goldenID], toGolden[kind][localID] = localID, goldenID
	}
	for key, kind := range map[string]string{"vip": "zone", "common": "zone", "bare": "zone", "pc1": "pc", "pc2": "pc", "pc3": "pc",
		"pc4": "pc", "pc5": "pc", "three": "tariff", "night": "tariff", "hour": "tariff"} {
		link(kind, golden.IDs[key], ids[key])
	}
	local := func(kind string, goldenID int64) int64 {
		if id, ok := toLocal[kind][goldenID]; ok {
			return id
		}
		return 9_000_000_000 + goldenID // такого нет ни в эталоне, ни здесь
	}
	num := func(v any) int64 { f, _ := v.(float64); return int64(f) }
	queryID := regexp.MustCompile(`(computer_id|computer_ids\[\]|tariff_id)=(\d+)`)
	cancelID := regexp.MustCompile(`/bookings/(\d+)/cancel`)
	codes := map[string]bool{} // коды броней обеих сторон → «<code>»

	// canon приводит ответ к виду эталона: id → id эталона, код брони → <code>, id записи истории → 0.
	var canon func(v any, key string, parent string) any
	canon = func(v any, key, parent string) any {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				x[k] = canon(val, k, key)
			}
			return x
		case []any:
			for i, val := range x {
				x[i] = canon(val, key, parent)
			}
			return x
		case float64:
			kind := ""
			switch {
			case key == "zone_ids", key == "id" && parent == "zones":
				kind = "zone"
			case key == "id" && parent == "computers":
				kind = "pc"
			case key == "id" && parent == "tariffs":
				kind = "tariff"
			case key == "id" && (parent == "booking" || parent == "bookings"):
				kind = "booking"
			case key == "id" && parent == "data":
				return float64(0)
			}
			if id, ok := toGolden[kind][int64(x)]; ok {
				return float64(id)
			}
			return x
		case string:
			for code := range codes {
				x = strings.ReplaceAll(x, code, "<code>")
			}
			// Тексты правил валидатора: на стенде — русский каталог переводов (validation.*), в тестовом сервере
			// каталога нет (ключ — английский текст Laravel; перевод по ключу проверяют тесты i18n).
			for ru, en := range ruleTexts {
				x = strings.ReplaceAll(x, ru, en)
			}
			return x
		}
		return v
	}

	for _, step := range golden.Steps {
		// Настройки клуба между шагами — как на стенде.
		switch step.Name {
		case "index-no-cancel":
			e.exec(`UPDATE clubs SET settings = jsonb_set(settings, '{club,app_booking_cancel}', 'false') WHERE id = $1`, club)
		case "index-disabled":
			e.exec(`UPDATE clubs SET settings = jsonb_set(settings, '{club,app_booking_enabled}', 'false') WHERE id = $1`, club)
		}
		path := queryID.ReplaceAllStringFunc(step.Path, func(m string) string {
			sub := queryID.FindStringSubmatch(m)
			id, _ := strconv.ParseInt(sub[2], 10, 64)
			kind := "pc"
			if sub[1] == "tariff_id" {
				kind = "tariff"
			}
			return sub[1] + "=" + strconv.FormatInt(local(kind, id), 10)
		})
		path = cancelID.ReplaceAllStringFunc(path, func(m string) string {
			id, _ := strconv.ParseInt(cancelID.FindStringSubmatch(m)[1], 10, 64)
			return fmt.Sprintf("/bookings/%d/cancel", local("booking", id))
		})
		var body any
		if step.Body != nil {
			b := map[string]any{}
			for k, v := range step.Body {
				switch k {
				case "club_computer_id":
					v = local("pc", num(v))
				case "tariff_id":
					v = local("tariff", num(v))
				case "club_computer_ids":
					var list []int64
					for _, id := range v.([]any) {
						list = append(list, local("pc", num(id)))
					}
					v = list
				}
				b[k] = v
			}
			body = b
		}
		r := e.do(step.Method, path, tokens[step.User], body)
		if r.status != step.Status {
			t.Fatalf("%s: код %d, в Laravel %d\n ответ %s\n эталон %s", step.Name, r.status, step.Status, r.raw, step.Response)
		}
		var want, got any
		if err := json.Unmarshal(step.Response, &want); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(r.raw), &got); err != nil {
			t.Fatalf("%s: %v: %s", step.Name, err, r.raw)
		}
		// Новая бронь: её id и код на обеих сторонах.
		if wb, ok := want.(map[string]any)["booking"].(map[string]any); ok {
			gb, _ := got.(map[string]any)["booking"].(map[string]any)
			if gb == nil {
				t.Fatalf("%s: в ответе нет booking: %s", step.Name, r.raw)
			}
			link("booking", num(wb["id"]), num(gb["id"]))
			for _, b := range []map[string]any{wb, gb} {
				if code, _ := b["code"].(string); len(code) == 6 {
					codes[code] = true
				} else {
					t.Fatalf("%s: код брони %v — не 6 цифр", step.Name, b["code"])
				}
			}
		}
		// 404 модели: «… [App\Models\ComputerReservation] <id>».
		if step.Status == 404 {
			if msg, ok := got.(map[string]any)["message"].(string); ok {
				if i := strings.LastIndex(msg, " "); i >= 0 {
					if id, err := strconv.ParseInt(msg[i+1:], 10, 64); err == nil {
						got.(map[string]any)["message"] = msg[:i+1] + strconv.FormatInt(toGolden["booking"][id], 10)
					}
				}
			}
		}
		want, got = canon(want, "", ""), canon(got, "", "")
		if !reflect.DeepEqual(got, want) {
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(want)
			t.Errorf("%s: ответ отличается от Laravel\n получено %s\n эталон   %s", step.Name, gj, wj)
		}
		if gk, wk := strings.Join(keyOrder(t, []byte(r.raw)), ","), strings.Join(keyOrder(t, step.Response), ","); gk != wk {
			t.Errorf("%s: порядок ключей отличается\n получено %s\n эталон   %s", step.Name, gk, wk)
		}
	}

	// Деньги после всей последовательности: оплачена одна бронь (900 ₸), вторая отменена с возвратом.
	main, bonus := tdb.Balances(t, club, guest)
	if main != 4100_00 || bonus != 1000_00 {
		t.Errorf("балансы гостя = %d / %d, ожидалось 410000 / 100000", main, bonus)
	}
	if n := e.scalar(`SELECT count(*) FROM ledger_transactions WHERE club_id = $1 AND kind IN ('reservation_payment', 'reservation_refund')`, club); n != 4 {
		t.Errorf("операций ledger по броням = %d, ожидалось 4 (2 оплаты + 2 возврата по ПК)", n)
	}
	if err := db.InTenantTx(context.Background(), tdb.App, club, func(q *store.Queries) error {
		rows, err := q.ListMemberBalanceDrift(context.Background(), club)
		if err == nil && len(rows) > 0 {
			err = fmt.Errorf("расхождение балансов с журналом: %+v", rows)
		}
		return err
	}); err != nil {
		t.Error(err)
	}
}

// TestBookingsRoutes — маршруты броней: подписка клуба (список и отмена от неё не зависят), лимиты как в старом,
// вход без токена, повтор запроса с Idempotency-Key, уведомление агента о балансе после коммита.
func TestBookingsRoutes(t *testing.T) {
	now := time.Date(2026, 11, 16, 5, 0, 0, 0, time.UTC)
	setup := func(t *testing.T, active bool) (e *env, club, pc, guest int64, token string) {
		e = newEnv(t, withNow(now))
		club = e.club
		if !active {
			club = e.newClub(false)
		}
		e.exec(`UPDATE clubs SET timezone = 'UTC', locale = 'ru', settings = '{"club": {"booking_interval": 10, "app_booking_enabled": true}}' WHERE id = $1`, club)
		zone := e.scalar("INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, 'VIP', 60000, now(), now()) RETURNING id", club)
		pc = e.pc(club, zone, 1, "")
		var phone string
		guest, phone = e.user(club, "Гость", "client", 0)
		e.balance(club, guest, 5000_00, 0)
		return e, club, pc, guest, e.token(phone, "mobile")
	}
	booking := func(pc int64, hour int) map[string]any {
		return map[string]any{"club_computer_id": pc, "starts_at": fmt.Sprintf("2026-11-16T%02d:00:00Z", hour), "duration_minutes": 60}
	}

	t.Run("без токена — 401", func(t *testing.T) {
		e, _, pc, _, _ := setup(t, true)
		for _, r := range []resp{
			e.do("GET", "/api/guest/bookings", "", nil), e.do("GET", "/api/guest/bookings/computers", "", nil),
			e.do("POST", "/api/guest/bookings", "", booking(pc, 12)), e.do("POST", "/api/guest/bookings/1/cancel", "", nil),
		} {
			if r.status != 401 {
				t.Fatalf("код %d, ожидался 401: %s", r.status, r.raw)
			}
		}
	})

	t.Run("клуб без подписки: бронь — 402, список и отмена с возвратом работают (UQ-12)", func(t *testing.T) {
		e, club, pc, guest, token := setup(t, true)
		r := e.do("POST", "/api/guest/bookings", token, booking(pc, 12))
		if r.status != 201 {
			t.Fatalf("бронь: %d %s", r.status, r.raw)
		}
		id := int64(path(r.body, "booking.id").(float64))
		e.exec("UPDATE club_subscriptions SET status = 'expired' WHERE club_id = $1", club)
		for _, p := range []string{"/api/guest/bookings/computers", "/api/guest/bookings/tariffs?starts_at=2026-11-16T12:00:00Z",
			"/api/guest/bookings/options?starts_at=2026-11-16T12:00:00Z&duration_minutes=60"} {
			if r := e.do("GET", p, token, nil); r.status != 402 {
				t.Fatalf("%s без подписки: %d %s", p, r.status, r.raw)
			}
		}
		if r := e.do("POST", "/api/guest/bookings", token, booking(pc, 15)); r.status != 402 {
			t.Fatalf("бронь без подписки: %d %s", r.status, r.raw)
		}
		if r := e.do("GET", "/api/guest/bookings", token, nil); r.status != 200 || len(r.body["bookings"].([]any)) != 1 {
			t.Fatalf("список без подписки: %d %s", r.status, r.raw)
		}
		if r := e.do("POST", fmt.Sprintf("/api/guest/bookings/%d/cancel", id), token, nil); r.status != 200 {
			t.Fatalf("отмена без подписки: %d %s", r.status, r.raw)
		}
		if main, _ := tdb.Balances(t, club, guest); main != 5000_00 {
			t.Fatalf("баланс после отмены = %d", main)
		}
	})

	t.Run("лимиты как в старом: бронь и отмена — 10 в минуту, списки — 60", func(t *testing.T) {
		e, _, pc, _, token := setup(t, true)
		for i := range 10 {
			if r := e.do("POST", "/api/guest/bookings", token, map[string]any{"club_computer_id": pc}); r.status != 422 {
				t.Fatalf("запрос %d: %d %s", i, r.status, r.raw)
			}
		}
		if r := e.do("POST", "/api/guest/bookings", token, booking(pc, 12)); r.status != 429 {
			t.Fatalf("11-я бронь за минуту: %d %s", r.status, r.raw)
		}
		for i := range 10 {
			if r := e.do("POST", "/api/guest/bookings/999999999/cancel", token, nil); r.status != 404 {
				t.Fatalf("отмена %d: %d %s", i, r.status, r.raw)
			}
		}
		if r := e.do("POST", "/api/guest/bookings/999999999/cancel", token, nil); r.status != 429 {
			t.Fatalf("11-я отмена за минуту: %d %s", r.status, r.raw)
		}
		for i := range 60 {
			if r := e.do("GET", "/api/guest/bookings", token, nil); r.status != 200 {
				t.Fatalf("список %d: %d %s", i, r.status, r.raw)
			}
		}
		if r := e.do("GET", "/api/guest/bookings", token, nil); r.status != 429 {
			t.Fatalf("61-й список за минуту: %d %s", r.status, r.raw)
		}
		// Маршрут отмены — только числовой id (whereNumber): не число — маршрута нет.
		if r := e.do("POST", "/api/guest/bookings/abc/cancel", token, nil); r.status != 404 {
			t.Fatalf("нечисловой id: %d %s", r.status, r.raw)
		}
	})

	t.Run("повтор с тем же Idempotency-Key не списывает дважды; агент узнаёт баланс после коммита", func(t *testing.T) {
		e, club, pc, guest, token := setup(t, true)
		first := e.do("POST", "/api/guest/bookings", token, booking(pc, 12), "Idempotency-Key", "tap-1")
		second := e.do("POST", "/api/guest/bookings", token, booking(pc, 12), "Idempotency-Key", "tap-1")
		if first.status != 201 || second.status != 201 || first.raw != second.raw {
			t.Fatalf("повтор: %d %s / %d %s", first.status, first.raw, second.status, second.raw)
		}
		if main, _ := tdb.Balances(t, club, guest); main != 4400_00 {
			t.Fatalf("баланс = %d, ожидалось одно списание 600 ₸", main)
		}
		if n := e.scalar("SELECT count(*) FROM computer_reservations WHERE club_id = $1", club); n != 1 {
			t.Fatalf("броней = %d", n)
		}
		if b := e.notifier.balance(guest); b == nil || b.MainTiyn != 4400_00 || b.BonusTiyn != 0 || b.Currency != "KZT" {
			t.Fatalf("BalanceChanged агенту = %v", b)
		}
		// Тот же ключ с другим телом — 409, а не вторая бронь.
		if r := e.do("POST", "/api/guest/bookings", token, booking(pc, 15), "Idempotency-Key", "tap-1"); r.status != 409 {
			t.Fatalf("тот же ключ, другое тело: %d %s", r.status, r.raw)
		}
		// Бронь отменена — повтор с тем же ключом не отвечает «ПК забронирован» (ревью 22.11.2026).
		id := e.scalar("SELECT id FROM computer_reservations WHERE club_id = $1", club)
		if r := e.do("POST", fmt.Sprintf("/api/guest/bookings/%d/cancel", id), token, nil); r.status != 200 {
			t.Fatalf("отмена: %d %s", r.status, r.raw)
		}
		third := e.do("POST", "/api/guest/bookings", token, booking(pc, 12), "Idempotency-Key", "tap-1")
		if third.status != 200 || !strings.Contains(third.raw, "Бронь отменена.") || strings.Contains(third.raw, "ПК забронирован") ||
			!strings.Contains(third.raw, `"cancelled"`) { //nolint:misspell // состояние брони старого кода
			t.Fatalf("повтор после отмены: %d %s", third.status, third.raw)
		}
		if main, _ := tdb.Balances(t, club, guest); main != 5000_00 {
			t.Fatalf("баланс после отмены и повтора = %d", main)
		}
	})

	t.Run("GS-541 история гостя: возврат и оплата брони", func(t *testing.T) {
		e, _, pc, _, token := setup(t, true)
		r := e.do("POST", "/api/guest/bookings", token, booking(pc, 12))
		if r.status != 201 {
			t.Fatalf("бронь: %d %s", r.status, r.raw)
		}
		id := int64(path(r.body, "booking.id").(float64))
		if r := e.do("POST", fmt.Sprintf("/api/guest/bookings/%d/cancel", id), token, nil); r.status != 200 {
			t.Fatalf("отмена: %d %s", r.status, r.raw)
		}
		h := e.do("GET", "/api/guest/history", token, nil)
		if h.status != 200 || path(h.body, "data.0.kind") != "refund" || path(h.body, "data.1.kind") != "purchase" ||
			path(h.body, "data.0.amount") != "600.00" || path(h.body, "data.1.amount") != "-600.00" ||
			path(h.body, "data.1.title") != "Бронь ПК PC-01" || path(h.body, "data.1.note") != "16.11.2026 12:00" {
			t.Fatalf("история: %d %s", h.status, h.raw)
		}
	})
}
