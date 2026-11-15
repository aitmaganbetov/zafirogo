//go:build integration

package httpserver

import (
	"net/http"
	"testing"
	"time"
)

// PB-14: ПК формы «бронь нескольких ПК» проверяются одним запросом — ошибки те же и у тех же полей.
func TestReservationsManyChecksComputersAtOnce(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e, _ := newG4Env(t, now)
	_, admin := e.staff("Админ", "admin")
	_, pc := e.zoneWithPC("Зал", nil, 3)
	client, _ := e.member("Алия", "client", "", false)

	r := e.post(admin, "/api/reservations/many", map[string]any{
		"computer_ids": []int64{pc, 999999, pc, 888888}, "user_id": client, "starts_at": "2026-10-16T10:00", "duration_minutes": 120})
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("many: %d %v", r.status, r.body)
	}
	for field, want := range map[string]bool{"computer_ids.0": true, "computer_ids.1": true, "computer_ids.2": true, "computer_ids.3": true} {
		if got := fieldError(r, field) != ""; got != want {
			t.Errorf("%s: error %v, want %v (%v)", field, got, want, r.body)
		}
	}
	errs := obj(r.body["errors"])
	// .0 и .2 — повтор (distinct), .1 и .3 — нет такого ПК (exists): по одной ошибке на поле.
	for _, f := range []string{"computer_ids.0", "computer_ids.1", "computer_ids.2", "computer_ids.3"} {
		if list, _ := errs[f].([]any); len(list) != 1 {
			t.Errorf("%s: %v", f, errs[f])
		}
	}
	if fieldError(r, "computer_ids.0") == fieldError(r, "computer_ids.1") {
		t.Errorf("distinct and exists errors must differ: %v", errs)
	}
	ok := e.post(admin, "/api/reservations/many", map[string]any{"computer_ids": []int64{pc}, "user_id": client, "starts_at": "2026-10-16T10:00", "duration_minutes": 120})
	expect(t, ok, http.StatusCreated, "many")
}

// PB-09: «на смене» — одним запросом по всем ролям смены: кассир с живой сессией панели на смене, оператор без
// сессии и не-сменные роли — нет; счётчики ролей — как раньше.
func TestWorkersOnShiftAllRolesAtOnce(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	e, _ := newG4Env(t, now)
	_, admin := e.staff("Админ", "admin")
	cashier, _ := e.staff("Кассир", "cashier")
	cashier2, _ := e.staff("Кассир 2", "cashier")
	operator, _ := e.member("Оператор", "operator", "", false) // без входа в панель

	r := e.get(admin, "/api/workers")
	expect(t, r, http.StatusOK, "workers")
	items, _ := r.body["items"].([]any)
	onShift := map[int64]bool{}
	for _, it := range items {
		onShift[num(obj(it)["user_id"])] = obj(it)["on_shift"] == true
	}
	if !onShift[cashier] || !onShift[cashier2] || onShift[operator] {
		t.Fatalf("on_shift: %v (cashiers %d, %d; operator %d)", onShift, cashier, cashier2, operator)
	}
	for id, on := range onShift {
		if on && id != cashier && id != cashier2 {
			t.Errorf("user %d is on shift but has no shift role", id)
		}
	}
	counts := map[string]float64{}
	for _, l := range r.body["shift_limits"].([]any) {
		counts[obj(l)["role"].(string)] = obj(l)["count"].(float64)
	}
	if counts["cashier"] != 2 || counts["operator"] != 0 || len(counts) != 2 {
		t.Fatalf("shift_limits: %v", r.body["shift_limits"])
	}
}
