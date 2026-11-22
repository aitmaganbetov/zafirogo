package reservations

// Бронь ПК в гостевом приложении: по часам (длительность × цена часа зоны) или тарифом; гость выбирает ПК,
// свободные на время, и сразу платит с балансов (со своей скидкой). Как начинается оплаченное время и когда
// возвращаются деньги — paid.go.
//
// Laravel: app/Services/GuestBookings.php (plan(), computers(), tariffs(), options(), book(), cancel(), present(),
// presentAll()); app/Services/AgentTariffPurchaseService.php (bookableTariffs(), bookedMinutes(), purchasesLeft(),
// tariffPrice(), hourlyPrice(), bonusPayment(), isAvailable()). Правила — docs/business-rules.md BR-174…BR-177, BR-190.
// Вызывающий даёт тенантную транзакцию клуба (q); клуб и гость — из аутентификации.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Пределы брони из приложения — константы GuestBookings (BR-177).
const (
	// MaxPending — броней впереди у гостя в клубе одновременно (ПК, забронированные вместе, — одна бронь).
	MaxPending = 2
	// MaxDaysAhead — на сколько дней вперёд можно бронировать.
	MaxDaysAhead = 7
	// MinMinutes, MaxMinutes — длительность брони по часам.
	MinMinutes = 30
	MaxMinutes = 720
	// MaxComputers — ПК в одной брони.
	MaxComputers = 10
)

// Тексты GuestBookings и GuestBookingController (ключи перевода).
const (
	MsgAppDisabled    = "Бронь из приложения в этом клубе выключена."
	MsgTariffTime     = "Этот тариф нельзя забронировать на это время."
	MsgPickComputers  = "Выберите от 1 до :count ПК."
	MsgTariffLeft     = "Тариф «:tariff» осталось купить на :count ПК."
	MsgCannotBookPC   = "Этот ПК нельзя забронировать."
	MsgCannotBookName = "ПК :computer нельзя забронировать."
	MsgAheadLimit     = "У вас уже :count бронь в этом клубе — это предел.|У вас уже :count брони в этом клубе — это предел.|У вас уже :count броней в этом клубе — это предел."
	MsgShort          = "Недостаточно средств: бронь стоит :price."
	MsgGuestNoCancel  = "Клуб не разрешает отменять бронь в приложении."
	MsgDuration       = "Длительность брони — от :min до :max минут."
	MsgTooFar         = "Бронь — не дальше чем на :count день вперёд.|Бронь — не дальше чем на :count дня вперёд.|Бронь — не дальше чем на :count дней вперёд."
	// Ответы контроллера.
	MsgBookedMany = "ПК забронированы. Код брони :code: на забронированных ПК его вводят на экране входа — оплаченное время начнётся само."
	MsgBookedOne  = "ПК забронирован. Войдите на нём в своё время — оплаченное время начнётся само."
	// MsgCancelled — ComputerReservationController::cancel(): здесь — ответ на повтор запроса брони, которая уже отменена.
	MsgCancelled      = "Бронь отменена."
	MsgGuestCancelled = "Бронь отменена, деньги вернулись на баланс."

	logPaid = "Оплачена бронь ПК :computer на :from"
	// opAppBooking — операция billing_requests (повтор запроса с тем же Idempotency-Key).
	opAppBooking = "app_booking"
)

// ErrAppDisabled — ensureEnabled(): HttpException(403, «Бронь из приложения в этом клубе выключена.»).
var ErrAppDisabled = errors.New("reservations: app booking is disabled in the club")

// app — клуб и гость запроса для расчётов брони.
type app struct {
	info  club.Info
	rules billingsvc.ClubRules
	// member — у гостя есть членство в клубе; discount — его скидка (LoyaltyProgram::discountFor()), role — роль.
	member   bool
	discount float64
	role     string
}

func loadApp(ctx context.Context, q *store.Queries, clubID, userID int64) (app, error) {
	info, err := club.Load(ctx, q, clubID)
	if err != nil {
		return app{}, err
	}
	rules, err := billingsvc.LoadClubRules(ctx, q, clubID)
	if err != nil {
		return app{}, err
	}
	a := app{info: info, rules: rules}
	m, err := q.GetMembershipForPricing(ctx, store.GetMembershipForPricingParams{ClubID: clubID, UserID: userID})
	switch {
	case err == nil:
		a.member, a.role = true, m.Role
		// Laravel: LoyaltyProgram::discountFor() (BR-011).
		a.discount = bonus.LoyaltyDiscountFor(rules.Bonus.LoyaltyLevelsInForce(), m.Discount, money.Tiyn(m.TotalSpent))
	case !errors.Is(err, pgx.ErrNoRows):
		return app{}, err
	}
	return a, nil
}

// hourly — hourlyPrice(): цена часа зоны для гостя (со скидкой, если клуб применяет её к почасовому времени).
func (a app) hourly(zonePrice int64) (money.Tiyn, error) {
	return pricing.HourlyPrice(money.Tiyn(zonePrice), a.rules.Rules, a.discount)
}

// bookTariff — тариф для брони: строка, расчётная часть и счётчик покупок.
type bookTariff struct {
	id          int64
	name        string
	description *string
	t           pricing.Tariff
	sold        int64
}

// available — isAvailable($tariff, $membership, $purchaseCount, $localStart): в продаже СЕЙЧАС, лимит количества не
// исчерпан, роль гостя, период действия содержит НАЧАЛО БРОНИ (BR-175).
func (a app) available(t bookTariff, startsAt, now time.Time) bool {
	if !t.t.OnSale(now, a.rules.Loc) {
		return false
	}
	if t.t.QuantityLimitEnabled && t.t.QuantityLimit != nil && t.sold >= *t.t.QuantityLimit {
		return false
	}
	if len(t.t.UserRoles) > 0 && !slices.Contains(t.t.UserRoles, a.role) {
		return false
	}
	return t.t.ValidityPeriods.ContainsAt(startsAt, a.rules.Loc)
}

// minutes — bookedMinutes(): время тарифа от начала брони, обрезанное концом периода действия.
func (a app) minutes(t bookTariff, startsAt time.Time) int64 {
	until, has := t.t.ValidityPeriods.EndOfAt(startsAt, a.rules.Loc)
	return BookedMinutes(t.t.EffectiveMinutes(), startsAt, until, has)
}

// bookable — bookableTariffs(): тарифы, которые гость может забронировать на это начало, по длительности и имени.
func (a app) bookable(ctx context.Context, q *store.Queries, startsAt, now time.Time) ([]bookTariff, error) {
	if !a.member {
		return nil, ErrNotFound // $user->currentMembership()->firstOrFail()
	}
	rows, err := q.AppBookingTariffs(ctx, a.info.ID)
	if err != nil {
		return nil, err
	}
	out := make([]bookTariff, 0, len(rows))
	for _, r := range rows {
		pt, err := billingsvc.PricingTariff(billingsvc.TariffFields{
			ID: r.ID, Name: r.Name, DurationMinutes: r.DurationMinutes, Price: r.Price, PaymentMethod: r.PaymentMethod,
			DiscountEnabled: r.DiscountEnabled, IsActive: r.IsActive, UsageLimitEnabled: r.UsageLimitEnabled,
			UsageLimitMinutes: r.UsageLimitMinutes, QuantityLimitEnabled: r.QuantityLimitEnabled, QuantityLimit: r.QuantityLimit,
			UserRoles: r.UserRoles, SalePeriods: r.SalePeriods, ValidityPeriods: r.ValidityPeriods,
		}, r.ZoneIds)
		if err != nil {
			return nil, err
		}
		t := bookTariff{id: r.ID, name: r.Name, description: r.Description, t: pt, sold: r.Sold}
		if a.available(t, startsAt, now) {
			out = append(out, t)
		}
	}
	return out, nil
}

// planned — что бронируется: по часам на minutes или тариф (его время от начала).
type planned struct {
	minutes int64
	tariff  *bookTariff
}

// ensureStart — начало не раньше now − 1 мин и не дальше MaxDaysAhead дней.
func ensureStart(startsAt, now time.Time) error {
	if startsAt.Before(now.Add(-time.Minute)) {
		return &Error{Field: "starts_at", Message: MsgPast}
	}
	if startsAt.After(now.AddDate(0, 0, MaxDaysAhead)) {
		return &Error{Field: "starts_at", Message: MsgTooFar, Params: map[string]string{"count": strconv.Itoa(MaxDaysAhead)}}
	}
	return nil
}

// plan — GuestBookings::plan().
func (a app) plan(ctx context.Context, q *store.Queries, startsAt time.Time, minutes, tariffID *int64, now time.Time) (planned, error) {
	if tariffID == nil {
		if err := ensureStart(startsAt, now); err != nil {
			return planned{}, err
		}
		var m int64
		if minutes != nil {
			m = *minutes
		}
		if m < MinMinutes || m > MaxMinutes {
			return planned{}, &Error{Field: "duration_minutes", Message: MsgDuration, Params: map[string]string{
				"min": strconv.Itoa(MinMinutes), "max": strconv.Itoa(MaxMinutes),
			}}
		}
		return planned{minutes: m}, nil
	}
	if err := ensureStart(startsAt, now); err != nil {
		return planned{}, err
	}
	tariffs, err := a.bookable(ctx, q, startsAt, now)
	if err != nil {
		return planned{}, err
	}
	for i := range tariffs {
		if tariffs[i].id == *tariffID {
			if m := a.minutes(tariffs[i], startsAt); m > 0 {
				return planned{minutes: m, tariff: &tariffs[i]}, nil
			}
			break
		}
	}
	return planned{}, &Error{Field: "tariff_id", Message: MsgTariffTime}
}

// price — GuestBookings::price(): цена брони одного ПК зоны (BR-174). zonePrice nil — у зоны нет цены часа.
func (a app) price(zonePrice *int64, p planned) (money.Tiyn, error) {
	if p.tariff != nil {
		return pricing.TariffPrice(p.tariff.t, a.rules.Rules, a.discount)
	}
	if zonePrice == nil {
		return 0, fmt.Errorf("reservations: zone without an hourly price")
	}
	hour, err := a.hourly(*zonePrice)
	if err != nil {
		return 0, err
	}
	return pricing.MinutesPrice(hour, p.minutes)
}

// ---------------------------------------------------------------------------
// Что видит гость
// ---------------------------------------------------------------------------

// AppComputer — ПК в списках приложения. Available — свободен на время брони (только options()).
type AppComputer struct {
	ID        int64
	Name      string
	Available bool
}

// AppZone — зона с ценой часа гостя (nil — у зоны нет цены часа) и её ПК. Price — цена брони в зоне (options()).
type AppZone struct {
	ID           int64
	Name         string
	PricePerHour *money.Tiyn
	Price        money.Tiyn
	Computers    []AppComputer
}

// AppTariff — тариф, который гость может забронировать: цена для гостя и время от начала брони.
type AppTariff struct {
	ID          int64
	Name        string
	Description *string
	Minutes     int64
	Price       money.Tiyn
	ZoneIDs     []int64
}

// AppBooking — бронь гостя как её показывает приложение: ПК, забронированные вместе, — одна бронь.
type AppBooking struct {
	ID        int64
	Code      *string
	Computers []string
	Zone      *string
	Tariff    *string
	StartsAt  time.Time
	EndsAt    time.Time
	State     string
	Paid      money.Tiyn
	PaidBonus money.Tiyn
	CanCancel bool
}

// AppIndex — GuestBookingController::index(): брони гостя и что разрешает клуб.
type AppIndex struct {
	Enabled    bool
	CanCancel  bool
	GapMinutes int64
	// Settings — clubs.settings (валюту старый API отдаёт буквально — её читает слой http/legacy).
	Settings []byte
	Bookings []AppBooking
}

// ListForGuest — index(): брони гостя с началом не раньше чем сутки назад (не больше 20 ПК), по началу.
func ListForGuest(ctx context.Context, q *store.Queries, clubID, userID int64, now time.Time) (AppIndex, error) {
	info, err := club.Load(ctx, q, clubID)
	if err != nil {
		return AppIndex{}, err
	}
	rows, err := q.AppBookingList(ctx, store.AppBookingListParams{ClubID: clubID, UserID: &userID, Since: now.AddDate(0, 0, -1)})
	if err != nil {
		return AppIndex{}, err
	}
	items := make([]bookingRow, len(rows))
	for i, r := range rows {
		items[i] = bookingRow(r)
	}
	return AppIndex{
		Enabled: AppBookingEnabled(info.Settings), CanCancel: GuestMayCancel(info.Settings), GapMinutes: info.BookingGapMinutes(),
		Settings: info.Settings, Bookings: presentAll(items, GuestMayCancel(info.Settings), now),
	}, nil
}

// bookingRow — строка брони с ПК, зоной и тарифом (AppBookingList / AppBookingGroup).
type bookingRow store.AppBookingListRow

// presentAll — presentAll(): группы по коду (без кода — сама бронь) в порядке первого появления.
func presentAll(rows []bookingRow, mayCancel bool, now time.Time) []AppBooking {
	var order []string
	groups := map[string][]bookingRow{}
	for _, r := range rows {
		key := "id" + strconv.FormatInt(r.ID, 10)
		if r.Code != nil {
			key = *r.Code
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], r)
	}
	out := make([]AppBooking, 0, len(order))
	for _, key := range order {
		out = append(out, present(groups[key], mayCancel, now))
	}
	return out
}

// present — GuestBookings::present(): ПК группы через запятую; состояние — active, пока идёт хоть один ПК, иначе
// pending, пока ждёт хоть один, иначе как у первого; отменить можно, только пока все ПК ждут и клуб разрешил.
func present(group []bookingRow, mayCancel bool, now time.Time) AppBooking {
	first := group[0]
	b := AppBooking{ID: first.ID, Code: first.Code, Tariff: first.TariffName, StartsAt: first.StartsAt, EndsAt: first.EndsAt, Computers: []string{}}
	var zones []string
	allPending, anyActive, anyPending := true, false, false
	for _, r := range group {
		s := State(r.StartsAt, r.EndsAt, r.ArrivedAt, r.CancelledAt, now)
		allPending = allPending && s == "pending"
		anyActive = anyActive || s == "active"
		anyPending = anyPending || s == "pending"
		if r.ComputerNumber != nil {
			b.Computers = append(b.Computers, club.ComputerName(r.ComputerDisplayName, r.ComputerDeviceName, *r.ComputerNumber))
		}
		if r.ZoneName != nil && *r.ZoneName != "" && !slices.Contains(zones, *r.ZoneName) {
			zones = append(zones, *r.ZoneName)
		}
		b.Paid += money.Tiyn(r.PaidMainCents + r.PaidBonusCents)
		b.PaidBonus += money.Tiyn(r.PaidBonusCents)
	}
	switch {
	case anyActive:
		b.State = "active"
	case anyPending:
		b.State = "pending"
	default:
		b.State = State(first.StartsAt, first.EndsAt, first.ArrivedAt, first.CancelledAt, now)
	}
	if len(zones) > 0 {
		z := strings.Join(zones, ", ")
		b.Zone = &z
	}
	b.CanCancel = allPending && mayCancel
	return b
}

// ensureEnabled — GuestBookings::ensureEnabled().
func (a app) ensureEnabled() error {
	if !AppBookingEnabled(a.info.Settings) {
		return ErrAppDisabled
	}
	return nil
}

// AppComputers — GuestBookings::computers(): ПК клуба по зонам (зоны с ПК, без служебных ПК is_reserved) с ценой
// часа гостя — он сначала выбирает ПК; свободен ли ПК, зависит от времени (AppOptions).
func AppComputers(ctx context.Context, q *store.Queries, clubID, userID int64) ([]AppZone, error) {
	a, err := loadApp(ctx, q, clubID, userID)
	if err != nil {
		return nil, err
	}
	if err := a.ensureEnabled(); err != nil {
		return nil, err
	}
	zones, err := q.AppBookingZones(ctx, clubID)
	if err != nil {
		return nil, err
	}
	pcs, err := q.AppBookingComputers(ctx, clubID)
	if err != nil {
		return nil, err
	}
	byZone := map[int64][]AppComputer{}
	for _, c := range pcs {
		if !c.IsReserved {
			byZone[c.ClubZoneID] = append(byZone[c.ClubZoneID], AppComputer{ID: c.ID, Name: club.ComputerName(c.DisplayName, c.DeviceName, c.Number)})
		}
	}
	out := []AppZone{}
	for _, z := range zones {
		if len(byZone[z.ID]) == 0 {
			continue
		}
		zone := AppZone{ID: z.ID, Name: z.Name, Computers: byZone[z.ID]}
		if z.PricePerHour != nil {
			h, err := a.hourly(*z.PricePerHour)
			if err != nil {
				return nil, err
			}
			zone.PricePerHour = &h
		}
		out = append(out, zone)
	}
	return out, nil
}

// AppTariffs — GuestBookings::tariffs(): тарифы, которые гость может забронировать на это начало, с ценой и
// временем; с выбранными ПК — только тарифы без зон или с зонами ВСЕХ этих ПК. Тариф, от которого до конца периода
// действия не осталось времени, не показывается.
func AppTariffs(ctx context.Context, q *store.Queries, clubID, userID int64, startsAt time.Time, computerIDs []int64, now time.Time) ([]AppTariff, error) {
	a, err := loadApp(ctx, q, clubID, userID)
	if err != nil {
		return nil, err
	}
	if err := a.ensureEnabled(); err != nil {
		return nil, err
	}
	if err := ensureStart(startsAt, now); err != nil {
		return nil, err
	}
	var zoneIDs []int64
	if len(computerIDs) > 0 {
		if zoneIDs, err = q.AppBookingComputerZones(ctx, store.AppBookingComputerZonesParams{ClubID: clubID, Ids: computerIDs}); err != nil {
			return nil, err
		}
	}
	tariffs, err := a.bookable(ctx, q, startsAt, now)
	if err != nil {
		return nil, err
	}
	out := []AppTariff{}
	for _, t := range tariffs {
		fits := true
		if len(t.t.ZoneIDs) > 0 {
			for _, z := range zoneIDs {
				fits = fits && slices.Contains(t.t.ZoneIDs, z)
			}
		}
		if !fits {
			continue
		}
		minutes := a.minutes(t, startsAt)
		if minutes <= 0 {
			continue
		}
		price, err := pricing.TariffPrice(t.t, a.rules.Rules, a.discount)
		if err != nil {
			return nil, err
		}
		ids := t.t.ZoneIDs
		if ids == nil {
			ids = []int64{}
		}
		out = append(out, AppTariff{ID: t.id, Name: t.name, Description: t.description, Minutes: minutes, Price: price, ZoneIDs: ids})
	}
	return out, nil
}

// AppOptions — GuestBookings::options(): зоны брони (с ценой часа или зоны тарифа), цена брони в каждой и ПК зоны —
// свободен или нет на это время: не служебный, нет ожидающей брони в [начало − интервал, конец + интервал), нет
// оплаченной сессии, которая заходит в интервал до начала.
func AppOptions(ctx context.Context, q *store.Queries, clubID, userID int64, startsAt time.Time, minutes, tariffID *int64, now time.Time) (int64, []AppZone, error) {
	a, err := loadApp(ctx, q, clubID, userID)
	if err != nil {
		return 0, nil, err
	}
	if err := a.ensureEnabled(); err != nil {
		return 0, nil, err
	}
	p, err := a.plan(ctx, q, startsAt, minutes, tariffID, now)
	if err != nil {
		return 0, nil, err
	}
	gap := time.Duration(a.info.BookingGapMinutes()) * time.Minute
	from := startsAt.Add(-gap)
	to := startsAt.Add(time.Duration(p.minutes)*time.Minute + gap)
	zones, err := q.AppBookingZones(ctx, clubID)
	if err != nil {
		return 0, nil, err
	}
	pcs, err := q.AppBookingComputers(ctx, clubID)
	if err != nil {
		return 0, nil, err
	}
	bookedIDs, err := q.AppBookingBookedComputerIDs(ctx, store.AppBookingBookedComputerIDsParams{ClubID: clubID, FromAt: from, ToAt: to})
	if err != nil {
		return 0, nil, err
	}
	byZone := map[int64][]AppComputer{}
	for _, c := range pcs {
		busy := session.Computer{SessionID: c.CurrentSessionID, SessionUserID: c.CurrentSessionUserID, SessionGuest: c.SessionGuest,
			SessionEndsAt: c.SessionEndsAt}.HasPaidSession(now) && c.SessionEndsAt.After(from)
		byZone[c.ClubZoneID] = append(byZone[c.ClubZoneID], AppComputer{
			ID: c.ID, Name: club.ComputerName(c.DisplayName, c.DeviceName, c.Number),
			Available: !c.IsReserved && !slices.Contains(bookedIDs, c.ID) && !busy,
		})
	}
	out := []AppZone{}
	for _, z := range zones {
		switch {
		case p.tariff == nil && z.PricePerHour == nil:
			continue
		case p.tariff != nil && len(p.tariff.t.ZoneIDs) > 0 && !slices.Contains(p.tariff.t.ZoneIDs, z.ID):
			continue
		}
		zone := AppZone{ID: z.ID, Name: z.Name, Computers: byZone[z.ID]}
		if zone.Computers == nil {
			zone.Computers = []AppComputer{}
		}
		if z.PricePerHour != nil {
			h, err := a.hourly(*z.PricePerHour)
			if err != nil {
				return 0, nil, err
			}
			zone.PricePerHour = &h
		}
		if zone.Price, err = a.price(z.PricePerHour, p); err != nil {
			return 0, nil, err
		}
		out = append(out, zone)
	}
	return p.minutes, out, nil
}

// ---------------------------------------------------------------------------
// Бронь и оплата
// ---------------------------------------------------------------------------

// BookRequest — POST /api/guest/bookings.
type BookRequest struct {
	ClubID int64
	UserID int64
	// ComputerIDs — ПК брони (1…MaxComputers; повторы убираются).
	ComputerIDs []int64
	// StartsAt — начало, округлённое до минуты.
	StartsAt time.Time
	// Minutes — длительность по часам (без тарифа); TariffID — бронь тарифом.
	Minutes  *int64
	TariffID *int64
	// IdempotencyKey — заголовок Idempotency-Key запроса, если клиент его прислал: повтор с тем же ключом и телом
	// возвращает первую бронь и не списывает второй раз. Пусто — повтор запроса отсекает проверка пересечения броней
	// (как в старом).
	IdempotencyKey string
}

// BookResult — созданная бронь (группа ПК).
type BookResult struct {
	// ReservationIDs — брони по ПК в порядке id ПК; первая — id брони для гостя.
	ReservationIDs []int64    `json:"reservation_ids"`
	Code           string     `json:"code"`
	Price          money.Tiyn `json:"price"`
	Main           money.Tiyn `json:"main"`
	Bonus          money.Tiyn `json:"bonus"`
	Minutes        int64      `json:"minutes"`
	// LedgerTransactionID — операция reservation_payment (0 — цена 0: денег не двигалось).
	LedgerTransactionID int64 `json:"ledger_transaction_id"`
	// Replayed — ответ на повтор запроса с тем же ключом идемпотентности (ничего не записано).
	Replayed bool `json:"-"`
}

// Book — GuestBookings::book(): бронирует ПК на один период и оплачивает все разом; у них один код (друзья гостя
// вводят его на экране входа этих ПК — StartByCode).
//
// Порядок как в старом коде (блокировки: тариф → ПК по возрастанию id → строка баланса гостя):
//
//	1…10 ПК без повторов → (тариф FOR UPDATE — его строка сериализует лимит количества, как покупка в агенте) →
//	plan() → лимит количества тарифа: осталось меньше, чем ПК, — ошибка tariff_id → ПК FOR UPDATE → каждый ПК: зона
//	с ценой часа (по часам) или зона тарифа, не служебный; цена на ПК (BR-174) → баланс FOR UPDATE (он же
//	сериализует брони гостя) → не больше MaxPending броней впереди → оплата: бонусная часть от ОБЩЕЙ цены по правилам
//	покупки без выбора баланса (bonusPayment(choice = null), BR-018) → код → бронь на каждый ПК с его долей оплаты
//	(SplitBonus; Create проверяет пересечения и идущую сессию) → списание → накопительные траты (основная часть) →
//	журнал reservation.paid (один на группу).
//
// Деньги — одной операцией ledger reservation_payment на группу (member_main → revenue_time, member_bonus →
// bonus_fund), ссылка — первая бронь группы, ключ reservation_payment:<её id>.
func Book(ctx context.Context, q *store.Queries, req BookRequest, env Env) (BookResult, error) {
	var out BookResult
	now := env.Now
	// array_values(array_unique($computerIds)).
	ids := make([]int64, 0, len(req.ComputerIDs))
	for _, id := range req.ComputerIDs {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > MaxComputers {
		return out, &Error{Field: "club_computer_ids", Message: MsgPickComputers, Params: map[string]string{"count": strconv.Itoa(MaxComputers)}}
	}
	if req.IdempotencyKey != "" {
		sorted := slices.Clone(ids)
		slices.Sort(sorted)
		replayed, err := billingsvc.ClaimRequest(ctx, q, req.ClubID, req.IdempotencyKey, opAppBooking, struct {
			UserID      int64
			ComputerIDs []int64
			StartsAt    time.Time
			Minutes     *int64
			TariffID    *int64
		}{req.UserID, sorted, req.StartsAt.UTC(), req.Minutes, req.TariffID}, &out, now)
		if err != nil || replayed {
			out.Replayed = replayed
			return out, err
		}
	}
	a, err := loadApp(ctx, q, req.ClubID, req.UserID)
	if err != nil {
		return out, err
	}
	if err := a.ensureEnabled(); err != nil {
		return out, err
	}
	// Строка тарифа сериализует лимит количества, как покупка в агенте («The tariff row serializes its purchase
	// limit, as in the agent»).
	if req.TariffID != nil {
		if _, err := q.LockTariffForPurchase(ctx, store.LockTariffForPurchaseParams{ClubID: req.ClubID, ID: *req.TariffID}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	p, err := a.plan(ctx, q, req.StartsAt, req.Minutes, req.TariffID, now)
	if err != nil {
		return out, err
	}
	if t := p.tariff; t != nil && t.t.QuantityLimitEnabled && t.t.QuantityLimit != nil {
		// purchasesLeft(): quantity_limit − покупки (продажи в агенте + неотменённые брони, BR-190).
		if left := *t.t.QuantityLimit - t.sold; left < int64(len(ids)) {
			return out, &Error{Field: "tariff_id", Message: MsgTariffLeft, Params: map[string]string{
				"tariff": t.name, "count": strconv.FormatInt(max(0, left), 10),
			}}
		}
	}
	// ПК, затем баланс (как у покупки): строка баланса заодно сериализует брони гостя.
	pcs, err := q.AppBookingLockComputers(ctx, store.AppBookingLockComputersParams{ClubID: req.ClubID, Ids: ids})
	if err != nil {
		return out, err
	}
	if len(pcs) != len(ids) {
		return out, &Error{Field: "club_computer_ids", Message: MsgCannotBookPC}
	}
	zoneRows, err := q.AppBookingZones(ctx, req.ClubID)
	if err != nil {
		return out, err
	}
	g, err := a.priceGroup(pcs, zoneRows, p)
	if err != nil {
		return out, err
	}
	prices, names, total := g.prices, g.names, g.total
	// MemberBalance::query()->where('user_id', …)->lockForUpdate()->firstOrFail().
	locked, err := ledger.LockBalances(ctx, q, req.ClubID, req.UserID)
	if err != nil {
		if errors.Is(err, ledger.ErrBalanceNotFound) {
			return out, ErrNotFound
		}
		return out, err
	}
	// Bookings made together are one booking for the limit.
	ahead, err := q.AppBookingAhead(ctx, store.AppBookingAheadParams{ClubID: req.ClubID, UserID: &req.UserID, Now: now})
	if err != nil {
		return out, err
	}
	if n := pendingGroups(ahead); n >= MaxPending {
		return out, &Error{Field: "starts_at", Message: MsgAheadLimit, Params: map[string]string{"count": strconv.Itoa(n)}}
	}

	// bonusPayment($price, $tariff?->payment_method ?? 'main_or_bonus', null, $mainBalance, $bonusBalance, …).
	method := pricing.MainOrBonus
	if p.tariff != nil {
		method = p.tariff.t.PaymentMethod
	}
	b := locked[req.UserID]
	pay, err := a.rules.Rules.Pay(total, method, pricing.ChoiceNone, pricing.Balances{Main: b.Main, Bonus: b.Bonus})
	if err != nil {
		if errors.Is(err, pricing.ErrInsufficientFunds) {
			return out, &Error{Field: "balance", Message: MsgShort, Params: map[string]string{"price": total.Tenge()}}
		}
		return out, err
	}
	shares, ok := SplitBonus(prices, pay.Bonus)
	if !ok {
		return out, fmt.Errorf("reservations: bonus share of %d does not fit prices %v", pay.Bonus, prices)
	}

	code, err := newCode(ctx, q, req.ClubID, now)
	if err != nil {
		return out, err
	}
	user, err := q.AppBookingUser(ctx, store.AppBookingUserParams{ClubID: req.ClubID, UserID: req.UserID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, ErrNotFound
		}
		return out, err
	}
	var tariffID *int64
	var tariffName any
	if p.tariff != nil {
		tariffID, tariffName = &p.tariff.id, p.tariff.name
	}
	endsAt := req.StartsAt.Add(time.Duration(p.minutes) * time.Minute)
	perPC := make([]map[string]any, len(pcs))
	for i, pc := range pcs {
		id, err := Create(ctx, q, a.info, req.UserID, Booking{
			ComputerID: pc.ID, UserID: req.UserID, CustomerName: user.Name, CustomerPhone: &user.Phone,
			StartsAt: req.StartsAt, EndsAt: endsAt,
			FromApp: true, PaidMain: prices[i] - shares[i], PaidBonus: shares[i], TariffID: tariffID, Code: &code,
		}, now)
		if err != nil {
			return out, err
		}
		out.ReservationIDs = append(out.ReservationIDs, id)
		perPC[i] = map[string]any{"reservation_id": id, "computer_id": pc.ID, "main": int64(prices[i] - shares[i]), "bonus": int64(shares[i])}
	}
	out.Code, out.Price, out.Main, out.Bonus, out.Minutes = code, total, pay.Main, pay.Bonus, p.minutes

	if total > 0 {
		if out.LedgerTransactionID, err = chargeGroup(ctx, q, req, env, out, tariffID, perPC); err != nil {
			return out, err
		}
	}
	// $this->loyalty->addSpending($user->getKey(), $main) — только основная часть (BR-042).
	if err := billing.AddSpending(ctx, q, req.ClubID, req.UserID, pay.Main, now); err != nil {
		return out, err
	}
	joined := strings.Join(names, ", ")
	from := req.StartsAt.In(a.info.Loc).Format("02.01.2006 15:04")
	if _, err := activity.Record(ctx, q, activity.Entry{
		ClubID: req.ClubID, Event: "reservation.paid", UserID: req.UserID, SubjectType: subjectBooking, SubjectID: out.ReservationIDs[0],
		At: now, Meta: env.Meta,
		Details: map[string]any{
			"amount": total.Tenge(), "currency": a.rules.Currency,
			"main_balance_amount": pay.Main.Tenge(), "bonus_balance_amount": pay.Bonus.Tenge(),
			"computer": joined, "zone": strings.Join(g.zoneNames, ", "), "tariff": tariffName,
			"starts_at": from, "duration_minutes": p.minutes,
		},
	}.Translated(logPaid, map[string]string{"computer": joined, "from": from})); err != nil {
		return out, err
	}
	if out.LedgerTransactionID > 0 {
		if err := balanceChanged(ctx, q, a.info, req.UserID, env); err != nil {
			return out, err
		}
	}
	if req.IdempotencyKey != "" {
		if err := billingsvc.RememberRequest(ctx, q, req.ClubID, req.IdempotencyKey, out); err != nil {
			return out, err
		}
	}
	return out, nil
}

// pricedGroup — ПК группы брони с ценами (порядок pcs).
type pricedGroup struct {
	prices    []money.Tiyn
	names     []string
	zoneNames []string // зоны группы по возрастанию id
	total     money.Tiyn
}

// priceGroup — часть GuestBookings::book() до блокировки баланса: каждый ПК — зона с ценой часа (по часам) или зона
// тарифа, не служебный; цена на ПК (BR-174); названия зон группы.
func (a app) priceGroup(pcs []store.AppBookingLockComputersRow, zoneRows []store.AppBookingZonesRow, p planned) (pricedGroup, error) {
	zones := make(map[int64]store.AppBookingZonesRow, len(zoneRows))
	for _, z := range zoneRows {
		zones[z.ID] = z
	}
	g := pricedGroup{prices: make([]money.Tiyn, len(pcs)), names: make([]string, len(pcs))}
	for i, pc := range pcs {
		g.names[i] = club.ComputerName(pc.DisplayName, pc.DeviceName, pc.Number)
		zone, hasZone := zones[pc.ClubZoneID]
		fits := hasZone
		if p.tariff == nil {
			fits = fits && zone.PricePerHour != nil
		} else {
			fits = fits && (len(p.tariff.t.ZoneIDs) == 0 || slices.Contains(p.tariff.t.ZoneIDs, zone.ID))
		}
		if !fits || pc.IsReserved {
			return g, &Error{Field: "club_computer_ids", Message: MsgCannotBookName, Params: map[string]string{"computer": g.names[i]}}
		}
		var err error
		if g.prices[i], err = a.price(zone.PricePerHour, p); err != nil {
			return g, err
		}
		g.total += g.prices[i]
	}
	// $zones->pluck('name')->implode(', '): зоны ПК группы (ClubZone::whereKey(...)->get() — по первичному ключу).
	zoneIDs := make([]int64, 0, len(pcs))
	for _, pc := range pcs {
		if !slices.Contains(zoneIDs, pc.ClubZoneID) {
			zoneIDs = append(zoneIDs, pc.ClubZoneID)
		}
	}
	slices.Sort(zoneIDs)
	for _, id := range zoneIDs {
		g.zoneNames = append(g.zoneNames, zones[id].Name)
	}
	return g, nil
}

// pendingGroups — сколько броней гостя впереди: брони, сделанные вместе (один код), считаются одной.
func pendingGroups(ahead []store.AppBookingAheadRow) int {
	var seen []string
	for _, r := range ahead {
		key := "id" + strconv.FormatInt(r.ID, 10)
		if r.Code != nil {
			key = *r.Code
		}
		if !slices.Contains(seen, key) {
			seen = append(seen, key)
		}
	}
	return len(seen)
}

// chargeGroup — $balance->update([...]) в book(): основной → выручка за время, бонусы → бонусный фонд (как покупка
// времени), одной операцией reservation_payment на группу; возвращает id операции ledger.
func chargeGroup(ctx context.Context, q *store.Queries, req BookRequest, env Env, out BookResult, tariffID *int64, perPC []map[string]any) (int64, error) {
	meta, err := json.Marshal(map[string]any{
		"code": out.Code, "price": int64(out.Price), "minutes": out.Minutes, "tariff_id": tariffID, "starts_at": req.StartsAt.UTC(),
		"computers": perPC,
	})
	if err != nil {
		return 0, err
	}
	res, err := billing.Charge(ctx, q, billing.ChargeParams{
		ClubID: req.ClubID, UserID: req.UserID, Kind: ledger.KindReservationPayment, Revenue: ledger.AccountRevenueTime,
		Main: out.Main, Bonus: out.Bonus,
		IdempotencyKey: "reservation_payment:" + strconv.FormatInt(out.ReservationIDs[0], 10),
		Refs:           ledger.Refs{ReservationID: out.ReservationIDs[0]}, Meta: meta, Actor: env.Actor,
	})
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficientFunds) {
			// Проверка под той же блокировкой уже прошла; сюда не попасть, но текст — старый.
			return 0, &Error{Field: "balance", Message: MsgShort, Params: map[string]string{"price": out.Price.Tenge()}}
		}
		return 0, err
	}
	return res.Ledger.TransactionID, nil
}

// newCode — GuestBookings::newCode(): 6 цифр, которых нет у броней клуба, закончившихся не раньше чем сутки назад.
// В старом коде проверка шла без блокировки — две брони одновременно могли получить один код (GAP-058). Здесь
// выдача кодов клуба сериализована транзакционной advisory-блокировкой: она держится до конца транзакции, то есть
// до вставки броней с этим кодом.
func newCode(ctx context.Context, q *store.Queries, clubID int64, now time.Time) (string, error) {
	if err := q.AppBookingLockCodes(ctx, clubID); err != nil {
		return "", err
	}
	limit := big.NewInt(1_000_000)
	for range 200 {
		n, err := rand.Int(rand.Reader, limit) // random_int(0, 999999)
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%06d", n.Int64())
		taken, err := q.AppBookingCodeTaken(ctx, store.AppBookingCodeTakenParams{ClubID: clubID, Code: code, Since: now.AddDate(0, 0, -1)})
		if err != nil {
			return "", err
		}
		if !taken {
			return code, nil
		}
	}
	return "", errors.New("reservations: no free booking code")
}

// Group — брони, сделанные вместе с этой (группа ПК по коду), как одна бронь приложения. ErrNotFound — брони с
// таким id у гостя нет.
func Group(ctx context.Context, q *store.Queries, clubID, userID, id int64, now time.Time) (AppBooking, error) {
	info, err := club.Load(ctx, q, clubID)
	if err != nil {
		return AppBooking{}, err
	}
	rows, err := groupRows(ctx, q, clubID, userID, id)
	if err != nil {
		return AppBooking{}, err
	}
	return present(rows, GuestMayCancel(info.Settings), now), nil
}

func groupRows(ctx context.Context, q *store.Queries, clubID, userID, id int64) ([]bookingRow, error) {
	own, err := q.AppBookingOfUser(ctx, store.AppBookingOfUserParams{ClubID: clubID, UserID: &userID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.AppBookingGroup(ctx, store.AppBookingGroupParams{ClubID: clubID, UserID: &userID, ID: id, Code: own.Code})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	out := make([]bookingRow, len(rows))
	for i, r := range rows {
		out[i] = bookingRow(r)
	}
	return out, nil
}

// CancelByGuest — GuestBookings::cancel(): гость отменяет свою бронь (все её ПК) до начала, если клуб разрешает, —
// деньги возвращаются. Чужая бронь — ErrNotFound (404). Все ПК группы отменяются одной транзакцией; состояние
// каждой брони проверяется под блокировкой (все ПК группы по возрастанию id → брони): если хоть одна уже началась или закрыта — ошибка, и
// транзакция вызывающего откатывается целиком.
func CancelByGuest(ctx context.Context, q *store.Queries, clubID, userID, id int64, env Env) error {
	info, err := club.Load(ctx, q, clubID)
	if err != nil {
		return err
	}
	rows, err := groupRows(ctx, q, clubID, userID, id)
	if err != nil {
		return err
	}
	if !GuestMayCancel(info.Settings) {
		return &Error{Field: "reservation", Message: MsgGuestNoCancel}
	}
	// Все ПК группы — сразу и по возрастанию id, как в Book (один порядок блокировок: отмена и встречная бронь тех
	// же ПК не блокируют друг друга накрест, даже если id броней группы идут не в порядке id ПК).
	pcs := make([]int64, 0, len(rows))
	for _, r := range rows {
		pcs = append(pcs, r.ClubComputerID)
	}
	if _, err := q.AppBookingLockComputers(ctx, store.AppBookingLockComputersParams{ClubID: clubID, Ids: pcs}); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := closeBooking(ctx, q, info, r.ID, env, false, true); err != nil {
			return err
		}
	}
	return nil
}
