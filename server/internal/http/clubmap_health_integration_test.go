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
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/session"
)

// newHealthEnv — как newMapEnv, но присутствие — настоящий presence.Store в Valkey (снимок, который пишет стрим).
func newHealthEnv(t *testing.T) (*adminEnv, *presence.Store) {
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
	pr := presence.NewStore(vk, fmt.Sprintf("t:health%d:", adminSeq.Add(1)), 0)
	srv := httptest.NewServer(NewRouter(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Auth: svc, Limiter: lim, Pool: d.App, Presence: pr,
	}))
	t.Cleanup(srv.Close)
	return &adminEnv{t: t, db: d, srv: srv, club: d.Club(t, fmt.Sprintf("health%d", adminSeq.Add(1)))}, pr
}

// Снимок присутствия со здоровьем (как пишет стрим: Acquire + Refresh) → карта клуба и карточка ПК; история проблем
// с keyset-пагинацией; право club_map.view; чужой ПК — 404.
func TestClubMapComputerHealth(t *testing.T) {
	ctx := context.Background()
	e, pr := newHealthEnv(t)
	_, manager := e.staff("Менеджер", "manager")
	_, noMap := e.staff("Без карты", "operator", `{"club_map.view": false}`)
	zone := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Зал', now(), now()) RETURNING id", e.club)
	pcNew := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 1, now(), now()) RETURNING id", e.club, zone)
	pcOld := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 2, now(), now()) RETURNING id", e.club, zone)
	pcOff := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 3, now(), now()) RETURNING id", e.club, zone)

	put := func(st presence.State) {
		own, _, _, err := pr.Acquire(ctx, e.club, st.ComputerID, "i1")
		if err != nil {
			t.Fatal(err)
		}
		st.ClubID, st.Online, st.Instance, st.Gen, st.LastSeenAt = e.club, true, "i1", own.Gen, time.Now().UTC()
		if err := pr.Refresh(ctx, presence.RefreshItem{Owner: own, State: &st, ClubID: e.club, ComputerID: st.ComputerID}); err != nil {
			t.Fatal(err)
		}
	}
	put(presence.State{ComputerID: pcNew, AgentVersion: "2.1.0", HasLoad: true, Load: presence.Load{CPUBasisPoints: 4250, RAMBasisPoints: 6100, IdleSeconds: 30},
		RTTMillis: 18, Problems: []string{health.KindCPUHot, health.KindMouse},
		Health: &presence.Health{CPUTempDeci: 925, GPUMeasured: true, GPULoadBP: 8000, LinkMbps: 1000, UptimeSeconds: 7200, Bugchecks7d: 0,
			Disks:       []presence.Disk{{Mount: "C:", System: true, TotalMB: 512000, FreeMB: 100000, Smart: presence.SmartOK}},
			Peripherals: []presence.Peripheral{{Kind: presence.PeripheralMouse, Name: "G102"}}, ReportedAt: time.Now().UTC()}})
	put(presence.State{ComputerID: pcOld, AgentVersion: "1.0.0"}) // старый агент: ни загрузки, ни здоровья

	r := e.get(manager, "/api/club-map/state")
	expect(t, r, http.StatusOK, "map state")
	byID := map[int64]map[string]any{}
	for _, c := range r.body["computers"].([]any) {
		m := c.(map[string]any)
		byID[num(m["id"])] = m
	}
	if l, ok := byID[pcNew]["load"].(map[string]any); !ok || num(l["cpu_bp"]) != 4250 || num(l["ram_bp"]) != 6100 || num(l["idle_seconds"]) != 30 {
		t.Fatalf("map load: %v", byID[pcNew])
	}
	if p, ok := byID[pcNew]["problems"].([]any); !ok || len(p) != 2 || p[0] != "cpu_hot" || p[1] != "mouse" || num(byID[pcNew]["rtt_ms"]) != 18 {
		t.Fatalf("map problems: %v", byID[pcNew])
	}
	for _, id := range []int64{pcOld, pcOff} {
		for _, k := range []string{"load", "problems", "rtt_ms"} {
			if _, ok := byID[id][k]; ok {
				t.Errorf("computer %d without agent data must not carry %s: %v", id, k, byID[id])
			}
		}
	}

	r = e.get(manager, fmt.Sprintf("/api/club-computers/%d/health", pcNew))
	expect(t, r, http.StatusOK, "health")
	hs, _ := r.body["health"].(map[string]any)
	if r.body["connection"] != "online" || r.body["agent_version"] != "2.1.0" || hs == nil || num(hs["cpu_temp_deci"]) != 925 ||
		num(hs["gpu_load_bp"]) != 8000 || hs["gpu_temp_deci"] != nil || num(hs["link_mbps"]) != 1000 || num(hs["uptime_seconds"]) != 7200 {
		t.Fatalf("health: %v", r.body)
	}
	if d := hs["disks"].([]any); len(d) != 1 || d[0].(map[string]any)["smart"] != "ok" || num(d[0].(map[string]any)["free_mb"]) != 100000 {
		t.Fatalf("disks: %v", hs["disks"])
	}
	if p := hs["peripherals"].([]any); len(p) != 1 || p[0].(map[string]any)["connected"] != false || p[0].(map[string]any)["name"] != "G102" {
		t.Fatalf("peripherals: %v", hs["peripherals"])
	}
	r = e.get(manager, fmt.Sprintf("/api/club-computers/%d/health", pcOff))
	expect(t, r, http.StatusOK, "offline health")
	if r.body["connection"] != "offline" || r.body["health"] != nil || r.body["load"] != nil || len(r.body["problems"].([]any)) != 0 {
		t.Fatalf("offline health: %v", r.body)
	}

	// История: 3 строки, страницы по 2 (новые сверху).
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for i, kind := range []string{"disk_low", "mouse", "cpu_hot"} {
		e.exec(`INSERT INTO computer_health_events (club_id, computer_id, kind, raised_at, cleared_at, details)
			VALUES ($1, $2, $3::varchar, $4::timestamptz, CASE WHEN $3::varchar = 'disk_low' THEN $4::timestamptz + interval '1 hour' END, '{"temp_deci": 925}')`,
			e.club, pcNew, kind, base.Add(time.Duration(i)*time.Hour))
	}
	r = e.get(manager, fmt.Sprintf("/api/club-computers/%d/health/events?limit=2", pcNew))
	expect(t, r, http.StatusOK, "history page 1")
	items := r.body["items"].([]any)
	cursor, _ := r.body["next_cursor"].(string)
	if len(items) != 2 || items[0].(map[string]any)["kind"] != "cpu_hot" || items[1].(map[string]any)["kind"] != "mouse" || cursor == "" ||
		items[0].(map[string]any)["cleared_at"] != nil || num(items[0].(map[string]any)["details"].(map[string]any)["temp_deci"]) != 925 {
		t.Fatalf("page 1: %v", r.body)
	}
	r = e.get(manager, fmt.Sprintf("/api/club-computers/%d/health/events?limit=2&cursor=%s", pcNew, cursor))
	expect(t, r, http.StatusOK, "history page 2")
	items = r.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["kind"] != "disk_low" || items[0].(map[string]any)["cleared_at"] == nil || r.body["next_cursor"] != nil {
		t.Fatalf("page 2: %v", r.body)
	}
	if r := e.get(manager, fmt.Sprintf("/api/club-computers/%d/health/events?cursor=%%21", pcNew)); r.status != http.StatusBadRequest {
		t.Fatalf("bad cursor: %d", r.status)
	}

	// Права и тенантность: без club_map.view — 403; ПК другого клуба — 404 (клуб из аутентификации).
	for _, p := range []string{"/api/club-computers/%d/health", "/api/club-computers/%d/health/events"} {
		if r := e.get(noMap, fmt.Sprintf(p, pcNew)); r.status != http.StatusForbidden {
			t.Errorf("%s without club_map.view: %d", p, r.status)
		}
	}
	other, _ := newHealthEnv(t)
	_, otherManager := other.staff("Чужой", "manager")
	for _, p := range []string{"/api/club-computers/%d/health", "/api/club-computers/%d/health/events"} {
		if r := other.get(otherManager, fmt.Sprintf(p, pcNew)); r.status != http.StatusNotFound {
			t.Errorf("%s of a foreign computer: %d", p, r.status)
		}
	}
}
