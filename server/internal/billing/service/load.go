package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ---------------------------------------------------------------------------
// Настройки клуба
// ---------------------------------------------------------------------------

// clubContext — то, что старые сервисы читали из CurrentClub: правила оплаты (PaymentRules), уровни
// лояльности (LoyaltyProgram), часовой пояс (CurrentClub::localTimezone()) и интервал между бронями
// (ComputerReservations::gapMinutes()).
type clubContext struct {
	rules      pricing.PaymentRules
	bonus      bonus.Settings
	loc        *time.Location
	gapMinutes int64
}

// DefaultBookingGapMinutes — ComputerReservations::DEFAULT_GAP_MINUTES.
const DefaultBookingGapMinutes int64 = 30

func loadClub(ctx context.Context, q *store.Queries, clubID int64) (clubContext, error) {
	c, err := q.GetClub(ctx, clubID)
	if err != nil {
		return clubContext{}, notFound("club", err)
	}
	fin, err := pricing.ParseFinances(c.Settings)
	if err != nil {
		// CurrentClub::settings(): некорректный JSON → [] (каст 'array' даёт null).
		fin = nil
	}
	bs, err := bonus.ParseSettings(c.Settings)
	if err != nil {
		return clubContext{}, fmt.Errorf("service: club settings: %w", err)
	}
	// Laravel: CurrentClub::localTimezone() — пустая строка → config('app.timezone') (UTC).
	loc := time.UTC
	if c.Timezone != "" {
		if loc, err = time.LoadLocation(c.Timezone); err != nil {
			return clubContext{}, fmt.Errorf("service: club timezone %q: %w", c.Timezone, err)
		}
	}
	return clubContext{
		rules:      pricing.RulesFromFinances(fin),
		bonus:      bs,
		loc:        loc,
		gapMinutes: bookingGapMinutes(c.Settings),
	}, nil
}

// bookingGapMinutes — ComputerReservations::gapMinutes() (стр. 23–28):
//
//	$value = settings()['club']['booking_interval'] ?? 30;
//	return is_numeric($value) ? max(0, (int) round((float) $value)) : 30;
func bookingGapMinutes(settings []byte) int64 {
	var root map[string]json.RawMessage
	if json.Unmarshal(settings, &root) != nil {
		return DefaultBookingGapMinutes
	}
	var club map[string]json.RawMessage
	if json.Unmarshal(root["club"], &club) != nil {
		return DefaultBookingGapMinutes
	}
	raw, ok := club["booking_interval"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return DefaultBookingGapMinutes
	}
	var f float64
	switch {
	case json.Unmarshal(raw, &f) == nil:
	default:
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return DefaultBookingGapMinutes // bool, массив — не is_numeric
		}
		// is_numeric(): десятичная или экспоненциальная запись, пробелы по краям (PHP 8).
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil || strings.ContainsAny(s, "xXpP_") || strings.EqualFold(strings.TrimSpace(s), "inf") ||
			strings.EqualFold(strings.TrimSpace(s), "nan") || strings.EqualFold(strings.TrimSpace(s), "infinity") {
			return DefaultBookingGapMinutes
		}
		f = v
	}
	return max(0, int64(money.PHPRound(f, 0)))
}

// ---------------------------------------------------------------------------
// Тариф
// ---------------------------------------------------------------------------

type tariffRow struct {
	ID                   int64
	Name                 string
	DurationMinutes      int64
	Price                int64
	PaymentMethod        string
	DiscountEnabled      bool
	IsActive             bool
	UsageLimitEnabled    bool
	UsageLimitMinutes    *int64
	QuantityLimitEnabled bool
	QuantityLimit        *int64
	UserRoles            json.RawMessage
	SalePeriods          json.RawMessage
	ValidityPeriods      json.RawMessage
}

// toPricing переводит строку tariffs (+ зоны club_zone_tariff) во входные данные pricing.
func (t tariffRow) toPricing(zoneIDs []int64) (pricing.Tariff, error) {
	sale, err := pricing.ParseWeeklyPeriods(t.SalePeriods)
	if err != nil {
		return pricing.Tariff{}, fmt.Errorf("service: tariff %d sale_periods: %w", t.ID, err)
	}
	validity, err := pricing.ParseWeeklyPeriods(t.ValidityPeriods)
	if err != nil {
		return pricing.Tariff{}, fmt.Errorf("service: tariff %d validity_periods: %w", t.ID, err)
	}
	return pricing.Tariff{
		Price:                money.Tiyn(t.Price),
		DurationMinutes:      t.DurationMinutes,
		PaymentMethod:        pricing.PaymentMethod(t.PaymentMethod),
		DiscountEnabled:      t.DiscountEnabled,
		IsActive:             t.IsActive,
		UsageLimitEnabled:    t.UsageLimitEnabled,
		UsageLimitMinutes:    t.UsageLimitMinutes,
		QuantityLimitEnabled: t.QuantityLimitEnabled,
		QuantityLimit:        t.QuantityLimit,
		UserRoles:            userRoles(t.UserRoles),
		SalePeriods:          sale,
		ValidityPeriods:      validity,
		ZoneIDs:              zoneIDs,
	}, nil
}

// nonStringRole — роль-заглушка для не строкового элемента user_roles: in_array($role, $roles, true)
// со строковой ролью его не находит, но массив при этом не пуст.
const nonStringRole = "\x00non-string"

// userRoles — tariffs.user_roles (каст 'array'): список строк; null/не массив — без ограничения.
// Laravel: AgentTariffPurchaseService::isAvailable() (стр. 323–326).
func userRoles(raw json.RawMessage) []string {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		var obj map[string]json.RawMessage // ассоциативный массив PHP — тоже array
		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}
		for _, v := range obj {
			list = append(list, v)
		}
	}
	if list == nil { // JSON null
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out = append(out, s)
		} else {
			out = append(out, nonStringRole)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// ПК
// ---------------------------------------------------------------------------

type computerRow struct {
	ID                   int64
	ClubZoneID           int64
	Number               int64
	DisplayName          *string
	DeviceName           *string
	IsReserved           bool
	LastSeenAt           *time.Time
	CurrentSessionID     *string
	CurrentSessionUserID *int64
	SessionGuest         bool
	SessionEndsAt        *time.Time
	StaffUntil           *time.Time
}

func lockComputer(ctx context.Context, q *store.Queries, clubID, id int64) (computerRow, error) {
	r, err := q.LockComputer(ctx, store.LockComputerParams{ClubID: clubID, ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return computerRow{}, notFound("computer", err)
		}
		return computerRow{}, err
	}
	return computerRow(r), nil
}

// displayName — ClubComputer::displayName(): display_name ?: device_name ?: sprintf('PC-%02d', number).
func (c computerRow) displayName() string {
	if c.DisplayName != nil && *c.DisplayName != "" {
		return *c.DisplayName
	}
	if c.DeviceName != nil && *c.DeviceName != "" {
		return *c.DeviceName
	}
	return fmt.Sprintf("PC-%02d", c.Number)
}

func (c computerRow) session() session.Computer {
	return session.Computer{
		ID: c.ID, ZoneID: c.ClubZoneID, IsReserved: c.IsReserved, LastSeenAt: c.LastSeenAt,
		SessionID: c.CurrentSessionID, SessionUserID: c.CurrentSessionUserID, SessionGuest: c.SessionGuest,
		SessionEndsAt: c.SessionEndsAt, StaffUntil: c.StaffUntil,
	}
}

// pricing — ПК для расчёта покупки: hasPaidSession() считает session.Computer.HasPaidSession (BR-030).
func (c computerRow) pricing(now time.Time) pricing.Computer {
	zone := c.ClubZoneID
	pc := pricing.Computer{
		ZoneID:        &zone,
		Reserved:      c.IsReserved,
		PaidSession:   c.session().HasPaidSession(now),
		SessionUserID: c.CurrentSessionUserID,
		SessionGuest:  c.SessionGuest,
	}
	if c.SessionEndsAt != nil {
		pc.SessionEndsAt = *c.SessionEndsAt
	}
	return pc
}

// ---------------------------------------------------------------------------
// Брони (ComputerReservations)
// ---------------------------------------------------------------------------

// blocking — ComputerReservations::blocking() (стр. 93–101): чужая ожидающая бронь ПК, пересекающая
// [now, until + интервал). userID 0 — гость (мешает любая бронь). nil — брони нет.
func blocking(ctx context.Context, q *store.Queries, clubID, computerID, userID int64, gap int64, now, until time.Time) (*time.Time, error) {
	r, err := q.GetBlockingReservation(ctx, store.GetBlockingReservationParams{
		ClubID: clubID, ComputerID: computerID, UserID: &userID,
		FromAt: now, Until: until.Add(time.Duration(gap) * time.Minute),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r.StartsAt, nil
}

// markArrivedBy — ComputerReservations::markArrivedBy() (стр. 104–111).
func markArrivedBy(ctx context.Context, q *store.Queries, clubID, computerID, userID int64, gap int64, now, until time.Time) error {
	return q.MarkReservationsArrived(ctx, store.MarkReservationsArrivedParams{
		ClubID: clubID, ComputerID: computerID, UserID: &userID, Now: &now,
		FromAt: now, Until: until.Add(time.Duration(gap) * time.Minute),
	})
}

// ---------------------------------------------------------------------------
// Накопительные траты (LoyaltyProgram)
// ---------------------------------------------------------------------------

// MaxTotalSpent — club_memberships.total_spent в старой схеме decimal(12,2): больше — ошибка MySQL и откат.
const MaxTotalSpent money.Tiyn = 999_999_999_999

// ErrTotalSpentOverflow — накопительные траты вышли бы за decimal(12,2).
var ErrTotalSpentOverflow = errors.New("service: total_spent overflow")

// addSpending — LoyaltyProgram::addSpending() (стр. 45–50): increment('total_spent') при cents > 0;
// нет членства — ничего (UPDATE 0 строк). BR-042: только оплата с основного баланса.
func addSpending(ctx context.Context, q *store.Queries, clubID, userID int64, cents money.Tiyn, now time.Time) error {
	if cents <= 0 {
		return nil
	}
	total, err := q.IncrementTotalSpent(ctx, store.IncrementTotalSpentParams{ClubID: clubID, UserID: userID, Cents: int64(cents), Now: &now})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if money.Tiyn(total) > MaxTotalSpent {
		return ErrTotalSpentOverflow
	}
	return nil
}

// subtractSpending — LoyaltyProgram::subtractSpending() (стр. 53–63): траты уменьшаются, но не ниже нуля
// (bonus.SubtractSpending под блокировкой строки членства). Нет членства — ничего.
func subtractSpending(ctx context.Context, q *store.Queries, clubID, userID int64, cents money.Tiyn, now time.Time) error {
	if cents <= 0 {
		return nil
	}
	m, err := q.LockMembershipForPurchase(ctx, store.LockMembershipForPurchaseParams{ClubID: clubID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	next := bonus.SubtractSpending(money.Tiyn(m.TotalSpent), cents)
	return q.SetTotalSpent(ctx, store.SetTotalSpentParams{ClubID: clubID, UserID: userID, TotalSpent: int64(next), Now: &now})
}

// ---------------------------------------------------------------------------
// Идемпотентность запросов (billing_requests)
// ---------------------------------------------------------------------------

// MaxIdempotencyKeyLen — billing_requests.idempotency_key varchar(200).
const MaxIdempotencyKeyLen = 200

// claim захватывает ключ запроса до блокировок. Если запрос с этим ключом уже выполнен: то же тело —
// ответ первого выполнения декодируется в out и возвращается true; другое тело или операция —
// ErrIdempotencyConflict. Параллельный запрос с тем же ключом ждёт COMMIT первого на уникальном индексе.
func (s *Service) claim(ctx context.Context, q *store.Queries, clubID int64, key, operation string, request, out any) (bool, error) {
	if strings.TrimSpace(key) == "" || len(key) > MaxIdempotencyKeyLen {
		return false, fmt.Errorf("%w: idempotency key must be 1..%d bytes", ErrInvalidInput, MaxIdempotencyKeyLen)
	}
	body, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Request   any    `json:"request"`
	}{operation, request})
	if err != nil {
		return false, err
	}
	fp := sha256.Sum256(body)
	_, err = q.ClaimBillingRequest(ctx, store.ClaimBillingRequestParams{
		ClubID: clubID, IdempotencyKey: key, Operation: operation, Fingerprint: fp[:], Now: s.clock.Now(),
	})
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("service: claim idempotency key: %w", err)
	}
	prev, err := q.GetBillingRequest(ctx, store.GetBillingRequestParams{ClubID: clubID, IdempotencyKey: key})
	if err != nil {
		return false, fmt.Errorf("service: read idempotency key: %w", err)
	}
	if prev.Operation != operation || !bytes.Equal(prev.Fingerprint, fp[:]) || len(prev.Response) == 0 {
		return false, ErrIdempotencyConflict
	}
	if err := json.Unmarshal(prev.Response, out); err != nil {
		return false, fmt.Errorf("service: decode stored response: %w", err)
	}
	return true, nil
}

// remember сохраняет ответ под ключом (в той же транзакции, что и операция).
func remember(ctx context.Context, q *store.Queries, clubID int64, key string, response any) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return q.SetBillingRequestResponse(ctx, store.SetBillingRequestResponseParams{ClubID: clubID, IdempotencyKey: key, Response: body})
}

// ---------------------------------------------------------------------------
// Аудит
// ---------------------------------------------------------------------------

// auditMoney — запись audit_log категории money со ссылкой на ledger-операцию.
func auditMoney(ctx context.Context, q *store.Queries, clubID int64, actor Actor, action string, subjectUserID, ledgerTxID int64, after map[string]any) error {
	body, err := json.Marshal(after)
	if err != nil {
		return err
	}
	kind := actor.Kind
	if kind == "" {
		kind = "system"
	}
	subjectType := "user"
	p := store.InsertAuditLogParams{
		ClubID: &clubID, ActorKind: kind, Category: "money", Action: action,
		SubjectType: &subjectType, SubjectID: &subjectUserID, After: body,
	}
	if ledgerTxID > 0 {
		p.LedgerTransactionID = &ledgerTxID
	}
	if actor.UserID > 0 {
		p.ActorUserID = &actor.UserID
	}
	if actor.IP.IsValid() {
		ip := actor.IP
		p.Ip = &ip
	}
	if actor.RequestID != "" {
		p.RequestID = &actor.RequestID
	}
	if err := q.InsertAuditLog(ctx, p); err != nil {
		return fmt.Errorf("service: audit: %w", err)
	}
	return nil
}

// ledgerKey — естественный ключ ledger-операции; длинный идентификатор заменяется его sha256.
func ledgerKey(prefix, id string) string {
	key := prefix + ":" + id
	if len(key) <= MaxIdempotencyKeyLen {
		return key
	}
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%s:sha256:%x", prefix, sum)
}
