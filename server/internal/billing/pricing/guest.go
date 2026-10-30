package pricing

import (
	"fmt"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// GuestComputer — ПК гостевой продажи (club_computers под FOR UPDATE, в порядке id) с ценой часа его зоны.
type GuestComputer struct {
	Name             string // displayName() — для текстов ошибок
	Computer         Computer
	ZonePricePerHour *money.Tiyn // club_zones.price_per_hour; nil — нет зоны или цены
}

// GuestComputerQuote — цена и время одного ПК.
type GuestComputerQuote struct {
	Price         money.Tiyn
	SessionEndsAt time.Time
	Extends       bool // гостевая сессия продлевается (тот же session_id)
}

// GuestSessionQuote — итог гостевой продажи.
type GuestSessionQuote struct {
	Minutes   int64 // длительность на каждом ПК
	Computers []GuestComputerQuote
	Total     money.Tiyn // одна запись прихода в кассу (formatCents($total))
}

// GuestSessionInput — продажа времени гостю без аккаунта на кассе.
type GuestSessionInput struct {
	// Тариф (is_active уже проверен запросом: find среди активных; не найден — передайте TariffMissing).
	// nil и !TariffMissing — продаются минуты.
	Tariff        *Tariff
	TariffMissing bool  // tariff_id указан, но активного тарифа нет
	Minutes       int64 // при продаже минут: 1…MaxMinutes (валидация запроса)
	Computers     []GuestComputer
	Now           time.Time
	Location      *time.Location
	// CheckBooking — проверка чужой брони ПК на [now, endsAt + интервал) (ComputerReservations::blocking() с userId 0);
	// вызывается для каждого ПК сразу после проверки занятости, как в старом коде. nil — не проверять.
	CheckBooking func(i int, pc GuestComputer, endsAt time.Time) error
}

// QuoteGuestSession — итог гостевой сессии: тариф (его цена БЕЗ скидки, на каждом ПК) или минуты × цена часа
// зоны каждого ПК; без лимита количества и ролей тарифа. Порядок проверок как в sell():
// тариф (активен, в продаже, в периоде действия) → для каждого ПК: цена (зона тарифа / цена часа) →
// занят (резерв или чужая не гостевая сессия) → бронь.
// Laravel: app/Services/GuestSessions.php sell() (стр. 34–96), priceFor() (стр. 98–113) (BR-038; GS-091, GS-092,
// GS-094, GS-063, GS-066, GAP-030, GAP-031).
func QuoteGuestSession(in GuestSessionInput) (GuestSessionQuote, error) {
	if in.TariffMissing || (in.Tariff != nil && (!in.Tariff.OnSale(in.Now, in.Location) || !in.Tariff.InValidityPeriod(in.Now, in.Location))) {
		return GuestSessionQuote{}, ErrGuestTariffUnavailable
	}
	var duration int64
	if in.Tariff != nil {
		duration = in.Tariff.EffectiveMinutes()
	} else {
		if in.Minutes < 1 || in.Minutes > MaxMinutes {
			return GuestSessionQuote{}, fmt.Errorf("%w: минуты %d вне 1…%d", ErrInvalidInput, in.Minutes, MaxMinutes)
		}
		duration = in.Minutes
	}
	if len(in.Computers) == 0 {
		return GuestSessionQuote{}, ErrGuestNoComputers
	}

	q := GuestSessionQuote{Minutes: duration, Computers: make([]GuestComputerQuote, 0, len(in.Computers))}
	var total money.Tiyn
	for i, pc := range in.Computers {
		price, err := guestPrice(pc, in.Tariff, duration)
		if err != nil {
			return GuestSessionQuote{}, err
		}
		c := pc.Computer
		extend := c.PaidSession
		if c.Reserved || (extend && !c.SessionGuest) {
			return GuestSessionQuote{}, ErrGuestComputerBusy.with(map[string]string{"computer": pc.Name})
		}
		ends, _ := c.SessionEnd(in.Now, duration)
		if in.CheckBooking != nil {
			if err := in.CheckBooking(i, pc, ends); err != nil {
				return GuestSessionQuote{}, err
			}
		}
		if total, err = money.Add(total, price); err != nil {
			return GuestSessionQuote{}, fmt.Errorf("%w: итог гостевой сессии", ErrInvalidInput)
		}
		q.Computers = append(q.Computers, GuestComputerQuote{Price: price, SessionEndsAt: ends, Extends: extend})
	}
	q.Total = total
	return q, nil
}

// guestPrice — GuestSessions::priceFor(): тариф — amountToCents($tariff->price) (без скидки; зона ПК должна быть
// в зонах тарифа), иначе minutesPriceInCents($zone->price_per_hour, $minutes) (цена часа без скидки).
func guestPrice(pc GuestComputer, t *Tariff, minutes int64) (money.Tiyn, error) {
	if t != nil {
		if !t.InZone(pc.Computer.ZoneID) {
			return 0, ErrGuestTariffZone.with(map[string]string{"computer": pc.Name})
		}
		if err := checkPrice(t.Price); err != nil {
			return 0, err
		}
		return t.Price, nil
	}
	if pc.ZonePricePerHour == nil {
		return 0, ErrGuestNoHourlyPrice.with(map[string]string{"computer": pc.Name})
	}
	return MinutesPrice(*pc.ZonePricePerHour, minutes)
}
