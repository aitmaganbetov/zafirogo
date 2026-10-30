//go:build integration

package httpserver

// Этап 3, группа 4: тарифы, брони, новости, задачи, чат, журнал действий, уведомления — HTTP → Postgres (RLS).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
)

type chatDelivery struct {
	mu   sync.Mutex
	sent []string
}

// newG4Env — стенд админки с часами now и записью доставок чата на ПК.
func newG4Env(t *testing.T, now time.Time) (*adminEnv, *chatDelivery) {
	t.Helper()
	d, vk := sharedAdmin(t)
	h, _ := password.New(password.Options{})
	sm, err := session.New(session.Options{Client: vk, Keys: []session.Key{{ID: "t", Secret: bytes.Repeat([]byte{7}, 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(auth.Options{Pool: d.App, Hasher: h, Sessions: sm})
	if err != nil {
		t.Fatal(err)
	}
	lim, err := ratelimit.New(ratelimit.Options{Client: vk})
	if err != nil {
		t.Fatal(err)
	}
	n := &fakeNotifier{}
	cashSvc := &cashsvc.Service{Notifier: n}
	bill := billingsvc.New(d.App, nil, cashSvc)
	pr := &fakePresence{states: map[int64][]presence.State{}}
	cd := &chatDelivery{}
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim,
		Pool: d.App, PlatformPool: d.Platform, Billing: bill, Cash: cashSvc, Presence: pr, Now: func() time.Time { return now },
		ChatToGuest: func(_ context.Context, _, userID, messageID int64, author, body string, _ time.Time) {
			cd.mu.Lock()
			defer cd.mu.Unlock()
			cd.sent = append(cd.sent, fmt.Sprintf("%d:%d:%s:%s", userID, messageID, author, body))
		},
	}))
	t.Cleanup(srv.Close)
	e := &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("g4%d", adminSeq.Add(1))), presence: pr, notifier: n}
	e.exec("UPDATE clubs SET timezone = 'Asia/Almaty' WHERE id = $1", e.club)
	return e, cd
}

func (e *adminEnv) send4(method, tok, path string, body any) resp {
	e.t.Helper()
	return do(e.t, method, e.srv.URL+path, body, bearer(tok))
}

func (e *adminEnv) zoneWithPC(name string, pricePerHour any, number int) (int64, int64) {
	e.t.Helper()
	z := e.scalar("INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at) VALUES ($1, $2, $3, now(), now()) RETURNING id", e.club, name, pricePerHour)
	pc := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, $3, now(), now()) RETURNING id", e.club, z, number)
	return z, pc
}

func tariffBody(name string, price int64, zones []int64) map[string]any {
	return map[string]any{
		"name": name, "duration_value": 2, "duration_unit": "hours", "price_tiyn": price, "payment_method": "main_or_bonus",
		"discount_enabled": false, "zone_ids": zones, "usage_limit_enabled": false, "quantity_limit_enabled": true, "quantity_limit": 5,
		"validity_periods": []map[string]string{{"start_day": "mon", "start_time": "00:00", "end_day": "sun", "end_time": "23:59"}},
		"sale_periods":     []map[string]string{{"start_day": "sat", "start_time": "10:00", "end_day": "sat", "end_time": "12:00"}},
		"is_active":        true,
	}
}

func TestG4Tariffs(t *testing.T) {
	// Чт 15.10.2026 12:00 Алматы — вне времени продажи (сб 10–12).
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e, _ := newG4Env(t, now)
	_, owner := e.staff("Владелец", "owner")
	_, manager := e.staff("Менеджер", "manager")
	_, cashier := e.staff("Кассир", "cashier")
	hall, _ := e.zoneWithPC("Зал", 60000, 1)
	vip, _ := e.zoneWithPC("VIP", nil, 2)
	empty := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Пустая', now(), now()) RETURNING id", e.club)

	t.Run("PQ-4/price-range", func(t *testing.T) {
		expectField(t, e.post(manager, "/api/tariffs", tariffBody("Дорогой", 1_000_000_000_000, nil)), "price", "The price field must not be greater than 9999999999.99.")
		expectField(t, e.post(manager, "/api/tariffs", tariffBody("Минус", -1, nil)), "price", "The price field must be at least 0.")
	})
	t.Run("rules", func(t *testing.T) {
		b := tariffBody("", 100, []int64{empty})
		b["validity_periods"] = []map[string]string{{"start_day": "xxx", "start_time": "25:00", "end_day": "sun", "end_time": "23:59"}}
		r := e.post(manager, "/api/tariffs", b)
		expect(t, r, http.StatusUnprocessableEntity, "rules")
		for _, f := range []string{"name", "zone_ids.0", "validity_periods.0.start_day", "validity_periods.0.start_time"} {
			if fieldError(r, f) == "" {
				t.Fatalf("no error for %s: %v", f, r.body)
			}
		}
		if r := e.post(cashier, "/api/tariffs", tariffBody("Касса", 100, nil)); r.status != http.StatusForbidden {
			t.Fatalf("cashier create: %d", r.status)
		}
	})

	r := e.post(manager, "/api/tariffs", tariffBody("2 часа", 120000, []int64{vip, hall}))
	expect(t, r, http.StatusCreated, "create")
	id := num(r.body["id"])
	if r.body["message"] != "Тариф создан." {
		t.Fatalf("message: %v", r.body)
	}
	expectField(t, e.post(manager, "/api/tariffs", tariffBody("2 ЧАСА", 1, nil)), "name", "The name has already been taken.")
	if e.activityCount("tariff.created", id) != 1 || e.scalar("SELECT count(*) FROM audit_log WHERE club_id = $1 AND action = 'tariff.created' AND subject_id = $2", e.club, id) != 1 {
		t.Fatal("journal/audit of create")
	}
	d := e.activityDetails("tariff.created")
	if d["zone_count"] != float64(2) || obj(d["_t"])["key"] != "Создан тариф: :tariff" {
		t.Fatalf("details: %v", d)
	}

	page := e.get(manager, "/api/tariffs/page")
	expect(t, page, 200, "page")
	zones, _ := page.body["zones"].([]any)
	if len(zones) != 2 || obj(zones[0])["name"] != "VIP" { // только зоны с ПК, по имени
		t.Fatalf("zones: %v", zones)
	}
	list, _ := page.body["tariffs"].([]any)
	tr := obj(list[0])
	if tr["on_sale"] != false || tr["in_validity_period"] != true || num(tr["sold"]) != 0 || page.body["can_delete"] != false {
		t.Fatalf("tariff: %v", page.body)
	}
	names, _ := tr["zone_names"].([]any)
	if len(names) != 2 || names[0] != "Зал" {
		t.Fatalf("zone names by zone id: %v", names)
	}
	if f := e.get(manager, fmt.Sprintf("/api/tariffs/page?zone=%d", empty)); f.body["zone_filter"] != nil {
		t.Fatalf("filter by a zone without PCs is ignored: %v", f.body["zone_filter"])
	}

	b := tariffBody("2 часа", 150000, []int64{hall})
	b["is_active"] = false
	r = e.send4("PATCH", manager, fmt.Sprintf("/api/tariffs/%d", id), b)
	expect(t, r, 200, "update")
	ch, _ := e.activityDetails("tariff.updated")["changed"].([]any)
	if fmt.Sprint(ch) != "[price is_active zones]" {
		t.Fatalf("changed: %v", ch)
	}
	if e.scalar("SELECT count(*) FROM club_zone_tariff WHERE club_id = $1 AND tariff_id = $2", e.club, id) != 1 {
		t.Fatal("zones synced")
	}
	if r := e.send4("DELETE", manager, fmt.Sprintf("/api/tariffs/%d", id), nil); r.status != http.StatusForbidden {
		t.Fatalf("manager delete: %d", r.status)
	}
	r = e.send4("DELETE", owner, fmt.Sprintf("/api/tariffs/%d", id), nil)
	if r.status == http.StatusForbidden {
		// tariffs.delete по умолчанию никому (как в старом): владелец выдаёт право себе явно.
		e.exec("UPDATE club_memberships SET permissions = '{\"tariffs.delete\": true}' WHERE club_id = $1 AND role = 'owner'", e.club)
		r = e.send4("DELETE", owner, fmt.Sprintf("/api/tariffs/%d", id), nil)
	}
	expect(t, r, 200, "delete")
	if r.body["message"] != "Тариф «2 часа» удалён." || e.scalar("SELECT count(*) FROM tariffs WHERE id = $1 AND deleted_at IS NOT NULL", id) != 1 {
		t.Fatalf("delete: %v", r.body)
	}
	// Имя удалённого тарифа снова свободно (Q-031).
	expect(t, e.post(manager, "/api/tariffs", tariffBody("2 часа", 1, nil)), http.StatusCreated, "name of deleted")
	if r := e.send4("PATCH", manager, fmt.Sprintf("/api/tariffs/%d", id), b); r.status != http.StatusNotFound {
		t.Fatalf("update deleted: %d", r.status)
	}
}

func TestG4Reservations(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC) // 12:00 Алматы
	e, _ := newG4Env(t, now)
	_, admin := e.staff("Админ", "admin")
	_, pc := e.zoneWithPC("Зал", nil, 3)
	client, _ := e.member("Алия", "client", "", false)

	body := map[string]any{"club_computer_id": pc, "user_id": client, "starts_at": "2026-10-15T14:00", "duration_minutes": 60, "note": "Турнир"}
	r := e.post(admin, "/api/reservations", body)
	expect(t, r, http.StatusCreated, "create")
	if r.body["date"] != "2026-10-15" || !strings.Contains(r.body["message"].(string), "Алия") {
		t.Fatalf("created: %v", r.body)
	}
	// Интервал 30 мин: 15:20 пересекается с 14:00–15:00 + 30.
	body["starts_at"] = "2026-10-15T15:20"
	expectField(t, e.post(admin, "/api/reservations", body), "starts_at", "ПК уже забронирован с 15.10 14:00 до 15:00 (между бронями нужно 30 мин).")
	body["starts_at"] = "2026-10-15T11:00"
	expectField(t, e.post(admin, "/api/reservations", body), "starts_at", "Нельзя забронировать ПК на прошедшее время.")
	body["starts_at"], body["user_id"] = "2026-10-15T18:00", nil
	expectField(t, e.post(admin, "/api/reservations", body), "user_id", "Выберите клиента из пользователей клуба.")

	l := e.get(admin, "/api/reservations?date=2026-10-15")
	items, _ := l.body["items"].([]any)
	if len(items) != 1 || obj(items[0])["state"] != "pending" || obj(items[0])["customer_label"] == "" || l.body["default_start"] != "2026-10-15T13:00" {
		t.Fatalf("list: %v", l.body)
	}
	rid := num(obj(items[0])["id"])
	expect(t, e.post(admin, fmt.Sprintf("/api/reservations/%d/arrive", rid), nil), 200, "arrive")
	expectField(t, e.post(admin, fmt.Sprintf("/api/reservations/%d/cancel", rid), nil), "reservation", "Эта бронь уже закрыта.")
	if e.activityCount("reservation.created", rid) != 1 || e.activityCount("reservation.arrived", rid) != 1 ||
		e.outboxCount("computer.reservation_changed") != 2 || e.outboxCount("reservation.changed") != 2 {
		t.Fatal("journal/events")
	}
	c := e.get(admin, "/api/reservations/customers?q=али")
	data, _ := c.body["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("customers: %v", c.body)
	}
	if c := e.get(admin, "/api/reservations/customers?q=а"); len(c.body["data"].([]any)) != 0 {
		t.Fatal("short query")
	}
	m := e.post(admin, "/api/reservations/many", map[string]any{"computer_ids": []int64{pc}, "user_id": client, "starts_at": "2026-10-16T10:00", "duration_minutes": 120})
	expect(t, m, http.StatusCreated, "many")
}

func TestG4Todos(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e, _ := newG4Env(t, now)
	_, admin := e.staff("Админ", "admin")
	_, operator := e.staff("Оператор", "operator")

	expectField(t, e.post(admin, "/api/todos", map[string]any{"title": "Протереть", "due_on": "2026-10-14"}), "due_on", "Задачу нельзя поставить на прошедший день.")
	r := e.post(admin, "/api/todos", map[string]any{"title": "Протереть столы", "due_on": "2026-10-15"})
	expect(t, r, http.StatusCreated, "create")
	e.exec("INSERT INTO todo_tasks (club_id, title, due_on, created_at, updated_at) VALUES ($1, 'Вчерашняя', '2026-10-14', now(), now())", e.club)
	l := e.get(operator, "/api/todos")
	tasks, _ := l.body["tasks"].([]any)
	if len(tasks) != 2 || obj(tasks[0])["title"] != "Вчерашняя" || l.body["is_today"] != true {
		t.Fatalf("today with overdue: %v", l.body)
	}
	if l := e.get(operator, "/api/todos?date=2026-10-14"); len(l.body["tasks"].([]any)) != 1 {
		t.Fatal("yesterday")
	}
	id := num(obj(tasks[1])["id"])
	tg := e.post(operator, fmt.Sprintf("/api/todos/%d/toggle", id), map[string]any{"done": true})
	if tg.status != 200 || tg.body["done"] != true || tg.body["done_by"] != "Оператор" {
		t.Fatalf("toggle: %v", tg.body)
	}
	e.post(operator, fmt.Sprintf("/api/todos/%d/toggle", id), map[string]any{"done": true}) // без изменений — без журнала
	if e.activityCount("todo.done", id) != 1 {
		t.Fatal("todo.done once")
	}
	if r := e.send4("DELETE", operator, fmt.Sprintf("/api/todos/%d", id), nil); r.status != http.StatusForbidden {
		t.Fatalf("operator delete: %d", r.status)
	}
	expect(t, e.send4("DELETE", admin, fmt.Sprintf("/api/todos/%d", id), nil), 200, "delete")
	if e.outboxCount("todo.changed") != 3 {
		t.Fatalf("todo.changed: %d", e.outboxCount("todo.changed"))
	}
}

func TestG4Chat(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e, cd := newG4Env(t, now)
	_, manager := e.staff("Менеджер", "manager")
	_, cashier := e.staff("Кассир Айгерим", "cashier")
	guest, _ := e.member("Гость", "client", "", false)
	e.exec("INSERT INTO chat_messages (club_id, customer_id, from_staff, body, created_at, updated_at) VALUES ($1, $2, false, 'Привет', now(), now())", e.club, guest)

	m := e.get(manager, "/api/chat")
	if m.body["can_answer"] != false || num(m.body["unread"]) != 1 || obj(m.body["customer"])["name"] != "Гость" {
		t.Fatalf("manager reads: %v", m.body)
	}
	if r := e.post(manager, fmt.Sprintf("/api/chat/%d/messages", guest), map[string]any{"body": "Ответ"}); r.status != http.StatusForbidden ||
		r.body["message"] != "Нет права отвечать в чате. Его даёт владелец клуба в «Настройки → Права»." {
		t.Fatalf("manager reply: %d %v", r.status, r.body)
	}
	c := e.get(cashier, fmt.Sprintf("/api/chat?customer=%d", guest))
	if c.body["can_answer"] != true || num(c.body["unread"]) != 0 {
		t.Fatalf("cashier opens: %v", c.body)
	}
	expectField(t, e.post(cashier, fmt.Sprintf("/api/chat/%d/messages", guest), map[string]any{"body": "  "}), "body", "The body field is required.")
	r := e.post(cashier, fmt.Sprintf("/api/chat/%d/messages", guest), map[string]any{"body": "Здравствуйте"})
	expect(t, r, http.StatusCreated, "reply")
	if obj(r.body["message"])["author"] != "Кассир Айгерим" || len(cd.sent) != 1 || !strings.HasSuffix(cd.sent[0], ":Кассир Айгерим:Здравствуйте") {
		t.Fatalf("reply: %v %v", r.body, cd.sent)
	}
	if r := e.post(cashier, "/api/chat/999999/messages", map[string]any{"body": "x"}); r.status != http.StatusNotFound {
		t.Fatalf("no conversation: %d", r.status)
	}
	if e.outboxCount("chat.unread") == 0 || e.outboxCount("chat.message") != 1 {
		t.Fatal("chat events")
	}
	p := e.get(cashier, fmt.Sprintf("/api/chat/%d/messages?after=0", guest))
	if msgs, _ := p.body["messages"].([]any); len(msgs) != 2 {
		t.Fatalf("poll: %v", p.body)
	}
}

func multipartBody(t *testing.T, fields map[string]string, img []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, k := range []string{"title", "body", "is_published", "remove_image"} {
		if v, ok := fields[k]; ok {
			_ = w.WriteField(k, v)
		}
	}
	if img != nil {
		fw, _ := w.CreateFormFile("image", "pic.png")
		_, _ = fw.Write(img)
	}
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

func (e *adminEnv) multipart(method, tok, path string, fields map[string]string, img []byte) resp {
	e.t.Helper()
	body, ct := multipartBody(e.t, fields, img)
	req, _ := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	out := resp{status: res.StatusCode, header: res.Header}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func pngImage(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestG4News(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e, _ := newG4Env(t, now)
	_, manager := e.staff("Менеджер", "manager")
	_, cashier := e.staff("Кассир", "cashier")

	expectField(t, e.multipart("POST", manager, "/api/news", map[string]string{"title": "", "body": "x"}, nil), "title", "The title field is required.")
	if r := e.multipart("POST", manager, "/api/news", map[string]string{"title": "T", "body": "B"}, pngImage(t, 10, 10)); fieldError(r, "image") == "" {
		t.Fatalf("small image: %v", r.body)
	}
	if r := e.multipart("POST", cashier, "/api/news", map[string]string{"title": "T", "body": "B"}, nil); r.status != http.StatusForbidden {
		t.Fatalf("cashier: %d", r.status)
	}
	img := pngImage(t, 64, 64)
	r := e.multipart("POST", manager, "/api/news", map[string]string{"title": "Турнир", "body": "Суббота", "is_published": "1"}, img)
	expect(t, r, http.StatusCreated, "create")
	l := e.get(manager, "/api/news")
	items, _ := l.body["items"].([]any)
	if len(items) != 1 || obj(items[0])["is_published"] != true || obj(items[0])["image_url"] == nil {
		t.Fatalf("list: %v", l.body)
	}
	id := num(obj(items[0])["id"])
	url := obj(items[0])["image_url"].(string)
	res, err := http.Get(e.srv.URL + url) //nolint:noctx // тест
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Equal(got, img) || res.Header.Get("Content-Type") != "image/png" || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("image: %d %s", res.StatusCode, res.Header)
	}
	if res, _ := http.Get(e.srv.URL + fmt.Sprintf("/news-images/%d/other.png", id)); res.StatusCode != 404 { //nolint:noctx // тест
		t.Fatalf("wrong file name: %d", res.StatusCode)
	}
	// Без is_published при изменении — черновик (?? false), картинка убрана.
	r = e.multipart("PATCH", manager, fmt.Sprintf("/api/news/%d", id), map[string]string{"title": "Турнир 2", "body": "Суббота", "remove_image": "1"}, nil)
	expect(t, r, 200, "update")
	if e.scalar("SELECT count(*) FROM news WHERE id = $1 AND NOT is_published AND image_path IS NULL", id) != 1 ||
		e.scalar("SELECT count(*) FROM news_images WHERE news_id = $1", id) != 0 {
		t.Fatal("update")
	}
	platform := e.scalar("INSERT INTO news (club_id, title, body, created_at, updated_at) VALUES (NULL, 'P', 'P', now(), now()) RETURNING id")
	if r := e.send4("DELETE", manager, fmt.Sprintf("/api/news/%d", platform), nil); r.status != http.StatusNotFound {
		t.Fatalf("platform news: %d", r.status)
	}
	expect(t, e.send4("DELETE", manager, fmt.Sprintf("/api/news/%d", id), nil), 200, "delete")
	if e.activityCount("news.created", id) != 1 || e.activityCount("news.deleted", 0) != 1 {
		t.Fatal("journal")
	}
}

func TestG4ActivityLogsAndNotifications(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e, _ := newG4Env(t, now)
	ownerID, owner := e.staff("Владелец", "owner")
	_, manager := e.staff("Менеджер", "manager")
	_, cashier := e.staff("Кассир", "cashier")
	ins := func(event, desc, details string, at time.Time) {
		var d any
		if details != "" {
			d = details
		}
		e.exec(`INSERT INTO activity_logs (club_id, event, description, user_id, details, ip_address, created_at)
			VALUES ($1, $2, $3, $4, $5::jsonb, '10.0.0.1', $6)`, e.club, event, desc, ownerID, d, at)
	}
	for i := range 55 {
		ins("cash.income", fmt.Sprintf("Пополнение %d", i), `{"amount": 1500.00, "ok": true}`, now.Add(-time.Duration(i)*time.Minute))
	}
	ins("tariff.created", "Создан тариф: X", `{"_t": {"key": "Создан тариф: :tariff", "params": {"tariff": "X"}}, "zone_count": 2}`, now.Add(-2*time.Hour))
	ins("auth.login_failed", "Неудачный вход: +7", "", now.Add(-time.Hour))

	r := e.get(manager, "/api/activity-logs")
	expect(t, r, 200, "list")
	items, _ := r.body["items"].([]any)
	if num(r.body["total"]) != 57 || len(items) != 50 || r.body["next_cursor"] == nil {
		t.Fatalf("page 1: total %v, %d items", r.body["total"], len(items))
	}
	det, _ := obj(items[0])["details"].([]any)
	if len(det) != 2 || obj(det[0])["key"] != "ok" || obj(det[0])["value"] != "да" || obj(det[1])["value"] != "1500" {
		t.Fatalf("details (jsonb key order, PHP number): %v", det)
	}
	r2 := e.get(manager, "/api/activity-logs?cursor="+r.body["next_cursor"].(string))
	if items2, _ := r2.body["items"].([]any); len(items2) != 7 || r2.body["next_cursor"] != nil {
		t.Fatalf("page 2: %v", r2.body)
	}
	f := e.get(manager, "/api/activity-logs?event=tariff.created&from=2026-10-15&to=2026-10-15")
	fi, _ := f.body["items"].([]any)
	if len(fi) != 1 || obj(fi[0])["description"] != "Создан тариф: X" || len(obj(fi[0])["details"].([]any)) != 1 {
		t.Fatalf("filter: %v", f.body)
	}
	expectField(t, e.get(manager, "/api/activity-logs?event=nope"), "event", "The selected event is invalid.")
	if r := e.get(cashier, "/api/activity-logs"); r.status != http.StatusForbidden {
		t.Fatalf("cashier: %d", r.status)
	}

	// Колокольчик: чужой неудачный вход — менеджеру; кассиру — только чат/заказы/остатки.
	n := e.get(manager, "/api/notifications")
	expect(t, n, 200, "notifications")
	list, _ := n.body["items"].([]any)
	if len(list) != 1 || obj(list[0])["id"] == "" || obj(list[0])["title"] != "Неудачный вход" || num(n.body["unread_count"]) != 1 {
		t.Fatalf("manager bell: %v", n.body)
	}
	if obj(list[0])["at_human"] != "1 час назад" {
		t.Fatalf("at_human: %v", obj(list[0])["at_human"])
	}
	if n := e.get(cashier, "/api/notifications"); len(n.body["items"].([]any)) != 0 {
		t.Fatalf("cashier bell: %v", n.body)
	}
	expect(t, e.post(manager, "/api/notifications/seen", nil), 200, "seen")
	if n := e.get(manager, "/api/notifications"); num(n.body["unread_count"]) != 0 {
		t.Fatalf("after seen: %v", n.body)
	}
	_ = owner
}
