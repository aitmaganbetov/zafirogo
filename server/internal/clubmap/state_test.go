package clubmap

import (
	"errors"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

func ptr[T any](v T) *T { return &v }

// ClubComputer::currentStatus() / computerStates(): статус и подсветка, связь — по присутствию.
func TestStatusAndLight(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	cases := []struct {
		name          string
		pc            Computer
		status, light string
	}{
		{"free online", Computer{Status: "online", Online: true}, "online", "pc-free"},
		{"never seen available", Computer{Status: "available"}, "offline", ""},
		{"never seen restarting", Computer{Status: "restarting"}, "restarting", ""},
		{"seen, now offline", Computer{Status: "online", LastSeenAt: &now}, "offline", ""},
		{"powered off from panel", Computer{Status: "offline", Online: true}, "offline", ""},
		{"reserved flag", Computer{Status: "online", Online: true, IsReserved: true}, "reserved", ""},
		{"booked now", Computer{Status: "online", BookedNow: true}, "reserved", ""},
		{"account session", Computer{Status: "online", Online: true, SessionID: ptr("s"), SessionUserID: ptr(int64(5)), SessionEndsAt: &later}, "online", "pc-account"},
		{"guest session", Computer{Status: "online", Online: true, SessionID: ptr("s"), SessionGuest: true, SessionEndsAt: &later}, "online", "pc-guest"},
		{"expired session", Computer{Status: "online", Online: true, SessionID: ptr("s"), SessionGuest: true, SessionEndsAt: &now}, "online", "pc-free"},
		{"staff wins", Computer{Status: "online", Online: true, StaffUntil: &later, SessionID: ptr("s"), SessionGuest: true, SessionEndsAt: &later}, "online", "pc-staff"},
	}
	for _, c := range cases {
		if s, l := c.pc.CurrentStatus(), c.pc.Light(now); s != c.status || l != c.light {
			t.Errorf("%s: got %s/%q, want %s/%q", c.name, s, l, c.status, c.light)
		}
	}
}

func TestAgentVersions(t *testing.T) {
	for _, c := range []struct {
		v      *string
		latest string
		want   bool
	}{
		{ptr("2.4.1"), "2.5.0", true}, {ptr("2.5.0"), "2.5.0", false}, {ptr("2.10.0"), "2.9.9", false},
		{ptr("dev-build"), "2.5.0", true}, {nil, "2.5.0", false}, {ptr("1.0.0"), "", false}, {ptr("2.5.0-rc1"), "2.5.0", false},
	} {
		if got := IsOutdated(c.v, c.latest); got != c.want {
			t.Errorf("IsOutdated(%v, %q) = %v", c.v, c.latest, got)
		}
	}
	if !ValidLatest("1.2.3") || ValidLatest("1.2") || ValidLatest("1.2.3-rc") {
		t.Fatal("ValidLatest")
	}
}

func TestDetailsAndName(t *testing.T) {
	got := Details(Hardware{MAC: ptr("AA:BB:CC:DD:EE:01"), LocalIP: ptr("10.0.0.2"), CPUModel: ptr("Ryzen"), CPUCores: ptr(int32(8)), RAMTotalMB: ptr(int64(32768)), OSVersion: ptr("Win 11")})
	if want := "MAC AA:BB:CC:DD:EE:01 · LAN 10.0.0.2 · Ryzen · 8 cores · 32,768 MB RAM · Win 11"; got != want {
		t.Fatalf("details %q", got)
	}
	if DisplayName(nil, nil, 7) != "PC-07" || DisplayName(ptr(""), ptr("dev"), 7) != "dev" || DisplayName(ptr("VIP"), ptr("dev"), 7) != "VIP" {
		t.Fatal("display name")
	}
}

// Тексты ответа control() по-русски, формы trans_choice — как Laravel.
func TestControlMessages(t *testing.T) {
	tr := func(key string, params map[string]string) string { return i18n.Render("ru", key, params) }
	if got := ControlMessage(ActionRestart, ControlCounts{Queued: 5, Outdated: 0}, "", tr); got != "Перезагрузка: ПК получат команду в течение минуты (5 ПК)." {
		t.Fatal(got)
	}
	if got := ControlMessage(ActionPowerOff, ControlCounts{Queued: 2, Outdated: 2}, "3.1.0", tr); got != "Агент на этих ПК устарел и не выполнит команду: обновите его до версии 3.1.0 или новее." {
		t.Fatal(got)
	}
	if got := ControlMessage(ActionPowerOn, ControlCounts{Woken: 0}, "", tr); got != MsgWakeNoMac {
		t.Fatal(got)
	}
	if got := ControlMessage(ActionPowerOn, ControlCounts{Woken: 2, WithoutMAC: 1, Relays: 3}, "", tr); got != "Сигнал включения отправят другие ПК клуба (Wake-on-LAN): 2 ПК. Без MAC-адреса: 1." {
		t.Fatal(got)
	}
	if got := ControlMessage(ActionPowerOn, ControlCounts{Woken: 1, OldRelays: 2}, "", tr); got != "Включить некому: на включённых ПК клуба старый агент. Сигнал Wake-on-LAN отправляет агент версии 0.0.66 или новее — обновите агент хотя бы на одном ПК." {
		t.Fatal(got)
	}
	if got := NotifyMessage(ControlCounts{Queued: 21}, "", tr); got != "Уведомление появится на экране в течение минуты (21 ПК)." {
		t.Fatal(got)
	}
	if _, err := ValidateMessage("   "); err == nil {
		t.Fatal("blank message")
	}
	var e *Error
	if err := ValidateIDs([]int64{1, 1}); !errors.As(err, &e) || e.Field != "computer_ids.1" {
		t.Fatal("distinct")
	}
}
