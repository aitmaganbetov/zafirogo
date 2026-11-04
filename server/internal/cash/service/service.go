// Package service — касса поверх БД: смены, продажи POS, пополнение баланса клиента, приход/расход, возвраты,
// X/Z-отчёты и запись гостевых сессий в смену (BR-050…BR-057). Расчёты и тексты ошибок — чистый пакет cash,
// движения денег — ledger (через billing для балансов клиента), остатки товаров — пакет stock.
//
// Каждая операция повторяет транзакцию старого кода (app/Services/CashRegisterService.php): открытая смена
// FOR UPDATE первой (она сериализует все операции кассы клуба), затем приход/товары/баланс клиента FOR UPDATE,
// проверки в том же порядке, запись. Методы вызываются внутри db.InTenantTx клуба (q — его Queries); при deadlock
// транзакция повторяется целиком, методы повторяемы.
//
// Идемпотентность: продажа, пополнение, приход/расход и гостевая сессия принимают ключ запроса кассы
// (cash_transactions.idempotency_key, 0007): повтор с тем же ключом возвращает прежнюю операцию (Replayed), с другим
// телом — ErrIdempotencyConflict. Ключ ledger-операции — тот же (без ключа — "cash_transaction:<id>").
// Возврат идемпотентен по приходу (refund_of_id уникален, ключ ledger "pos_refund:<id>" / "top_up_refund:<id>").
//
// Проводки (docs/schema-changes.md §10.1; касса — актив, приход = дебет −):
//
//	pos_sale       cash_drawer −S, revenue_pos +S         pos_refund   обратные
//	cash_income    cash_drawer −S, cash_other +S          cash_expense cash_drawer +S, cash_other −S
//	guest_session  cash_drawer −S, revenue_time +S
//	top_up / top_up_refund — billing.TopUp / billing.RefundTopUp
//	cash_shift_opening — разница начального остатка смены и остатка кассы по журналу: cash_drawer ∓d,
//	opening_balance ±d (решение CSQ-1); остаток кассы — снимок закрытой смены + проводки после него (CSQ-7)
//
// Аудит (audit_log, категория money): cash.shift_opened, cash.shift_closed, cash.pos_sale, cash.income, cash.expense,
// cash.refund, cash.guest_session, cash.z_report_printed; пополнение и его возврат пишет billing (money.top_up…).
// Журнал интерфейса (activity_logs), события админки и вебхуки (outbox) — в той же транзакции (events.go).
// Права (cash.delete на возврат и т.п.) проверяет вызывающий.
package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Service — касса клуба. Нулевое значение готово к работе.
type Service struct {
	// Now — часы (opened_at, created_at операций, отметка печати Z). nil — time.Now.
	// Время ledger-операций и аудита ставит БД (now() транзакции).
	Now func() time.Time
	// Rand — источник случайности для nonce печати Z-отчёта. nil — crypto/rand.
	Rand io.Reader
	// ZNonces — одноразовые nonce печати Z-отчёта (Q-015: Valkey, NewValkeyZNonces). nil — в памяти процесса
	// (тесты и один инстанс; на нескольких инстансах nonce, выданный одним, другой не узнает).
	ZNonces ZNonceStore
	// Notifier — уведомления агента о балансе клиента после коммита (через billingsvc.AfterCommit:
	// вызывающий создаёт billingsvc.WithCommitHooks и выполняет их после COMMIT). nil — без уведомлений.
	Notifier billingsvc.SessionNotifier
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Actor — кто выполняет операцию (сотрудник кассы; Kind — audit_log.actor_kind, по умолчанию staff).
type Actor = billing.BalanceActor

// Ошибки уровня запроса (тексты правил кассы — *cash.Error, склада — *stock.Error).
var (
	// ErrNotFound — смены/операции/клиента нет в клубе (findOrFail → 404).
	ErrNotFound = errors.New("cash: not found")
	// ErrShiftNotOpen — отметка печати Z для закрытой смены (markZReportPrinted: abort 422).
	ErrShiftNotOpen = errors.New("cash: shift is not open")
	// ErrIdempotencyConflict — ключ запроса уже использован операцией с другим телом.
	ErrIdempotencyConflict = ledger.ErrIdempotencyConflict
)

// MsgGuestOpenShiftFirst — GuestSessions::sell(): нет открытой смены (поле payment_method).
const MsgGuestOpenShiftFirst = "Сначала откройте смену в кассе."

// ---------------------------------------------------------------------------
// Смена
// ---------------------------------------------------------------------------

// LockOpenShift — открытая смена клуба FOR UPDATE (CashShift::where('status', open)->lockForUpdate()->first()); nil — нет.
// С неё начинается любая операция кассы — и запись гостевой сессии (GuestSessions::sell() блокирует смену до ПК).
func LockOpenShift(ctx context.Context, q *store.Queries, clubID int64) (*store.CashShift, error) {
	row, err := q.LockOpenCashShift(ctx, clubID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cash: lock open shift: %w", err)
	}
	return &row, nil
}

// ShiftOf — смена для расчётов пакета cash.
func ShiftOf(row *store.CashShift) *cash.Shift {
	if row == nil {
		return nil
	}
	s := &cash.Shift{
		ID: row.ID, Status: cash.Status(row.Status),
		OpeningBalance: money.Tiyn(row.OpeningBalance),
		OpenedBy:       deref(row.OpenedBy), ClosedBy: deref(row.ClosedBy),
	}
	if row.ClosingBalance != nil {
		s.ClosingBalance = money.Tiyn(*row.ClosingBalance)
	}
	return s
}

// shiftTotals — CashRegisterService::totals(): SUM по типам в SQL (BR-051).
func shiftTotals(ctx context.Context, q *store.Queries, clubID int64, shift *store.CashShift) (cash.Totals, error) {
	if shift == nil {
		return cash.Totals{}, nil
	}
	sums, err := q.CashShiftTotals(ctx, store.CashShiftTotalsParams{ClubID: clubID, CashShiftID: shift.ID})
	if err != nil {
		return cash.Totals{}, fmt.Errorf("cash: totals: %w", err)
	}
	return cash.Totals{
		Opening: money.Tiyn(shift.OpeningBalance), Income: money.Tiyn(sums.Income), Expense: money.Tiyn(sums.Expense),
	}, nil
}

// ---------------------------------------------------------------------------
// Кассовая операция
// ---------------------------------------------------------------------------

// OperationOf — кассовая операция для правил пакета cash (receipt_data разобран).
func OperationOf(row store.CashTransaction) (cash.Operation, error) {
	receipt, err := decodeReceipt(row.ReceiptData)
	if err != nil {
		return cash.Operation{}, err
	}
	op := cash.Operation{
		ID: row.ID, ShiftID: row.CashShiftID, UserID: deref(row.UserID), Type: cash.Type(row.Type),
		Amount: money.Tiyn(row.Amount), Description: row.Description, Receipt: receipt,
		RefundOfID: deref(row.RefundOfID),
	}
	if row.IncomeKind != nil {
		op.Kind = cash.IncomeKind(*row.IncomeKind)
	}
	if row.PaymentMethod != nil {
		op.PaymentMethod = cash.PaymentMethod(*row.PaymentMethod)
	}
	return op, nil
}

type newTransaction struct {
	ShiftID       int64
	RefundOfID    int64
	UserID        int64
	Type          cash.Type
	Amount        money.Tiyn
	Description   string
	Receipt       *cash.Receipt
	PaymentMethod cash.PaymentMethod
	Kind          cash.IncomeKind
	Key           string
}

func (s *Service) insertTransaction(ctx context.Context, q *store.Queries, clubID int64, t newTransaction) (store.CashTransaction, error) {
	var receipt json.RawMessage
	if t.Receipt != nil {
		var err error
		if receipt, err = encodeReceipt(*t.Receipt); err != nil {
			return store.CashTransaction{}, err
		}
	}
	now := s.now()
	row, err := q.InsertCashTransaction(ctx, store.InsertCashTransactionParams{
		ClubID: clubID, CashShiftID: t.ShiftID, RefundOfID: nonZero(t.RefundOfID), UserID: nonZero(t.UserID),
		Type: string(t.Type), Amount: int64(t.Amount), Description: t.Description, ReceiptData: receipt,
		PaymentMethod: nonEmpty(string(t.PaymentMethod)), IncomeKind: nonEmpty(string(t.Kind)),
		IdempotencyKey: nonEmpty(t.Key), Now: &now,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			switch pgErr.ConstraintName {
			case "cash_transactions_refund_of_id_unique":
				return store.CashTransaction{}, &cash.Error{Field: "refund", Message: cash.MsgAlreadyRefunded}
			case "cash_transactions_club_id_idempotency_key_unique":
				return store.CashTransaction{}, ErrIdempotencyConflict
			}
		}
		return store.CashTransaction{}, fmt.Errorf("cash: insert transaction: %w", err)
	}
	return row, nil
}

// findByKey — операция с этим ключом запроса (повтор). nil — нет или ключ пуст.
func findByKey(ctx context.Context, q *store.Queries, clubID int64, key string) (*store.CashTransaction, error) {
	if key == "" {
		return nil, nil
	}
	if len(key) > ledger.MaxIdempotencyKeyLen {
		return nil, fmt.Errorf("%w: idempotency key is longer than %d bytes", ledger.ErrInvalidOperation, ledger.MaxIdempotencyKeyLen)
	}
	row, err := q.GetCashTransactionByKey(ctx, store.GetCashTransactionByKeyParams{ClubID: clubID, IdempotencyKey: &key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cash: find by key: %w", err)
	}
	return &row, nil
}

// ledgerKey — ключ ledger-операции кассового документа: ключ запроса или естественный "cash_transaction:<id>".
func ledgerKey(key string, cashTransactionID int64) string {
	if key != "" {
		return key
	}
	return "cash_transaction:" + strconv.FormatInt(cashTransactionID, 10)
}

// postDrawer — операция «касса ↔ счёт клуба»: drawerIn > 0 — деньги пришли в кассу (дебет кассы −S, кредит counter +S),
// < 0 — ушли из кассы. Нулевая сумма — без операции (ledger не принимает нулевых проводок).
func postDrawer(ctx context.Context, q *store.Queries, clubID int64, kind ledger.Kind, key string, drawerIn money.Tiyn,
	counter ledger.AccountKind, refs ledger.Refs, meta map[string]any, actor Actor) (*ledger.Result, error) {
	if drawerIn == 0 {
		return nil, nil
	}
	var rawMeta json.RawMessage
	if meta != nil {
		var err error
		if rawMeta, err = json.Marshal(meta); err != nil {
			return nil, err
		}
	}
	res, err := ledger.Post(ctx, q, ledger.Operation{
		ClubID: clubID, Kind: kind, IdempotencyKey: key,
		Entries: []ledger.Entry{
			{Account: ledger.ClubAccount(ledger.AccountCashDrawer), Amount: -drawerIn},
			{Account: ledger.ClubAccount(counter), Amount: drawerIn},
		},
		Meta: rawMeta, ActorUserID: actor.UserID, Refs: refs,
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// audit — запись аудита кассы (категория money).
func audit(ctx context.Context, q *store.Queries, clubID int64, actor Actor, action, subjectType string, subjectID int64,
	ledgerTx *ledger.Result, after map[string]any) error {
	body, err := json.Marshal(after)
	if err != nil {
		return err
	}
	kind := actor.Kind
	if kind == "" {
		kind = "staff"
	}
	params := store.InsertAuditLogParams{
		ClubID: &clubID, ActorKind: kind, Category: "money", Action: action,
		SubjectType: &subjectType, SubjectID: &subjectID, After: body,
	}
	if ledgerTx != nil {
		params.LedgerTransactionID = &ledgerTx.TransactionID
	}
	if actor.UserID > 0 {
		params.ActorUserID = &actor.UserID
	}
	if actor.IP.IsValid() {
		ip := actor.IP
		params.Ip = &ip
	}
	if actor.RequestID != "" {
		params.RequestID = &actor.RequestID
	}
	if err := q.InsertAuditLog(ctx, params); err != nil {
		return fmt.Errorf("cash: audit: %w", err)
	}
	return nil
}

// userNames — имена пользователей клуба по id.
func userNames(ctx context.Context, q *store.Queries, clubID int64, ids ...int64) (map[int64]string, error) {
	var want []int64
	for _, id := range ids {
		if id > 0 {
			want = append(want, id)
		}
	}
	out := make(map[int64]string, len(want))
	if len(want) == 0 {
		return out, nil
	}
	rows, err := q.ListClubUserNames(ctx, store.ListClubUserNamesParams{ClubID: clubID, Ids: want})
	if err != nil {
		return nil, fmt.Errorf("cash: user names: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = r.Name
	}
	return out, nil
}

func randomString(r io.Reader, n int) (string, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	if r == nil {
		r = rand.Reader
	}
	buf := make([]byte, n)
	out := make([]byte, 0, n)
	for len(out) < n {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", fmt.Errorf("cash: random: %w", err)
		}
		for _, b := range buf {
			// 248 = 62 × 4: без смещения распределения.
			if b < 248 && len(out) < n {
				out = append(out, alphabet[int(b)%len(alphabet)])
			}
		}
	}
	return string(out), nil
}

func tiyn(v int64) money.Tiyn { return money.Tiyn(v) }

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func nonZero(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
