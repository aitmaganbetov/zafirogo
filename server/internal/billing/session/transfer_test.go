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
	zones := AllowedZones([]ZonePurchase{{ID: 1, Source: SourceTariff, TariffFound: true, TariffZoneIDs: []int64{zoneHall}}}, nil)
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
	zones := AllowedZones([]ZonePurchase{{ID: 1, Source: SourceMinutes, ZoneID: zoneHall}}, nil)
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
			if got := AllowedZones(tc.in, nil); !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Errorf("= %v, want %v", got, tc.want)
			}
		})
	}
}

// GS-574 «a tariff moves to a zone of the same or a lower level, time by the minute stays in its zone» (BR-196,
// Zafiro 79d014f, 55e3878): тариф действует в VIP1 (уровень 3) — свободны VIP2 (3) и Hall (1), не Top (4) и не зона
// без уровня; поминутное время, купленное в VIP1 последним, остаётся в VIP1 при любом уровне.
func TestAllowedZonesLevelsGS574(t *testing.T) {
	const (
		vip1  int64 = 11 // уровень 3, ПК клиента
		vip2  int64 = 12 // уровень 3
		hall  int64 = 13 // уровень 1
		top   int64 = 14 // уровень 4
		plain int64 = 15 // без уровня
	)
	// ListLevelZoneIDs(VIP1): level <= 3.
	levelZones := []int64{vip1, vip2, hall}
	tariff := ZonePurchase{ID: 1, Source: SourceTariff, TariffFound: true, TariffZoneIDs: []int64{vip1}}
	minutes := ZonePurchase{ID: 2, Source: SourceMinutes, ZoneID: vip1}
	online := at(-10 * time.Second)
	from := Computer{ID: 1, ZoneID: vip1, LastSeenAt: online, SessionID: str("sess-1"), SessionUserID: i64(customer), SessionEndsAt: at(time.Hour)}
	pcs := []Computer{from,
		{ID: 2, ZoneID: vip2, LastSeenAt: online},
		{ID: 3, ZoneID: hall, LastSeenAt: online},
		{ID: 4, ZoneID: top, LastSeenAt: online},
		{ID: 5, ZoneID: plain, LastSeenAt: online},
	}
	tests := []struct {
		name      string
		purchases []ZonePurchase
		want      map[int64]bool // ПК → available
	}{
		{"GS-574/тариф-тот-же-и-ниже-уровень", []ZonePurchase{tariff}, map[int64]bool{1: false, 2: true, 3: true, 4: false, 5: false}},
		{"GS-574/поминутка-только-своя-зона", []ZonePurchase{tariff, minutes}, map[int64]bool{1: false, 2: false, 3: false, 4: false, 5: false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			zones := AllowedZones(tc.purchases, levelZones)
			for _, pc := range pcs {
				if got := ListedAsFree(pc, from.ID, zones, false, t0); got != tc.want[pc.ID] {
					t.Errorf("карта: ПК %d available = %v, want %v", pc.ID, got, tc.want[pc.ID])
				}
				if pc.ID == from.ID {
					continue
				}
				target := pc
				_, err := PlanTransfer(from, &target, customer, zones, false, t0)
				if ok := err == nil; ok != tc.want[pc.ID] {
					t.Errorf("пересадка: ПК %d err = %v, want ok %v", pc.ID, err, tc.want[pc.ID])
				}
				if err != nil && !errors.Is(err, ErrTransferUnavailable) {
					t.Errorf("пересадка: ПК %d err = %v", pc.ID, err)
				}
			}
		})
	}
}

// BR-196: зоны уровня добавляются только к зонам тарифа; тариф без зон или удалённый — по-прежнему любая зона,
// у текущей зоны нет уровня (levelZones пуст) — только зоны тарифа.
func TestAllowedZonesLevels(t *testing.T) {
	tariff := func(zones ...int64) ZonePurchase {
		return ZonePurchase{ID: 1, Source: SourceTariff, TariffFound: true, TariffZoneIDs: zones}
	}
	tests := []struct {
		name   string
		in     []ZonePurchase
		levels []int64
		want   []int64
	}{
		{"BR-196/зона-без-уровня", []ZonePurchase{tariff(zoneVIP)}, nil, []int64{zoneVIP}},
		{"BR-196/объединение-без-повторов", []ZonePurchase{tariff(zoneVIP, 3)}, []int64{zoneHall, zoneVIP}, []int64{zoneVIP, 3, zoneHall}},
		{"BR-196/тариф-без-зон", []ZonePurchase{tariff()}, []int64{zoneHall}, nil},
		{"BR-196/тариф-удалён", []ZonePurchase{{ID: 1, Source: SourceTariff, TariffZoneIDs: []int64{zoneVIP}}}, []int64{zoneHall}, nil},
		{"BR-196/нет-покупок", nil, []int64{zoneHall}, nil},
		{"BR-196/поминутка", []ZonePurchase{{ID: 1, Source: SourceMinutes, ZoneID: zoneVIP}}, []int64{zoneHall, zoneVIP}, []int64{zoneVIP}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllowedZones(tc.in, tc.levels); !slices.Equal(got, tc.want) || (got == nil) != (tc.want == nil) {
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
