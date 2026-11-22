package session

import (
	"slices"
	"time"
)

// Ошибки пересадки (тексты — ключи t() старого кода).
var (
	ErrTransferSameOrMissing = &Error{Field: "target_computer_id", Message: "Выберите другой свободный ПК."}
	ErrTransferNotOwnSession = &Error{Field: "computer_id", Message: "На этом ПК нет вашей оплаченной сессии."}
	ErrTransferUnavailable   = &Error{Field: "target_computer_id", Message: "Этот ПК уже занят или ваш тариф на нём не действует. Выберите другой."}
)

// PurchaseSource — откуда взялась часть сессии. В старом коде зону пересадки определял журнал
// действий: учитывались только события tariff.purchased и time.purchased; запуск пакета
// (time.package_started) в выборку не попадал.
type PurchaseSource string

// Источники части сессии.
const (
	SourceTariff  PurchaseSource = "tariff"  // tariff.purchased
	SourceMinutes PurchaseSource = "minutes" // time.purchased
	SourcePackage PurchaseSource = "package" // time.package_started — для зон пересадки не учитывается
	// SourceBooking — оплаченное время брони из приложения (ComputerReservations::startPaidSession()): в журнале
	// старого кода это reservation.arrived, а не покупка, — для зон пересадки не учитывается.
	SourceBooking PurchaseSource = "booking"
)

// ZonePurchase — покупка сессии для выбора зон пересадки.
type ZonePurchase struct {
	ID     int64 // порядок: берётся последняя (максимальный ID)
	Source PurchaseSource
	// ZoneID — зона, в которой куплено поминутное время (subject time.purchased).
	ZoneID int64
	// TariffFound — тариф существует и НЕ удалён мягко: старый код искал Tariff::find() без withTrashed.
	TariffFound   bool
	TariffZoneIDs []int64 // зоны тарифа; пусто — все
}

// AllowedZones — зоны, куда можно пересесть; nil — любая зона. levelZoneIDs — зоны клуба с уровнем не выше
// уровня текущей зоны клиента (store.ListLevelZoneIDs); у текущей зоны нет уровня — пусто.
//
// Laravel: app/Services/SeatTransfers.php allowedZoneIds() (стр. 136–157), BR-035, BR-196:
// последняя по id запись журнала tariff.purchased|time.purchased этой сессии; поминутное время —
// только своя зона; тариф не найден или без зон — любая; иначе зоны тарифа ∪ зоны уровня
// (array_values(array_unique([...зоны тарифа, ...зоны уровня])), Zafiro 79d014f, 55e3878).
// Решение Q-007/OQ-BR-04 (docs/DECISIONS.md) — источник не журнал, а покупки сессии; смысл тот же, пакеты
// не учитываются.
func AllowedZones(purchases []ZonePurchase, levelZoneIDs []int64) []int64 {
	last := lastZonePurchase(purchases)
	if last == nil {
		return nil
	}
	if last.Source == SourceMinutes {
		return []int64{last.ZoneID}
	}
	if !last.TariffFound || len(last.TariffZoneIDs) == 0 {
		return nil
	}
	zones := slices.Clone(last.TariffZoneIDs)
	for _, id := range levelZoneIDs {
		if !slices.Contains(zones, id) {
			zones = append(zones, id)
		}
	}
	return zones
}

// FreeForTransfer — целевой ПК свободен для пересадки (проверка внутри transfer()).
//
// Laravel: SeatTransfers::transfer() стр. 95–99: !is_reserved && online && !hasPaidSession &&
// !hasStaffVisit && нет чужой брони до конца сессии + интервал (blockedByBooking считает вызывающий,
// ComputerReservations::blocking()).
func FreeForTransfer(target Computer, blockedByBooking bool, now time.Time) bool {
	return !target.IsReserved && target.Online(now) && !target.HasPaidSession(now) &&
		!target.HasStaffVisit(now) && !blockedByBooking
}

// ListedAsFree — ПК показывается доступным на карте пересадки (freeSeatsFor()).
//
// Laravel: SeatTransfers::freeSeatsFor() стр. 31–42. Условие слегка отличается от transfer():
// сессия проверяется только по session_ends_at (null или <= now), а не hasPaidSession; онлайн —
// SQL `last_seen_at >= now() - 90 c` с now, усечённым до секунды. Повторяем как есть.
//
// Решение GG-6 / AS-2: при Connected != nil «в сети» берётся из присутствия агента (как в Online() и
// TransferSeat) — last_seen_at теперь пишется только при смене состояния.
func ListedAsFree(c Computer, fromID int64, zones []int64, blockedByBooking bool, now time.Time) bool {
	floorNow := StoredTime(now)
	online := c.LastSeenAt != nil && !c.LastSeenAt.Before(floorNow.Add(-HeartbeatTimeout))
	if c.Connected != nil {
		online = *c.Connected
	}
	return c.ID != fromID &&
		!c.IsReserved &&
		online &&
		(c.SessionEndsAt == nil || !c.SessionEndsAt.After(floorNow)) &&
		(c.StaffUntil == nil || !c.StaffUntil.After(floorNow)) &&
		(zones == nil || slices.Contains(zones, c.ZoneID)) &&
		!blockedByBooking
}

// Transfer — план пересадки: какие поля записать на оба ПК.
type Transfer struct {
	SessionID     string
	UserID        int64
	SessionEndsAt time.Time // тот же конец: переносится весь остаток, денег не списывается
	// SecondsMoved — сколько целых секунд сессии переезжает (для журнала/ответа; в старом коде не считалось).
	SecondsMoved int64
	// ClearFrom — на исходном ПК обнулить current_session_id, current_session_user_id, session_ends_at.
	// session_guest НЕ трогается ни на исходном, ни на целевом ПК (как в старом коде, BR-035 §15).
	ClearFrom bool
	// Части оплаты (session_purchases) не меняются: они привязаны к session_id, поэтому возврат при
	// завершении на новом ПК считается так же (GAP-036).
}

// PlanTransfer — пересадка клиента userID с ПК from на target.
//
// Laravel: app/Services/SeatTransfers.php transfer() (стр. 83–120), ensureOwnSession() (122–127).
// Порядок проверок как в старом коде: тот же/несуществующий ПК → своя оплаченная сессия на исходном →
// свободен ли целевой и подходит ли зона. target == nil — ПК не найден в клубе.
func PlanTransfer(from Computer, target *Computer, userID int64, zones []int64, targetBlockedByBooking bool, now time.Time) (Transfer, error) {
	if target == nil || target.ID == from.ID {
		return Transfer{}, ErrTransferSameOrMissing
	}
	if !from.HasPaidSession(now) || !from.sessionOwnedBy(userID) {
		return Transfer{}, ErrTransferNotOwnSession
	}
	if !FreeForTransfer(*target, targetBlockedByBooking, now) || (zones != nil && !slices.Contains(zones, target.ZoneID)) {
		return Transfer{}, ErrTransferUnavailable
	}
	return Transfer{
		SessionID:     *from.SessionID,
		UserID:        userID,
		SessionEndsAt: *from.SessionEndsAt,
		SecondsMoved:  SecondsUntil(from.SessionEndsAt, now),
		ClearFrom:     true,
	}, nil
}

// lastZonePurchase — последняя по id покупка сессии без пакетов и времени брони (allowedZoneIds(): последняя запись
// журнала tariff.purchased|time.purchased).
func lastZonePurchase(purchases []ZonePurchase) *ZonePurchase {
	var last *ZonePurchase
	for i := range purchases {
		p := &purchases[i]
		if p.Source == SourcePackage || p.Source == SourceBooking {
			continue
		}
		if last == nil || p.ID > last.ID {
			last = p
		}
	}
	return last
}

// NeedsLevelZones — AllowedZones использует зоны уровня: последняя покупка — найденный тариф с зонами.
// Laravel запрашивает уровень только в этой ветке allowedZoneIds(); иначе запрос зон уровня не нужен.
func NeedsLevelZones(purchases []ZonePurchase) bool {
	last := lastZonePurchase(purchases)
	return last != nil && last.Source != SourceMinutes && last.TariffFound && len(last.TariffZoneIDs) > 0
}
