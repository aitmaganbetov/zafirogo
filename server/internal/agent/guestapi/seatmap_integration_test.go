//go:build integration

package guestapi_test

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// BC-03 / решение GG-6: GetSeatMap — карта пересадки (SeatTransfers::map()): зоны, все ПК и признак «можно пересесть»
// по тем же правилам, что TransferSeat; «в сети» — из присутствия агента (открытый поток), а не по last_seen_at.
func TestGetSeatMap(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: `{"guests": {"allow_guest_self_transfer": true}}`})
	phone := uniqPhone()
	user := h.customer("Арман", phone, "password", 200000, 0)
	other := h.customer("Сосед", uniqPhone(), "password", 0, 0)
	own, free, offline, reserved, booked, busy, staffed, otherZone :=
		h.enrollPC(), h.enrollPC(), h.enrollPC(), h.enrollPC(), h.enrollPC(), h.enrollPC(), h.enrollPC(), h.enrollPC()
	// В сети — все, кроме offline (поток агента открыт → club_computers.status = 'online').
	for i, p := range []*pc{own, free, reserved, booked, busy, staffed, otherZone} {
		p.connect("boot-seat-"+string(rune('a'+i)), 0)
	}
	// Состояние idle снимает отметку сотрудника — дождаться его записи до staff_until (иначе гонка).
	h.waitAgentState(staffed.id, "idle")
	vip := h.int(`INSERT INTO club_zones (club_id, name, price_per_hour, icon, theme, sort_order, position_x, position_y, width, height, created_at, updated_at)
		VALUES ($1, 'VIP', 90000, 'crown', 'purple', 1, 400, 30, 300, 200, now(), now()) RETURNING id`, h.club)
	h.exec("UPDATE club_computers SET club_zone_id = $2 WHERE id = $1", otherZone.id, vip)
	h.exec("UPDATE club_computers SET position_x = 11, position_y = 22, display_name = 'Свободный' WHERE id = $1", free.id)
	h.exec("UPDATE club_computers SET is_reserved = true WHERE id = $1", reserved.id)
	h.exec("UPDATE club_computers SET staff_until = now() + interval '10 minutes' WHERE id = $1", staffed.id)
	h.exec(`UPDATE club_computers SET current_session_id = gen_random_uuid()::text, current_session_user_id = $2,
		session_ends_at = now() + interval '30 minutes' WHERE id = $1`, busy.id, other)
	// Чужая бронь через 20 минут — пересекает остаток сессии (60 мин) + интервал между бронями.
	h.exec(`INSERT INTO computer_reservations (club_id, club_computer_id, user_id, starts_at, ends_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now())`, h.club, booked.id, other, h.clock.Now().Add(20*time.Minute), h.clock.Now().Add(80*time.Minute))
	// Своя бронь клиента на свободном ПК не мешает (ComputerReservations::blocking()).
	h.exec(`INSERT INTO computer_reservations (club_id, club_computer_id, user_id, starts_at, ends_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now())`, h.club, free.id, user, h.clock.Now().Add(20*time.Minute), h.clock.Now().Add(80*time.Minute))
	ctx := withToken(own.mustLogin(phone, "password"))

	// Без своей оплаченной сессии — ensureOwnSession(): «На этом ПК нет вашей оплаченной сессии.»
	_, err := own.guest().GetSeatMap(ctx, &agentv1.GetSeatMapRequest{})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_NO_ACTIVE_SESSION)

	// Время по минутам действует только в своей зоне (allowedZoneIds(): time.purchased → зона покупки).
	if _, err := own.guest().PurchaseMinutes(ctx, &agentv1.PurchaseMinutesRequest{IdempotencyKey: key(), Minutes: 60,
		BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN}); err != nil {
		t.Fatal(err)
	}
	m, err := own.guest().GetSeatMap(ctx, &agentv1.GetSeatMapRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if m.GetCurrentComputerId() != own.id || len(m.GetZones()) != 2 || len(m.GetSeats()) != 8 {
		t.Fatalf("map: current %d, zones %d, seats %d", m.GetCurrentComputerId(), len(m.GetZones()), len(m.GetSeats()))
	}
	// Зоны — по sort_order, id; поля карты как в SeatTransfers::map().
	if z := m.GetZones()[0]; z.GetZoneId() != h.zone || z.GetName() != "Hall" || z.GetIcon() != "desktop" || z.GetTheme() != "neutral" ||
		z.GetX() != 20 || z.GetY() != 20 || z.GetWidth() != 320 || z.GetHeight() != 180 {
		t.Fatalf("zone 0 = %+v", z)
	}
	if z := m.GetZones()[1]; z.GetZoneId() != vip || z.GetName() != "VIP" || z.GetIcon() != "crown" || z.GetTheme() != "purple" ||
		z.GetX() != 400 || z.GetY() != 30 || z.GetWidth() != 300 || z.GetHeight() != 200 {
		t.Fatalf("zone 1 = %+v", z)
	}
	want := map[int64]bool{own.id: false, free.id: true, offline.id: false, reserved.id: false, booked.id: false,
		busy.id: false, staffed.id: false, otherZone.id: false}
	names := map[int64]string{own.id: "свой ПК", free.id: "свободный", offline.id: "не в сети", reserved.id: "зарезервирован",
		booked.id: "чужая бронь", busy.id: "чужая сессия", staffed.id: "сотрудник", otherZone.id: "другая зона"}
	var prevZone, prevNumber int64
	for _, s := range m.GetSeats() {
		id := s.GetComputer().GetComputerId()
		if avail, ok := want[id]; !ok || s.GetAvailable() != avail {
			t.Errorf("%s (pc %d): available = %v, want %v", names[id], id, s.GetAvailable(), avail)
		}
		// Порядок: зона, номер.
		var number int64
		h.scan(&number, "SELECT number FROM club_computers WHERE id = $1", id)
		if s.GetZoneId() < prevZone || (s.GetZoneId() == prevZone && number < prevNumber) {
			t.Errorf("seats are not ordered by zone and number at pc %d", id)
		}
		prevZone, prevNumber = s.GetZoneId(), number
		if id == free.id && (s.GetComputer().GetDisplayName() != "Свободный" || s.GetX() != 11 || s.GetY() != 22 ||
			s.GetZoneId() != h.zone || s.GetComputer().GetZoneName() != "Hall") {
			t.Errorf("free seat = %+v", s)
		}
		if id == otherZone.id && (s.GetZoneId() != vip || s.GetComputer().GetZoneName() != "VIP") {
			t.Errorf("vip seat = %+v", s)
		}
	}

	// Карта и пересадка согласованы: на «доступный» пересадка проходит, на «недоступный» — отказ.
	for _, p := range []*pc{offline, reserved, booked, busy, staffed, otherZone} {
		_, err := own.guest().TransferSeat(ctx, &agentv1.TransferSeatRequest{IdempotencyKey: key(), TargetComputerId: p.id})
		wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SEAT_UNAVAILABLE)
	}
	if _, err := own.guest().TransferSeat(ctx, &agentv1.TransferSeatRequest{IdempotencyKey: key(), TargetComputerId: free.id}); err != nil {
		t.Fatalf("transfer to the seat the map offered: %v", err)
	}
	// Сессия уехала — на прежнем ПК карты больше нет.
	_, err = own.guest().GetSeatMap(ctx, &agentv1.GetSeatMapRequest{})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_NO_ACTIVE_SESSION)

	// Функция выключена в клубе — GuestFeatures::ensure('self_transfer').
	h.exec(`UPDATE clubs SET settings = '{"guests": {"allow_guest_self_transfer": false}}' WHERE id = $1`, h.club)
	_, err = own.guest().GetSeatMap(ctx, &agentv1.GetSeatMapRequest{})
	wantReason(t, err, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_FEATURE_DISABLED)
}

// GS-574 (BR-196, Zafiro 79d014f, 55e3878; GuestFeaturesTest.php «a tariff moves to a zone of the same or a lower
// level, time by the minute stays in its zone»): тариф действует только в VIP1 (уровень 3) — карта и пересадка
// открывают VIP2 (3) и Hall (1), но не Top (4) и не зону без уровня; после покупки минут в VIP1 — только VIP1.
func TestSeatMapZoneLevelsGS574(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true, settings: `{"guests": {"allow_guest_self_transfer": true}}`})
	phone := uniqPhone()
	h.customer("Арман", phone, "password", 500000, 0)
	zone := func(name string, level any, price any) int64 {
		return h.int(`INSERT INTO club_zones (club_id, name, level, price_per_hour, created_at, updated_at)
			VALUES ($1, $2, $3, $4, now(), now()) RETURNING id`, h.club, name, level, price)
	}
	vip1, vip2, hall, top := zone("VIP1", 3, 60000), zone("VIP2", 3, nil), zone("Hall1", 1, nil), zone("Top", 4, nil)
	from, inVip2, inHall, inTop, inPlain := h.enrollPC(), h.enrollPC(), h.enrollPC(), h.enrollPC(), h.enrollPC()
	for i, p := range []*pc{from, inVip2, inHall, inTop, inPlain} {
		p.connect("boot-level-"+string(rune('a'+i)), 0)
	}
	for p, z := range map[*pc]int64{from: vip1, inVip2: vip2, inHall: hall, inTop: top} {
		h.exec("UPDATE club_computers SET club_zone_id = $2 WHERE id = $1", p.id, z)
	}
	// inPlain остаётся в зоне харнеса «Hall» — без уровня.
	tariff := h.int(`INSERT INTO tariffs (club_id, name, duration_minutes, price, currency, payment_method, is_active, created_at, updated_at)
		VALUES ($1, 'VIP1', 120, 10000, 'KZT', 'main_or_bonus', true, now(), now()) RETURNING id`, h.club)
	h.exec("INSERT INTO club_zone_tariff (club_zone_id, tariff_id, club_id) VALUES ($1, $2, $3)", vip1, tariff, h.club)
	ctx := withToken(from.mustLogin(phone, "password"))
	if _, err := from.guest().PurchaseTariff(ctx, &agentv1.PurchaseTariffRequest{IdempotencyKey: key(), TariffId: tariff,
		BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN}); err != nil {
		t.Fatal(err)
	}
	check := func(name string, want map[int64]bool) {
		t.Helper()
		m, err := from.guest().GetSeatMap(ctx, &agentv1.GetSeatMapRequest{})
		if err != nil {
			t.Fatal(err)
		}
		got := map[int64]bool{}
		for _, s := range m.GetSeats() {
			if _, ok := want[s.GetComputer().GetComputerId()]; ok {
				got[s.GetComputer().GetComputerId()] = s.GetAvailable()
			}
		}
		for id, w := range want {
			if got[id] != w {
				t.Errorf("%s: pc %d available = %v, want %v", name, id, got[id], w)
			}
		}
		// Карта и пересадка согласованы: недоступный ПК — отказ.
		for id, w := range want {
			if w || id == from.id {
				continue
			}
			_, err := from.guest().TransferSeat(ctx, &agentv1.TransferSeatRequest{IdempotencyKey: key(), TargetComputerId: id})
			wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SEAT_UNAVAILABLE)
		}
	}
	t.Run("GS-574/тариф", func(t *testing.T) {
		check("тариф", map[int64]bool{from.id: false, inVip2.id: true, inHall.id: true, inTop.id: false, inPlain.id: false})
	})
	t.Run("GS-574/поминутно", func(t *testing.T) {
		if _, err := from.guest().PurchaseMinutes(ctx, &agentv1.PurchaseMinutesRequest{IdempotencyKey: key(), Minutes: 10,
			BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN}); err != nil {
			t.Fatal(err)
		}
		check("поминутно", map[int64]bool{from.id: false, inVip2.id: false, inHall.id: false, inTop.id: false, inPlain.id: false})
	})
	t.Run("GS-574/уровень-текущей-зоны-снят", func(t *testing.T) {
		// Ещё один тариф последним: зона ПК клиента без уровня — только зоны тарифа.
		if _, err := from.guest().PurchaseTariff(ctx, &agentv1.PurchaseTariffRequest{IdempotencyKey: key(), TariffId: tariff,
			BalanceSource: agentv1.BalanceSource_BALANCE_SOURCE_MAIN}); err != nil {
			t.Fatal(err)
		}
		check("тариф, уровни", map[int64]bool{inVip2.id: true, inHall.id: true, inTop.id: false})
		h.exec("UPDATE club_zones SET level = NULL WHERE id = $1", vip1)
		check("тариф, без уровня", map[int64]bool{inVip2.id: false, inHall.id: false, inTop.id: false, inPlain.id: false})
		// Пересадка на открытый уровнем ПК проходит.
		h.exec("UPDATE club_zones SET level = 3 WHERE id = $1", vip1)
		if _, err := from.guest().TransferSeat(ctx, &agentv1.TransferSeatRequest{IdempotencyKey: key(), TargetComputerId: inHall.id}); err != nil {
			t.Fatalf("transfer to a lower level: %v", err)
		}
	})
}
