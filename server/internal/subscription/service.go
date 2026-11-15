package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// KaspiSetting — ключ настройки платформы со ссылкой Kaspi Pay и получателем.
// Laravel: SubscriptionBilling::KASPI_SETTING.
const KaspiSetting = "billing_kaspi"

// Тексты SubscriptionBilling и BillingController (ключи перевода).
const (
	MsgPendingExists = "У клуба уже есть неоплаченный счёт: оплатите или отмените его."
	MsgClosed        = "Счёт уже закрыт."
	MsgNotConfigured = "Оплата пока не настроена. Напишите в поддержку Zafiro."
)

// Ошибки сервиса. Обработчики превращают их в ответы: ErrNotFound — 404, остальные — ошибка поля формы.
var (
	// ErrNotFound — счёта нет (у клуба — нет среди своих).
	ErrNotFound = errors.New("subscription: счёт не найден")
	// ErrPendingExists — у клуба уже есть неоплаченный счёт (BR-184).
	ErrPendingExists = errors.New("subscription: у клуба уже есть неоплаченный счёт")
	// ErrClosed — счёт уже оплачен или отменён.
	ErrClosed = errors.New("subscription: счёт уже закрыт")
	// ErrPlanNotPayable — тариф выключен или без цены (Rule::in(payablePlans())).
	ErrPlanNotPayable = errors.New("subscription: за тариф нельзя заплатить")
	// ErrMonths — срок не из Months.
	ErrMonths = errors.New("subscription: недопустимый срок")
	// ErrNotConfigured — ссылка Kaspi не задана: клубы не могут выставить счёт.
	ErrNotConfigured = errors.New("subscription: оплата не настроена")
)

const pendingUniqueIndex = "subscription_invoices_club_id_pending_unique"

// Kaspi — ссылка Kaspi Pay платформы и получатель, которого видят клубы. Не секрет.
type Kaspi struct {
	URL       string
	Recipient string
}

// ParseKaspi — SubscriptionBilling::kaspi(): значение настройки или nil, если ссылки нет.
//
//	return isset($value['url']) ? ['url' => (string) $value['url'], 'recipient' => $value['recipient'] ?? null] : null;
func ParseKaspi(raw json.RawMessage) *Kaspi {
	var v struct {
		URL       *string `json:"url"`
		Recipient *string `json:"recipient"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil || v.URL == nil || *v.URL == "" {
		return nil
	}
	k := &Kaspi{URL: *v.URL}
	if v.Recipient != nil {
		k.Recipient = *v.Recipient
	}
	return k
}

// ClubKaspi — настройка Kaspi в транзакции клуба (nil — оплата не настроена).
func ClubKaspi(ctx context.Context, q *store.Queries) (*Kaspi, error) {
	raw, err := q.SubscriptionKaspiSetting(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseKaspi(raw), nil
}

// Terms — условия счёта клуба на сейчас: за сколько ПК он платит и с какой скидкой.
type Terms struct {
	// Computers — ПК клуба, не меньше одного (BR-180).
	Computers int64
	// OwnDiscount — действующая индивидуальная скидка клуба; Discount — она или скидка активного реселлера,
	// какая больше (BR-181).
	OwnDiscount int
	Discount    int
	// ResellerID, ResellerBP — активный реселлер клуба и его процент (снимок в счёт); 0 — реселлера нет.
	ResellerID int64
	ResellerBP int
}

// Subscription — то, что условиям нужно от подписки клуба (nil — подписки нет).
type Subscription struct {
	DiscountPercent int
	DiscountUntil   *time.Time
}

// ClubTerms — число ПК и скидка клуба (SubscriptionBilling::computers(), discountFor()). loc — пояс клуба.
func ClubTerms(ctx context.Context, q *store.Queries, clubID int64, sub *Subscription, now time.Time, loc *time.Location) (Terms, error) {
	var t Terms
	registered, err := q.SubscriptionClubComputers(ctx, clubID)
	if err != nil {
		return t, err
	}
	t.Computers = Computers(registered)
	if sub != nil {
		t.OwnDiscount = OwnDiscount(sub.DiscountPercent, sub.DiscountUntil, now, loc)
	}
	resellerDiscount := 0
	// Reseller::query()->where('is_active', true)->find($club->reseller_id): остановленный реселлер не даёт ни скидки,
	// ни процента.
	switch r, err := q.SubscriptionClubReseller(ctx, clubID); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return t, err
	case r.IsActive:
		t.ResellerID, t.ResellerBP, resellerDiscount = r.ID, int(r.CommissionBp), int(r.DiscountPercent)
	}
	t.Discount = Discount(t.OwnDiscount, resellerDiscount)
	return t, nil
}

// OpenParams — счёт клуба ClubID за тариф PlanID на Months месяцев; By — кто выставил.
type OpenParams struct {
	ClubID int64
	PlanID int64
	Months int
	By     int64
	Now    time.Time
	// Loc — часовой пояс клуба (последний день скидки считается по нему).
	Loc *time.Location
}

// Opened — выставленный счёт.
type Opened struct {
	ID               int64
	PlanName         string
	Months           int
	Computers        int64
	PricePerComputer int64
	DiscountPercent  int
	Amount           int64
}

// Open — клуб выставляет себе счёт: снимок числа ПК, цены тарифа, скидки и реселлера; у клуба один неоплаченный
// счёт. Выполняется в транзакции клуба q. Проверки — в порядке BillingController::store(): тариф и срок
// (валидация формы), настройка Kaspi, затем неоплаченный счёт.
// Laravel: app/Services/SubscriptionBilling.php SubscriptionBilling::open(), BillingController::store().
func Open(ctx context.Context, q *store.Queries, p OpenParams) (Opened, error) {
	var out Opened
	plans, err := q.SubscriptionPayablePlans(ctx)
	if err != nil {
		return out, err
	}
	var plan *store.SubscriptionPayablePlansRow
	for i := range plans {
		if plans[i].ID == p.PlanID {
			plan = &plans[i]
		}
	}
	// Обе ошибки формы сразу, как $request->validate().
	var invalid []error
	if plan == nil {
		invalid = append(invalid, ErrPlanNotPayable)
	}
	if !ValidMonths(p.Months) {
		invalid = append(invalid, ErrMonths)
	}
	if len(invalid) > 0 {
		return out, errors.Join(invalid...)
	}
	kaspi, err := ClubKaspi(ctx, q)
	if err != nil {
		return out, err
	}
	if kaspi == nil {
		return out, ErrNotConfigured
	}
	pending, err := q.SubscriptionHasPendingInvoice(ctx, p.ClubID)
	if err != nil {
		return out, err
	}
	if pending {
		return out, ErrPendingExists
	}
	var sub *Subscription
	switch row, err := q.BillingSubscription(ctx, p.ClubID); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return out, err
	default:
		sub = &Subscription{DiscountPercent: int(row.DiscountPercent), DiscountUntil: row.DiscountUntil}
	}
	terms, err := ClubTerms(ctx, q, p.ClubID, sub, p.Now, p.Loc)
	if err != nil {
		return out, err
	}
	out = Opened{PlanName: plan.Name, Months: p.Months, Computers: terms.Computers, PricePerComputer: plan.PriceMonthly,
		DiscountPercent: terms.Discount, Amount: Amount(plan.PriceMonthly, terms.Computers, p.Months, terms.Discount)}
	params := store.SubscriptionInsertInvoiceParams{
		ClubID: p.ClubID, SaasPlanID: plan.ID, Months: int16(p.Months), Computers: int32(terms.Computers), // #nosec G115 -- срок из Months; ПК клуба — тысячи
		PricePerComputer: plan.PriceMonthly, DiscountPercent: int16(terms.Discount), Amount: out.Amount, // #nosec G115 -- 0…100
		CreatedBy: &p.By, Now: &p.Now,
	}
	if terms.ResellerID > 0 {
		bp := int32(terms.ResellerBP) // #nosec G115 -- 0…10000 (CHECK resellers)
		params.ResellerID, params.ResellerBp = &terms.ResellerID, &bp
	}
	out.ID, err = q.SubscriptionInsertInvoice(ctx, params)
	// Гонка двух выставлений (в том числе у клуба без строки подписки, GAP-046): второй упирается в уникальный индекс.
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == pendingUniqueIndex {
		return out, ErrPendingExists
	}
	return out, err
}

// CancelOwn — клуб отменяет свой неоплаченный счёт: только pending → canceled; чужого счёта для него нет.
// Laravel: BillingController::cancel() (скоуп клуба — 404), SubscriptionBilling::cancel().
func CancelOwn(ctx context.Context, q *store.Queries, clubID, invoiceID int64, now time.Time) error {
	if _, err := q.SubscriptionInvoiceStatus(ctx, store.SubscriptionInvoiceStatusParams{ClubID: clubID, ID: invoiceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	n, err := q.SubscriptionCancelInvoice(ctx, store.SubscriptionCancelInvoiceParams{ClubID: clubID, ID: invoiceID, Now: &now})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrClosed
	}
	return nil
}

// Paid — итог отметки оплаты.
type Paid struct {
	InvoiceID int64
	ClubID    int64
	ClubName  string
	Months    int
	Amount    int64
	// PeriodEndsAt — до какого момента продлена подписка.
	PeriodEndsAt time.Time
	// SubscriptionID — строка подписки (созданная, если её не было); Before — её состояние до оплаты (nil — не было).
	SubscriptionID int64
	Before         *store.SuperadminLockSubscriptionRow
	// ResellerAmount — начислено реселлеру счёта (nil — реселлера в счёте нет).
	ResellerAmount *int64
}

// MarkPaid — деньги по счёту пришли: подписка клуба — тариф счёта, active, ещё N месяцев от конца текущего периода
// (или от сейчас, PeriodEnd); счёт — paid. Блокировки: счёт, затем подписка; повторная отметка — ErrClosed, срок не
// меняется. В той же транзакции — журнал денег платформы: invoice_paid:<id> и, если в счёте есть реселлер,
// reseller_accrual:<id>. Выполняется в транзакции пула платформы q; by — суперадмин.
// Laravel: app/Services/SubscriptionBilling.php SubscriptionBilling::markPaid() (BR-183, BR-186).
func MarkPaid(ctx context.Context, q *store.Queries, invoiceID, by int64, now time.Time) (Paid, error) {
	var out Paid
	inv, err := q.SuperadminLockInvoice(ctx, invoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if inv.Status != "pending" {
		return out, ErrClosed
	}
	out = Paid{InvoiceID: inv.ID, ClubID: inv.ClubID, ClubName: inv.ClubName, Months: int(inv.Months), Amount: inv.Amount}

	sub, err := q.SuperadminLockSubscription(ctx, inv.ClubID)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if exists {
		out.Before, out.SubscriptionID = &sub, sub.ID
		out.PeriodEndsAt = PeriodEnd(now, sub.Status, sub.TrialEndsAt, sub.CurrentPeriodEndsAt, out.Months)
		err = q.SuperadminExtendSubscription(ctx, store.SuperadminExtendSubscriptionParams{
			SaasPlanID: inv.SaasPlanID, EndsAt: &out.PeriodEndsAt, Now: &now, ClubID: inv.ClubID})
	} else {
		// (new ClubSubscription)->forceFill(['club_id' => …, 'started_at' => now()])
		out.PeriodEndsAt = PeriodEnd(now, "", nil, nil, out.Months)
		out.SubscriptionID, err = q.SuperadminInsertSubscription(ctx, store.SuperadminInsertSubscriptionParams{
			ClubID: inv.ClubID, SaasPlanID: inv.SaasPlanID, Now: now, EndsAt: &out.PeriodEndsAt})
	}
	if err != nil {
		return out, err
	}

	// Реселлер получает свой процент с оплаченных денег; удалённый до оплаты (reseller_id = NULL) — ничего.
	if inv.ResellerID != nil {
		bp := 0
		if inv.ResellerBp != nil {
			bp = int(*inv.ResellerBp)
		}
		earning := ResellerEarning(inv.Amount, bp)
		out.ResellerAmount = &earning
	}
	if err := q.SuperadminMarkInvoicePaid(ctx, store.SuperadminMarkInvoicePaidParams{
		ID: inv.ID, Now: &now, ConfirmedBy: &by, PeriodEndsAt: &out.PeriodEndsAt, ResellerAmount: out.ResellerAmount}); err != nil {
		return out, err
	}

	number := Number(inv.ID)
	meta, err := json.Marshal(map[string]any{"number": number, "months": out.Months, "period_ends_at": out.PeriodEndsAt.UTC().Format(time.RFC3339)})
	if err != nil {
		return out, err
	}
	id := strconv.FormatInt(inv.ID, 10)
	if err := q.SuperadminInsertPlatformMoney(ctx, store.SuperadminInsertPlatformMoneyParams{
		Kind: "invoice_paid", IdempotencyKey: "invoice_paid:" + id, Amount: inv.Amount, ClubID: &inv.ClubID, InvoiceID: &inv.ID,
		ResellerID: inv.ResellerID, ActorUserID: &by, Meta: meta, Now: now}); err != nil {
		return out, err
	}
	return out, accrueReseller(ctx, q, inv, out.ResellerAmount, by, now)
}

// accrueReseller — строка reseller_accrual:<id счёта> журнала денег платформы: начисление реселлеру с оплаты (BR-186).
// Сумма начисления уже записана в счёт (reseller_amount) той же транзакцией; «заработано» реселлера считается по
// счетам (BR-187), журнал — неизменяемая история. Кабинет и выплаты реселлера — тема Д.
func accrueReseller(ctx context.Context, q *store.Queries, inv store.SuperadminLockInvoiceRow, earning *int64, by int64, now time.Time) error {
	if inv.ResellerID == nil || earning == nil {
		return nil
	}
	bp := 0
	if inv.ResellerBp != nil {
		bp = int(*inv.ResellerBp)
	}
	meta, err := json.Marshal(map[string]any{"number": Number(inv.ID), "reseller_bp": bp, "invoice_amount": inv.Amount})
	if err != nil {
		return err
	}
	return q.SuperadminInsertPlatformMoney(ctx, store.SuperadminInsertPlatformMoneyParams{
		Kind: "reseller_accrual", IdempotencyKey: "reseller_accrual:" + strconv.FormatInt(inv.ID, 10), Amount: *earning,
		ClubID: &inv.ClubID, InvoiceID: &inv.ID, ResellerID: inv.ResellerID, ActorUserID: &by, Meta: meta, Now: now})
}

// Canceled — отменённый суперадмином счёт.
type Canceled struct {
	InvoiceID int64
	ClubID    int64
	ClubName  string
}

// Cancel — суперадмин отменяет неоплаченный счёт любого клуба (pending → canceled) под блокировкой строки.
// Laravel: Superadmin\InvoiceController::cancel(), SubscriptionBilling::cancel().
func Cancel(ctx context.Context, q *store.Queries, invoiceID int64, now time.Time) (Canceled, error) {
	inv, err := q.SuperadminLockInvoice(ctx, invoiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Canceled{}, ErrNotFound
	}
	if err != nil {
		return Canceled{}, err
	}
	if inv.Status != "pending" {
		return Canceled{}, ErrClosed
	}
	return Canceled{InvoiceID: inv.ID, ClubID: inv.ClubID, ClubName: inv.ClubName},
		q.SuperadminCancelInvoice(ctx, store.SuperadminCancelInvoiceParams{ID: inv.ID, Now: &now})
}
