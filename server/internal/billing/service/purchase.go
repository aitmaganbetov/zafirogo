package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Операции billing_requests.
const (
	opTariffPurchase  = "tariff_purchase"
	opMinutesPurchase = "minutes_purchase"
	opPackageStart    = "package_start"
	opGuestSession    = "guest_session"
)

// Источник части сессии (session_purchases.source, 0006).
const (
	sourceTariff  = string(session.SourceTariff)
	sourceMinutes = string(session.SourceMinutes)
	sourcePackage = string(session.SourcePackage)
)

// PurchaseResult — ответ покупки времени (поля ответа sell(): new_balance, new_bonus, minutes_added,
// session_id, session_ends_at) и то, что записано.
type PurchaseResult struct {
	NewBalance    money.Tiyn `json:"new_balance"`
	NewBonus      money.Tiyn `json:"new_bonus"`
	MinutesAdded  int64      `json:"minutes_added"`
	SessionID     string     `json:"session_id"`
	SessionEndsAt time.Time  `json:"session_ends_at"`
	// ExtendsSession — время добавлено к идущей сессии (тот же session_id).
	ExtendsSession bool       `json:"extends_session"`
	Price          money.Tiyn `json:"price"`
	MainPaid       money.Tiyn `json:"main_paid"`
	BonusPaid      money.Tiyn `json:"bonus_paid"`
	PurchaseID     int64      `json:"purchase_id"` // session_purchases.id
	// LedgerTransactionID — операция ledger (0 — цена 0: денег не двигалось).
	LedgerTransactionID int64 `json:"ledger_transaction_id"`
	// Replayed — ответ на повтор запроса с тем же ключом идемпотентности (ничего не записано).
	Replayed bool `json:"-"`
}

// TariffPurchaseRequest — покупка тарифа клиентом на ПК (POST /api/tariffs/{tariff}/purchase).
type TariffPurchaseRequest struct {
	ClubID         int64
	UserID         int64
	TariffID       int64
	ComputerID     int64
	Balance        pricing.BalanceChoice // "", main, bonus (валидация запроса — у вызывающего)
	IdempotencyKey string
	Actor          Actor
}

// PurchaseTariff — покупка тарифа в агенте.
//
// Laravel: app/Services/AgentTariffPurchaseService.php purchase() (стр. 62–101) и sell() (стр. 236–311).
// Порядок как в старом коде: тариф FOR UPDATE → членство FOR UPDATE → ПК FOR UPDATE → (баланс FOR UPDATE) →
// isAvailable (лимит количества — счётчик tariff_sales, Решение Q-007 (docs/DECISIONS.md)) → ensureComputerFor →
// зоны тарифа → цена → раскладка по балансам → бронь → списание (ledger tariff_purchase) → траты лояльности →
// часть сессии → сессия ПК. Баланс блокируется раньше проверок (в Laravel — внутри sell()): порядок
// блокировок тот же, отличается только то, что при отказе строка баланса тоже была заблокирована.
func (s *Service) PurchaseTariff(ctx context.Context, req TariffPurchaseRequest) (PurchaseResult, error) {
	var (
		out         PurchaseResult
		currencyOut string
	)
	err := s.tx(ctx, req.ClubID, func(q *store.Queries) error {
		out = PurchaseResult{}
		replayed, err := s.claim(ctx, q, req.ClubID, req.IdempotencyKey, opTariffPurchase, struct {
			UserID, TariffID, ComputerID int64
			Balance                      pricing.BalanceChoice
		}{req.UserID, req.TariffID, req.ComputerID, req.Balance}, &out)
		if err != nil || replayed {
			out.Replayed = replayed
			return err
		}
		now := s.clock.Now()
		club, err := loadClub(ctx, q, req.ClubID)
		if err != nil {
			return err
		}

		t, err := q.LockTariffForPurchase(ctx, store.LockTariffForPurchaseParams{ClubID: req.ClubID, ID: req.TariffID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("tariff", err)
			}
			return err
		}
		zones, err := q.ListTariffZoneIDs(ctx, store.ListTariffZoneIDsParams{ClubID: req.ClubID, TariffID: t.ID})
		if err != nil {
			return err
		}
		tariff, err := tariffRow(t).toPricing(zones)
		if err != nil {
			return err
		}
		m, err := q.LockMembershipForPurchase(ctx, store.LockMembershipForPurchaseParams{ClubID: req.ClubID, UserID: req.UserID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("membership", err)
			}
			return err
		}
		pc, err := lockComputer(ctx, q, req.ClubID, req.ComputerID)
		if err != nil {
			return err
		}
		sold, err := q.GetTariffSales(ctx, store.GetTariffSalesParams{ClubID: req.ClubID, TariffID: t.ID})
		if err != nil {
			return err
		}
		in := pricing.TariffPurchaseInput{
			Tariff: tariff, Rules: club.rules,
			// Laravel: LoyaltyProgram::discountFor() (BR-011).
			DiscountPercent: bonus.LoyaltyDiscountFor(club.bonus.LoyaltyLevelsInForce(), m.Discount, money.Tiyn(m.TotalSpent)),
			Role:            m.Role, PurchaseCount: sold, UserID: req.UserID,
			Computer: pc.pricing(now), Choice: req.Balance, Now: now, Location: club.loc,
		}
		quote, err := quoteWithBalance(ctx, q, req.ClubID, req.UserID, func(b pricing.Balances) (pricing.Purchase, error) {
			in.Balances = b
			return pricing.QuoteTariffPurchase(in)
		})
		if err != nil {
			return err
		}
		tariffID := t.ID
		zone := pc.ClubZoneID
		out, err = s.sell(ctx, q, sale{
			clubID: req.ClubID, userID: req.UserID, computer: pc, club: club, now: now, quote: quote,
			part: part{
				kind: session.KindTariff, tariffID: &tariffID, name: t.Name,
				// Laravel: purchase() стр. 85 — now()->addMinutes(usage_limit_minutes), хранится с точностью до секунды.
				expiresAt: session.TariffPartExpiresAt(session.TariffUsage{
					DurationMinutes: t.DurationMinutes, UsageLimitEnabled: t.UsageLimitEnabled, UsageLimitMinutes: t.UsageLimitMinutes,
				}, now),
				zoneID: &zone, source: sourceTariff,
			},
			ledgerKind: ledger.KindTariffPurchase, key: req.IdempotencyKey, actor: req.Actor,
			meta: map[string]any{"tariff_id": t.ID, "tariff": t.Name, "computer_id": pc.ID},
		})
		if err != nil {
			return err
		}
		currency, err := q.GetTariffCurrency(ctx, store.GetTariffCurrencyParams{ClubID: req.ClubID, ID: t.ID})
		if err != nil {
			return err
		}
		// Laravel: AgentTariffPurchaseService::purchase() (стр. 89–98) — журнал tariff.purchased.
		if err := RecordActivity(ctx, q, activity.Entry{
			ClubID: req.ClubID, Event: "tariff.purchased", Description: "Куплен тариф: " + t.Name,
			UserID: req.UserID, SubjectType: SubjectTariff, SubjectID: t.ID,
			Details: map[string]any{
				"amount": out.Price.Tenge(), "currency": currency,
				"main_balance_amount": (out.Price - out.BonusPaid).Tenge(), "bonus_balance_amount": out.BonusPaid.Tenge(),
				"duration_minutes": out.MinutesAdded, "computer_id": pc.ID, "session_id": out.SessionID,
				"session_ends_at": ISO8601(out.SessionEndsAt),
			},
		}); err != nil {
			return err
		}
		currencyOut = club.currency
		// Счётчик лимита количества: только продажи в агенте (Решение Q-007 (docs/DECISIONS.md)).
		if err := q.IncrementTariffSales(ctx, store.IncrementTariffSalesParams{ClubID: req.ClubID, TariffID: t.ID, Now: now}); err != nil {
			return err
		}
		return remember(ctx, q, req.ClubID, req.IdempotencyKey, out)
	})
	if err == nil && !out.Replayed {
		s.notifyPurchase(ctx, req.ClubID, req.UserID, req.ComputerID, out, currencyOut, req.Actor.UserID)
	}
	return out, err
}

// MinutesPurchaseRequest — покупка минут по цене часа зоны ПК (POST /api/guest/minutes/purchase).
type MinutesPurchaseRequest struct {
	ClubID         int64
	UserID         int64
	ComputerID     int64
	Minutes        int64 // 1…720 (валидация запроса)
	Balance        pricing.BalanceChoice
	IdempotencyKey string
	Actor          Actor
}

// PurchaseMinutes — поминутная покупка.
//
// Laravel: app/Services/AgentTariffPurchaseService.php purchaseMinutes() (стр. 110–138), hourlyPrice()
// (стр. 141–150), sell(). Порядок: ПК FOR UPDATE → зона (без блокировки) → нет цены часа → ensureComputerFor →
// цена (скидка, если клуб применяет её к поминутке и у клиента есть членство) → sell().
func (s *Service) PurchaseMinutes(ctx context.Context, req MinutesPurchaseRequest) (PurchaseResult, error) {
	return s.purchaseMinutes(ctx, req, opMinutesPurchase, struct {
		UserID, ComputerID, Minutes int64
		Balance                     pricing.BalanceChoice
	}{req.UserID, req.ComputerID, req.Minutes, req.Balance}, func(*money.Tiyn, pricing.PaymentRules, float64, pricing.Balances) (int64, error) {
		return req.Minutes, nil
	})
}

// minutesResolver — сколько минут покупать: обычная покупка — из запроса, «на весь баланс» — по балансу под
// блокировкой (whole_balance.go). zonePrice nil — у зоны нет цены часа.
type minutesResolver func(zonePrice *money.Tiyn, rules pricing.PaymentRules, discount float64, b pricing.Balances) (int64, error)

// purchaseMinutes — общая часть PurchaseMinutes и PurchaseWholeBalance (операция billing_requests op, тело
// запроса для идемпотентности claimBody); минуты — resolve под блокировкой баланса.
func (s *Service) purchaseMinutes(ctx context.Context, req MinutesPurchaseRequest, op string, claimBody any, resolve minutesResolver) (PurchaseResult, error) {
	var (
		out         PurchaseResult
		currencyOut string
	)
	err := s.tx(ctx, req.ClubID, func(q *store.Queries) error {
		out = PurchaseResult{}
		replayed, err := s.claim(ctx, q, req.ClubID, req.IdempotencyKey, op, claimBody, &out)
		if err != nil || replayed {
			out.Replayed = replayed
			return err
		}
		now := s.clock.Now()
		club, err := loadClub(ctx, q, req.ClubID)
		if err != nil {
			return err
		}
		pc, err := lockComputer(ctx, q, req.ClubID, req.ComputerID)
		if err != nil {
			return err
		}
		var zoneName string
		var zonePrice *money.Tiyn
		zone, err := q.GetZone(ctx, store.GetZoneParams{ClubID: req.ClubID, ID: pc.ClubZoneID})
		switch {
		case err == nil:
			zoneName = zone.Name
			if zone.PricePerHour != nil {
				p := money.Tiyn(*zone.PricePerHour)
				zonePrice = &p
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		// Laravel: hourlyPrice() — без членства скидки нет.
		var discount float64
		m, err := q.GetMembershipForPricing(ctx, store.GetMembershipForPricingParams{ClubID: req.ClubID, UserID: req.UserID})
		switch {
		case err == nil:
			discount = bonus.LoyaltyDiscountFor(club.bonus.LoyaltyLevelsInForce(), m.Discount, money.Tiyn(m.TotalSpent))
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		in := pricing.MinutesPurchaseInput{
			ZonePricePerHour: zonePrice, Rules: club.rules, DiscountPercent: discount, UserID: req.UserID,
			Computer: pc.pricing(now), Choice: req.Balance, Now: now,
		}
		quote, err := quoteWithBalance(ctx, q, req.ClubID, req.UserID, func(b pricing.Balances) (pricing.Purchase, error) {
			minutes, err := resolve(zonePrice, club.rules, discount, b)
			if err != nil {
				return pricing.Purchase{}, err
			}
			in.Balances, in.Minutes = b, minutes
			return pricing.QuoteMinutesPurchase(in)
		})
		if err != nil {
			return err
		}
		zoneID := pc.ClubZoneID
		out, err = s.sell(ctx, q, sale{
			clubID: req.ClubID, userID: req.UserID, computer: pc, club: club, now: now, quote: quote,
			part:       part{kind: session.KindMinutes, name: zoneName, zoneID: &zoneID, source: sourceMinutes},
			ledgerKind: ledger.KindMinutesPurchase, key: req.IdempotencyKey, actor: req.Actor,
			meta: map[string]any{"zone_id": zoneID, "zone": zoneName, "minutes": quote.Minutes, "computer_id": pc.ID},
		})
		if err != nil {
			return err
		}
		// Laravel: AgentTariffPurchaseService::purchaseMinutes() (стр. 124–135) — журнал time.purchased;
		// price_per_hour — hourlyPrice(): цена часа зоны со скидкой клиента (formatCents).
		hourly, err := pricing.HourlyPrice(*zonePrice, club.rules, discount)
		if err != nil {
			return err
		}
		if err := RecordActivity(ctx, q, activity.Entry{
			ClubID: req.ClubID, Event: "time.purchased",
			Description: "Куплено " + itoa(out.MinutesAdded) + " мин. в зоне " + zoneName,
			UserID:      req.UserID, SubjectType: SubjectZone, SubjectID: zoneID,
			Details: map[string]any{
				"amount": out.Price.Tenge(), "currency": club.currency, "price_per_hour": hourly.Tenge(),
				"main_balance_amount": (out.Price - out.BonusPaid).Tenge(), "bonus_balance_amount": out.BonusPaid.Tenge(),
				"duration_minutes": out.MinutesAdded, "computer_id": pc.ID, "session_id": out.SessionID,
				"session_ends_at": ISO8601(out.SessionEndsAt),
			},
		}); err != nil {
			return err
		}
		currencyOut = club.currency
		return remember(ctx, q, req.ClubID, req.IdempotencyKey, out)
	})
	if err == nil && !out.Replayed {
		s.notifyPurchase(ctx, req.ClubID, req.UserID, req.ComputerID, out, currencyOut, req.Actor.UserID)
	}
	return out, err
}

// PackageStartRequest — запуск сохранённого пакета (POST /api/guest/packages/{package}/start).
type PackageStartRequest struct {
	ClubID         int64
	UserID         int64
	PackageID      int64
	ComputerID     int64
	IdempotencyKey string
	Actor          Actor
}

// StartPackage — запуск сохранённого времени тарифа без оплаты.
//
// Laravel: app/Services/AgentTariffPurchaseService.php startPackage() (стр. 171–203): пакет клиента FOR UPDATE →
// ПК FOR UPDATE → минуты min(остаток, целых минут до срока) (≤ 0 → «Срок абонемента истёк.»; удаление пакета
// в старом коде откатывалось исключением — пакет оставался до суточной задачи, GAP-014. Решение Q-011
// (docs/DECISIONS.md): после отката пакет сгорает в отдельной транзакции — ledger package_expired и журнал) →
// ensureComputerFor → зоны тарифа (withTrashed) → период действия → sell(0, main_balance_only, main) →
// удаление пакета. Ledger минут: операция package_start, минус весь остаток пакета (пакет удаляется
// целиком, даже если срок урезал минуты; Решение Q-011 (docs/DECISIONS.md)).
func (s *Service) StartPackage(ctx context.Context, req PackageStartRequest) (PurchaseResult, error) {
	var out PurchaseResult
	err := s.tx(ctx, req.ClubID, func(q *store.Queries) error {
		out = PurchaseResult{}
		replayed, err := s.claim(ctx, q, req.ClubID, req.IdempotencyKey, opPackageStart, struct {
			UserID, PackageID, ComputerID int64
		}{req.UserID, req.PackageID, req.ComputerID}, &out)
		if err != nil || replayed {
			out.Replayed = replayed
			return err
		}
		now := s.clock.Now()
		club, err := loadClub(ctx, q, req.ClubID)
		if err != nil {
			return err
		}
		pkg, err := q.LockTimePackage(ctx, store.LockTimePackageParams{ClubID: req.ClubID, UserID: req.UserID, ID: req.PackageID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound("time package", err)
			}
			return err
		}
		pc, err := lockComputer(ctx, q, req.ClubID, req.ComputerID)
		if err != nil {
			return err
		}
		// $package->tariff — с мягко удалёнными; удалённый физически (tariff_id NULL) — без ограничений.
		var tariff *pricing.Tariff
		if pkg.TariffID != nil {
			t, err := q.GetTariffWithTrashed(ctx, store.GetTariffWithTrashedParams{ClubID: req.ClubID, ID: *pkg.TariffID})
			switch {
			case err == nil:
				zones, err := q.ListTariffZoneIDs(ctx, store.ListTariffZoneIDsParams{ClubID: req.ClubID, TariffID: t.ID})
				if err != nil {
					return err
				}
				pt, err := tariffRow{
					ID: t.ID, Name: t.Name, DurationMinutes: t.DurationMinutes, Price: t.Price, PaymentMethod: t.PaymentMethod,
					DiscountEnabled: t.DiscountEnabled, IsActive: t.IsActive, UsageLimitEnabled: t.UsageLimitEnabled,
					UsageLimitMinutes: t.UsageLimitMinutes, QuantityLimitEnabled: t.QuantityLimitEnabled,
					QuantityLimit: t.QuantityLimit, UserRoles: t.UserRoles, SalePeriods: t.SalePeriods,
					ValidityPeriods: t.ValidityPeriods,
				}.toPricing(zones)
				if err != nil {
					return err
				}
				tariff = &pt
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
		}
		in := pricing.PackageStartInput{
			MinutesLeft: pkg.MinutesLeft, ExpiresAt: pkg.ExpiresAt, Tariff: tariff, UserID: req.UserID,
			Computer: pc.pricing(now), Now: now, Location: club.loc,
		}
		quote, err := quoteWithBalance(ctx, q, req.ClubID, req.UserID, func(b pricing.Balances) (pricing.Purchase, error) {
			in.Balances = b
			return pricing.QuoteStartPackage(in)
		})
		if err != nil {
			return err
		}
		zoneID := pc.ClubZoneID
		expires := session.StoredTime(pkg.ExpiresAt)
		out, err = s.sell(ctx, q, sale{
			clubID: req.ClubID, userID: req.UserID, computer: pc, club: club, now: now, quote: quote,
			part: part{
				kind: session.KindTariff, tariffID: pkg.TariffID, name: pkg.Name, expiresAt: &expires,
				zoneID: &zoneID, source: sourcePackage,
			},
			key: req.IdempotencyKey, actor: req.Actor,
		})
		if err != nil {
			return err
		}
		// Ledger минут: пакет израсходован целиком (Q-011: сгоревшая из-за срока часть — тоже движение).
		meta, err := json.Marshal(map[string]any{
			"started_minutes": quote.Minutes, "lost_minutes": pkg.MinutesLeft - quote.Minutes,
			"computer_id": pc.ID, "session_id": out.SessionID,
		})
		if err != nil {
			return err
		}
		var tariffID int64
		if pkg.TariffID != nil {
			tariffID = *pkg.TariffID
		}
		if pkg.MinutesLeft > 0 {
			res, err := ledger.Post(ctx, q, ledger.Operation{
				ClubID: req.ClubID, Kind: ledger.KindPackageStart, IdempotencyKey: req.IdempotencyKey,
				Minutes: []ledger.MinuteEntry{{UserID: req.UserID, TimePackageID: pkg.ID, TariffID: tariffID, Minutes: -pkg.MinutesLeft}},
				Meta:    meta, ActorUserID: req.Actor.UserID, Refs: ledger.Refs{SessionID: out.SessionID},
			})
			if err != nil {
				return err
			}
			out.LedgerTransactionID = res.TransactionID
		}
		if err := q.DeleteTimePackage(ctx, store.DeleteTimePackageParams{ClubID: req.ClubID, ID: pkg.ID}); err != nil {
			return err
		}
		// Laravel: AgentTariffPurchaseService::startPackage() (стр. 193–197) — журнал time.package_started.
		if err := RecordActivity(ctx, q, activity.Entry{
			ClubID: req.ClubID, Event: "time.package_started",
			Description: "Запущен абонемент " + pkg.Name + ": " + itoa(out.MinutesAdded) + " мин.",
			UserID:      req.UserID, SubjectType: SubjectComputer, SubjectID: pc.ID,
			Details: map[string]any{
				"minutes": out.MinutesAdded, "session_id": out.SessionID, "session_ends_at": ISO8601(out.SessionEndsAt),
			},
		}); err != nil {
			return err
		}
		return remember(ctx, q, req.ClubID, req.IdempotencyKey, out)
	})
	if errors.Is(err, pricing.ErrPackageExpired) {
		// Решение Q-011 (docs/DECISIONS.md): основная транзакция откачена (ключ идемпотентности не занят),
		// пакет сгорает отдельно. Ошибка сгорания не заменяет ответ клиенту — пакет добьёт суточная задача.
		if berr := s.burnIfExpired(ctx, req); berr != nil {
			return out, errors.Join(err, fmt.Errorf("service: burn expired package: %w", berr))
		}
	}
	if err == nil && !out.Replayed {
		s.notifyPurchase(ctx, req.ClubID, req.UserID, req.ComputerID, out, "", req.Actor.UserID)
	}
	return out, err
}

// notifyPurchase — после коммита покупки: сессия на ПК (новая или продлённая) и балансы, если деньги списаны.
func (s *Service) notifyPurchase(ctx context.Context, clubID, userID, computerID int64, out PurchaseResult, currency string, issuedBy int64) {
	if s.notifier == nil {
		return
	}
	var started time.Time
	if !out.ExtendsSession {
		started = s.clock.Now()
	}
	NotifySessionStarted(ctx, s.notifier, clubID, computerID, out.SessionID, userID, false, started, out.SessionEndsAt, issuedBy)
	if out.LedgerTransactionID > 0 && out.MainPaid+out.BonusPaid > 0 {
		NotifyBalance(ctx, s.notifier, clubID, userID, out.NewBalance, out.NewBonus, currency)
	}
}

// quoteWithBalance блокирует баланс клиента и считает покупку. Нет строки баланса (в Laravel
// MemberBalance::firstOrFail() внутри sell(), после всех проверок покупки) — сначала проверки покупки
// с нулевыми балансами: их ошибка важнее; ошибки оплаты в этом случае — 404, как в старом коде.
func quoteWithBalance(ctx context.Context, q *store.Queries, clubID, userID int64, quote func(pricing.Balances) (pricing.Purchase, error)) (pricing.Purchase, error) {
	locked, err := ledger.LockBalances(ctx, q, clubID, userID)
	if err != nil {
		if !errors.Is(err, ledger.ErrBalanceNotFound) {
			return pricing.Purchase{}, err
		}
		if _, qerr := quote(pricing.Balances{}); qerr != nil && !isPaymentError(qerr) {
			return pricing.Purchase{}, qerr
		}
		return pricing.Purchase{}, notFound("member balance", err)
	}
	b := locked[userID]
	return quote(pricing.Balances{Main: b.Main, Bonus: b.Bonus})
}

// isPaymentError — ошибка раскладки по балансам (sell(), после блокировки баланса).
func isPaymentError(err error) bool {
	for _, e := range []error{pricing.ErrMainBalanceOnly, pricing.ErrBonusNotAllowed, pricing.ErrBonusPercentLimit,
		pricing.ErrBonusNotEnough, pricing.ErrInsufficientFunds} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// part — часть сессии (session_purchases) без сумм и минут.
type part struct {
	kind      session.Kind
	tariffID  *int64
	name      string
	expiresAt *time.Time
	zoneID    *int64
	source    string
}

type sale struct {
	clubID, userID int64
	computer       computerRow
	club           clubContext
	now            time.Time
	quote          pricing.Purchase
	part           part
	ledgerKind     ledger.Kind // пусто — без денег (запуск пакета)
	key            string
	actor          Actor
	meta           map[string]any
}

// sell — вторая половина AgentTariffPurchaseService::sell() (стр. 271–310) после раскладки по балансам:
// продление или новая сессия → бронь → списание → траты лояльности → часть сессии → сессия ПК.
func (s *Service) sell(ctx context.Context, q *store.Queries, in sale) (PurchaseResult, error) {
	pay := in.quote.Payment
	// Laravel: $hasActiveSession = hasPaidSession(); $sessionId = …; $sessionEndsAt = (…)->addMinutes($minutes).
	ext := session.Extend(in.computer.session(), in.quote.Minutes, in.now)
	if ext.Extends != in.quote.ExtendsSession {
		return PurchaseResult{}, fmt.Errorf("service: session extension mismatch on computer %d", in.computer.ID)
	}
	sessionID := ext.SessionID
	if !ext.Extends {
		sessionID = s.newSessionID()
	}

	// Время, заходящее на чужую бронь, не продаётся; своя бронь клиента отмечается выполненной.
	// Laravel: ComputerReservations::blocking() / markArrivedBy() с $sessionEndsAt в памяти (не усечённым).
	if at, err := blocking(ctx, q, in.clubID, in.computer.ID, in.userID, in.club.gapMinutes, in.now, in.quote.SessionEndsAt); err != nil {
		return PurchaseResult{}, err
	} else if at != nil {
		// $booking->starts_at->format('H:i'): модель в часовом поясе CurrentClub::timezone(), а для api/* он UTC
		// (SetCurrentClub::handle() стр. 89–91 → useUtc()), и клиент в агенте видел время брони в UTC.
		// Решение SV-9 (docs/DECISIONS.md): время — в часовом поясе клуба (clubs.timezone), как в гостевой продаже.
		return PurchaseResult{}, ErrBookedFrom.with(map[string]string{"time": at.In(in.club.loc).Format("15:04")})
	}
	if err := markArrivedBy(ctx, q, in.clubID, in.computer.ID, in.userID, in.club.gapMinutes, in.now, in.quote.SessionEndsAt); err != nil {
		return PurchaseResult{}, err
	}

	out := PurchaseResult{
		NewBalance: pay.After.Main, NewBonus: pay.After.Bonus, MinutesAdded: in.quote.Minutes,
		SessionID: sessionID, SessionEndsAt: ext.EndsAt, ExtendsSession: ext.Extends,
		Price: in.quote.Price, MainPaid: pay.Main, BonusPaid: pay.Bonus,
	}

	// Списание (Laravel: $balance->update([...])): основной → выручка за время, бонусы → бонусный фонд.
	if in.ledgerKind != "" && pay.Main+pay.Bonus > 0 {
		meta := map[string]any{"price": int64(in.quote.Price), "minutes": in.quote.Minutes, "session_id": sessionID}
		for k, v := range in.meta {
			meta[k] = v
		}
		body, err := json.Marshal(meta)
		if err != nil {
			return PurchaseResult{}, err
		}
		res, err := billing.Charge(ctx, q, billing.ChargeParams{
			ClubID: in.clubID, UserID: in.userID, Kind: in.ledgerKind, Revenue: ledger.AccountRevenueTime,
			Main: pay.Main, Bonus: pay.Bonus, IdempotencyKey: in.key,
			Refs: ledger.Refs{SessionID: sessionID}, Meta: body, Actor: in.actor,
		})
		if err != nil {
			if errors.Is(err, ledger.ErrInsufficientFunds) {
				// Проверка под той же блокировкой уже прошла в pricing; сюда не попасть, но текст — старый.
				return PurchaseResult{}, pricing.ErrInsufficientFunds
			}
			return PurchaseResult{}, err
		}
		out.LedgerTransactionID = res.Ledger.TransactionID
		out.NewBalance, out.NewBonus = res.NewBalance, res.NewBonusBalance
		if _, ok := res.Ledger.BalanceAfter(ledger.Main(in.userID)); !ok {
			out.NewBalance = pay.After.Main
		}
		if _, ok := res.Ledger.BalanceAfter(ledger.Bonus(in.userID)); !ok {
			out.NewBonus = pay.After.Bonus
		}
	}
	// Laravel: $this->loyalty->addSpending($user->getKey(), $mainPayment) — только основной баланс (BR-042).
	if err := addSpending(ctx, q, in.clubID, in.userID, pay.Main, in.now); err != nil {
		return PurchaseResult{}, err
	}
	source := in.part.source
	id, err := q.InsertSessionPurchase(ctx, store.InsertSessionPurchaseParams{
		ClubID: in.clubID, SessionID: sessionID, UserID: in.userID, Kind: string(in.part.kind),
		TariffID: in.part.tariffID, Name: in.part.name, Minutes: in.quote.Minutes,
		MainCents: int64(pay.Main), BonusCents: int64(pay.Bonus), ExpiresAt: in.part.expiresAt,
		ZoneID: in.part.zoneID, Source: &source, Now: &in.now,
	})
	if err != nil {
		return PurchaseResult{}, err
	}
	out.PurchaseID = id
	user := in.userID
	ends := ext.EndsAt
	if err := q.SetComputerSession(ctx, store.SetComputerSessionParams{
		ClubID: in.clubID, ID: in.computer.ID, SessionID: &sessionID, SessionUserID: &user,
		SessionGuest: false, SessionEndsAt: &ends, Now: &in.now,
	}); err != nil {
		return PurchaseResult{}, err
	}
	// События админки (docs/realtime.md): карта клуба и баланс клиента.
	if err := AddEvent(ctx, q, in.clubID, "computer.state", computerEvent(in.computer.ID, sessionID, user, false, &ends)); err != nil {
		return PurchaseResult{}, err
	}
	if out.LedgerTransactionID > 0 {
		if err := AddEvent(ctx, q, in.clubID, "balance.changed", BalanceEvent(in.userID, out.NewBalance, out.NewBonus)); err != nil {
			return PurchaseResult{}, err
		}
	}
	return out, nil
}

// itoa — для текстов.
func itoa(v int64) string { return strconv.FormatInt(v, 10) }
