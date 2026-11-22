package pricing

import (
	"fmt"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Computer — состояние ПК под FOR UPDATE, нужное для покупки времени (club_computers).
type Computer struct {
	ZoneID   *int64 // club_zone_id
	Reserved bool   // is_reserved — ручной резерв ПК
	// Оплаченная сессия идёт: current_session_id не NULL и session_ends_at в будущем
	// (ClubComputer::hasPaidSession(), BR-030) — вычисляет вызывающий.
	PaidSession   bool
	SessionUserID *int64    // current_session_user_id (NULL у гостевой сессии)
	SessionGuest  bool      // session_guest
	SessionEndsAt time.Time // session_ends_at (значимо при PaidSession)
}

// CheckForCustomer — ПК может принять время этого клиента: не в резерве, нет чужой оплаченной сессии.
// Гостевая сессия (SessionUserID = nil) — тоже чужая.
// Laravel: app/Services/AgentTariffPurchaseService.php ensureComputerFor() (стр. 256–265) (BR-020).
func (c Computer) CheckForCustomer(userID int64) error {
	if c.Reserved {
		return ErrComputerReserved
	}
	if c.PaidSession && (c.SessionUserID == nil || *c.SessionUserID != userID) {
		return ErrComputerBusy
	}
	return nil
}

// SessionEnd — новый конец сессии: при идущей оплаченной сессии время прибавляется к её концу
// (продление), иначе — к now. Вторым значением — продлевается ли сессия (тот же session_id).
// Laravel: app/Services/AgentTariffPurchaseService.php sell() (стр. 283–285), GuestSessions::sell() (стр. 64–69)
// (BR-020, GAP-016, GS-091).
func (c Computer) SessionEnd(now time.Time, minutes int64) (time.Time, bool) {
	if c.PaidSession {
		return c.SessionEndsAt.Add(time.Duration(minutes) * time.Minute), true
	}
	return now.Add(time.Duration(minutes) * time.Minute), false
}

// SessionEndUntil — SessionEnd с концом периода действия тарифа until (nil — конца нет): время тарифа
// останавливается в until, цена та же (куплен в 15:00 на 08:00–18:00 — до 18:00).
//
//	$startsAt = $hasActiveSession ? $computer->session_ends_at->copy() : now();
//	$sessionEndsAt = $startsAt->copy()->addMinutes($minutes);
//	if ($until !== null && $sessionEndsAt->greaterThan($until)) {
//	    if ($until->lessThanOrEqualTo($startsAt)) throw … 'Тариф действует до :time, а время на ПК уже оплачено дальше.'
//	    $sessionEndsAt = $until->copy();
//	    $minutes = (int) ceil(($until->getTimestamp() - $startsAt->getTimestamp()) / 60);
//	}
//
// Возвращает конец сессии, продлевается ли она, минуты части сессии (урезанные) и было ли урезание.
// :time — until по часам клуба loc (H:i).
// Laravel: app/Services/AgentTariffPurchaseService.php sell() (BR-020, BR-024; GS-529, GS-530).
func (c Computer) SessionEndUntil(now time.Time, minutes int64, until *time.Time, loc *time.Location) (ends time.Time, extends bool, sold int64, cut bool, err error) {
	ends, extends = c.SessionEnd(now, minutes)
	if until == nil || !ends.After(*until) {
		return ends, extends, minutes, false, nil
	}
	startsAt := now
	if extends {
		startsAt = c.SessionEndsAt
	}
	if !until.After(startsAt) {
		if loc == nil {
			loc = time.UTC
		}
		return time.Time{}, false, 0, false, ErrTariffValidUntil.with(map[string]string{"time": until.In(loc).Format("15:04")})
	}
	return *until, extends, ceilMinutes(until.Unix() - startsAt.Unix()), true, nil
}

// ceilMinutes — (int) ceil($seconds / 60): целые минуты с округлением вверх (getTimestamp() — целые секунды).
func ceilMinutes(seconds int64) int64 {
	m := seconds / 60
	if seconds%60 > 0 {
		m++
	}
	return m
}

// Purchase — расчёт покупки времени: что списать, сколько минут, до какого момента сессия.
// Бронь на интервал [now, SessionEndsAt + интервал) проверяет вызывающий ПОСЛЕ Quote*
// (в sell() она идёт после проверки денег, BR-082).
type Purchase struct {
	Price   money.Tiyn // цена со скидкой (amount журнала)
	Payment Payment
	Minutes int64 // минуты части сессии (minutes_added); урезаны концом периода действия тарифа
	// FullMinutes — минуты до урезания концом периода действия (effectiveMinutes() тарифа / минуты пакета);
	// равны Minutes, если урезания не было. Разница у тарифа с лимитом использования остаётся пакетом.
	FullMinutes int64
	// CutAtValidityEnd — время остановлено концом периода действия тарифа: SessionEndsAt = этот конец.
	CutAtValidityEnd bool
	PartExpiresAt    *time.Time // expires_at части: только тариф с лимитом использования
	SessionEndsAt    time.Time
	ExtendsSession   bool // true — тот же session_id, иначе новый
}

// TariffPurchaseInput — данные покупки тарифа в агенте.
type TariffPurchaseInput struct {
	Tariff          Tariff
	Rules           PaymentRules
	DiscountPercent float64 // LoyaltyProgram::discountFor() клиента (float, %)
	Role            string  // club_memberships.role
	PurchaseCount   int64   // продаж тарифа в агенте (Решение Q-007 (docs/DECISIONS.md))
	UserID          int64
	Computer        Computer
	Choice          BalanceChoice
	Balances        Balances
	Now             time.Time
	Location        *time.Location // часовой пояс клуба (CurrentClub::localTimezone()); nil — UTC
}

// QuoteTariffPurchase — расчётная часть покупки тарифа в порядке проверок старого кода:
// доступность (isAvailable) → ПК (ensureComputerFor) → зона тарифа → цена (priceInCents) → конец периода
// действия (sell(): время останавливается в нём, раньше раскладки по балансам) → оплата (sell).
// Laravel: app/Services/AgentTariffPurchaseService.php purchase(), sell()
// (BR-013, BR-015, BR-016, BR-018, BR-020; GS-049, GS-050, GS-059, GS-529, GS-530, GAP-006…GAP-008, GAP-011,
// GAP-015, GAP-016).
func QuoteTariffPurchase(in TariffPurchaseInput) (Purchase, error) {
	// Решение SV-2 (docs/DECISIONS.md): выключенный тариф (is_active = false) не продаётся — «Тариф недоступен.», как
	// тариф не в продаже. В старом purchase() is_active не проверялся: тариф скрывался только из списка агента,
	// но покупался по id.
	if !in.Tariff.IsActive || in.Tariff.Availability(in.Role, in.PurchaseCount, in.Now, in.Location) != Available {
		return Purchase{}, ErrTariffUnavailable
	}
	if err := in.Computer.CheckForCustomer(in.UserID); err != nil {
		return Purchase{}, err
	}
	if !in.Tariff.InZone(in.Computer.ZoneID) {
		return Purchase{}, ErrTariffNotForComputer
	}
	price, err := TariffPrice(in.Tariff, in.Rules, in.DiscountPercent)
	if err != nil {
		return Purchase{}, err
	}
	// Laravel: purchase() — sell(…, $tariff->validityEndsAt()): проверка конца периода идёт в начале sell(),
	// до блокировки баланса и раскладки оплаты.
	full := in.Tariff.EffectiveMinutes()
	ends, extends, minutes, cut, err := in.Computer.SessionEndUntil(in.Now, full, validityEnd(&in.Tariff, in.Now, in.Location), in.Location)
	if err != nil {
		return Purchase{}, err
	}
	pay, err := in.Rules.Pay(price, in.Tariff.PaymentMethod, in.Choice, in.Balances)
	if err != nil {
		return Purchase{}, err
	}
	return Purchase{
		Price: price, Payment: pay, Minutes: minutes, FullMinutes: full, CutAtValidityEnd: cut,
		PartExpiresAt: in.Tariff.UsageExpiresAt(in.Now),
		SessionEndsAt: ends, ExtendsSession: extends,
	}, nil
}

// MinutesPurchaseInput — данные покупки минут по цене часа зоны ПК.
type MinutesPurchaseInput struct {
	ZonePricePerHour *money.Tiyn // club_zones.price_per_hour; nil — зоны нет или цена не задана
	Rules            PaymentRules
	DiscountPercent  float64 // 0 — клиент без членства в клубе
	UserID           int64
	Computer         Computer
	Minutes          int64 // 1…MaxMinutes (валидация запроса)
	Choice           BalanceChoice
	Balances         Balances
	Now              time.Time
}

// QuoteMinutesPurchase — расчётная часть покупки минут: зона без цены → ошибка; ПК; цена
// minutesPriceInCents(hourlyPrice(...), minutes) (двойное округление); оплата как у тарифа main_or_bonus.
// Laravel: app/Services/AgentTariffPurchaseService.php purchaseMinutes() (стр. 152–180) (BR-014, BR-021;
// GS-039, GS-040, GS-051, GAP-010).
func QuoteMinutesPurchase(in MinutesPurchaseInput) (Purchase, error) {
	if in.Minutes < 1 || in.Minutes > MaxMinutes {
		// GuestMinutesController::purchase(): 'minutes' => required|integer|min:1|max:720 — проверка запроса.
		return Purchase{}, fmt.Errorf("%w: минуты %d вне 1…%d", ErrInvalidInput, in.Minutes, MaxMinutes)
	}
	if in.ZonePricePerHour == nil {
		return Purchase{}, ErrNoHourlyPrice
	}
	if err := in.Computer.CheckForCustomer(in.UserID); err != nil {
		return Purchase{}, err
	}
	hour, err := HourlyPrice(*in.ZonePricePerHour, in.Rules, in.DiscountPercent)
	if err != nil {
		return Purchase{}, err
	}
	price, err := MinutesPrice(hour, in.Minutes)
	if err != nil {
		return Purchase{}, err
	}
	pay, err := in.Rules.Pay(price, MainOrBonus, in.Choice, in.Balances)
	if err != nil {
		return Purchase{}, err
	}
	ends, extends := in.Computer.SessionEnd(in.Now, in.Minutes)
	return Purchase{Price: price, Payment: pay, Minutes: in.Minutes, FullMinutes: in.Minutes, SessionEndsAt: ends, ExtendsSession: extends}, nil
}

// PackageStartMinutes — сколько минут запускает сохранённый пакет: остаток, но не дальше срока.
//
//	min($package->minutes_left, intdiv(max(0, $package->expires_at->getTimestamp() - now()->getTimestamp()), 60))
//
// ≤ 0 → пакет истёк (ErrPackageExpired; старый код удаляет пакет, но исключение откатывает удаление — OQ-BR-06).
// Laravel: app/Services/AgentTariffPurchaseService.php startPackage() (стр. 214–247) (BR-024, GAP-014).
func PackageStartMinutes(minutesLeft int64, expiresAt, now time.Time) (int64, error) {
	minutes := min(minutesLeft, money.IntDiv(max(0, expiresAt.Unix()-now.Unix()), 60))
	if minutes <= 0 {
		return 0, ErrPackageExpired
	}
	return minutes, nil
}

// PackageStartInput — данные запуска сохранённого пакета (time_packages под FOR UPDATE).
type PackageStartInput struct {
	MinutesLeft int64
	ExpiresAt   time.Time
	// Тариф пакета (withTrashed — и удалённый); nil — тариф удалён физически: без ограничений зон и периодов.
	Tariff   *Tariff
	UserID   int64
	Computer Computer
	Balances Balances // sell() проверяет и цену 0: отрицательный баланс → «Недостаточно средств…»
	Now      time.Time
	Location *time.Location
}

// QuoteStartPackage — расчётная часть запуска пакета: минуты (PackageStartMinutes) → ПК → зоны тарифа →
// период действия тарифа → конец периода действия (sell(): время дальше него остаётся в пакете); оплата —
// sell(0, 'main_balance_only', 'main'), т. е. без списаний.
// Часть сессии: kind=tariff, expires_at = срок пакета (тот же, а не новый). Purchase.FullMinutes — минуты после
// ограничения сроком пакета, Purchase.Minutes — после конца периода действия; что остаётся в пакете — решает
// вызывающий (service.StartPackage, UF-1).
// Laravel: app/Services/AgentTariffPurchaseService.php startPackage() (BR-024, GS-047, GS-530, GAP-014).
func QuoteStartPackage(in PackageStartInput) (Purchase, error) {
	minutes, err := PackageStartMinutes(in.MinutesLeft, in.ExpiresAt, in.Now)
	if err != nil {
		return Purchase{}, err
	}
	if err := in.Computer.CheckForCustomer(in.UserID); err != nil {
		return Purchase{}, err
	}
	if in.Tariff != nil && !in.Tariff.InZone(in.Computer.ZoneID) {
		return Purchase{}, ErrPackageNotForPC
	}
	if in.Tariff != nil && !in.Tariff.InValidityPeriod(in.Now, in.Location) {
		return Purchase{}, ErrPackageOutOfPeriod
	}
	// Laravel: startPackage() — sell(…, $tariff?->validityEndsAt()).
	ends, extends, sold, cut, err := in.Computer.SessionEndUntil(in.Now, minutes, validityEnd(in.Tariff, in.Now, in.Location), in.Location)
	if err != nil {
		return Purchase{}, err
	}
	pay, err := DefaultPaymentRules().Pay(0, MainBalanceOnly, ChoiceMain, in.Balances)
	if err != nil {
		return Purchase{}, err
	}
	expires := in.ExpiresAt
	return Purchase{
		Payment: pay, Minutes: sold, FullMinutes: minutes, CutAtValidityEnd: cut,
		PartExpiresAt: &expires, SessionEndsAt: ends, ExtendsSession: extends,
	}, nil
}

// validityEnd — $tariff?->validityEndsAt(): конец текущего периода действия тарифа; nil — тарифа нет или конца нет.
func validityEnd(t *Tariff, now time.Time, loc *time.Location) *time.Time {
	if t == nil {
		return nil
	}
	until, ok := t.ValidityEndsAt(now, loc)
	if !ok {
		return nil
	}
	return &until
}
