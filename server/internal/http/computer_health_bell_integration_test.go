//go:build integration

package httpserver

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
)

// Колокольчик «Проблемы с ПК» (S7-H-11): переход в журнале здоровья (health.Journal.Record) → notifications.changed
// в outbox (только на реальный переход) и пункт в GET /api/notifications своего клуба для ролей, которые видят
// «Не на связи»; снятая проблема — пункта нет; повторное появление после «прочитано» — снова непрочитано.
func TestNotificationsComputerHealth(t *testing.T) {
	ctx := context.Background()
	e, _ := newHealthEnv(t)
	other, _ := newHealthEnv(t)
	_, manager := e.staff("Менеджер", "manager")
	_, operator := e.staff("Оператор", "operator")
	_, cashier := e.staff("Кассир", "cashier")
	_, otherManager := other.staff("Чужой менеджер", "manager")
	zone := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Зал', now(), now()) RETURNING id", e.club)
	pc := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, created_at, updated_at) VALUES ($1, $2, 7, now(), now()) RETURNING id", e.club, zone)
	pc2 := e.scalar("INSERT INTO club_computers (club_id, club_zone_id, number, display_name, created_at, updated_at) VALUES ($1, $2, 8, 'VIP-08', now(), now()) RETURNING id", e.club, zone)

	j := health.NewJournal(e.db.App)
	record := func(computer int64, at time.Time, trs ...health.Transition) {
		t.Helper()
		if err := j.Record(ctx, e.club, computer, trs, at); err != nil {
			t.Fatal(err)
		}
	}
	bell := func(tok string) (map[string]map[string]any, int64) { t.Helper(); return healthBell(e, tok) }
	// Пункты — по ссылке на карту (id меняется с каждой новой проблемой ПК).
	pcID, pc2ID := "/club-map?computer="+strconv.FormatInt(pc, 10), "/club-map?computer="+strconv.FormatInt(pc2, 10)

	before := e.outboxCount("notifications.changed")
	t0 := time.Now().UTC().Add(-time.Minute)
	record(pc, t0,
		health.Transition{Kind: health.KindMouse, Raised: true},
		health.Transition{Kind: health.KindCPUHot, Raised: true, Details: map[string]any{"temp_deci": 915, "threshold_c": 90}})
	if got := e.outboxCount("notifications.changed"); got != before+1 {
		t.Fatalf("raise: notifications.changed %d → %d, want +1 per transition batch", before, got)
	}
	// Повтор того же перехода (строки нет) — ни строки, ни события.
	record(pc, t0.Add(time.Second), health.Transition{Kind: health.KindMouse, Raised: true})
	if got := e.outboxCount("notifications.changed"); got != before+1 {
		t.Fatalf("repeat without a row must not notify: %d", got)
	}
	record(pc2, t0.Add(2*time.Second), health.Transition{Kind: health.KindDiskSmart, Raised: true, Details: map[string]any{"disks": []any{}}})

	items, unread := bell(manager)
	it := items[pcID]
	if it == nil || it["title"] != "Проблемы с ПК PC-07" || it["body"] != "Перегрев CPU (92 °C), Мышь не подключена" ||
		it["url"] != "/club-map?computer="+strconv.FormatInt(pc, 10) || it["icon"] != "fa-triangle-exclamation" || it["unread"] != true {
		t.Fatalf("manager bell: %v", items)
	}
	if it2 := items[pc2ID]; it2 == nil || it2["title"] != "Проблемы с ПК VIP-08" || it2["body"] != "Диск неисправен (SMART)" {
		t.Fatalf("second PC: %v", items)
	}
	if unread < 2 {
		t.Fatalf("unread_count %v", unread)
	}
	if items, _ := bell(operator); items[pcID] == nil {
		t.Fatalf("operator (sees «Не на связи») must see PC problems: %v", items)
	}
	if items, _ := bell(cashier); len(items) != 0 {
		t.Fatalf("cashier must not see PC problems: %v", items)
	}
	if items, _ := healthBell(other, otherManager); len(items) != 0 {
		t.Fatalf("other club sees our PC problems: %v", items)
	}

	firstID := it["id"]

	// Прочитано — пункт остаётся, но не новый.
	expect(t, e.post(manager, "/api/notifications/seen", nil), http.StatusOK, "seen")
	if items, _ := bell(manager); items[pcID]["unread"] != false || items[pcID]["id"] != firstID {
		t.Fatalf("after seen: %v", items[pcID])
	}

	// Снята одна из двух — пункт с оставшейся; снята последняя — пункта нет (событие на каждый переход).
	n := e.outboxCount("notifications.changed")
	record(pc, time.Now().UTC(), health.Transition{Kind: health.KindCPUHot})
	if items, _ := bell(manager); items[pcID]["body"] != "Мышь не подключена" {
		t.Fatalf("after clearing cpu_hot: %v", items[pcID])
	}
	record(pc, time.Now().UTC(), health.Transition{Kind: health.KindMouse})
	if items, _ := bell(manager); items[pcID] != nil || items[pc2ID] == nil {
		t.Fatalf("after clearing all problems of the PC: %v", items)
	}
	if got := e.outboxCount("notifications.changed"); got != n+2 {
		t.Fatalf("clear: notifications.changed %d → %d, want +2", n, got)
	}
	// Снятие без открытой строки — без события.
	record(pc, time.Now().UTC(), health.Transition{Kind: health.KindMouse})
	if got := e.outboxCount("notifications.changed"); got != n+2 {
		t.Fatalf("clear without a row must not notify: %d", got)
	}

	// Та же проблема снова после «прочитано» — снова непрочитано.
	time.Sleep(10 * time.Millisecond)
	record(pc, time.Now().UTC(), health.Transition{Kind: health.KindMouse, Raised: true})
	if items, _ := bell(manager); items[pcID] == nil || items[pcID]["unread"] != true || items[pcID]["body"] != "Мышь не подключена" ||
		items[pcID]["id"] == firstID {
		t.Fatalf("raised again (new id — the bell chimes again): %v", items[pcID])
	}
}

// healthBell — пункты «Проблемы с ПК» колокольчика сотрудника окружения (по id) и unread_count.
func healthBell(e *adminEnv, tok string) (map[string]map[string]any, int64) {
	e.t.Helper()
	r := e.get(tok, "/api/notifications")
	expect(e.t, r, http.StatusOK, "notifications")
	out := map[string]map[string]any{}
	for _, it := range r.body["items"].([]any) {
		if id, _ := obj(it)["id"].(string); strings.HasPrefix(id, "computer-health-") {
			out[obj(it)["url"].(string)] = obj(it)
		}
	}
	return out, num(r.body["unread_count"])
}
