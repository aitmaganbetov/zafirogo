package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// EndSessionRequest — досрочное завершение сессии ПК (POST /api/club-computers/{pc}/session/end,
// «Завершить сессию» в панели).
type EndSessionRequest struct {
	ClubID     int64
	ComputerID int64
	SessionID  string
	Actor      Actor // UserID — кто завершает
	// IsStaff — сотрудник клуба (Gate::allows('register', ClubComputer::class)); проверяет вызывающий.
	IsStaff bool
}

// EndSessionResult — {refund_main, refund_bonus, kept_minutes} старого кода и записанное.
type EndSessionResult struct {
	RefundMain  money.Tiyn
	RefundBonus money.Tiyn
	KeptMinutes int64
	// CustomerID — клиент сессии (0 — гостевая или сессия старого агента без клиента: расчёта нет).
	CustomerID int64
	// PackageIDs — созданные time_packages.
	PackageIDs []int64
	// LedgerTransactionID — операция session_refund или package_kept (0 — нечего записывать).
	LedgerTransactionID int64
	// Settlement — полный план расчёта (по частям), nil без клиента.
	Settlement *session.Settlement
	// NewBalance, NewBonus — балансы клиента после возврата (BalanceKnown = false — возврата не было).
	NewBalance, NewBonus money.Tiyn
	BalanceKnown         bool
}

// EndSession — клиент-владелец или сотрудник завершают сессию досрочно; неиспользованные минуты
// поминутной оплаты возвращаются на балансы, остаток тарифа с лимитом использования сохраняется пакетом.
//
// Laravel: app/Services/ClubComputerSessions.php end() (стр. 203–229) и app/Services/SessionSettlement.php
// settle() (стр. 26–79), одна транзакция:
//
//	ПК FOR UPDATE → session_id не совпадает → «На компьютере нет такой активной сессии.» →
//	не сотрудник и не владелец → 403 → обнулить сессию ПК →
//	клиент есть: части по id desc → Settle → пакеты → удалить все части →
//	возврат > 0: баланс FOR UPDATE (нет строки — деньги не начисляются, OQ-BR-12) → траты −= возврат с основного.
//
// Ledger: одна операция session_refund (деньги назад: revenue_time → member_main, bonus_fund → member_bonus,
// и минуты сохранённых пакетов) или package_kept (только минуты); ключ "session_end:<session_id>".
// Гостевая сессия (клиента нет) денег не возвращает (GAP-031).
func (s *Service) EndSession(ctx context.Context, req EndSessionRequest) (EndSessionResult, error) {
	var (
		out      EndSessionResult
		currency string
	)
	err := s.tx(ctx, req.ClubID, func(q *store.Queries) error {
		out = EndSessionResult{}
		now := s.clock.Now()
		pc, err := lockComputer(ctx, q, req.ClubID, req.ComputerID)
		if err != nil {
			return err
		}
		if pc.CurrentSessionID == nil || *pc.CurrentSessionID != req.SessionID {
			return ErrNoSuchSession
		}
		if !req.IsStaff && (pc.CurrentSessionUserID == nil || *pc.CurrentSessionUserID != req.Actor.UserID) {
			return ErrForbidden
		}
		endsAt := pc.SessionEndsAt
		customer := pc.CurrentSessionUserID
		if err := q.ClearComputerSession(ctx, store.ClearComputerSessionParams{ClubID: req.ClubID, ID: pc.ID, Now: &now}); err != nil {
			return err
		}
		if customer != nil {
			out.CustomerID = *customer
			if err := s.settle(ctx, q, req, *customer, endsAt, now, pc.displayName(), &out); err != nil {
				return err
			}
		}
		// Laravel: ClubComputerSessions::end() (стр. 222–225) — журнал computer.session_ended от того, кто завершил.
		planned := "—"
		if endsAt != nil {
			planned = ISO8601(*endsAt)
		}
		if err := RecordActivity(ctx, q, activity.Entry{
			ClubID: req.ClubID, Event: "computer.session_ended",
			Description: "Сессия на ПК " + pc.displayName() + " завершена досрочно",
			UserID:      req.Actor.UserID, SubjectType: SubjectComputer, SubjectID: pc.ID,
			Details: map[string]any{"session_id": req.SessionID, "planned_end": planned},
		}); err != nil {
			return err
		}
		if err := AddEvent(ctx, q, req.ClubID, "computer.state", computerEvent(pc.ID, "", 0, false, nil)); err != nil {
			return err
		}
		if out.BalanceKnown {
			if err := AddEvent(ctx, q, req.ClubID, "balance.changed", BalanceEvent(out.CustomerID, out.NewBalance, out.NewBonus)); err != nil {
				return err
			}
			club, err := loadClub(ctx, q, req.ClubID)
			if err != nil {
				return err
			}
			currency = club.currency
		}
		return nil
	})
	if err == nil && s.notifier != nil {
		reason := agentv1.SessionEndReason_SESSION_END_REASON_GUEST
		if req.IsStaff {
			reason = agentv1.SessionEndReason_SESSION_END_REASON_STAFF
		}
		NotifySessionEnded(ctx, s.notifier, req.ClubID, req.ComputerID, &agentv1.EndSession{
			SessionId: req.SessionID, Reason: reason, RefundedMainTiyn: int64(out.RefundMain),
			RefundedBonusTiyn: int64(out.RefundBonus), KeptMinutes: keptMinutes32(out.KeptMinutes),
		}, req.Actor.UserID)
		if out.BalanceKnown {
			NotifyBalance(ctx, s.notifier, req.ClubID, out.CustomerID, out.NewBalance, out.NewBonus, currency)
		}
	}
	return out, err
}

// keptMinutes32 — минуты пакета для протокола агента (int32, с насыщением).
func keptMinutes32(m int64) int32 {
	if m > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(m) // #nosec G115 -- ограничено выше, m >= 0
}

// settle — SessionSettlement::settle() (стр. 26–79) поверх БД.
func (s *Service) settle(ctx context.Context, q *store.Queries, req EndSessionRequest, customer int64, endsAt *time.Time, now time.Time,
	computerName string, out *EndSessionResult) error {
	rows, err := q.ListSessionPurchases(ctx, store.ListSessionPurchasesParams{ClubID: req.ClubID, SessionID: req.SessionID})
	if err != nil {
		return err
	}
	parts := make([]session.Purchase, len(rows))
	for i, r := range rows {
		parts[i] = session.Purchase{
			ID: r.ID, UserID: r.UserID, Kind: session.Kind(r.Kind), TariffID: r.TariffID, Name: r.Name,
			Minutes: r.Minutes, MainCents: money.Tiyn(r.MainCents), BonusCents: money.Tiyn(r.BonusCents), ExpiresAt: r.ExpiresAt,
		}
	}
	st, err := session.Settle(parts, customer, endsAt, now)
	if err != nil {
		return err
	}
	out.Settlement = &st
	out.KeptMinutes = st.KeptMinutes

	// Пакеты: TimePackage::create(['user_id', 'tariff_id', 'name', 'minutes_left', 'expires_at']).
	var minutes []ledger.MinuteEntry
	for _, p := range st.Packages {
		id, err := q.InsertTimePackage(ctx, store.InsertTimePackageParams{
			ClubID: req.ClubID, UserID: p.UserID, TariffID: p.TariffID, Name: p.Name,
			MinutesLeft: p.MinutesLeft, ExpiresAt: p.ExpiresAt, Now: &now,
		})
		if err != nil {
			return err
		}
		out.PackageIDs = append(out.PackageIDs, id)
		var tariffID int64
		if p.TariffID != nil {
			tariffID = *p.TariffID
		}
		minutes = append(minutes, ledger.MinuteEntry{UserID: p.UserID, TimePackageID: id, TariffID: tariffID, Minutes: p.MinutesLeft})
	}
	// SessionPurchase::where('session_id', …)->delete() — все части, включая чужие и использованные.
	if err := q.DeleteSessionPurchases(ctx, store.DeleteSessionPurchasesParams{ClubID: req.ClubID, SessionID: req.SessionID}); err != nil {
		return err
	}

	var entries []ledger.Entry
	if st.RefundMain+st.RefundBonus > 0 {
		// $balance = MemberBalance::where('user_id')->lockForUpdate()->first(); null — деньги не начисляются.
		_, err := ledger.LockBalances(ctx, q, req.ClubID, customer)
		switch {
		case err == nil:
			out.RefundMain, out.RefundBonus = st.RefundMain, st.RefundBonus
			if st.RefundMain > 0 {
				entries = append(entries,
					ledger.Entry{Account: ledger.Main(customer), Amount: st.RefundMain},
					ledger.Entry{Account: ledger.ClubAccount(ledger.AccountRevenueTime), Amount: -st.RefundMain})
			}
			if st.RefundBonus > 0 {
				entries = append(entries,
					ledger.Entry{Account: ledger.Bonus(customer), Amount: st.RefundBonus},
					ledger.Entry{Account: ledger.ClubAccount(ledger.AccountBonusFund), Amount: -st.RefundBonus})
			}
		case !errors.Is(err, ledger.ErrBalanceNotFound):
			return err
		}
	}
	if len(entries) > 0 || len(minutes) > 0 {
		kind := ledger.KindSessionRefund
		if len(entries) == 0 {
			kind = ledger.KindPackageKept
		}
		meta, err := json.Marshal(map[string]any{
			"computer_id": req.ComputerID, "seconds_left": st.SecondsLeft,
			"refund_main": int64(out.RefundMain), "refund_bonus": int64(out.RefundBonus), "kept_minutes": st.KeptMinutes,
		})
		if err != nil {
			return err
		}
		res, err := ledger.Post(ctx, q, ledger.Operation{
			ClubID: req.ClubID, Kind: kind, IdempotencyKey: ledgerKey("session_end", req.SessionID),
			Entries: entries, Minutes: minutes, Meta: meta, ActorUserID: req.Actor.UserID,
			Refs: ledger.Refs{SessionID: req.SessionID},
		})
		if err != nil {
			return err
		}
		out.LedgerTransactionID = res.TransactionID
		after := map[string]any{
			"session_id": req.SessionID, "refund_main": int64(out.RefundMain), "refund_bonus": int64(out.RefundBonus),
			"kept_minutes": st.KeptMinutes, "packages": slices.Clone(out.PackageIDs),
		}
		if b, ok := res.BalanceAfter(ledger.Main(customer)); ok {
			after["new_balance"] = int64(b)
			out.NewBalance, out.BalanceKnown = b, true
		}
		if b, ok := res.BalanceAfter(ledger.Bonus(customer)); ok {
			after["new_bonus_balance"] = int64(b)
			out.NewBonus, out.BalanceKnown = b, true
		}
		if out.BalanceKnown {
			// Второй баланс операция не трогала — берём текущий из member_balances.
			mb, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: req.ClubID, UserID: customer})
			if err != nil {
				return err
			}
			if _, ok := res.BalanceAfter(ledger.Main(customer)); !ok {
				out.NewBalance = money.Tiyn(mb.Balance)
			}
			if _, ok := res.BalanceAfter(ledger.Bonus(customer)); !ok {
				out.NewBonus = money.Tiyn(mb.BonusBalance)
			}
		}
		if err := auditMoney(ctx, q, req.ClubID, req.Actor, "money."+string(kind), customer, res.TransactionID, after); err != nil {
			return err
		}
	}
	// Laravel: if (refund_main + refund_bonus > 0) subtractSpending($userId, refund_main) — даже без строки баланса.
	if st.RefundMain+st.RefundBonus > 0 {
		if err := subtractSpending(ctx, q, req.ClubID, customer, st.LoyaltySpendingDecrease, now); err != nil {
			return err
		}
	}
	// Laravel: SessionSettlement::settle() (стр. 63–76) — журнал time.refunded и time.package_kept от того, кто
	// завершил, предмет — клиент. Суммы — расчётные (refund_main/refund_bonus), даже если строки баланса не было.
	if st.RefundMain+st.RefundBonus > 0 {
		if err := RecordActivity(ctx, q, activity.Entry{
			ClubID: req.ClubID, Event: "time.refunded",
			Description: "Возврат за неиспользованное время на ПК " + computerName,
			UserID:      req.Actor.UserID, SubjectType: SubjectUser, SubjectID: customer,
			Details: map[string]any{
				"main_balance_amount": st.RefundMain.Tenge(), "bonus_balance_amount": st.RefundBonus.Tenge(),
				"session_id": req.SessionID,
			},
		}); err != nil {
			return err
		}
	}
	if st.KeptMinutes > 0 {
		if err := RecordActivity(ctx, q, activity.Entry{
			ClubID: req.ClubID, Event: "time.package_kept",
			Description: "Сохранено " + itoa(st.KeptMinutes) + " мин. абонемента",
			UserID:      req.Actor.UserID, SubjectType: SubjectUser, SubjectID: customer,
			Details: map[string]any{"minutes": st.KeptMinutes, "session_id": req.SessionID},
		}); err != nil {
			return err
		}
	}
	return nil
}
