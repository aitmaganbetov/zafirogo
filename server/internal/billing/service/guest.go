package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// CashRecorder — кассовый слой (server/internal/cash), который подключает гостевую продажу к смене.
// Оба метода вызываются в транзакции сервиса (q — её запросы, app.club_id уже стоит).
type CashRecorder interface {
	// LockOpenShift блокирует открытую смену клуба:
	// CashShift::query()->where('status', 'open')->lockForUpdate()->first() (GuestSessions::sell() стр. 190).
	// Нет открытой смены — ok = false (сервис вернёт «Сначала откройте смену в кассе.»).
	LockOpenShift(ctx context.Context, q *store.Queries, clubID int64) (shiftID int64, ok bool, err error)

	// RecordGuestSessionIncome записывает приход гостевой сессии в смену: cash_transactions
	// {type income, amount, description, user_id = кассир, payment_method, income_kind guest_session,
	// idempotency_key} и ledger-операцию guest_session (cash_drawer −Amount, revenue_time +Amount,
	// Refs.CashShiftID/CashTransactionID, тот же ключ идемпотентности) с её аудитом.
	// Amount может быть 0 (тариф за 0): старый код всё равно создавал приход 0.00; проводок тогда нет.
	// Сервис захватывает ключ раньше (billing_requests), поэтому повтор сюда не доходит.
	// Laravel: GuestSessions::sell() стр. 233–245 ($shift->transactions()->create(), журнал cash.income и
	// computer.guest_session).
	RecordGuestSessionIncome(ctx context.Context, q *store.Queries, in GuestIncome) (GuestIncomeResult, error)
}

// GuestIncome — приход гостевой сессии в смену.
type GuestIncome struct {
	ClubID         int64
	ShiftID        int64
	CashierID      int64
	Amount         money.Tiyn
	PaymentMethod  string // cash, card, qr
	Description    string // «Гостевая сессия: :what, ПК :computers (:payment)» (ru)
	IdempotencyKey string
	Actor          Actor
	// Детали для журнала (details старого кода).
	Computers string // имена ПК через ", "
	Minutes   int64
	Tariff    string // имя тарифа или «—»
}

// GuestIncomeResult — записанный приход.
type GuestIncomeResult struct {
	CashTransactionID   int64
	LedgerTransactionID int64 // 0 — проводок нет (сумма 0)
}

// GuestPaymentMethods — GuestSessions::PAYMENT_METHODS.
var GuestPaymentMethods = []string{"cash", "card", "qr"}

// guestPaymentLabels — [__('наличные'), __('карта'), __('QR-код')] (ключи перевода).
var guestPaymentLabels = map[string]string{"cash": "наличные", "card": "карта", "qr": "QR-код"}

// GuestSessionRequest — продажа времени гостю без аккаунта на кассе (POST /club-computers/guest-session).
type GuestSessionRequest struct {
	ClubID         int64
	ComputerIDs    []int64
	TariffID       *int64 // nil — минуты по цене часа зоны
	Minutes        int64  // при TariffID == nil: 1…720 (валидация запроса)
	PaymentMethod  string
	CashierID      int64
	IdempotencyKey string
	Actor          Actor
}

// GuestComputerResult — гостевая сессия на одном ПК.
type GuestComputerResult struct {
	ComputerID    int64      `json:"computer_id"`
	Name          string     `json:"name"`
	SessionID     string     `json:"session_id"`
	SessionEndsAt time.Time  `json:"session_ends_at"`
	Extends       bool       `json:"extends"`
	Price         money.Tiyn `json:"price"`
}

// GuestSessionResult — ответ продажи (amount, minutes, ПК).
type GuestSessionResult struct {
	Amount              money.Tiyn            `json:"amount"`
	Minutes             int64                 `json:"minutes"`
	Computers           []GuestComputerResult `json:"computers"`
	Description         string                `json:"description"`
	CashTransactionID   int64                 `json:"cash_transaction_id"`
	LedgerTransactionID int64                 `json:"ledger_transaction_id"`
	Replayed            bool                  `json:"-"`
}

// SellGuestSession — гостевая сессия на выбранных ПК, оплата в открытую смену.
//
// Laravel: app/Services/GuestSessions.php sell() (стр. 187–249), priceFor() (стр. 251–266):
// открытая смена FOR UPDATE → активный тариф (без блокировки) в продаже и в периоде действия →
// ПК FOR UPDATE по id (не все найдены → «Выберите ПК на карте клуба.») → для каждого ПК: цена → занят →
// бронь (чужая любая, гость = 0) → сессия (продление гостевой — тот же id) → один приход в кассу на сумму.
// Цена тарифа — без скидки, минуты — по цене часа зоны каждого ПК (BR-038). Лимит количества тарифа не
// расходуется (Решение Q-007 (docs/DECISIONS.md)).
func (s *Service) SellGuestSession(ctx context.Context, req GuestSessionRequest) (GuestSessionResult, error) {
	if s.cash == nil {
		return GuestSessionResult{}, ErrNoCashRecorder
	}
	if !slices.Contains(GuestPaymentMethods, req.PaymentMethod) {
		return GuestSessionResult{}, fmt.Errorf("%w: payment method %q", ErrInvalidInput, req.PaymentMethod)
	}
	var out GuestSessionResult
	err := s.tx(ctx, req.ClubID, func(q *store.Queries) error {
		out = GuestSessionResult{}
		ids := slices.Clone(req.ComputerIDs)
		slices.Sort(ids)
		ids = slices.Compact(ids) // count(array_unique($computerIds))
		replayed, err := s.claim(ctx, q, req.ClubID, req.IdempotencyKey, opGuestSession, struct {
			ComputerIDs   []int64
			TariffID      *int64
			Minutes       int64
			PaymentMethod string
			CashierID     int64
		}{ids, req.TariffID, req.Minutes, req.PaymentMethod, req.CashierID}, &out)
		if err != nil || replayed {
			out.Replayed = replayed
			return err
		}
		now := s.clock.Now()

		shiftID, ok, err := s.cash.LockOpenShift(ctx, q, req.ClubID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoOpenShift
		}
		club, err := loadClub(ctx, q, req.ClubID)
		if err != nil {
			return err
		}

		in := pricing.GuestSessionInput{Minutes: req.Minutes, Now: now, Location: club.loc}
		var tariffName string
		if req.TariffID != nil {
			t, err := q.GetActiveTariff(ctx, store.GetActiveTariffParams{ClubID: req.ClubID, ID: *req.TariffID})
			switch {
			case err == nil:
				zones, err := q.ListTariffZoneIDs(ctx, store.ListTariffZoneIDsParams{ClubID: req.ClubID, TariffID: t.ID})
				if err != nil {
					return err
				}
				pt, err := tariffRow(t).toPricing(zones)
				if err != nil {
					return err
				}
				in.Tariff = &pt
				tariffName = t.Name
			case errors.Is(err, pgx.ErrNoRows):
				in.TariffMissing = true
			default:
				return err
			}
		}

		rows, err := q.LockComputers(ctx, store.LockComputersParams{ClubID: req.ClubID, Ids: ids})
		if err != nil {
			return err
		}
		if len(rows) != len(ids) || len(ids) == 0 {
			// Проверка тарифа в старом коде идёт раньше, поэтому сначала его ошибка (ПК не передаются).
			_, qerr := pricing.QuoteGuestSession(in)
			return qerr
		}
		pcs := make([]computerRow, len(rows))
		zoneIDs := make([]int64, 0, len(rows))
		for i, r := range rows {
			pcs[i] = computerRow(r)
			zoneIDs = append(zoneIDs, r.ClubZoneID)
		}
		zones, err := q.ListZonesByIDs(ctx, store.ListZonesByIDsParams{ClubID: req.ClubID, Ids: zoneIDs})
		if err != nil {
			return err
		}
		prices := make(map[int64]*money.Tiyn, len(zones))
		for _, z := range zones {
			if z.PricePerHour != nil {
				p := money.Tiyn(*z.PricePerHour)
				prices[z.ID] = &p
			}
		}
		for _, pc := range pcs {
			in.Computers = append(in.Computers, pricing.GuestComputer{
				Name: pc.displayName(), Computer: pc.pricing(now), ZonePricePerHour: prices[pc.ClubZoneID],
			})
		}
		in.CheckBooking = func(i int, pc pricing.GuestComputer, endsAt time.Time) error {
			at, err := blocking(ctx, q, req.ClubID, pcs[i].ID, 0, club.gapMinutes, now, endsAt)
			if err != nil {
				return err
			}
			if at != nil {
				return ErrGuestBookedFrom.with(map[string]string{"computer": pc.Name, "time": at.In(club.loc).Format("15:04")})
			}
			return nil
		}
		quote, err := pricing.QuoteGuestSession(in)
		if err != nil {
			return err
		}

		names := make([]string, len(pcs))
		for i, pc := range pcs {
			c := quote.Computers[i]
			sid := ""
			if c.Extends {
				sid = *pc.CurrentSessionID
			} else {
				sid = s.newSessionID()
			}
			ends := session.StoredTime(c.SessionEndsAt)
			if err := q.SetComputerSession(ctx, store.SetComputerSessionParams{
				ClubID: req.ClubID, ID: pc.ID, SessionID: &sid, SessionUserID: nil,
				SessionGuest: true, SessionEndsAt: &ends, Now: &now,
			}); err != nil {
				return err
			}
			names[i] = pc.displayName()
			out.Computers = append(out.Computers, GuestComputerResult{
				ComputerID: pc.ID, Name: names[i], SessionID: sid, SessionEndsAt: ends, Extends: c.Extends, Price: c.Price,
			})
			if err := AddEvent(ctx, q, req.ClubID, "computer.state", computerEvent(pc.ID, sid, 0, true, &ends)); err != nil {
				return err
			}
		}
		out.Amount, out.Minutes = quote.Total, quote.Minutes

		joined := strings.Join(names, ", ")
		what := minutesRu(quote.Minutes)
		detailTariff := "—"
		if in.Tariff != nil {
			what = "тариф «" + tariffName + "»"
			detailTariff = tariffName
		}
		out.Description = "Гостевая сессия: " + what + ", ПК " + joined + " (" + guestPaymentLabels[req.PaymentMethod] + ")"
		rec, err := s.cash.RecordGuestSessionIncome(ctx, q, GuestIncome{
			ClubID: req.ClubID, ShiftID: shiftID, CashierID: req.CashierID, Amount: quote.Total,
			PaymentMethod: req.PaymentMethod, Description: out.Description, IdempotencyKey: req.IdempotencyKey,
			Actor: req.Actor, Computers: joined, Minutes: quote.Minutes, Tariff: detailTariff,
		})
		if err != nil {
			return err
		}
		out.CashTransactionID, out.LedgerTransactionID = rec.CashTransactionID, rec.LedgerTransactionID
		return remember(ctx, q, req.ClubID, req.IdempotencyKey, out)
	})
	if err == nil && !out.Replayed && s.notifier != nil {
		now := s.clock.Now()
		for _, c := range out.Computers {
			started := now
			if c.Extends {
				started = time.Time{}
			}
			NotifySessionStarted(ctx, s.notifier, req.ClubID, c.ComputerID, c.SessionID, 0, true, started, c.SessionEndsAt, req.CashierID)
		}
	}
	return out, err
}

// minutesRu — trans_choice(':count минута|:count минуты|:count минут', $n) для русского языка
// (правило множественного числа ru: 1, 21 … — минута; 2–4, 22–24 … — минуты; остальное — минут).
// Описание операции кассы хранится текстом (как в старом коде, где __() вызывался при записи).
func minutesRu(n int64) string {
	mod10, mod100 := n%10, n%100
	word := "минут"
	switch {
	case mod10 == 1 && mod100 != 11:
		word = "минута"
	case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
		word = "минуты"
	}
	return itoa(n) + " " + word
}
