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
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
)

// newWeeklyEnv — панель клуба с фиксированными часами (границы недель в поясе клуба).
func newWeeklyEnv(t *testing.T, now time.Time) *adminEnv {
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
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim, Pool: d.App,
		Now: func() time.Time { return now },
	}))
	t.Cleanup(srv.Close)
	if _, err := db.EnsurePartitions(context.Background(), d.Platform, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 4); err != nil {
		t.Fatal(err)
	}
	return &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("weekly%d", adminSeq.Add(1)))}
}

// Недели здоровья ПК: агрегаты SQL по неделям в поясе клуба (Asia/Almaty, +05) с границей «воскресенье 23:00 /
// понедельник 00:00», взвешенные средние, часы с проблемами из журнала, вердикт с причинами и трендом к прошлой неделе;
// чужой клуб не попадает (club_id + RLS); точки по часам; лимиты параметров; права и 404 чужого ПК.
func TestClubMapComputerHealthWeekly(t *testing.T) {
	// Среда 04.11.2026 12:00 по Алматы; текущая неделя — с понедельника 02.11 00:00 +05 (= 01.11 19:00 UTC).
	now := time.Date(2026, 11, 4, 7, 0, 0, 0, time.UTC)
	e := newWeeklyEnv(t, now)
	_, manager := e.staff("Менеджер", "manager")
	_, noMap := e.staff("Без карты", "operator", `{"club_map.view": false}`)
	e.exec("UPDATE clubs SET timezone = 'Asia/Almaty' WHERE id = $1", e.club)
	zone := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Зал', now(), now()) RETURNING id", e.club)
	pc := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 1, now(), now()) RETURNING id", e.club, zone)
	other := newWeeklyEnv(t, now)
	_, otherManager := other.staff("Чужой", "manager")
	otherZone := other.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Зал', now(), now()) RETURNING id", other.club)
	otherPC := other.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 1, now(), now()) RETURNING id", other.club, otherZone)

	row := func(club, computer int64, hour string, online, avgT, maxT int, extra string) {
		t.Helper()
		e.exec(`INSERT INTO computer_health_hourly (club_id, computer_id, hour, online_minutes, session_minutes,
			cpu_load_avg_bp, cpu_load_max_bp, cpu_temp_avg_deci, cpu_temp_max_deci, system_disk_free_mb, system_disk_total_mb)
			VALUES ($1, $2, $3::timestamptz, $4, 0, 3000, 9000, $5, $6, 200000, 512000)`, club, computer, hour, online, avgT, maxT)
		if extra != "" {
			e.exec(`UPDATE computer_health_hourly SET `+extra+` WHERE club_id = $1 AND computer_id = $2 AND hour = $3::timestamptz`,
				club, computer, hour)
		}
	}
	// Прошлая неделя: воскресенье 01.11 23:00 по Алматы — последний час прошлой недели.
	row(e.club, pc, "2026-11-01T18:00:00Z", 60, 600, 650, "")
	// Текущая неделя: понедельник 00:00 по Алматы — первый час недели.
	row(e.club, pc, "2026-11-01T19:00:00Z", 30, 700, 950, "bugchecks = 1, smart_worst = 'warning', system_disk_free_mb = 40000")
	row(e.club, pc, "2026-11-02T10:00:00Z", 60, 700, 920, "unexpected_shutdowns = 1, peripheral_disconnects = 2, smart_worst = 'ok'")
	row(e.club, pc, "2026-11-03T10:00:00Z", 60, 760, 910, "")
	// Давняя неделя (12.10) — только при weeks ≥ 4.
	row(e.club, pc, "2026-10-13T10:00:00Z", 60, 500, 550, "")
	// Строка чужого клуба с id нашего ПК (FK это допускает) и строка чужого ПК — не должны попасть.
	row(other.club, pc, "2026-11-02T11:00:00Z", 60, 990, 999, "bugchecks = 5")
	row(other.club, otherPC, "2026-11-02T11:00:00Z", 60, 990, 999, "")
	// Проблема 10:30–11:30 UTC 02.11 пересекается с часом 10:00 (у часа 11:00 строки нет — ПК «не в сети»).
	e.exec(`INSERT INTO computer_health_events (club_id, computer_id, kind, raised_at, cleared_at)
		VALUES ($1, $2, 'mouse', '2026-11-02T10:30:00Z', '2026-11-02T11:30:00Z')`, e.club, pc)

	get := func(env *adminEnv, tok string, path string) resp {
		t.Helper()
		return env.get(tok, path)
	}
	weekly := func(q string) []any {
		t.Helper()
		r := get(e, manager, fmt.Sprintf("/api/club-computers/%d/health/weekly%s", pc, q))
		expect(t, r, http.StatusOK, "weekly"+q)
		return r.body["weeks"].([]any)
	}
	weeks := weekly("")
	if len(weeks) != 3 {
		t.Fatalf("weeks (default 8): %d: %v", len(weeks), weeks)
	}
	cur, prev := weeks[0].(map[string]any), weeks[1].(map[string]any)
	if cur["start"] != "2026-11-02T00:00:00+05:00" || cur["end"] != "2026-11-09T00:00:00+05:00" ||
		prev["start"] != "2026-10-26T00:00:00+05:00" || weeks[2].(map[string]any)["start"] != "2026-10-12T00:00:00+05:00" {
		t.Fatalf("week bounds: %v / %v / %v", cur["start"], prev["start"], weeks[2].(map[string]any)["start"])
	}
	// Текущая: 30 + 60 + 60 минут; средняя t = (700·30 + 700·60 + 760·60) / 150 = 724; часов ≥ 90 °C — 3.
	want := map[string]int64{"online_minutes": 150, "cpu_temp_avg_deci": 724, "cpu_temp_max_deci": 950, "cpu_hot_hours": 3,
		"problem_hours": 1, "bugchecks": 1, "unexpected_shutdowns": 1, "peripheral_disconnects": 2, "system_disk_free_mb": 40000,
		"system_disk_free_bp": 781, "cpu_load_avg_bp": 3000, "cpu_load_max_bp": 9000, "gpu_hot_hours": 0}
	for k, v := range want {
		if num(cur[k]) != v {
			t.Errorf("current week %s = %v, want %d", k, cur[k], v)
		}
	}
	if cur["smart"] != "warning" || cur["gpu_temp_avg_deci"] != nil {
		t.Errorf("current week smart/gpu: %v %v", cur["smart"], cur["gpu_temp_avg_deci"])
	}
	type reason struct {
		Code, Level      string
		Value, Threshold int64
	}
	reasons := func(w map[string]any) (string, []reason) {
		v := w["verdict"].(map[string]any)
		out := []reason{}
		for _, x := range v["reasons"].([]any) {
			m := x.(map[string]any)
			out = append(out, reason{m["code"].(string), m["level"].(string), num(m["value"]), num(m["threshold"])})
		}
		return v["level"].(string), out
	}
	level, got := reasons(cur)
	wantReasons := []reason{{"bugchecks", "repair", 1, 1}, {"cpu_hot", "watch", 3, 90}, {"smart_warning", "watch", 0, 0},
		{"disk_low", "watch", 7, 10}, {"cpu_temp_rise", "watch", 12, 5}}
	if level != "repair" || !reflect.DeepEqual(got, wantReasons) {
		t.Errorf("current verdict: %s %+v", level, got)
	}
	if level, got := reasons(prev); level != "ok" || len(got) != 0 || num(prev["online_minutes"]) != 60 || num(prev["problem_hours"]) != 0 {
		t.Errorf("previous week: %s %+v %v", level, got, prev)
	}
	// weeks=1 — только текущая, тренд к прошлой неделе всё равно считается.
	if w := weekly("?weeks=1"); len(w) != 1 {
		t.Errorf("weeks=1: %v", w)
	} else if _, got := reasons(w[0].(map[string]any)); !reflect.DeepEqual(got, wantReasons) {
		t.Errorf("weeks=1 verdict: %+v", got)
	}
	if w := weekly("?weeks=2"); len(w) != 2 {
		t.Errorf("weeks=2: %d", len(w))
	}

	// Точки по часам текущей недели: по возрастанию, без прошлой недели и чужих строк.
	q := "?from=" + url.QueryEscape("2026-11-02T00:00:00+05:00") + "&to=" + url.QueryEscape("2026-11-09T00:00:00+05:00")
	r := get(e, manager, fmt.Sprintf("/api/club-computers/%d/health/hourly%s", pc, q))
	expect(t, r, http.StatusOK, "hourly")
	items := r.body["items"].([]any)
	if len(items) != 3 || items[0].(map[string]any)["hour"] != "2026-11-01T19:00:00Z" || num(items[0].(map[string]any)["cpu_temp_max_deci"]) != 950 ||
		items[2].(map[string]any)["hour"] != "2026-11-03T10:00:00Z" || items[0].(map[string]any)["gpu_temp_max_deci"] != nil {
		t.Fatalf("hourly: %v", items)
	}

	// Лимиты параметров — 422.
	for _, bad := range []string{
		fmt.Sprintf("/api/club-computers/%d/health/weekly?weeks=0", pc),
		fmt.Sprintf("/api/club-computers/%d/health/weekly?weeks=13", pc),
		fmt.Sprintf("/api/club-computers/%d/health/hourly?from=2026-11-02T00:00:00Z&to=2026-11-02T00:00:00Z", pc),
		fmt.Sprintf("/api/club-computers/%d/health/hourly?from=2026-11-01T00:00:00Z&to=2026-11-09T00:00:01Z", pc),
	} {
		if r := get(e, manager, bad); r.status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d, want 422", bad, r.status)
		}
	}
	if r := get(e, manager, fmt.Sprintf("/api/club-computers/%d/health/hourly?from=2026-11-01T00:00:00Z&to=2026-11-09T00:00:00Z", pc)); r.status != http.StatusOK {
		t.Errorf("8 days: %d", r.status)
	}
	if r := get(e, manager, fmt.Sprintf("/api/club-computers/%d/health/hourly?to=2026-11-09T00:00:00Z", pc)); r.status != http.StatusBadRequest {
		t.Errorf("without from: %d", r.status)
	}

	// Права: без club_map.view — 403; ПК другого клуба — 404 (клуб из аутентификации); чужой менеджер своего ПК
	// видит только свою строку.
	for _, p := range []string{"/health/weekly", "/health/hourly" + q} {
		p = fmt.Sprintf("/api/club-computers/%d", pc) + p
		if r := get(e, noMap, p); r.status != http.StatusForbidden {
			t.Errorf("%s without club_map.view: %d", p, r.status)
		}
		if r := get(other, otherManager, p); r.status != http.StatusNotFound {
			t.Errorf("%s of a foreign computer: %d", p, r.status)
		}
	}
	r = get(other, otherManager, fmt.Sprintf("/api/club-computers/%d/health/weekly", otherPC))
	expect(t, r, http.StatusOK, "other club weekly")
	if w := r.body["weeks"].([]any); len(w) != 1 || num(w[0].(map[string]any)["online_minutes"]) != 60 {
		t.Errorf("other club weeks: %v", w)
	}
}

// Бездисковый ПК (S7-H-18; последний сертификат — по ключу образа): API отдаёт diskless, у недели нет полей системного
// диска и SMART, причин вердикта по дискам нет (строки истории — до перевода ПК на загрузку по сети), часы с проблемами
// дисков не считаются, медленная сеть больше 2 ч — «наблюдать». Обычный ПК с теми же строками — причины по дискам есть,
// медленная сеть в вердикт не входит.
func TestClubMapComputerHealthDiskless(t *testing.T) {
	now := time.Date(2026, 11, 4, 7, 0, 0, 0, time.UTC)
	e := newWeeklyEnv(t, now)
	_, manager := e.staff("Менеджер", "manager")
	zone := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Зал', now(), now()) RETURNING id", e.club)
	regular := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 1, now(), now()) RETURNING id", e.club, zone)
	diskless := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 2, now(), now()) RETURNING id", e.club, zone)
	cert := func(pc int64, serial string, diskless bool, issued string) {
		t.Helper()
		via := "enroll"
		if diskless {
			via = "image_key"
		}
		e.exec(`INSERT INTO agent_certificates (serial, club_id, club_computer_id, spki_sha256, not_before, not_after, issued_at,
			issued_via, diskless, revoked_at, revoke_reason)
			VALUES ($1, $2, $3, $4, $5::timestamptz, $5::timestamptz + interval '365 days', $5::timestamptz, $6, $7,
			        CASE WHEN $7 THEN NULL ELSE $5::timestamptz + interval '1 day' END,
			        CASE WHEN $7 THEN NULL ELSE 'reenrolled' END)`,
			fmt.Sprintf("%x0%s", e.club, serial), e.club, pc, make([]byte, 32), issued, via, diskless)
	}
	// Обычный ПК — сертификат по коду. Бездисковый — сначала был с диском (код, год срока, отозван при переводе), затем
	// сертификаты по ключу образа: решает последний выпущенный, а не самый долгий.
	cert(regular, "a1", false, "2026-10-01T00:00:00Z")
	cert(diskless, "b1", false, "2026-10-01T00:00:00Z")
	cert(diskless, "b2", true, "2026-10-20T00:00:00Z")
	for _, pc := range []int64{regular, diskless} {
		for _, hour := range []string{"2026-11-02T10:00:00Z", "2026-11-02T11:00:00Z", "2026-11-02T12:00:00Z", "2026-11-02T13:00:00Z"} {
			e.exec(`INSERT INTO computer_health_hourly (club_id, computer_id, hour, online_minutes, session_minutes,
				system_disk_free_mb, system_disk_total_mb, smart_worst) VALUES ($1, $2, $3::timestamptz, 60, 0, 1000, 512000, 'failing')`,
				e.club, pc, hour)
		}
		e.exec(`INSERT INTO computer_health_events (club_id, computer_id, kind, raised_at, cleared_at)
			VALUES ($1, $2, 'disk_smart', '2026-11-02T09:00:00Z', '2026-11-02T14:00:00Z'),
			       ($1, $2, 'network_slow', '2026-11-02T09:30:00Z', '2026-11-02T12:30:00Z')`, e.club, pc)
	}

	type view struct {
		diskless bool
		week     map[string]any
		codes    []string
	}
	read := func(pc int64) view {
		t.Helper()
		r := e.get(manager, fmt.Sprintf("/api/club-computers/%d/health/weekly", pc))
		expect(t, r, http.StatusOK, "weekly")
		weeks := r.body["weeks"].([]any)
		if len(weeks) != 1 {
			t.Fatalf("weeks: %v", weeks)
		}
		w := weeks[0].(map[string]any)
		v := view{diskless: r.body["diskless"] == true, week: w, codes: []string{}}
		for _, x := range w["verdict"].(map[string]any)["reasons"].([]any) {
			m := x.(map[string]any)
			v.codes = append(v.codes, fmt.Sprintf("%s:%s:%v:%v", m["code"], m["level"], m["value"], m["threshold"]))
		}
		return v
	}
	reg := read(regular)
	if reg.diskless || num(reg.week["system_disk_free_mb"]) != 1000 || reg.week["smart"] != "failing" ||
		num(reg.week["problem_hours"]) != 4 || num(reg.week["network_slow_hours"]) != 3 ||
		!reflect.DeepEqual(reg.codes, []string{"smart_failing:repair:0:0", "disk_low:watch:0:10"}) {
		t.Errorf("regular PC: diskless %v, week %v, reasons %v", reg.diskless, reg.week, reg.codes)
	}
	dl := read(diskless)
	if _, ok := dl.week["system_disk_free_mb"]; ok || dl.week["smart"] != nil || dl.week["system_disk_free_bp"] != nil {
		t.Errorf("diskless PC week has disk fields: %v", dl.week)
	}
	if !dl.diskless || num(dl.week["problem_hours"]) != 3 || num(dl.week["network_slow_hours"]) != 3 ||
		dl.week["verdict"].(map[string]any)["level"] != "watch" || !reflect.DeepEqual(dl.codes, []string{"network_slow:watch:3:1000"}) {
		t.Errorf("diskless PC: diskless %v, week %v, reasons %v", dl.diskless, dl.week, dl.codes)
	}

	// Состояние и точки по часам тоже несут признак (фронт прячет разделы дисков даже у ПК не в сети).
	for pc, want := range map[int64]bool{regular: false, diskless: true} {
		r := e.get(manager, fmt.Sprintf("/api/club-computers/%d/health", pc))
		expect(t, r, http.StatusOK, "health")
		if r.body["diskless"] != want {
			t.Errorf("health %d: diskless = %v, want %v", pc, r.body["diskless"], want)
		}
		q := "?from=" + url.QueryEscape("2026-11-02T00:00:00Z") + "&to=" + url.QueryEscape("2026-11-03T00:00:00Z")
		r = e.get(manager, fmt.Sprintf("/api/club-computers/%d/health/hourly%s", pc, q))
		expect(t, r, http.StatusOK, "hourly")
		if r.body["diskless"] != want || len(r.body["items"].([]any)) != 4 {
			t.Errorf("hourly %d: diskless = %v, want %v (%v)", pc, r.body["diskless"], want, r.body)
		}
	}
}
