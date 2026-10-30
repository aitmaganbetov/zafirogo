package service

import (
	"context"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// TransferRequest — пересадка клиента на другой ПК (POST /api/guest/seat-transfer).
type TransferRequest struct {
	ClubID           int64
	UserID           int64
	FromComputerID   int64
	TargetComputerID int64
}

// TransferResult — сессия на новом ПК.
type TransferResult struct {
	ComputerID    int64
	SessionID     string
	SessionEndsAt time.Time
	SecondsMoved  int64
}

// TransferSeat переносит остаток оплаченной сессии на свободный ПК, где действует тариф сессии.
// Денег не двигается: части сессии привязаны к session_id и переезжают вместе с ним (GAP-036).
//
// Laravel: app/Services/SeatTransfers.php transfer() (стр. 83–120): оба ПК FOR UPDATE в порядке id →
// тот же/несуществующий ПК → своя оплаченная сессия на исходном → свободен ли целевой (не резерв, онлайн,
// без сессии и сотрудника, без чужой брони до конца сессии + интервал) и подходит ли зона
// (allowedZoneIds() — последняя покупка сессии; Допущение Этапа 1: Q-007/OQ-BR-04 — из session_purchases,
// а не из журнала действий) → исходный ПК: id/клиент/конец = NULL (session_guest не трогается) →
// целевой ПК: те же поля сессии.
func (s *Service) TransferSeat(ctx context.Context, req TransferRequest) (TransferResult, error) {
	var out TransferResult
	err := s.tx(ctx, req.ClubID, func(q *store.Queries) error {
		out = TransferResult{}
		now := s.clock.Now()
		club, err := loadClub(ctx, q, req.ClubID)
		if err != nil {
			return err
		}
		rows, err := q.LockComputers(ctx, store.LockComputersParams{ClubID: req.ClubID, Ids: []int64{req.FromComputerID, req.TargetComputerID}})
		if err != nil {
			return err
		}
		var from, target *computerRow
		for _, r := range rows {
			c := computerRow(r)
			if c.ID == req.FromComputerID && from == nil {
				from = &c
			} else if c.ID == req.TargetComputerID {
				target = &c
			}
		}
		if from == nil {
			return session.ErrTransferSameOrMissing
		}
		var targetPC *session.Computer
		if target != nil {
			t := target.session()
			targetPC = &t
		}
		var zones []int64
		blocked := false
		if from.CurrentSessionID != nil && target != nil && from.session().HasPaidSession(now) {
			zones, err = allowedZones(ctx, q, req.ClubID, *from.CurrentSessionID)
			if err != nil {
				return err
			}
			at, err := blocking(ctx, q, req.ClubID, target.ID, req.UserID, club.gapMinutes, now, *from.SessionEndsAt)
			if err != nil {
				return err
			}
			blocked = at != nil
		}
		plan, err := session.PlanTransfer(from.session(), targetPC, req.UserID, zones, blocked, now)
		if err != nil {
			return err
		}
		if err := q.MoveComputerSession(ctx, store.MoveComputerSessionParams{ClubID: req.ClubID, ID: from.ID, Now: &now}); err != nil {
			return err
		}
		sid, user, ends := plan.SessionID, plan.UserID, plan.SessionEndsAt
		if err := q.MoveComputerSession(ctx, store.MoveComputerSessionParams{
			ClubID: req.ClubID, ID: target.ID, SessionID: &sid, SessionUserID: &user, SessionEndsAt: &ends, Now: &now,
		}); err != nil {
			return err
		}
		out = TransferResult{ComputerID: target.ID, SessionID: sid, SessionEndsAt: ends, SecondsMoved: plan.SecondsMoved}
		return nil
	})
	return out, err
}

// allowedZones — SeatTransfers::allowedZoneIds() по частям сессии. Части до миграции 0006 (source NULL):
// kind tariff — как покупка тарифа (запуск пакета от неё не отличить), kind minutes без зоны — пропускается
// (зона неизвестна; см. open-questions/stage1-service.md SV-3).
func allowedZones(ctx context.Context, q *store.Queries, clubID int64, sessionID string) ([]int64, error) {
	rows, err := q.ListSessionZonePurchases(ctx, store.ListSessionZonePurchasesParams{ClubID: clubID, SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	purchases := make([]session.ZonePurchase, 0, len(rows))
	for _, r := range rows {
		src := session.PurchaseSource(r.Kind)
		if r.Source != nil {
			src = session.PurchaseSource(*r.Source)
		}
		zp := session.ZonePurchase{ID: r.ID, Source: src, TariffFound: r.TariffFound, TariffZoneIDs: r.TariffZoneIds}
		if src == session.SourceMinutes {
			if r.ZoneID == nil {
				continue
			}
			zp.ZoneID = *r.ZoneID
		}
		purchases = append(purchases, zp)
	}
	return session.AllowedZones(purchases), nil
}
