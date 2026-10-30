package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// OpenShiftParams — открытие смены. OpeningBalance — cash.ParseOpeningBalance.
type OpenShiftParams struct {
	ClubID         int64
	OpeningBalance money.Tiyn
	Actor          Actor
}

// OpenShiftResult — открытая смена и проводка выравнивания кассы (nil — остаток кассы по журналу уже равен
// начальному остатку смены).
type OpenShiftResult struct {
	Shift            store.CashShift
	DrawerAdjustment *ledger.Result
}

// OpenShift — открытие кассовой смены (BR-050).
//
// Laravel: app/Services/CashRegisterService.php open() (~стр. 87–98): открытая смена FOR UPDATE exists() →
// «Смена уже открыта.» (поле opening_balance); create(opened_by, status open, open_marker true, opening_balance, opened_at = now).
// Гонку двух открытий ловит уникальный индекс (club_id, open_marker) — та же ошибка.
//
// Новое (ledger): начальный остаток смены вводит кассир, а касса в журнале — остаток прошлых смен. Разница
// (деньги, внесённые или изъятые между сменами) проводится операцией cash_shift_opening со счётом
// opening_balance, ключ "cash_shift_open:<id>" — после неё остаток cash_drawer равен остатку смены.
// Решение CSQ-1 (docs/DECISIONS.md): отдельный вид, а не cash_income/cash_expense с meta.reason.
// Остаток кассы по журналу — снимок последней закрытой смены + проводки после него (решение CSQ-7, drawerBalance).
func (s *Service) OpenShift(ctx context.Context, q *store.Queries, p OpenShiftParams) (OpenShiftResult, error) {
	if p.OpeningBalance < 0 {
		return OpenShiftResult{}, &cash.Error{Field: "opening_balance", Rule: cash.RuleMin}
	}
	if p.OpeningBalance > cash.MaxShiftAmount {
		return OpenShiftResult{}, &cash.Error{Field: "opening_balance", Rule: cash.RuleMax}
	}
	open, err := LockOpenShift(ctx, q, p.ClubID)
	if err != nil {
		return OpenShiftResult{}, err
	}
	if err := cash.CheckOpen(ShiftOf(open)); err != nil {
		return OpenShiftResult{}, err
	}
	now := s.now()
	row, err := q.InsertCashShift(ctx, store.InsertCashShiftParams{
		ClubID: p.ClubID, OpenedBy: nonZero(p.Actor.UserID), OpeningBalance: int64(p.OpeningBalance), Now: now,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "cash_shifts_club_id_open_marker_unique" {
			return OpenShiftResult{}, &cash.Error{Field: "opening_balance", Message: cash.MsgShiftAlreadyOpen}
		}
		return OpenShiftResult{}, fmt.Errorf("cash: open shift: %w", err)
	}

	drawerTiyn, _, err := drawerBalance(ctx, q, p.ClubID)
	if err != nil {
		return OpenShiftResult{}, err
	}
	drawer := int64(drawerTiyn)
	delta := p.OpeningBalance - drawerTiyn
	// Решение CSQ-1 (docs/DECISIONS.md): вид cash_shift_opening для разницы любого знака.
	adj, err := postDrawer(ctx, q, p.ClubID, ledger.KindCashShiftOpening, "cash_shift_open:"+strconv.FormatInt(row.ID, 10), delta,
		ledger.AccountOpeningBalance, ledger.Refs{CashShiftID: row.ID},
		map[string]any{"drawer_before": drawer, "opening_balance": int64(p.OpeningBalance)}, p.Actor)
	if err != nil {
		return OpenShiftResult{}, err
	}
	// Laravel: CashRegisterService::open() (стр. 94) — журнал cash.shift_opened.
	if err := record(ctx, q, activity.Entry{
		ClubID: p.ClubID, Event: "cash.shift_opened", Description: "Открыта кассовая смена", UserID: p.Actor.UserID,
		SubjectType: billingsvc.SubjectCashShift, SubjectID: row.ID,
		Details: map[string]any{"opening_balance": billingsvc.PHPFloat(p.OpeningBalance)},
	}); err != nil {
		return OpenShiftResult{}, err
	}
	if err := shiftEvent(ctx, q, p.ClubID, row.ID, "opened"); err != nil {
		return OpenShiftResult{}, err
	}
	return OpenShiftResult{Shift: row, DrawerAdjustment: adj}, audit(ctx, q, p.ClubID, p.Actor, "cash.shift_opened", "cash_shift", row.ID, adj,
		map[string]any{"opening_balance": int64(p.OpeningBalance), "drawer_before": drawer})
}

// CloseShiftParams — закрытие смены.
type CloseShiftParams struct {
	ClubID  int64
	ShiftID int64
	Actor   Actor
	// RequireZReport — веб-панель закрывает смену только после отметки печати Z-отчёта
	// (CashRegisterController::close()); приложение сотрудника — без неё (Api/Staff/CashController::close()).
	RequireZReport bool
}

// CloseShiftResult — закрытая смена и её итоги.
type CloseShiftResult struct {
	Shift  store.CashShift
	Totals cash.Totals
}

// CloseShift — закрытие смены (BR-056).
//
// Laravel: CashRegisterController::close() (~стр. 135–145): без отметки печати — «Сначала распечатайте Z-отчёт…» (поле shift);
// CashRegisterService::close() (~стр. 255–267): смена FOR UPDATE (findOrFail); не open → «Смена уже закрыта.»;
// closed_by, status closed, open_marker null, closing_balance = balance(), closed_at = now.
// Проводок нет: все операции смены уже в журнале, остаток cash_drawer равен closing_balance.
// Решение CSQ-7 (docs/DECISIONS.md): смена запоминает снимок остатка кассы по журналу (последняя учтённая проводка
// cash_drawer и остаток после неё) — следующее открытие суммирует только проводки после снимка.
func (s *Service) CloseShift(ctx context.Context, q *store.Queries, p CloseShiftParams) (CloseShiftResult, error) {
	row, err := q.LockCashShift(ctx, store.LockCashShiftParams{ClubID: p.ClubID, ID: p.ShiftID})
	if errors.Is(err, pgx.ErrNoRows) {
		return CloseShiftResult{}, ErrNotFound
	}
	if err != nil {
		return CloseShiftResult{}, fmt.Errorf("cash: lock shift: %w", err)
	}
	totals, err := shiftTotals(ctx, q, p.ClubID, &row)
	if err != nil {
		return CloseShiftResult{}, err
	}
	printed := !p.RequireZReport || row.ZReportPrintedAt != nil
	closing, err := cash.Close(ShiftOf(&row), totals, printed)
	if err != nil {
		return CloseShiftResult{}, err
	}
	// Смена заблокирована: все операции кассы клуба (и все проводки cash_drawer) ждут эту транзакцию,
	// поэтому снимок видит всё, что было до закрытия, а следующие проводки получат id больше снимка.
	drawer, lastEntryID, err := drawerBalance(ctx, q, p.ClubID)
	if err != nil {
		return CloseShiftResult{}, err
	}
	now := s.now()
	closingBalance := int64(closing)
	closed, err := q.CloseCashShift(ctx, store.CloseCashShiftParams{
		ClubID: p.ClubID, ID: row.ID, ClosedBy: nonZero(p.Actor.UserID), ClosingBalance: &closingBalance, Now: &now,
		DrawerSnapshotEntryID: lastEntryID, DrawerSnapshotBalance: int64(drawer),
	})
	if err != nil {
		return CloseShiftResult{}, fmt.Errorf("cash: close shift: %w", err)
	}
	// Laravel: CashRegisterService::close() (стр. 263) — журнал cash.shift_closed.
	if err := record(ctx, q, activity.Entry{
		ClubID: p.ClubID, Event: "cash.shift_closed", Description: "Закрыта кассовая смена", UserID: p.Actor.UserID,
		SubjectType: billingsvc.SubjectCashShift, SubjectID: row.ID,
		Details: map[string]any{"closing_balance": billingsvc.PHPFloat(closing)},
	}); err != nil {
		return CloseShiftResult{}, err
	}
	if err := shiftEvent(ctx, q, p.ClubID, row.ID, "closed"); err != nil {
		return CloseShiftResult{}, err
	}
	return CloseShiftResult{Shift: closed, Totals: totals}, audit(ctx, q, p.ClubID, p.Actor, "cash.shift_closed", "cash_shift", row.ID, nil,
		map[string]any{"closing_balance": int64(closing), "income": int64(totals.Income), "expense": int64(totals.Expense)})
}

// IssueZReportNonce — одноразовый nonce печати Z-отчёта смены (Str::random(40)); прежний перестаёт действовать.
// В БД — только его sha256 (Q-015).
// Laravel: CashRegisterController::zReport() — session()->put("z_report_nonce.{id}", Str::random(40)).
func (s *Service) IssueZReportNonce(ctx context.Context, q *store.Queries, clubID, shiftID int64) (string, error) {
	if _, err := q.GetCashShift(ctx, store.GetCashShiftParams{ClubID: clubID, ID: shiftID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("cash: shift: %w", err)
	}
	nonce, err := randomString(s.Rand, 40)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(nonce))
	if err := q.SetZReportNonce(ctx, store.SetZReportNonceParams{ClubID: clubID, ID: shiftID, NonceHash: sum[:]}); err != nil {
		return "", fmt.Errorf("cash: z nonce: %w", err)
	}
	return nonce, nil
}

// MarkZReportPrinted — отметка «Z-отчёт напечатан» (afterprint в браузере). Возвращает false, если nonce неверный или
// уже использован (419 в старом коде); nonce при этом гасится, поэтому транзакцию надо закоммитить и при false.
//
// Laravel: CashRegisterController::markZReportPrinted() (~стр. 178–190): смена не open → abort 422 (ErrShiftNotOpen, nonce
// не тратится); $expected = session()->pull(nonce) — одноразовый; hash_equals; флаг печати; журнал cash.z_report_printed.
// Отличие: флаг печати хранится у смены, а не в сессии сотрудника (закрыть может любой с правом) — CSQ-3.
func (s *Service) MarkZReportPrinted(ctx context.Context, q *store.Queries, clubID, shiftID int64, nonce string, actor Actor) (bool, error) {
	row, err := q.LockZReportNonce(ctx, store.LockZReportNonceParams{ClubID: clubID, ID: shiftID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("cash: z nonce: %w", err)
	}
	if row.Status != string(cash.StatusOpen) {
		return false, ErrShiftNotOpen
	}
	sum := sha256.Sum256([]byte(nonce))
	ok := len(row.ZReportNonceHash) == sha256.Size && nonce != "" && subtle.ConstantTimeCompare(row.ZReportNonceHash, sum[:]) == 1
	if !ok {
		if row.ZReportNonceHash != nil {
			if err := q.SetZReportNonce(ctx, store.SetZReportNonceParams{ClubID: clubID, ID: shiftID, NonceHash: nil}); err != nil {
				return false, fmt.Errorf("cash: z nonce: %w", err)
			}
		}
		return false, nil
	}
	now := s.now()
	if err := q.MarkZReportPrinted(ctx, store.MarkZReportPrintedParams{
		ClubID: clubID, ID: shiftID, Now: &now, PrintedBy: nonZero(actor.UserID),
	}); err != nil {
		return false, fmt.Errorf("cash: mark z printed: %w", err)
	}
	// Laravel: CashRegisterController::markZReportPrinted() (стр. 187) — журнал cash.z_report_printed без details.
	if err := record(ctx, q, activity.Entry{
		ClubID: clubID, Event: "cash.z_report_printed", Description: "Напечатан Z-отчёт смены #" + strconv.FormatInt(shiftID, 10),
		UserID: actor.UserID, SubjectType: billingsvc.SubjectCashShift, SubjectID: shiftID,
	}); err != nil {
		return false, err
	}
	return true, audit(ctx, q, clubID, actor, "cash.z_report_printed", "cash_shift", shiftID, nil, map[string]any{"shift_id": shiftID})
}

// CurrentShift — открытая смена клуба и её итоги без блокировки (страница кассы, API сотрудника); nil — нет.
func (s *Service) CurrentShift(ctx context.Context, q *store.Queries, clubID int64) (*store.CashShift, cash.Totals, error) {
	row, err := q.GetOpenCashShift(ctx, clubID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, cash.Totals{}, nil
	}
	if err != nil {
		return nil, cash.Totals{}, fmt.Errorf("cash: open shift: %w", err)
	}
	totals, err := shiftTotals(ctx, q, clubID, &row)
	return &row, totals, err
}

// ShiftTotals — итоги смены (CashRegisterService::totals()).
func (s *Service) ShiftTotals(ctx context.Context, q *store.Queries, clubID, shiftID int64) (cash.Totals, error) {
	row, err := q.GetCashShift(ctx, store.GetCashShiftParams{ClubID: clubID, ID: shiftID})
	if errors.Is(err, pgx.ErrNoRows) {
		return cash.Totals{}, ErrNotFound
	}
	if err != nil {
		return cash.Totals{}, fmt.Errorf("cash: shift: %w", err)
	}
	return shiftTotals(ctx, q, clubID, &row)
}

// drawerSnapshotMargin — запас нижней границы created_at при чтении проводок после снимка. created_at проводки —
// начало её транзакции (now()), а не момент записи: транзакция, начавшаяся до закрытия смены, может записать
// проводку кассы после снимка (например, открытие следующей смены, ждавшее блокировку). Граница нужна только для
// отсечения старых партиций; сутки — с большим запасом над временем жизни транзакции (lock_timeout 5 с).
const drawerSnapshotMargin = 24 * time.Hour

// drawerBalance — остаток счёта cash_drawer по журналу и id последней его проводки (0 — проводок нет).
//
// Решение CSQ-7 (docs/DECISIONS.md): остаток = снимок последней закрытой смены (cash_shifts.drawer_snapshot_*) +
// проводки с id больше снимка, а не SUM по всем проводкам кассы за годы. Нет снимка (первая смена, перенесённые
// смены) — полная сумма. Вызывать под блокировкой кассы (открытая смена FOR UPDATE или открытие без смены):
// проводки cash_drawer пишутся только под ней, поэтому порядок id совпадает с порядком коммитов.
func drawerBalance(ctx context.Context, q *store.Queries, clubID int64) (money.Tiyn, int64, error) {
	var (
		base, after int64
		from        time.Time // без снимка — с начала времён
	)
	snap, err := q.LatestCashDrawerSnapshot(ctx, clubID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return 0, 0, fmt.Errorf("cash: drawer snapshot: %w", err)
	default:
		base, after, from = snap.Balance, snap.EntryID, snap.SnapshotAt.Add(-drawerSnapshotMargin)
	}
	since, err := q.CashDrawerLedgerSince(ctx, store.CashDrawerLedgerSinceParams{
		ClubID: clubID, AfterEntryID: after, FromCreatedAt: from,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("cash: drawer balance: %w", err)
	}
	balance, err := money.Add(money.Tiyn(base), money.Tiyn(since.Balance))
	if err != nil {
		return 0, 0, fmt.Errorf("cash: drawer balance: %w", err)
	}
	return balance, max(after, since.LastEntryID), nil
}
