//go:build integration

package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
)

// fakeSender — команды агентам (agent/commands.Sender).
type fakeSender struct {
	mu   sync.Mutex
	sent map[int64][]commands.Kind
}

func (f *fakeSender) Send(_ context.Context, _, computerID int64, cmd *agentv1.Command) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent[computerID] = append(f.sent[computerID], commands.KindOf(cmd))
	return "id", nil
}

func (f *fakeSender) kinds(pc int64) []commands.Kind {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]commands.Kind(nil), f.sent[pc]...)
}

// newMapEnv — как newAdminEnv, плюс отправитель команд агентам.
func newMapEnv(t *testing.T) (*adminEnv, *fakeSender) {
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
	bill.SetNotifier(n)
	pr := &fakePresence{states: map[int64][]presence.State{}}
	snd := &fakeSender{sent: map[int64][]commands.Kind{}}
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim,
		Pool: d.App, Billing: bill, Cash: cashSvc, Presence: pr, AgentCommands: snd,
	}))
	t.Cleanup(srv.Close)
	return &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("map%d", adminSeq.Add(1))), presence: pr, notifier: n}, snd
}

func (e *adminEnv) send(method, tok, path string, body any) resp {
	e.t.Helper()
	return do(e.t, method, e.srv.URL+path, body, bearer(tok))
}

func zoneBody(name string, extra map[string]any) map[string]any {
	b := map[string]any{"name": name, "icon": "desktop", "theme": "neutral", "status": "available", "is_featured": false, "sort_order": 0}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// Зоны: создание, правила формы, тарифы зоны, пересечение карточек, журнал.
func TestClubMapZones(t *testing.T) {
	e, _ := newMapEnv(t)
	_, manager := e.staff("Менеджер", "manager")
	_, operator := e.staff("Оператор", "operator")

	expectField(t, e.post(manager, "/api/club-map", zoneBody("", nil)), "name", "The name field is required.")
	expectField(t, e.post(manager, "/api/club-map", zoneBody("Z", map[string]any{"icon": "rocket"})), "icon", "The selected icon is invalid.")
	expectField(t, e.post(manager, "/api/club-map", zoneBody("Z", map[string]any{"price_per_hour_tiyn": 0})), "price_per_hour", "The price per hour field must be at least 0.01.")
	if r := e.post(operator, "/api/club-map", zoneBody("Z", nil)); r.status != http.StatusForbidden {
		t.Fatalf("operator create: %d", r.status)
	}

	t1 := e.scalar("INSERT INTO tariffs (club_id, name, duration_minutes, price, created_at, updated_at) VALUES ($1, 'Час', 60, 50000, now(), now()) RETURNING id", e.club)
	r := e.post(manager, "/api/club-map", zoneBody("Зал", map[string]any{"capacity": 10, "tariff_ids": []int64{t1}, "price_per_hour_tiyn": 60000}))
	expect(t, r, http.StatusCreated, "create zone")
	if r.body["message"] != "Зона добавлена." {
		t.Fatalf("message %v", r.body)
	}
	z1 := num(r.body["id"])
	z2 := num(e.post(manager, "/api/club-map", zoneBody("Бар", map[string]any{"icon": "mug-hot"})).body["id"])
	// Позиции: 20 + n·30 (n — число зон до создания).
	if x := e.scalar("SELECT position_x FROM club_zones WHERE id = $1", z2); x != 50 {
		t.Fatalf("second zone x = %d", x)
	}
	if e.activityCount("zone.created", z1) != 1 {
		t.Fatal("zone.created")
	}
	// Пересечение с зазором 20 — 409 с текстом старого кода.
	r = e.send("PATCH", manager, fmt.Sprintf("/api/club-map/%d/layout", z2), map[string]any{"position_x": 20, "position_y": 20, "width": 320, "height": 180})
	if r.status != http.StatusConflict || r.body["message"] != "Карточки залов не могут пересекаться." {
		t.Fatalf("overlap: %d %v", r.status, r.body)
	}
	expect(t, e.send("PATCH", manager, fmt.Sprintf("/api/club-map/%d/layout", z2), map[string]any{"position_x": 600, "position_y": 20, "width": 320, "height": 180}), 200, "layout")
	// Изменение: тариф снят — журнал с tariff_ids.
	expect(t, e.send("PUT", manager, fmt.Sprintf("/api/club-map/%d", z1), zoneBody("Зал 1", map[string]any{"capacity": 10})), 200, "update")
	if n := e.scalar("SELECT count(*) FROM club_zone_tariff WHERE club_zone_id = $1", z1); n != 0 {
		t.Fatalf("tariffs left %d", n)
	}
	if e.activityCount("zone.updated", z1) != 1 {
		t.Fatal("zone.updated")
	}

	// Экран: зоны с тарифами, тарифы клуба.
	m := e.get(operator, "/api/club-map")
	expect(t, m, 200, "map")
	if len(m.body["zones"].([]any)) != 2 || len(m.body["tariffs"].([]any)) != 1 || m.body["guest_mode"] != true {
		t.Fatalf("map %v", m.body)
	}
}

// Решение Q-018: зону с идущей сессией не удалить; тарифы, оставшиеся без зон, выключаются.
func TestClubMapDeleteZoneQ018(t *testing.T) {
	e, _ := newMapEnv(t)
	_, manager := e.staff("Менеджер", "manager")
	z1 := num(e.post(manager, "/api/club-map", zoneBody("A", nil)).body["id"])
	z2 := num(e.post(manager, "/api/club-map", zoneBody("B", nil)).body["id"])
	only := e.scalar("INSERT INTO tariffs (club_id, name, duration_minutes, price, created_at, updated_at) VALUES ($1, 'Только A', 60, 50000, now(), now()) RETURNING id", e.club)
	both := e.scalar("INSERT INTO tariffs (club_id, name, duration_minutes, price, created_at, updated_at) VALUES ($1, 'A и B', 60, 50000, now(), now()) RETURNING id", e.club)
	everywhere := e.scalar("INSERT INTO tariffs (club_id, name, duration_minutes, price, created_at, updated_at) VALUES ($1, 'Везде', 60, 50000, now(), now()) RETURNING id", e.club)
	e.exec("INSERT INTO club_zone_tariff (club_zone_id, tariff_id, club_id) VALUES ($1, $3, $4), ($1, $5, $4), ($2, $5, $4)", z1, z2, only, e.club, both)
	pc := num(e.post(manager, fmt.Sprintf("/api/club-map/%d/computers", z1), nil).body["id"])
	e.exec("UPDATE club_computers SET current_session_id = 'S-1', session_guest = true, session_ends_at = now() + interval '1 hour' WHERE id = $1", pc)

	r := e.send("DELETE", manager, fmt.Sprintf("/api/club-map/%d", z1), nil)
	expectField(t, r, "zone", "На ПК зоны идёт сессия: удалить зону можно после её окончания.")
	if e.scalar("SELECT count(*) FROM club_zones WHERE id = $1", z1) != 1 {
		t.Fatal("zone deleted with a running session")
	}
	// Сессия закончилась (конец в прошлом) — удалить можно.
	e.exec("UPDATE club_computers SET session_ends_at = now() - interval '1 minute' WHERE id = $1", pc)
	r = e.send("DELETE", manager, fmt.Sprintf("/api/club-map/%d", z1), nil)
	expect(t, r, 200, "delete")
	if list := r.body["disabled_tariffs"].([]any); len(list) != 1 || list[0] != "Только A" {
		t.Fatalf("disabled %v", r.body)
	}
	active := func(id int64) int64 {
		return e.scalar("SELECT is_active::int FROM tariffs WHERE id = $1", id)
	}
	if active(only) != 0 || active(both) != 1 || active(everywhere) != 1 {
		t.Fatalf("active: only=%d both=%d everywhere=%d", active(only), active(both), active(everywhere))
	}
	if e.scalar("SELECT count(*) FROM club_computers WHERE id = $1", pc) != 0 || e.activityCount("zone.deleted", z1) != 1 {
		t.Fatal("cascade / journal")
	}
}

// Карточки ПК: добавление по сетке, правила формы, удаление при сессии, место в зоне.
func TestClubMapComputers(t *testing.T) {
	e, _ := newMapEnv(t)
	_, manager := e.staff("Менеджер", "manager")
	z := num(e.post(manager, "/api/club-map", zoneBody("Зал", map[string]any{"capacity": 2})).body["id"])
	bar := num(e.post(manager, "/api/club-map", zoneBody("Бар", map[string]any{"icon": "mug-hot"})).body["id"])
	if r := e.post(manager, fmt.Sprintf("/api/club-map/%d/computers", bar), nil); r.status != 404 {
		t.Fatalf("add to bar: %d", r.status)
	}
	a := e.post(manager, fmt.Sprintf("/api/club-map/%d/computers", z), nil)
	b := e.post(manager, fmt.Sprintf("/api/club-map/%d/computers", z), nil)
	c := e.post(manager, fmt.Sprintf("/api/club-map/%d/computers", z), nil)
	if a.body["added"] != true || b.body["added"] != true || c.body["added"] != false || c.body["message"] != "Достигнута вместимость зоны." {
		t.Fatalf("add: %v %v %v", a.body, b.body, c.body)
	}
	pa, pb := num(a.body["id"]), num(b.body["id"])
	if x := e.scalar("SELECT position_x FROM club_computers WHERE id = $1", pb); x != 52 {
		t.Fatalf("second pc x = %d", x)
	}
	path := fmt.Sprintf("/api/club-computers/%d", pb)
	expectField(t, e.send("PATCH", manager, path, map[string]any{"number": 1, "club_zone_id": z}), "number", "Номер уже используется в этой зоне.")
	expectField(t, e.send("PATCH", manager, path, map[string]any{"number": 2, "club_zone_id": z, "mac_address": "zz"}), "mac_address", "Введите MAC-адрес в формате AA:BB:CC:DD:EE:FF.")
	expect(t, e.send("PATCH", manager, path, map[string]any{"number": 7, "display_name": "VIP", "mac_address": "aa-bb-cc-dd-ee-0f", "club_zone_id": z}), 200, "update pc")
	var mac string
	if err := e.db.Owner.QueryRow(context.Background(), "SELECT mac_address FROM club_computers WHERE id = $1", pb).Scan(&mac); err != nil || mac != "AA:BB:CC:DD:EE:0F" {
		t.Fatalf("mac %q %v", mac, err)
	}
	expectField(t, e.send("PATCH", manager, fmt.Sprintf("/api/club-computers/%d", pa), map[string]any{"number": 1, "mac_address": "AA:BB:CC:DD:EE:0F", "club_zone_id": z}), "mac_address", "Этот MAC-адрес уже привязан к другому ПК клуба.")
	// Место: ячейка занята — 409.
	r := e.send("PATCH", manager, fmt.Sprintf("/api/club-computers/%d/layout", pb), map[string]any{"position_x": 5, "position_y": 5})
	if r.status != http.StatusConflict || r.body["message"] != "Эта ячейка уже занята другим компьютером." {
		t.Fatalf("cell: %d %v", r.status, r.body)
	}
	// Удаление при идущей сессии — 422 с именем ПК.
	e.exec("UPDATE club_computers SET current_session_id = 'S-2', session_guest = true, session_ends_at = now() + interval '1 hour' WHERE id = $1", pb)
	expectField(t, e.send("DELETE", manager, path, nil), "computer", "На ПК VIP идёт сессия: удалить его можно после её окончания.")
	e.exec("UPDATE club_computers SET current_session_id = NULL, session_guest = false, session_ends_at = NULL WHERE id = $1", pb)
	if r := e.send("DELETE", manager, path, nil); r.status != 200 || r.body["message"] != "ПК VIP удалён." {
		t.Fatalf("delete: %d %v", r.status, r.body)
	}
}

// Управление ПК: команды агентам по возможностям, статус, техрежим, флаг брони, уведомление, тексты как в старом.
func TestClubMapControl(t *testing.T) {
	e, snd := newMapEnv(t)
	_, operator := e.staff("Оператор", "operator")
	_, cashier := e.staff("Кассир", "cashier")
	z := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Hall', now(), now()) RETURNING id", e.club)
	newPC := func(n int, version string) int64 {
		return e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, agent_version, mac_address, created_at, updated_at) VALUES ($1, $2, $3, NULLIF($4, ''), $5, now(), now()) RETURNING id",
			e.club, z, n, version, fmt.Sprintf("AA:BB:CC:DD:EE:%02d", n))
	}
	pc1, pc2, pc3, relay := newPC(1, "3.0.0"), newPC(2, "3.0.0"), newPC(3, ""), newPC(4, "3.0.0")
	caps := presence.Capabilities{PowerControl: true, ServiceMode: true, ShowMessage: true, WakeOnLan: true}
	e.presence.mu.Lock()
	e.presence.states[e.club] = []presence.State{
		{ClubID: e.club, ComputerID: pc1, Online: true, Capabilities: caps, LastSeenAt: time.Now()},
		{ClubID: e.club, ComputerID: pc2, Online: true, LastSeenAt: time.Now()}, // старый агент: без возможностей
		{ClubID: e.club, ComputerID: relay, Online: true, Capabilities: caps, LastSeenAt: time.Now()},
	}
	e.presence.mu.Unlock()

	// Завершение сессий — pc_control.delete (admin, manager, operator): кассиру 403.
	if r := e.post(cashier, "/api/club-computers/end-sessions", map[string]any{"computer_ids": []int64{pc1}}); r.status != 403 {
		t.Fatalf("cashier: %d", r.status)
	}
	expectField(t, e.post(operator, "/api/club-computers/control", map[string]any{"computer_ids": []int64{pc1}, "action": "fly"}), "action", "The selected action is invalid.")
	expectField(t, e.post(operator, "/api/club-computers/control", map[string]any{"computer_ids": []int64{pc1, 999999}, "action": "restart"}), "computer_ids.1", "The selected computer ids.1 is invalid.")

	r := e.post(operator, "/api/club-computers/control", map[string]any{"computer_ids": []int64{pc1, pc2, pc3}, "action": "restart"})
	expect(t, r, 200, "restart")
	want := "Перезагрузка: ПК получит команду в течение минуты (1 ПК). На 2 ПК агент устарел (нужна версия 0.0.66): они команду не выполнят."
	if r.body["status"] != "restarting" || r.body["message"] != want {
		t.Fatalf("restart: %v", r.body)
	}
	if k := snd.kinds(pc1); len(k) != 1 || k[0] != commands.KindReboot || len(snd.kinds(pc2)) != 0 {
		t.Fatalf("sent %v", snd.sent)
	}
	if e.scalar("SELECT count(*) FROM club_computers WHERE club_id = $1 AND status = 'restarting'", e.club) != 3 || e.activityCount("computer.controlled", 0) != 1 {
		t.Fatal("status / journal")
	}

	// Техрежим: визит сотрудника из панели.
	expect(t, e.post(operator, "/api/club-computers/control", map[string]any{"computer_ids": []int64{pc1}, "action": "service"}), 200, "service")
	if e.scalar("SELECT count(*) FROM club_computers WHERE id = $1 AND staff_kind = 'panel' AND staff_until > now()", pc1) != 1 {
		t.Fatal("staff visit")
	}
	// Включение: Wake-on-LAN через включённый ПК клуба (не из выбранных).
	r = e.post(operator, "/api/club-computers/control", map[string]any{"computer_ids": []int64{pc3}, "action": "power_on"})
	if r.body["message"] != "Сигнал включения отправят другие ПК клуба (Wake-on-LAN): 1 ПК. В BIOS этих ПК должен быть включён Wake-on-LAN." {
		t.Fatalf("power on: %v", r.body)
	}
	if k := snd.kinds(relay); len(k) != 1 || k[0] != commands.KindWakeOnLan {
		t.Fatalf("relay %v", k)
	}
	// Флаг брони: переключение.
	r = e.post(operator, "/api/club-computers/control", map[string]any{"computer_ids": []int64{pc1, pc2}, "action": "reserve"})
	if r.body["reserved"] != true || e.scalar("SELECT count(*) FROM club_computers WHERE club_id = $1 AND is_reserved", e.club) != 2 {
		t.Fatalf("reserve %v", r.body)
	}
	st := e.get(operator, fmt.Sprintf("/api/club-map/state?ids=%d", pc1))
	if list := st.body["computers"].([]any); len(list) != 1 || obj(list[0])["status"] != "reserved" {
		t.Fatalf("state %v", st.body)
	}

	// Уведомление.
	expectField(t, e.post(operator, "/api/club-computers/notify", map[string]any{"computer_ids": []int64{pc1}, "message": ""}), "message", "The message field is required.")
	r = e.post(operator, "/api/club-computers/notify", map[string]any{"computer_ids": []int64{pc1, pc2}, "message": " Привет "})
	if r.body["sent"] != float64(1) || r.body["message"] != "Уведомление появится на экране в течение минуты (1 ПК). На 1 ПК агент устарел (нужна версия 0.0.73): там уведомления не будет." {
		t.Fatalf("notify %v", r.body)
	}
	if e.activityCount("computer.messaged", 0) != 1 {
		t.Fatal("computer.messaged")
	}
	// Завершение сессий: нет сессий — текст старого кода.
	r = e.post(operator, "/api/club-computers/end-sessions", map[string]any{"computer_ids": []int64{pc1}})
	if r.body["ended"] != float64(0) || r.body["message"] != "На выбранных ПК нет активной сессии." {
		t.Fatalf("end %v", r.body)
	}
}

// Карта: подсветка и статус ПК по присутствию и сессиям; дашборд — сводка и роли; список ПК.
func TestClubMapStatesDashboardList(t *testing.T) {
	e, _ := newMapEnv(t)
	ownerID, owner := e.staff("Владелец", "owner")
	_, cashier := e.staff("Кассир", "cashier")
	_ = ownerID
	z := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Hall', now(), now()) RETURNING id", e.club)
	free := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, status, created_at, updated_at) VALUES ($1, $2, 1, 'online', now(), now()) RETURNING id", e.club, z)
	guest := e.scalar(`INSERT INTO club_computers (club_id, club_zone_id, number, status, current_session_id, session_guest, session_ends_at, created_at, updated_at)
		VALUES ($1, $2, 2, 'online', 'S-9', true, now() + interval '1 hour', now(), now()) RETURNING id`, e.club, z)
	off := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, status, last_seen_at, created_at, updated_at) VALUES ($1, $2, 3, 'online', now() - interval '1 day', now(), now()) RETURNING id", e.club, z)
	booked := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 4, now(), now()) RETURNING id", e.club, z)
	e.exec("INSERT INTO computer_reservations (club_id, club_computer_id, starts_at, ends_at, created_at, updated_at) VALUES ($1, $2, now() + interval '10 minutes', now() + interval '70 minutes', now(), now())", e.club, booked)
	e.presence.mu.Lock()
	e.presence.states[e.club] = []presence.State{
		{ClubID: e.club, ComputerID: free, Online: true, AgentState: "idle", LastSeenAt: time.Now()},
		{ClubID: e.club, ComputerID: guest, Online: true, AgentState: "in_session", LastSeenAt: time.Now()},
	}
	e.presence.mu.Unlock()
	m := e.get(owner, "/api/club-map")
	expect(t, m, 200, "map")
	got := map[int64]map[string]any{}
	for _, s := range m.body["states"].([]any) {
		got[num(obj(s)["id"])] = obj(s)
	}
	check := func(id int64, status, light string) {
		t.Helper()
		if got[id]["status"] != status || got[id]["light"] != light {
			t.Fatalf("pc %d: %v", id, got[id])
		}
	}
	check(free, "online", "pc-free")
	check(guest, "online", "pc-guest")
	check(off, "offline", "")
	check(booked, "reserved", "")
	if got[guest]["session_tariff"] != "Оплачено на кассе" || got[guest]["session_user"] != "Гость без аккаунта" {
		t.Fatalf("guest %v", got[guest])
	}

	// Дашборд: кассиру — 403 (старый код уводил на кассу), владельцу — сводка.
	if r := e.get(cashier, "/api/dashboard"); r.status != 403 {
		t.Fatalf("cashier dashboard %d", r.status)
	}
	d := e.get(owner, "/api/dashboard")
	expect(t, d, 200, "dashboard")
	if num(d.body["computer_count"]) != 4 || num(d.body["online_computer_count"]) != 2 || num(d.body["reserved_computer_count"]) != 0 ||
		num(d.body["zone_count"]) != 1 || len(d.body["days"].([]any)) != 7 {
		t.Fatalf("dashboard %v", d.body)
	}

	// Список ПК: по зоне и номеру, связь — из присутствия.
	l := e.get(cashier, "/api/club-computers")
	expect(t, l, 200, "list")
	items := l.body["items"].([]any)
	if num(l.body["total"]) != 4 || len(items) != 4 || obj(items[0])["connection"] != "online" || obj(items[2])["connection"] != "offline" {
		t.Fatalf("list %v", l.body)
	}
}

// F1-5: тексты ошибок API на языке клуба — ключ с подстановками и правило валидатора.
func TestErrorMessagesInClubLocale(t *testing.T) {
	e, _ := newMapEnv(t)
	_, manager := e.staff("Менеджер", "manager")
	e.exec("INSERT INTO languages (code, name, is_enabled, is_system, sort_order, created_at, updated_at) VALUES ('kk', 'Қазақша', true, true, 2, now(), now()) ON CONFLICT (code) DO NOTHING")
	lang := e.scalar("SELECT id FROM languages WHERE code = 'kk'")
	for _, row := range [][3]string{
		{"*", "На ПК :computer идёт сессия: удалить его можно после её окончания.", "ПК :computer: сессия жүріп жатыр."},
		{"validation", "required", "«:attribute» өрісі міндетті."},
		{"validation", "attributes.name", "атауы"},
	} {
		e.exec(`INSERT INTO translations (language_id, "group", key, key_hash, value, created_at, updated_at)
			VALUES ($1, $2::text, $3::text, md5($2::text || $3::text), $4, now() + interval '1 second', now() + interval '1 second')
			ON CONFLICT (language_id, "group", key_hash) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
			lang, row[0], row[1], row[2])
	}
	e.exec("UPDATE clubs SET locale = 'kk' WHERE id = $1", e.club)
	expectField(t, e.post(manager, "/api/club-map", zoneBody("", nil)), "name", "«атауы» өрісі міндетті.")
	z := num(e.post(manager, "/api/club-map", zoneBody("Зал", nil)).body["id"])
	pc := num(e.post(manager, fmt.Sprintf("/api/club-map/%d/computers", z), nil).body["id"])
	e.exec("UPDATE club_computers SET current_session_id = 'S', session_guest = true, session_ends_at = now() + interval '1 hour' WHERE id = $1", pc)
	expectField(t, e.send("DELETE", manager, fmt.Sprintf("/api/club-computers/%d", pc), nil), "computer", "ПК PC-01: сессия жүріп жатыр.")
}
