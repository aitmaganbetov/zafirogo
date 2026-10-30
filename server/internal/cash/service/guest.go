package service

import (
	"context"
	"unicode/utf8"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// GuestSessionRecorded — приход гостевой сессии в смене.
type GuestSessionRecorded struct {
	Transaction store.CashTransaction
	Ledger      *ledger.Result // nil — нулевая сумма
	Replayed    bool
}

// GuestSessionIncome — приход гостевой сессии в смену (вход интерфейса billing/service.CashRecorder).
type GuestSessionIncome = billingsvc.GuestIncome

// GuestSessionIncomeResult — записанный приход (billing/service.GuestIncomeResult).
type GuestSessionIncomeResult = billingsvc.GuestIncomeResult

// Service реализует кассовый слой продажи гостевых сессий (billing/service.New(pool, clock, cashService)).
var _ billingsvc.CashRecorder = (*Service)(nil)

// LockOpenShift — метод интерфейса CashRecorder: открытая смена клуба FOR UPDATE (как функция LockOpenShift);
// ok = false — смены нет (сервис вернёт «Сначала откройте смену в кассе.»).
func (s *Service) LockOpenShift(ctx context.Context, q *store.Queries, clubID int64) (shiftID int64, ok bool, err error) {
	row, err := LockOpenShift(ctx, q, clubID)
	if err != nil || row == nil {
		return 0, false, err
	}
	return row.ID, true, nil
}

// RecordGuestSessionIncome — метод интерфейса CashRecorder: приход гостевой сессии в смену in.ShiftID
// (её вызывающий заблокировал LockOpenShift первой, как GuestSessions::sell()), ledger guest_session и аудит.
func (s *Service) RecordGuestSessionIncome(ctx context.Context, q *store.Queries, in GuestSessionIncome) (GuestSessionIncomeResult, error) {
	actor := in.Actor
	if actor.UserID == 0 {
		actor.UserID = in.CashierID
	}
	if actor.Kind == "" {
		actor.Kind = "staff"
	}
	rec, err := s.recordGuest(ctx, q, in, actor)
	if err != nil {
		return GuestSessionIncomeResult{}, err
	}
	out := GuestSessionIncomeResult{CashTransactionID: rec.Transaction.ID}
	if rec.Ledger != nil {
		out.LedgerTransactionID = rec.Ledger.TransactionID
	}
	return out, nil
}

// RecordGuestSession записывает продажу гостевой сессии (время без аккаунта) приходом открытой смены — то же, что
// RecordGuestSessionIncome, с позиционными параметрами. Вызывать в той же транзакции клуба, где сервис гостевых
// сессий занимает ПК (q — её Queries); смену он должен заблокировать первой (LockOpenShift), как
// GuestSessions::sell() — повторная блокировка здесь ничего не ждёт.
//
// Laravel: app/Services/GuestSessions.php sell() (~стр. 36–94): открытая смена FOR UPDATE (нет → «Сначала откройте смену
// в кассе.», поле payment_method); приход {user_id = кассир, amount = Σ цен ПК (formatCents), description
// «Гостевая сессия: :what, ПК :computers (:payment)» — формирует вызывающий}. В X/Z — вид «Гостевые сессии»
// (income_kind = guest_session вместо журнала cash.income с details.computers, OQ-BR-04); вернуть нельзя.
// Способ оплаты — payment_method (Q-013). Проводка guest_session: cash_drawer −S, revenue_time +S (S = 0 — без проводок).
// idempotencyKey — ключ запроса продажи (повтор возвращает прежнюю операцию, Replayed); пусто — без защиты от повтора.
func (s *Service) RecordGuestSession(ctx context.Context, q *store.Queries, clubID, staffID int64, amount money.Tiyn,
	description string, method cash.PaymentMethod, idempotencyKey string) (GuestSessionRecorded, error) {
	return s.recordGuest(ctx, q, GuestSessionIncome{
		ClubID: clubID, CashierID: staffID, Amount: amount, PaymentMethod: string(method),
		Description: description, IdempotencyKey: idempotencyKey,
	}, Actor{UserID: staffID, Kind: "staff"})
}

func (s *Service) recordGuest(ctx context.Context, q *store.Queries, in GuestSessionIncome, actor Actor) (GuestSessionRecorded, error) {
	method := cash.PaymentMethod(in.PaymentMethod)
	clubID, amount, description, idempotencyKey := in.ClubID, in.Amount, in.Description, in.IdempotencyKey
	if amount < 0 || amount > cash.MaxShiftAmount {
		return GuestSessionRecorded{}, &cash.Error{Field: "computer_ids", Rule: cash.RuleMax}
	}
	if !method.Valid() {
		return GuestSessionRecorded{}, &cash.Error{Field: "payment_method", Rule: cash.RuleIn}
	}
	if description == "" || utf8.RuneCountInString(description) > cash.MaxDescription {
		// description varchar(255): длинный список ПК в старом коде — ошибка MySQL (500).
		return GuestSessionRecorded{}, &cash.Error{Field: "computer_ids", Rule: cash.RuleMax}
	}
	shiftRow, err := LockOpenShift(ctx, q, clubID)
	if err != nil {
		return GuestSessionRecorded{}, err
	}
	if prev, err := findByKey(ctx, q, clubID, idempotencyKey); err != nil || prev != nil {
		if err != nil {
			return GuestSessionRecorded{}, err
		}
		if prev.IncomeKind == nil || *prev.IncomeKind != string(cash.KindGuestSession) ||
			prev.Amount != int64(amount) || prev.Description != description {
			return GuestSessionRecorded{}, ErrIdempotencyConflict
		}
		return GuestSessionRecorded{Transaction: *prev, Replayed: true}, nil
	}
	if shiftRow == nil || (in.ShiftID != 0 && in.ShiftID != shiftRow.ID) {
		return GuestSessionRecorded{}, &cash.Error{Field: "payment_method", Message: MsgGuestOpenShiftFirst}
	}
	row, err := s.insertTransaction(ctx, q, clubID, newTransaction{
		ShiftID: shiftRow.ID, UserID: in.CashierID, Type: cash.Income, Amount: amount, Description: description,
		PaymentMethod: method, Kind: cash.KindGuestSession, Key: idempotencyKey,
	})
	if err != nil {
		return GuestSessionRecorded{}, err
	}
	res, err := postDrawer(ctx, q, clubID, ledger.KindGuestSession, ledgerKey(idempotencyKey, row.ID), amount,
		ledger.AccountRevenueTime, ledger.Refs{CashShiftID: shiftRow.ID, CashTransactionID: row.ID},
		map[string]any{"payment_method": string(method)}, actor)
	if err != nil {
		return GuestSessionRecorded{}, err
	}
	details := map[string]any{"amount": int64(amount), "payment_method": string(method)}
	if in.Computers != "" {
		details["computers"], details["minutes"], details["tariff"] = in.Computers, in.Minutes, in.Tariff
	}
	// Laravel: GuestSessions::sell() (стр. 89–92) — журнал cash.income и computer.guest_session (без предмета)
	// с одними details: amount — строка formatCents, способ оплаты — подписью.
	tariff := in.Tariff
	if tariff == "" {
		tariff = "—"
	}
	journal := map[string]any{
		"computers": in.Computers, "minutes": in.Minutes, "amount": amount.Tenge(),
		"payment_method": paymentLabels[method], "tariff": tariff,
	}
	if err := record(ctx, q, activity.Entry{
		ClubID: clubID, Event: "cash.income", Description: description, UserID: in.CashierID,
		SubjectType: billingsvc.SubjectCashTransaction, SubjectID: row.ID, Details: journal,
	}); err != nil {
		return GuestSessionRecorded{}, err
	}
	if err := record(ctx, q, activity.Entry{
		ClubID: clubID, Event: "computer.guest_session", Description: description, UserID: in.CashierID, Details: journal,
	}); err != nil {
		return GuestSessionRecorded{}, err
	}
	if err := cashTransactionEvent(ctx, q, clubID, row, txGuestSession); err != nil {
		return GuestSessionRecorded{}, err
	}
	return GuestSessionRecorded{Transaction: row, Ledger: res}, audit(ctx, q, clubID, actor, "cash.guest_session", "cash_transaction", row.ID, res, details)
}
