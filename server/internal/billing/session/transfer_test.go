package session

import (
	"errors"
	"slices"
	"testing"
	"time"
)

const (
	zoneHall int64 = 1
	zoneVIP  int64 = 2
)

// GS-103 «a customer moves the rest of the session to a free PC where the tariff is valid» (BR-035).
func TestTransferGS103(t *testing.T) {
	now := t0 // 2026-10-15 10:00 в оригинале; важны только интервалы
	online := at(-10 * time.Second)
	from := Computer{ID: 1, ZoneID: zoneHall, LastSeenAt: online, SessionID: str("sess-1"), SessionUserID: i64(customer), SessionEndsAt: at(90 * time.Minute)}
	pcs := []struct {
		c       Computer
		booking bool
		want    bool
	}{
		{from, false, false},
		{Computer{ID: 2, ZoneID: zoneHall, LastSeenAt: online}, false, true},
		{Computer{ID: 3, ZoneID: zoneHall, LastSeenAt: online, SessionID: str("s3"), SessionUserID: i64(99), SessionEndsAt: at(time.Hour)}, false, false},
		{Computer{ID: 4, ZoneID: zoneHall, LastSeenAt: at(-91 * time.Second)}, false, false},
		{Computer{ID: 5, ZoneID: zoneVIP, LastSeenAt: online}, false, false},
		{Computer{ID: 6, ZoneID: zoneHall, LastSeenAt: online, IsReserved: true}, false, false},
		{Computer{ID: 7, ZoneID: zoneHall, LastSeenAt: online}, true, false}, // бронь 11:00–12:00 в остатке сессии
	}
	// Журнал: tariff.purchased {session_id sess-1}; тариф «Hall» только для Hall.
	zones := AllowedZones([]ZonePurchase{{ID: 1, Source: SourceTariff, TariffFound: true, TariffZoneIDs: []int64{zoneHall}}})
	if !slices.Equal(zones, []int64{zoneHall}) {
		t.Fatalf("zones = %v", zones)
	}
	t.Run("GS-103/карта", func(t *testing.T) {
		for _, p := range pcs {
			if got := ListedAsFree(p.c, from.ID, zones, p.booking, now); got != p.want {
				t.Errorf("ПК %d available = %v, want %v", p.c.ID, got, p.want)
			}
		}
	})
	t.Run("GS-103/пересадка", func(t *testing.T) {
		for _, p := range pcs[2:] {
			target := p.c
			if _, err := PlanTransfer(from, &target, customer, zones, p.booking, now); !errors.Is(err, ErrTransferUnavailable) {
				t.Errorf("ПК %d: err = %v", p.c.ID, err)
			}
		}
		if _, err := PlanTransfer(from, &pcs[1].c, 99, zones, false, now); !errors.Is(err, ErrTransferNotOwnSession) {
			t.Errorf("чужой клиент: %v", err)
		}
		if _, err := PlanTransfer(from, &from, customer, zones, false, now); !errors.Is(err, ErrTransferSameOrMissing) {
			t.Errorf("тот же ПК: %v", err)
		}
		if _, err := PlanTransfer(from, nil, customer, zones, false, now); !errors.Is(err, ErrTransferSameOrMissing) {
			t.Errorf("нет ПК: %v", err)
		}
		tr, err := PlanTransfer(from, &pcs[1].c, customer, zones, false, now)
		if err != nil {
			t.Fatal(err)
		}
		want := Transfer{SessionID: "sess-1", UserID: customer, SessionEndsAt: *at(90 * time.Minute), SecondsMoved: 5400, ClearFrom: true}
		if tr != want {
			t.Fatalf("transfer = %+v, want %+v", tr, want)
		}
	})
}

// GAP-035 — поминутное время пересаживается только в своей зоне.
func TestTransferGAP035(t *testing.T) {
	from := Computer{ID: 1, ZoneID: zoneHall, LastSeenAt: at(0), SessionID: str("s"), SessionUserID: i64(customer), SessionEndsAt: at(time.Hour)}
	zones := AllowedZones([]ZonePurchase{{ID: 1, Source: SourceMinutes, ZoneID: zoneHall}})
	vip := Computer{ID: 20, ZoneID: zoneVIP, LastSeenAt: at(0)}
	hall := Computer{ID: 21, ZoneID: zoneHall, LastSeenAt: at(0)}
	t.Run("GAP-035", func(t *testing.T) {
		if _, err := PlanTransfer(from, &vip, customer, zones, false, t0); !errors.Is(err, ErrTransferUnavailable) {
			t.Errorf("VIP: %v", err)
		}
		if _, err := PlanTransfer(from, &hall, customer, zones, false, t0); err != nil {
			t.Errorf("Hall: %v", err)
		}
	})
}

// GAP-036 — после пересадки возврат считается по той же сессии: 30 мин из 60 за 600.00 → 300.00.
func TestTransferGAP036(t *testing.T) {
	from := Computer{ID: 1, ZoneID: zoneHall, LastSeenAt: at(0), SessionID: str("s"), SessionUserID: i64(customer), SessionEndsAt: at(time.Hour)}
	target := Computer{ID: 30, ZoneID: zoneHall, LastSeenAt: at(10 * time.Minute)}
	t.Run("GAP-036", func(t *testing.T) {
		tr, err := PlanTransfer(from, &target, customer, nil, false, t0.Add(10*time.Minute))
		if err != nil || tr.SessionID != "s" || !tr.SessionEndsAt.Equal(t0.Add(time.Hour)) || tr.SecondsMoved != 3000 {
			t.Fatalf("= %+v, %v", tr, err)
		}
		got, err := Settle([]Purchase{minutesPart(1, 60, 60000, 0)}, customer, &tr.SessionEndsAt, t0.Add(30*time.Minute))
		if err != nil || got.RefundMain != 30000 {
			t.Fatalf("= %+v, %v", got, err)
		}
	})
}

func TestAllowedZones(t *testing.T) {
	tests := []struct {
		name string
		in   []ZonePurchase
		want []int64
	}{
		{"нет-покупок", nil, nil},
		{"только-пакет", []ZonePurchase{{ID: 1, Source: SourcePackage, TariffFound: true, TariffZoneIDs: []int64{zoneVIP}}}, nil},
		{"пакет-после-поминутки", []ZonePurchase{{ID: 1, Source: SourceMinutes, ZoneID: zoneHall}, {ID: 2, Source: SourcePackage, TariffFound: true, TariffZoneIDs: []int64{zoneVIP}}}, []int64{zoneHall}},
		{"последняя-поминутка", []ZonePurchase{{ID: 1, Source: SourceTariff, TariffFound: true, TariffZoneIDs: []int64{zoneVIP}}, {ID: 2, Source: SourceMinutes, ZoneID: zoneHall}}, []int64{zoneHall}},
		{"последний-тариф", []ZonePurchase{{ID: 2, Source: SourceTariff, TariffFound: true, TariffZoneIDs: []int64{zoneVIP, 3}}, {ID: 1, Source: SourceMinutes, ZoneID: zoneHall}}, []int64{zoneVIP, 3}},
		{"тариф-без-зон", []ZonePurchase{{ID: 1, Source: SourceTariff, TariffFound: true}}, nil},
		{"тариф-удалён", []ZonePurchase{{ID: 1, Source: SourceTariff, TariffFound: false, TariffZoneIDs: []int64{zoneVIP}}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllowedZones(tc.in); !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Errorf("= %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFreeEdges(t *testing.T) {
	// connectionStatus(): last_seen_at >= now − 90 с с микросекундами now; список (SQL) сравнивает с усечённым now.
	last := t0
	c := Computer{ID: 2, LastSeenAt: &last}
	now := t0.Add(90*time.Second + 500*time.Millisecond)
	if c.Online(now) {
		t.Error("transfer: 90.5 с — офлайн")
	}
	if !ListedAsFree(c, 1, nil, false, now) {
		t.Error("список: усечённый now — ещё онлайн")
	}
	staff := Computer{ID: 2, LastSeenAt: at(0), StaffUntil: at(time.Minute)}
	if FreeForTransfer(staff, false, t0) || ListedAsFree(staff, 1, nil, false, t0) {
		t.Error("визит сотрудника занимает ПК")
	}
	// Старый агент: current_session_id без конца — свободен в обоих условиях.
	oldAgent := Computer{ID: 2, LastSeenAt: at(0), SessionID: str("x")}
	if !FreeForTransfer(oldAgent, false, t0) || !ListedAsFree(oldAgent, 1, nil, false, t0) {
		t.Error("сессия без оплаты не занимает ПК")
	}
	// Истёкшая, но не снятая сессия — ПК свободен.
	expired := Computer{ID: 2, LastSeenAt: at(0), SessionID: str("x"), SessionUserID: i64(9), SessionEndsAt: at(-time.Second)}
	if !FreeForTransfer(expired, false, t0) || !ListedAsFree(expired, 1, nil, false, t0) {
		t.Error("истёкшая сессия не занимает ПК")
	}
}
